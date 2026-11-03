package issues

import (
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

// Kueue findings: a run Kueue will not admit as configured, a run Kueue
// stopped, and a queue that admits nothing because its configuration is
// broken. Which Workload states ask for action is RAD-811's classification
// (schedulinginsight.ClassifyKueueWorkload, Attention); this source decides
// only what the Workload alone can't: whether an inactive queue is stopped on
// purpose or broken, read from the queue objects themselves.

const (
	kueueGroup              = "kueue.x-k8s.io"
	kueueWorkloadKind       = "Workload"
	kueueLocalQueueKind     = "LocalQueue"
	kueueClusterQueueKind   = "ClusterQueue"
	kueueAdmissionCheckKind = "AdmissionCheck"
	provisioningRequestKind = "ProvisioningRequest"
)

const (
	ReasonKueueExceedsQuota                 = "KueueExceedsQuota"
	ReasonKueueNoMatchingFlavor             = "KueueNoMatchingFlavor"
	ReasonKueueNamespaceNotEligible         = "KueueNamespaceNotEligible"
	ReasonKueueLocalQueueMissing            = "KueueLocalQueueMissing"
	ReasonKueueClusterQueueMissing          = "KueueClusterQueueMissing"
	ReasonKueueClusterQueueInactive         = "KueueClusterQueueInactive"
	ReasonKueueInvalidWorkload              = "KueueInvalidWorkload"
	ReasonKueueAdmissionCheckRejected       = "KueueAdmissionCheckRejected"
	ReasonKueueRequeuingLimitExceeded       = "KueueRequeuingLimitExceeded"
	ReasonKueueMaximumExecutionTimeExceeded = "KueueMaximumExecutionTimeExceeded"
	ReasonKueueDeactivatedByKueue           = "KueueDeactivatedByKueue"
)

const (
	factKueueAdmission           = "kueue_admission"
	factKueueQueue               = "kueue_queue"
	factKueueAffectedRuns        = "kueue_affected_runs"
	factKueueAdmissionCheck      = "kueue_admission_check"
	factKueueProvisioningRequest = "kueue_provisioning_request"
)

// kueueGroupGVR carries the group newConditionIssue records on a row.
var kueueGroupGVR = schema.GroupVersionResource{Group: kueueGroup}

// kueueConfigurationGrace keeps a configuration finding out of Issues while
// the object is new: a GitOps apply can create a Job before the queue or
// quota it names, and Kueue re-evaluates within seconds once they exist.
const kueueConfigurationGrace = time.Minute

// maxKueueLinkedObjects bounds the ProvisioningRequests and AdmissionChecks
// linked on one row.
const maxKueueLinkedObjects = 3

// kueueBrokenClusterQueueReasons are the ClusterQueue Active=False reasons
// Kueue (v0.18-v0.20, apis/kueue/v1beta2/clusterqueue_types.go) sets for a
// configuration that can't admit anything. Stopped and Terminating are on
// purpose; Unknown and any newer reason are not known to be actionable.
var kueueBrokenClusterQueueReasons = map[string]bool{
	"FlavorNotFound":                           true,
	"AdmissionCheckNotFound":                   true,
	"AdmissionCheckInactive":                   true,
	"MultipleMultiKueueAdmissionChecks":        true,
	"MultiKueueAdmissionCheckAppliedPerFlavor": true,
	"MultiKueueWithProvisioningRequest":        true,
	"TopologyNotFound":                         true,
	"CohortCycleDetected":                      true,
}

// LocalQueue Active=False reasons (Kueue's localqueue_controller.go).
const (
	kueueLocalQueueClusterQueueMissing  = "ClusterQueueDoesNotExist"
	kueueLocalQueueClusterQueueInactive = "ClusterQueueIsInactive"
)

// kueueInventory is one Kueue (or ProvisioningRequest) kind as Radar's cache
// holds it for the requested scope. OK means the read is complete for that
// scope. Gap says why it isn't, when that leaves findings unchecked; it is
// empty when the kind isn't served.
type kueueInventory struct {
	Items []*unstructured.Unstructured
	OK    bool
	Gap   issuesapi.UncheckedReason
}

type kueueIssueProvider interface {
	// kueueInventory reads group/kind for namespaces (nil: every namespace
	// the caller's compose covers; ignored for cluster-scoped kinds).
	kueueInventory(group, kind string, namespaces []string) kueueInventory
	// kueueRoots resolves Workloads to the top of their controller chain for
	// one compose.
	kueueRoots() kueueRootResolver
}

type kueueRootResolver interface {
	// chain is the Workload's controller chain above it, nearest first and
	// the run's root last, read from the same incarnation (UID) of the
	// Workload; empty when there is none or it can't be followed.
	chain(workload *unstructured.Unstructured) []Ref
	// valid reports whether every root answered came from current caches.
	valid() bool
}

// kueueRun is a Workload that a queue finding affects.
type kueueRun struct {
	workload *unstructured.Unstructured
	state    resourcecontext.KueueAdmissionState
}

type kueueDetector struct {
	p         kueueIssueProvider
	f         Filters
	unchecked *uncheckedSet
	now       time.Time
	roots     kueueRootResolver
	inventory map[string]kueueInventory
	// requestsByOwner indexes retained ProvisioningRequests by controller UID.
	requestsByOwner map[types.UID][]*unstructured.Unstructured
	requestsRead    bool
	requestsOK      bool
}

func detectKueueIssues(p Provider, f Filters, unchecked *uncheckedSet) []Issue {
	kp, ok := p.(kueueIssueProvider)
	if !ok {
		return nil
	}
	d := &kueueDetector{p: kp, f: f, unchecked: unchecked, now: time.Now(), inventory: map[string]kueueInventory{}}
	// Each inventory answers on its own: without Workloads the queues are
	// still read, and without LocalQueues the ClusterQueues are.
	workloads, _ := d.read(kueueGroup, kueueWorkloadKind)

	variantParents := map[types.UID]bool{}
	for _, w := range workloads {
		if owner := metav1.GetControllerOf(w); owner != nil && isKueueWorkloadOwner(*owner) {
			variantParents[owner.UID] = true
		}
	}

	var out []Issue
	// Runs that report an inactive or missing queue, by namespace/LocalQueue.
	dependent := map[string][]kueueRun{}
	for _, w := range workloads {
		if w == nil || w.GetDeletionTimestamp() != nil || variantParents[w.GetUID()] {
			continue
		}
		if owner := metav1.GetControllerOf(w); owner != nil && isKueueWorkloadOwner(*owner) {
			// A concurrent-admission variant is expected to lose; its own
			// state says nothing about whether the run is blocked.
			continue
		}
		classified, ok := schedulinginsight.ClassifyKueueWorkload(w)
		if !ok || classified.Admission.Stale {
			continue
		}
		state := classified.Admission
		switch {
		case isKueueQueueDependency(state.State):
			queue, _, _ := unstructured.NestedString(w.Object, "spec", "queueName")
			if queue != "" {
				key := w.GetNamespace() + "/" + queue
				dependent[key] = append(dependent[key], kueueRun{workload: w, state: state})
			}
		case state.Attention == resourcecontext.KueueAttentionActionNeeded:
			if issue, ok := d.runIssue(w, state); ok {
				out = append(out, issue)
			}
		}
	}

	queueIssues, covered := d.queueIssues(dependent)
	out = append(out, queueIssues...)
	// A run that reports a missing ClusterQueue no LocalQueue row explained
	// (the LocalQueue couldn't be read, or Kueue hasn't marked it yet) still
	// gets its own row: the Workload's report is enough.
	for _, runs := range dependent {
		for _, run := range runs {
			if run.state.State == resourcecontext.KueueAdmissionClusterQueueMissing && !covered[run.workload.GetUID()] {
				if issue, ok := d.runIssue(run.workload, run.state); ok {
					out = append(out, issue)
				}
			}
		}
	}

	if d.roots != nil && !d.roots.valid() {
		// The caches changed under the ownership reads: claim no roots and
		// keep only the Workloads among the affected resources.
		for i := range out {
			if out[i].Owner.Kind != "" {
				out[i].Owner = Ref{}
				enrichIdentity(&out[i])
			}
			out[i].AffectedRefs = kueueWorkloadRefsOnly(out[i].AffectedRefs)
			out[i].DiagnosticContext = withoutFact(out[i].DiagnosticContext, factKueueAffectedRuns)
			if len(out[i].AffectedRefs) > 0 && out[i].Kind != kueueWorkloadKind {
				out[i].DiagnosticContext = withFact(out[i].DiagnosticContext, affectedRefsFact(factKueueAffectedRuns, kueueAffectedRunsMessage(out[i].Kind), out[i].AffectedRefs))
			}
		}
	}
	return out
}

func kueueWorkloadRefsOnly(refs []Ref) []Ref {
	var out []Ref
	for _, ref := range refs {
		if ref.Group == kueueGroup && ref.Kind == kueueWorkloadKind {
			out = append(out, ref)
		}
	}
	return out
}

func withFact(ctx *issuesapi.DiagnosticContext, fact issuesapi.DiagnosticFact) *issuesapi.DiagnosticContext {
	if ctx == nil {
		return &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleCandidate, Facts: []issuesapi.DiagnosticFact{fact}}
	}
	out := *ctx
	out.Facts = append(append([]issuesapi.DiagnosticFact(nil), ctx.Facts...), fact)
	return &out
}

