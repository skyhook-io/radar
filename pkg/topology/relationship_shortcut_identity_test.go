package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestRelationshipShortcutsDoNotClaimCustomCoreKinds(t *testing.T) {
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "operator"}}},
		pods:        []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "pod"}, Spec: corev1.PodSpec{NodeName: "shared"}}},
		pvcs:        []*corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "shared"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "shared"}}},
		pvs:         []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "shared"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "shared", ClaimRef: &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: "app", Name: "shared"}}}},
	}
	for _, tc := range []struct{ kind, plural, namespace string }{{"Node", "nodes", ""}, {"PersistentVolume", "persistentvolumes", ""}, {"PersistentVolumeClaim", "persistentvolumeclaims", "app"}, {"StorageClass", "storageclasses", ""}} {
		t.Run(tc.kind, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "custom.example.io", Version: "v1", Resource: tc.plural}
			obj := genericIdentityObject(gvr, tc.kind, tc.namespace, "shared", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator"})
			if tc.namespace == "" {
				obj.SetOwnerReferences(nil)
			}
			d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: tc.kind}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {obj}}, listCalls: map[schema.GroupVersionResource]int{}}
			topo, err := NewBuilder(provider).WithDynamic(d).Build(DefaultBuildOptions())
			if err != nil {
				t.Fatal(err)
			}
			rel := GetRelationshipsWithObject(tc.plural, tc.namespace, "shared", obj, topo, provider, d, nil)
			if rel != nil && (len(rel.Pods) > 0 || len(rel.Children) > 0 || len(rel.Consumers) > 0 || len(rel.ConfigRefs) > 0) {
				t.Errorf("custom %s borrowed core shortcuts: %+v", tc.kind, rel)
			}
			core := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": tc.kind, "metadata": map[string]any{"name": "shared", "namespace": tc.namespace}}}
			if tc.kind == "StorageClass" {
				core.SetAPIVersion("storage.k8s.io/v1")
			}
			coreRel := GetRelationshipsWithObject(tc.plural, tc.namespace, "shared", core, topo, provider, d, nil)
			if coreRel == nil {
				t.Fatal("core shortcut was lost")
			}
			switch tc.kind {
			case "Node":
				if len(coreRel.Pods) != 1 {
					t.Errorf("core Node pods=%v", coreRel.Pods)
				}
			case "PersistentVolumeClaim", "StorageClass":
				if len(coreRel.Children) != 1 {
					t.Errorf("core %s children=%v", tc.kind, coreRel.Children)
				}
			case "PersistentVolume":
				if len(coreRel.Consumers) != 1 || len(coreRel.ConfigRefs) != 1 {
					t.Errorf("core PV=%+v", coreRel)
				}
			}
		})
	}
}
