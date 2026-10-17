package k8score

import (
	"slices"

	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
)

// kindStore is one informer's store for a kind and that informer's own sync.
type kindStore struct {
	indexer cache.Indexer
	synced  func() bool
}

func (rc *ResourceCache) addKindStore(key, namespace string, inf cache.SharedIndexInformer) {
	if rc.kindStores[key] == nil {
		rc.kindStores[key] = map[string]kindStore{}
	}
	rc.kindStores[key][namespace] = kindStore{indexer: inf.GetIndexer(), synced: inf.HasSynced}
}

// storeFor returns the store holding namespace's objects: that namespace's own
// informer, or the one informer serving the kind's whole scope.
func (rc *ResourceCache) storeFor(key, namespace string) (kindStore, bool) {
	stores := rc.kindStores[key]
	if st, ok := stores[namespace]; ok {
		return st, true
	}
	st, ok := stores[""]
	return st, ok
}

// pastDeadline reports a tracked kind whose deferred sync deadline fired.
// Background kinds (Events) have no deadline, and a kind mid-Phase-1 is not in
// the tracking map, so neither is ever past it.
func (rc *ResourceCache) pastDeadline(key string) bool {
	if !rc.deferredFailed.Load() || rc.backgroundKeys[key] {
		return false
	}
	rc.deferredMu.RLock()
	_, tracked := rc.deferredSynced[key]
	rc.deferredMu.RUnlock()
	return tracked
}

func (rc *ResourceCache) storeReadiness(key string, st kindStore) KindReadiness {
	if st.synced() {
		return KindReady
	}
	if rc.pastDeadline(key) {
		return KindFailed
	}
	return KindPending
}

// KindReadAssessment contains full membership facts for one cache handle.
// Requested nil means all namespaces; an empty non-nil set means no target.
// Consumers must project namespaces to what the caller may see.
type KindReadAssessment struct {
	// Readiness is the whole kind's state; it is Ready only once every
	// informer for the kind has synced.
	Readiness KindReadiness
	Requested []string
	// AllNamespaces: one informer covers every namespace, so each namespace's
	// state is Readiness.
	AllNamespaces bool
	// Read, Loading, Failed and Uncovered partition the target: Requested, or
	// for an all-namespaces request on a namespace-scoped collector, the
	// namespaces it collects. A namespace is Read when the store holding it is
	// complete now; Failed when its load missed the sync deadline. Uncovered
	// is known from configuration before any sync. An all-namespaces request
	// on an all-namespaces informer has no partition: Readiness is its state
	// and Read is nil. So is a kind Radar doesn't watch (Readiness
	// Unavailable), which has nothing to assess. Otherwise Read is non-nil
	// even when empty, because readers take a nil namespace list to mean
	// every namespace.
	Read, Loading, Failed, Uncovered []string
	// Complete: every requested namespace is readable now; an empty target is
	// complete. An all-namespaces request (Requested nil) is complete only on
	// one informer over every namespace that has loaded: a namespace-scoped
	// cache never collects every namespace.
	Complete bool
}

// AssessKindRead assesses an already authorized namespace target without I/O.
// It describes the scope this cache can serve now, not a read that happened.
func (rc *ResourceCache) AssessKindRead(resource string, requested []string) KindReadAssessment {
	a := KindReadAssessment{Readiness: rc.KindReadinessFor(resource), Requested: slices.Clone(requested)}
	if a.Readiness == KindUnavailable {
		return a
	}
	covered := rc.KindNamespaces(resource)
	if covered == nil {
		a.AllNamespaces = true
		if requested == nil {
			a.Complete = a.Readiness == KindReady
			return a
		}
		a.Read = []string{}
		a.assign(a.Readiness, slices.Clone(requested)...)
		a.Complete = len(a.Loading) == 0 && len(a.Failed) == 0
		return a
	}
	a.Read = []string{}
	target := requested
	if target == nil {
		target = covered
	}
	isCovered := namespaceSet(covered)
	for _, namespace := range target {
		if !isCovered[namespace] {
			a.Uncovered = append(a.Uncovered, namespace)
			continue
		}
		state := a.Readiness
		if st, ok := rc.storeFor(resource, namespace); ok {
			state = rc.storeReadiness(resource, st)
		}
		a.assign(state, namespace)
	}
	a.Complete = requested != nil && len(a.Loading) == 0 && len(a.Failed) == 0 && len(a.Uncovered) == 0
	return a
}

