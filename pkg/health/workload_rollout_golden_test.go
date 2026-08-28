package health

import (
	"encoding/json"
	"os"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type rolloutGoldenVector struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Phase     RolloutPhase    `json:"phase"`
	Active    bool            `json:"active"`
	Manual    bool            `json:"manual"`
	Label     string          `json:"label"`
	Detail    string          `json:"detail"`
	Desired   int32           `json:"desired"`
	Updated   int32           `json:"updated"`
	Ready     int32           `json:"ready"`
	Available int32           `json:"available"`
	Object    json.RawMessage `json:"object"`
}

func TestWorkloadRolloutGoldenVectorsCrossLang(t *testing.T) {
	raw, err := os.ReadFile("testdata/workload_rollout_vectors.json")
	if err != nil {
		t.Fatalf("read rollout vectors: %v", err)
	}
	var file struct {
		Vectors []rolloutGoldenVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse rollout vectors: %v", err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("no rollout vectors loaded")
	}
	for _, vector := range file.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			got := classifyRolloutGolden(t, vector.Kind, vector.Object)
			if got.Phase != vector.Phase || got.Active != vector.Active || got.Manual != vector.Manual || got.Label != vector.Label || got.Detail != vector.Detail || got.Desired != vector.Desired || got.Updated != vector.Updated || got.Ready != vector.Ready || got.Available != vector.Available {
				t.Fatalf("got %#v, want phase=%q active=%t manual=%t label=%q detail=%q counts=%d/%d/%d/%d", got, vector.Phase, vector.Active, vector.Manual, vector.Label, vector.Detail, vector.Desired, vector.Updated, vector.Ready, vector.Available)
			}
		})
	}
}

func classifyRolloutGolden(t *testing.T, kind string, raw json.RawMessage) WorkloadRolloutActivity {
	t.Helper()
	switch kind {
	case "Deployment":
		var obj appsv1.Deployment
		mustUnmarshal(t, raw, &obj)
		return WorkloadRollout(&obj)
	case "StatefulSet":
		var obj appsv1.StatefulSet
		mustUnmarshal(t, raw, &obj)
		return WorkloadRollout(&obj)
	case "DaemonSet":
		var obj appsv1.DaemonSet
		mustUnmarshal(t, raw, &obj)
		return WorkloadRollout(&obj)
	case "Rollout":
		var obj unstructured.Unstructured
		mustUnmarshal(t, raw, &obj)
		return WorkloadRollout(&obj)
	default:
		t.Fatalf("unsupported rollout vector kind %q", kind)
		return WorkloadRolloutActivity{}
	}
}
