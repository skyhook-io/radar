package k8score

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// scriptedDiscovery answers discovery with whatever the test sets.
type scriptedDiscovery struct {
	discovery.DiscoveryInterface
	lists []*metav1.APIResourceList
	err   error
	calls atomic.Int32
}

func TestCatalogGenerationTracksAllMutations(t *testing.T) {
	client := &scriptedDiscovery{lists: widgetLists()}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	previous := d.Catalog()
	if previous.Generation == 0 {
		t.Fatal("initial discovery did not advance its generation")
	}
	check := func() {
		t.Helper()
		current, changed := d.CatalogIfChanged(previous.Generation)
		if !changed || current.Generation <= previous.Generation {
			t.Fatalf("mutation: generation %d, changed %v; previous %d", current.Generation, changed, previous.Generation)
		}
		if current.Generation != d.Catalog().Generation {
			t.Fatal("conditional snapshot has a different generation from Catalog")
		}
		if same, changed := d.CatalogIfChanged(current.Generation); changed || same.Resources != nil || same.FailedGroups != nil {
			t.Fatal("unchanged generation copied a snapshot")
		}
		previous = current
	}
	r := APIResource{Group: "example.io", Version: "v1", Kind: "Gadget", Name: "gadgets", Namespaced: true}
	d.AddAPIResource(r)
	check()
	r.Kind, r.Namespaced = "Changed", false
	d.AddAPIResource(r)
	check()
	for _, failure := range []error{
		&discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{{Group: "failed.io", Version: "v1"}: errors.New("unavailable")}},
		&discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{{Group: "other.io", Version: "v1"}: errors.New("unavailable")}},
		errors.New("unclassified"), nil,
	} {
		client.err = failure
		if err := d.Refresh(); err != nil {
			t.Fatal(err)
		}
		check()
	}
	client.lists, client.err = nil, errors.New("unavailable")
	if err := d.Refresh(); err == nil {
		t.Fatal("expected failed refresh")
	}
	// A failed refresh keeps the last catalog, so its generation doesn't move.
	if _, changed := d.CatalogIfChanged(previous.Generation); changed {
		t.Fatal("a failed refresh advanced the generation of an unchanged catalog")
	}
	if !previous.Complete || len(previous.Resources) != 2 {
		t.Fatal("generation tracking changed the failed-refresh catalog contract")
	}
	client.err = nil
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	check()
	if len(previous.Resources) != 0 || !previous.Complete {
		t.Fatal("empty successful refresh did not remove resources")
	}
}

func TestCatalogGenerationAndResourcesAreOneSnapshot(t *testing.T) {
	d, err := NewResourceDiscovery(&scriptedDiscovery{})
	if err != nil {
		t.Fatal(err)
	}
	initial := d.Catalog().Generation
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 100 {
			d.AddAPIResource(APIResource{Group: "example.io", Version: "v1", Kind: "Thing", Name: fmt.Sprint("things", i)})
		}
	})
	for range 100 {
		c := d.Catalog()
		if c.Generation-initial != uint64(len(c.Resources)) {
			t.Fatalf("snapshot generation %d contains %d resources (initial %d)", c.Generation, len(c.Resources), initial)
		}
		if next, changed := d.CatalogIfChanged(c.Generation); changed && next.Generation-initial != uint64(len(next.Resources)) {
			t.Fatalf("conditional snapshot generation %d contains %d resources", next.Generation, len(next.Resources))
		}
	}
	wg.Wait()
	if c := d.Catalog(); c.Generation-initial != 100 || len(c.Resources) != 100 {
		t.Fatalf("final snapshot = generation %d, resources %d", c.Generation, len(c.Resources))
	}
}

func TestCatalogIfChangedOnNilAndUninitializedDiscovery(t *testing.T) {
	var missing *ResourceDiscovery
	if c, changed := missing.CatalogIfChanged(0); changed || c.Resources != nil {
		t.Fatal("nil discovery returned a changed catalog")
	}
	d := &ResourceDiscovery{}
	if c, changed := d.CatalogIfChanged(0); changed || c.Resources != nil {
		t.Fatal("unchanged zero generation returned a snapshot")
	}
}

func (d *scriptedDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	d.calls.Add(1)
	return nil, d.lists, d.err
}

func widgetLists() []*metav1.APIResourceList {
	verbs := []string{"delete", "get", "list", "watch"}
	return []*metav1.APIResourceList{
		{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: verbs}}},
		{GroupVersion: "example.io/v1beta1", APIResources: []metav1.APIResource{
			{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: verbs},
			{Name: "widgets/status", Kind: "Widget", Namespaced: true, Verbs: []string{"get"}},
		}},
	}
}

func TestCatalogKeepsEveryServedVersionWithoutRefreshing(t *testing.T) {
	client := &scriptedDiscovery{lists: widgetLists()}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	c := d.Catalog()
	var versions []string
	for _, r := range c.Resources {
		versions = append(versions, r.Group+"/"+r.Version+"/"+r.Name)
	}
	if want := []string{"example.io/v1/widgets", "example.io/v1beta1/widgets"}; !slices.Equal(versions, want) {
		t.Fatalf("catalog resources = %v, want %v (every served version, no subresources)", versions, want)
	}
	if !c.Complete {
		t.Fatal("catalog of a clean discovery must be complete")
	}
	c.Resources[0].Kind = "Changed"
	if d.Catalog().Resources[0].Kind != "Widget" {
		t.Fatal("a caller's change to its catalog reached discovery")
	}
	if client.calls.Load() != 1 {
		t.Fatalf("discovery was called %d times; Catalog must not refresh", client.calls.Load())
	}
}