func kueueAffectedRunsMessage(kind string) string {
	if kind == kueueClusterQueueKind {
		return "Runs Kueue reports blocked by this ClusterQueue, through the LocalQueues that point to it, with their Workloads."
	}
	return "Runs submitted to this LocalQueue that Kueue reports blocked by it, with their Workloads."
}

func isKueueQueueDependency(code resourcecontext.KueueAdmissionCode) bool {
	switch code {
	case resourcecontext.KueueAdmissionLocalQueueInactive, resourcecontext.KueueAdmissionClusterQueueInactive, resourcecontext.KueueAdmissionClusterQueueMissing:
		return true
	}
	return false
}

func isKueueWorkloadOwner(owner metav1.OwnerReference) bool {
	return resourceid.GroupFromAPIVersion(owner.APIVersion) == kueueGroup && owner.Kind == kueueWorkloadKind
}

// read returns a kind's inventory for the compose's scope, recording a gap
// when it is incomplete. ok is false when the kind can't be answered.
func (d *kueueDetector) read(group, kind string) ([]*unstructured.Unstructured, bool) {
	inv := d.readEvidence(group, kind)
	if !inv.OK && inv.Gap != "" {
		d.unchecked.add(issuesapi.UncheckedSourceKueue, inv.Gap, issuesapi.UncheckedKind{Group: group, Kind: kind})
	}
	return inv.Items, inv.OK
}

