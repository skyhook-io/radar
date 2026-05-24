package issues

import (
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/policyreports"
)

// fakeProvider — minimal Provider for unit testing. Each field
// pre-stages what the corresponding method returns. Test cases assemble
// one of these and pass it to Compose.
type fakeProvider struct {
	problems     []k8s.Problem
	missingRefs  []k8s.Problem
	capiProblems []k8s.Problem
	events       []*corev1.Event
	dynamic      map[schema.GroupVersionResource][]*unstructured.Unstructured
	kinds        map[schema.GroupVersionResource]string
	namespaced   map[schema.GroupVersionResource]bool
	kyverno      []policyreports.SubjectFindings
	kyvernoStat  string
}

func (f *fakeProvider) DetectProblems(_ []string) []k8s.Problem     { return f.problems }
func (f *fakeProvider) DetectMissingRefs(_ []string) []k8s.Problem  { return f.missingRefs }
func (f *fakeProvider) DetectCAPIProblems(_ []string) []k8s.Problem { return f.capiProblems }
func (f *fakeProvider) WarningEvents(_ []string, _ time.Duration) []*corev1.Event {
	return f.events
}
func (f *fakeProvider) WatchedDynamic() []schema.GroupVersionResource {
	out := make([]schema.GroupVersionResource, 0, len(f.dynamic))
	for g := range f.dynamic {
		out = append(out, g)
	}
	return out
}
func (f *fakeProvider) ListDynamic(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return f.dynamic[gvr], nil
}
func (f *fakeProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	return f.kinds[gvr]
}
func (f *fakeProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool) {
	namespaced, ok := f.namespaced[gvr]
	return namespaced, ok
}
func (f *fakeProvider) KyvernoFindings() []policyreports.SubjectFindings {
	return f.kyverno
}
func (f *fakeProvider) KyvernoStatus() string {
	return f.kyvernoStat
}

func TestCompose_NormalizesProblemSeverity(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Deployment", Namespace: "ns", Name: "a", Severity: "critical", Reason: "down"},
			{Kind: "Deployment", Namespace: "ns", Name: "b", Severity: "high", Reason: "slow"},
			{Kind: "Deployment", Namespace: "ns", Name: "c", Severity: "medium", Reason: "warn"},
		},
	}
	out := Compose(p, Filters{})
	if len(out) != 3 {
		t.Fatalf("got %d issues", len(out))
	}
	bySev := map[Severity]int{}
	for _, i := range out {
		bySev[i.Severity]++
	}
	if bySev[SeverityCritical] != 1 || bySev[SeverityWarning] != 2 {
		t.Fatalf("severity normalization wrong: %+v", bySev)
	}
}

func TestCompose_WarningEventsIncluded(t *testing.T) {
	now := time.Now()
	p := &fakeProvider{
		events: []*corev1.Event{
			{
				ObjectMeta:     metav1.ObjectMeta{Namespace: "ns", Name: "evt-1"},
				InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"},
				Reason:         "FailedMount",
				Message:        "could not mount volume",
				Type:           corev1.EventTypeWarning,
				FirstTimestamp: metav1.Time{Time: now.Add(-2 * time.Minute)},
				LastTimestamp:  metav1.Time{Time: now.Add(-1 * time.Minute)},
				Count:          5,
			},
		},
	}
	// Events are opt-in; IncludeEvents=true is required to surface them
	// from Compose. The default-off behavior is covered separately by
	// TestCompose_EventsExcludedByDefault.
	out := Compose(p, Filters{IncludeEvents: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues", len(out))
	}
	if out[0].Source != SourceEvent {
		t.Fatalf("expected source=event, got %s", out[0].Source)
	}
	if out[0].Count != 5 {
		t.Fatalf("count not propagated: %d", out[0].Count)
	}
}

func TestCompose_EventsExcludedByDefault(t *testing.T) {
	// The default Compose call must NOT surface warning events. Pins
	// the opt-in contract so a future refactor doesn't silently
	// re-enable the event flood on noisy clusters.
	now := time.Now()
	p := &fakeProvider{
		events: []*corev1.Event{{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "ns", Name: "evt-1"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "p"},
			Reason:         "FailedMount",
			Type:           corev1.EventTypeWarning,
			LastTimestamp:  metav1.Time{Time: now},
			Count:          1,
		}},
	}
	out := Compose(p, Filters{})
	if len(out) != 0 {
		t.Fatalf("event leaked through default Compose: %+v", out)
	}
}

