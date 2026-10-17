package k8s

import (
	"sync"

	"k8s.io/client-go/kubernetes"
)

// ResourceObserver receives every typed-informer change, noisy ones and
// initial-sync adds included. source is the clientset the reporting cache was
// built from: informer shutdown on a context switch is asynchronous, so a late
// callback can belong to a cache that has since been replaced, and an
// observer holding its own clientset compares against source to ignore it.
// Observers run on the informer's delivery goroutine and must return quickly.
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
