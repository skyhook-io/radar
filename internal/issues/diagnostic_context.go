package issues

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

const (
	maxDiagnosticRefs       = 5
	maxDiagnosticIssueRefs  = 5
	maxDiagnosticFacts      = 4
	factExplicitReference   = "explicit_reference"
	factOwnerRollup         = "owner_rollup"
	factSelectedBackend     = "selected_backend_issue"
	factServiceConfig       = "service_config_mismatch"
	factServiceEnvReference = "service_env_reference"
	factProbeTarget         = "probe_target_mismatch"
	factBlockedInit         = "blocked_init_container"
	factRestartCause        = "restart_cause"
	factNodeBlastRadius     = "node_blast_radius"
	factPVCBlastRadius      = "pvc_blast_radius"
)

type serviceBackendIssueProvider interface {
	SelectedPodsForService(namespace, name string) []Ref
}

type nodeBlastRadiusProvider interface {
	PodsOnNode(nodeName string) []Ref
}

type pvcBlastRadiusProvider interface {
	PodsMountingPVC(namespace, pvcName string) []Ref
}

// pvcRootCategories are PVC-level problems that block the pods mounting the claim.
var pvcRootCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryPVCPending:      true,
	issuesapi.CategoryPVCLost:         true,
	issuesapi.CategoryPVCResizeFailed: true,
}

// pvcAttributableCategories are the pod-side manifestations of a broken PVC: the
// pod can't schedule (volume binding), can't mount, or is stuck creating. An
// unrelated crashloop on a pod that merely happens to mount the claim is not
// PVC-caused and is excluded.
var pvcAttributableCategories = map[issuesapi.Category]bool{
	issuesapi.CategoryUnschedulable:     true,
	issuesapi.CategoryContainerWaiting:  true,
	issuesapi.CategoryVolumeMountFailed: true,
}

// nodeReasonAttributable maps a node problem reason to the pod-issue categories
// that reason can plausibly CAUSE — keyed on the reason because the cases are not
// equivalent. Resource pressure produces specific, live symptoms (memory → OOM /
// OOM-restart loops; disk or PID exhaustion → containers that can't be created).
//
// A fully NotReady (dead-kubelet) node is DELIBERATELY ABSENT: once the kubelet
// stops reporting, a pod's status is stale, so its crashloop / OOM rows are
// pre-existing application problems that merely happen to sit on the node — not
// node-caused. Linking them would tell the operator "the node may be the cause"
// when it isn't. The genuine dead-node blast radius (terminating / evicted pods,
// unschedulable replacements) isn't captured by these runtime categories and is
// left to a future, reason-aware detector. App-dominant categories (crashloop,
// probe failures, image pull, missing config) are excluded for the same reason.
var nodeReasonAttributable = map[string]map[issuesapi.Category]bool{
	"MemoryPressure": {
		issuesapi.CategoryOOMKilled:   true,
		issuesapi.CategoryHighRestart: true,
	},
	"DiskPressure": {
		issuesapi.CategoryContainerWaiting: true,
	},
	"PIDPressure": {
		issuesapi.CategoryContainerWaiting: true,
	},
}

type changeContextProvider interface {
	ChangeContextForIssue(Issue) *issuesapi.ChangeContext
}

