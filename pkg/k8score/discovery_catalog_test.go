package k8score

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/fake"
)

// scriptedDiscovery answers discovery with whatever the test sets.
type scriptedDiscovery struct {
	discovery.DiscoveryInterface
	lists []*metav1.APIResourceList
	err   error
	calls atomic.Int32
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
	if c.Generation != 1 || !c.Complete {
		t.Fatalf("catalog = generation %d, complete %v; want generation 1, complete", c.Generation, c.Complete)
	}
	c.Resources[0].Kind = "Changed"
	if d.Catalog().Resources[0].Kind != "Widget" {
		t.Fatal("a caller's change to its catalog reached discovery")
	}
	if d.Generation(); client.calls.Load() != 1 {
		t.Fatalf("discovery was called %d times; Catalog and Generation must not refresh", client.calls.Load())
	}
}

func TestCatalogGenerationCountsOnlyChangesToResources(t *testing.T) {
	client := &scriptedDiscovery{err: errors.New("discovery unavailable")}
	d, err := NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	if c := d.Catalog(); c.Generation != 0 || len(c.Resources) != 0 || c.Complete {
		t.Fatalf("discovery that never answered: generation %d, %d resources, complete %v; want 0, 0, incomplete", c.Generation, len(c.Resources), c.Complete)
	}
	// Resources added without a discovery answer change the catalog but say
	// nothing about what else is served.
	d.AddAPIResource(APIResource{Group: "example.io", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true})
	if c := d.Catalog(); c.Generation != 1 || c.Complete {
		t.Fatalf("after adding a resource to unanswered discovery: generation %d, complete %v; want 1, incomplete", c.Generation, c.Complete)
	}

	client.lists, client.err = widgetLists(), nil
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if c := d.Catalog(); c.Generation != 2 || !c.Complete {
		t.Fatalf("after a refresh that returned resources: generation %d, complete %v; want 2, complete", c.Generation, c.Complete)
	}

	client.lists, client.err = nil, errors.New("discovery unavailable")
	if err := d.Refresh(); err == nil {
		t.Fatal("a refresh returning nothing must fail")
	}
	if c := d.Catalog(); c.Generation != 2 || len(c.Resources) != 2 || !c.Complete {
		t.Fatalf("a failed refresh changed the catalog: generation %d, %d resources, complete %v", c.Generation, len(c.Resources), c.Complete)
	}

	d.AddAPIResource(APIResource{Group: "example.io", Version: "v1", Kind: "Gadget", Name: "gadgets", Namespaced: true})
	if got := d.Generation(); got != 3 {
		t.Fatalf("after adding a resource: generation %d, want 3", got)
	}

	client.lists = widgetLists()
	client.err = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("service unavailable"),
	}}
	_ = d.Refresh()
	if c := d.Catalog(); c.Generation != 4 || c.Complete {
		t.Fatalf("partial refresh: generation %d, complete %v", c.Generation, c.Complete)
	}

	// Any error alongside resources leaves the answer incomplete, whatever its
	// type.
	client.err = errors.New("discovery interrupted")
	_ = d.Refresh()
	if c := d.Catalog(); c.Generation != 5 || c.Complete {
		t.Fatalf("refresh with an error and resources: generation %d, complete %v; want 5, incomplete", c.Generation, c.Complete)
	}
	client.err = nil
	_ = d.Refresh()
	if !d.Catalog().Complete {
		t.Fatal("a clean refresh must make the catalog complete again")
	}
}

func TestStoppedReportsAStoppedCache(t *testing.T) {
	var none *ResourceCache
	var noDynamic *DynamicResourceCache
	if none.Stopped() || noDynamic.Stopped() {
		t.Fatal("a nil cache was never started, so it never stopped")
	}
	rc, err := NewResourceCache(CacheConfig{Client: fake.NewClientset(), ResourceTypes: map[string]bool{Pods: true}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: newWidgetDynamicClient()})
	if err != nil {
		t.Fatal(err)
	}
	if rc.Stopped() || d.Stopped() {
		t.Fatal("a running cache reported stopped")
	}
	rc.Stop()
	d.Stop()
	if !rc.Stopped() || !d.Stopped() {
		t.Fatalf("after Stop: typed %v, dynamic %v; want both stopped", rc.Stopped(), d.Stopped())
	}
}
