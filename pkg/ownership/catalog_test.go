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
	want := CatalogKind{GroupKind: widget, Resource: "widgets", Namespaced: true, Versions: []string{"v1", "v1beta1"}, HasGCVerbs: true}
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

// A reference names a kind, not a resource, so a kind two resources of one
// group serve can't be read or searched as either.
func TestCatalogMarksAKindServedByTwoResourcesAmbiguous(t *testing.T) {
	widget := resourceid.GroupKind{Group: "example.io", Kind: "Widget"}
	getOnly := ServedResource{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "awidgets", Verbs: []string{"get"}}
	collected := ServedResource{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "widgets", Namespaced: true, Verbs: []string{"delete", "list", "watch"}}
	for _, order := range [][]ServedResource{{getOnly, collected}, {collected, getOnly}} {
		c := NewCatalog(order, true)
		if _, found := c.Kind(widget); found || !c.Ambiguous(widget) || c.Serves(widget, "v1") {
			t.Fatalf("Widget must be ambiguous, never resolved to one resource: found=%v ambiguous=%v", found, c.Ambiguous(widget))
		}
		if got := c.AmbiguousDependentKinds(); !slices.Equal(got, []AmbiguousKind{{GroupKind: widget, Namespaced: true}}) {
			t.Fatalf("AmbiguousDependentKinds = %+v; want Widget, collected through its namespaced resource", got)
		}
		if len(c.DependentKinds()) != 0 {
			t.Fatal("an ambiguous kind must not join the dependent kinds as one of its resources")
		}
	}
	if NewCatalog([]ServedResource{collected}, true).Ambiguous(widget) {
		t.Fatal("one resource serving a kind is not ambiguous")
	}
}

// Aggregated APIs can serve one kind through several query resources that
// nothing can list, watch or delete, as Kueue's visibility API serves
// PendingWorkloadsSummary through clusterqueues and localqueues. The kind is
// ambiguous, but no object of it can be a dependent.
func TestCatalogKeepsAnAmbiguousKindNothingCollectsOutOfDependents(t *testing.T) {
	summary := resourceid.GroupKind{Group: "visibility.kueue.x-k8s.io", Kind: "PendingWorkloadsSummary"}
	c := NewCatalog([]ServedResource{
		{Group: summary.Group, Version: "v1beta2", Kind: summary.Kind, Resource: "clusterqueues"},
		{Group: summary.Group, Version: "v1beta2", Kind: summary.Kind, Resource: "localqueues", Namespaced: true},
	}, true)
	if !c.Ambiguous(summary) {
		t.Fatal("two resources serving the kind must still make it ambiguous")
	}
	if got := c.AmbiguousDependentKinds(); len(got) != 0 {
		t.Fatalf("AmbiguousDependentKinds = %+v; nothing can collect a summary", got)
	}
	scoped := NewCatalog([]ServedResource{
		{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "widgets", Namespaced: true, Verbs: []string{"get"}},
		{Group: "example.io", Version: "v1", Kind: "Widget", Resource: "clusterwidgets", Verbs: []string{"delete", "list", "watch"}},
	}, true)
	if got := scoped.AmbiguousDependentKinds(); !slices.Equal(got, []AmbiguousKind{{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, ClusterScoped: true}}) {
		t.Fatalf("AmbiguousDependentKinds = %+v; want Widget, collected only cluster-wide", got)
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