func TestCompose_GenericCRDConditionFallback(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "my-app", "namespace": "argocd"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               "Synced",
					"status":             "False",
					"reason":             "OutOfSync",
					"message":            "drift detected",
					"lastTransitionTime": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Application"},
	}
	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1", len(out))
	}
	hit := out[0]
	if hit.Source != SourceCondition {
		t.Fatalf("source: %s", hit.Source)
	}
	if hit.Group != "argoproj.io" {
		t.Fatalf("group not propagated: %+v", hit)
	}
	if hit.Severity != SeverityWarning {
		t.Fatalf("severity: %s", hit.Severity)
	}
	if hit.Reason == "" || hit.Message != "drift detected" {
		t.Fatalf("reason/message: %+v", hit)
	}
}

func TestCompose_CAPIGroupSkippedByGenericFallback(t *testing.T) {
	// Curated CAPI checker owns this group — generic fallback should
	// skip it to avoid double-reporting.
	gvr := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	cl := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "c1"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "X"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {cl}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Cluster"},
	}
	out := Compose(p, Filters{Sources: []Source{SourceCondition}})
	if len(out) != 0 {
		t.Fatalf("CAPI should be skipped by generic fallback: %+v", out)
	}
}

func TestCompose_DropsUnauthorizedClusterScopedIssues(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Deployment", Namespace: "team-a", Name: "api", Severity: "critical", Reason: "down"},
			{Kind: "Node", Name: "worker-1", Severity: "critical", Reason: "not ready"},
		},
	}
	out := Compose(p, Filters{
		CanReadClusterScoped: func(kind, group string) bool {
			if kind != "Node" || group != "" {
				t.Fatalf("unexpected cluster-scoped check: kind=%q group=%q", kind, group)
			}
			return false
		},
	})
	if len(out) != 1 {
		t.Fatalf("expected only namespaced issue, got %+v", out)
	}
	if out[0].Kind != "Deployment" || out[0].Namespace != "team-a" {
		t.Fatalf("wrong issue retained: %+v", out)
	}
}

func TestCompose_DropsUnauthorizedClusterScopedCRDConditions(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	np := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": "default"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "Drifted"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {np}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "NodePool"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	out := Compose(p, Filters{
		Sources: []Source{SourceCondition},
		CanReadClusterScoped: func(kind, group string) bool {
			if kind != "NodePool" || group != "karpenter.sh" {
				t.Fatalf("unexpected cluster-scoped check: kind=%q group=%q", kind, group)
			}
			return false
		},
	})
	if len(out) != 0 {
		t.Fatalf("cluster-scoped CRD condition leaked despite denied access: %+v", out)
	}
}

func TestCompose_KyvernoExcludedByDefault(t *testing.T) {
	p := &fakeProvider{
		kyverno: []policyreports.SubjectFindings{{
			Subject: policyreports.Subject{Kind: "Pod", Namespace: "prod", Name: "web"},
			Findings: []policyreports.Finding{
				{Policy: "require-resource-limits", Result: "fail", Message: "missing cpu limit"},
			},
		}},
	}
	out := Compose(p, Filters{})
	for _, i := range out {
		if i.Source == SourceKyverno {
			t.Fatalf("kyverno should be excluded by default, got: %+v", i)
		}
	}
}

