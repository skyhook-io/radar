package policyreports

import (
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/audit"
)

// MaxIndexedReports caps how many PolicyReport documents the index keeps,
// chosen by newest `metadata.creationTimestamp` first. Reports beyond the
// cap are silently dropped on rebuild. Tunable here for clusters where a
// single namespace generates a runaway number of reports — the index is
// purely diagnostic, so dropping the oldest is acceptable.
const MaxIndexedReports = 500

// Index maps subject keys ("Kind/namespace/name", per audit.ResourceKey) to
// the policy findings that apply to that subject. It is safe for concurrent
// read/write: callers building the index from informer events may swap
// contents while other callers serve `FindingsFor` lookups.
//
// The index is a pure projection of the input reports — it owns no
// underlying state and does not refetch.
type Index struct {
	mu        sync.RWMutex
	bySubject map[string][]Finding
}

// NewIndex returns an empty Index.
func NewIndex() *Index {
	return &Index{bySubject: make(map[string][]Finding)}
}

// BuildIndex constructs an Index from a slice of PolicyReport documents
// (both namespaced PolicyReport and cluster-scoped ClusterPolicyReport).
// Reports are processed newest-first by `metadata.creationTimestamp`, and
// only the first MaxIndexedReports are considered — older reports are
// dropped to bound memory.
//
// For each report, every entry in `results[]` becomes one Finding per
// resource in `results[].resources[]`. When a result has no `resources[]`
// (single-target reports), the enclosing `report.scope` is used as the
// subject. Reports with neither resources nor a scope contribute no
// findings (there is no subject to index by).
func BuildIndex(reports []*unstructured.Unstructured) *Index {
	idx := NewIndex()
	idx.Replace(reports)
	return idx
}

// Replace rebuilds the index in-place from the given reports. Existing
// entries are discarded. Used by the live-update path: an informer event
// handler re-lists the cache and calls Replace to keep the index fresh.
func (i *Index) Replace(reports []*unstructured.Unstructured) {
	if i == nil {
		return
	}

	// Sort newest-first so the cap drops the oldest reports.
	sorted := make([]*unstructured.Unstructured, len(reports))
	copy(sorted, reports)
	sort.SliceStable(sorted, func(a, b int) bool {
		return sorted[a].GetCreationTimestamp().Time.After(sorted[b].GetCreationTimestamp().Time)
	})
	if len(sorted) > MaxIndexedReports {
		sorted = sorted[:MaxIndexedReports]
	}

	next := make(map[string][]Finding)
	for _, r := range sorted {
		extractFindings(r, next)
	}

	i.mu.Lock()
	i.bySubject = next
	i.mu.Unlock()
}

// FindingsFor returns the findings indexed for the given subject. Returns
// nil if no findings are recorded for that subject.
//
// The returned slice is a defensive copy: callers may freely sort, truncate,
// or filter it without racing the index's own rebuild path. The cost is
// modest — findings per subject are bounded (Kyverno emits at most one
// PolicyReport entry per (policy, rule, resource) tuple, and pathological
// reports are capped during BuildIndex anyway).
func (i *Index) FindingsFor(kind, namespace, name string) []Finding {
	if i == nil {
		return nil
	}
	key := audit.ResourceKey(kind, namespace, name)
	i.mu.RLock()
	defer i.mu.RUnlock()
	src := i.bySubject[key]
	if len(src) == 0 {
		return nil
	}
	out := make([]Finding, len(src))
	copy(out, src)
	return out
}

// Size returns the number of distinct subjects with at least one indexed
// finding. Useful for diagnostics and tests.
func (i *Index) Size() int {
	if i == nil {
		return 0
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.bySubject)
}

