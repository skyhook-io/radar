package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// ResourceDiscovery wraps the shared k8score implementation.
type ResourceDiscovery struct {
	*k8score.ResourceDiscovery
	// backgroundRefresh is held while a background refresh runs, so callers
	// arriving during a slow refresh don't each park another goroutine.
	backgroundRefresh atomic.Bool
	// missRefresh serializes LookupRefreshingOnMiss's refreshes; missRefreshAt
	// is when the last one started.
	missRefresh   sync.Mutex
	missRefreshAt time.Time
}

// Re-export types so existing callers compile without changes.
type APIResource = k8score.APIResource
type DiscoveryStats = k8score.DiscoveryStats

// isMoreStableVersion delegates to the shared implementation.
// Needed by dynamic_cache.go (same package, calls it without qualifier).
var isMoreStableVersion = k8score.IsMoreStableVersion

// InitResourceDiscovery initializes the resource discovery module.
//
// Not a sync.Once: a failed attempt must not spend the one initialization the
// Once allows, or the next call skips the body and reports success for a
// subsystem that never came up. Discovery starts in parallel with the caches,
// so it reaches here before a client exists whenever the cluster is
// unreachable, and InitAllSubsystems lets that goroutine outlive the failure.
func InitResourceDiscovery() error {
	return Current().initResourceDiscovery(context.Background())
}

func (conn *Connection) initResourceDiscovery(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn.Discovery() != nil {
		return nil
	}
	client := conn.DiscoveryClient()
	if client == nil {
		return fmt.Errorf("discovery client not initialized")
	}
	core, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		return err
	}
	conn.publishDiscovery(core)
	return nil
}

func (c *Connection) publishDiscovery(core *k8score.ResourceDiscovery) {
	c.discovery.CompareAndSwap(nil, &ResourceDiscovery{ResourceDiscovery: core})
}

// GetResourceDiscovery returns the installed connection's discovery, or nil
// until its first build finishes. Lock-free, so diagnostics can read it too.
func GetResourceDiscovery() *ResourceDiscovery { return Current().Discovery() }

// ResetResourceDiscovery gives the next build a fresh connection on the same
// clients. A build still running for the old one lands there, unserved.
func ResetResourceDiscovery() {
	clientMu.Lock()
	installedConn.Store(installedConn.Load().fresh())
	clientMu.Unlock()
}

// Refresh delegates to the embedded implementation.
// Note: GetAPIResources, GetGVR, GetGVRWithGroup, GetResource, IsKnownResource,
// IsCRD, SupportsWatch, SupportsWatchGVR, GetKindForGVR, and Stats are all
// promoted from the embedded *k8score.ResourceDiscovery and work without wrappers.

// GetGVR returns the GroupVersionResource — needed as a direct method because
// the embedded type is accessed via pointer and callers pass *ResourceDiscovery.
func (d *ResourceDiscovery) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	if d == nil || d.ResourceDiscovery == nil {
		return schema.GroupVersionResource{}, false
	}
	return d.ResourceDiscovery.GetGVR(kindOrName)
}

// GetGVRWithGroup returns the GroupVersionResource for a kind with a specific API group.
func (d *ResourceDiscovery) GetGVRWithGroup(kindOrName string, group string) (schema.GroupVersionResource, bool) {
	if d == nil || d.ResourceDiscovery == nil {
		return schema.GroupVersionResource{}, false
	}
	return d.ResourceDiscovery.GetGVRWithGroup(kindOrName, group)
}

// ServedGVR resolves a kind in a group to the most stable version the connected
// cluster serves. It satisfies gitops.FluxGVRResolver. Expired discovery is
// refreshed in the background rather than on the caller's request: a slow
// discovery endpoint for an unrelated API would otherwise use up the action's
// deadline. Callers get the cached version until a refresh succeeds, so after
// a CRD upgrade retires a version, actions can still use it until then.
func ServedGVR(group, kind string) (schema.GroupVersionResource, bool) {
	return Current().ServedGVR(group, kind)
}

