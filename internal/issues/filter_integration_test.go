package issues

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/k8s"
)

// TestCompose_GroupedCELCountMatchesMemberTotal pins that `count > N` evaluates
// against the GROUPED member total, not the always-1 flat evidence count. Six
// pods of one Deployment fold to a single grouped issue with count=6.
func TestCompose_GroupedCELCountMatchesMemberTotal(t *testing.T) {
	probs := make([]k8s.Detection, 0, 6)
	for i := 0; i < 6; i++ {
		probs = append(probs, k8s.Detection{
			Kind: "Pod", Namespace: "ns", Name: fmt.Sprintf("web-%d", i),
			Severity: "critical", Reason: "CrashLoopBackOff",
			OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web",
		})
	}
	p := &fakeProvider{problems: probs}
	f, err := filter.CompileIssueFilter(`count > 5`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ComposeWithStats(p, Filters{Grouped: true, Filter: f})
	if len(out) != 1 || out[0].Count != 6 {
		t.Fatalf("count>5 should match the 6-pod grouped issue (count=6), got %+v", out)
	}
}

// Filter integration tests — exercise ComposeWithStats with a compiled
// CEL filter, covering match/drop, eval-error stats, and the
// post-filter ordering invariant that limit applies last.

func TestCompose_WithCELFilter_MatchesAndDrops(t *testing.T) {
	// Two problem rows; a reason predicate should keep only the match.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "crash", Severity: "critical", Reason: "CrashLoopBackOff"},
			{Kind: "Pod", Namespace: "ns", Name: "oom", Severity: "critical", Reason: "OOMKilled"},
		},
	}
	f, err := filter.CompileIssueFilter(`reason == "OOMKilled"`)
	if err != nil {
		t.Fatal(err)
	}
	out, stats := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 1 || out[0].Name != "oom" {
		t.Fatalf("expected single OOMKilled hit, got %+v", out)
	}
	if stats.FilterErrors != 0 {
		t.Errorf("clean filter, expected no eval errors, got %d", stats.FilterErrors)
	}
}

func TestCompose_FilterAppliedBeforeLimit(t *testing.T) {
	// Many problem issues + one filter-matching issue; limit=10 must
	// see all 50 critical problems, the filter narrows to a smaller
	// set, and limit caps that. Wrong order (limit-before-filter)
	// would discard issues silently.
	probs := make([]k8s.Detection, 0, 50)
	for i := 0; i < 50; i++ {
		probs = append(probs, k8s.Detection{Kind: "Pod", Namespace: "warn-ns", Name: "p", Severity: "high"})
	}
	probs = append(probs, k8s.Detection{Kind: "Pod", Namespace: "crit-ns", Name: "critical-one", Severity: "critical"})
	p := &fakeProvider{problems: probs}
	f, err := filter.CompileIssueFilter(`severity == "critical"`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ComposeWithStats(p, Filters{Filter: f, Limit: 10})
	if len(out) != 1 {
		t.Fatalf("filter should leave 1 critical issue, got %d", len(out))
	}
	if out[0].Name != "critical-one" {
		t.Errorf("filter dropped the critical one: %+v", out)
	}
}

func TestCompose_WithCELFilter_SourceBinding(t *testing.T) {
	// The `source=` query param was removed; the CEL `source` binding is now
	// the ONLY way to slice issues by detector (documented migration path in
	// the HTTP handler + MCP tool schema). Guard that the binding exists and
	// slices correctly across two distinct sources.
	// Non-curated CRD (KEDA ScaledObject) for the generic condition row, so it
	// reaches SourceCondition (Argo/Flux now route through the GitOps detector
	// under SourceProblem).
	gvr := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "keda.sh/v1alpha1",
		"kind":       "ScaledObject",
		"metadata":   map[string]any{"name": "my-app", "namespace": "apps"},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": "ScalerFailed", "message": "drift"},
		}},
	}}
	p := &fakeProvider{
		problems: []k8s.Detection{{Kind: "Deployment", Namespace: "argocd", Name: "api", Severity: "critical", Reason: "down"}},
		dynamic:  map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}},
		kinds:    map[schema.GroupVersionResource]string{gvr: "ScaledObject"},
	}
	f, err := filter.CompileIssueFilter(`source == "condition"`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 1 || out[0].Source != SourceCondition {
		t.Fatalf("source==\"condition\" should keep only the condition row, got %+v", out)
	}
}

func TestCompose_WithCELFilter_CategoryBinding(t *testing.T) {
	// category + category_group are filterable bindings, not just output
	// labels — the UI facet and agents slice on them. Guard that both
	// compile and match against the derived classification.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "img", Severity: "critical", Reason: "ImagePullBackOff"},
			{Kind: "Pod", Namespace: "ns", Name: "crash", Severity: "critical", Reason: "CrashLoopBackOff"},
		},
	}
	f, err := filter.CompileIssueFilter(`category == "image_pull_failed"`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 1 || out[0].Name != "img" {
		t.Fatalf(`category=="image_pull_failed" should keep only the image-pull row, got %+v`, out)
	}

	f, err = filter.CompileIssueFilter(`category_group == "startup"`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ = ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 1 || out[0].Name != "img" {
		t.Fatalf(`category_group=="startup" should keep only the startup row, got %+v`, out)
	}
}

func TestCompose_WithCELFilter_DiagnosisBindings(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{
				Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "broken-sync",
				Severity: "critical", Reason: "OperationFailed",
				Cause: "The destination namespace does not exist.", RemediationKind: "create-namespace", RemediationTarget: "demo",
			},
			{Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "other", Severity: "critical", Reason: "OperationFailed"},
		},
	}
	f, err := filter.CompileIssueFilter(`remediation_kind == "create-namespace" && remediation_target == "demo" && cause.contains("namespace")`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 1 || out[0].Name != "broken-sync" {
		t.Fatalf("diagnosis filter should keep only the structured-remediation row, got %+v", out)
	}
}

func TestCompose_FilterEvalError_StatsPopulated(t *testing.T) {
	// Reference an unbound-but-syntactically-valid path that won't
	// resolve on any actual issue row — the dyn-typed env declares
	// these as known types, so the failure is at eval not compile.
	// (Using nonsense int comparison to force the error.)
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Name: "p", Severity: "warning"},
		},
	}
	f, err := filter.CompileIssueFilter(`count > int(severity)`)
	if err != nil {
		t.Fatal(err)
	}
	out, stats := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 0 {
		t.Errorf("expected eval errors to drop the row, got %+v", out)
	}
	if stats.FilterErrors == 0 {
		t.Error("expected FilterErrors > 0 so agents can self-correct")
	}
	if stats.FilterErrorSample == "" {
		t.Error("expected FilterErrorSample populated")
	}
}