func enrichDiagnosticContext(shaped, flat, grouped []Issue, p Provider) []Issue {
	if len(shaped) == 0 {
		return shaped
	}

	groupedByID := map[string]Issue(nil)
	if len(grouped) > 0 {
		groupedByID = make(map[string]Issue, len(grouped))
		for _, g := range grouped {
			groupedByID[g.ID] = g
		}
	}

	flatByResource := make(map[string][]Issue, len(flat))
	for _, f := range flat {
		key := resourceKey(f.Group, f.Kind, f.Namespace, f.Name)
		flatByResource[key] = append(flatByResource[key], f)
	}

	var serviceProvider serviceBackendIssueProvider
	if sp, ok := p.(serviceBackendIssueProvider); ok {
		serviceProvider = sp
	}
	var nodeProvider nodeBlastRadiusProvider
	if np, ok := p.(nodeBlastRadiusProvider); ok {
		nodeProvider = np
	}
	var pvcProvider pvcBlastRadiusProvider
	if pp, ok := p.(pvcBlastRadiusProvider); ok {
		pvcProvider = pp
	}
	var changeProvider changeContextProvider
	if cp, ok := p.(changeContextProvider); ok {
		changeProvider = cp
	}

	out := append([]Issue(nil), shaped...)
	for idx := range out {
		var b diagnosticContextBuilder
		i := &out[idx]
		if changeProvider != nil {
			i.ChangeContext = changeProvider.ChangeContextForIssue(*i)
		}

		if i.Source == SourceMissingRef {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factExplicitReference,
				Message: "Detected from an explicit reference to an object that does not exist or cannot be resolved.",
			})
		}

		if isServiceConfigMismatch(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factServiceConfig,
				Message: i.Reason,
			})
		}

		if isServiceEnvReferenceMismatch(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factServiceEnvReference,
				Message: diagnosticMessage(*i),
			})
		}

		if isProbeTargetMismatch(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factProbeTarget,
				Message: diagnosticMessage(*i),
			})
		}

		if isBlockedInitContainer(*i) {
			b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
				Type:    factBlockedInit,
				Message: diagnosticMessage(*i),
			})
		}

		if fact, ok := restartCauseFact(*i); ok {
			b.add(issuesapi.DiagnosticRoleContext, fact)
		}

		if i.GroupingScope == issuesapi.ScopeWorkload && len(i.Members) > 0 {
			refs := limitRefs(i.Members, maxDiagnosticRefs)
			msg := fmt.Sprintf("Grouped from %d affected resource(s) under this %s.", i.Count, i.Kind)
			if i.MembersTruncated {
				msg += " Member refs are truncated."
			}
			b.add(issuesapi.DiagnosticRoleRollup, issuesapi.DiagnosticFact{
				Type:    factOwnerRollup,
				Message: msg,
				Refs:    refs,
			})
		}

		if serviceProvider != nil && isServiceBackendContextCandidate(*i) {
			addServiceBackendContext(&b, *i, serviceProvider, flatByResource, groupedByID)
		}

		if nodeProvider != nil && i.Kind == "Node" && i.Category == issuesapi.CategoryNodeNotReady {
			addNodeBlastRadiusContext(&b, *i, nodeProvider, flatByResource, groupedByID)
		}

		if pvcProvider != nil && i.Kind == "PersistentVolumeClaim" && pvcRootCategories[i.Category] {
			addPVCBlastRadiusContext(&b, *i, pvcProvider, flatByResource, groupedByID)
		}

		if ctx := b.build(); ctx != nil {
			i.DiagnosticContext = ctx
		}
	}

	return out
}

func addServiceBackendContext(b *diagnosticContextBuilder, issue Issue, serviceProvider serviceBackendIssueProvider, flatByResource map[string][]Issue, groupedByID map[string]Issue) {
	if !isServiceBackendContextCandidate(issue) {
		return
	}
	pods := serviceProvider.SelectedPodsForService(issue.Namespace, issue.Name)
	if len(pods) == 0 {
		return
	}
	pods = append([]Ref(nil), pods...)
	sortRefs(pods)

	seenIDs := make(map[string]bool)
	var related []issuesapi.IssueRef
	var refs []Ref
	for _, pod := range pods {
		key := resourceKey(pod.Group, pod.Kind, pod.Namespace, pod.Name)
		for _, flatIssue := range flatByResource[key] {
			if flatIssue.ID == issue.ID || seenIDs[flatIssue.ID] {
				continue
			}
			grouped, ok := groupedByID[flatIssue.ID]
			if !ok {
				grouped = flatIssue
			}
			related = append(related, issueRef(grouped))
			refs = append(refs, pod)
			seenIDs[flatIssue.ID] = true
			if len(related) >= maxDiagnosticIssueRefs {
				break
			}
		}
		if len(related) >= maxDiagnosticIssueRefs {
			break
		}
	}
	if len(related) == 0 {
		return
	}

	sortIssueRefs(related)
	sortRefs(refs)
	b.add(issuesapi.DiagnosticRoleAffected, issuesapi.DiagnosticFact{
		Type:          factSelectedBackend,
		Message:       "Selected backend pod(s) already have active issues.",
		Confidence:    issuesapi.ConfidenceHigh, // declared selector edge Service→Pod
		Refs:          limitRefs(dedupeRefs(refs), maxDiagnosticRefs),
		RelatedIssues: limitIssueRefs(related, maxDiagnosticIssueRefs),
	})
}