// readEvidence reads a kind that only adds evidence to rows found from
// other kinds, so a gap in it hides no finding and records none.
func (d *kueueDetector) readEvidence(group, kind string) kueueInventory {
	key := group + "/" + kind
	inv, seen := d.inventory[key]
	if !seen {
		inv = d.p.kueueInventory(group, kind, d.f.Namespaces)
		d.inventory[key] = inv
	}
	return inv
}

// chain is the Workload's controller chain above it, nearest first.
func (d *kueueDetector) chain(w *unstructured.Unstructured) []Ref {
	if d.roots == nil {
		d.roots = d.p.kueueRoots()
	}
	if d.roots == nil {
		return nil
	}
	return d.roots.chain(w)
}

// runRefs are the resources a run consists of: the Workload and every
// controller above it, so the Workload's page, an intermediate Job's page
// and the root's page all find a finding about the run.
func (d *kueueDetector) runRefs(w *unstructured.Unstructured) []Ref {
	return append([]Ref{kueueWorkloadRef(w)}, d.chain(w)...)
}

func kueueWorkloadRef(w *unstructured.Unstructured) Ref {
	return Ref{Group: kueueGroup, Kind: kueueWorkloadKind, Namespace: w.GetNamespace(), Name: w.GetName()}
}

type kueueRunFinding struct {
	reason string
	cause  string
	action string
	// fact is the classified fact the finding rests on.
	fact *resourcecontext.KueueAdmissionFact
	// configuration findings wait out kueueConfigurationGrace.
	configuration bool
	// checks links the AdmissionChecks and ProvisioningRequests involved.
	checks bool
}

// kueueRunFindingFor maps an action_needed state to its finding. The
// deactivation cause is the reason RAD-811 kept on the fact.
func kueueRunFindingFor(state resourcecontext.KueueAdmissionState) (kueueRunFinding, bool) {
	switch state.State {
	case resourcecontext.KueueAdmissionExceedsQuota:
		return kueueRunFinding{
			reason: ReasonKueueExceedsQuota, fact: state.Blocker, configuration: true,
			cause:  "Kueue reports that this run requests more than its ClusterQueue, or its cohort, could ever provide, so it will not be admitted as configured.",
			action: "Reduce the run's resource requests or parallelism, or raise the ClusterQueue's nominal quota or borrowing limit for the resources the message names.",
		}, true
	case resourcecontext.KueueAdmissionNoMatchingFlavor:
		return kueueRunFinding{
			reason: ReasonKueueNoMatchingFlavor, fact: state.Blocker, configuration: true,
			cause:  "Kueue reports that no ResourceFlavor in the ClusterQueue can hold this run: taints, node affinity or topology rule every flavor out, or the queue offers no quota for a requested resource.",
			action: "Compare the run's node selectors, tolerations and requested resources with the ClusterQueue's flavors, then change the run or add a flavor and quota that fit it.",
		}, true
	case resourcecontext.KueueAdmissionNamespaceNotEligible:
		return kueueRunFinding{
			reason: ReasonKueueNamespaceNotEligible, fact: state.Blocker, configuration: true,
			cause:  "Kueue reports that the ClusterQueue's namespaceSelector does not match this namespace, so it admits no run submitted here.",
			action: "Submit through a LocalQueue whose ClusterQueue admits this namespace, or change the ClusterQueue's namespaceSelector.",
		}, true
	case resourcecontext.KueueAdmissionLocalQueueMissing:
		return kueueRunFinding{
			reason: ReasonKueueLocalQueueMissing, fact: state.Blocker, configuration: true,
			cause:  "Kueue reports that the LocalQueue this run names (its kueue.x-k8s.io/queue-name) does not exist in this namespace, or is being deleted.",
			action: "Create the LocalQueue, or point the run's queue-name label at an existing LocalQueue.",
		}, true
	case resourcecontext.KueueAdmissionClusterQueueMissing:
		return kueueRunFinding{
			reason: ReasonKueueClusterQueueMissing, fact: state.Blocker, configuration: true,
			cause:  "Kueue reports that the ClusterQueue this run's LocalQueue points to does not exist, or is being deleted.",
			action: "Create the ClusterQueue, or point the LocalQueue's spec.clusterQueue at an existing one.",
		}, true
	case resourcecontext.KueueAdmissionInvalidWorkload:
		return kueueRunFinding{
			reason: ReasonKueueInvalidWorkload, fact: state.Blocker, configuration: true,
			cause:  "Kueue reports a problem with this run's own configuration (its resources, a LimitRange, or resource claims), so it can't be admitted.",
			action: "Fix the run's spec as the message describes; Kueue evaluates it again when the run changes.",
		}, true
	case resourcecontext.KueueAdmissionCheckRejected:
		return kueueAdmissionCheckFinding(state.Blocker), true
	case resourcecontext.KueueAdmissionDeactivationPending, resourcecontext.KueueAdmissionDeactivatedByKueue:
		fact := state.Inactive
		if fact == nil {
			return kueueRunFinding{}, false
		}
		cause := strings.TrimPrefix(fact.Reason, "DeactivatedDueTo")
		switch cause {
		case "AdmissionCheck":
			return kueueAdmissionCheckFinding(fact), true
		case "RequeuingLimitExceeded":
			return kueueRunFinding{
				reason: ReasonKueueRequeuingLimitExceeded, fact: fact,
				cause:  "Kueue deactivated this run (spec.active=false) after evicting and requeuing it the most times its configuration allows, usually because its Pods did not become ready in time.",
				action: "Find why the run's Pods weren't ready (image pulls, scheduling, crashes) and fix it, then set the Workload's spec.active to true or resubmit the run.",
			}, true
		case "MaximumExecutionTimeExceeded":
			return kueueRunFinding{
				reason: ReasonKueueMaximumExecutionTimeExceeded, fact: fact,
				cause:  "Kueue deactivated this run (spec.active=false) because it ran past its maximum execution time.",
				action: "If the run needs longer, raise its maximum execution time (kueue.x-k8s.io/max-exec-time-seconds) and resubmit; otherwise find why it ran long.",
			}, true
		}
		return kueueRunFinding{
			reason: ReasonKueueDeactivatedByKueue, fact: fact,
			cause:  "Kueue deactivated this run (spec.active=false) for the reason in the message.",
			action: "Fix the cause in the message, then set the Workload's spec.active to true or resubmit the run.",
		}, true
	}
	return kueueRunFinding{}, false
}

