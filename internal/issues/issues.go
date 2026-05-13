package issues

import (
	"log"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// Provider abstracts the data sources Compose needs. Implementations
// in production come from the in-process radar caches; tests can
// inject fakes without standing up an informer stack.
type Provider interface {
	DetectProblems(namespaces []string) []k8s.Problem
	DetectCAPIProblems(namespaces []string) []k8s.Problem
	AuditFindings(namespaces []string) []bp.Finding
	WarningEvents(namespaces []string, since time.Duration) []*corev1.Event
	// CRD-condition fallback inputs.
	WatchedDynamic() []schema.GroupVersionResource
	ListDynamic(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
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

// Compose runs the four sources and merges their output. Backward-
// compatible signature for callers that don't care about stats.
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
	if f.Limit == 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}

	out := make([]Issue, 0, 64)
	now := time.Now()

	// ---- Source: problem (radar's hardcoded checks) -----------------
	if wantSource(f, SourceProblem) {
		for _, p := range p.DetectProblems(f.Namespaces) {
			out = append(out, fromProblem(p, now))
		}
		for _, p := range p.DetectCAPIProblems(f.Namespaces) {
			out = append(out, fromProblem(p, now))
		}
	}

	// ---- Source: condition (generic CRD .status.conditions fallback) ----
	if wantSource(f, SourceCondition) {
		out = append(out, detectGenericCRDIssues(p, f)...)
	}

	// ---- Source: audit (best-practice findings) --------------------
	// Off by default — audit findings are loud; the AI/MCP user case
	// usually wants problems first. Set IncludeAudit to opt in.
	if f.IncludeAudit && wantSource(f, SourceAudit) {
		for _, fin := range p.AuditFindings(f.Namespaces) {
			out = append(out, fromAudit(fin, now))
		}
	}

	// ---- Source: event (recent K8s Warning events) -----------------
	// Gated by IncludeEvents, analogous to IncludeAudit. Events are
	// the noisiest source by far on real clusters (each broken Pod
	// emits a Warning Event every few seconds, retained for the cache
	// window) and almost always duplicate signal already surfaced by
	// SourceProblem. Default-off keeps the Issue count aligned with
	// the per-cluster "X problems" intuition; user opts in via
	// include_events=true or by passing "event" in source=.
	if f.IncludeEvents && wantSource(f, SourceEvent) {
		for _, e := range p.WarningEvents(f.Namespaces, f.Since) {
			out = append(out, fromWarningEvent(e))
		}
	}

	// Apply remaining filters (severity, kind, namespace) post-compose
	// since each source has its own native filtering surface and
	// pushing filters down individually would multiply branching.
	out = applyFilters(out, f)
	out = applyClusterScopedAccess(out, f)

	// Optional CEL filter — evaluated last so it sees the normalized
	// row shape. Eval errors count as non-match (matches "missing
	// field" semantics; agent gets zero hits + a clean response,
	// rather than a 500). Runtime causes: missing-field traversal,
	// type mismatches on dyn-typed nested fields, cost-limit
	// overruns. Parse/type errors against the declared bindings
	// fail at compile and never reach here. ComposeStats surfaces
	// the count + first sample back to the handler so the agent can
	// distinguish "filter excluded everything" from "cluster has
	// nothing matching."
	var stats ComposeStats
	if f.Filter != nil {
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

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	stats.TotalMatched = len(out)
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, stats
}

// detectGenericCRDIssues walks every watched dynamic CRD and emits a
// warning Issue for each object that has a False Ready/Available/etc.
// condition. Skips kinds owned by curated checkers (Cluster API today)
// to avoid double-reporting.
func detectGenericCRDIssues(p Provider, f Filters) []Issue {
	gvrs := p.WatchedDynamic()
	if len(gvrs) == 0 {
		return nil
	}
	var out []Issue
	for _, gvr := range gvrs {
		if isCuratedCRDGroup(gvr.Group) {
			continue
		}
		kind := p.KindForGVR(gvr)
		if kind == "" {
			continue
		}
		clusterScoped, _, _ := classifyDynamicScope(p, gvr, kind)
		if clusterScoped && f.CanReadClusterScoped != nil && !f.CanReadClusterScoped(kind, gvr.Group) {
			continue
		}
		// Per-namespace iteration when scope is set; cluster-wide list
		// otherwise. List with empty namespace returns all namespaces.
		queryNs := []string{""}
		if !clusterScoped && len(f.Namespaces) > 0 {
			queryNs = f.Namespaces
		}
		for _, ns := range queryNs {
			items, err := p.ListDynamic(gvr, ns)
			if err != nil {
				continue
			}
			for _, u := range items {
				condType, reason, msg, since, ok := FindFalseCondition(u)
				if !ok {
					continue
				}
				lastSeen := time.Now().Add(-since)
				out = append(out, Issue{
					Severity:  SeverityWarning,
					Source:    SourceCondition,
					Kind:      kind,
					Group:     gvr.Group,
					Namespace: u.GetNamespace(),
					Name:      u.GetName(),
					Reason:    condTypeReason(condType, reason),
					Message:   msg,
					FirstSeen: lastSeen,
					LastSeen:  lastSeen,
					Count:     1,
				})
			}
		}
	}
	return out
}

func classifyDynamicScope(p Provider, gvr schema.GroupVersionResource, kind string) (bool, string, string) {
	if sp, ok := p.(dynamicScopeProvider); ok {
		if namespaced, known := sp.NamespacedForGVR(gvr); known {
			return !namespaced, gvr.Group, gvr.Resource
		}
	}
	return k8s.ClassifyKindScope(kind, gvr.Group)
}

// isCuratedCRDGroup returns true for groups that have their own
// dedicated checker upstream — generic fallback skips them so we
// don't emit duplicate issues with shallower context. Add to this
// list whenever a curated checker is wired into Compose.
func isCuratedCRDGroup(group string) bool {
	switch group {
	case "cluster.x-k8s.io",
		"controlplane.cluster.x-k8s.io",
		"infrastructure.cluster.x-k8s.io",
		"bootstrap.cluster.x-k8s.io":
		return true
	}
	return false
}

// condTypeReason combines the condition type (e.g. "Ready") and the
// optional reason ("CrashLoopBackOff") into one display string. When
// reason is empty, falls back to "<Type>=False".
func condTypeReason(condType, reason string) string {
	if reason != "" {
		return condType + ": " + reason
	}
	return condType + "=False"
}

// ---------------------------------------------------------------------------
// Source-specific normalization
// ---------------------------------------------------------------------------

func fromProblem(p k8s.Problem, now time.Time) Issue {
	sev := SeverityWarning
	if p.Severity == "critical" {
		sev = SeverityCritical
	}
	since := now.Add(-time.Duration(p.DurationSeconds) * time.Second)
	return Issue{
		Severity:  sev,
		Source:    SourceProblem,
		Kind:      p.Kind,
		Group:     p.Group,
		Namespace: p.Namespace,
		Name:      p.Name,
		Reason:    p.Reason,
		Message:   p.Message,
		FirstSeen: since,
		LastSeen:  now,
		Count:     1,
	}
}

func fromAudit(fin bp.Finding, now time.Time) Issue {
	sev := SeverityWarning
	if fin.Severity == bp.SeverityDanger {
		sev = SeverityCritical
	}
	return Issue{
		Severity:  sev,
		Source:    SourceAudit,
		Kind:      fin.Kind,
		Namespace: fin.Namespace,
		Name:      fin.Name,
		Reason:    fin.CheckID,
		Message:   fin.Message,
		FirstSeen: now,
		LastSeen:  now,
		Count:     1,
	}
}

// fromWarningEvent maps a K8s Warning event to an Issue. Severity is
// always `warning`; events don't ship a severity scale that maps cleanly
// to our `critical` tier (a CrashLoopBackOff event coexists with the
// problem-source `critical` Deployment issue, so we don't double-amplify).
func fromWarningEvent(e *corev1.Event) Issue {
	first := e.FirstTimestamp.Time
	last := e.LastTimestamp.Time
	if last.IsZero() {
		last = e.EventTime.Time
	}
	if first.IsZero() {
		first = last
	}
	return Issue{
		Severity:  SeverityWarning,
		Source:    SourceEvent,
		Kind:      e.InvolvedObject.Kind,
		Namespace: e.Namespace,
		Name:      e.InvolvedObject.Name,
		Reason:    e.Reason,
		Message:   e.Message,
		FirstSeen: first,
		LastSeen:  last,
		Count:     int(e.Count),
	}
}

// ---------------------------------------------------------------------------
// Filter + sort helpers
// ---------------------------------------------------------------------------

func wantSource(f Filters, s Source) bool {
	if len(f.Sources) == 0 {
		return true
	}
	for _, want := range f.Sources {
		if want == s {
			return true
		}
	}
	return false
}

func applyFilters(in []Issue, f Filters) []Issue {
	if len(f.Severities) == 0 && len(f.Kinds) == 0 {
		return in
	}
	wantSev := map[Severity]bool{}
	for _, s := range f.Severities {
		wantSev[s] = true
	}
	wantKind := map[string]bool{}
	for _, k := range f.Kinds {
		wantKind[strings.ToLower(k)] = true
	}
	out := in[:0]
	for _, i := range in {
		if len(wantSev) > 0 && !wantSev[i.Severity] {
			continue
		}
		if len(wantKind) > 0 && !wantKind[strings.ToLower(i.Kind)] {
			continue
		}
		out = append(out, i)
	}
	return out
}

func applyClusterScopedAccess(in []Issue, f Filters) []Issue {
	if f.CanReadClusterScoped == nil {
		return in
	}
	out := make([]Issue, 0, len(in))
	for _, i := range in {
		if i.Namespace != "" {
			out = append(out, i)
			continue
		}
		// Namespace-less issue: must be cluster-scoped (a namespaced
		// resource without a namespace would be invalid wire data). We
		// previously gated on k8s.ClassifyKindScope (a hardcoded list of
		// known cluster-scoped kinds) and silently dropped anything that
		// didn't match — which meant CRDs like Karpenter NodePool, whose
		// emitter already classified them as cluster-scoped via dynamic
		// API discovery, vanished from the issues list for authenticated
		// users. CanReadClusterScoped (SAR-backed) is authoritative on
		// access; we don't need a pre-classification gate at this layer.
		if f.CanReadClusterScoped(i.Kind, i.Group) {
			out = append(out, i)
		}
	}
	return out
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	}
	return 0
}