// linkBlastRadius adds a candidate-role fact linking a root issue to the
// category-attributable issues of a set of affected pods (each flat pod issue
// mapped to its grouped form). Non-destructive — it only annotates the root.
// `accept` is an optional per-symptom guard beyond the category filter.
func linkBlastRadius(b *diagnosticContextBuilder, pods []Ref, attributable map[issuesapi.Category]bool, flatByResource map[string][]Issue, groupedByID map[string]Issue, factType string, conf issuesapi.Confidence, message string, accept func(Issue) bool) {
	if len(pods) == 0 {
		return
	}
	// Keep each linked issue paired with the pod it came from, so ranking and
	// capping act on the pair — otherwise the displayed pods (Refs) and the
	// displayed issues (RelatedIssues) are sorted/capped independently and the
	// two lists diverge past the cap.
	type linked struct {
		ref Ref
		rel issuesapi.IssueRef
	}
	seenIDs := make(map[string]bool)
	var links []linked
	for _, pod := range pods {
		key := resourceKey(pod.Group, pod.Kind, pod.Namespace, pod.Name)
		for _, flatIssue := range flatByResource[key] {
			if !attributable[flatIssue.Category] || seenIDs[flatIssue.ID] {
				continue
			}
			if accept != nil && !accept(flatIssue) {
				continue
			}
			grouped, ok := groupedByID[flatIssue.ID]
			if !ok {
				grouped = flatIssue
			}
			links = append(links, linked{ref: pod, rel: issueRef(grouped)})
			seenIDs[flatIssue.ID] = true
		}
	}
	if len(links) == 0 {
		return
	}
	// Rank by the linked issue's severity BEFORE capping, so the cap keeps the
	// worst issues (and their pods), not whatever came first in iteration order.
	sort.SliceStable(links, func(i, j int) bool { return lessIssueRef(links[i].rel, links[j].rel) })
	if len(links) > maxDiagnosticIssueRefs {
		links = links[:maxDiagnosticIssueRefs]
	}
	related := make([]issuesapi.IssueRef, 0, len(links))
	refs := make([]Ref, 0, len(links))
	for _, l := range links {
		related = append(related, l.rel)
		refs = append(refs, l.ref)
	}
	b.add(issuesapi.DiagnosticRoleCandidate, issuesapi.DiagnosticFact{
		Type:          factType,
		Message:       message,
		Confidence:    conf,
		Refs:          limitRefs(dedupeRefs(refs), maxDiagnosticRefs),
		RelatedIssues: related,
	})
}

// addNodeBlastRadiusContext links a resource-pressured node to the pod issues
// that pressure plausibly caused (matched by spec.nodeName, gated by the
// reason→category map). The node is the candidate root; the pod issues are its
// blast radius. Confidence is medium, not high: spec.nodeName proves a pod is ON
// the node, and the category is consistent with the pressure, but co-located is
// not proof of cause — hence the "may be the cause / verify" framing. A node whose
// reason has no attributable categories (a dead-kubelet NotReady node, or an
// unrecognized reason) links nothing. No timestamp guard: pod-issue onset isn't
// reliably recorded (FirstSeen tracks pod age, not failure onset), so a guard
// would drop legitimate long-running pods while still admitting unrelated ones.
func addNodeBlastRadiusContext(b *diagnosticContextBuilder, node Issue, np nodeBlastRadiusProvider, flatByResource map[string][]Issue, groupedByID map[string]Issue) {
	// A node can hit several pressures at once (memory + disk + PID); those
	// detections share the node_not_ready ID and group into one issue that keeps
	// only one representative Reason. Union the attributable categories across ALL
	// of the node's detected reasons so a multi-pressure node links every pressure's
	// pods (OOM under memory, stuck-creation under disk/PID), not just the
	// representative's. (The flat node rows for both pressures sit under the node's
	// resource key whether we were handed the grouped issue or a flat one.)
	attributable := map[issuesapi.Category]bool{}
	for _, f := range flatByResource[issueResourceKey(node)] {
		if f.Kind == "Node" && f.Category == issuesapi.CategoryNodeNotReady {
			for c := range nodeReasonAttributable[f.Reason] {
				attributable[c] = true
			}
		}
	}
	// Fallback to the issue's own reason if no flat node rows are indexed under
	// this key (defensive — normally the detections are present).
	if len(attributable) == 0 {
		for c := range nodeReasonAttributable[node.Reason] {
			attributable[c] = true
		}
	}
	if len(attributable) == 0 {
		return
	}
	linkBlastRadius(b, np.PodsOnNode(node.Name), attributable, flatByResource, groupedByID,
		factNodeBlastRadius, issuesapi.ConfidenceMedium,
		"Pods on this node show problems consistent with its resource pressure — the node may be the cause.", nil)
}

