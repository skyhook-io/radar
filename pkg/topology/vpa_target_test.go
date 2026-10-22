package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestVPATargetPreservesDeclaredAndVersionlessBuiltinIdentity(t *testing.T) {
	vpaGVR := schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	vpa := func(name, kind, version, targetName string) *unstructured.Unstructured {
		obj := genericIdentityObject(vpaGVR, "VerticalPodAutoscaler", "team", name)
		obj.Object["spec"] = map[string]any{"targetRef": map[string]any{"kind": kind, "apiVersion": version, "name": targetName}}
		return obj
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{vpaGVR, rolloutGVR},
		kinds:   map[schema.GroupVersionResource]string{vpaGVR: "VerticalPodAutoscaler", rolloutGVR: "Rollout"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			vpaGVR: {
				vpa("implicit", "Deployment", "", "web"),
				vpa("explicit", "Deployment", "apps/v1", "web"),
				vpa("bare-rollout", "Rollout", "", "worker"),
				vpa("argo-rollout", "Rollout", "argoproj.io/v1alpha1", "worker"),
			},
			rolloutGVR: {genericIdentityObject(rolloutGVR, "Rollout", "team", "worker")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct{ name, group string }{{"web", "apps"}, {"web", "apps"}, {}, {"worker", "argoproj.io"}} {
		obj := dynamic.resources[vpaGVR][i]
		rel := GetRelationshipsWithObject("VerticalPodAutoscaler", "team", obj.GetName(), obj, topo, provider, dynamic, nil)
		if want.name == "" {
			if rel != nil && rel.ScaleTarget != nil {
				t.Errorf("%s acquired an unresolvable scale target: %+v", obj.GetName(), rel.ScaleTarget)
			}
			continue
		}
		if rel == nil || rel.ScaleTarget == nil || rel.ScaleTarget.Name != want.name || rel.ScaleTarget.Group != want.group {
			t.Errorf("%s target = %+v, want %+v", obj.GetName(), rel, want)
		}
	}
	rel := GetRelationshipsWithObject("Deployment", "team", "web", provider.deployments[0], topo, provider, dynamic, nil)
	if rel == nil || len(rel.Scalers) != 2 {
		t.Fatalf("versionless and explicit VPAs must appear on the Deployment: %+v", rel)
	}
	for _, ref := range rel.Scalers {
		if ref.Group != "autoscaling.k8s.io" {
			t.Errorf("autoscaler lost its group: %+v", ref)
		}
	}
}
