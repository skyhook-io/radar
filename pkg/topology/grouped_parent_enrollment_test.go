package topology

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"testing"
)

func groupedParentOwnerRef(apiVersion, kind, name string, uid types.UID, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: &controller}
}
func TestGroupedCustomOwnerIsEnrolledWithoutBecomingGroupOwnership(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	parent := genericIdentityObject(gvr, "Widget", "team", "owner")
	parent.SetUID("owner-uid")
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {parent}}, listCalls: make(map[schema.GroupVersionResource]int)}
	provider := &mockProvider{}
	for i := range 7 {
		provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%d", i), Namespace: "team", UID: types.UID(fmt.Sprintf("pod-%d", i)), Labels: map[string]string{"app": "workers"}, OwnerReferences: []metav1.OwnerReference{groupedParentOwnerRef("parents.example.io/v1", "Widget", "owner", "owner-uid", true)}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	opts := DefaultBuildOptions()
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range topo.Edges {
		if edge.OwnerAggregation != nil {
			found = true
			if edge.OwnerController != nil {
				t.Fatalf("synthetic group became ownership: %+v", edge)
			}
		}
	}
	if !found {
		t.Fatalf("grouped custom ownership disappeared: %+v", topo)
	}
	rel := GetRelationships("Widget", "team", "owner", topo, provider, dynamic)
	if rel != nil && (len(rel.Children) != 0) {
		t.Fatalf("synthetic group became a resource reference: %+v", rel)
	}
	preview := GetCascadeDeletePreview(ResourceRef{Group: gvr.Group, Kind: "Widget", Namespace: "team", Name: "owner"}, topo, dynamic)
	if len(preview.Dependents) != 0 {
		t.Fatalf("synthetic group became garbage collection: %+v", preview)
	}
}

func TestActualPodGroupCRDKeepsItsMetadataAncestors(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	groups := schema.GroupVersionResource{Group: "scheduling.example.io", Version: "v1", Resource: "podgroups"}
	parent := genericIdentityObject(widgets, "Widget", "team", "root")
	parent.SetUID("root-uid")
	group := genericIdentityObject(groups, "PodGroup", "team", "batch")
	group.SetUID("group-uid")
	group.SetOwnerReferences([]metav1.OwnerReference{groupedParentOwnerRef("parents.example.io/v1", "Widget", "root", "root-uid", true)})
	provider := &mockProvider{pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "team", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{groupedParentOwnerRef("scheduling.example.io/v1", "PodGroup", "batch", "group-uid", true)}}}}}
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{widgets, groups}, kinds: map[schema.GroupVersionResource]string{widgets: "Widget", groups: "PodGroup"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{widgets: {parent}, groups: {group}}, listCalls: make(map[schema.GroupVersionResource]int)}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Nodes) != 3 {
		t.Fatalf("actual PodGroup lost metadata ancestors: %+v", topo.Nodes)
	}
}

func TestGroupedMembersKeepOnlyCurrentIncarnationAndExplicitAggregation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	parent := genericIdentityObject(gvr, "Widget", "team", "owner")
	parent.SetUID("current")
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {parent}}, listCalls: make(map[schema.GroupVersionResource]int)}
	provider := &mockProvider{}
	for i := 0; i < 7; i++ {
		uid := types.UID("stale")
		if i == 6 {
			uid = "current"
		}
		provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%d", i), Namespace: "team", UID: types.UID(fmt.Sprintf("pod-%d", i)), Labels: map[string]string{"app": "workers"}, OwnerReferences: []metav1.OwnerReference{groupedParentOwnerRef("parents.example.io/v1", "Widget", "owner", uid, false)}}})
	}
	for _, allStale := range []bool{false, true} {
		if allStale {
			provider.pods[6].OwnerReferences[0].UID = "stale"
		}
		topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, edge := range topo.Edges {
			if edge.OwnerAggregation != nil {
				found = true
				if !*edge.OwnerAggregation || edge.OwnerAggregationController == nil || *edge.OwnerAggregationController || edge.OwnerController != nil {
					t.Fatalf("misclassified aggregate: %+v", edge)
				}
			}
		}
		if found == allStale {
			t.Fatalf("matching incarnation found=%v allStale=%v", found, allStale)
		}
	}
}
