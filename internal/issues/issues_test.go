package issues

import (
	"sort"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// fakeProvider — minimal Provider for unit testing. Each field
// pre-stages what the corresponding method returns. Test cases assemble
// one of these and pass it to Compose.
type fakeProvider struct {
	problems     []k8s.Problem
	capiProblems []k8s.Problem
	audit        []bp.Finding
	events       []*corev1.Event
	dynamic      map[schema.GroupVersionResource][]*unstructured.Unstructured
	kinds        map[schema.GroupVersionResource]string
	namespaced   map[schema.GroupVersionResource]bool
}

func (f *fakeProvider) DetectProblems(_ []string) []k8s.Problem     { return f.problems }
func (f *fakeProvider) DetectCAPIProblems(_ []string) []k8s.Problem { return f.capiProblems }
func (f *fakeProvider) AuditFindings(_ []string) []bp.Finding       { return f.audit }
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

func TestCompose_AuditExcludedByDefault(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "p", Severity: "critical", Reason: "x"},
		},
		audit: []bp.Finding{
			{Kind: "Pod", Name: "p", CheckID: "no-resource-limits", Severity: bp.SeverityWarning, Message: "no limits"},
		},
	}
	out := Compose(p, Filters{})
	for _, i := range out {
		if i.Source == SourceAudit {
			t.Fatal("audit should be excluded by default")
		}
	}
	out = Compose(p, Filters{IncludeAudit: true})
	hasAudit := false
	for _, i := range out {
		if i.Source == SourceAudit {
			hasAudit = true
		}
	}
	if !hasAudit {
		t.Fatal("audit should be included when IncludeAudit=true")
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
	// Events are opt-in (analogous to audit); IncludeEvents=true is
	// required to surface them from Compose. The default-off behavior
	// is covered separately by TestCompose_EventsExcludedByDefault.
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
	// the audit-style opt-in contract so a future refactor doesn't
	// silently re-enable the event flood on noisy clusters.
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
