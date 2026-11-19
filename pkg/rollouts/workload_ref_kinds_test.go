package rollouts

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Every kind WorkloadRefKinds lists resolves, in its own group, and nothing
// else does.
func TestWorkloadRefKindsAreWhatResolves(t *testing.T) {
	for _, gk := range WorkloadRefKinds() {
		ro := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"workloadRef": map[string]any{"apiVersion": gk.WithVersion("v1").GroupVersion().String(), "kind": gk.Kind, "name": "web"}}}}
		if _, err := ResolveTemplateTarget(ro); err != nil {
			t.Errorf("%v doesn't resolve: %v", gk, err)
		}
	}
	if len(WorkloadRefKinds()) != len(workloadRefTargets) {
		t.Fatalf("kinds = %v", WorkloadRefKinds())
	}
}
