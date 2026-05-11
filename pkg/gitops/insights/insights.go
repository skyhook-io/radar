package insights

import (
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
)

type Insight struct {
	Summary      Summary       `json:"summary"`
	Issues       []Issue       `json:"issues"`
	Changes      []Change      `json:"changes"`
	Plan         []PlanItem    `json:"plan"`
	History      []HistoryItem `json:"history"`
	Capabilities Capabilities  `json:"capabilities"`
	// Warnings explain non-fatal reasons the response is incomplete (RBAC
	// short-circuit, controller unreachable). UI uses this to distinguish
	// "no data" from "we couldn't fetch it".
	Warnings []string `json:"warnings,omitempty"`
	// Partial=true means desired-manifest diffs (Git vs live) aren't computed —
	// always true today. Pairs with Summary.PartialReason for the UI hint.
	Partial bool `json:"partial"`
}

type Summary struct {
	Tool             string `json:"tool"`
	Kind             string `json:"kind"`
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	Sync             string `json:"sync,omitempty"`
	Health           string `json:"health,omitempty"`
	OperationPhase   string `json:"operationPhase,omitempty"`
	OperationMessage string `json:"operationMessage,omitempty"`
	Source           string `json:"source,omitempty"`
	TargetRevision   string `json:"targetRevision,omitempty"`
	LastRevision     string `json:"lastRevision,omitempty"`
	LastReconcile    string `json:"lastReconcile,omitempty"`
	PartialReason    string `json:"partialReason,omitempty"`
	// AutoSyncMode is the human-readable syncPolicy chip label, e.g.
	// "Manual", "Auto", "Auto · prune", "Auto · self-heal",
	// "Auto · prune · self-heal", "Suspended" (Flux), or "".
	AutoSyncMode string `json:"autoSyncMode,omitempty"`
	// Terminating mirrors metadata.deletionTimestamp. When true the
	// operations layer rejects mutating verbs with ErrResourceTerminating;
	// the UI renders the [Terminating] chip and disables action buttons.
	Terminating          bool   `json:"terminating,omitempty"`
	TerminationStartedAt string `json:"terminationStartedAt,omitempty"`
	// Finalizers names the controller(s) that must run cleanup before
	// deletion completes. When the resource is stuck Terminating, this is
	// the operator's first lead on which controller to investigate.
	Finalizers []string `json:"finalizers,omitempty"`
}

type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Remediation describes a structured next-step for an Issue. Kind names the
// pattern (RemediationCreateNamespace etc.); Target names the K8s resource
// the remedy operates on (a namespace name, a resource ref, etc.). The
// frontend dispatches on Kind to render the right button + onClick handler.
//
// Invariants (per Kind):
//   RemediationCreateNamespace: Target MUST be a non-empty namespace name.
//
// Construct via NewCreateNamespaceRemediation rather than struct literal —
// the constructor enforces the per-Kind invariants; literal construction
// can produce a Remediation that ships to the frontend with a Kind it can't
// act on. Validate() runs the same check for callers that hold a value.
type Remediation struct {
	Kind   RemediationKind `json:"kind"`
	Target string          `json:"target,omitempty"`
	// Hint is operator-facing copy explaining what the action will do.
	// Distinct from the Issue's own Action string, which describes the
	// manual path; Hint describes what *this button* does.
	Hint string `json:"hint,omitempty"`
}

// NewCreateNamespaceRemediation constructs a validated create-namespace
// remediation. Returns nil when the namespace name is empty — every caller
// that holds the namespace name already does so because a regex captured it,
// so nil here means "the parser didn't actually capture a target" and the
// Issue should ship without a Remediation rather than with a broken one.
func NewCreateNamespaceRemediation(namespace, hint string) *Remediation {
	if namespace == "" {
		return nil
	}
	return &Remediation{Kind: RemediationCreateNamespace, Target: namespace, Hint: hint}
}

// Validate reports whether the Remediation is internally consistent for its
// Kind. Returns nil for valid values, an error describing the violation
// otherwise. Used by tests + future consumers that build remediations from
// untrusted input.
func (r *Remediation) Validate() error {
	if r == nil {
		return nil
	}
	switch r.Kind {
	case RemediationCreateNamespace:
		if r.Target == "" {
			return fmt.Errorf("create-namespace remediation requires Target (namespace name)")
		}
		return nil
	default:
		return fmt.Errorf("unknown remediation kind %q", r.Kind)
	}
}

type Issue struct {
	Severity Severity `json:"severity"`
	Scope    Scope    `json:"scope"`
	Reason   string   `json:"reason"`
	Message  string   `json:"message"`
	Refs     []Ref    `json:"refs,omitempty"`
	Action   string   `json:"action,omitempty"`
	// Remediation, when set, exposes a structured one-click fix for this
	// Issue. Frontend renders a contextual button on the failure card.
	// Nil when no automated remedy is appropriate; the Action string still
	// describes the manual path in that case.
	Remediation *Remediation `json:"remediation,omitempty"`
	// Cause is the parsed root-cause label for recognized error patterns
	// (annotation-too-large, webhook denial, RBAC). UI falls back to
	// Message when empty.
	Cause string `json:"cause,omitempty"`
	// RetryCount parsed from Argo's "(retried N times)" suffix. 0 means
	// no retry info or first attempt; the UI suppresses the "stuck"
	// indicator at 0 regardless of Stuck.
	RetryCount int `json:"retryCount,omitempty"`
	// Stuck=true when retry count crosses the "no longer transient"
	// threshold. Drives a stronger visual treatment.
	Stuck bool `json:"stuck,omitempty"`
}

type Change struct {
	Ref      Ref      `json:"ref"`
	Category Category `json:"category"`
	Sync     string   `json:"sync,omitempty"`
	Health   string   `json:"health,omitempty"`
	Message  string   `json:"message,omitempty"`
	// SyncError is Argo's status.resources[].syncResult message — the last
	// sync's per-resource failure. Distinct from Message (live health) so
	// the UI can show "degraded right now" vs "last sync errored".
	SyncError string `json:"syncError,omitempty"`
	// HookPhase identifies sync hook resources (PreSync / PostSync /
	// SyncFail / PostDelete); empty for non-hook resources.
	HookPhase  string `json:"hookPhase,omitempty"`
	HasDesired bool   `json:"hasDesired"`
	HasLive    bool   `json:"hasLive"`
	// Drift is the per-field diff between desired (parsed from the
	// kubectl.kubernetes.io/last-applied-configuration annotation) and live
	// spec. Nil when the diff isn't computable: SSA / Helm-installed
	// resources don't carry the annotation, and missing live data also nils.
	Drift *Drift `json:"drift,omitempty"`
	// RecentEvents are the newest-first events for this resource (capped),
	// inlined in the Changes view so ImagePullBackOff / FailedScheduling /
	// webhook denials are visible without opening the resource drawer.
	RecentEvents []EventSummary `json:"recentEvents,omitempty"`
	Partial      bool           `json:"partial"`
	PartialNote  string         `json:"partialNote,omitempty"`
}

// Drift describes the per-field difference between desired and live spec.
// Only entries that meaningfully differ are included; unchanged fields are
// elided. The UI renders this inline so the user can see exactly what's
// drifted without having to call the Argo API or run `argocd app diff`.
type Drift struct {
	Entries []DriftEntry `json:"entries"`
	// Source identifies how the desired state was derived. Currently only
	// DriftSourceLastApplied; future SSA support may add others.
	Source DriftSource `json:"source"`
	// Truncated is set when the diff exceeded our entry cap; UI uses this
	// to show "and N more differences — open in Argo for full diff".
	Truncated bool `json:"truncated,omitempty"`
}

// DriftEntry is a single field-level difference. Path uses dot-notation
// from the root (e.g. "spec.disruption.expireAfter"); array indices appear
// as ".[0]". Desired/Live are JSON-encoded so map/array values survive the
// wire round-trip — the UI pretty-prints them.
type DriftEntry struct {
	Path    string  `json:"path"`
	Op      DriftOp `json:"op"`
	Desired string  `json:"desired,omitempty"`
	Live    string  `json:"live,omitempty"`
}

type PlanItem struct {
	Ref          Ref      `json:"ref"`
	Phase        string   `json:"phase,omitempty"`
	Wave         int      `json:"wave,omitempty"`
	WaveSet      bool     `json:"waveSet,omitempty"`
	Order        int      `json:"order"`
	Hook         string   `json:"hook,omitempty"`
	Relationship string   `json:"relationship,omitempty"`
	Status       string   `json:"status,omitempty"`
	BlockedBy    []Ref    `json:"blockedBy,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

type HistoryItem struct {
	ID          string `json:"id,omitempty"`
	Revision    string `json:"revision,omitempty"`
	DeployedAt  string `json:"deployedAt,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Message     string `json:"message,omitempty"`
	Source      string `json:"source,omitempty"`
	InitiatedBy string `json:"initiatedBy,omitempty"`
}

