package topology

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestGenericCRDOwnershipUsesObservedIncarnation(t *testing.T) {
	for _, ownerUID := range []string{"parent-current", "parent-deleted"} {
		t.Run(ownerUID, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
			child := genericIdentityObject(gvr, "Widget", "team", "child", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "app", UID: types.UID(ownerUID)})
			child.SetUID("child-current")
			before := child.DeepCopy()
			dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {child}}, listCalls: map[schema.GroupVersionResource]int{}}
			parent := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team", UID: "parent-current"}}
			topo, err := NewBuilder(&mockProvider{deployments: []*appsv1.Deployment{parent}}).WithDynamic(dynamic).Build(DefaultBuildOptions())
			if err != nil {
				t.Fatal(err)
			}
			wantChild := ownerUID == "parent-current"
			if got := nodeByID(topo.Nodes, "widget/team/child/relationships.example.io"); (got != nil) != wantChild {
				t.Fatalf("owner UID %s joined replacement: nodes=%+v edges=%+v", ownerUID, topo.Nodes, topo.Edges)
			}
			if dynamic.getCalls != 0 || !reflect.DeepEqual(child, before) {
				t.Fatalf("ownership fetched or mutated input: gets=%d child=%+v", dynamic.getCalls, child)
			}
			wire, err := json.Marshal(topo)
			if err != nil || strings.Contains(string(wire), "parent-current") || strings.Contains(string(wire), "child-current") {
				t.Fatalf("incarnation leaked into graph JSON: %s, %v", wire, err)
			}
		})
	}
}

func TestObservedOwnerResolutionKeepsGroupNamespaceAndClusterScope(t *testing.T) {
	idx := IndexByResource(&Topology{Nodes: []Node{
		{uid: "apps", ID: "deployment/team/app", Kind: KindDeployment, Name: "app", Data: map[string]any{"namespace": "team"}},
		{uid: "other", ID: "deployment/other/app", Kind: KindDeployment, Name: "app", Data: map[string]any{"namespace": "other"}},
		{uid: "foreign", ID: "deployment/team/app/example.io", Kind: "Deployment", Name: "app", Data: map[string]any{"namespace": "team", "apiVersion": "example.io/v1"}},
		{uid: "cluster", ID: "node//host", Kind: KindNode, Name: "host", Data: map[string]any{"namespace": ""}},
	}})
	cases := []struct {
		version, kind, namespace, name, uid, wantID string
		matches                                     bool
	}{
		{"apps/v1", "Deployment", "team", "app", "apps", "deployment/team/app", true},
		{"apps/v1", "Deployment", "team", "app", "deleted", "deployment/team/app", false},
		{"apps/v1", "Deployment", "other", "app", "other", "deployment/other/app", true},
		{"example.io/v1", "Deployment", "team", "app", "foreign", "deployment/team/app/example.io", true},
		{"v1", "Node", "team", "host", "cluster", "node//host", true},
		{"", "Deployment", "team", "app", "apps", "", false},
		{"apps/v1", "Deployment", "missing", "app", "apps", "", false},
	}
	for _, tc := range cases {
		node, matches := idx.ResolveObservedOwner(resourceid.OwnerReference(tc.version, tc.kind, tc.name, tc.uid, tc.namespace))
		if matches != tc.matches || (node == nil) != (tc.wantID == "") || node != nil && node.ID != tc.wantID {
			t.Fatalf("%+v => %+v, %v", tc, node, matches)
		}
	}
}

func TestSeededGenericCRDRejectsExistingReplacementEdge(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
	child := genericIdentityObject(gvr, "Widget", "team", "child", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "app", UID: "deleted"})
	child.SetUID("current-child")
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {child}}, listCalls: map[schema.GroupVersionResource]int{}}
	nodes := []Node{
		{uid: "current", ID: "deployment/team/app", Kind: KindDeployment, Name: "app", Data: map[string]any{"namespace": "team"}},
		{ID: "widget/team/child", Kind: "Widget", Name: "child", Data: map[string]any{"namespace": "team", "apiVersion": "relationships.example.io/v1"}},
	}
	edges := []Edge{{ID: "owner", Source: nodes[0].ID, Target: nodes[1].ID, Type: EdgeManages}}
	nodes, edges = (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, edges, DefaultBuildOptions())
	if len(nodes) != 2 || len(edges) != 0 || nodes[1].uid != "current-child" {
		t.Fatalf("seeded replacement ownership survived: %+v %+v", nodes, edges)
	}
}

