package k8s

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"
)

// InitAllSubsystems initializes all subsystems in the correct order.
// Used for both initial boot and after context switch.
//
// Returns an error if a critical subsystem (timeline or resource cache) fails
// to initialize. All other subsystems log warnings and continue in degraded mode.
//
// External subsystem callbacks (timeline, traffic, prometheus) must be
// registered via the Register*Funcs methods before calling this function.
//
// The progress callback receives human-readable status messages suitable for
// display in the UI (e.g. via SSE connection status updates).
func InitAllSubsystems(ctx context.Context, progress func(string)) error {
	return Current().InitSubsystems(ctx, progress)
}

func (conn *Connection) InitSubsystems(ctx context.Context, progress func(string)) error {
	return conn.initAllSubsystems(ctx, progress, 0, nil)
}

func (conn *Connection) initAllSubsystems(ctx context.Context, progress func(string), discoveryWait time.Duration, tracker *ContextSwitchTracker) error {
	subsystemStart := time.Now()

	// 1. Timeline — before caches so events during warmup are captured
	contextSwitchMu.RLock()
	tlReinitFn := timelineReinitFunc
	contextSwitchMu.RUnlock()
	if tlReinitFn != nil {
		progress("Initializing timeline...")
		t := time.Now()
		if err := tlReinitFn(); err != nil {
			return fmt.Errorf("timeline init failed: %w", err)
		}
		logTiming("   Timeline init: %v", time.Since(t))
	}

	// 2. Resource cache (typed informers) — critical, topology depends on this.
	// Start API resource discovery in parallel since it's independent.
	if tracker != nil && !tracker.setCanStop(ctx, true) {
		return context.Canceled
	}
	progress("Loading workloads...")
	t := time.Now()

	// Discovery can outlive a failed or cancelled switch if an exec plugin stalls.
	discoveryDone := make(chan struct{})
	if ctx.Err() == nil {
		go func() {
			defer close(discoveryDone)
			dt := time.Now()
			if err := conn.initResourceDiscovery(ctx); err != nil {
				log.Printf("Warning: resource discovery init failed: %v", err)
			}
			logTiming("   API resource discovery: %v (parallel)", time.Since(dt))
		}()
	} else {
		close(discoveryDone)
	}

	// Resource cache init (RBAC checks + informer sync) — the dominant cost
	if err := conn.initResourceCache(ctx); err != nil {
		return fmt.Errorf("resource cache init failed: %w", err)
	}
	logTiming("   Resource cache init: %v", time.Since(t))
	cache := conn.ResourceCache()
	if cache != nil {
		log.Printf("Resource cache initialized with %d resources", cache.GetResourceCount())
	}

	discoveryPending := false
	if ctx.Err() == nil {
		progress("Waiting for Kubernetes API discovery…")
		waitCtx := ctx
		if discoveryWait > 0 {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithTimeout(ctx, discoveryWait)
			defer cancel()
		}
		select {
		case <-discoveryDone:
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for Kubernetes API discovery: %w", ctx.Err())
			}
			log.Printf("Kubernetes API discovery still pending after %v; serving typed resources", discoveryWait)
			discoveryPending = true
		}
	}

	// Bail if context was canceled while we were waiting
	if ctx.Err() != nil {
		log.Printf("[ops] InitAllSubsystems bailing: context canceled (%v since start)", time.Since(subsystemStart))
		return ctx.Err()
	}

	if tracker != nil && !tracker.setCanStop(ctx, false) {
		return context.Canceled
	}

	if cache != nil {
		if discoveryPending {
			cache.apiDiscoveryPending.Store(true)
			go func() {
				defer cache.apiDiscoveryPending.Store(false)
				// A credential plugin can stall discovery for a long time; a
				// retired cache must not stay reachable from here meanwhile.
				select {
				case <-discoveryDone:
				case <-cache.Done():
					return
				}
				if conn.cacheIsCurrent() {
					startDynamicResourceCache(conn, cache)
				}
			}()
		} else {
			progress("Loading custom resources...")
			startDynamicResourceCache(conn, cache)
		}
	}

	// Bail if context was canceled during dynamic cache init
	if ctx.Err() != nil {
		log.Printf("[ops] InitAllSubsystems bailing after dynamic cache: context canceled (%v since start)", time.Since(subsystemStart))
		return ctx.Err()
	}

	// 4. Remaining subsystems — all independent, run in parallel.
	// These are fast individually but running them in parallel saves the serial sum.
	t = time.Now()
	var remainingWg sync.WaitGroup

	remainingWg.Add(1)
	go func() {
		defer remainingWg.Done()
		mt := time.Now()
		conn.initMetricsHistory()
		logTiming("   Metrics history init: %v (parallel)", time.Since(mt))
	}()

	remainingWg.Wait()
	logTiming("   Remaining subsystems (parallel): %v", time.Since(t))
	logTiming(" InitAllSubsystems total: %v", time.Since(subsystemStart))

	return nil
}

