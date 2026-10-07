package topology

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestGenericParentsEnrollBothDirectionsFromObservedDependent(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	owner := func(name, uid string) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "parents.example.io/v1", Kind: "Widget", Name: name, UID: types.UID(uid)}
	}
	root := genericIdentityObject(gvr, "Widget", "team", "root")
	root.SetUID("root-uid")
	middle := genericIdentityObject(gvr, "Widget", "team", "middle", owner("root", "root-uid"))
	middle.SetUID("middle-uid")
	sibling := genericIdentityObject(gvr, "Widget", "team", "sibling", owner("root", "root-uid"))
	sibling.SetUID("sibling-uid")
	unrelated := genericIdentityObject(gvr, "Widget", "team", "unrelated")
	unrelated.SetUID("unrelated-uid")
	seed := Node{ID: "pod/team/live", Kind: KindPod, Name: "live", uid: "pod-uid", observed: true, ownerReferences: []metav1.OwnerReference{owner("middle", "middle-uid")}, Data: map[string]any{"namespace": "team"}}
	for _, order := range [][]*unstructured.Unstructured{{sibling, middle, root, unrelated}, {root, middle, sibling, unrelated}} {
		before := make([]*unstructured.Unstructured, len(order))
		for i, u := range order {
			before[i] = u.DeepCopy()
		}
		dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: order}, listCalls: map[schema.GroupVersionResource]int{}}
		nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes([]Node{seed}, nil, DefaultBuildOptions())
		if len(nodes) != 4 || len(edges) != 3 || nodeByID(nodes, "widget/team/unrelated/parents.example.io") != nil {
			t.Fatalf("closure enrolled unrelated data or lost connected ownership: %+v %+v", nodes, edges)
		}
		for _, pair := range [][2]string{{"widget/team/root/parents.example.io", "widget/team/middle/parents.example.io"}, {"widget/team/middle/parents.example.io", "pod/team/live"}, {"widget/team/root/parents.example.io", "widget/team/sibling/parents.example.io"}} {
			found := false
			for _, edge := range edges {
				found = found || edge.Source == pair[0] && edge.Target == pair[1] && edge.Type == EdgeManages
			}
			if !found {
				t.Fatalf("missing ownership %v: %+v", pair, edges)
			}
		}
		if dynamic.listCalls[gvr] != 1 || dynamic.getCalls != 0 || !reflect.DeepEqual(order, before) {
			t.Fatalf("closure fetched per edge or mutated inputs: lists=%v gets=%d", dynamic.listCalls, dynamic.getCalls)
		}
		wire, err := json.Marshal(&Topology{Nodes: nodes, Edges: edges})
		if err != nil || strings.Contains(string(wire), "root-uid") || strings.Contains(string(wire), "ownerReferences") {
			t.Fatalf("private owner identity leaked: %s %v", wire, err)
		}
		nodes, edges = (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, edges, DefaultBuildOptions())
		if len(nodes) != 4 || len(edges) != 3 {
			t.Fatalf("repeated closure duplicated graph: %+v %+v", nodes, edges)
		}
	}
}

func TestGenericParentEnrollmentRequiresCurrentScopedIdentity(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name, version, namespace, ownerUID string
		observed, want                     bool
	}{
		{"current namespaced", "parents.example.io/v1", "team", "current", true, true},
		{"current cluster scoped", "parents.example.io/v1", "", "current", true, true},
		{"other namespace", "parents.example.io/v1", "other", "current", true, false},
		{"other group", "other.example.io/v1", "team", "current", true, false},
		{"replaced owner", "parents.example.io/v1", "team", "old", true, false},
		{"missing reference UID", "parents.example.io/v1", "team", "", true, false},
		{"declaration-only dependent", "parents.example.io/v1", "team", "current", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := genericIdentityObject(gvr, "Widget", tc.namespace, "parent")
			parent.SetUID("current")
			dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {parent}}, listCalls: map[schema.GroupVersionResource]int{}}
			seed := Node{ID: "job/team/live", Kind: KindJob, Name: "live", observed: tc.observed, ownerReferences: []metav1.OwnerReference{{APIVersion: tc.version, Kind: "Widget", Name: "parent", UID: types.UID(tc.ownerUID)}}, Data: map[string]any{"namespace": "team"}}
			nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes([]Node{seed}, nil, DefaultBuildOptions())
			if (len(nodes) == 2) != tc.want || (len(edges) == 1) != tc.want {
				t.Fatalf("invalid owner enrolled or valid owner lost: %+v %+v", nodes, edges)
			}
		})
	}
}

func TestGenericParentEnrollmentDoesNotSeedDisconnectedCycles(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	a := genericIdentityObject(gvr, "Widget", "team", "a", metav1.OwnerReference{APIVersion: "parents.example.io/v1", Kind: "Widget", Name: "b", UID: "b"})
	a.SetUID("a")
	b := genericIdentityObject(gvr, "Widget", "team", "b", metav1.OwnerReference{APIVersion: "parents.example.io/v1", Kind: "Widget", Name: "a", UID: "a"})
	b.SetUID("b")
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {a, b}}, listCalls: map[schema.GroupVersionResource]int{}}
	nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes(nil, nil, DefaultBuildOptions())
	if len(nodes) != 0 || len(edges) != 0 {
		t.Fatalf("disconnected cycle became visible: %+v %+v", nodes, edges)
	}
}

func TestGenericParentEnrollmentRetainsKindCap(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	var resources []*unstructured.Unstructured
	var refs []metav1.OwnerReference
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("parent-%02d", i)
		u := genericIdentityObject(gvr, "Widget", "team", name)
		u.SetUID(types.UID(name))
		resources = append(resources, u)
		refs = append(refs, metav1.OwnerReference{APIVersion: "parents.example.io/v1", Kind: "Widget", Name: name, UID: types.UID(name)})
	}
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: resources}, listCalls: map[schema.GroupVersionResource]int{}}
	seed := Node{ID: "pod/team/live", Kind: KindPod, Name: "live", observed: true, ownerReferences: refs, Data: map[string]any{"namespace": "team"}}
	nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes([]Node{seed}, nil, DefaultBuildOptions())
	if len(nodes) != 51 || len(edges) != 50 || dynamic.listCalls[gvr] != 1 || dynamic.getCalls != 0 {
		t.Fatalf("cap or cache-read budget changed: %d nodes %d edges %+v", len(nodes), len(edges), dynamic)
	}
}