type Capabilities struct {
	Sync              bool     `json:"sync"`
	Refresh           bool     `json:"refresh"`
	Terminate         bool     `json:"terminate"`
	Suspend           bool     `json:"suspend"`
	Resume            bool     `json:"resume"`
	SyncWithSource    bool     `json:"syncWithSource"`
	SelectiveSync     bool     `json:"selectiveSync"`
	Rollback          bool     `json:"rollback"`
	UnsupportedReason string   `json:"unsupportedReason,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
}

// Resolver supplies the cluster-state lookups insights needs beyond what's
// already on the GitOps root CR. Both methods return zero values on miss
// (nil object, nil events) — callers must tolerate misses since RBAC,
// kind-not-cached, and namespace filtering can all suppress results.
//
// A nil Resolver is valid and means "skip the enrichment that would need
// these lookups": no per-resource drift diff, no recent events. Tests and
// preview callers use nil; the production handler wires the dynamic cache.
type Resolver interface {
	// GetLive returns the live unstructured object, used to read the
	// kubectl.kubernetes.io/last-applied-configuration annotation and
	// diff it against the live spec.
	GetLive(group, kind, namespace, name string) *unstructured.Unstructured
	// RecentEvents returns up to a small handful of recent events for the
	// referenced resource, newest first. Used to surface "why is this
	// stuck" causes (image pull failure, PVC pending, webhook denial)
	// inline next to the change row instead of forcing a drill-in.
	RecentEvents(group, kind, namespace, name string) []EventSummary
	// FinalizerOwnerStatus returns a short health summary of the
	// controller responsible for clearing the given finalizer key on
	// `root`. Returns an empty string when the finalizer key isn't
	// recognized, the install namespace doesn't have matching pods, or
	// the lookup fails — callers must tolerate empty as "no signal".
	//
	// Used by detectPendingDeletion to bridge the gap between "this is
	// stuck on a finalizer" (controller-side responsibility) and *which*
	// controller and *what state it's in*. Without this signal, a stuck
	// deletion just says "investigate the controller"; with it, the
	// Issue can say "argocd-application-controller is CrashLoopBackOff
	// — start there".
	FinalizerOwnerStatus(finalizer string, root *unstructured.Unstructured) string
}

// EventSummary is a compact projection of a corev1.Event for UI display.
// We strip everything that's not useful at a glance — count + type + reason
// + message + age is what an operator scans first.
type EventSummary struct {
	Type               string `json:"type"`            // Normal | Warning
	Reason             string `json:"reason"`          // FailedScheduling, ImagePullBackOff, etc.
	Message            string `json:"message"`         // human-readable detail
	Count              int32  `json:"count,omitempty"` // event aggregation count (>1 indicates repetition)
	LastTimestamp      string `json:"lastTimestamp"`   // RFC3339 of most recent occurrence
	ReportingComponent string `json:"reportingComponent,omitempty"`
}

func Build(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, resolver Resolver) Insight {
	tool := detectTool(root)
	out := Insight{
		Summary:      buildSummary(root, tool),
		Issues:       buildIssues(root, resourceTree, tool, resolver),
		Changes:      buildChanges(root, resourceTree, tool, resolver),
		Plan:         buildPlan(root, resourceTree, tool),
		History:      buildHistory(root, tool),
		Capabilities: buildCapabilities(root, tool),
		Partial:      true,
	}
	out.Summary.PartialReason = "Radar shows the controller's drift assessment plus a per-resource field diff and recent events (when available). For the canonical line-by-line diff against Git, use the Argo CD UI or `argocd app diff`."
	return out
}

func detectTool(root *unstructured.Unstructured) string {
	if root == nil {
		return ""
	}
	if strings.EqualFold(root.GetKind(), "Application") || strings.Contains(root.GetAPIVersion(), "argoproj.io/") {
		return "argocd"
	}
	return "fluxcd"
}

func buildSummary(root *unstructured.Unstructured, tool string) Summary {
	s := Summary{
		Tool:      tool,
		Kind:      root.GetKind(),
		Namespace: root.GetNamespace(),
		Name:      root.GetName(),
	}
	// Lifecycle: zero-cost to surface even on healthy resources, removes
	// a class of "the page lies — clicking buttons does nothing" bugs
	// where the resource is actually pending deletion.
	if dt := root.GetDeletionTimestamp(); dt != nil && !dt.IsZero() {
		s.Terminating = true
		s.TerminationStartedAt = dt.UTC().Format(time.RFC3339)
		s.Finalizers = root.GetFinalizers()
	}
	if tool == "argocd" {
		s.Sync, _, _ = unstructured.NestedString(root.Object, "status", "sync", "status")
		s.Health, _, _ = unstructured.NestedString(root.Object, "status", "health", "status")
		s.OperationPhase, _, _ = unstructured.NestedString(root.Object, "status", "operationState", "phase")
		s.OperationMessage, _, _ = unstructured.NestedString(root.Object, "status", "operationState", "message")
		s.TargetRevision, _, _ = unstructured.NestedString(root.Object, "status", "sync", "revision")
		s.LastRevision, _, _ = unstructured.NestedString(root.Object, "status", "operationState", "syncResult", "revision")
		s.LastReconcile, _, _ = unstructured.NestedString(root.Object, "status", "reconciledAt")
		source, _, _ := unstructured.NestedMap(root.Object, "spec", "source")
		if len(source) == 0 {
			sources, _, _ := unstructured.NestedSlice(root.Object, "spec", "sources")
			if len(sources) > 0 {
				source, _ = sources[0].(map[string]any)
			}
		}
		s.Source = joinNonEmpty(gitops.StringValue(source["repoURL"]), gitops.StringValue(source["path"]), gitops.StringValue(source["chart"]))
		s.AutoSyncMode = describeArgoAutoSync(root)
		return s
	}
	status := fluxStatus(root)
	s.Sync = status.sync
	s.Health = status.health
	s.TargetRevision, _, _ = unstructured.NestedString(root.Object, "status", "lastAttemptedRevision")
	s.LastRevision, _, _ = unstructured.NestedString(root.Object, "status", "lastAppliedRevision")
	s.LastReconcile, _, _ = unstructured.NestedString(root.Object, "status", "lastHandledReconcileAt")
	if s.LastReconcile == "" {
		s.LastReconcile = newestConditionTime(root)
	}
	if ref, ok := nestedRef(root, "spec", "sourceRef"); ok {
		s.Source = ref.Kind + "/" + ref.Name
	} else if ref, ok := nestedRef(root, "spec", "chart", "spec", "sourceRef"); ok {
		s.Source = ref.Kind + "/" + ref.Name
	}
	if suspended, _, _ := unstructured.NestedBool(root.Object, "spec", "suspend"); suspended {
		s.AutoSyncMode = "Suspended"
	} else {
		s.AutoSyncMode = "Auto"
	}
	return s
}

// describeArgoAutoSync formats spec.syncPolicy.automated into a chip label.
// Empty when the field can't be read; "Manual" when automated is absent.
func describeArgoAutoSync(root *unstructured.Unstructured) string {
	automated, found, _ := unstructured.NestedMap(root.Object, "spec", "syncPolicy", "automated")
	if !found {
		return "Manual"
	}
	parts := []string{"Auto"}
	if v, ok := automated["prune"].(bool); ok && v {
		parts = append(parts, "prune")
	}
	if v, ok := automated["selfHeal"].(bool); ok && v {
		parts = append(parts, "self-heal")
	}
	return strings.Join(parts, " · ")
}

func buildIssues(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string, resolver Resolver) []Issue {
	var out []Issue
	// Pending deletion is appended first; the severity-stable sort below
	// may reorder by severity-rank (e.g. a critical operation failure can
	// land above an alert-tier lifecycle issue). The user-facing
	// "lifecycle dominates" contract is enforced by the *frontend*
	// (GitOpsIssuesBand extracts scope=lifecycle to render as a banner
	// above all other issues), so the array order here is incidental.
	// If a future caller renders Issues in raw order without the banner
	// extraction, that caller must hoist the lifecycle Issue itself.
	if pd := detectPendingDeletion(root, resolver); pd != nil {
		out = append(out, *pd)
	}
	// suppressedRefs tracks resources whose own Issue is causally derivative of
	// a parent operation failure (e.g. an OutOfSync resource issue is just
	// the per-resource view of an apply that already failed at the operation
	// level). Hiding these prevents the user from seeing the same root cause
	// rendered in three different forms.
	suppressedRefs := map[string]bool{}
	suppressedNamespaces := map[string]bool{}
	// operationFailed gates two downstream suppressions when the parent op
	// has parked in Failed/Error: (1) Argo's SyncError condition is a
	// parallel encoding of the same operationState.message we already render
	// in the failure card, and (2) per-resource Missing/OutOfSync issues
	// for resources that can't exist because the parent failure is upstream
	// (e.g. missing namespace) are just downstream symptoms. The user has
	// already seen the root cause in the failure card; surfacing the
	// derivative rows below it makes the page look like 4 separate problems
	// instead of 1.
	operationFailed := false
	if tool == "argocd" {
		if phase, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "phase"); phase == "Failed" || phase == "Error" {
			operationFailed = true
			msg, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "message")
			parsed := parseArgoOperationError(msg)
			issue := Issue{
				Severity:    SeverityCritical,
				Scope:       ScopeOperation,
				Reason:      phase,
				Message:     fallback(msg, "Last sync operation failed"),
				Action:      "Open Activity for operation details.",
				Cause:       parsed.Cause,
				RetryCount:  parsed.RetryCount,
				Stuck:       parsed.Stuck,
				Remediation: parsed.Remediation,
			}
			if parsed.AffectedKind != "" && parsed.AffectedName != "" {
				ref := Ref{Kind: parsed.AffectedKind, Name: parsed.AffectedName}
				issue.Refs = []Ref{ref}
				// argoAffectedRefRE captures Kind + Name only — Argo's operation
				// message doesn't include the resource namespace. The per-resource
				// pass below carries a populated namespace, so we key the
				// suppression set by kind+name only; using refKey here would
				// silently fail to match any namespaced resource.
				suppressedRefs[suppressionKey(ref)] = true
			}
			// When the remediation pins the root cause to a single missing
			// namespace, every resource targeting that namespace is just a
			// downstream symptom — suppress them in the per-resource pass.
			if parsed.Remediation != nil && parsed.Remediation.Kind == RemediationCreateNamespace {
				suppressedNamespaces[parsed.Remediation.Target] = true
			}
			out = append(out, issue)
		} else if phase == "Running" {
			out = append(out, Issue{Severity: SeverityInfo, Scope: ScopeOperation, Reason: "Running", Message: "A sync operation is currently running.", Action: "Wait for completion or terminate if it is stuck."})
		} else if stuck := detectStuckDriftLoop(root); stuck != nil {
			// Stuck-drift-loop detector: the user's "this is stuck forever and
			// nothing tells me why" case. Argo reports the last sync as
			// Succeeded but the app is still OutOfSync, auto-sync is on, and
			// reconciledAt is recent. Something is mutating the resource
			// after each apply (controller defaults, conversion webhook,
			// another operator). Without this issue, the only signal is the
			// OutOfSync badge — which the user has been staring at for hours.
			out = append(out, *stuck)
		} else if drift := detectManualDriftWithoutAutoSync(root); drift != nil {
			// Manual drift without auto-sync: app is OutOfSync but auto-sync
			// is off, so nothing will reconcile until a human clicks Sync.
			// Common operator confusion: "I see drift, why isn't anything
			// happening?" Answer: nothing is *supposed* to happen
			// automatically.
			out = append(out, *drift)
		}
		// Argo Application status.conditions are how the controller signals
		// app-level problems that aren't tied to a specific operation
		// (ComparisonError, InvalidSpecError, OrphanedResourceWarning, …) —
		// the answers to "why is this app broken" when no operation has run.
		// When an operation HAS failed, SyncError is a parallel encoding of
		// the same message we already render in the failure card; skip it.
		for _, ci := range argoApplicationConditions(root) {
			if operationFailed && ci.Reason == argoSyncErrorConditionType {
				continue
			}
			out = append(out, ci)
		}
		// buildIssues uses change data only for resource-level issue
		// detection — the per-resource diff/events live on the Change
		// objects emitted by buildChanges. Pass nil resolver here to skip
		// the (unused) drift computation in this code path.
		for _, change := range argoResourceChanges(root, nil) {
			// Suppress a resource issue when its kind/name match a resource
			// already named in the operation failure — same root cause, no
			// value in showing it twice. Also suppress every resource in a
			// namespace named by a structured remediation (the missing
			// namespace IS the cause; per-resource Missing rows are noise).
			if suppressedRefs[suppressionKey(change.Ref)] {
				continue
			}
			if change.Ref.Namespace != "" && suppressedNamespaces[change.Ref.Namespace] {
				continue
			}
			if change.Health == "Degraded" || change.Health == "Missing" {
				out = append(out, Issue{Severity: SeverityCritical, Scope: ScopeResource, Reason: change.Health, Message: fmt.Sprintf("%s %s is %s", change.Ref.Kind, change.Ref.Name, change.Health), Refs: []Ref{change.Ref}, Action: "Open the resource drawer for events, logs, and YAML."})
			} else if change.Sync == "OutOfSync" {
				out = append(out, Issue{Severity: SeverityWarning, Scope: ScopeResource, Reason: "OutOfSync", Message: fmt.Sprintf("%s %s is out of sync", change.Ref.Kind, change.Ref.Name), Refs: []Ref{change.Ref}, Action: "Review Changes or run sync."})
			}
		}
	} else {
		for _, c := range conditions(root) {
			if c.status == "False" && (c.typ == "Ready" || c.typ == "Healthy" || c.typ == "Released" || c.typ == "TestSuccess") {
				out = append(out, Issue{Severity: SeverityCritical, Scope: ScopeCondition, Reason: fallback(c.reason, c.typ), Message: fallback(c.message, c.typ+" is false"), Action: fluxActionForReason(c.reason)})
			}
			if c.status == "True" && c.typ == "Stalled" {
				out = append(out, Issue{Severity: SeverityCritical, Scope: ScopeCondition, Reason: fallback(c.reason, "Stalled"), Message: fallback(c.message, "Reconciliation is stalled"), Action: fluxActionForReason(c.reason)})
			}
			if c.status == "True" && c.typ == "Reconciling" {
				out = append(out, Issue{Severity: SeverityInfo, Scope: ScopeCondition, Reason: fallback(c.reason, "Reconciling"), Message: fallback(c.message, "Reconciliation is in progress")})
			}
		}
	}
	if resourceTree != nil && resourceTree.Summary.Degraded > 0 && len(out) == 0 {
		out = append(out, Issue{Severity: SeverityWarning, Scope: ScopeTree, Reason: "DegradedResources", Message: fmt.Sprintf("%d managed resources are degraded", resourceTree.Summary.Degraded), Action: "Use the graph or Resources tab to inspect affected resources."})
	}
	// Dedup by (scope, reason, message) — Flux carries the same failure
	// reason in multiple status.conditions slots (Released=False *and*
	// Reconciling=False both report "UpgradeFailed" with the identical
	// message), which produced visible duplicate rows in the UI panel.
	// Argo similarly can repeat a condition across reconcile attempts.
	// Keep the first occurrence (which preserves whatever ordering the
	// detector chain already chose).
	out = dedupeIssues(out)
	sort.SliceStable(out, func(i, j int) bool { return severityRank(out[i].Severity) < severityRank(out[j].Severity) })
	return out
}

// dedupeIssues removes Issues that share the same (scope, reason, message,
// firstRef) tuple as an earlier entry. Keeps the first occurrence and
// discards later duplicates, preserving detector-chain ordering.
//
// Including the first Ref's Kind+Name in the key keeps per-resource
// issues distinct (two pods both ImagePullBackOff with the same message
// but different refs stay as two issues). Issues without refs
// (operation/condition/lifecycle scopes) collapse correctly because
// their ref-suffix is "" identically.
func dedupeIssues(in []Issue) []Issue {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Issue, 0, len(in))
	for _, i := range in {
		// Refs differentiate per-resource issues; include the first ref's
		// kind+name in the dedup key so a class of resource-level issues
		// isn't silently collapsed into one. Empty refs (operation/
		// condition/lifecycle scopes) collapse correctly because their
		// ref-suffix is "" identically.
		var refKey string
		if len(i.Refs) > 0 {
			refKey = i.Refs[0].Kind + "/" + i.Refs[0].Name
		}
		k := string(i.Scope) + "|" + i.Reason + "|" + i.Message + "|" + refKey
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, i)
	}
	return out
}
func buildChanges(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string, live Resolver) []Change {
	if tool == "argocd" {
		return argoResourceChanges(root, live)
	}
	if resourceTree == nil {
		return nil
	}
	var out []Change
	for _, n := range resourceTree.Nodes {
		if n.Role == gitopstree.RoleRoot || n.Role == gitopstree.RoleGroup {
			continue
		}
		category := categorizeFluxChange(n.Sync, n.Health)
		partial := true
		note := "Flux inventory confirms this resource is managed; desired manifest content is not available in Radar yet."
		out = append(out, Change{
			Ref:         refFromTree(n.Ref),
			Category:    category,
			Sync:        n.Sync,
			Health:      firstNonEmpty(n.Health, n.TopologyStatus),
			HasLive:     n.Ref.UID != "",
			HasDesired:  false,
			Partial:     partial,
			PartialNote: note,
		})
	}
	sortChanges(out)
	return out
}

// argoIgnoreDifferences holds a parsed view of the Application's
// spec.ignoreDifferences[]. Each entry scopes a set of JSON pointers to a
// (group, kind, optional name, optional namespace) — the same shape Argo's
// own diff pipeline reads. pointersFor returns the pointer list applicable
// to a given resource ref, with broader rules (no name/namespace) and
// narrower rules (named resource) both contributing.
type argoIgnoreDifferences struct {
	rules []argoIgnoreRule
}

type argoIgnoreRule struct {
	group, kind, name, namespace string
	pointers                     []string
}

func parseArgoIgnoreDifferences(root *unstructured.Unstructured) argoIgnoreDifferences {
	raw, _, _ := unstructured.NestedSlice(root.Object, "spec", "ignoreDifferences")
	out := argoIgnoreDifferences{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ptrs, _, _ := unstructured.NestedStringSlice(m, "jsonPointers")
		// jqPathExpressions are a separate Argo feature (more powerful but
		// requires a JQ engine to evaluate). Skip them here — JSONPointer
		// covers the majority of "ignore this field" rules. Future work.
		if len(ptrs) == 0 {
			// Affected rules disappear silently otherwise; surface a one-
			// shot warning so operators can correlate "Radar shows drift
			// Argo's UI suppresses" with the gap.
			jq, _, _ := unstructured.NestedStringSlice(m, "jqPathExpressions")
			if len(jq) > 0 {
				logJQIgnoreOnce(gitops.StringValue(m["group"]), gitops.StringValue(m["kind"]))
			}
			continue
		}
		out.rules = append(out.rules, argoIgnoreRule{
			group:     gitops.StringValue(m["group"]),
			kind:      gitops.StringValue(m["kind"]),
			name:      gitops.StringValue(m["name"]),
			namespace: gitops.StringValue(m["namespace"]),
			pointers:  ptrs,
		})
	}
	return out
}

func (a argoIgnoreDifferences) pointersFor(ref Ref) []string {
	if len(a.rules) == 0 {
		return nil
	}
	var out []string
	for _, r := range a.rules {
		if r.matches(ref) {
			out = append(out, r.pointers...)
		}
	}
	return out
}

// matches reports whether the rule applies to the given resource ref.
// All four scope fields (group/kind/name/namespace) treat empty-string as
// a wildcard — Argo's own scoping semantics. A rule that omits `group` and
// `kind` matches every resource; a rule that names `name` narrows to that
// single resource. Matching upstream behavior matters because operators
// copy Argo Application manifests verbatim and expect the same effect.
func (r argoIgnoreRule) matches(ref Ref) bool {
	if r.group != "" && r.group != ref.Group {
		return false
	}
	if r.kind != "" && r.kind != ref.Kind {
		return false
	}
	if r.name != "" && r.name != ref.Name {
		return false
	}
	if r.namespace != "" && r.namespace != ref.Namespace {
		return false
	}
	return true
}

func argoResourceChanges(root *unstructured.Unstructured, resolver Resolver) []Change {
	raw, _, _ := unstructured.NestedSlice(root.Object, "status", "resources")
	// Pre-parse the Application's spec.ignoreDifferences so each resource's
	// drift computation can filter out operator-declared exemptions before
	// they reach the UI.
	ignoreRules := parseArgoIgnoreDifferences(root)
	out := make([]Change, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref := Ref{
			Group:     gitops.StringValue(m["group"]),
			Kind:      gitops.StringValue(m["kind"]),
			Namespace: gitops.StringValue(m["namespace"]),
			Name:      gitops.StringValue(m["name"]),
		}
		if ref.Kind == "" || ref.Name == "" {
			continue
		}
		health := ""
		if hm, ok := m["health"].(map[string]any); ok {
			health = gitops.StringValue(hm["status"])
		}
		sync := gitops.StringValue(m["status"])
		category := categorizeArgoChange(sync, health)
		// Argo records per-resource sync failures under a syncResult sibling
		// (set during/after a failed sync attempt). Surface the message as
		// an error unless status explicitly marks success ("Synced"/"Pruned").
		// Empty status counts as "unknown — show the message" because Argo
		// can write a pre-apply failure message before stamping a status.
		syncError := ""
		hookPhase := ""
		if sr, ok := m["syncResult"].(map[string]any); ok {
			status := gitops.StringValue(sr["status"])
			if status != "Synced" && status != "Pruned" {
				syncError = gitops.StringValue(sr["message"])
			}
			hookPhase = gitops.StringValue(sr["hookPhase"])
		}
		change := Change{
			Ref:         ref,
			Category:    category,
			Sync:        sync,
			Health:      health,
			Message:     nestedMessage(m["health"]),
			SyncError:   syncError,
			HookPhase:   hookPhase,
			HasDesired:  false,
			HasLive:     true,
			Partial:     true,
			PartialNote: "Argo reports resource status here; desired manifest content is not available in Radar yet.",
		}
		// Enrich from live cluster state when a resolver is wired. The
		// drift diff turns the bare "OutOfSync" badge into a concrete
		// list of differing fields; recent events surface the underlying
		// "why is this stuck" cause for things like ImagePullBackOff or
		// FailedScheduling that the GitOps CR never sees.
		if resolver != nil {
			if live := resolver.GetLive(ref.Group, ref.Kind, ref.Namespace, ref.Name); live != nil {
				if drift := computeDriftFromLastApplied(live, ignoreRules.pointersFor(ref)); drift != nil {
					change.Drift = drift
				}
			}
			if events := resolver.RecentEvents(ref.Group, ref.Kind, ref.Namespace, ref.Name); len(events) > 0 {
				change.RecentEvents = events
			}
		}
		out = append(out, change)
	}
	sortChanges(out)
	return out
}

func buildPlan(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string) []PlanItem {
	if resourceTree == nil {
		return nil
	}
	items := make([]PlanItem, 0, len(resourceTree.Nodes))
	for _, n := range resourceTree.Nodes {
		if n.Role == gitopstree.RoleGroup {
			continue
		}
		// The root node is the GitOps CR itself (Application / Kustomization /
		// HelmRelease). Including it in the plan reads as "the controller will
		// sync itself," which is an Argo internal that confuses operators. The
		// plan is about what gets applied to the cluster as a result of sync;
		// the root is the trigger, not a planned change.
		if n.Role == gitopstree.RoleRoot {
			continue
		}
		item := PlanItem{
			Ref:          refFromTree(n.Ref),
			Order:        len(items) + 1,
			Hook:         stringData(n.Data, "hook"),
			Relationship: stripUnknown(stringData(n.Data, "relationship")),
			// Strip "Unknown" tokens before joining — Sync/Health/TopologyStatus
			// each default to "Unknown" when the controller hasn't reported,
			// so a raw join produces noise like "OutOfSync · Unknown · unknown"
			// that reads as broken in the UI chip.
			Status: joinNonEmpty(stripUnknown(n.Sync), stripUnknown(n.Health), stripUnknown(n.TopologyStatus)),
		}
		if wave, ok := parseWave(stringData(n.Data, "syncWave")); ok {
			item.Wave = wave
			item.WaveSet = true
		}
		item.Phase = firstNonEmpty(stringData(n.Data, "syncPhase"), phaseFromHook(item.Hook))
		if tool == "fluxcd" && item.Relationship == "" {
			if n.Role == gitopstree.RoleRoot {
				item.Relationship = "root"
			} else {
				item.Relationship = "managed"
			}
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if tool == "argocd" {
			if phaseRank(items[i].Phase) != phaseRank(items[j].Phase) {
				return phaseRank(items[i].Phase) < phaseRank(items[j].Phase)
			}
			if items[i].Wave != items[j].Wave {
				return items[i].Wave < items[j].Wave
			}
		}
		if kindRank(items[i].Ref.Kind) != kindRank(items[j].Ref.Kind) {
			return kindRank(items[i].Ref.Kind) < kindRank(items[j].Ref.Kind)
		}
		return items[i].Ref.Name < items[j].Ref.Name
	})
	for i := range items {
		items[i].Order = i + 1
	}
	return items
}

func buildHistory(root *unstructured.Unstructured, tool string) []HistoryItem {
	if tool == "argocd" {
		raw, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
		out := make([]HistoryItem, 0, len(raw)+1)
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id := ""
			switch v := m["id"].(type) {
			case int64:
				id = strconv.FormatInt(v, 10)
			case float64:
				// JSON numbers decode as float64; client-go's structured
				// deep-copy preserves int64 — both branches are reachable.
				id = strconv.Itoa(int(v))
			default:
				if m["id"] != nil {
					log.Printf("[gitops/insights] history entry %s/%s has unexpected id type %T (%v); rollback for this entry will be unavailable", root.GetNamespace(), root.GetName(), m["id"], m["id"])
				}
			}
			source := ""
			if sm, ok := m["source"].(map[string]any); ok {
				source = joinNonEmpty(gitops.StringValue(sm["repoURL"]), gitops.StringValue(sm["path"]), gitops.StringValue(sm["chart"]))
			}
			// initiatedBy carries who triggered the sync. Username is set for
			// human/api triggers; automated is a *bool*, not a string — Argo
			// flips it true when the controller's auto-sync fires. We coerce
			// to "automated" so the UI doesn't show empty initiator on
			// controller-triggered history rows (the common case).
			initiatedBy := ""
			if ib, ok := m["initiatedBy"].(map[string]any); ok {
				initiatedBy = gitops.StringValue(ib["username"])
				if initiatedBy == "" {
					if auto, ok := ib["automated"].(bool); ok && auto {
						initiatedBy = "automated"
					}
				}
			}
			out = append(out, HistoryItem{ID: id, Revision: gitops.StringValue(m["revision"]), DeployedAt: gitops.StringValue(m["deployedAt"]), Source: source, InitiatedBy: initiatedBy})
		}
		if op, ok, _ := unstructured.NestedMap(root.Object, "status", "operationState"); ok {
			initiatedBy := ""
			if opMap, ok := op["operation"].(map[string]any); ok {
				if ib, ok := opMap["initiatedBy"].(map[string]any); ok {
					initiatedBy = gitops.StringValue(ib["username"])
					if initiatedBy == "" {
						if auto, ok := ib["automated"].(bool); ok && auto {
							initiatedBy = "automated"
						}
					}
				}
			}
			// finishedAt is empty while a sync is in flight. Fall back to
			// startedAt so the running entry still has a timestamp; without
			// this, the descending sort below pushed the in-flight row to
			// the *bottom* of history, hiding the most operationally
			// relevant entry from the user.
			deployedAt := gitops.StringValue(op["finishedAt"])
			if deployedAt == "" {
				deployedAt = gitops.StringValue(op["startedAt"])
			}
			out = append(out, HistoryItem{
				Phase:       gitops.StringValue(op["phase"]),
				Message:     gitops.StringValue(op["message"]),
				DeployedAt:  deployedAt,
				Revision:    nestedString(op, "syncResult", "revision"),
				InitiatedBy: initiatedBy,
			})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].DeployedAt > out[j].DeployedAt })
		return out
	}
	var out []HistoryItem
	// Dedupe by (message, reason). Flux HelmReleases routinely carry the
	// same message on multiple conditions (Released=True and Ready=True both
	// report "Helm install succeeded for release X with chart Y@Z") with
	// timestamps a second apart, so timestamp can't be part of the key.
	// Same message + same reason = one logical event surfaced redundantly.
	seen := make(map[string]struct{})
	revision := firstNonEmpty(nestedString(root.Object, "status", "lastAppliedRevision"), nestedString(root.Object, "status", "lastAttemptedRevision"))
	for _, c := range conditions(root) {
		key := c.message + "|" + c.reason
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, HistoryItem{
			ID:         c.typ,
			Phase:      fluxPhaseLabel(c.status, c.reason),
			Message:    c.message,
			DeployedAt: c.lastTransitionTime,
			Revision:   revision,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DeployedAt > out[j].DeployedAt })
	return out
}

func buildCapabilities(root *unstructured.Unstructured, tool string) Capabilities {
	if tool == "argocd" {
		hasHistory := false
		raw, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok && gitops.StringValue(m["revision"]) != "" {
				hasHistory = true
				break
			}
		}
		return Capabilities{Sync: true, Refresh: true, Terminate: true, Suspend: true, Resume: true, SelectiveSync: true, Rollback: hasHistory, Warnings: []string{"Selective sync skips hooks and is not equivalent to a full application sync."}}
	}
	syncWithSource := root.GetKind() == "Kustomization" || root.GetKind() == "HelmRelease"
	return Capabilities{Sync: true, Suspend: true, Resume: true, SyncWithSource: syncWithSource, UnsupportedReason: "Flux reconciles through source/workload controllers; per-resource selective sync and generic rollback are not exposed by Radar."}
}

type condition struct {
	typ                string
	status             string
	reason             string
	message            string
	lastTransitionTime string
}

func conditions(root *unstructured.Unstructured) []condition {
	raw, _, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
	out := make([]condition, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, condition{
			typ:                gitops.StringValue(m["type"]),
			status:             gitops.StringValue(m["status"]),
			reason:             gitops.StringValue(m["reason"]),
			message:            gitops.StringValue(m["message"]),
			lastTransitionTime: gitops.StringValue(m["lastTransitionTime"]),
		})
	}
	return out
}

type fluxState struct {
	sync   string
	health string
}

func fluxStatus(root *unstructured.Unstructured) fluxState {
	if suspended, _, _ := unstructured.NestedBool(root.Object, "spec", "suspend"); suspended {
		return fluxState{sync: "Unknown", health: "Suspended"}
	}
	ready := ""
	reconciling := false
	stalled := false
	for _, c := range conditions(root) {
		if c.typ == "Ready" {
			ready = c.status
		}
		if c.typ == "Reconciling" && c.status == "True" {
			reconciling = true
		}
		if c.typ == "Stalled" && c.status == "True" {
			stalled = true
		}
	}
	if reconciling {
		return fluxState{sync: "Reconciling", health: "Progressing"}
	}
	if stalled {
		return fluxState{sync: "OutOfSync", health: "Degraded"}
	}
	if ready == "True" {
		return fluxState{sync: "Synced", health: "Healthy"}
	}
	if ready == "False" {
		return fluxState{sync: "OutOfSync", health: "Degraded"}
	}
	return fluxState{sync: "Unknown", health: "Unknown"}
}

func nestedRef(root *unstructured.Unstructured, fields ...string) (Ref, bool) {
	m, ok, _ := unstructured.NestedMap(root.Object, fields...)
	if !ok {
		return Ref{}, false
	}
	name := gitops.StringValue(m["name"])
	kind := gitops.StringValue(m["kind"])
	if name == "" || kind == "" {
		return Ref{}, false
	}
	return Ref{Group: gitops.GroupFromAPIVersion(gitops.StringValue(m["apiVersion"])), Kind: kind, Namespace: firstNonEmpty(gitops.StringValue(m["namespace"]), root.GetNamespace()), Name: name}, true
}

func refFromTree(ref gitopstree.ResourceRef) Ref {
	return Ref{Group: ref.Group, Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

func sortChanges(out []Change) {
	sort.SliceStable(out, func(i, j int) bool {
		if changeRank(out[i].Category) != changeRank(out[j].Category) {
			return changeRank(out[i].Category) < changeRank(out[j].Category)
		}
		if out[i].Ref.Kind != out[j].Ref.Kind {
			return out[i].Ref.Kind < out[j].Ref.Kind
		}
		return out[i].Ref.Name < out[j].Ref.Name
	})
}

// unknownPairLogged dedupes the "unknown vocabulary" warnings emitted by
// categorizeArgoChange / categorizeFluxChange. The GitOps detail page polls
// every 2s while an op runs, with one call per managed resource — without
// dedup, a single non-canonical health value (e.g. a controller emitting
// `OK` instead of `Healthy`) would flood the log at hundreds of lines/min.
// The set grows monotonically, but the vocabulary is closed and small, so
// memory use is bounded by the cluster's actual non-canonical value count.
var unknownPairLogged sync.Map

func logUnknownPairOnce(tool, sync, health string) {
	key := tool + "|" + sync + "|" + health
	if _, loaded := unknownPairLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[gitops/insights] unknown %s sync/health combination sync=%q health=%q — falling back to Unknown", tool, sync, health)
}

// categorizeArgoChange maps Argo's per-resource sync + health into a Category
// constant. Inputs come from status.resources[].status (sync) and
// status.resources[].health.status — both vocabularies are documented and
// stable, so unknown values are a real bug and are logged once per
// (sync, health) pair via logUnknownPairOnce.
func categorizeArgoChange(sync, health string) Category {
	// Health takes precedence for the failure tiers — a resource in Sync
	// but degraded is more important to surface than its sync state.
	switch health {
	case "Degraded":
		return CategoryDegraded
	case "Missing":
		return CategoryMissing
	case "Progressing":
		return CategoryProgressing
	case "Suspended":
		return CategorySuspended
	}
	switch sync {
	case "Synced":
		return CategorySynced
	case "OutOfSync":
		return CategoryOutOfSync
	case "Pruned":
		return CategoryPruned
	case "Unknown", "":
		return CategoryUnknown
	}
	logUnknownPairOnce("Argo", sync, health)
	return CategoryUnknown
}

// categorizeFluxChange does the same for Flux's tree-derived sync/health.
// Inputs are the gitopstree.Node fields rather than raw Flux conditions.
func categorizeFluxChange(sync, health string) Category {
	switch health {
	case "Degraded":
		return CategoryDegraded
	case "Missing":
		return CategoryMissing
	case "Progressing":
		return CategoryProgressing
	case "Suspended":
		return CategorySuspended
	}
	if sync == "OutOfSync" {
		return CategoryOutOfSync
	}
	// Flux managed resources without a degraded health are reported as
	// Synced — they pass the inventory check; per-field drift would need
	// the desired-manifest path that doesn't exist yet.
	return CategorySynced
}

func changeRank(category Category) int {
	switch category {
	case CategoryDegraded, CategoryMissing:
		return 0
	case CategoryOutOfSync:
		return 1
	case CategoryProgressing, CategoryReconciling:
		return 2
	case CategoryUnknown:
		return 3
	case CategorySynced, CategoryPruned, CategoryHook, CategorySuspended:
		return 4
	default:
		// Unknown Category values surface here only via the categorize*
		// helpers' fallback path, which already logs. Sort them at the end.
		return 5
	}
}

// severityRank orders Issues for the buildIssues output sort.
// Critical → alert → warning → info → unknown. Matches the project-wide
// severity vocabulary in CLAUDE.md; the alert tier is the intermediate
// between degraded/amber and unhealthy/red.
func severityRank(severity Severity) int {
	switch severity {
	case SeverityCritical:
		return 0
	case SeverityAlert:
		return 1
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 3
	default:
		return 4
	}
}

func phaseRank(phase string) int {
	switch phase {
	case "PreSync":
		return 0
	case "", "Sync":
		return 1
	case "PostSync":
		return 2
	case "SyncFail":
		return 3
	case "PostDelete":
		return 4
	default:
		return 5
	}
}

func kindRank(kind string) int {
	switch kind {
	case "Namespace":
		return 0
	case "CustomResourceDefinition":
		return 1
	case "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding":
		return 2
	case "Secret", "ConfigMap":
		return 3
	case "Service", "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
		return 4
	default:
		return 5
	}
}

func phaseFromHook(hook string) string {
	if hook == "" || hook == "Skip" {
		return ""
	}
	return hook
}

func fluxActionForReason(reason string) string {
	switch reason {
	case "DependencyNotReady":
		return "Inspect the dependency chain in the graph."
	case "ArtifactFailed":
		return "Inspect the Flux source and reconcile it."
	case "BuildFailed":
		return "Check the source path and rendered manifests."
	case "HealthCheckFailed":
		return "Open unhealthy managed resources for events and status."
	case "InstallFailed", "UpgradeFailed", "TestFailed":
		return "Inspect HelmRelease conditions and controller events."
	default:
		return "Review conditions and reconcile after fixing the source of failure."
	}
}

func parseWave(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	i, err := strconv.Atoi(value)
	return i, err == nil
}

func newestConditionTime(root *unstructured.Unstructured) string {
	newest := ""
	for _, c := range conditions(root) {
		if c.lastTransitionTime > newest {
			newest = c.lastTransitionTime
		}
	}
	return newest
}

func stringData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	return gitops.StringValue(data[key])
}

func nestedString(v any, fields ...string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for i, field := range fields {
		if i == len(fields)-1 {
			return gitops.StringValue(m[field])
		}
		m, ok = m[field].(map[string]any)
		if !ok {
			return ""
		}
	}
	return ""
}

func nestedMessage(v any) string {
	if m, ok := v.(map[string]any); ok {
		return gitops.StringValue(m["message"])
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fallback(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// stripUnknown returns "" for strings that carry no signal (empty or
// case-insensitive "unknown"), so callers can use joinNonEmpty without
// dragging "Unknown" placeholders into compound display strings.
func stripUnknown(value string) string {
	if strings.EqualFold(value, "unknown") {
		return ""
	}
	return value
}

func joinNonEmpty(values ...string) string {
	var parts []string
	for _, value := range values {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

// fluxPhaseLabel collapses Flux's per-condition (status, reason) pair into a
// single outcome word — the value goes into HistoryItem.Phase, which the
// frontend renders as a colored chip on each history row. Without this, the
// raw join surfaces internal encodings like "True · installsucceeded" to the
// user; the cleaned label reads as the actual outcome ("Succeeded", "Failed",
// "Reconciling") and matches the vocabulary the FE's gitopsToSeverity already
// understands.
func fluxPhaseLabel(status, reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "succeed"):
		return "Succeeded"
	case strings.Contains(r, "fail"):
		return "Failed"
	case strings.Contains(r, "error"):
		return "Failed"
	case strings.Contains(r, "progress"), strings.Contains(r, "reconcil"):
		return "Reconciling"
	case strings.Contains(r, "suspend"):
		return "Suspended"
	}
	// Unknown reason — fall back to the condition status alone. True/False are
	// less informative than a named outcome but better than joining both into
	// a hybrid that confuses readers.
	if reason != "" {
		return reason
	}
	return status
}

// refKey is the key used to dedup issue refs across the operation+resource
// pass. Group is intentionally omitted — the operation message rarely
// includes it, and kind+name+namespace is enough disambiguation in practice.
func refKey(r Ref) string {
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// suppressionKey matches a resource by kind+name only — Argo's operation
// failure message names the affected resource without a namespace, so a
// suppression set built from operation messages must compare on the same
// projection. Two same-named resources of the same kind in different
// namespaces would dedup with each other under this key; in practice
// operations only affect one of them at a time and the cost of over-dedup
// (showing one too few rows briefly) is less than the cost of under-dedup
// (every namespaced resource fails the namespace-aware refKey match and the
// duplicate rows persist forever — the exact regression this exists to
// prevent).
func suppressionKey(r Ref) string {
	return r.Kind + "/" + r.Name
}

// parsedFailure carries fields extracted from an Argo operationState.message.
// Unparsed parts of the original message remain available to the UI as the
// raw error — the parser only adds structure, never replaces or hides text.
type parsedFailure struct {
	Cause        string // plain-English root cause; empty if unrecognized
	AffectedKind string
	AffectedName string
	RetryCount   int
	Stuck        bool
	// Remediation, when set, exposes a structured fix the UI can render as
	// a contextual button. Only populated for patterns where the next step
	// is unambiguous and safe (e.g. create a missing namespace).
	Remediation *Remediation
}

// stuckRetryThreshold is the retry count at which we stop calling a failure
// "transient" and start calling it stuck. Argo retries with backoff up to 5
// times by default; reaching that ceiling means the controller has given up
// hoping for self-recovery, which is exactly when the user needs the
// stronger visual.
const stuckRetryThreshold = 5

// Capture group: <Kind>(.<group>...)? "<name>". Examples this matches:
//
//	CustomResourceDefinition.apiextensions.k8s.io "scaledjobs.keda.sh"
//	Deployment.apps "billing"
//	Service "billing"
//
// We don't need the group; the leading kind + quoted name is what users read.
var argoAffectedRefRE = regexp.MustCompile(`([A-Z][A-Za-z0-9]+)(?:\.[A-Za-z0-9.\-]+)?\s+"([^"]+)"`)

