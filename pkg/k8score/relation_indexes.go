package k8score

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/relations"
)

const ReflectsIndex = "reflects"

// ReflectsKey asks for a source's mirrors in every collected namespace.
func ReflectsKey(namespace, name string) string { return namespace + "/" + name + "\x00*" }

func referrerIndexers(gr schema.GroupResource) cache.Indexers {
	out := cache.Indexers{}
	for name, fn := range relations.ReferrerIndexers[gr] {
		out[name] = func(obj any) ([]string, error) {
			if q, ok := obj.(indexQuery); ok {
				return q, nil
			}
			return fn(obj)
		}
	}
	return out
}

var relationIndexers = map[string]cache.Indexers{
	Secrets:                referrerIndexers(schema.GroupResource{Resource: "secrets"}),
	ConfigMaps:             referrerIndexers(schema.GroupResource{Resource: "configmaps"}),
	PersistentVolumes:      referrerIndexers(schema.GroupResource{Resource: "persistentvolumes"}),
	PersistentVolumeClaims: referrerIndexers(schema.GroupResource{Resource: "persistentvolumeclaims"}),
}

var dynamicRelationIndexers = func() map[schema.GroupResource]cache.Indexers {
	out := map[schema.GroupResource]cache.Indexers{}
	for gr := range relations.ReferrerIndexers {
		out[gr] = referrerIndexers(gr)
	}
	return out
}()

// TypedIndexMissing distinguishes a missing index from an incomplete store.
func (rc *ResourceCache) TypedIndexMissing(resource, index string) bool {
	if index == "" {
		return false
	}
	for _, st := range rc.kindStores[resource] {
		if st.synced() && st.indexer.GetIndexers()[index] == nil {
			return true
		}
	}
	return false
}

// DynamicIndexMissing checks only the ready stores serving this scope.
func (d *DynamicResourceCache) DynamicIndexMissing(gvr schema.GroupVersionResource, namespace, index string) bool {
	if index == "" {
		return false
	}
	entries, ok := d.servedEntries(gvr, namespace)
	if !ok {
		return false
	}
	for _, e := range entries {
		if e.informer.GetIndexer().GetIndexers()[index] == nil {
			return true
		}
	}
	return false
}
