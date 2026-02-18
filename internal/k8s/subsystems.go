package k8s

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// InitAllSubsystems initializes all subsystems in the correct order.
// Used for both initial boot and after context switch.
//
// Returns an error if a critical subsystem (resource cache) fails to
// initialize. All other subsystems log warnings and continue in degraded mode.
//
// External subsystem callbacks (timeline, helm, traffic) must be registered
// via the Register*Funcs methods before calling this function.
//
// The progress callback receives human-readable status messages suitable for
// display in the UI (e.g. via SSE connection status updates).
func InitAllSubsystems(progress func(string)) error {
	// 1. Timeline — before caches so events during warmup are captured
	contextSwitchMu.RLock()
	tlReinitFn := timelineReinitFunc
	contextSwitchMu.RUnlock()
	if tlReinitFn != nil {
		progress("Initializing timeline...")
		if err := tlReinitFn(); err != nil {
			log.Printf("Warning: timeline init failed: %v", err)
		}
	}

	// 2. Resource cache (typed informers) — critical, everything depends on this
	progress("Loading workloads...")
	if err := InitResourceCache(); err != nil {
		return fmt.Errorf("resource cache init failed: %w", err)
	}
	if cache := GetResourceCache(); cache != nil {
		log.Printf("Resource cache initialized with %d resources", cache.GetResourceCount())
	}

	// 3. API resource discovery
	progress("Discovering API resources...")
	if err := InitResourceDiscovery(); err != nil {
		log.Printf("Warning: resource discovery init failed: %v", err)
	}

	// 4. Dynamic cache (factory init is synchronous; CRD warmup and discovery kick off async)
	progress("Loading custom resources...")
	if cache := GetResourceCache(); cache != nil {
		changeCh := cache.ChangesRaw()
		if err := InitDynamicResourceCache(changeCh); err != nil {
			log.Printf("Warning: dynamic resource cache init failed: %v", err)
		}

		// CRD warmup and full discovery run in background.
		// Common CRDs appear in topology quickly as they sync;
		// remaining CRDs appear as DiscoverAllCRDs completes.
		if dc := GetDynamicResourceCache(); dc != nil {
			go func() {
				// Warmup runs in its own recover so a panic there
				// doesn't prevent full CRD discovery from running.
				func() {
					defer func() {
						if r := recover(); r != nil {
							buf := make([]byte, 4096)
							n := runtime.Stack(buf, false)
							log.Printf("PANIC in CRD warmup: %v\n%s", r, buf[:n])
						}
					}()
					WarmupCommonCRDs()
				}()
				dc.DiscoverAllCRDs()
			}()
		}
	}

	// 5. Metrics history
	InitMetricsHistory()

	// 6. Helm
	contextSwitchMu.RLock()
	hReinitFn := helmReinitFunc
	contextSwitchMu.RUnlock()
	if hReinitFn != nil {
		progress("Loading Helm releases...")
		if err := hReinitFn(GetKubeconfigPath()); err != nil {
			log.Printf("Warning: Helm init failed: %v", err)
		}
	}

	// 7. Traffic
	contextSwitchMu.RLock()
	trReinitFn := trafficReinitFunc
	contextSwitchMu.RUnlock()
	if trReinitFn != nil {
		progress("Initializing traffic analysis...")
		if err := trReinitFn(); err != nil {
			log.Printf("Warning: traffic init failed: %v", err)
		}
	}

	return nil
}

// CacheMaxAge is the maximum age for cached state data before it's considered stale.
const CacheMaxAge = 24 * time.Hour