// "(retried N times)" suffix Argo appends when its retry policy has fired.
var argoRetryRE = regexp.MustCompile(`\(retried (\d+) times?\)`)

// `namespaces "<name>" not found` — fires when the Application targets a
// namespace that doesn't exist and CreateNamespace=false. The most common
// "why won't this sync" case for new environments. Captured separately so
// the parser can populate a structured Remediation (Create namespace button)
// rather than relying on the generic affected-ref regex.
var argoMissingNamespaceRE = regexp.MustCompile(`namespaces "([^"]+)" not found`)

// Pattern table: ordered list of (matcher, plain-English cause). First match
// wins. Keep patterns specific — generic catch-alls would mask more useful
// matches. Cases below cover the failure modes operators see most: validation
// limits, admission rejection, RBAC, conflicts, registration, connectivity.
var argoErrorPatterns = []struct {
	match *regexp.Regexp
	cause string
}{
	// Missing namespace pattern: keep this first so a more specific
	// match wins over the generic "not found" message.
	{regexp.MustCompile(`namespaces "[^"]+" not found`), "The destination namespace does not exist. Create it, or enable CreateNamespace=true in the Application's syncOptions so Argo creates it on sync."},
	{regexp.MustCompile(`metadata\.annotations:\s*Too long`), "An annotation on the desired manifest exceeds Kubernetes' 256 KB metadata limit. Switch to server-side apply (Sync options → Server-side apply) or shrink the offending annotation."},
	{regexp.MustCompile(`metadata\.labels:\s*Too long`), "Labels exceed Kubernetes' 64-character-per-key limit. Shorten label keys or values."},
	// Hook patterns come BEFORE webhook patterns: Argo's hook failure
	// messages can include the substring "webhook" coincidentally (e.g.
	// "validating-webhook-hook"), and the more-specific hook framing is
	// what the operator needs first.
	{regexp.MustCompile(`(?i)\b(presync|postsync|sync(?:fail)?|postdelete|skipdryrun)\b.*?(?:hook|phase).*?(?:failed|error)`), "A sync hook failed. Inspect the hook resource (Job/Pod) for events and logs to see why it errored."},
	{regexp.MustCompile(`(?i)hook .*? failed`), "A sync hook failed. Open Activity for the hook's exit reason; the failed hook resource itself usually has events that explain it."},
	{regexp.MustCompile(`admission webhook ".*?" denied the request`), "An admission webhook rejected the apply. Check the webhook's policy or its target server."},
	{regexp.MustCompile(`is forbidden:\s*User`), "RBAC denied this operation. The Argo controller's ServiceAccount lacks the required permissions."},
	{regexp.MustCompile(`already exists`), "A resource with this name already exists in the cluster. It may have been created outside of GitOps or owned by a different application."},
	{regexp.MustCompile(`no matches for kind`), "The CustomResourceDefinition for this kind isn't registered in the cluster. Install or wait for the operator that owns this CRD."},
	{regexp.MustCompile(`(?i)dial tcp.*(?:i/o timeout|connection refused|no route to host)`), "Cluster unreachable from the Argo controller. Check API server connectivity and network policies."},
	{regexp.MustCompile(`field is immutable`), "Tried to change a field Kubernetes treats as immutable. Recreate the resource (delete + reapply) or revert the change."},
	{regexp.MustCompile(`unable to recognize`), "The manifest references an API version the cluster doesn't recognize. Check apiVersion against the installed CRDs."},
	{regexp.MustCompile(`Operation cannot be fulfilled.*the object has been modified`), "The resource was modified concurrently between Argo's read and write. The next sync attempt should resolve it; investigate if it persists."},
}

