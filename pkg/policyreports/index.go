package policyreports

import (
	"cmp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/skyhook-io/radar/pkg/resourceid"

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

// MaxIndexedReports is retained for source compatibility. It no longer limits
// the index; every report supplied to BuildIndex or Replace is included.
//
// Deprecated: PolicyReport retention belongs to the cluster and report
// producers, not the index.
const MaxIndexedReports = 500

// Index maps subject keys ("Group/Kind/namespace/name", group-prefixed
// so distinct CRDs sharing a Kind don't collide) to the policy findings
// that apply to that subject. It is safe for concurrent read/write:
// callers building the index from informer events may swap contents
// while other callers serve `FindingsFor` lookups.
//
// The index is a pure projection of the input reports — it owns no
// underlying state and does not refetch.

// sourcedFinding is a Finding plus the report API groups it arrived in.
//
// Provenance is kept BESIDE the finding rather than inside it on purpose.
// Finding is the dedup key in dedupeFindings, which exists to collapse the same
// result arriving in both report families while a cluster migrates from
// wgpolicyk8s.io to openreports.io. A group field inside Finding would change
// that key, silently stop the collapse, and double-count every violation on
// exactly the mid-migration clusters the dedup was written for.
type sourcedFinding struct {
	Finding Finding
	// Groups are the report API groups this identical finding was seen in,
	// sorted and deduped. More than one means both families carry it.
	Groups []string
}

type Index struct {
	mu        sync.RWMutex
	bySubject map[string][]sourcedFinding
	byPolicy  map[string][]PolicyOutcome
}

// NewIndex returns an empty Index.
func NewIndex() *Index {
	return &Index{
		bySubject: make(map[string][]sourcedFinding),
		byPolicy:  make(map[string][]PolicyOutcome),
	}
}

// PolicyOutcome pairs a subject with one finding a policy recorded against it.
// It is the reverse of the bySubject projection: "which resources did this
// policy examine, and what did it decide about each".
type PolicyOutcome struct {
	Subject Subject
	Finding Finding
	// Groups are the report API groups this outcome was read from. A caller
	// authorizing per family filters on these; an outcome seen in both families
	// carries both, so a caller who can read either may see it.
	Groups []string
}

// BuildIndex constructs an Index from a slice of PolicyReport documents
// (both namespaced PolicyReport and cluster-scoped ClusterPolicyReport).
// Every supplied report is included; retention is owned by the cluster and
// the report producers rather than this projection.
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

	next := make(map[string][]sourcedFinding)
	for _, r := range reports {
		extractFindings(r, next)
	}
	dedupeFindings(next)
	nextByPolicy := buildPolicyIndex(next)

	i.mu.Lock()
	i.bySubject = next
	i.byPolicy = nextByPolicy
	i.mu.Unlock()
}

// buildPolicyIndex inverts the per-subject map into a per-policy one.
//
// Built here rather than projected per request: the caller reads this on every
// policy drawer open and on every SSE-driven refresh, and walking the whole
// subject map each time would copy every finding in the cluster to answer a
// question about one policy.
func buildPolicyIndex(bySubject map[string][]sourcedFinding) map[string][]PolicyOutcome {
	byPolicy := make(map[string][]PolicyOutcome)
	for key, findings := range bySubject {
		group, kind, ns, name, ok := parseSubjectKey(key)
		if !ok {
			continue
		}
		subject := Subject{Group: group, Kind: kind, Namespace: ns, Name: name}
		for _, sf := range findings {
			if sf.Finding.Policy == "" {
				continue
			}
			byPolicy[sf.Finding.Policy] = append(byPolicy[sf.Finding.Policy],
				PolicyOutcome{Subject: subject, Finding: sf.Finding, Groups: sf.Groups})
		}
	}
	for policy, outcomes := range byPolicy {
		slices.SortFunc(outcomes, func(left, right PolicyOutcome) int {
			return cmp.Or(
				cmp.Compare(left.Subject.Namespace, right.Subject.Namespace),
				cmp.Compare(left.Subject.Kind, right.Subject.Kind),
				cmp.Compare(left.Subject.Name, right.Subject.Name),
				cmp.Compare(left.Finding.Rule, right.Finding.Rule),
				cmp.Compare(left.Finding.Result, right.Finding.Result),
			)
		})
		byPolicy[policy] = outcomes
	}
	return byPolicy
}