func TestCompose_KyvernoIncludedWhenOptedIn(t *testing.T) {
	p := &fakeProvider{
		kyverno: []policyreports.SubjectFindings{{
			Subject: policyreports.Subject{Kind: "Pod", Namespace: "prod", Name: "web"},
			Findings: []policyreports.Finding{
				{Policy: "require-resource-limits", Result: "fail", Message: "missing cpu limit"},
			},
		}},
	}
	out := Compose(p, Filters{IncludeKyverno: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	got := out[0]
	if got.Source != SourceKyverno {
		t.Fatalf("source: %s", got.Source)
	}
	if got.Severity != SeverityCritical {
		t.Fatalf("fail should map to critical, got %s", got.Severity)
	}
	if got.Kind != "Pod" || got.Namespace != "prod" || got.Name != "web" {
		t.Fatalf("subject not propagated: %+v", got)
	}
	if got.Reason != "require-resource-limits" {
		t.Fatalf("reason should be policy name, got %q", got.Reason)
	}
	if got.Message != "missing cpu limit" {
		t.Fatalf("message not propagated: %q", got.Message)
	}
	if got.Count != 1 {
		t.Fatalf("count should be 1, got %d", got.Count)
	}
}

func TestCompose_KyvernoSeverityMapping(t *testing.T) {
	// fail/error → critical, warn → warning, pass/skip → omitted.
	p := &fakeProvider{
		kyverno: []policyreports.SubjectFindings{{
			Subject: policyreports.Subject{Kind: "Pod", Namespace: "ns", Name: "p"},
			Findings: []policyreports.Finding{
				{Policy: "p1", Rule: "r1", Result: "fail", Message: "fail msg"},
				{Policy: "p2", Rule: "r2", Result: "warn", Message: "warn msg"},
				{Policy: "p3", Rule: "r3", Result: "error", Message: "error msg"},
				{Policy: "p4", Rule: "r4", Result: "pass", Message: "pass msg"},
				{Policy: "p5", Rule: "r5", Result: "skip", Message: "skip msg"},
			},
		}},
	}
	out := Compose(p, Filters{IncludeKyverno: true})
	bySev := map[Severity]int{}
	for _, i := range out {
		bySev[i.Severity]++
	}
	if bySev[SeverityCritical] != 2 {
		t.Fatalf("expected 2 critical (fail+error), got %d: %+v", bySev[SeverityCritical], out)
	}
	if bySev[SeverityWarning] != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", bySev[SeverityWarning], out)
	}
	// pass + skip must not appear.
	for _, i := range out {
		if strings.Contains(i.Message, "pass msg") || strings.Contains(i.Message, "skip msg") {
			t.Fatalf("pass/skip leaked into issues: %+v", i)
		}
	}
}

func TestCompose_KyvernoNamespaceFilter(t *testing.T) {
	p := &fakeProvider{
		kyverno: []policyreports.SubjectFindings{
			{
				Subject:  policyreports.Subject{Kind: "Pod", Namespace: "prod", Name: "web"},
				Findings: []policyreports.Finding{{Policy: "p1", Result: "fail"}},
			},
			{
				Subject:  policyreports.Subject{Kind: "Pod", Namespace: "dev", Name: "api"},
				Findings: []policyreports.Finding{{Policy: "p2", Result: "fail"}},
			},
			{
				// Cluster-scoped: namespace filter must NOT drop this.
				Subject:  policyreports.Subject{Kind: "ClusterRole", Namespace: "", Name: "admin"},
				Findings: []policyreports.Finding{{Policy: "p3", Result: "warn"}},
			},
		},
	}
	out := Compose(p, Filters{
		IncludeKyverno: true,
		Namespaces:     []string{"prod"},
	})
	gotByName := map[string]bool{}
	for _, i := range out {
		gotByName[i.Name] = true
	}
	if !gotByName["web"] {
		t.Fatalf("prod/web should appear: %+v", out)
	}
	if gotByName["api"] {
		t.Fatalf("dev/api should be filtered out: %+v", out)
	}
	if !gotByName["admin"] {
		t.Fatalf("cluster-scoped subject should pass namespace filter: %+v", out)
	}
}

func TestCompose_KyvernoNilFindingsGraceful(t *testing.T) {
	// PolicyReport index returns nil when Kyverno is not installed —
	// that's the common case and must not produce issues or errors.
	p := &fakeProvider{kyverno: nil}
	out := Compose(p, Filters{IncludeKyverno: true})
	for _, i := range out {
		if i.Source == SourceKyverno {
			t.Fatalf("nil findings should not produce kyverno issues: %+v", i)
		}
	}
}

// TestCompose_KyvernoGroupPropagated pins that fromKyverno wires the
// Subject.Group into Issue.Group. Without this, agents and the SPA can't
// tell which CRD a finding belongs to when the Kind is ambiguous (e.g.
// argoproj.io/Application vs another vendor's Application), and the
// SAR-backed CanReadClusterScoped check would query the wrong group.
func TestCompose_KyvernoGroupPropagated(t *testing.T) {
	p := &fakeProvider{
		kyverno: []policyreports.SubjectFindings{
			{
				Subject: policyreports.Subject{
					Group:     "argoproj.io",
					Kind:      "Application",
					Namespace: "prod",
					Name:      "myapp",
				},
				Findings: []policyreports.Finding{
					{Policy: "no-sync-loop", Result: "fail", Message: "sync loop"},
				},
			},
			{
				// Core kind: empty group must pass through (not silently
				// replaced with anything else).
				Subject: policyreports.Subject{
					Group:     "",
					Kind:      "Pod",
					Namespace: "prod",
					Name:      "web",
				},
				Findings: []policyreports.Finding{
					{Policy: "require-resource-limits", Result: "fail"},
				},
			},
		},
	}
	out := Compose(p, Filters{IncludeKyverno: true})
	if len(out) != 2 {
		t.Fatalf("expected 2 issues, got %d: %+v", len(out), out)
	}
	byKind := map[string]Issue{}
	for _, i := range out {
		byKind[i.Kind] = i
	}
	if app, ok := byKind["Application"]; !ok || app.Group != "argoproj.io" {
		t.Errorf("Application Group not propagated: %+v", app)
	}
	if pod, ok := byKind["Pod"]; !ok || pod.Group != "" {
		t.Errorf("Pod Group should be empty: %+v", pod)
	}
}

func TestCompose_KyvernoSourceListNarrowsButDoesNotOptIn(t *testing.T) {
	// Pins the documented contract: `Sources` is a FILTER, not an
	// additive opt-in. The list narrows the response to the named
	// sources but does NOT enable collection of noisy sources —
	// IncludeKyverno (set by the HTTP/MCP handlers) is what gates
	// kyverno emission. With Sources={kyverno} and IncludeKyverno=false
	// the response is empty. With IncludeKyverno=true the response is
	// kyverno-only (problem source is filtered out because it isn't in
	// Sources) — i.e. source=kyverno returns ONLY kyverno rows, not
	// "defaults plus kyverno".
	p := &fakeProvider{
		kyverno: []policyreports.SubjectFindings{{
			Subject:  policyreports.Subject{Kind: "Pod", Namespace: "ns", Name: "p"},
			Findings: []policyreports.Finding{{Policy: "p1", Result: "fail"}},
		}},
		problems: []k8s.Problem{
			{Kind: "Pod", Namespace: "ns", Name: "p", Severity: "critical", Reason: "x"},
		},
	}
	// Sources={kyverno} but IncludeKyverno=false → no kyverno emission,
	// and problem source filtered out → empty.
	out := Compose(p, Filters{Sources: []Source{SourceKyverno}})
	if len(out) != 0 {
		t.Fatalf("expected 0 issues without IncludeKyverno, got %+v", out)
	}
	// With IncludeKyverno=true and Sources={kyverno} → only kyverno.
	out = Compose(p, Filters{Sources: []Source{SourceKyverno}, IncludeKyverno: true})
	if len(out) != 1 || out[0].Source != SourceKyverno {
		t.Fatalf("expected only kyverno, got %+v", out)
	}
}

func TestCompose_SeveritySortedDescending(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "warn1", Severity: "high"},
			{Kind: "Pod", Name: "crit1", Severity: "critical"},
			{Kind: "Pod", Name: "warn2", Severity: "medium"},
		},
	}
	out := Compose(p, Filters{})
	if out[0].Name != "crit1" {
		t.Fatalf("critical should sort first, got %+v", out[0])
	}
}