// addPVCBlastRadiusContext links a broken PVC (pending / lost / resize-failed) to
// the storage issues of the pods that mount it. The mount edge is the declared
// claimName, so a volume_mount_failed is unambiguously this PVC's fault (high
// confidence). An unschedulable / container-waiting pod that mounts the claim is
// only linked when its own message confirms a volume cause — a pod can mount the
// PVC yet be unschedulable for CPU or waiting on unrelated config, which must not
// be attributed to the PVC.
func addPVCBlastRadiusContext(b *diagnosticContextBuilder, pvc Issue, pp pvcBlastRadiusProvider, flatByResource map[string][]Issue, groupedByID map[string]Issue) {
	linkBlastRadius(b, pp.PodsMountingPVC(pvc.Namespace, pvc.Name), pvcAttributableCategories, flatByResource, groupedByID,
		factPVCBlastRadius, issuesapi.ConfidenceHigh,
		"Pods mounting this PVC are blocked by it.",
		func(symptom Issue) bool {
			if symptom.Category == issuesapi.CategoryVolumeMountFailed {
				return true
			}
			return symptomMentionsVolume(symptom, pvc.Name)
		})
}

// symptomMentionsVolume reports whether a scheduling / waiting symptom's text
// confirms a volume cause — it names the PVC, or carries the scheduler's
// volume-binding language — so an unrelated CPU-unschedulable pod that merely
// mounts the claim isn't attributed to it.
func symptomMentionsVolume(symptom Issue, pvcName string) bool {
	text := strings.ToLower(symptom.Message + " " + symptom.Reason)
	// Match the PVC name only when QUOTED, the way Kubernetes prints it
	// (persistentvolumeclaim "name" not found). A bare substring match would fire
	// on any text that happens to contain a short claim name like "data".
	if pvcName != "" && strings.Contains(text, `"`+strings.ToLower(pvcName)+`"`) {
		return true
	}
	return strings.Contains(text, "persistentvolumeclaim") || strings.Contains(text, "unbound") || strings.Contains(text, "volume node affinity")
}

func isServiceBackendContextCandidate(issue Issue) bool {
	return issue.Kind == "Service" && issue.Category == issuesapi.CategoryServiceNoEndpoints && strings.Contains(issue.Reason, "selected pods ready")
}

func isServiceConfigMismatch(i Issue) bool {
	if i.Category != issuesapi.CategoryServiceNoEndpoints {
		return false
	}
	reason := strings.ToLower(i.Reason)
	return strings.Contains(reason, "selector matches no pods") || strings.Contains(reason, "unresolved named targetport")
}

func isServiceEnvReferenceMismatch(i Issue) bool {
	return i.Reason == "Service port mismatch" || i.Reason == "Missing referenced Service"
}

func isProbeTargetMismatch(i Issue) bool {
	return i.Reason == "ReadinessProbeInvalid" || i.Reason == "LivenessProbeInvalid"
}

func isBlockedInitContainer(i Issue) bool {
	return i.Reason == "InitContainerStalled"
}

