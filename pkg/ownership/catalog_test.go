package ownership

import (
	"reflect"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestCatalogMergesVersionsAndKeepsTheGarbageCollectorsKinds(t *testing.T) {
	gc := []string{"create", "delete", "get", "list", "watch"}
	widget := resourceid.GroupKind{Group: "example.io", Kind: "Widget"}
	c := NewCatalog([]ServedResource{
		{Group: "example.io", Version: "v1beta1", Kind: "Widget", Resource: "widgets", Namespaced: true, Verbs: []string{"get", "list"}},
		{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "widgets", Namespaced: true, Verbs: gc},
		{Group: "example.io", Version: "v1", Kind: "Gadget", Resource: "gadgets", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics", Resource: "pods", Namespaced: true, Verbs: []string{"get", "list"}},
		{Group: "", Version: "v1", Kind: "Event", Resource: "events", Namespaced: true, Verbs: gc},
		{Group: "events.k8s.io", Version: "v1", Kind: "Event", Resource: "events", Namespaced: true, Verbs: gc},
		{Group: "", Version: "v1", Kind: "Node", Resource: "nodes", Verbs: gc},
	}, true)

	got, ok := c.Kind(widget)
	want := Kind{GroupKind: widget, Resource: "widgets", Namespaced: true, Versions: []string{"v1", "v1beta1"}, GarbageCollected: true}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Widget = %+v, %v; want %+v", got, ok, want)
	}
	if !c.Serves(widget, "v1beta1") || c.Serves(widget, "v2") || c.Serves(resourceid.GroupKind{Kind: "Widget"}, "v1") {
		t.Fatal("Serves must name exactly the group, kind and versions discovery listed")
	}
	var dependents []string
	for _, k := range c.DependentKinds() {
		dependents = append(dependents, k.GroupKind.String())
	}
	// Gadget lacks delete and PodMetrics lacks watch, so the garbage collector
	// never acts on them; both Events are left out by design.
	if want := []string{"Node", "Widget.example.io"}; !slices.Equal(dependents, want) {
		t.Fatalf("DependentKinds = %v, want %v", dependents, want)
	}
	if !c.Complete() || NewCatalog(nil, false).Complete() {
		t.Fatal("Complete must be what discovery said")
	}
}

func TestCatalogSettlesAKindServedByTwoResources(t *testing.T) {
	for _, order := range [][]ServedResource{
		{{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "widgets"}, {Group: "example.io", Version: "v1", Kind: "Widget", Resource: "awidgets", Namespaced: true}},
		{{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "awidgets", Namespaced: true}, {Group: "example.io", Version: "v1", Kind: "Widget", Resource: "widgets"}},
	} {
		k, _ := NewCatalog(order, true).Kind(resourceid.GroupKind{Group: "example.io", Kind: "Widget"})
		if k.Resource != "awidgets" || !k.Namespaced {
			t.Fatalf("Widget settled on %+v; want awidgets whatever discovery's order", k)
		}
	}
}

func TestIsEventNamesBothEventKinds(t *testing.T) {
	for gk, want := range map[resourceid.GroupKind]bool{
		{Kind: "Event"}:                         true,
		{Group: "events.k8s.io", Kind: "Event"}: true,
		{Group: "example.io", Kind: "Event"}:    false,
		{Kind: "Pod"}:                           false,
	} {
		if IsEvent(gk) != want {
			t.Errorf("IsEvent(%v) = %v, want %v", gk, !want, want)
		}
	}
}

func TestZeroReadIsUnavailable(t *testing.T) {
	var r ObjectRead
	if r.State != Unavailable || r.State.String() != "unavailable" {
		t.Fatalf("zero read = %v; an unset read must never claim an answer", r.State)
	}
}