func TestCompose_SeverityFilter(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "a", Severity: "critical"},
			{Kind: "Pod", Name: "b", Severity: "medium"},
		},
	}
	out := Compose(p, Filters{Severities: []Severity{SeverityCritical}})
	if len(out) != 1 || out[0].Name != "a" {
		t.Fatalf("severity filter wrong: %+v", out)
	}
}

func TestCompose_KindFilter(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "p", Severity: "critical"},
			{Kind: "Deployment", Name: "d", Severity: "critical"},
		},
	}
	out := Compose(p, Filters{Kinds: []string{"Pod"}})
	if len(out) != 1 || out[0].Kind != "Pod" {
		t.Fatalf("kind filter wrong: %+v", out)
	}
}

func TestCompose_LimitTruncates(t *testing.T) {
	probs := make([]k8s.Problem, 0, 50)
	for i := 0; i < 50; i++ {
		probs = append(probs, k8s.Problem{Kind: "Pod", Name: "p", Severity: "critical"})
	}
	p := &fakeProvider{problems: probs}
	out := Compose(p, Filters{Limit: 10})
	if len(out) != 10 {
		t.Fatalf("limit not honored: %d", len(out))
	}
}

func TestCompose_DeterministicOrderForTies(t *testing.T) {
	// Same severity + same last-seen → tiebreak on (kind, ns, name).
	// All hits are critical, all DurationSeconds=0, so LastSeen ties.
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Service", Namespace: "ns", Name: "z", Severity: "critical"},
			{Kind: "Pod", Namespace: "ns", Name: "a", Severity: "critical"},
			{Kind: "Pod", Namespace: "ns", Name: "b", Severity: "critical"},
		},
	}
	out := Compose(p, Filters{})
	got := []string{out[0].Kind + "/" + out[0].Name, out[1].Kind + "/" + out[1].Name, out[2].Kind + "/" + out[2].Name}
	want := []string{"Pod/a", "Pod/b", "Service/z"} // Pod < Service alphabetically
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("tiebreak order: got %v want %v", got, want)
	}
}