// InitAllSubsystemsCached is the cache-aware entry point for subsystem initialization.
// It attempts to load discovery, RBAC, and CRD access data from the state cache.
// On cache hit: skips expensive API server calls, starts informers directly.
// On cache miss: falls back to full InitAllSubsystems.
// A background goroutine always runs to validate and update the cache.
func InitAllSubsystemsCached(stateCache *StateCache, clusterID string, progress func(string)) error {
	if stateCache == nil {
		log.Println("No state cache provided, using full initialization")
		return InitAllSubsystems(progress)
	}

	// Try loading cached data
	cachedResources, err := stateCache.GetAPIResources(clusterID, CacheMaxAge)
	if err != nil {
		log.Printf("Warning: failed to read API resource cache: %v", err)
	}
	cachedRBAC, err := stateCache.GetRBACResults(clusterID, CacheMaxAge)
	if err != nil {
		log.Printf("Warning: failed to read RBAC cache: %v", err)
	}
	cachedCRDs, err := stateCache.GetCRDAccess(clusterID, CacheMaxAge)
	if err != nil {
		log.Printf("Warning: failed to read CRD access cache: %v", err)
	}

	cacheHit := cachedResources != nil && cachedRBAC != nil
	if !cacheHit {
		log.Println("State cache miss — performing full initialization")
		if err := InitAllSubsystems(progress); err != nil {
			return err
		}
		// Save results to cache for next boot
		go saveAllToCache(stateCache, clusterID)
		return nil
	}

	log.Printf("State cache hit — loading %d API resources, %d RBAC results, %d CRD access entries",
		len(cachedResources), len(cachedRBAC), len(cachedCRDs))

	// 1. Timeline — before caches so events during warmup are captured
	contextSwitchMu.RLock()
	tlReinitFn := timelineReinitFunc
	contextSwitchMu.RUnlock()
	if tlReinitFn != nil {
		progress("Initializing timeline...")
		if err := tlReinitFn(); err != nil {
			log.Printf("Warning: timeline init failed: %v", err)
		}
	}

	// 2. Resource cache (typed informers) — use cached RBAC to skip permission checks
	progress("Loading workloads (cached)...")
	_ = ImportRBACResults(cachedRBAC) // populates cachedPermResult
	if err := InitResourceCache(); err != nil {
		return fmt.Errorf("resource cache init failed: %w", err)
	}
	if cache := GetResourceCache(); cache != nil {
		log.Printf("Resource cache initialized with %d resources", cache.GetResourceCount())
	}

	// 3. API resource discovery — load from cache (skip ServerGroupsAndResources)
	progress("Loading API resources (cached)...")
	if err := InitResourceDiscoveryFromCache(cachedResources); err != nil {
		log.Printf("Warning: cached discovery load failed, falling back to live: %v", err)
		if err := InitResourceDiscovery(); err != nil {
			log.Printf("Warning: resource discovery init failed: %v", err)
		}
	}

	// 4. Dynamic cache factory init
	progress("Loading custom resources (cached)...")
	if cache := GetResourceCache(); cache != nil {
		changeCh := cache.ChangesRaw()
		if err := InitDynamicResourceCache(changeCh); err != nil {
			log.Printf("Warning: dynamic resource cache init failed: %v", err)
		}

		if dc := GetDynamicResourceCache(); dc != nil {
			if cachedCRDs != nil && len(cachedCRDs) > 0 {
				// Build GVR list from cached accessible CRDs
				var gvrs []schema.GroupVersionResource
				for _, c := range cachedCRDs {
					if c.Allowed {
						gvrs = append(gvrs, schema.GroupVersionResource{
							Group:    c.Group,
							Version:  c.Version,
							Resource: c.Resource,
						})
					}
				}
				// Start watching directly from cache (no probeAccess!)
				go func() {
					defer func() {
						if r := recover(); r != nil {
							buf := make([]byte, 4096)
							n := runtime.Stack(buf, false)
							log.Printf("PANIC in cached CRD warmup: %v\n%s", r, buf[:n])
						}
					}()
					dc.WarmupFromCache(gvrs, 30*time.Second)

					// Mark discovery as complete so EnsureWatching doesn't block
					dc.discoveryMu.Lock()
					if dc.discoveryStatus != CRDDiscoveryComplete {
						dc.discoveryStatus = CRDDiscoveryComplete
						close(dc.discoveryDone)
					}
					dc.discoveryMu.Unlock()
					notifyCRDDiscoveryComplete()
				}()
			} else {
				// No CRD cache — fall back to full discovery
				go func() {
					func() {
						defer func() {
							if r := recover(); r != nil {
								buf := make([]byte, 4096)
								n := runtime.Stack(buf, false)
								log.Printf("PANIC in CRD warmup: %v\n%s", r, buf[:n])
							}
						}()
						WarmupCommonCRDs()
					}()
					dc.DiscoverAllCRDs()
				}()
			}
		}
	}

	// 5. Metrics history
	InitMetricsHistory()

	// 6+7. Helm and Traffic — run in parallel (both are independent)
	progress("Loading Helm & traffic...")
	var initWg sync.WaitGroup

	contextSwitchMu.RLock()
	hReinitFn := helmReinitFunc
	trReinitFn := trafficReinitFunc
	contextSwitchMu.RUnlock()

	if hReinitFn != nil {
		initWg.Add(1)
		go func() {
			defer initWg.Done()
			if err := hReinitFn(GetKubeconfigPath()); err != nil {
				log.Printf("Warning: Helm init failed: %v", err)
			}
		}()
	}

	if trReinitFn != nil {
		initWg.Add(1)
		go func() {
			defer initWg.Done()
			if err := trReinitFn(); err != nil {
				log.Printf("Warning: traffic init failed: %v", err)
			}
		}()
	}

	initWg.Wait()

	// Background: validate cache and update if needed
	go backgroundValidateAndUpdate(stateCache, clusterID)

	return nil
}

