package k8score

import (
	"fmt"
	"log"
	"slices"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

// OwnerUIDIndex indexes every cached object but Events by the UIDs its owner
// references name, so the objects naming one owner are found without scanning
// their kind.
const OwnerUIDIndex = "ownerUID"

var ownerUIDIndexers = cache.Indexers{OwnerUIDIndex: ownerUIDs}

// ownerUIDs returns the UIDs obj's owner references name, whatever their other
// fields say: the garbage collector treats an object as a dependent of every
// UID it names, even when the reference's kind or name is wrong.
func ownerUIDs(obj any) ([]string, error) {
	switch o := obj.(type) {
	case indexQuery:
		return o, nil
	case *unstructured.Unstructured:
		refs, _, _ := unstructured.NestedFieldNoCopy(o.Object, "metadata", "ownerReferences")
		list, _ := refs.([]any)
		var uids []string
		for _, r := range list {
			ref, _ := r.(map[string]any)
			if uid, _ := ref["uid"].(string); uid != "" {
				uids = append(uids, uid)
			}
		}
		return uids, nil
	case metav1.Object:
		refs := o.GetOwnerReferences()
		if len(refs) == 0 {
			return nil, nil
		}
		uids := make([]string, 0, len(refs))
		for _, r := range refs {
			if r.UID != "" {
				uids = append(uids, string(r.UID))
			}
		}
		return uids, nil
	}
	return nil, nil
}

// isEventResource reports either API group's Events, which are never indexed.
func isEventResource(gvr schema.GroupVersionResource) bool {
	return gvr.Resource == "events" && (gvr.Group == "" || gvr.Group == "events.k8s.io")
}

// ReadableDependents returns the objects of resource in namespaces whose owner
// references name uid, under ReadableIndexedObjects' rules.
func (rc *ResourceCache) ReadableDependents(resource string, namespaces []string, uid string) ([]apiruntime.Object, bool) {
	return rc.ReadableIndexedObjects(resource, namespaces, OwnerUIDIndex, uid)
}

// ReadableIndexedObjects returns the objects of resource in namespaces that
// index files under any of keys, under ReadableObjects' rules: ok is false
// unless each namespace's store is complete now, and nil namespaces means the
// kind's whole scope. An object filed under several keys is returned once,
// and no keys read nothing. For one of k8score's own indexes each store is
// read once for all the keys; any other index, such as the namespace index,
// is read key by key. ok is also false when the stores lack the index. The
// objects are the cached ones: callers must not modify or keep them.
func (rc *ResourceCache) ReadableIndexedObjects(resource string, namespaces []string, index string, keys ...string) ([]apiruntime.Object, bool) {
	if rc == nil || !rc.isEnabled(resource) || rc.kindStores[resource] == nil {
		return nil, false
	}
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	read := func(indexer cache.Indexer) ([]any, error) { return readIndex(indexer, index, keys) }
	var hits []any
	if namespaces == nil {
		for _, st := range rc.kindStores[resource] {
			if !st.synced() {
				return nil, false
			}
			items, err := read(st.indexer)
			if err != nil {
				return nil, false
			}
			hits = append(hits, items...)
		}
	} else {
		covered := rc.KindNamespaces(resource)
		isCovered := namespaceSet(covered)
		wanted := namespaceSet(namespaces)
		seen := map[cache.Indexer]bool{}
		for _, namespace := range namespaces {
			if covered != nil && !isCovered[namespace] {
				return nil, false
			}
			st, ok := rc.storeFor(resource, namespace)
			if !ok || !st.synced() {
				return nil, false
			}
			if seen[st.indexer] {
				continue
			}
			seen[st.indexer] = true
			items, err := read(st.indexer)
			if err != nil {
				return nil, false
			}
			for _, item := range items {
				if m, err := apimeta.Accessor(item); err == nil && wanted[m.GetNamespace()] {
					hits = append(hits, item)
				}
			}
		}
	}
	out := make([]apiruntime.Object, 0, len(hits))
	for _, item := range hits {
		if obj, ok := item.(apiruntime.Object); ok {
			out = append(out, obj)
		}
	}
	return out, true
}

// readIndex reads the objects indexer files under any of keys, which are
// sorted and distinct, each once; an empty index reads every object. One of k8score's own indexes answers every
// key from one hold of the store's lock; any other is read key by key.
func readIndex(indexer cache.Indexer, index string, keys []string) ([]any, error) {
	switch {
	case index == "":
		return indexer.List(), nil
	case len(keys) == 0:
		return nil, nil
	case indexer.GetIndexers()[index] == nil:
		// Index registration is generated and pinned by tests, so only a code
		// bug reads a store by an index it lacks; the caller reports a read error.
		log.Printf("[k8score] Store lacks index %q", index)
		return nil, fmt.Errorf("index %q is missing", index)
	case len(keys) == 1:
		return indexer.ByIndex(index, keys[0])
	case multiKeyIndexes[index]:
		return indexer.Index(index, indexQuery(keys))
	}
	var items []any
	seen := map[string]bool{}
	for _, key := range keys {
		found, err := indexer.ByIndex(index, key)
		if err != nil {
			return nil, err
		}
		for _, item := range found {
			if k, err := cache.MetaNamespaceKeyFunc(item); err == nil && !seen[k] {
				seen[k] = true
				items = append(items, item)
			}
		}
	}
	return items, nil
}

// multiKeyIndexes are k8score's own indexes, whose index functions answer an
// indexQuery.
var multiKeyIndexes = map[string]bool{OwnerUIDIndex: true, GitOpsMembersIndex: true, KueueQueueIndex: true}

// indexQuery is a read of keys from one of k8score's indexes: their index
// functions answer it with the keys themselves, so Indexer.Index returns the
// objects filed under any of them from one hold of the store's lock.
type indexQuery []string

// ReadableIndexed returns the objects of gvr in namespace that index holds
// under any of keys — for OwnerUIDIndex, those whose owner references name one
// of the UIDs — from the informers serving namespace now, under
// ReadableObject's rules. An object filed under several keys is returned
// once, and no keys read nothing. For one of k8score's own indexes each
// informer's store is read once for all the keys, so an object that moves
// from one key to another during the read is found under one of them; any
// other index, such as the namespace index, is read key by key. namespace "" reads only a
// cluster-wide informer, which holds every namespace of a namespaced
// resource. ok is also false when the serving informers lack the index. The
// objects are the cached ones: callers must not modify or keep them.
func (d *DynamicResourceCache) ReadableIndexed(gvr schema.GroupVersionResource, namespace, index string, keys ...string) (objs []*unstructured.Unstructured, ok bool) {
	entries, ok := d.servedEntries(gvr, namespace)
	if !ok {
		return nil, false
	}
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	for _, e := range entries {
		items, err := readIndex(e.informer.GetIndexer(), index, keys)
		if err != nil {
			return nil, false
		}
		for _, item := range items {
			if u, isUnstructured := item.(*unstructured.Unstructured); isUnstructured && (namespace == "" || u.GetNamespace() == namespace) {
				objs = append(objs, u)
			}
		}
	}
	return objs, true
}