func parseArgoOperationError(msg string) parsedFailure {
	if msg == "" {
		return parsedFailure{}
	}
	out := parsedFailure{}
	for _, p := range argoErrorPatterns {
		if p.match.MatchString(msg) {
			out.Cause = p.cause
			break
		}
	}
	if m := argoAffectedRefRE.FindStringSubmatch(msg); len(m) == 3 {
		out.AffectedKind = m[1]
		out.AffectedName = m[2]
	}
	if m := argoRetryRE.FindStringSubmatch(msg); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out.RetryCount = n
			out.Stuck = n >= stuckRetryThreshold
		}
	}
	// Structured remediation: only the missing-namespace pattern offers a
	// one-click fix in v1. Other patterns surface diagnosis-only via Cause.
	if m := argoMissingNamespaceRE.FindStringSubmatch(msg); len(m) == 2 {
		out.Remediation = NewCreateNamespaceRemediation(m[1], "Creates the missing namespace and re-triggers reconciliation.")
	}
	// Telemetry: when nothing matched (no Cause, no AffectedRef), log once
	// so operators can grep server logs for "operation errors that escaped
	// the recognizer" and tune the pattern table. The dedup is necessary
	// because the GitOps detail page polls every 2s during a running op —
	// a single unrecognized failure would otherwise spam the log.
	if out.Cause == "" && out.AffectedKind == "" {
		logUnrecognizedOpError(msg)
	}
	return out
}