// backgroundValidateAndUpdate runs a fresh discovery in the background,
// diffs against the cache, and saves updated results. This ensures the
// cache stays current even if the cluster has changed since last boot.
func backgroundValidateAndUpdate(stateCache *StateCache, clusterID string) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			log.Printf("PANIC in background cache validation: %v\n%s", r, buf[:n])
		}
	}()

	// Wait a bit for the main init to finish and the app to be usable
	time.Sleep(5 * time.Second)

	log.Println("[cache] Starting background validation...")

	// Re-discover API resources from the live cluster
	discovery := GetResourceDiscovery()
	if discovery != nil {
		if err := discovery.refresh(); err != nil {
			log.Printf("[cache] Warning: background refresh of API resources failed: %v", err)
		} else {
			// Save updated discovery to cache
			resources := discovery.ExportForCache()
			if err := stateCache.SaveAPIResources(clusterID, resources); err != nil {
				log.Printf("[cache] Warning: failed to save API resources to cache: %v", err)
			} else {
				log.Printf("[cache] Updated API resource cache (%d resources)", len(resources))
			}
		}
	}

	// Save CRD access results from the now-running dynamic cache
	if dc := GetDynamicResourceCache(); dc != nil {
		// Wait for CRD discovery to complete (if it was started)
		select {
		case <-dc.discoveryDone:
		case <-time.After(60 * time.Second):
			log.Println("[cache] Timeout waiting for CRD discovery in background validation")
		}

		crdAccess := dc.ExportCRDAccess()
		if err := stateCache.SaveCRDAccess(clusterID, crdAccess); err != nil {
			log.Printf("[cache] Warning: failed to save CRD access to cache: %v", err)
		} else {
			log.Printf("[cache] Updated CRD access cache (%d entries)", len(crdAccess))
		}
	}

	// Save RBAC results
	rbacResults := ExportRBACResults()
	if rbacResults != nil {
		if err := stateCache.SaveRBACResults(clusterID, rbacResults); err != nil {
			log.Printf("[cache] Warning: failed to save RBAC results to cache: %v", err)
		} else {
			log.Printf("[cache] Updated RBAC cache (%d entries)", len(rbacResults))
		}
	}

	// Update cluster last_seen_at
	stateCache.SaveCluster(clusterID, GetContextName(), "", "")

	log.Println("[cache] Background validation complete")
}

// saveAllToCache saves all current subsystem state to the cache.
// Called after a cache-miss full init to populate the cache for next boot.
func saveAllToCache(stateCache *StateCache, clusterID string) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			log.Printf("PANIC in saveAllToCache: %v\n%s", r, buf[:n])
		}
	}()

	// Wait for initialization to settle
	time.Sleep(3 * time.Second)

	// Save API resources
	if discovery := GetResourceDiscovery(); discovery != nil {
		resources := discovery.ExportForCache()
		if err := stateCache.SaveAPIResources(clusterID, resources); err != nil {
			log.Printf("[cache] Warning: failed to save API resources: %v", err)
		} else {
			log.Printf("[cache] Saved %d API resources to cache", len(resources))
		}
	}

	// Save RBAC results
	rbacResults := ExportRBACResults()
	if rbacResults != nil {
		if err := stateCache.SaveRBACResults(clusterID, rbacResults); err != nil {
			log.Printf("[cache] Warning: failed to save RBAC results: %v", err)
		} else {
			log.Printf("[cache] Saved %d RBAC results to cache", len(rbacResults))
		}
	}

	// Wait for CRD discovery before saving CRD access
	if dc := GetDynamicResourceCache(); dc != nil {
		select {
		case <-dc.discoveryDone:
		case <-time.After(120 * time.Second):
			log.Println("[cache] Timeout waiting for CRD discovery to save cache")
		}

		crdAccess := dc.ExportCRDAccess()
		if err := stateCache.SaveCRDAccess(clusterID, crdAccess); err != nil {
			log.Printf("[cache] Warning: failed to save CRD access: %v", err)
		} else {
			log.Printf("[cache] Saved %d CRD access entries to cache", len(crdAccess))
		}
	}

	log.Println("[cache] Initial cache population complete")
}

// ResetAllSubsystems tears down all subsystems in reverse order of init.
// Safe to call on first boot when singletons are nil.
// Each reset is wrapped in a panic recover so a failure in one subsystem
// does not prevent remaining subsystems from being torn down.
func ResetAllSubsystems() {
	// 7. Traffic
	contextSwitchMu.RLock()
	trResetFn := trafficResetFunc
	contextSwitchMu.RUnlock()
	if trResetFn != nil {
		safeReset("traffic", trResetFn)
	}

	// 6. Helm
	contextSwitchMu.RLock()
	hResetFn := helmResetFunc
	contextSwitchMu.RUnlock()
	if hResetFn != nil {
		safeReset("Helm", hResetFn)
	}

	// 5. Metrics history
	safeReset("metrics history", ResetMetricsHistory)

	// 4. Dynamic cache
	safeReset("dynamic resource cache", ResetDynamicResourceCache)

	// 3. Resource discovery
	safeReset("resource discovery", ResetResourceDiscovery)

	// 2. Resource cache
	safeReset("resource cache", ResetResourceCache)

	// 1. Timeline
	contextSwitchMu.RLock()
	tlResetFn := timelineResetFunc
	contextSwitchMu.RUnlock()
	if tlResetFn != nil {
		safeReset("timeline", tlResetFn)
	}
}

// safeReset calls fn inside a deferred recover so a panic in one subsystem's
// teardown does not prevent the remaining subsystems from being reset.
func safeReset(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			log.Printf("PANIC in %s reset: %v\n%s", name, r, buf[:n])
		}
	}()
	log.Printf("Stopping %s...", name)
	fn()
}
