package k8score

import (
	"sort"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

type dynamicObservationScope struct {
	entries     []*informerEntry
	namespaces  []string
	clusterWide bool
}

// DynamicObservationView indexes informer scopes for one reader, such as a
// search request. Returned namespace slices are read-only. Sync, retained
// observations and probe retries remain live; informer lifecycle changes
// rebuild the index. A view must not be used concurrently.
type DynamicObservationView struct {
	cache      *DynamicResourceCache
	scopes     map[schema.GroupVersionResource]dynamicObservationScope
	generation uint64
}

// NewObservationView builds the selected GVRs' scopes in one informer pass.
func (d *DynamicResourceCache) NewObservationView(gvrs []schema.GroupVersionResource) *DynamicObservationView {
	v := &DynamicObservationView{cache: d, scopes: make(map[schema.GroupVersionResource]dynamicObservationScope, len(gvrs))}
	for _, gvr := range gvrs {
		v.scopes[gvr] = dynamicObservationScope{}
	}
	if d != nil {
		d.mu.RLock()
		defer d.mu.RUnlock()
		v.rebuildLocked()
	}
	return v
}

func (v *DynamicObservationView) rebuildLocked() {
	for gvr := range v.scopes {
		v.scopes[gvr] = dynamicObservationScope{}
	}
	for key, entry := range v.cache.informers {
		scope, selected := v.scopes[key.gvr]
		if !selected {
			continue
		}
		scope.entries = append(scope.entries, entry)
		if key.ns == "" {
			scope.clusterWide = true
		} else {
			scope.namespaces = append(scope.namespaces, key.ns)
		}
		v.scopes[key.gvr] = scope
	}
	for _, scope := range v.scopes {
		sort.Strings(scope.namespaces)
	}
	v.generation = v.cache.informerGeneration
}

// NamespaceObservation for a selected GVR has the same readiness and retry contract as the cache's
// NamespaceObservation, without rebuilding the kind's scope for each namespace.
func (v *DynamicObservationView) NamespaceObservation(gvr schema.GroupVersionResource, namespace string) DynamicResourceObservation {
	d := v.cache
	if d == nil {
		return DynamicResourceObservation{State: DynamicObservationUnwatched, ReasonCode: "not_observed"}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.stopped {
		return DynamicResourceObservation{State: DynamicObservationUnwatched, ReasonCode: "cache_stopped"}
	}
	if v.generation != d.informerGeneration {
		v.rebuildLocked()
	}
	return d.observationLocked(gvr, namespace, v.scopes[gvr])
}
