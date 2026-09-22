package k8score

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

type namespaceReadInformer struct {
	cache.SharedIndexInformer
	indexer cache.Indexer
	synced  bool
}

func (i namespaceReadInformer) GetIndexer() cache.Indexer { return i.indexer }
func (i namespaceReadInformer) HasSynced() bool           { return i.synced }

type namespaceOnlyIndexer struct {
	cache.Indexer
	namespaces []string
}

func (i *namespaceOnlyIndexer) List() []any {
	panic("bounded namespace read must not materialize global cache")
}
func (i *namespaceOnlyIndexer) ByIndex(index, value string) ([]any, error) {
	if index != cache.NamespaceIndex {
		panic("expected namespace index")
	}
	i.namespaces = append(i.namespaces, value)
	return i.Indexer.ByIndex(index, value)
}

func TestListWatchedNamespaceReadOnlyScopesBeforeLimiting(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	idx := &namespaceOnlyIndexer{Indexer: cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})}
	seed := func(ns, name string) {
		t.Helper()
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"namespace": ns, "name": name}}}
		if err := idx.Add(u); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 2000; n++ {
		seed("foreign", fmt.Sprintf("x%d", n))
	}
	seed("target", "one")
	d := &DynamicResourceCache{informers: map[informerKey]*informerEntry{{gvr: gvr}: {informer: namespaceReadInformer{indexer: idx, synced: true}}}}
	got, truncated, err := d.ListWatchedNamespaceReadOnly(gvr, "target", 500)
	if err != nil || truncated || len(got) != 1 || got[0].GetNamespace() != "target" {
		t.Fatalf("foreign cache affected result: %v %v %v", got, truncated, err)
	}
	for n := 0; n < 600; n++ {
		seed("target", fmt.Sprintf("x%d", n))
	}
	got, truncated, err = d.ListWatchedNamespaceReadOnly(gvr, "target", 500)
	if err != nil || !truncated || len(got) != 0 {
		t.Fatalf("candidate limit lost: %d %v %v", len(got), truncated, err)
	}
	again, againTruncated, againErr := d.ListWatchedNamespaceReadOnly(gvr, "target", 500)
	if againErr != nil || !againTruncated || len(again) != 0 {
		t.Fatal("large namespace produced unstable sample")
	}
	for _, ns := range idx.namespaces {
		if ns != "target" {
			t.Fatalf("read unrelated namespace %q", ns)
		}
	}
	if _, _, err = d.ListWatchedNamespaceReadOnly(gvr, "", 500); err == nil {
		t.Fatal("unscoped read accepted")
	}
	if _, _, err = d.ListWatchedNamespaceReadOnly(gvr, "target", 0); err == nil {
		t.Fatal("unbounded read accepted")
	}
	if len(d.informers) != 1 {
		t.Fatal("read started informer")
	}
}

func TestListWatchedNamespaceReadOnlyDoesNotStartOrUseUnsyncedWatch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	d := &DynamicResourceCache{informers: map[informerKey]*informerEntry{}}
	if _, _, err := d.ListWatchedNamespaceReadOnly(gvr, "target", 5); err == nil {
		t.Fatal("missing watch accepted")
	}
	if len(d.informers) != 0 {
		t.Fatal("watch created")
	}
	d.informers[informerKey{gvr: gvr, ns: "target"}] = &informerEntry{informer: namespaceReadInformer{synced: false}}
	if _, _, err := d.ListWatchedNamespaceReadOnly(gvr, "target", 5); err == nil {
		t.Fatal("unsynced watch accepted")
	}
}
