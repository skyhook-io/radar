package insights

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
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
	// Partial signals that the response reflects only what the controller
	// reports — desired-manifest diffs (the gap between Git and live state)
	// are not computed here. Always true today; reserved for when desired
	// rendering lands. Frontend uses this to decide whether to show a
	// "partial view" hint via Summary.PartialReason.
	Partial bool `json:"partial"`
}

type Summary struct {
	Tool           string `json:"tool"`
	Kind           string `json:"kind"`
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Sync           string `json:"sync,omitempty"`
	Health         string `json:"health,omitempty"`
	OperationPhase string `json:"operationPhase,omitempty"`
	// OperationMessage is the latest operation status message from
	// status.operationState.message. Surfaced in the status strip so the
	// "what's happening right now" answer doesn't require switching to
	// the Activity tab.
	OperationMessage string `json:"operationMessage,omitempty"`
	Source           string `json:"source,omitempty"`
	TargetRevision   string `json:"targetRevision,omitempty"`
	LastRevision     string `json:"lastRevision,omitempty"`
	LastReconcile    string `json:"lastReconcile,omitempty"`
	PartialReason    string `json:"partialReason,omitempty"`
	// AutoSyncMode describes the current syncPolicy.automated configuration
	// in human-readable form. One of: "Manual", "Auto", "Auto · prune",
	// "Auto · self-heal", "Auto · prune · self-heal", or "" if not derivable.
	// Frontend renders as a small chip in the status strip.
	AutoSyncMode string `json:"autoSyncMode,omitempty"`
}

