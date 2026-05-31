package issues

import (
	"log"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// Provider abstracts the data sources Compose needs. Implementations
// in production come from the in-process radar caches; tests can
// inject fakes without standing up an informer stack.
type Provider interface {
	DetectProblems(namespaces []string) []k8s.Detection
	DetectCAPIProblems(namespaces []string) []k8s.Detection
	// DetectGitOpsProblems returns failing ArgoCD Applications and Flux
	// Kustomizations/HelmReleases — the reconciler-health failure class the
	// generic CRD-condition fallback structurally can't read (Argo encodes
	// health/sync outside status.conditions). Surfaced under SourceProblem.
	DetectGitOpsProblems(namespaces []string) []k8s.Detection
	// DetectMissingRefs returns dangling-reference problems (Pod→missing
	// PVC/CM/Secret/SA, HPA→missing target, Ingress→missing backend, etc.)
	// plus webhook-config refs. Surfaced under SourceMissingRef so agents
	// can filter the "direct config error" category separately from the
	// workload-state-based SourceProblem signals.
	DetectMissingRefs(namespaces []string) []k8s.Detection
	// DetectScheduling returns placement/admission/post-bind failures —
	// unschedulable Pods (with the offending node constraint resolved),
	// admission rejections (quota/LimitRange/PodSecurity/webhook, where no
	// Pod exists), and pods stuck post-bind (CNI/volume). Surfaced under
	// SourceScheduling so agents/UI can isolate "why won't this run".
	DetectScheduling(namespaces []string) []k8s.Detection
	// CRD-condition fallback inputs.
	WatchedDynamic() []schema.GroupVersionResource
	ListDynamic(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
	// ListDynamicAllNamespaces unions a namespaced CRD's objects across every
	// watched scope (cluster-wide and/or per-namespace informers). Used ONLY on
	// the cluster-wide-intent path (no namespace filter ⇒ caller is cluster-wide
	// authorized), where a plain ListDynamic(gvr,"") would read only a
	// cluster-wide informer and silently drop namespace-scoped contents.
	ListDynamicAllNamespaces(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error)
	KindForGVR(gvr schema.GroupVersionResource) string
}

type dynamicScopeProvider interface {
	NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool)
}

// ComposeStats reports anything the caller would want to surface
// alongside the issue list — currently CEL-filter eval-error
// counters so the caller can distinguish "filter excluded
// everything" from "cluster has nothing matching."
type ComposeStats struct {
	FilterErrors      int
	FilterErrorSample string
	// TotalMatched is the count of issues that survived ALL filters
	// (severity, source, kind, namespace, CEL) but BEFORE the Limit
	// truncation. Surfaced so the hub aggregator + agents + UI can
	// distinguish "this cluster had 500 issues; we returned 200" from
	// "this cluster had 200." Equal to len(returned slice) when no
	// truncation occurred.
	TotalMatched int
}

// ListResponse is the canonical /api/issues + MCP `issues` response shape. HTTP
// and MCP both build from NewListResponse so the contract can't drift between
// them (and the hub mirrors one shape). Visibility and NarrowHint are set by the
// caller — visibility is server-side RBAC data; NarrowHint is the MCP steering
// string for a capped result.
type ListResponse = issuesapi.Response

// NewListResponse fills the shared fields from a Compose result. out==nil
// becomes [] so the wire always carries a JSON array, never null.
func NewListResponse(out []Issue, stats ComposeStats) ListResponse {
	if out == nil {
		out = []Issue{}
	}
	return ListResponse{
		Issues:            out,
		Total:             len(out),
		TotalMatched:      stats.TotalMatched,
		FilterErrors:      stats.FilterErrors,
		FilterErrorSample: stats.FilterErrorSample,
	}
}

// Compose runs the curated operational sources and merges their output.
// Backward-compatible signature for callers that don't care about stats.
func Compose(p Provider, f Filters) []Issue {
	out, _ := ComposeWithStats(p, f)
	return out
}

// ComposeWithStats does the same work as Compose but also returns
// counters the caller may want to forward — currently the per-row
// CEL filter eval-error count + first error sample. Sort order is
// severity desc, then last-seen desc, then kind/ns/name for stable
// tiebreaks.
func ComposeWithStats(p Provider, f Filters) ([]Issue, ComposeStats) {
	// Negative Limit is the "uncapped" sentinel: callers that need the
	// full matched set (per-resource issue indexes for /api/ai list +
	// search summaryContext) pass NoLimit so a 5000-issue cluster
	// doesn't silently drop counts for resources whose issues fall in
	// the tail beyond MaxLimit. Zero still maps to DefaultLimit so the
	// public /api/issues + MCP issues_list keep their tight caps.
	uncapped := f.Limit < 0
	if f.Limit == 0 {
		f.Limit = DefaultLimit
	}
	if !uncapped && f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}

	out := make([]Issue, 0, 64)
	now := time.Now()

	// issues = "what's broken right now" — the curated operational
	// sources, always composed. Raw Warning events live in get_events /
	// the timeline; Kyverno / policy posture lives with audit/compliance;
	// static best-practice findings live in audit. None of those belong in
	// the live-failure stream, so they are deliberately NOT sources here.
	// `source` survives only as an output label on each row (+ CEL filter),
	// not as an input filter — detection provenance is not a triage axis.

	// ---- 1. Collect flat evidence from every curated source ----------
	// emit drops info-severity problems: those are inert/posture findings
	// (deprecated-RBAC residue, singleton-StatefulSet headless-DNS trivia) —
	// classified honestly at the Problem layer for other surfaces, but NOT part
	// of the live "what's broken now" issue stream. Issues stays critical|warning.
	emit := func(ps []k8s.Detection, source Source) {
		for _, pr := range ps {
			if pr.Severity == "info" {
				continue
			}
			out = append(out, fromProblem(pr, now, source))
		}
	}
	emit(p.DetectProblems(f.Namespaces), SourceProblem)       // hardcoded per-kind checks
	emit(p.DetectCAPIProblems(f.Namespaces), SourceProblem)   // Cluster API
	emit(p.DetectGitOpsProblems(f.Namespaces), SourceProblem) // Argo/Flux reconciler health
	emit(p.DetectMissingRefs(f.Namespaces), SourceMissingRef) // dangling by-name refs
	emit(p.DetectScheduling(f.Namespaces), SourceScheduling)  // placement/admission/post-bind
	out = append(out, detectGenericCRDIssues(p, f)...)        // generic CRD .status.conditions

	// ---- 2. Evidence-level transforms (operate on flat rows) ---------
	// RBAC gating on the underlying resource, and dedup that compares child
	// symptoms against parent rollups across member pods — both need the flat
	// rows, so they run BEFORE grouping and BEFORE the public filters.
	out = applyClusterScopedAccess(out, f)
	out = dedupePodSchedulingOverProblem(out)
	out = dedupeWorkloadDegradedOverChild(out)

	// ---- 3. Shape: fold to the public grouped model ------------------
	// A grouped row's Kind/Name is the SUBJECT (the owner a 50-pod crashloop
	// rolls up to) and Count is the affected-resource fan-out excluding that
	// subject. Folding before the public filters is what makes kind= match the
	// subject and count> match the fan-out — filtering the flat evidence first
	// would drop a pod-evidenced Deployment issue under kind=Deployment and
	// never see a fan-out count.
	if f.Grouped {
		out = GroupIssues(out)
	}

	// ---- 4. Public filters on the shaped rows ------------------------
	out = applyFilters(out, f) // severity + kind, against subject (grouped) or evidence (flat)
	var stats ComposeStats
	if f.Filter != nil {
		// CEL evaluated last so it sees the public shape. Eval errors count as
		// non-match ("missing field" semantics: zero hits + clean response, not
		// a 500); ComposeStats forwards the count + first sample so the caller
		// can distinguish "filter excluded everything" from "nothing matched."
		filtered := out[:0]
		var firstErr error
		errCount := 0
		for _, i := range out {
			ok, err := f.Filter.Match(issueToActivation(i))
			if err != nil {
				errCount++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if ok {
				filtered = append(filtered, i)
			}
		}
		if errCount > 0 {
			log.Printf("[issues] CEL filter eval errors: %d/%d rows; first=%v", errCount, len(out), firstErr)
			stats.FilterErrors = errCount
			if firstErr != nil {
				stats.FilterErrorSample = firstErr.Error()
			}
		}
		out = filtered
	}

	// ---- 5. Sort, count, cap -----------------------------------------
	// GroupIssues already sorted deterministically; the flat path sorts here
	// with the same comparator so both orders agree.
	if !f.Grouped {
		sort.SliceStable(out, func(i, j int) bool { return lessIssue(out[i], out[j]) })
	}
	stats.TotalMatched = len(out)
	if !uncapped && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, stats
}
