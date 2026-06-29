package policyreports

import (
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Subject identifies the resource a set of Findings applies to.
//
// Group is the API group of the subject (e.g. "apps" for Deployment,
// "" for core/v1 kinds like Pod). Without it, a CRD with the same Kind
// as a built-in (or two CRDs sharing a Kind across different groups,
// e.g. argoproj.io/Application vs unrelated.io/Application) would
// collide on the same index entry and consumers couldn't apply group-
// aware RBAC checks against the resulting Issues.
type Subject struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// SubjectFindings pairs a subject ref with the findings indexed against it.
// Returned by Index.All so consumers can enumerate every indexed subject
// without needing per-subject lookups.
type SubjectFindings struct {
	Subject  Subject
	Findings []Finding
}

// MaxIndexedReports caps how many PolicyReport documents the index keeps,
// chosen by newest `metadata.creationTimestamp` first. Reports beyond the
// cap are silently dropped on rebuild. Tunable here for clusters where a
// single namespace generates a runaway number of reports — the index is
// purely diagnostic, so dropping the oldest is acceptable.
const MaxIndexedReports = 500

// Index maps subject keys ("Group/Kind/namespace/name", group-prefixed
// so distinct CRDs sharing a Kind don't collide) to the policy findings
// that apply to that subject. It is safe for concurrent read/write:
// callers building the index from informer events may swap contents
// while other callers serve `FindingsFor` lookups.
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
// Group is the subject's API group ("" for core kinds like Pod). It's
// part of the index key so distinct CRDs sharing a Kind don't collide.
//
// The returned slice is a defensive copy: callers may freely sort, truncate,
// or filter it without racing the index's own rebuild path. The cost is
// modest — findings per subject are bounded (Kyverno emits at most one
// PolicyReport entry per (policy, rule, resource) tuple, and pathological
// reports are capped during BuildIndex anyway).
func (i *Index) FindingsFor(group, kind, namespace, name string) []Finding {
	if i == nil {
		return nil
	}
	key := subjectKey(group, kind, namespace, name)
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

// All returns every indexed subject together with its findings. Both the
// outer slice and each per-subject Findings slice are defensive copies —
// callers may freely sort, truncate, or filter them without racing the
// index's rebuild path. Subjects appear in alphabetical key order so the
// caller's downstream output is stable regardless of go's map-iteration
// randomization.
func (i *Index) All() []SubjectFindings {
	if i == nil {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	if len(i.bySubject) == 0 {
		return nil
	}
	keys := make([]string, 0, len(i.bySubject))
	for k := range i.bySubject {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]SubjectFindings, 0, len(keys))
	for _, k := range keys {
		src := i.bySubject[k]
		if len(src) == 0 {
			continue
		}
		group, kind, ns, name, ok := parseSubjectKey(k)
		if !ok {
			continue
		}
		fs := make([]Finding, len(src))
		copy(fs, src)
		out = append(out, SubjectFindings{
			Subject:  Subject{Group: group, Kind: kind, Namespace: ns, Name: name},
			Findings: fs,
		})
	}
	return out
}

// subjectKey is the index key format: "group/Kind/namespace/name", with
// an empty group segment for core kinds (Pod, Service, ...) and an empty
// namespace segment for cluster-scoped subjects (Node, ClusterRole, ...).
// Group must come first because both "group" and "namespace" can be
// empty independently; encoding group last would make a cluster-scoped
// CRD ("apps.example.io/Foo//bar") indistinguishable from a namespaced
// core-group subject under a parse that allowed three-part keys.
func subjectKey(group, kind, namespace, name string) string {
	return group + "/" + kind + "/" + namespace + "/" + name
}

// parseSubjectKey reverses subjectKey. Returns ok=false for any key that
// does not have exactly four slash-delimited parts. K8s groups, names,
// namespaces, and kinds can't contain '/' (DNS-label / DNS-subdomain
// rules), so the SplitN is unambiguous in practice.
func parseSubjectKey(key string) (group, kind, namespace, name string, ok bool) {
	parts := strings.SplitN(key, "/", 4)
	if len(parts) != 4 {
		return "", "", "", "", false
	}
	// Kind and name must be non-empty; group and namespace may be empty
	// (core-group + cluster-scoped subjects respectively).
	if parts[1] == "" || parts[3] == "" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
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
// destination map. The map's keys are subjectKey values; the helper
// centralizes the shape so callers don't have to know about the
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

	scopeGroup, scopeKind, scopeNS, scopeName := reportScope(report)

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
				key := subjectKey(scopeGroup, scopeKind, ns, scopeName)
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
			key := subjectKey(s.group, s.kind, ns, s.name)
			dst[key] = append(dst[key], f)
		}
	}
}

// reportScope returns the `report.scope` subject as (group, kind,
// namespace, name). All four are empty strings when scope is missing —
// the caller treats that case as "no scope-only fallback available".
// Group is derived from `scope.apiVersion` (the part before "/"; "" for
// core kinds like Pod whose apiVersion is just "v1").
func reportScope(report *unstructured.Unstructured) (group, kind, namespace, name string) {
	scope, found, err := unstructured.NestedMap(report.Object, "scope")
	if err != nil || !found {
		return "", "", "", ""
	}
	return groupFromAPIVersion(stringField(scope, "apiVersion")),
		stringField(scope, "kind"),
		stringField(scope, "namespace"),
		stringField(scope, "name")
}

type subjectRef struct {
	group     string
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
			group:     groupFromAPIVersion(stringField(m, "apiVersion")),
			kind:      stringField(m, "kind"),
			namespace: stringField(m, "namespace"),
			name:      stringField(m, "name"),
		})
	}
	return refs, true
}

// groupFromAPIVersion extracts the API group from a Kubernetes apiVersion
// string ("apps/v1" → "apps", "v1" → "", "" → ""). The pkg/gitops package
// has a public helper that does the same job, but we copy it here so that
// the policyreports package stays free of cross-package dependencies (it
// is reused by callers that don't want the gitops surface).
func groupFromAPIVersion(apiVersion string) string {
	if apiVersion == "" || apiVersion == "v1" {
		return ""
	}
	if before, _, ok := strings.Cut(apiVersion, "/"); ok {
		return before
	}
	// apiVersion without a slash is a non-core group with no version
	// (rare/malformed) — treat the whole string as the group.
	return apiVersion
}

func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
