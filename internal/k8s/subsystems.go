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
// External subsystem callbacks (timeline, helm, traffic, prometheus) must be
// registered via the Register*Funcs methods before calling this function.
//
// The progress callback receives human-readable status messages suitable for
// display in the UI (e.g. via SSE connection status updates).
func InitAllSubsystems(ctx context.Context, progress func(string)) error {
	return initAllSubsystems(ctx, progress, 0, nil)
}

func initAllSubsystems(ctx context.Context, progress func(string), discoveryWait time.Duration, tracker *ContextSwitchTracker) error {
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

	// Snapshot callback refs before parallel section
	contextSwitchMu.RLock()
	hReinitFn := helmReinitFunc
	trReinitFn := trafficReinitFunc
	promReinitFn := prometheusReinitFunc
	contextSwitchMu.RUnlock()

	// Discovery can outlive a failed or cancelled switch if an exec plugin stalls.
	discoveryDone := make(chan struct{})
	if ctx.Err() == nil {
		go func() {
			defer close(discoveryDone)
			dt := time.Now()
			if err := initResourceDiscovery(ctx); err != nil {
				log.Printf("Warning: resource discovery init failed: %v", err)
			}
			logTiming("   API resource discovery: %v (parallel)", time.Since(dt))
		}()
	} else {
		close(discoveryDone)
	}

	// Resource cache init (RBAC checks + informer sync) — the dominant cost
	if err := InitResourceCache(ctx); err != nil {
		return fmt.Errorf("resource cache init failed: %w", err)
	}
	logTiming("   Resource cache init: %v", time.Since(t))
	cache := GetResourceCache()
	if cache != nil {
		log.Printf("Resource cache initialized with %d resources", cache.GetResourceCount())
	}

	lifecycleCtx := OperationContext()
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
				// Teardown and replacement hold contextOpMu. Only install the
				// late cache while this typed cache still serves. An operation
				// can be cancelled without replacing it (a rescope whose
				// connectivity check failed), so use the current one.
				contextOpMu.RLock()
				defer contextOpMu.RUnlock()
				if GetResourceCache() == cache {
					startDynamicResourceCache(OperationContext(), cache)
				}
			}()
		} else {
			progress("Loading custom resources...")
			startDynamicResourceCache(lifecycleCtx, cache)
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

// The operation context outlives the switch's init context, which is cancelled
// on success while the deferred typed informers may still be syncing.
func startDynamicResourceCache(lifecycleCtx context.Context, cache *ResourceCache) {
	t := time.Now()
	if err := InitDynamicResourceCache(cache.ChangesRaw()); err != nil {
		log.Printf("Warning: dynamic resource cache init failed: %v", err)
	}

	if dc := GetDynamicResourceCache(); dc != nil {
		go func() {
			crdStart := time.Now()
			deferredDone := cache.DeferredDone()
			stillCurrent := func() bool {
				return GetDynamicResourceCache() == dc && GetResourceCache() == cache
			}
			runCRDWarmupSequence(lifecycleCtx, nil, stillCurrent, dc.StartWatchReconciler)
			runCRDWarmupSequence(lifecycleCtx, deferredDone, stillCurrent,
				func() {
					wt := time.Now()
					RegisterSupportedCRDFallbacks()
					WarmupCommonCRDs()
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
					WarmupKyvernoPolicyReports()
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
// are deferred, so the gate cannot wedge; ctx cancellation (context switch /
// CancelOngoingOperations) abandons the wait and any remaining steps.
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
// 4) remaining (metrics history, helm, traffic, prometheus).
// Safe to call on first boot when singletons are nil.
// Each reset is wrapped in a panic recover so a failure in one subsystem
// does not prevent remaining subsystems from being torn down.
func ResetAllSubsystems() {
	// The point of no return: nothing can be admitted against the connection
	// whose resources are about to go.
	RetireServingConnection()
	// Step 4 subsystems (reverse): prometheus, traffic, helm, metrics history
	contextSwitchMu.RLock()
	costResetFn := costResetFunc
	contextSwitchMu.RUnlock()
	if costResetFn != nil {
		safeReset("cost source", costResetFn)
	}

	contextSwitchMu.RLock()
	promResetFn := prometheusResetFunc
	contextSwitchMu.RUnlock()
	if promResetFn != nil {
		safeReset("prometheus", promResetFn)
	}

	contextSwitchMu.RLock()
	trResetFn := trafficResetFunc
	contextSwitchMu.RUnlock()
	if trResetFn != nil {
		safeReset("traffic", trResetFn)
	}

	contextSwitchMu.RLock()
	hResetFn := helmResetFunc
	contextSwitchMu.RUnlock()
	if hResetFn != nil {
		safeReset("Helm", hResetFn)
	}

	safeReset("metrics history", ResetMetricsHistory)

	// Step 3: dynamic cache. Reset the PolicyReport index first because
	// it holds references into the dynamic cache's informer indexers —
	// clearing the index before tearing down the informers avoids using
	// half-disposed informers on the next event-driven rebuild.
	safeReset("policy report index", ResetPolicyReportIndex)
	safeReset("dynamic resource cache", ResetDynamicResourceCache)

	// Step 2: resource discovery + resource cache
	safeReset("resource discovery", ResetResourceDiscovery)
	safeReset("resource cache", ResetResourceCache)

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