// kueueAdmissionCheckFinding keeps one reason for a rejected check and the
// deactivation Kueue follows it with, so the row survives that transition.
func kueueAdmissionCheckFinding(fact *resourcecontext.KueueAdmissionFact) kueueRunFinding {
	return kueueRunFinding{
		reason: ReasonKueueAdmissionCheckRejected, fact: fact, checks: true,
		cause:  "An AdmissionCheck rejected this run. Kueue does not retry a rejection: it deactivates the run (spec.active=false), so it stays unadmitted until someone acts.",
		action: "Read the check's message, and for a provisioning check the ProvisioningRequest's outcome, fix the cause, then set the Workload's spec.active to true or resubmit the run.",
	}
}

func (d *kueueDetector) runIssue(w *unstructured.Unstructured, state resourcecontext.KueueAdmissionState) (Issue, bool) {
	finding, ok := kueueRunFindingFor(state)
	if !ok || finding.fact == nil {
		return Issue{}, false
	}
	created := w.GetCreationTimestamp().Time
	if finding.configuration && !created.IsZero() && d.now.Sub(created) < kueueConfigurationGrace {
		return Issue{}, false
	}
	transitionAt, transitionKnown := kueueFactTransition(w, finding.fact)
	message := finding.fact.Message
	issue := newConditionIssue(kueueGroupGVR, kueueWorkloadKind, w.GetNamespace(), w.GetName(), SeverityWarning,
		finding.reason, message, transitionAt, transitionKnown, finding.reason+"\x00"+w.GetName(), created)
	issue.RawMessage = message
	issue.Cause, issue.Action = finding.cause, finding.action
	if chain := d.chain(w); len(chain) > 0 {
		issue.Owner = chain[len(chain)-1]
		// A root above the run's own Job (a CronJob) is the row's subject;
		// the Job's page still finds the row.
		issue.AffectedRefs = chain[:len(chain)-1]
	}
	enrichIdentity(&issue)

	facts := []issuesapi.DiagnosticFact{kueueAdmissionFact(w, state)}
	if finding.checks {
		facts = append(facts, d.admissionCheckFacts(w)...)
		facts = append(facts, d.provisioningFacts(w)...)
	}
	issue.DiagnosticContext = &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleCandidate, Facts: facts}
	return issue, true
}

// kueueFactTransition is when the fact's evidence last changed, when that
// dates this finding: an Evicted or DeactivationTarget condition, or a
// check's state. A QuotaReserved condition can stay False while its reason
// changes, so its transition doesn't date the current reason.
func kueueFactTransition(w *unstructured.Unstructured, fact *resourcecontext.KueueAdmissionFact) (time.Time, bool) {
	switch fact.Source {
	case resourcecontext.KueueFactSourceCondition:
		if fact.Name != "Evicted" && fact.Name != "DeactivationTarget" {
			return time.Time{}, false
		}
		if cm, ok := kueueConditionMap(w, fact.Name); ok {
			return conditionTransitionTime(cm)
		}
	case resourcecontext.KueueFactSourceAdmissionCheck:
		checks, _, _ := unstructured.NestedSlice(w.Object, "status", "admissionChecks")
		for _, raw := range checks {
			if cm, ok := raw.(map[string]any); ok && cm["name"] == fact.Name {
				return conditionTransitionTime(cm)
			}
		}
	}
	return time.Time{}, false
}

func kueueConditionMap(u *unstructured.Unstructured, conditionType string) (map[string]any, bool) {
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, raw := range conditions {
		if cm, ok := raw.(map[string]any); ok && cm["type"] == conditionType {
			return cm, true
		}
	}
	return nil, false
}

