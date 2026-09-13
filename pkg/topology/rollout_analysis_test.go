package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestActiveAnalysisRuns_AllFourTriggers(t *testing.T) {
	status := map[string]any{
		"canary": map[string]any{
			"currentStepAnalysisRunStatus":       map[string]any{"name": "ar-step", "status": "Inconclusive", "message": "no verdict"},
			"currentBackgroundAnalysisRunStatus": map[string]any{"name": "ar-bg", "status": "Successful"},
		},
		"blueGreen": map[string]any{
			"prePromotionAnalysisRunStatus":  map[string]any{"name": "ar-pre", "status": "Running"},
			"postPromotionAnalysisRunStatus": map[string]any{"name": "ar-post", "status": "Failed"},
		},
	}

	runs := activeAnalysisRuns(status)
	if len(runs) != 4 {
		t.Fatalf("got %d runs, want 4: %+v", len(runs), runs)
	}

	byTrigger := map[string]rolloutAnalysisRunRef{}
	for _, run := range runs {
		byTrigger[run.trigger] = run
	}
	for _, want := range []struct{ trigger, name, phase string }{
		{"step", "ar-step", "Inconclusive"},
		{"background", "ar-bg", "Successful"},
		{"pre-promotion", "ar-pre", "Running"},
		{"post-promotion", "ar-post", "Failed"},
	} {
		got, present := byTrigger[want.trigger]
		if !present {
			t.Errorf("trigger %q missing", want.trigger)
			continue
		}
		if got.name != want.name || got.phase != want.phase {
			t.Errorf("trigger %q = {%s %s}, want {%s %s}", want.trigger, got.name, got.phase, want.name, want.phase)
		}
	}
	if byTrigger["step"].message != "no verdict" {
		t.Errorf("message not carried through: %q", byTrigger["step"].message)
	}
}

// A slot with no name is a slot the controller has not populated — graphing it
// would produce a node pointing at no resource.
func TestActiveAnalysisRuns_SkipsUnnamedSlots(t *testing.T) {
	cases := []struct {
		name   string
		status map[string]any
	}{
		{"empty status", map[string]any{}},
		{"nil canary", map[string]any{"canary": nil}},
		{"slot present but nameless", map[string]any{
			"canary": map[string]any{"currentStepAnalysisRunStatus": map[string]any{"status": "Running"}}}},
		{"empty name", map[string]any{
			"canary": map[string]any{"currentStepAnalysisRunStatus": map[string]any{"name": "", "status": "Running"}}}},
		{"wrong shape", map[string]any{
			"canary": map[string]any{"currentStepAnalysisRunStatus": "not-a-map"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runs := activeAnalysisRuns(tc.status); len(runs) != 0 {
				t.Fatalf("got %d runs, want none: %+v", len(runs), runs)
			}
		})
	}
}

func TestAnalysisRunHealth(t *testing.T) {
	for phase, want := range map[string]HealthStatus{
		"Successful":   StatusHealthy,
		"Running":      StatusDegraded,
		"Pending":      StatusDegraded,
		"Inconclusive": StatusDegraded,
		"Failed":       StatusUnhealthy,
		"Error":        StatusUnhealthy,
		"":             StatusUnknown,
		"Weird":        StatusUnknown,
	} {
		if got := analysisRunHealth(phase); got != want {
			t.Errorf("analysisRunHealth(%q) = %q, want %q", phase, got, want)
		}
	}
}

// EdgeUses means HPA/VPA/KEDA — relationships.go files it under Autoscaler /
// Scale Target, so a Rollout rendered as its own AnalysisRun's autoscaler.
func TestRolloutManagesItsAnalysisRuns(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{"replicas": int64(3)},
		"status": map[string]any{
			"phase": "Paused",
			"canary": map[string]any{
				"currentStepAnalysisRunStatus": map[string]any{"name": "web-abc-1", "status": "Inconclusive"},
			},
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{
		gvr:      schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"},
		rollouts: []*unstructured.Unstructured{rollout},
	}

	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	runID := "analysisrun/prod/web-abc-1"
	if findNode(topo, runID) == nil {
		t.Fatalf("missing AnalysisRun node %q; nodes=%+v", runID, topo.Nodes)
	}

	var found *Edge
	for i := range topo.Edges {
		if topo.Edges[i].Target == runID {
			found = &topo.Edges[i]
		}
	}
	if found == nil {
		t.Fatalf("no edge reaches %q; edges=%+v", runID, topo.Edges)
	}
	if found.Type != EdgeManages {
		t.Errorf("edge type = %q, want %q (ownership, not scaling)", found.Type, EdgeManages)
	}
	if found.Label != "step" {
		t.Errorf("edge label = %q, want the trigger %q", found.Label, "step")
	}
}

func TestAnalysisRunIsExcludedFromGenericCRDPass(t *testing.T) {
	if !genericCRDExcluded(schema.GroupVersionResource{Group: "argoproj.io"}, "AnalysisRun") {
		t.Fatal("analysisrun must be excluded from the generic owner-ref pass")
	}
	if !genericCRDExcluded(schema.GroupVersionResource{Group: "argoproj.io"}, "Rollout") {
		t.Fatal("rollout lost its exclusion")
	}
}
