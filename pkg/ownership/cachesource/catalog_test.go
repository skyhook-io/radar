package cachesource

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type catalogDiscoveryClient struct {
	discovery.DiscoveryInterface
	lists []*metav1.APIResourceList
	err   error
}

func (d *catalogDiscoveryClient) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, d.lists, d.err
}

func TestCatalogMemoTracksDiscoveryMutations(t *testing.T) {
	client := &catalogDiscoveryClient{lists: discoveryLists()}
	d, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	caches := Caches{Typed: &k8score.ResourceCache{}, Discovery: d}
	source := New(func() Caches { return caches })
	first := begin(t, source).Catalog()
	check := func(previous *ownership.Catalog) *ownership.Catalog {
		t.Helper()
		next := begin(t, source).Catalog()
		if next == previous {
			t.Fatal("a mutation reused the previous catalog")
		}
		if begin(t, source).Catalog() != next {
			t.Fatal("the same generation rebuilt its catalog")
		}
		return next
	}
	if begin(t, source).Catalog() != first {
		t.Fatal("unchanged discovery did not reuse the catalog")
	}
	gk := resourceid.GroupKind{Group: "example.io", Kind: "Sprocket"}
	r := k8score.APIResource{Group: gk.Group, Kind: gk.Kind, Name: "sprockets", Version: "v1", Namespaced: true, Verbs: []string{"delete", "list", "watch"}}
	d.AddAPIResource(r)
	added := check(first)
	if k, ok := added.Kind(gk); !ok || !k.Namespaced || !k.HasGCVerbs {
		t.Fatalf("added kind = %+v, %v", k, ok)
	}
	r.Namespaced, r.Verbs = false, []string{"get"}
	d.AddAPIResource(r)
	replaced := check(added)
	if k, ok := replaced.Kind(gk); !ok || k.Namespaced || k.HasGCVerbs {
		t.Fatalf("replaced kind = %+v, %v", k, ok)
	}
	if k, _ := added.Kind(gk); !k.Namespaced || !k.HasGCVerbs {
		t.Fatal("replacement mutated an old session's catalog")
	}
	if _, ok := first.Kind(gk); ok {
		t.Fatal("addition mutated an old session's catalog")
	}
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	removed := check(replaced)
	if _, ok := removed.Kind(gk); ok {
		t.Fatal("refresh retained a removed kind")
	}
	client.err = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "failed.io", Version: "v1"}: errors.New("unavailable"),
	}}
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	partial := check(removed)
	if partial.Complete() || partial.CoversGroup("failed.io") || !partial.CoversGroup("example.io") {
		t.Fatal("partial refresh lost its group coverage")
	}
	client.err = &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "example.io", Version: "v1"}: errors.New("unavailable"),
	}}
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	changedGroups := check(partial)
	if changedGroups.CoversGroup("example.io") || !changedGroups.CoversGroup("failed.io") || !partial.CoversGroup("example.io") {
		t.Fatal("failed-group mutation did not replace coverage independently of resources")
	}
	client.err = errors.New("unclassified failure")
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	unknown := check(changedGroups)
	if unknown.CoversGroup("example.io") || unknown.CoversGroup("failed.io") {
		t.Fatal("unclassified failure retained group coverage")
	}
	client.err = nil
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	complete := check(unknown)
	if !complete.Complete() || !first.Complete() {
		t.Fatal("clean refresh must restore completeness without changing old sessions")
	}
	client.lists, client.err = nil, errors.New("discovery unavailable")
	if err := d.Refresh(); err == nil {
		t.Fatal("expected failed refresh")
	}
	failed := check(complete)
	if !failed.Complete() || !failed.Serves(resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, "v1") {
		t.Fatal("failed refresh lost the last usable discovery catalog")
	}
}

func TestCatalogMemoDoesNotCrossDiscoveryInstances(t *testing.T) {
	client := &catalogDiscoveryClient{lists: discoveryLists()}
	first, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	client.lists = nil
	second, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	if first.Catalog().Generation != second.Catalog().Generation {
		t.Fatal("the fixture must switch between discovery instances with the same generation")
	}
	caches := Caches{Typed: &k8score.ResourceCache{}, Discovery: first}
	source := New(func() Caches { return caches })
	before := begin(t, source).Catalog()
	caches.Discovery = second
	after := begin(t, source).Catalog()
	if before == after || len(after.DependentKinds()) != 0 || len(before.DependentKinds()) == 0 {
		t.Fatal("switching discovery reused the previous connection's catalog")
	}
	caches.Discovery = nil
	without := begin(t, source).Catalog()
	if without == after || without.Complete() || begin(t, source).Catalog() != without {
		t.Fatal("nil discovery did not get its own reusable incomplete catalog")
	}
	caches.Discovery = first
	if !begin(t, source).Catalog().Serves(resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, "v1") {
		t.Fatal("switching back did not restore the original discovery")
	}
}

func TestConcurrentBeginSharesCatalogDuringDiscoveryMutations(t *testing.T) {
	client := &catalogDiscoveryClient{lists: discoveryLists()}
	d, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		t.Fatal(err)
	}
	caches := Caches{Typed: &k8score.ResourceCache{}, Discovery: d}
	source := New(func() Caches { return caches })
	const readers = 16
	catalogs := make(chan *ownership.Catalog, readers)
	errs := make(chan error, 2*readers+1)
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			session, err := source.Begin()
			if err != nil {
				errs <- err
				return
			}
			catalogs <- session.Catalog()
		})
	}
	wg.Wait()
	close(catalogs)
	first := begin(t, source).Catalog()
	for c := range catalogs {
		if c != first {
			t.Fatal("concurrent sessions at one generation got different catalogs")
		}
	}
	start := make(chan struct{})
	for range readers {
		wg.Go(func() {
			<-start
			for range 100 {
				session, err := source.Begin()
				if err != nil {
					errs <- err
					return
				}
				for _, k := range session.Catalog().DependentKinds() {
					if !session.Catalog().Serves(k.GroupKind, k.Versions[0]) {
						errs <- fmt.Errorf("catalog changed within a session: %v", k.GroupKind)
						return
					}
				}
			}
		})
	}
	wg.Go(func() {
		<-start
		for range 50 {
			d.AddAPIResource(k8score.APIResource{Group: "example.io", Kind: "New", Name: "new", Version: "v1", Verbs: []string{"delete", "list", "watch"}})
			if err := d.Refresh(); err != nil {
				errs <- err
				return
			}
		}
	})
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if _, ok := first.Kind(resourceid.GroupKind{Group: "example.io", Kind: "New"}); ok {
		t.Fatal("concurrent mutations reached the original session's catalog")
	}
}