// kueueActive reads a queue's Active condition, current only when it was
// written for the object's generation (or carries none).
func kueueActive(u *unstructured.Unstructured) (status, reason, message string, current bool) {
	cm, ok := kueueConditionMap(u, "Active")
	if !ok {
		return "", "", "", false
	}
	status, _ = cm["status"].(string)
	reason, _ = cm["reason"].(string)
	message, _ = cm["message"].(string)
	observed, _ := cm["observedGeneration"].(int64)
	if f, isFloat := cm["observedGeneration"].(float64); isFloat {
		observed = int64(f)
	}
	current = observed == 0 || u.GetGeneration() == 0 || observed >= u.GetGeneration()
	return status, reason, message, current
}

func kueueStopped(u *unstructured.Unstructured) bool {
	policy, _, _ := unstructured.NestedString(u.Object, "spec", "stopPolicy")
	return policy != "" && policy != "None"
}

func kueueAdmissionFact(w *unstructured.Unstructured, state resourcecontext.KueueAdmissionState) issuesapi.DiagnosticFact {
	parts := []string{fmt.Sprintf("Kueue admission state %s.", state.State)}
	for _, axis := range []struct {
		name string
		fact *resourcecontext.KueueAdmissionFact
	}{{"Inactive", state.Inactive}, {"Blocker", state.Blocker}, {"Disruption", state.Disruption}} {
		if axis.fact == nil {
			continue
		}
		parts = append(parts, axis.name+": "+kueueFactText(axis.fact)+".")
	}
	return issuesapi.DiagnosticFact{
		Type:       factKueueAdmission,
		Message:    strings.Join(parts, " "),
		Confidence: issuesapi.ConfidenceHigh,
		Refs:       []Ref{kueueWorkloadRef(w)},
	}
}

func kueueFactText(fact *resourcecontext.KueueAdmissionFact) string {
	text := string(fact.Code)
	switch fact.Source {
	case resourcecontext.KueueFactSourceCondition:
		text += fmt.Sprintf(" (condition %s, reason %s)", fact.Name, fact.Reason)
	case resourcecontext.KueueFactSourceAdmissionCheck:
		text += fmt.Sprintf(" (AdmissionCheck %s, state %s)", fact.Name, fact.Reason)
	case resourcecontext.KueueFactSourceSpec:
		text += fmt.Sprintf(" (spec.%s)", fact.Name)
	}
	if fact.Stale {
		text += ", reported for an earlier generation"
	}
	return text
}