var unrecognizedOpErrorLogged sync.Map

// jqIgnoreLogged deduplicates the "jq-only ignoreDifferences" warning so it
// fires once per (group, kind) over the process lifetime — Argo Application
// reconciles emit insights every 2s, and operators don't need the same
// warning a thousand times.
var jqIgnoreLogged sync.Map

func logJQIgnoreOnce(group, kind string) {
	key := group + "/" + kind
	if _, loaded := jqIgnoreLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[gitops/drift] ignoreDifferences rule for %s/%s uses jqPathExpressions which Radar doesn't evaluate; some drift entries Argo's UI suppresses may appear here", group, kind)
}

func logUnrecognizedOpError(msg string) {
	// Truncate at 200 chars: typical Argo error messages are short; outlier
	// stack-trace dumps would otherwise flood the log line.
	key := msg
	if len(key) > 200 {
		key = key[:200]
	}
	if _, loaded := unrecognizedOpErrorLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[gitops/insights] unrecognized argo operation error (no pattern matched): %q", key)
}

// detectPendingDeletion returns an Issue when the GitOps root resource has
// metadata.deletionTimestamp set. Tool-agnostic — applies to both Argo
// Applications and Flux Kustomizations/HelmReleases.
//
// Severity ramps with how long deletion has been pending:
//
//	<5min  → info     ("Deletion in progress, finalizers running")
//	5-30m  → warning  ("Deletion pending — finalizers blocking")
//	>30m   → alert    ("Deletion stuck — controller likely unhealthy")
//
// The 5min threshold gives healthy controllers time to run their finalizers.
// The 30min threshold is the boundary past which any reasonable cleanup
// would have completed; beyond it the finalizer's owning controller is
// almost certainly the problem (CrashLoopBackOff, network partition, etc).
//
// Why this matters: a resource with deletionTimestamp is queryable but
// any mutating action on it is futile (Sync/Reconcile/Rollback all fail
// or no-op because the resource is being torn down). Without this issue,
// the user sees Sync/Health badges that look "live" and clicks buttons
// that produce confusing K8s errors. Returns nil on a healthy resource —
// caller appends only on hit.
func detectPendingDeletion(root *unstructured.Unstructured, resolver Resolver) *Issue {
	dt := root.GetDeletionTimestamp()
	if dt == nil || dt.IsZero() {
		return nil
	}
	finalizers := root.GetFinalizers()
	age := time.Since(dt.Time)
	// Clock-skew guard: a deletionTimestamp meaningfully in the future
	// usually means Radar's local clock is behind the cluster API server
	// (or vice versa). Without this, the severity ramp would treat the
	// resource as "started 0s ago, info severity" and skip the controller-
	// health enrichment that gates on warning+. A 6h-stuck zombie with
	// even moderate clock skew would silently demote. Surface the skew
	// explicitly so the operator can investigate NTP rather than chase a
	// phantom info-tier issue.
	if age < -1*time.Minute {
		return &Issue{
			Severity: SeverityInfo,
			Scope:    ScopeLifecycle,
			Reason:   "Terminating",
			Message:  fmt.Sprintf("This resource is being deleted, but its deletionTimestamp (%s) is in the future relative to Radar — likely clock skew between Radar and the cluster API server.", dt.UTC().Format(time.RFC3339)),
			Action:   "Verify NTP / time sync. Once clocks agree, the lifecycle severity will reflect the true deletion age.",
		}
	}
	if age < 0 {
		age = 0
	}
	rel := formatAgeShort(age)

	severity := SeverityInfo
	reason := "Terminating"
	msg := fmt.Sprintf("This resource is being deleted (started %s ago).", rel)
	action := "Wait for finalizers to complete cleanup."
	// Inclusive thresholds (>=) — at exactly the boundary, escalate.
	// Two reasons: matches user intuition ("by 30 minutes this is stuck"),
	// and avoids flaky tests where time.Since drifts micro-seconds past
	// the cutoff between Now() and the comparison.
	//
	// keep in sync: pkg/audit/checks.go::stuckTerminatingThresholdWarning
	// (5min) and stuckTerminatingThresholdDanger (30min). The audit and
	// the per-resource Issue must agree on what counts as "stuck" so an
	// operator scanning both surfaces sees consistent severity.
	switch {
	case age >= 30*time.Minute:
		severity = SeverityAlert
		msg = fmt.Sprintf("Deletion has been pending for %s — finalizers are blocking cleanup.", rel)
		action = "The owning controller of the finalizer is likely unhealthy. Check its pod logs and DNS / network reachability."
	case age >= 5*time.Minute:
		severity = SeverityWarning
		msg = fmt.Sprintf("Deletion has been pending for %s.", rel)
		action = "Wait a few more minutes; if it remains stuck, investigate the finalizer's owning controller."
	}
	if len(finalizers) > 0 {
		msg += " Finalizers: " + strings.Join(finalizers, ", ") + "."
	}

	// Enrich with controller health when we can identify the finalizer
	// owner. The resolver may return "" — typical when the finalizer
	// isn't in our catalog or the controller's pods aren't in the
	// expected namespace — in which case we fall through to the
	// finalizer-list-only message above.
	//
	// We probe each finalizer in order and surface the *first* signal
	// that's actually informative. Stopping at the first hit (rather
	// than concatenating all) keeps the Cause text scannable; finalizers
	// after the first are usually controller-specific cleanup keys that
	// follow the lead controller's lifecycle.
	if resolver != nil && severity != SeverityInfo {
		// Only enrich at warning+; the <5min case isn't actionable yet, and a
		// controller-health line on a healthy controller would overstate urgency.
		for _, f := range finalizers {
			if status := resolver.FinalizerOwnerStatus(f, root); status != "" {
				return &Issue{
					Severity: severity,
					Scope:    ScopeLifecycle,
					Reason:   reason,
					Message:  msg,
					Action:   action,
					Cause:    status,
					Stuck:    severity == SeverityAlert,
				}
			}
		}
	}

	return &Issue{
		Severity: severity,
		Scope:    ScopeLifecycle,
		Reason:   reason,
		Message:  msg,
		Action:   action,
		Stuck:    severity == SeverityAlert,
	}
}