func TestGenericOwnerClosureJoinsParentsEnrolledAfterChild(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
	controller := true
	root := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "root", UID: "root-uid", Controller: &controller}
	late := genericIdentityObject(gvr, "Widget", "team", "late", root)
	late.SetUID("late-uid")
	child := genericIdentityObject(gvr, "Widget", "team", "child", root, metav1.OwnerReference{APIVersion: "relationships.example.io/v1", Kind: "Widget", Name: "late", UID: "late-uid"})
	child.SetUID("child-uid")
	for _, order := range [][]*unstructured.Unstructured{{child, late}, {late, child}} {
		dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: order}, listCalls: map[schema.GroupVersionResource]int{}}
		nodes := []Node{{uid: "root-uid", ID: "deployment/team/root", Kind: KindDeployment, Name: "root", Data: map[string]any{"namespace": "team"}}}
		nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())
		if len(nodes) != 3 || len(edges) != 3 {
			t.Fatalf("late owner lost: nodes=%+v edges=%+v", nodes, edges)
		}
		found := false
		for _, edge := range edges {
			if edge.Source == "widget/team/late/relationships.example.io" && edge.Target == "widget/team/child/relationships.example.io" {
				found = true
			}
		}
		if !found {
			t.Fatalf("late parent edge missing: %+v", edges)
		}
		rel := GetRelationshipsWithObject("Widget", "team", "child", child, &Topology{Nodes: nodes, Edges: edges}, nil, dynamic, nil)
		if rel == nil || rel.Owner == nil || rel.Owner.Kind != "Deployment" || rel.Owner.Name != "root" {
			t.Fatalf("controller displaced by non-controller parent: %+v", rel)
		}

		nodes, edges = (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, edges, DefaultBuildOptions())
		if len(nodes) != 3 || len(edges) != 3 {
			t.Fatalf("repeat pass duplicates: %+v %+v", nodes, edges)
		}
	}
}

func TestGenericCRDOwnershipUsesObservedConfigIncarnations(t *testing.T) {
	for _, parent := range []Node{
		configMapNode(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "team", UID: "current-config"}}),
		secretNode(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "team", UID: "current-config"}}),
	} {
		t.Run(string(parent.Kind), func(t *testing.T) {
			if parent.uid != "current-config" {
				t.Fatalf("observed config UID = %q", parent.uid)
			}
			gvr := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
			for _, uid := range []types.UID{"current-config", "deleted-config"} {
				child := genericIdentityObject(gvr, "Widget", "team", "child", metav1.OwnerReference{APIVersion: "v1", Kind: string(parent.Kind), Name: "config", UID: uid})
				dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {child}}, listCalls: map[schema.GroupVersionResource]int{}}
				nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes([]Node{parent}, nil, DefaultBuildOptions())
				want := uid == "current-config"
				if (len(nodes) == 2) != want || (len(edges) == 1) != want {
					t.Fatalf("owner UID %s: nodes=%+v edges=%+v", uid, nodes, edges)
				}
			}
		})
	}
}

func TestGenericOwnerClosureRejectsSelfOwner(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
	child := genericIdentityObject(gvr, "Widget", "team", "child", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "root", UID: "root-uid"}, metav1.OwnerReference{APIVersion: "relationships.example.io/v1", Kind: "Widget", Name: "child", UID: "child-uid"})
	child.SetUID("child-uid")
	p := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {child}}, listCalls: map[schema.GroupVersionResource]int{}}
	nodes, edges := (&Builder{dynamic: p}).addGenericCRDNodes([]Node{{uid: "root-uid", ID: "deployment/team/root", Kind: KindDeployment, Name: "root", Data: map[string]any{"namespace": "team"}}}, nil, DefaultBuildOptions())
	if len(nodes) != 2 || len(edges) != 1 || edges[0].Source == edges[0].Target {
		t.Fatalf("self-reference became graph ownership: %+v %+v", nodes, edges)
	}
}