// silence unused-import lint when sort isn't used elsewhere
var _ = sort.Strings

// flattenNamespacedProblems exists to keep CacheProvider's per-
// namespace fan-out from leaking + duplicating cluster-scoped
// problems (Node, etc.). These tests pin that contract.

func TestFlattenNamespacedProblems_DropsClusterScopedEntries(t *testing.T) {
	// Each per-namespace list as returned by k8s.DetectProblems
	// includes the cluster-scoped Node block — without filtering, a
	// namespace-bounded caller asking for {ns1, ns2} would see Node
	// problems twice AND see them at all (RBAC violation if the user
	// lacks `list nodes` at cluster scope).
	perNs := [][]k8s.Problem{
		{
			{Kind: "Pod", Namespace: "ns1", Name: "p1", Severity: "critical"},
			{Kind: "Node", Name: "node-1", Severity: "high"}, // empty Namespace
		},
		{
			{Kind: "Pod", Namespace: "ns2", Name: "p2", Severity: "critical"},
			{Kind: "Node", Name: "node-1", Severity: "high"}, // dup leak
		},
	}
	out := flattenNamespacedProblems(perNs)
	if len(out) != 2 {
		t.Fatalf("want 2 namespaced problems, got %d: %+v", len(out), out)
	}
	for _, p := range out {
		if p.Kind == "Node" {
			t.Errorf("Node problem leaked through namespace-scoped flatten: %+v", p)
		}
		if p.Namespace == "" {
			t.Errorf("cluster-scoped problem leaked: %+v", p)
		}
	}
}

func TestFlattenNamespacedProblems_PreservesNamespacedAcrossSlices(t *testing.T) {
	// Namespaced rows from different per-namespace calls all survive
	// — no over-zealous dedup.
	perNs := [][]k8s.Problem{
		{{Kind: "Pod", Namespace: "ns1", Name: "a"}},
		{{Kind: "Pod", Namespace: "ns2", Name: "a"}}, // same name, different ns
		{{Kind: "Service", Namespace: "ns3", Name: "svc"}},
	}
	out := flattenNamespacedProblems(perNs)
	if len(out) != 3 {
		t.Fatalf("want 3 problems preserved, got %d: %+v", len(out), out)
	}
}