// TargetReadiness is the state of the requested namespaces alone: Pending
// while any of them loads, Failed when one missed its load deadline and none
// is loading, Ready otherwise. Other namespaces of the kind don't count, so a
// target of loaded namespaces is Ready while another namespace still loads.
// Uncovered namespaces don't count either; Complete covers them. Without a
// partition (one informer for every namespace and no target), it is Readiness.
func (a KindReadAssessment) TargetReadiness() KindReadiness {
	if a.Read == nil {
		return a.Readiness
	}
	switch {
	case len(a.Loading) > 0:
		return KindPending
	case len(a.Failed) > 0:
		return KindFailed
	}
	return KindReady
}

// namespaceSet indexes namespaces for membership checks: a collector's list
// and a caller's target can each hold thousands.
func namespaceSet(namespaces []string) map[string]bool {
	set := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		set[ns] = true
	}
	return set
}

func (a *KindReadAssessment) assign(state KindReadiness, namespaces ...string) {
	switch state {
	case KindReady:
		a.Read = append(a.Read, namespaces...)
	case KindFailed:
		a.Failed = append(a.Failed, namespaces...)
	default:
		a.Loading = append(a.Loading, namespaces...)
	}
}

// readableIndexers returns one read per store a request needs. With nil
// namespaces each read is a whole store, for the kind's whole scope; otherwise
// each read selects one namespace's objects from the store holding them. It
// fails unless every store involved is complete now.
func (rc *ResourceCache) readableIndexers(resource string, namespaces []string) ([]func() []any, bool) {
	if rc == nil || !rc.isEnabled(resource) || rc.kindStores[resource] == nil {
		return nil, false
	}
	stores := rc.kindStores[resource]
	var reads []func() []any
	if namespaces == nil {
		for _, st := range stores {
			if !st.synced() {
				return nil, false
			}
			reads = append(reads, st.indexer.List)
		}
		return reads, true
	}
	covered := rc.KindNamespaces(resource)
	isCovered := namespaceSet(covered)
	for _, namespace := range namespaces {
		if covered != nil && !isCovered[namespace] {
			return nil, false
		}
		st, ok := rc.storeFor(resource, namespace)
		if !ok || !st.synced() {
			return nil, false
		}
		reads = append(reads, func() []any {
			items, _ := st.indexer.ByIndex(cache.NamespaceIndex, namespace)
			return items
		})
	}
	return reads, true
}

// ReadableObjects lists resource in namespaces straight from the informer
// stores holding them. ok is false unless each namespace's store is complete
// now, so a namespace whose own informer has synced can be read while another
// namespace of the kind is still loading. It never consults the kind's
// whole-kind readiness, so it is for readers that assessed the scope first.
func (rc *ResourceCache) ReadableObjects(resource string, namespaces []string) ([]apiruntime.Object, bool) {
	reads, ok := rc.readableIndexers(resource, namespaces)
	if !ok {
		return nil, false
	}
	var out []apiruntime.Object
	for _, read := range reads {
		for _, item := range read() {
			if obj, isObj := item.(apiruntime.Object); isObj {
				out = append(out, obj)
			}
		}
	}
	return out, true
}

// ReadableCount counts resource in namespaces under ReadableObjects' rules
// without copying the objects.
func (rc *ResourceCache) ReadableCount(resource string, namespaces []string) (int, bool) {
	reads, ok := rc.readableIndexers(resource, namespaces)
	if !ok {
		return 0, false
	}
	n := 0
	for _, read := range reads {
		n += len(read())
	}
	return n, true
}

// ReadableObject gets one object under ReadableObjects' rules. found reports
// whether it exists; ok is false when its namespace is not readable now, in
// which case a miss proves nothing. namespace is "" for a cluster-scoped kind.
func (rc *ResourceCache) ReadableObject(resource, namespace, name string) (obj apiruntime.Object, found, ok bool) {
	if rc == nil || !rc.isEnabled(resource) || rc.kindStores[resource] == nil {
		return nil, false, false
	}
	if covered := rc.KindNamespaces(resource); covered != nil && !slices.Contains(covered, namespace) {
		return nil, false, false
	}
	st, exists := rc.storeFor(resource, namespace)
	if !exists || !st.synced() {
		return nil, false, false
	}
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	item, found, err := st.indexer.GetByKey(key)
	if err != nil || !found {
		return nil, false, err == nil
	}
	obj, isObj := item.(apiruntime.Object)
	return obj, isObj, isObj
}