// formatAgeShort renders a duration as a compact relative string used in
// Issue messages: "3s", "12m", "4h", "21d".
//
// keep in sync: pkg/audit/checks.go::formatDurationShort (byte-identical)
// and web/src/components/gitops/GitOpsView.tsx::formatRelativeAge
// (TypeScript). Adding a new tier (e.g. "weeks") in one and not the
// others would let the lifecycle banner, the chip tooltip, the audit
// finding, and the fleet "Pending Nago" cell disagree on the same
// duration. Worth consolidating into a shared package eventually.
func formatAgeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// detectStuckDriftLoop emits a critical issue when an Argo Application is
// in the "applied successfully but still drifted" state — the case where
// the user stares at the OutOfSync badge for hours wondering why nothing
// happens. All four conditions must hold:
//
//   - sync status is OutOfSync (drift exists)
//   - last operation phase is Succeeded (the apply itself didn't error)
//   - auto-sync is enabled (so Argo *would* fix it if it could)
//   - reconciledAt is recent (controller is actively trying)
//
// Together these mean: Argo is doing exactly what it's configured to do,
// the apply call returns success, and yet the live state immediately
// reverts to differing from desired. The cause is almost always a
// controller or admission webhook mutating the resource after each apply
// — the "perpetual drift loop" pattern.
//
// Returns nil when conditions don't match — callers append only on hit.
func detectStuckDriftLoop(root *unstructured.Unstructured) *Issue {
	sync, _, _ := unstructured.NestedString(root.Object, "status", "sync", "status")
	if sync != "OutOfSync" {
		return nil
	}
	phase, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "phase")
	if phase != "Succeeded" {
		return nil
	}
	if describeArgoAutoSync(root) == "Manual" {
		return nil
	}
	reconciledAt, _, _ := unstructured.NestedString(root.Object, "status", "reconciledAt")
	if reconciledAt == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, reconciledAt)
	if err != nil {
		log.Printf("[gitops/insights] detectStuckDriftLoop: unparseable status.reconciledAt %q on %s/%s: %v", reconciledAt, root.GetNamespace(), root.GetName(), err)
		return nil
	}
	// 30-minute window: long enough to allow a legitimate slow-converging
	// resource (think CRDs that take many seconds per reconcile) to settle,
	// short enough that "haven't reconciled in an hour" doesn't trigger the
	// stuck banner — that case is a different problem (controller down).
	if time.Since(t) > 30*time.Minute {
		return nil
	}
	return &Issue{
		Severity: SeverityCritical,
		Scope:    ScopeOperation,
		Reason:   "StuckDriftLoop",
		Message:  "Sync succeeded but the application is still OutOfSync. A controller or admission webhook is likely mutating resources after each apply.",
		Cause:    "Auto-sync ran successfully and the controller's last reconcile is recent, but live state keeps diverging from Git. Common causes: a mutating admission webhook adds defaults Argo isn't told to ignore; a sibling controller (e.g. Karpenter, Istio, cert-manager) writes back into spec; the Git manifest uses a deprecated API schema that the conversion webhook rewrites.",
		Action:   "Open Changes to see the per-resource drift. Match the diff against your Git manifest, the resource's controller, and any mutating webhooks.",
		Stuck:    true,
	}
}