func restartCauseFact(i Issue) (issuesapi.DiagnosticFact, bool) {
	if i.RestartCount <= 0 && i.LastTerminatedReason == "" {
		return issuesapi.DiagnosticFact{}, false
	}
	var parts []string
	if i.RestartCount > 0 {
		parts = append(parts, fmt.Sprintf("restartCount=%d", i.RestartCount))
	}
	if i.LastTerminatedReason != "" {
		parts = append(parts, fmt.Sprintf("lastTerminatedReason=%s", i.LastTerminatedReason))
	}
	if i.Reason == "LivenessProbeFailed" || i.Reason == "ReadinessProbeFailed" {
		parts = append(parts, fmt.Sprintf("probeFailure=%s", i.Reason))
	}
	return issuesapi.DiagnosticFact{
		Type:    factRestartCause,
		Message: "Container restart evidence: " + strings.Join(parts, ", ") + ".",
	}, true
}

func diagnosticMessage(i Issue) string {
	if i.Message != "" {
		return i.Message
	}
	return i.Reason
}

func issueRef(i Issue) issuesapi.IssueRef {
	return issuesapi.IssueRef{
		Ref:      Ref{Group: i.Group, Kind: i.Kind, Namespace: i.Namespace, Name: i.Name},
		Reason:   i.Reason,
		Category: i.Category,
		Severity: i.Severity,
	}
}

type diagnosticContextBuilder struct {
	role      issuesapi.DiagnosticRole
	facts     []issuesapi.DiagnosticFact
	factRanks []int
}

func (b *diagnosticContextBuilder) add(role issuesapi.DiagnosticRole, fact issuesapi.DiagnosticFact) {
	if fact.Type == "" {
		return
	}
	rank := diagnosticRoleRank(role)
	if rank > diagnosticRoleRank(b.role) {
		b.role = role
	}
	if len(b.facts) >= maxDiagnosticFacts {
		replace := -1
		lowest := rank
		for idx, existing := range b.factRanks {
			if existing < lowest {
				lowest = existing
				replace = idx
			}
		}
		if replace < 0 {
			return
		}
		b.facts[replace] = fact
		b.factRanks[replace] = rank
		return
	}
	b.facts = append(b.facts, fact)
	b.factRanks = append(b.factRanks, rank)
}

func (b diagnosticContextBuilder) build() *issuesapi.DiagnosticContext {
	if len(b.facts) == 0 {
		return nil
	}
	return &issuesapi.DiagnosticContext{Role: b.role, Facts: b.facts}
}

func diagnosticRoleRank(role issuesapi.DiagnosticRole) int {
	switch role {
	case issuesapi.DiagnosticRoleCandidate:
		return 4
	case issuesapi.DiagnosticRoleAffected:
		return 3
	case issuesapi.DiagnosticRoleRollup:
		return 2
	case issuesapi.DiagnosticRoleContext:
		return 1
	default:
		return 0
	}
}

func limitRefs(refs []Ref, max int) []Ref {
	if len(refs) == 0 || max <= 0 {
		return nil
	}
	out := append([]Ref(nil), refs...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func limitIssueRefs(refs []issuesapi.IssueRef, max int) []issuesapi.IssueRef {
	if len(refs) == 0 || max <= 0 {
		return nil
	}
	out := append([]issuesapi.IssueRef(nil), refs...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func dedupeRefs(refs []Ref) []Ref {
	seen := make(map[string]bool, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		key := resourceKey(ref.Group, ref.Kind, ref.Namespace, ref.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}

// lessIssueRef orders issue refs worst-first: severity desc, then a stable total
// order over the identity fields.
func lessIssueRef(a, b issuesapi.IssueRef) bool {
	if a.Severity != b.Severity {
		return SeverityRank(a.Severity) > SeverityRank(b.Severity)
	}
	if a.Ref.Namespace != b.Ref.Namespace {
		return a.Ref.Namespace < b.Ref.Namespace
	}
	if a.Ref.Name != b.Ref.Name {
		return a.Ref.Name < b.Ref.Name
	}
	if a.Ref.Kind != b.Ref.Kind {
		return a.Ref.Kind < b.Ref.Kind
	}
	if a.Ref.Group != b.Ref.Group {
		return a.Ref.Group < b.Ref.Group
	}
	return a.Reason < b.Reason
}

func sortIssueRefs(refs []issuesapi.IssueRef) {
	sort.SliceStable(refs, func(i, j int) bool { return lessIssueRef(refs[i], refs[j]) })
}