func TestCatalogIsCompleteOnlyAfterACleanDiscovery(t *testing.T) {
	client := &scriptedDiscovery{err: errors.New("discovery unavailable")}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	if c := d.Catalog(); len(c.Resources) != 0 || c.Complete {
		t.Fatalf("discovery that never answered: %d resources, complete %v; want 0, incomplete", len(c.Resources), c.Complete)
	}
	// Resources added without a discovery answer change the catalog but say
	// nothing about what else is served.
	d.AddAPIResource(APIResource{Group: "example.io", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true})
	if c := d.Catalog(); len(c.Resources) != 1 || c.Complete {
		t.Fatalf("after adding a resource to unanswered discovery: %d resources, complete %v; want 1, incomplete", len(c.Resources), c.Complete)
	}

	client.lists, client.err = widgetLists(), nil
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !d.Catalog().Complete {
		t.Fatal("after a refresh that returned resources: incomplete, want complete")
	}

	client.lists, client.err = nil, errors.New("discovery unavailable")
	if err := d.Refresh(); err == nil {
		t.Fatal("a refresh returning nothing must fail")
	}
	if c := d.Catalog(); len(c.Resources) != 2 || !c.Complete {
		t.Fatalf("a failed refresh changed the catalog: %d resources, complete %v", len(c.Resources), c.Complete)
	}

	client.lists = widgetLists()
	client.err = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("service unavailable"),
	}}
	_ = d.Refresh()
	if d.Catalog().Complete {
		t.Fatal("partial refresh: complete, want incomplete")
	}

	// Any error alongside resources leaves the answer incomplete, whatever its
	// type.
	client.err = errors.New("discovery interrupted")
	_ = d.Refresh()
	if d.Catalog().Complete {
		t.Fatal("refresh with an error and resources: complete, want incomplete")
	}
	client.err = nil
	_ = d.Refresh()
	if !d.Catalog().Complete {
		t.Fatal("a clean refresh must make the catalog complete again")
	}
}

func TestCurrentKindResolutionDoesNotInferCertaintyAfterDiscoveryFailure(t *testing.T) {
	client := &scriptedDiscovery{lists: widgetLists()}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	ref := resourceid.UnqualifiedReference("Widget", "default", "example")
	if got := resourceid.ResolveCurrent(ref, d); !got.OK() || got.Uncertain {
		t.Fatalf("clean discovery: %+v", got)
	}
	client.lists, client.err = nil, errors.New("discovery unavailable")
	if err := d.Refresh(); err == nil {
		t.Fatal("expected discovery failure")
	}
	if got := resourceid.ResolveCurrent(ref, d); !got.OK() || !got.Uncertain {
		t.Fatalf("stale discovery must not establish current uniqueness: %+v", got)
	}
	client.lists = widgetLists()
	client.err = errors.New("discovery interrupted")
	_ = d.Refresh()
	if got := resourceid.ResolveCurrent(ref, d); !got.OK() || !got.Uncertain {
		t.Fatalf("resources with an error must not establish uniqueness: %+v", got)
	}
	client.err = nil
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := resourceid.ResolveCurrent(ref, d); !got.OK() || got.Uncertain {
		t.Fatalf("recovered discovery: %+v", got)
	}
}

func TestCatalogSnapshotsTheFailedGroupsOfItsResources(t *testing.T) {
	failure := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("unavailable"),
		{Group: "metrics.k8s.io", Version: "v1"}:      errors.New("unavailable"),
		{Group: "", Version: "v1"}:                    errors.New("unavailable"),
	}}
	client := &scriptedDiscovery{lists: widgetLists(), err: failure}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	before := d.Catalog()
	if before.Complete || !slices.Equal(before.FailedGroups, []string{"", "metrics.k8s.io"}) {
		t.Fatalf("partial catalog = %+v", before)
	}
	before.FailedGroups[0] = "changed"
	if !slices.Equal(d.Catalog().FailedGroups, []string{"", "metrics.k8s.io"}) {
		t.Fatal("a caller's failed-group slice changed discovery")
	}
	client.lists = nil
	if err := d.Refresh(); err == nil {
		t.Fatal("a refresh returning no resources must fail")
	}
	if got := d.Catalog(); got.Complete || !slices.Equal(got.FailedGroups, []string{"", "metrics.k8s.io"}) || len(got.Resources) != 2 {
		t.Fatalf("a failed refresh changed the catalog: %+v", got)
	}
	client.lists, client.err = widgetLists(), errors.New("discovery interrupted")
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := d.Catalog(); got.Complete || len(got.FailedGroups) != 0 {
		t.Fatalf("unclassified failure must cover no group: %+v", got)
	}
	client.err = nil
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if got := d.Catalog(); !got.Complete || len(got.FailedGroups) != 0 {
		t.Fatalf("recovered discovery = %+v", got)
	}
}