func TestFlattenNamespacedProblems_EmptyInputReturnsNil(t *testing.T) {
	if out := flattenNamespacedProblems(nil); len(out) != 0 {
		t.Errorf("nil input should produce empty output, got %+v", out)
	}
	if out := flattenNamespacedProblems([][]k8s.Problem{}); len(out) != 0 {
		t.Errorf("empty input should produce empty output, got %+v", out)
	}
}

// countingProvider wraps fakeProvider and tallies ListDynamic calls per
// GVR. Used by TestDetectGenericCRDIssues_SkipsListWhenKindFiltered to
// pin that detectGenericCRDIssues short-circuits the per-GVR
// ListDynamic call when f.Kinds excludes the GVR's kind — on clusters
// with hundreds of watched CRDs, scanning every one for a pods-only
// summaryContext request was the dominant cost.
type countingProvider struct {
	fakeProvider
	listCalls map[schema.GroupVersionResource]int
}

func (c *countingProvider) ListDynamic(gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if c.listCalls == nil {
		c.listCalls = map[schema.GroupVersionResource]int{}
	}
	c.listCalls[gvr]++
	return c.fakeProvider.ListDynamic(gvr, ns)
}

// TestDetectGenericCRDIssues_SkipsListWhenKindFiltered pins the
// "scan all CRDs before kindFilter applies" perf fix in
// detectGenericCRDIssues. Pre-fix, a Compose call with Kinds=["Pod"]
// still iterated every watched CRD GVR and ran ListDynamic on each;
// applyFilters then discarded the non-matching rows at the end.
//
// On a cluster with hundreds of watched CRDs this dominated the
// summaryContext per-row index build for list_resources kind=pods.
// The fix routes f.Kinds awareness into detectGenericCRDIssues so
// non-matching GVRs skip the ListDynamic call entirely.
func TestDetectGenericCRDIssues_SkipsListWhenKindFiltered(t *testing.T) {
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	npGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}

	p := &countingProvider{
		fakeProvider: fakeProvider{
			dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
				podGVR: {}, // empty — only counts the call.
				appGVR: {{Object: map[string]any{
					"metadata": map[string]any{"name": "a", "namespace": "argocd"},
					"status": map[string]any{
						"conditions": []any{
							map[string]any{"type": "Synced", "status": "False", "reason": "Drift"},
						},
					},
				}}},
				npGVR: {}, // empty — only counts the call.
			},
			kinds: map[schema.GroupVersionResource]string{
				podGVR: "Pod",
				appGVR: "Application",
				npGVR:  "NodePool",
			},
		},
	}

	// kindFilter restricts to Application — the other two GVRs must NOT
	// be listed. detectGenericCRDIssues lowercases the kind comparison
	// (mirrors applyFilters), so the canonical "Application" matches the
	// emitted Kind for the argoproj.io GVR.
	_ = detectGenericCRDIssues(p, Filters{Kinds: []string{"Application"}})

	if got := p.listCalls[podGVR]; got != 0 {
		t.Errorf("Pod GVR ListDynamic calls = %d, want 0 (kind filter must skip non-matching GVRs)", got)
	}
	if got := p.listCalls[npGVR]; got != 0 {
		t.Errorf("NodePool GVR ListDynamic calls = %d, want 0 (kind filter must skip non-matching GVRs)", got)
	}
	if got := p.listCalls[appGVR]; got == 0 {
		t.Errorf("Application GVR ListDynamic calls = %d, want >= 1 (matching kind must still be scanned)", got)
	}

	// Sanity: empty Kinds filter scans every GVR (no per-kind shortcut
	// when caller didn't ask for one). Pins that the fix is filter-aware
	// rather than always-skip.
	p.listCalls = nil
	_ = detectGenericCRDIssues(p, Filters{})
	for gvr, want := range map[schema.GroupVersionResource]bool{podGVR: true, appGVR: true, npGVR: true} {
		if got := p.listCalls[gvr] > 0; got != want {
			t.Errorf("no kind filter: GVR %s called=%v, want %v", gvr.Resource, got, want)
		}
	}
}