func startDynamicResourceCache(conn *Connection, cache *ResourceCache) {
	t := time.Now()
	if err := conn.initDynamicResourceCache(cache.ChangesRaw()); err != nil {
		log.Printf("Warning: dynamic resource cache init failed: %v", err)
	}

	if dc := conn.DynamicResourceCache(); dc != nil {
		lifetime := conn.dynamicCacheState().lifetime
		go func() {
			crdStart := time.Now()
			deferredDone := cache.DeferredDone()
			stillCurrent := func() bool {
				return !dc.IsStopped()
			}
			runCRDWarmupSequence(lifetime, nil, stillCurrent, dc.StartWatchReconciler)
			runCRDWarmupSequence(lifetime, deferredDone, stillCurrent,
				func() {
					wt := time.Now()
					conn.registerSupportedCRDFallbacks()
					warmupCommonCRDs(dc, conn.Discovery())
					logTiming("   CRD warmup: %v (background)", time.Since(wt))
				},
				func() {
					dt := time.Now()
					dc.DiscoverAllCRDs()
					logTiming("   CRD full discovery: %v (background)", time.Since(dt))
				},
				func() {
					// Conditional Kyverno warmup runs after discovery so the
					// IsKyvernoInstalled() signal sees every CRD that landed
					// during WarmupCommonCRDs or DiscoverAllCRDs (admin may
					// have installed Kyverno after Radar started).
					pt := time.Now()
					conn.warmupPolicyReports()
					logTiming("   Kyverno PolicyReport warmup: %v (background)", time.Since(pt))
				},
			)
			logTiming("   CRD total (warmup+discovery): %v (background)", time.Since(crdStart))
		}()
	}
	logTiming("   Dynamic cache factory init: %v", time.Since(t))
}

// runCRDWarmupSequence runs the background CRD warmup/discovery steps in
// order, after the deferred typed informers finish their initial sync. The CRD
// probes and LISTs share the apiserver connection and rate-limit budget with
// the deferred LISTs — ReplicaSets and Secrets, the heaviest kinds on large
// clusters — so letting them race starves the sync that resource views block
// on. deferredDone always closes, even when deferred sync fails or no kinds
// are deferred. Closing the dynamic cache abandons the wait and any remaining steps.
//
// stillCurrent, when non-nil, is checked after the gate and between steps so a
// sequence that outlives a context switch does not run against a replaced
// cache. Pass nil in tests that drive the helper directly.
func runCRDWarmupSequence(ctx context.Context, deferredDone <-chan struct{}, stillCurrent func() bool, steps ...func()) {
	if deferredDone != nil {
		gateStart := time.Now()
		select {
		case <-deferredDone:
			logTiming("   CRD warmup gate: deferred informers done after %v (background)", time.Since(gateStart))
		case <-ctx.Done():
			log.Printf("CRD warmup abandoned during deferred gate after %v: %v", time.Since(gateStart), ctx.Err())
			return
		}
	}
	if stillCurrent != nil && !stillCurrent() {
		log.Printf("CRD warmup abandoned: cluster caches replaced during deferred gate")
		return
	}
	for _, step := range steps {
		if ctx.Err() != nil {
			log.Printf("CRD warmup abandoned between steps: %v", ctx.Err())
			return
		}
		if stillCurrent != nil && !stillCurrent() {
			log.Printf("CRD warmup abandoned: cluster caches replaced between steps")
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 4096)
					n := runtime.Stack(buf, false)
					log.Printf("PANIC in CRD warmup step: %v\n%s", r, buf[:n])
				}
			}()
			step()
		}()
	}
}

// ResetAllSubsystems tears down all subsystems in reverse order of init.
// Init order: 1) timeline, 2) resource cache + API discovery, 3) dynamic cache,
// 4) metrics history.
// Safe to call on first boot when singletons are nil.
// Each reset is wrapped in a panic recover so a failure in one subsystem
// does not prevent remaining subsystems from being torn down.
func ResetAllSubsystems() {
	resetAllSubsystems(Current())
}

func resetAllSubsystems(conn *Connection) {
	// The point of no return: nothing can be admitted against the connection
	// whose resources are about to go.
	RetireServingConnection()
	safeReset("automatic port-forwards", conn.PortForwards().Close)
	safeReset("metrics history", conn.closeMetricsHistory)

	safeReset("dynamic resource cache", conn.closeDynamicResourceCache)

	// Readers use the installed connection's permissions to interpret the
	// typed cache. Detach that cache before installing the fresh connection.
	safeReset("resource cache", conn.closeResourceCache)
	safeReset("resource discovery", ResetResourceDiscovery)

	// Step 1: timeline
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
