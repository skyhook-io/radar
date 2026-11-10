package k8s

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// ResourceDiscovery wraps the shared k8score implementation.
// The singleton pattern (Init/Get/Reset) stays here because it depends on
// the package-level GetDiscoveryClient() function.
type ResourceDiscovery struct {
	*k8score.ResourceDiscovery
	// backgroundRefresh is held while ServedGVR's refresh runs, so callers
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

var (
	resourceDiscovery *ResourceDiscovery
	// The client the singleton was built against. The singleton is served only
	// while this is still the active client: a context switch resets discovery
	// BEFORE it swaps the client, so a build that starts and finishes inside
	// that gap looks current by every test available at publish time — the
	// staleness only becomes visible when the swap lands, which is why it is
	// checked on every read instead.
	resourceDiscoveryClient *discovery.DiscoveryClient
	discoveryMu             sync.Mutex
	// Bumped by every reset. Catches what the client binding cannot: a reset
	// with no client swap (in-cluster recovery), where a build from before the
	// reset carries the right client but pre-reset data.
	discoveryEpoch uint64
)

// currentResourceDiscoveryLocked returns the singleton only while the client
// it was built against is still the active one. discoveryMu must be held.
func currentResourceDiscoveryLocked() *ResourceDiscovery {
	if resourceDiscovery == nil {
		return nil
	}
	if resourceDiscoveryClient != GetDiscoveryClient() {
		return nil
	}
	return resourceDiscovery
}

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
	discoveryMu.Lock()
	if currentResourceDiscoveryLocked() != nil {
		discoveryMu.Unlock()
		return nil
	}
	client := GetDiscoveryClient()
	epoch := discoveryEpoch
	discoveryMu.Unlock()

	// Checked while the value is still a *discovery.DiscoveryClient: handing a
	// nil one to NewResourceDiscovery's discovery.DiscoveryInterface parameter
	// makes a non-nil interface holding a nil pointer, so its own nil guard does
	// not fire and the first call on the client dereferences nil.
	if client == nil {
		return fmt.Errorf("discovery client not initialized")
	}

	// Built without the lock: NewResourceDiscovery performs an API round-trip,
	// and holding discoveryMu across it would block a concurrent reset.
	core, err := k8score.NewResourceDiscovery(client)
	if err != nil {
		return err
	}

	// A discarded build is not an error: it means a newer context switch owns
	// the singleton now, and that switch runs its own init.
	publishResourceDiscovery(core, epoch, client)
	return nil
}

// publishResourceDiscovery installs core as the singleton, recording the
// client it was built against, unless the build is known stale: a reset bumped
// the epoch since the snapshot, the client was already swapped, or a valid
// singleton for the current client exists. It cannot catch a build that runs
// entirely between a context switch's reset and its client swap — at that
// moment the old client is still the active one and nothing distinguishes the
// build from a legitimate one. That case is why the binding is recorded: the
// moment the swap lands, currentResourceDiscoveryLocked stops serving the
// singleton, and the next init replaces it.
func publishResourceDiscovery(core *k8score.ResourceDiscovery, epoch uint64, client *discovery.DiscoveryClient) {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	if discoveryEpoch != epoch {
		return
	}
	if GetDiscoveryClient() != client {
		return
	}
	if currentResourceDiscoveryLocked() != nil {
		return
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	resourceDiscoveryClient = client
}

// GetResourceDiscovery returns the singleton discovery instance, or nil while
// none exists for the currently active client.
func GetResourceDiscovery() *ResourceDiscovery {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	return currentResourceDiscoveryLocked()
}

// ResetResourceDiscovery clears the resource discovery instance so it can be
// reinitialized for a new cluster after context switch.
func ResetResourceDiscovery() {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()

	resourceDiscovery = nil
	resourceDiscoveryClient = nil
	discoveryEpoch++
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
	d := GetResourceDiscovery()
	if d == nil || d.ResourceDiscovery == nil {
		return schema.GroupVersionResource{}, false
	}
	if d.backgroundRefresh.CompareAndSwap(false, true) {
		go func() {
			defer d.backgroundRefresh.Store(false)
			if err := d.RefreshIfStale(); err != nil {
				log.Printf("[discovery] Background refresh failed: %v", err)
			}
		}()
	}
	return d.GetGVRWithGroup(kind, group)
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
