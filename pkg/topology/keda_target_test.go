package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestKEDAScaleTargetAppliesOmittedAPIVersionDefault(t *testing.T) {
	scaledObjectGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	scaledObject := func(name string, target map[string]any) *unstructured.Unstructured {
		so := genericIdentityObject(scaledObjectGVR, "ScaledObject", "team", name)
		so.Object["spec"] = map[string]any{"scaleTargetRef": target}
		return so
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{scaledObjectGVR, rolloutGVR},
		kinds:   map[schema.GroupVersionResource]string{scaledObjectGVR: "ScaledObject", rolloutGVR: "Rollout"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			scaledObjectGVR: {
				scaledObject("defaults", map[string]any{"name": "web"}),
				scaledObject("bare-rollout", map[string]any{"kind": "Rollout", "name": "worker"}),
				scaledObject("argo-rollout", map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "name": "worker"}),
				scaledObject("statefulset", map[string]any{"kind": "StatefulSet", "name": "db"}),
			},
			rolloutGVR: {genericIdentityObject(rolloutGVR, "Rollout", "team", "worker")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{
		deployments:  []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}},
		statefulSets: []*appsv1.StatefulSet{{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "team"}}},
	}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := map[string]bool{}
	for _, edge := range topo.Edges {
		edges[edge.Source+" -> "+edge.Target] = true
	}
	for _, want := range []string{"scaledobject/team/defaults -> deployment/team/web", "scaledobject/team/argo-rollout -> rollout/team/worker", "scaledobject/team/statefulset -> statefulset/team/db"} {
		if !edges[want] {
			t.Errorf("missing edge %s in %v", want, edges)
		}
	}
	if edges["scaledobject/team/bare-rollout -> rollout/team/worker"] {
		t.Error("a Rollout target with omitted apiVersion (apps/v1) was joined to the Argo Rollout")
	}
}
