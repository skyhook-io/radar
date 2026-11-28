package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGenericPhaseErroredIsUnhealthyAndAbortedIsNot(t *testing.T) {
	for phase, want := range map[string]HealthStatus{"Errored": StatusUnhealthy, "Failed": StatusUnhealthy, "Aborted": StatusUnknown} {
		obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kargo.akuity.io/v1alpha1", "kind": "Promotion", "status": map[string]any{"phase": phase}}}
		if got := extractGenericStatus(obj); got != want {
			t.Errorf("%s: %s, want %s", phase, got, want)
		}
	}
}
