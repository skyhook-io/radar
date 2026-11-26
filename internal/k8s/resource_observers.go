package k8s

import (
	"slices"
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
//   - source is the request clientset (GetClient()) of the connection the
//     reporting cache belongs to, not the informer clientset the cache lists
//     through. Observers are attached to the cache that reports the event.
//   - It sees typed resources only, and not core Events, which take the
//     cache's separate OnEventChange path. The dynamic (CRD) cache does not
//     feed these observers.
//
// The cache's Changes() channel is not a substitute: it has a single reader
// (the SSE broadcaster), it never carries noisy updates, and a send to a full
// buffer is dropped.
type ResourceObserver func(source kubernetes.Interface, change ResourceChange, obj, oldObj any)

type resourceObservers struct {
	mu          sync.RWMutex
	subscribers []*ResourceObserver
}

func (c *ResourceCache) SubscribeObservedChanges(fn ResourceObserver) func() {
	c.observers.mu.Lock()
	c.observers.subscribers = append(slices.Clone(c.observers.subscribers), &fn)
	c.observers.mu.Unlock()
	return func() {
		c.observers.mu.Lock()
		c.observers.subscribers = slices.DeleteFunc(slices.Clone(c.observers.subscribers), func(subscriber *ResourceObserver) bool {
			return subscriber == &fn
		})
		c.observers.mu.Unlock()
	}
}

func (c *ResourceCache) notifyObservedChanges(source kubernetes.Interface, change ResourceChange, obj, oldObj any) {
	c.observers.mu.RLock()
	subscribers := c.observers.subscribers
	c.observers.mu.RUnlock()
	for _, fn := range subscribers {
		(*fn)(source, change, obj, oldObj)
	}
}

func observedChangeHandler(source kubernetes.Interface, observers ...ResourceObserver) func(change ResourceChange, obj, oldObj any) {
	return func(change ResourceChange, obj, oldObj any) {
		for _, fn := range observers {
			fn(source, change, obj, oldObj)
		}
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
