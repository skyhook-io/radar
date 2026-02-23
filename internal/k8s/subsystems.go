package k8s

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"
)

// InitAllSubsystems initializes all subsystems in the correct order.
// Used for both initial boot and after context switch.
//
// Returns an error if a critical subsystem (resource cache) fails to
// initialize. All other subsystems log warnings and continue in degraded mode.
//
// External subsystem callbacks (timeline, helm, traffic, prometheus) must be
// registered via the Register*Funcs methods before calling this function.
//
// The progress callback receives human-readable status messages suitable for
// display in the UI (e.g. via SSE connection status updates).
func InitAllSubsystems(progress func(string)) error {
	subsystemStart := time.Now()

	// 1. Timeline — before caches so events during warmup are captured
	contextSwitchMu.RLock()
	tlReinitFn := timelineReinitFunc
	contextSwitchMu.RUnlock()
	if tlReinitFn != nil {
		progress("Initializing timeline...")
		t := time.Now()
		if err := tlReinitFn(); err != nil {
			log.Printf("Warning: timeline init failed: %v", err)
		}
		logTiming("   Timeline init: %v", time.Since(t))
	}

	// 2. Resource cache (typed informers) — critical, topology depends on this.
	// Start API resource discovery in parallel since it's independent.
	progress("Loading workloads...")
	t := time.Now()

	// Snapshot callback refs before parallel section
	contextSwitchMu.RLock()
	hReinitFn := helmReinitFunc
	trReinitFn := trafficReinitFunc
	promReinitFn := prometheusReinitFunc
	contextSwitchMu.RUnlock()

	// Start API discovery in parallel with the resource cache sync.
	// Discovery calls ServerGroupsAndResources() which is independent of informer sync.
	var discoveryWg sync.WaitGroup
	var discoveryErr error
	discoveryWg.Add(1)
	go func() {
		defer discoveryWg.Done()
		dt := time.Now()
		if err := InitResourceDiscovery(); err != nil {
			discoveryErr = err
		}
		logTiming("   API resource discovery: %v (parallel)", time.Since(dt))
	}()

	// Resource cache init (RBAC checks + informer sync) — the dominant cost
	if err := InitResourceCache(); err != nil {
		return fmt.Errorf("resource cache init failed: %w", err)
	}
	logTiming("   Resource cache init: %v", time.Since(t))
	if cache := GetResourceCache(); cache != nil {
		log.Printf("Resource cache initialized with %d resources", cache.GetResourceCount())
	}

	// Wait for discovery to complete before dynamic cache (which needs discovery data)
	discoveryWg.Wait()
	if discoveryErr != nil {
		log.Printf("Warning: resource discovery init failed: %v", discoveryErr)
	}

	// 3. Dynamic cache (factory init is synchronous; CRD warmup and discovery kick off async)
	progress("Loading custom resources...")
	t = time.Now()
	if cache := GetResourceCache(); cache != nil {
		changeCh := cache.ChangesRaw()
		if err := InitDynamicResourceCache(changeCh); err != nil {
			log.Printf("Warning: dynamic resource cache init failed: %v", err)
		}

		// CRD warmup and full discovery run in background.
		if dc := GetDynamicResourceCache(); dc != nil {
			go func() {
				crdStart := time.Now()
				func() {
					defer func() {
						if r := recover(); r != nil {
							buf := make([]byte, 4096)
							n := runtime.Stack(buf, false)
							log.Printf("PANIC in CRD warmup: %v\n%s", r, buf[:n])
						}
					}()
					wt := time.Now()
					WarmupCommonCRDs()
					logTiming("   CRD warmup: %v (background)", time.Since(wt))
				}()
				dt := time.Now()
				dc.DiscoverAllCRDs()
				logTiming("   CRD full discovery: %v (background)", time.Since(dt))
				logTiming("   CRD total (warmup+discovery): %v (background)", time.Since(crdStart))
			}()
		}
	}
	logTiming("   Dynamic cache factory init: %v", time.Since(t))

	// 4. Remaining subsystems — all independent, run in parallel.
	// These are fast individually but running them in parallel saves the serial sum.
	t = time.Now()
	var remainingWg sync.WaitGroup

	remainingWg.Add(1)
	go func() {
		defer remainingWg.Done()
		mt := time.Now()
		InitMetricsHistory()
		logTiming("   Metrics history init: %v (parallel)", time.Since(mt))
	}()

	if hReinitFn != nil {
		remainingWg.Add(1)
		go func() {
			defer remainingWg.Done()
			ht := time.Now()
			if err := hReinitFn(GetKubeconfigPath()); err != nil {
				log.Printf("Warning: Helm init failed: %v", err)
			}
			logTiming("   Helm init: %v (parallel)", time.Since(ht))
		}()
	}

	if trReinitFn != nil {
		remainingWg.Add(1)
		go func() {
			defer remainingWg.Done()
			tt := time.Now()
			if err := trReinitFn(); err != nil {
				log.Printf("Warning: traffic init failed: %v", err)
			}
			logTiming("   Traffic init: %v (parallel)", time.Since(tt))
		}()
	}

	if promReinitFn != nil {
		remainingWg.Add(1)
		go func() {
			defer remainingWg.Done()
			pt := time.Now()
			if err := promReinitFn(); err != nil {
				log.Printf("Warning: Prometheus init failed: %v", err)
			}
			logTiming("   Prometheus init: %v (parallel)", time.Since(pt))
		}()
	}

	remainingWg.Wait()
	logTiming("   Remaining subsystems (parallel): %v", time.Since(t))
	logTiming(" InitAllSubsystems total: %v", time.Since(subsystemStart))

	return nil
}

// ResetAllSubsystems tears down all subsystems in reverse order of init.
// Safe to call on first boot when singletons are nil.
// Each reset is wrapped in a panic recover so a failure in one subsystem
// does not prevent remaining subsystems from being torn down.
func ResetAllSubsystems() {
	// 8. Prometheus metrics client
	contextSwitchMu.RLock()
	promResetFn := prometheusResetFunc
	contextSwitchMu.RUnlock()
	if promResetFn != nil {
		safeReset("prometheus", promResetFn)
	}

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
