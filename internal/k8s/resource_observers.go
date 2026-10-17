package k8s

import (
	"sync"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/client-go/kubernetes"
)

// ResourceObserver is the hook for code that must see every change to a
// typed-informer resource: initial-sync adds, updates the noisy filter keeps
// off the timeline and the changes channel, and deletes. It is fed from the
// resource cache's OnObservedChange.
//
// The contract:
//   - It runs on the informer's delivery goroutine, so it must return
//     quickly. Filter on the object first (kind, operation, a cheap
//     predicate) and take locks only for what passes.
//   - Hand slow work (network calls, discovery, anything that can block) to
//     a worker of your own.
//   - source is the clientset the reporting cache was built from. Informer
//     shutdown on a context switch is asynchronous, so a late callback can
//     belong to a cache that has since been replaced; an observer holding its
//     own clientset compares against source to ignore it.
//   - It sees typed resources only, and not core Events, which take the
//     cache's separate OnEventChange path. The dynamic (CRD) cache does not
//     feed these observers.
//
// The cache's Changes() channel is not a substitute: it has a single reader
// (the SSE broadcaster), it never carries noisy updates, and a send to a full
// buffer is dropped.
type ResourceObserver func(source kubernetes.Interface, change ResourceChange, obj, oldObj any)

var (
	resourceObserversMu sync.RWMutex
	resourceObservers   []ResourceObserver
)

// OnResourceObserved registers an observer for the lifetime of the process;
// it follows every cache built after a context switch or rescope.
func OnResourceObserved(fn ResourceObserver) {
	resourceObserversMu.Lock()
	defer resourceObserversMu.Unlock()
	resourceObservers = append(resourceObservers, fn)
}

func notifyResourceObservers(source kubernetes.Interface, change ResourceChange, obj, oldObj any) {
	resourceObserversMu.RLock()
	observers := resourceObservers
	resourceObserversMu.RUnlock()
	for _, fn := range observers {
		fn(source, change, obj, oldObj)
	}
}

// observedChangeHandler builds a cache's OnObservedChange. cacheObservers
// keep that one cache's own state and are bound to it here, not looked up at
// delivery: a late event from a replaced cache must land in the replaced
// cache's state, never in its successor's. They run first, in order, then
// every process-wide observer.
func observedChangeHandler(source kubernetes.Interface, cacheObservers ...ResourceObserver) func(change ResourceChange, obj, oldObj any) {
	return func(change ResourceChange, obj, oldObj any) {
		for _, fn := range cacheObservers {
			fn(source, change, obj, oldObj)
		}
		notifyResourceObservers(source, change, obj, oldObj)
	}
}

func (index *secretDataManagerWriteIndex) observeChange(_ kubernetes.Interface, change ResourceChange, obj, _ any) {
	index.reconcile(change, obj)
}

func (t *cronJobScheduleObservationTracker) observeChange(_ kubernetes.Interface, change ResourceChange, obj, _ any) {
	if cj, ok := obj.(*batchv1.CronJob); ok {
		t.observe(change.Operation, cj)
	}
}