// OutcomesForPolicy returns every subject this policy recorded a finding
// against, in stable order. `policy` must match the report's `results[].policy`
// value verbatim — Kyverno writes a bare name for cluster-scoped policies and
// `namespace/name` for namespaced ones, so callers holding a policy object
// should try both shapes.
//
// The returned slice is a defensive copy.
func (i *Index) OutcomesForPolicy(policy string) []PolicyOutcome {
	if i == nil || policy == "" {
		return nil
	}
	i.mu.RLock()
	defer i.mu.RUnlock()
	src := i.byPolicy[policy]
	if len(src) == 0 {
		return nil
	}
	out := make([]PolicyOutcome, len(src))
	copy(out, src)
	return out
}

// autogenPrefixes are the rule-name prefixes Kyverno synthesises when it
// expands a Pod rule to the controllers that own Pods. The generated rules are
// not written by the user and must not be presented as if they were.
var autogenPrefixes = []string{"autogen-cronjob-", "autogen-"}

// BaseRule strips Kyverno's autogen prefix from a rule name, reporting whether
// one was present. `autogen-validate-image-tag` and `validate-image-tag` are
// the same authored rule applied at two levels; showing both as peers doubles
// the rule list and invites the question of which one is real.
func BaseRule(rule string) (base string, autogen bool) {
	for _, p := range autogenPrefixes {
		if strings.HasPrefix(rule, p) {
			return strings.TrimPrefix(rule, p), true
		}
	}
	return rule, false
}

// dedupeFindings collapses byte-identical findings within each subject.
//
// A cluster migrating between report APIs (wgpolicyk8s.io → openreports.io)
// can serve both families at once, and the caller may legitimately watch both
// when both hold data — stale reports in the old family otherwise disappear
// from view entirely. The same (policy, rule, result, message, source) on the
// same subject carries no extra information whichever report it arrived in,
// and counting it twice would inflate every violation total.
// Sorting by the complete finding value keeps downstream top-N summaries
// stable even though informer stores do not promise report iteration order.
func dedupeFindings(bySubject map[string][]sourcedFinding) {
	for key, findings := range bySubject {
		if len(findings) < 2 {
			continue
		}
		// Keyed on Finding exactly as before — the collapse must not weaken.
		// The groups of every duplicate are unioned onto the survivor so
		// provenance is preserved without entering the key.
		at := make(map[Finding]int, len(findings))
		out := findings[:0]
		for _, sf := range findings {
			if idx, ok := at[sf.Finding]; ok {
				out[idx].Groups = mergeGroups(out[idx].Groups, sf.Groups)
				continue
			}
			at[sf.Finding] = len(out)
			out = append(out, sf)
		}
		slices.SortFunc(out, func(left, right sourcedFinding) int {
			return cmp.Or(
				cmp.Compare(left.Finding.Policy, right.Finding.Policy),
				cmp.Compare(left.Finding.Rule, right.Finding.Rule),
				cmp.Compare(left.Finding.Result, right.Finding.Result),
				cmp.Compare(left.Finding.Severity, right.Finding.Severity),
				cmp.Compare(left.Finding.Category, right.Finding.Category),
				cmp.Compare(left.Finding.Message, right.Finding.Message),
				cmp.Compare(left.Finding.Source, right.Finding.Source),
			)
		})
		bySubject[key] = out
	}
}

// mergeGroups unions two sorted-unique group lists.
func mergeGroups(a, b []string) []string {
	for _, g := range b {
		if !slices.Contains(a, g) {
			a = append(a, g)
		}
	}
	slices.Sort(a)
	return a
}

// FindingsFor returns the findings indexed for the given subject. Returns
// nil if no findings are recorded for that subject.
//
// Group is the subject's API group ("" for core kinds like Pod). It's
// part of the index key so distinct CRDs sharing a Kind don't collide.
//
// The returned slice is a defensive copy: callers may freely sort, truncate,
// or filter it without racing the index's own rebuild path.
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
	out := make([]Finding, 0, len(src))
	for _, sf := range src {
		out = append(out, sf.Finding)
	}
	return out
}

// SourcedFindingsFor returns the findings for a subject with the report
// families each came from.
//
// The plain FindingsFor drops that provenance, and a caller who drops it cannot
// filter on it — which is how the per-resource view came to show a caller
// findings from a family they are not authorized to read while the per-policy
// view filtered them out. Same index, same subject, two different answers.
func (i *Index) SourcedFindingsFor(group, kind, namespace, name string) []PolicyOutcome {
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
	out := make([]PolicyOutcome, 0, len(src))
	for _, sf := range src {
		out = append(out, PolicyOutcome{
			Subject: Subject{Group: group, Kind: kind, Namespace: namespace, Name: name},
			Finding: sf.Finding,
			Groups:  append([]string(nil), sf.Groups...),
		})
	}
	return out
}