// admissionCheckFacts link the AdmissionChecks the Workload lists, with each
// check's own Active condition. Freshness of a Workload's check state is not
// established: Kueue records no generation for it.
func (d *kueueDetector) admissionCheckFacts(w *unstructured.Unstructured) []issuesapi.DiagnosticFact {
	raw, _, _ := unstructured.NestedSlice(w.Object, "status", "admissionChecks")
	var names []string
	for _, item := range raw {
		if entry, ok := item.(map[string]any); ok {
			if name, _ := entry["name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	if len(names) > maxKueueLinkedObjects {
		names = names[:maxKueueLinkedObjects]
	}
	inv := d.readEvidence(kueueGroup, kueueAdmissionCheckKind)
	ok := inv.OK
	byName := map[string]*unstructured.Unstructured{}
	for _, check := range inv.Items {
		byName[check.GetName()] = check
	}
	facts := make([]issuesapi.DiagnosticFact, 0, len(names))
	for _, name := range names {
		ref := Ref{Group: kueueGroup, Kind: kueueAdmissionCheckKind, Name: name}
		message := fmt.Sprintf("AdmissionCheck %s: Radar couldn't read it.", name)
		if ok {
			message = kueueObjectActiveText(kueueAdmissionCheckKind, name, byName[name])
		}
		facts = append(facts, issuesapi.DiagnosticFact{Type: factKueueAdmissionCheck, Message: message, Confidence: issuesapi.ConfidenceHigh, Refs: []Ref{ref}})
	}
	return facts
}

func kueueObjectActiveText(kind, name string, u *unstructured.Unstructured) string {
	if u == nil {
		return fmt.Sprintf("%s %s does not exist.", kind, name)
	}
	label := kind + " " + name
	if controller, _, _ := unstructured.NestedString(u.Object, "spec", "controllerName"); controller != "" {
		label += " (controller " + controller + ")"
	}
	status, reason, message, current := kueueActive(u)
	if status == "" {
		return label + " reports no Active condition."
	}
	text := fmt.Sprintf("%s: Active=%s", label, status)
	if reason != "" {
		text += " (" + reason + ")"
	}
	if message != "" {
		text += ": " + message
	}
	if !current {
		text += " (reported for an earlier generation)"
	}
	return text
}

// provisioningFacts link the retained ProvisioningRequests this Workload's
// incarnation controls, newest first, with their native outcome.
func (d *kueueDetector) provisioningFacts(w *unstructured.Unstructured) []issuesapi.DiagnosticFact {
	if !d.requestsRead {
		d.requestsRead = true
		inv := d.readEvidence(schedulinginsight.ProvisioningRequestGroup, provisioningRequestKind)
		d.requestsOK = inv.OK || inv.Gap == ""
		d.requestsByOwner = map[types.UID][]*unstructured.Unstructured{}
		for _, item := range inv.Items {
			if owner := metav1.GetControllerOf(item); owner != nil {
				d.requestsByOwner[owner.UID] = append(d.requestsByOwner[owner.UID], item)
			}
		}
	}
	if !d.requestsOK {
		return []issuesapi.DiagnosticFact{{
			Type:    factKueueProvisioningRequest,
			Message: "Radar couldn't read ProvisioningRequests, so any it retains for this Workload aren't shown.",
		}}
	}
	requests := schedulinginsight.KueueProvisioningRequests(w, d.requestsByOwner[w.GetUID()])
	if len(requests) > maxKueueLinkedObjects {
		requests = requests[:maxKueueLinkedObjects]
	}
	facts := make([]issuesapi.DiagnosticFact, 0, len(requests))
	for _, pr := range requests {
		outcome := "no outcome reported"
		for _, conditionType := range []string{"Failed", "CapacityRevoked", "BookingExpired", "Provisioned", "Accepted"} {
			cm, ok := kueueConditionMap(pr, conditionType)
			if !ok || cm["status"] != "True" {
				continue
			}
			reason, _ := cm["reason"].(string)
			message, _ := cm["message"].(string)
			outcome = conditionType + "=True"
			if reason != "" {
				outcome += " (" + reason + ")"
			}
			if message != "" {
				outcome += ": " + truncateKueueText(message)
			}
			break
		}
		facts = append(facts, issuesapi.DiagnosticFact{
			Type:       factKueueProvisioningRequest,
			Message:    fmt.Sprintf("ProvisioningRequest %s, created by Kueue for this Workload (not matched to a specific check): %s.", pr.GetName(), outcome),
			Confidence: issuesapi.ConfidenceHigh,
			Refs:       []Ref{{Group: schedulinginsight.ProvisioningRequestGroup, Kind: provisioningRequestKind, Namespace: pr.GetNamespace(), Name: pr.GetName()}},
		})
	}
	return facts
}

func truncateKueueText(s string) string {
	const max = 256
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// queueIssues raises a LocalQueue whose ClusterQueue is missing or broken,
// and, without a namespace filter, the broken ClusterQueue itself. covered
// holds the Workloads a LocalQueue row lists as affected.
func (d *kueueDetector) queueIssues(dependent map[string][]kueueRun) ([]Issue, map[types.UID]bool) {
	covered := map[types.UID]bool{}
	localQueues, _ := d.read(kueueGroup, kueueLocalQueueKind)
	canReadClusterQueues := d.f.CanReadClusterScoped == nil || d.f.CanReadClusterScoped(kueueClusterQueueKind, kueueGroup)
	var clusterQueues map[string]*unstructured.Unstructured
	clusterQueuesRead := false
	readClusterQueues := func() bool {
		if clusterQueues == nil && !clusterQueuesRead {
			clusterQueuesRead = true
			items, ok := d.read(kueueGroup, kueueClusterQueueKind)
			if !ok {
				return false
			}
			clusterQueues = make(map[string]*unstructured.Unstructured, len(items))
			for _, cq := range items {
				clusterQueues[cq.GetName()] = cq
			}
		}
		return clusterQueues != nil
	}

	var out []Issue
	affectedByClusterQueue := map[string][]Ref{}
	localQueues = append([]*unstructured.Unstructured(nil), localQueues...)
	sort.Slice(localQueues, func(i, j int) bool {
		return localQueues[i].GetNamespace()+"/"+localQueues[i].GetName() < localQueues[j].GetNamespace()+"/"+localQueues[j].GetName()
	})
	for _, lq := range localQueues {
		if lq == nil || lq.GetDeletionTimestamp() != nil || kueueStopped(lq) {
			continue
		}
		status, reason, lqMessage, current := kueueActive(lq)
		if status != "False" || !current || d.inGrace(lq) {
			continue
		}
		created := lq.GetCreationTimestamp().Time
		cqName, _, _ := unstructured.NestedString(lq.Object, "spec", "clusterQueue")
		runs := dependent[lq.GetNamespace()+"/"+lq.GetName()]
		var issueReason, message, cause, action string
		var requiredReads []EvidenceRead
		var queueFact *issuesapi.DiagnosticFact
		switch reason {
		case kueueLocalQueueClusterQueueMissing:
			issueReason = ReasonKueueClusterQueueMissing
			message = fmt.Sprintf("Kueue reports ClusterQueue %s does not exist.", cqName)
			cause = fmt.Sprintf("Kueue reports that ClusterQueue %s, which this LocalQueue points to, does not exist, so it admits no run submitted here.", cqName)
			action = "Create the ClusterQueue, or point the LocalQueue's spec.clusterQueue at an existing one."
		case kueueLocalQueueClusterQueueInactive:
			if !canReadClusterQueues {
				// The ClusterQueue decides whether this is a stop or a fault;
				// without it, say the check wasn't made rather than nothing.
				subjects := []Ref{{Group: kueueGroup, Kind: kueueLocalQueueKind, Namespace: lq.GetNamespace(), Name: lq.GetName()}}
				for _, run := range runs {
					subjects = append(subjects, d.runRefs(run.workload)...)
				}
				d.unchecked.addFor(issuesapi.UncheckedSourceKueue, issuesapi.UncheckedReasonCallerAccessDenied, issuesapi.UncheckedKind{Group: kueueGroup, Kind: kueueClusterQueueKind}, subjects)
				continue
			}
			if !readClusterQueues() {
				continue
			}
			cq := clusterQueues[cqName]
			if cq == nil {
				issueReason = ReasonKueueClusterQueueMissing
				message = fmt.Sprintf("ClusterQueue %s was not found.", cqName)
				cause = fmt.Sprintf("Kueue reports this LocalQueue inactive, and its ClusterQueue %s is not in Radar's complete ClusterQueue inventory.", cqName)
				action = "Create the ClusterQueue, or point the LocalQueue's spec.clusterQueue at an existing one."
				requiredReads = []EvidenceRead{{Group: kueueGroup, Resource: "clusterqueues", Verb: "list"}}
				break
			}
			cqReason, cqMessage, broken := kueueBrokenClusterQueue(cq)
			if !broken {
				continue
			}
			issueReason = ReasonKueueClusterQueueInactive
			message = fmt.Sprintf("ClusterQueue %s: %s", cqName, cqMessage)
			cause = fmt.Sprintf("This LocalQueue's ClusterQueue %s is inactive because of its configuration (%s), so Kueue admits no run submitted here. It is not stopped on purpose: its stopPolicy is None.", cqName, cqReason)
			action = "Fix the ClusterQueue as its message says, for example by creating the missing ResourceFlavor, AdmissionCheck or Topology it references."
			requiredReads = []EvidenceRead{{Group: kueueGroup, Resource: "clusterqueues", Verb: "list"}}
			fact := issuesapi.DiagnosticFact{
				Type: factKueueQueue, Confidence: issuesapi.ConfidenceHigh,
				Message: fmt.Sprintf("ClusterQueue %s: Active=False (%s): %s", cqName, cqReason, cqMessage),
				Refs:    []Ref{{Group: kueueGroup, Kind: kueueClusterQueueKind, Name: cqName}},
			}
			queueFact = &fact
		default:
			continue
		}

		issue := newConditionIssue(kueueGroupGVR, kueueLocalQueueKind, lq.GetNamespace(), lq.GetName(), SeverityWarning,
			issueReason, message, time.Time{}, false, issueReason+"\x00"+cqName, created)
		issue.RawMessage = lqMessage
		issue.MessageSource = issuesapi.MessageSourceRadar
		issue.Cause, issue.Action = cause, action
		issue.RequiredReads = requiredReads
		var facts []issuesapi.DiagnosticFact
		if queueFact != nil {
			facts = append(facts, *queueFact)
		}
		if affected := d.affectedRuns(runs, covered); len(affected) > 0 {
			issue.AffectedRefs = affected
			facts = append(facts, affectedRefsFact(factKueueAffectedRuns, kueueAffectedRunsMessage(kueueLocalQueueKind), affected))
			if issueReason == ReasonKueueClusterQueueInactive {
				affectedByClusterQueue[cqName] = append(affectedByClusterQueue[cqName], affected...)
			}
		}
		if len(facts) > 0 {
			issue.DiagnosticContext = &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleCandidate, Facts: facts}
		}
		out = append(out, issue)
	}

	// The ClusterQueue itself is cluster-scoped: like every cluster-scoped row,
	// it only joins answers that aren't narrowed to namespaces.
	if len(d.f.Namespaces) == 0 && canReadClusterQueues && readClusterQueues() {
		names := make([]string, 0, len(clusterQueues))
		for name := range clusterQueues {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			cq := clusterQueues[name]
			reason, message, broken := kueueBrokenClusterQueue(cq)
			if !broken {
				continue
			}
			if d.inGrace(cq) {
				continue
			}
			created := cq.GetCreationTimestamp().Time
			issue := newConditionIssue(kueueGroupGVR, kueueClusterQueueKind, "", name, SeverityWarning,
				ReasonKueueClusterQueueInactive, message, time.Time{}, false, ReasonKueueClusterQueueInactive, created)
			issue.RawMessage = message
			issue.Cause = fmt.Sprintf("Kueue reports this ClusterQueue inactive because of its configuration (%s), so it admits no new Workloads. It is not stopped on purpose: its stopPolicy is None.", reason)
			issue.Action = "Fix the ClusterQueue as its message says, for example by creating the missing ResourceFlavor, AdmissionCheck or Topology it references."
			facts := d.clusterQueueCheckFacts(cq, reason)
			if affected := dedupeRefs(affectedByClusterQueue[name]); len(affected) > 0 {
				issue.AffectedRefs = affected
				facts = append(facts, affectedRefsFact(factKueueAffectedRuns, kueueAffectedRunsMessage(kueueClusterQueueKind), affected))
			}
			if len(facts) > 0 {
				issue.DiagnosticContext = &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleCandidate, Facts: facts}
			}
			out = append(out, issue)
		}
	}
	return out, covered
}

// kueueBrokenClusterQueue reports a ClusterQueue that admits nothing because
// of its configuration, from its own current Active condition and stop policy.
func kueueBrokenClusterQueue(cq *unstructured.Unstructured) (reason, message string, broken bool) {
	if cq.GetDeletionTimestamp() != nil || kueueStopped(cq) {
		return "", "", false
	}
	status, reason, message, current := kueueActive(cq)
	if status != "False" || !current || !kueueBrokenClusterQueueReasons[reason] {
		return "", "", false
	}
	return reason, message, true
}

// clusterQueueCheckFacts link the AdmissionChecks a ClusterQueue names that
// are missing or not Active, when that is why it is inactive.
func (d *kueueDetector) clusterQueueCheckFacts(cq *unstructured.Unstructured, reason string) []issuesapi.DiagnosticFact {
	if reason != "AdmissionCheckNotFound" && reason != "AdmissionCheckInactive" {
		return nil
	}
	names := kueueClusterQueueCheckNames(cq)
	if len(names) == 0 {
		return nil
	}
	inv := d.readEvidence(kueueGroup, kueueAdmissionCheckKind)
	if !inv.OK {
		return nil
	}
	byName := map[string]*unstructured.Unstructured{}
	for _, check := range inv.Items {
		byName[check.GetName()] = check
	}
	var facts []issuesapi.DiagnosticFact
	for _, name := range names {
		check := byName[name]
		if check != nil {
			if status, _, _, _ := kueueActive(check); status == "True" {
				continue
			}
		}
		facts = append(facts, issuesapi.DiagnosticFact{
			Type: factKueueAdmissionCheck, Confidence: issuesapi.ConfidenceHigh,
			Message: kueueObjectActiveText(kueueAdmissionCheckKind, name, check),
			Refs:    []Ref{{Group: kueueGroup, Kind: kueueAdmissionCheckKind, Name: name}},
		})
		if len(facts) == maxKueueLinkedObjects {
			break
		}
	}
	return facts
}

// kueueClusterQueueCheckNames are the AdmissionChecks a ClusterQueue's spec
// names, in either the list or the per-flavor strategy form.
func kueueClusterQueueCheckNames(cq *unstructured.Unstructured) []string {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	list, _, _ := unstructured.NestedStringSlice(cq.Object, "spec", "admissionChecks")
	for _, name := range list {
		add(name)
	}
	rules, _, _ := unstructured.NestedSlice(cq.Object, "spec", "admissionChecksStrategy", "admissionChecks")
	for _, raw := range rules {
		if rule, ok := raw.(map[string]any); ok {
			name, _ := rule["name"].(string)
			add(name)
		}
	}
	sort.Strings(names)
	return names
}

func (d *kueueDetector) affectedRuns(runs []kueueRun, covered map[types.UID]bool) []Ref {
	refs := make([]Ref, 0, len(runs))
	for _, run := range runs {
		covered[run.workload.GetUID()] = true
		refs = append(refs, d.runRefs(run.workload)...)
	}
	refs = dedupeRefs(refs)
	sort.Slice(refs, func(i, j int) bool {
		return resourceKey(refs[i].Group, refs[i].Kind, refs[i].Namespace, refs[i].Name) < resourceKey(refs[j].Group, refs[j].Kind, refs[j].Namespace, refs[j].Name)
	})
	return refs
}

func withoutFact(ctx *issuesapi.DiagnosticContext, factType string) *issuesapi.DiagnosticContext {
	if ctx == nil {
		return nil
	}
	kept := make([]issuesapi.DiagnosticFact, 0, len(ctx.Facts))
	for _, fact := range ctx.Facts {
		if fact.Type != factType {
			kept = append(kept, fact)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	out := *ctx
	out.Facts = kept
	return &out
}

func isKueueClusterQueueRow(i Issue) bool {
	return i.Group == kueueGroup && i.Kind == kueueClusterQueueKind && i.Reason == ReasonKueueClusterQueueInactive
}

// addKueueClusterQueueEdges points each LocalQueue row that names this broken
// ClusterQueue at it. Grouped mode only, as for every incident parent.
func addKueueClusterQueueEdges(cq Issue, edges *[]incidentEdge, groupedByID map[string]Issue) {
	var symptoms []string
	for id, row := range groupedByID {
		if row.Group != kueueGroup || row.Kind != kueueLocalQueueKind || row.DiagnosticContext == nil {
			continue
		}
		for _, fact := range row.DiagnosticContext.Facts {
			if fact.Type == factKueueQueue && len(fact.Refs) == 1 && fact.Refs[0].Kind == kueueClusterQueueKind && fact.Refs[0].Group == kueueGroup && fact.Refs[0].Name == cq.Name {
				symptoms = append(symptoms, id)
				break
			}
		}
	}
	sort.Strings(symptoms)
	recordIncidentEdges(edges, cq, factKueueQueue, issuesapi.ConfidenceHigh, symptoms)
}

// isKueueLinkFact is a fact whose text describes the objects it links, so a
// caller who can't read one of them gets neither the link nor the text.
func isKueueLinkFact(factType string) bool {
	switch factType {
	case factKueueAdmission, factKueueQueue, factKueueAdmissionCheck, factKueueProvisioningRequest:
		return true
	}
	return false
}

// inGrace keeps a queue out of Issues for a minute after it was created or
// its Active condition last changed: a GitOps apply or a recreated flavor can
// leave it inactive for seconds.
func (d *kueueDetector) inGrace(u *unstructured.Unstructured) bool {
	since := u.GetCreationTimestamp().Time
	if cm, ok := kueueConditionMap(u, "Active"); ok {
		if at, known := conditionTransitionTime(cm); known && at.After(since) {
			since = at
		}
	}
	return !since.IsZero() && d.now.Sub(since) < kueueConfigurationGrace
}