// detectManualDriftWithoutAutoSync emits a warning when an Argo Application
// is OutOfSync but auto-sync is disabled. The user otherwise has no signal
// that the drift won't resolve on its own — they wait, nothing happens,
// and they file the bug. This issue puts a clear "Click Sync" prompt at
// the top of the page so the next-step is obvious.
//
// Returns nil when conditions don't match — caller appends only on hit.
func detectManualDriftWithoutAutoSync(root *unstructured.Unstructured) *Issue {
	sync, _, _ := unstructured.NestedString(root.Object, "status", "sync", "status")
	if sync != "OutOfSync" {
		return nil
	}
	// Only fire when auto-sync is genuinely off. "Auto" with selfHeal off
	// is a separate (more nuanced) case — Argo would still apply on a
	// new Git revision, just not on manual drift; we leave that for a
	// future refinement rather than risk a false-positive banner here.
	if describeArgoAutoSync(root) != "Manual" {
		return nil
	}
	return &Issue{
		Severity: SeverityWarning,
		Scope:    ScopeOperation,
		Reason:   "ManualDrift",
		Message:  "Application is OutOfSync and auto-sync is disabled — nothing will reconcile until you click Sync.",
		Action:   "Open Changes to review the per-resource diff, then click Sync to apply. Enable auto-sync if you want this to fix itself going forward.",
	}
}

