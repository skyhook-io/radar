package k8s

import (
	"fmt"
	"log"
	"runtime"
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
