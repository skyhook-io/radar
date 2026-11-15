package k8score

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// subscribeRefresh coalesces discovery refresh notifications. The caller must
// unsubscribe when its cache stops; notifications never block discovery.
func (d *ResourceDiscovery) subscribeRefresh() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	d.refreshListenersMu.Lock()
	if d.refreshListeners == nil {
		d.refreshListeners = make(map[chan struct{}]struct{})
	}
	d.refreshListeners[ch] = struct{}{}
	d.refreshListenersMu.Unlock()
	return ch, func() { d.refreshListenersMu.Lock(); delete(d.refreshListeners, ch); d.refreshListenersMu.Unlock() }
}

func (d *ResourceDiscovery) notifyRefresh() {
	if d == nil {
		return
	}
	d.refreshListenersMu.Lock()
	defer d.refreshListenersMu.Unlock()
	for ch := range d.refreshListeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// RequestWatch schedules a cache-owned activation without waiting for discovery,
// probes or sync. Concurrent requests share one task per GVR; recent denials are
// answered by EnsureWatchingContext's denial memory rather than new API calls.
func (d *DynamicResourceCache) RequestWatch(gvr schema.GroupVersionResource) {
	if d == nil || d.hasCoveringInformer(gvr, "") || d.recentDenial(gvr, "") != nil {
		return
	}
	d.mu.Lock()
	if d.stopped || d.watchRequests[gvr] {
		d.mu.Unlock()
		return
	}
	if d.watchRequests == nil {
		d.watchRequests = make(map[schema.GroupVersionResource]bool)
	}
	d.watchRequests[gvr] = true
	d.mu.Unlock()
	go func() {
		defer func() { d.mu.Lock(); delete(d.watchRequests, gvr); d.mu.Unlock() }()
		_ = d.EnsureWatchingContext(d.probeCtx, gvr, "")
	}()
}

// StartWatchReconciler activates served required kinds now, after discovery
// refreshes, and every 75 seconds (longer than the denial TTL). It belongs to
// this cache instance and stops with it, including across context or scope changes.
func (d *DynamicResourceCache) StartWatchReconciler() {
	d.startWatchReconciler(deniedScopeTTL + 15*time.Second)
}

func (d *DynamicResourceCache) startWatchReconciler(interval time.Duration) {
	if d == nil || d.config.Discovery == nil || len(d.config.RequiredWatchKinds) == 0 {
		return
	}
	d.watchReconcilerOnce.Do(func() {
		d.watchReconcilerStarted.Store(true)
		// The initial CRD informer list must not launch the generic small-CRD
		// scan before the deferred-informer gate opens.
		if resources, err := d.config.Discovery.GetAPIResources(); err == nil {
			var keys []string
			for _, r := range resources {
				if r.IsCRD {
					keys = append(keys, r.Group+"/"+r.Name)
				}
			}
			d.markCRDsSeen(keys)
		}
		_ = d.addGVRChangeHandler(crdGVR, d.crdEventHandler(), true)
		refreshed, unsubscribe := d.config.Discovery.subscribeRefresh()
		d.reconcileWatches()
		go func() {
			defer unsubscribe()
			tick := time.NewTicker(interval)
			defer tick.Stop()
			for {
				select {
				case <-d.stopCh:
					return
				case <-refreshed:
					d.reconcileWatches()
				case <-tick.C:
					_ = d.config.Discovery.RefreshIfStale()
					d.reconcileWatches()
				}
			}
		}()
	})
}

func (d *DynamicResourceCache) reconcileWatches() {
	if !d.watchReconcilerStarted.Load() || d.config.Discovery == nil || d.isStopped() {
		return
	}
	if d.config.Discovery.SupportsWatchGVR(crdGVR) {
		d.RequestWatch(crdGVR)
	}
	for _, k := range d.config.RequiredWatchKinds {
		if gvr, ok := d.config.Discovery.GetGVRWithGroup(k.Kind, k.Group); ok && d.config.Discovery.SupportsWatchGVR(gvr) {
			d.RequestWatch(gvr)
		}
	}
}

func (d *DynamicResourceCache) requiredWatch(gvr schema.GroupVersionResource) bool {
	if d.config.Discovery == nil {
		return false
	}
	kind := d.config.Discovery.GetKindForGVR(gvr)
	for _, k := range d.config.RequiredWatchKinds {
		if k.Group == gvr.Group && k.Kind == kind {
			return true
		}
	}
	return false
}