// ServedGVRFor returns the served-version resolver for a request: its bound
// connection's, so a bound write resolves on the cluster it was admitted to,
// else the installed connection's.
func ServedGVRFor(ctx context.Context) func(group, kind string) (schema.GroupVersionResource, bool) {
	if c := ConnectionFrom(ctx); c != nil {
		return c.ServedGVR
	}
	return ServedGVR
}

// ServedGVR is the package ServedGVR for this connection's discovery.
func (c *Connection) ServedGVR(group, kind string) (schema.GroupVersionResource, bool) {
	d := c.Discovery()
	if d == nil || d.ResourceDiscovery == nil {
		return schema.GroupVersionResource{}, false
	}
	d.refreshIfStaleInBackground()
	return d.GetGVRWithGroup(kind, group)
}

// refreshIfStaleInBackground reports whether the caller saw an expired snapshot.
func (d *ResourceDiscovery) refreshIfStaleInBackground() bool {
	if d == nil || d.ResourceDiscovery == nil || !d.IsStale() {
		return false
	}
	d.refreshInBackground(func() {
		if err := d.RefreshIfStale(); err != nil {
			log.Printf("[discovery] Background refresh failed: %v", err)
		}
	})
	return true
}

func (d *ResourceDiscovery) refreshInBackground(refresh func()) {
	if d.backgroundRefresh.CompareAndSwap(false, true) {
		go func() {
			defer d.backgroundRefresh.Store(false)
			refresh()
		}()
	}
}

// discoveryMissRefreshInterval bounds how often lookups that find nothing
// refresh discovery, so a misspelled kind doesn't cost a discovery round trip
// on every request.
const discoveryMissRefreshInterval = 30 * time.Second

// LookupRefreshingOnMiss returns lookup's answer. When lookup finds nothing,
// discovery is refreshed and lookup asked again, so an API installed since
// the last refresh resolves instead of reading as unknown. Concurrent misses
// share one refresh, and misses refresh at most once per
// discoveryMissRefreshInterval.
func (d *ResourceDiscovery) LookupRefreshingOnMiss(lookup func(*ResourceDiscovery) (schema.GroupVersionResource, bool)) (schema.GroupVersionResource, bool) {
	if d == nil || d.ResourceDiscovery == nil {
		return schema.GroupVersionResource{}, false
	}
	if gvr, ok := lookup(d); ok {
		return gvr, true
	}
	d.missRefresh.Lock()
	defer d.missRefresh.Unlock()
	if gvr, ok := lookup(d); ok || time.Since(d.missRefreshAt) < discoveryMissRefreshInterval {
		return gvr, ok
	}
	d.missRefreshAt = time.Now()
	if err := d.Refresh(); err != nil {
		log.Printf("[discovery] Refresh after a lookup miss failed: %v", err)
	}
	return lookup(d)
}

// RetryFailedRefresh refreshes discovery in the background when its last
// refresh returned nothing usable, at most once per
// discoveryMissRefreshInterval, so a catalog that came back empty fills in
// without waiting for the cache TTL.
func (d *ResourceDiscovery) RetryFailedRefresh() {
	if d == nil || d.ResourceDiscovery == nil || !d.LastRefreshFailed() {
		return
	}
	d.refreshInBackground(func() {
		d.LookupRefreshingOnMiss(func(*ResourceDiscovery) (schema.GroupVersionResource, bool) {
			return schema.GroupVersionResource{}, false
		})
	})
}

// SupportsWatchGVR checks if a GVR supports list and watch verbs.
func (d *ResourceDiscovery) SupportsWatchGVR(gvr schema.GroupVersionResource) bool {
	if d == nil || d.ResourceDiscovery == nil {
		return false
	}
	return d.ResourceDiscovery.SupportsWatchGVR(gvr)
}

// GetKindForGVR returns the Kind name for a given GVR.
func (d *ResourceDiscovery) GetKindForGVR(gvr schema.GroupVersionResource) string {
	if d == nil || d.ResourceDiscovery == nil {
		return ""
	}
	return d.ResourceDiscovery.GetKindForGVR(gvr)
}
