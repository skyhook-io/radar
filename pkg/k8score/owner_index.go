package k8score

import (
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
// references name uid, under ReadableObjects' rules: ok is false unless each
// namespace's store is complete now, and nil namespaces means the kind's whole
// scope. The objects are the cached ones: callers must not modify or keep
// them.
func (rc *ResourceCache) ReadableDependents(resource string, namespaces []string, uid string) ([]apiruntime.Object, bool) {
	if rc == nil || !rc.isEnabled(resource) || rc.kindStores[resource] == nil {
		return nil, false
	}
	var hits []any
	if namespaces == nil {
		for _, st := range rc.kindStores[resource] {
			if !st.synced() {
				return nil, false
			}
			items, err := st.indexer.ByIndex(OwnerUIDIndex, uid)
			if err != nil {
				return nil, false
			}
			hits = append(hits, items...)
		}
	} else {
		covered := rc.KindNamespaces(resource)
		isCovered := namespaceSet(covered)
		wanted := namespaceSet(namespaces)
		read := map[cache.Indexer]bool{}
		for _, namespace := range namespaces {
			if covered != nil && !isCovered[namespace] {
				return nil, false
			}
			st, ok := rc.storeFor(resource, namespace)
			if !ok || !st.synced() {
				return nil, false
			}
			if read[st.indexer] {
				continue
			}
			read[st.indexer] = true
			items, err := st.indexer.ByIndex(OwnerUIDIndex, uid)
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

// Indexes reports whether gr's informers carry index: OwnerUIDIndex for every
// resource but Events, and the indexes DynamicCacheConfig.Indexers gives it.
func (d *DynamicResourceCache) Indexes(gr schema.GroupResource, index string) bool {
	if d == nil {
		return false
	}
	if index == OwnerUIDIndex {
		return !isEventResource(gr.WithVersion(""))
	}
	_, ok := d.config.Indexers[gr][index]
	return ok
}

// ReadableIndexed returns the objects of gvr in namespace that index holds
// under key — for OwnerUIDIndex, those whose owner references name the UID key
// — from the informers serving namespace now, under ReadableObject's rules.
// namespace "" reads only a cluster-wide informer, which holds every namespace
// of a namespaced resource. ok is also false when the serving informers lack
// the index. The objects are the cached ones: callers must not modify or keep
// them.
func (d *DynamicResourceCache) ReadableIndexed(gvr schema.GroupVersionResource, namespace, index, key string) (objs []*unstructured.Unstructured, server DynamicReadServer, ok bool) {
	server, ok = d.ReadServer(gvr, namespace)
	if !ok {
		return nil, server, false
	}
	for _, e := range server.entries {
		items, err := e.informer.GetIndexer().ByIndex(index, key)
		if err != nil {
			return nil, server, false
		}
		for _, item := range items {
			if u, isUnstructured := item.(*unstructured.Unstructured); isUnstructured && (namespace == "" || u.GetNamespace() == namespace) {
				objs = append(objs, u)
			}
		}
	}
	return objs, server, true
}