// SourcedFindingsForAnyEngine is FindingsForAnyEngine with the provenance kept.
//
// A caller that both labels its result with an engine name AND authorizes per
// report family needs the two filters together: engine alone over-counts once a
// second producer is installed, family alone attributes another engine's
// findings to Kyverno. The agent-facing surfaces need exactly that pair.
func (i *Index) SourcedFindingsForAnyEngine(group, kind, namespace, name string, engines ...Engine) []PolicyOutcome {
	if len(engines) == 0 {
		return nil
	}
	all := i.SourcedFindingsFor(group, kind, namespace, name)
	if len(all) == 0 {
		return nil
	}
	want := make(map[Engine]bool, len(engines))
	for _, e := range engines {
		want[e] = true
	}
	out := make([]PolicyOutcome, 0, len(all))
	for _, o := range all {
		if want[o.Finding.Engine()] {
			out = append(out, o)
		}
	}
	return out
}

// FindingsForEngine returns the findings indexed for the given subject that
// were produced by the given engine. Same contract as FindingsFor,
// filtered.
//
// Filtering happens on the normalized engine rather than the raw Source
// because one engine emits several source values — asking for
// EngineKyverno must return legacy `kyverno` results and modern
// `KyvernoValidatingPolicy` results alike. A caller that matched on the raw
// string would silently see only part of what Kyverno reported.
func (i *Index) FindingsForEngine(group, kind, namespace, name string, engine Engine) []Finding {
	return i.FindingsForAnyEngine(group, kind, namespace, name, engine)
}

// FindingsForAnyEngine returns the findings indexed for the given subject that
// were produced by any of the given engines.
//
// Use this over FindingsFor wherever the result is labelled with an engine
// name. The index is shared across producers — Kyverno, Trivy, Falco adapters
// and VAP evaluation all write into the same report families — so an
// unfiltered read presented under one engine's name over-counts the moment a
// second engine is installed.
//
// Passing no engines returns nil. An empty filter means "nothing matches"
// rather than "everything", so a caller that accidentally passes an empty set
// gets an obviously empty result instead of silently unfiltered data.
func (i *Index) FindingsForAnyEngine(group, kind, namespace, name string, engines ...Engine) []Finding {
	if len(engines) == 0 {
		return nil
	}
	all := i.FindingsFor(group, kind, namespace, name)
	if len(all) == 0 {
		return nil
	}
	want := make(map[Engine]bool, len(engines))
	for _, e := range engines {
		want[e] = true
	}
	out := make([]Finding, 0, len(all))
	for _, f := range all {
		if want[f.Engine()] {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
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
		fs := make([]Finding, 0, len(src))
		for _, sf := range src {
			fs = append(fs, sf.Finding)
		}
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
//	    source: string                               # producer type, not engine
//	    resources:
//	      - apiVersion, kind, namespace, name, uid     # optional, can be []
//	    ...
func extractFindings(report *unstructured.Unstructured, dst map[string][]sourcedFinding) {
	if report == nil {
		return
	}

	scopeGroup, scopeKind, scopeNS, scopeName := reportScope(report)
	// The report's OWN group — which family it came from — not the subject's.
	reportGroup := resourceid.GroupFromAPIVersion(report.GetAPIVersion())

	rawResults, found, err := unstructured.NestedFieldNoCopy(report.Object, "results")
	if err != nil || !found {
		return
	}
	results, ok := rawResults.([]any)
	if !ok {
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
			Source:   stringField(entry, "source"),
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
				dst[key] = append(dst[key], sourcedFinding{Finding: f, Groups: []string{reportGroup}})
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
			dst[key] = append(dst[key], sourcedFinding{Finding: f, Groups: []string{reportGroup}})
		}
	}
}

// reportScope returns the `report.scope` subject as (group, kind,
// namespace, name). All four are empty strings when scope is missing —
// the caller treats that case as "no scope-only fallback available".
// Group is derived from `scope.apiVersion` (the part before "/"; "" for
// core kinds like Pod whose apiVersion is just "v1").
func reportScope(report *unstructured.Unstructured) (group, kind, namespace, name string) {
	rawScope, found, err := unstructured.NestedFieldNoCopy(report.Object, "scope")
	if err != nil || !found {
		return "", "", "", ""
	}
	scope, ok := rawScope.(map[string]any)
	if !ok {
		return "", "", "", ""
	}
	return resourceid.GroupFromAPIVersion(stringField(scope, "apiVersion")),
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
			group:     resourceid.GroupFromAPIVersion(stringField(m, "apiVersion")),
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