// extractFindings appends one Finding per (result, resource) pair into the
// destination map. The map's keys are `audit.ResourceKey` values; the
// helper centralizes the shape so callers don't have to know about the
// PolicyReport schema.
//
// Schema reference (https://github.com/kubernetes-sigs/wg-policy-prototypes):
//
//	apiVersion: wgpolicyk8s.io/v1alpha2
//	kind: PolicyReport
//	metadata: { ... }
//	scope: { apiVersion, kind, namespace, name, uid }  # optional
//	results:
//	  - policy: string
//	    rule: string
//	    result: pass|fail|warn|error|skip
//	    severity: info|low|medium|high|critical
//	    category: string
//	    message: string
//	    resources:
//	      - apiVersion, kind, namespace, name, uid     # optional, can be []
//	    ...
func extractFindings(report *unstructured.Unstructured, dst map[string][]Finding) {
	if report == nil {
		return
	}

	scopeKind, scopeNS, scopeName := reportScope(report)

	results, found, err := unstructured.NestedSlice(report.Object, "results")
	if err != nil || !found {
		return
	}

	reportNamespace := report.GetNamespace() // for ClusterPolicyReport, "".

	for _, raw := range results {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		f := Finding{
			Policy:   stringField(entry, "policy"),
			Rule:     stringField(entry, "rule"),
			Result:   stringField(entry, "result"),
			Severity: stringField(entry, "severity"),
			Category: stringField(entry, "category"),
			Message:  stringField(entry, "message"),
		}

		subjects, hasResources := resultResources(entry)

		// Pre-filter to subjects that can actually be indexed. A non-empty
		// resources[] slice of empty objects (e.g. `resources: [{}]` from
		// a malformed CRD) would otherwise skip scope fallback below AND
		// then get filtered to nothing in the index loop — silently
		// dropping a finding that the report's scope could have rescued.
		validSubjects := make([]subjectRef, 0, len(subjects))
		for _, s := range subjects {
			if s.kind != "" && s.name != "" {
				validSubjects = append(validSubjects, s)
			}
		}

		if !hasResources || len(validSubjects) == 0 {
			// Scope-only report (or all subjects malformed): the report
			// itself is bound to one subject via `report.scope`. The
			// PolicyReport namespace overrides the scope's namespace when
			// scope.namespace is unset (some engines emit only kind/name
			// in scope for namespaced reports and rely on metadata.namespace).
			if scopeKind != "" && scopeName != "" {
				ns := scopeNS
				if ns == "" {
					ns = reportNamespace
				}
				key := audit.ResourceKey(scopeKind, ns, scopeName)
				dst[key] = append(dst[key], f)
			}
			continue
		}

		for _, s := range validSubjects {
			ns := s.namespace
			// Namespaced PolicyReports default subject namespace to the
			// report's namespace when not set on the resource ref — this
			// mirrors how Kyverno emits namespaced reports.
			if ns == "" {
				ns = reportNamespace
			}
			key := audit.ResourceKey(s.kind, ns, s.name)
			dst[key] = append(dst[key], f)
		}
	}
}

// reportScope returns the `report.scope` subject as (kind, namespace, name).
// All three are empty strings when scope is missing — the caller treats
// that case as "no scope-only fallback available".
func reportScope(report *unstructured.Unstructured) (string, string, string) {
	scope, found, err := unstructured.NestedMap(report.Object, "scope")
	if err != nil || !found {
		return "", "", ""
	}
	return stringField(scope, "kind"), stringField(scope, "namespace"), stringField(scope, "name")
}

type subjectRef struct {
	kind      string
	namespace string
	name      string
}

// resultResources reads `results[].resources[]` into subjectRefs. Returns
// (refs, true) when the `resources` key was present at all (even if empty),
// (nil, false) when the key was absent — the caller distinguishes
// "explicitly no resources" (empty slice) from "scope-only report" (key
// absent / single-target) so the scope fallback only fires in the latter.
//
// In practice both engines we've observed (Kyverno, Trivy) either emit
// `resources` populated or omit it entirely when the report is scope-only,
// so we treat empty-but-present the same as scope-only as well — this is
// the more useful behavior and matches operator intent.
func resultResources(entry map[string]any) ([]subjectRef, bool) {
	raw, ok := entry["resources"]
	if !ok || raw == nil {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	refs := make([]subjectRef, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		refs = append(refs, subjectRef{
			kind:      stringField(m, "kind"),
			namespace: stringField(m, "namespace"),
			name:      stringField(m, "name"),
		})
	}
	return refs, true
}

func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