// argoApplicationConditions extracts Argo Application status.conditions[]
// into Issues. Argo conditions are how the controller signals app-level
// problems that aren't tied to a specific operation: ComparisonError when
// the source can't be loaded (bad repo, missing revision), InvalidSpecError
// when the Application spec itself is broken, OrphanedResourceWarning when
// children outside the inventory exist, etc.
//
// Severity mapping follows the convention in the Argo source: types ending
// in "Error" are critical; "Warning" types are warning; everything else is
// info. We elide condition types we don't recognize when the message is
// also empty — they're often controller-internal noise.
// argoSyncErrorConditionType is the literal Argo emits in its
// Application.status.conditions[].type when the last sync produced an error
// (equivalent to the failure already captured in operationState). buildIssues
// uses it to dedup the parallel-encoded SyncError condition with the operation
// failure issue. Pulled out as a constant so a future Argo rename (or our own
// re-extraction of the Reason field from the underlying type) is visible.
const argoSyncErrorConditionType = "SyncError"

func argoApplicationConditions(root *unstructured.Unstructured) []Issue {
	raw, _, _ := unstructured.NestedSlice(root.Object, "status", "conditions")
	if len(raw) == 0 {
		return nil
	}
	out := make([]Issue, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ := gitops.StringValue(m["type"])
		msg := gitops.StringValue(m["message"])
		if typ == "" && msg == "" {
			continue
		}
		severity := SeverityInfo
		switch {
		case strings.HasSuffix(typ, "Error"):
			severity = SeverityCritical
		case strings.HasSuffix(typ, "Warning"):
			severity = SeverityWarning
		}
		action := ""
		switch typ {
		case "ComparisonError":
			action = "Verify the repo URL, branch/tag, and credentials. Check argocd-repo-server logs for fetch errors."
		case "InvalidSpecError":
			action = "Fix the Application spec — check destination, source, and project references."
		case "OrphanedResourceWarning":
			action = "Resources exist in the destination namespace that aren't part of any application. Add to an app or label them as ignored."
		case "RepeatedResourceWarning":
			action = "The same resource is declared by multiple Argo Applications. Remove the duplicate declaration."
		case "ExcludedResourceWarning":
			action = "A managed resource is excluded by the Argo controller's resource.exclusions. Adjust controller config or remove the resource."
		case "SharedResourceWarning":
			action = "This resource is also tracked by another Application. Move it to a single owner."
		}
		out = append(out, Issue{
			Severity: severity,
			Scope:    ScopeCondition,
			Reason:   fallback(typ, "Condition"),
			Message:  fallback(msg, typ),
			Action:   action,
		})
	}
	return out
}
