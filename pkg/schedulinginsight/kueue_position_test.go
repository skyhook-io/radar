package schedulinginsight

import (
	"encoding/json"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestKueueWorkloadEligibleForPendingPositionVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/kueue_awaiting_admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Cases []struct {
			Name     string         `json:"name"`
			Workload map[string]any `json:"workload"`
			Awaiting bool           `json:"awaiting"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, version := range []string{"v1beta1", "v1beta2"} {
		for _, tc := range vectors.Cases {
			u := &unstructured.Unstructured{Object: tc.Workload}
			u.SetAPIVersion("kueue.x-k8s.io/" + version)
			u.SetKind("Workload")
			if got := KueueWorkloadEligibleForPendingPosition(u); got != tc.Awaiting {
				t.Errorf("%s/%s: got %v, want %v", version, tc.Name, got, tc.Awaiting)
			}
		}
	}
}

func TestKueueWorkloadEligibleForPendingPositionRejectsOtherKinds(t *testing.T) {
	for _, apiVersion := range []string{"visibility.kueue.x-k8s.io/v1beta2", "batch/v1"} {
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": "Workload", "spec": map[string]any{"queueName": "q"}}}
		if KueueWorkloadEligibleForPendingPosition(u) {
			t.Errorf("%s Workload must not be treated as a Kueue Workload", apiVersion)
		}
	}
}