type Ref struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type Issue struct {
	Severity string `json:"severity"`
	Scope    string `json:"scope"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Refs     []Ref  `json:"refs,omitempty"`
	Action   string `json:"action,omitempty"`
	// Cause is a plain-English explanation of the root cause when the issue
	// matches a recognized error pattern (annotation too large, webhook
	// rejection, RBAC denial, etc.). Empty when the message wasn't
	// recognized — UI falls back to showing Message only.
	Cause string `json:"cause,omitempty"`
	// RetryCount is the number of times Argo retried this operation before
	// surfacing the failure. Parsed from "(retried N times)" suffix. 0
	// means either no retry info was available or this was the first
	// attempt; UI should not render a "stuck" indicator at 0.
	RetryCount int `json:"retryCount,omitempty"`
	// Stuck is true when retry count crossed a threshold where transient
	// recovery is no longer plausible. Drives a stronger visual treatment.
	Stuck bool `json:"stuck,omitempty"`
}

type Change struct {
	Ref     Ref    `json:"ref"`
	Category string `json:"category"`
	Sync    string `json:"sync,omitempty"`
	Health  string `json:"health,omitempty"`
	Message string `json:"message,omitempty"`
	// SyncError carries the per-resource sync failure message from
	// status.resources[].syncResult when the last sync attempt for this
	// resource failed. Distinct from Message (which is the live health
	// message) — surfacing both lets the user tell "this resource is
	// degraded right now" from "the last sync attempt for this resource
	// errored". Empty when sync succeeded.
	SyncError string `json:"syncError,omitempty"`
	// HookPhase identifies sync hook resources (PreSync / PostSync /
	// SyncFail / PostDelete) so the UI can mark them visually distinct
	// from regular resources. Empty for non-hook resources.
	HookPhase   string `json:"hookPhase,omitempty"`
	HasDesired  bool   `json:"hasDesired"`
	HasLive     bool   `json:"hasLive"`
	// Drift carries a structured per-field diff between the desired state
	// (parsed from kubectl.kubernetes.io/last-applied-configuration) and
	// the live spec. Nil when we couldn't compute a diff (no last-applied
	// annotation, no live object available, parse failure). The annotation
	// is reliably present on Argo client-side-applied resources; SSA and
	// Helm-applied resources don't carry it.
	Drift *Drift `json:"drift,omitempty"`
	// RecentEvents are the most recent (newest first) events involving this
	// resource. Surfaced inline in the Changes view so operators can see
	// "ImagePullBackOff", "FailedScheduling", "FailedMount" etc. without
	// drilling into the standard resource drawer. Empty when no events
	// exist or no resolver was provided.
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
	// "lastAppliedAnnotation"; future SSA support may add others.
	Source string `json:"source"`
	// Truncated is set when the diff exceeded our entry cap; UI uses this
	// to show "and N more differences — open in Argo for full diff".
	Truncated bool `json:"truncated,omitempty"`
}

// DriftEntry is a single field-level difference. Path uses dot-notation
// rooted at the top-level (e.g. "spec.disruption.expireAfter"). Array
// indices appear as ".[0]". Op is one of:
//
//	"removed" — present in desired, absent (or different) in live
//	"added"   — present in live, not in desired (controller default,
//	            mutating webhook, server-side defaulting)
//	"changed" — both present with different scalar values
//
// Desired/Live are JSON-encoded so structured values (maps, arrays) survive
// the wire round-trip; the UI pretty-prints them.
type DriftEntry struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	Desired string `json:"desired,omitempty"`
	Live    string `json:"live,omitempty"`
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
}

// EventSummary is a compact projection of a corev1.Event for UI display.
// We strip everything that's not useful at a glance — count + type + reason
// + message + age is what an operator scans first.
type EventSummary struct {
	Type           string `json:"type"`              // Normal | Warning
	Reason         string `json:"reason"`            // FailedScheduling, ImagePullBackOff, etc.
	Message        string `json:"message"`           // human-readable detail
	Count          int32  `json:"count,omitempty"`   // event aggregation count (>1 indicates repetition)
	LastTimestamp  string `json:"lastTimestamp"`     // RFC3339 of most recent occurrence
	ReportingComponent string `json:"reportingComponent,omitempty"`
}

func Build(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, resolver Resolver) Insight {
	tool := detectTool(root)
	out := Insight{
		Summary:      buildSummary(root, tool),
		Issues:       buildIssues(root, resourceTree, tool),
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

func buildIssues(root *unstructured.Unstructured, resourceTree *gitopstree.ResourceTree, tool string) []Issue {
	var out []Issue
	// suppressedRefs tracks resources whose own Issue is causally derivative of
	// a parent operation failure (e.g. an OutOfSync resource issue is just
	// the per-resource view of an apply that already failed at the operation
	// level). Hiding these prevents the user from seeing the same root cause
	// rendered in three different forms.
	suppressedRefs := map[string]bool{}
	if tool == "argocd" {
		if phase, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "phase"); phase == "Failed" || phase == "Error" {
			msg, _, _ := unstructured.NestedString(root.Object, "status", "operationState", "message")
			parsed := parseArgoOperationError(msg)
			issue := Issue{
				Severity:   "critical",
				Scope:      "operation",
				Reason:     phase,
				Message:    fallback(msg, "Last sync operation failed"),
				Action:     "Open Activity for operation details.",
				Cause:      parsed.Cause,
				RetryCount: parsed.RetryCount,
				Stuck:      parsed.Stuck,
			}
			if parsed.AffectedKind != "" && parsed.AffectedName != "" {
				ref := Ref{Kind: parsed.AffectedKind, Name: parsed.AffectedName}
				issue.Refs = []Ref{ref}
				suppressedRefs[refKey(ref)] = true
			}
			out = append(out, issue)
		} else if phase == "Running" {
			out = append(out, Issue{Severity: "info", Scope: "operation", Reason: "Running", Message: "A sync operation is currently running.", Action: "Wait for completion or terminate if it is stuck."})
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
		// Argo Application status.conditions surface controller-level problems
		// (ComparisonError = repo unreachable / revision missing,
		// InvalidSpecError = bad app spec, OrphanedResourceWarning, etc.).
		// We previously parsed conditions only for Flux; symmetric coverage
		// for Argo catches a class of "why is this app broken" questions
		// where the answer is the controller couldn't even compute drift.
		out = append(out, argoApplicationConditions(root)...)
		// buildIssues uses change data only for resource-level issue
		// detection — the per-resource diff/events live on the Change
		// objects emitted by buildChanges. Pass nil resolver here to skip
		// the (unused) drift computation in this code path.
		for _, change := range argoResourceChanges(root, nil) {
			// Suppress a resource issue when its kind/name match a resource
			// already named in the operation failure — same root cause, no
			// value in showing it twice.
			if suppressedRefs[refKey(change.Ref)] {
				continue
			}
			if change.Health == "Degraded" || change.Health == "Missing" {
				out = append(out, Issue{Severity: "critical", Scope: "resource", Reason: change.Health, Message: fmt.Sprintf("%s %s is %s", change.Ref.Kind, change.Ref.Name, change.Health), Refs: []Ref{change.Ref}, Action: "Open the resource drawer for events, logs, and YAML."})
			} else if change.Sync == "OutOfSync" {
				out = append(out, Issue{Severity: "warning", Scope: "resource", Reason: "OutOfSync", Message: fmt.Sprintf("%s %s is out of sync", change.Ref.Kind, change.Ref.Name), Refs: []Ref{change.Ref}, Action: "Review Changes or run sync."})
			}
		}
	} else {
		for _, c := range conditions(root) {
			if c.status == "False" && (c.typ == "Ready" || c.typ == "Healthy" || c.typ == "Released" || c.typ == "TestSuccess") {
				out = append(out, Issue{Severity: "critical", Scope: "condition", Reason: fallback(c.reason, c.typ), Message: fallback(c.message, c.typ+" is false"), Action: fluxActionForReason(c.reason)})
			}
			if c.status == "True" && c.typ == "Stalled" {
				out = append(out, Issue{Severity: "critical", Scope: "condition", Reason: fallback(c.reason, "Stalled"), Message: fallback(c.message, "Reconciliation is stalled"), Action: fluxActionForReason(c.reason)})
			}
			if c.status == "True" && c.typ == "Reconciling" {
				out = append(out, Issue{Severity: "info", Scope: "condition", Reason: fallback(c.reason, "Reconciling"), Message: fallback(c.message, "Reconciliation is in progress")})
			}
		}
	}
	if resourceTree != nil && resourceTree.Summary.Degraded > 0 && len(out) == 0 {
		out = append(out, Issue{Severity: "warning", Scope: "tree", Reason: "DegradedResources", Message: fmt.Sprintf("%d managed resources are degraded", resourceTree.Summary.Degraded), Action: "Use the graph or Resources tab to inspect affected resources."})
	}
	sort.SliceStable(out, func(i, j int) bool { return severityRank(out[i].Severity) < severityRank(out[j].Severity) })
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
		category := "Synced"
		partial := true
		note := "Flux inventory confirms this resource is managed; desired manifest content is not available in Radar yet."
		if n.Health == "Degraded" || n.Health == "Missing" {
			category = n.Health
		} else if n.Sync == "OutOfSync" {
			category = "OutOfSync"
		}
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

func argoResourceChanges(root *unstructured.Unstructured, resolver Resolver) []Change {
	raw, _, _ := unstructured.NestedSlice(root.Object, "status", "resources")
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
		category := firstNonEmpty(sync, health, "Unknown")
		if health == "Degraded" || health == "Missing" {
			category = health
		} else if sync == "Synced" && (health == "" || health == "Healthy") {
			category = "Synced"
		}
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
				if drift := computeDriftFromLastApplied(live); drift != nil {
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
			if v, ok := m["id"].(int64); ok {
				id = strconv.FormatInt(v, 10)
			} else if v, ok := m["id"].(float64); ok {
				id = strconv.Itoa(int(v))
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
	for _, c := range conditions(root) {
		out = append(out, HistoryItem{
			ID:         c.typ,
			Phase:      joinNonEmpty(c.status, c.reason),
			Message:    c.message,
			DeployedAt: c.lastTransitionTime,
			Revision:   firstNonEmpty(nestedString(root.Object, "status", "lastAppliedRevision"), nestedString(root.Object, "status", "lastAttemptedRevision")),
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

func changeRank(category string) int {
	switch category {
	case "Degraded", "Missing":
		return 0
	case "OutOfSync":
		return 1
	case "Progressing", "Reconciling":
		return 2
	case "Unknown":
		return 3
	default:
		return 4
	}
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
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

// refKey is the key used to dedup issue refs across the operation+resource
// pass. Group is intentionally omitted — the operation message rarely
// includes it, and kind+name+namespace is enough disambiguation in practice.
func refKey(r Ref) string {
	return r.Kind + "/" + r.Namespace + "/" + r.Name
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
}

// stuckRetryThreshold is the retry count at which we stop calling a failure
// "transient" and start calling it stuck. Argo retries with backoff up to 5
// times by default; reaching that ceiling means the controller has given up
// hoping for self-recovery, which is exactly when the user needs the
// stronger visual.
const stuckRetryThreshold = 5

// Capture group: <Kind>(.<group>...)? "<name>". Examples this matches:
//   CustomResourceDefinition.apiextensions.k8s.io "scaledjobs.keda.sh"
//   Deployment.apps "billing"
//   Service "billing"
// We don't need the group; the leading kind + quoted name is what users read.
var argoAffectedRefRE = regexp.MustCompile(`([A-Z][A-Za-z0-9]+)(?:\.[A-Za-z0-9.\-]+)?\s+"([^"]+)"`)

// "(retried N times)" suffix Argo appends when its retry policy has fired.
var argoRetryRE = regexp.MustCompile(`\(retried (\d+) times?\)`)

// Pattern table: ordered list of (matcher, plain-English cause). First match
// wins. Keep patterns specific — generic catch-alls would mask more useful
// matches. Cases below cover the failure modes operators see most: validation
// limits, admission rejection, RBAC, conflicts, registration, connectivity.
var argoErrorPatterns = []struct {
	match *regexp.Regexp
	cause string
}{
	{regexp.MustCompile(`metadata\.annotations:\s*Too long`), "Annotations exceed Kubernetes' 256 KB metadata limit. Reduce or split the annotations on this resource."},
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
	return out
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
		Severity: "critical",
		Scope:    "operation",
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
		Severity: "warning",
		Scope:    "operation",
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
		severity := "info"
		switch {
		case strings.HasSuffix(typ, "Error"):
			severity = "critical"
		case strings.HasSuffix(typ, "Warning"):
			severity = "warning"
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
			Scope:    "condition",
			Reason:   fallback(typ, "Condition"),
			Message:  fallback(msg, typ),
			Action:   action,
		})
	}
	return out
}

