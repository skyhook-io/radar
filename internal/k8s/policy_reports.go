package k8s

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/policyreports"
)

// PolicyReport GVRs. Kept here (not in supportedCRDFallbacks) because
// warmup is conditional — we only register informers for these CRDs when
// Kyverno's own Policy/ClusterPolicy CRDs are present in discovery.
//
// We try v1alpha2 first (the dominant version Kyverno emits) and fall back
// to v1beta1 if v1alpha2 is not registered. Most clusters in the wild
// (Kyverno 1.10+) ship v1alpha2.
var (
	policyReportGVRs = []schema.GroupVersionResource{
		{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"},
		{Group: "wgpolicyk8s.io", Version: "v1beta1", Resource: "policyreports"},
	}
	clusterPolicyReportGVRs = []schema.GroupVersionResource{
		{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"},
		{Group: "wgpolicyk8s.io", Version: "v1beta1", Resource: "clusterpolicyreports"},
	}
)

// kyvernoReportWarmupCap caps how many PolicyReport documents the index
// keeps in memory. The pkg/policyreports.MaxIndexedReports constant is the
// authoritative number; this re-export lives here for easy operator-side
// tuning at the integration boundary (so anyone grepping the codebase for
// "Kyverno" finds the tunable without having to know the package layout).
const kyvernoReportWarmupCap = policyreports.MaxIndexedReports

// policyReportIndex is the singleton index instance, populated when
// Kyverno is detected and kept up to date by PolicyReport informer
// events. Nil when Kyverno is absent — callers must nil-check.
var (
	policyReportIndex   atomic.Pointer[policyreports.Index]
	policyReportInit    sync.Once
	policyReportWatched []schema.GroupVersionResource // set during warmup, used by event-driven refresh
	policyReportMu      sync.Mutex                    // guards rebuild (debounce)
	policyReportPending atomic.Bool                   // true when a rebuild is already queued

	// debounceDelay is how long after an informer event we wait before
	// rebuilding the index. PolicyReport updates often arrive in bursts
	// (Kyverno re-evaluates all matched resources on a single Policy
	// change), so coalescing them avoids redundant rebuilds.
	rebuildDebounce = 500 * time.Millisecond
)

// GetPolicyReportIndex returns the singleton PolicyReport index, or nil
// when Kyverno is not installed on the cluster. Callers must treat the
// nil return as "no Kyverno findings available" and degrade gracefully.
//
// Returned indexes are safe for concurrent reads; the index swaps its
// internal state atomically during rebuilds.
func GetPolicyReportIndex() *policyreports.Index {
	return policyReportIndex.Load()
}

// WarmupKyvernoPolicyReports conditionally enables PolicyReport tracking.
// Called once after CRD discovery completes. Decision tree:
//
//  1. If Kyverno is NOT installed (no kyverno.io/Policy or ClusterPolicy
//     in discovery) → no-op, leave reports in the deferred-fetch tier.
//  2. If Kyverno is installed → start informers for the working-group
//     PolicyReport CRDs, build the index from current contents, and
//     register event handlers for live updates.
//
// Safe to call multiple times; only the first invocation does work.
// Subsequent calls are no-ops (sync.Once-guarded). Reset via
// ResetPolicyReportIndex on context switch.
//
// TODO(post-T5): mid-runtime Kyverno install is not handled. If Kyverno is
// installed AFTER initial CRD discovery completes (e.g. operator deployed
// post-boot), this function won't re-fire and PolicyReports stay in the
// deferred tier until the next context switch resets the once. To support
// this, hook OnCRDDiscoveryComplete in pkg/k8score/dynamic_cache.go (around
// the rediscovery path) to re-evaluate IsKyvernoInstalled and warm up
// lazily. Documented limitation, not blocking — context switches are the
// dominant lifecycle event in practice.
func WarmupKyvernoPolicyReports() {
	policyReportInit.Do(func() {
		discovery := GetResourceDiscovery()
		if discovery == nil || discovery.ResourceDiscovery == nil {
			log.Printf("[policy-reports] No resource discovery available; skipping Kyverno detection")
			return
		}
		if !discovery.IsKyvernoInstalled() {
			log.Printf("[policy-reports] Kyverno not detected (no kyverno.io/Policy or ClusterPolicy); leaving PolicyReports deferred")
			return
		}

		cache := GetDynamicResourceCache()
		if cache == nil || cache.DynamicResourceCache == nil {
			log.Printf("[policy-reports] Dynamic resource cache not initialized; cannot warm up PolicyReports")
			return
		}

		// Pick the actual GVRs registered on this cluster — there are two
		// candidate versions per kind. We prefer v1alpha2 (most common)
		// but accept v1beta1 if that's what's installed.
		watched := make([]schema.GroupVersionResource, 0, 2)
		for _, candidate := range policyReportGVRs {
			if discovery.SupportsWatchGVR(candidate) {
				watched = append(watched, candidate)
				break
			}
		}
		for _, candidate := range clusterPolicyReportGVRs {
			if discovery.SupportsWatchGVR(candidate) {
				watched = append(watched, candidate)
				break
			}
		}

		if len(watched) == 0 {
			log.Printf("[policy-reports] Kyverno detected but no wgpolicyk8s.io PolicyReport CRDs are registered for watch; nothing to warm up")
			return
		}

		log.Printf("[policy-reports] Kyverno detected; warming up %d PolicyReport CRDs (cap=%d reports)", len(watched), kyvernoReportWarmupCap)
		cache.WarmupParallel(watched, 30*time.Second)

		// Initialize the index from current cache contents so the first
		// lookup after warmup is hot — without this, callers would race
		// with the informer's initial event burst.
		idx := policyreports.NewIndex()
		idx.Replace(listPolicyReportsAll(watched))
		policyReportIndex.Store(idx)
		policyReportWatched = watched

		// Register event handlers for live updates. Each handler does a
		// debounced rebuild — PolicyReport events arrive in bursts when
		// Kyverno re-evaluates a policy, and rebuilding once per burst
		// is cheaper than per-event incremental updates given how small
		// the index is (≤500 reports).
		handler := toolscache.ResourceEventHandlerFuncs{
			AddFunc:    func(_ any) { scheduleRebuild() },
			UpdateFunc: func(_, _ any) { scheduleRebuild() },
			DeleteFunc: func(_ any) { scheduleRebuild() },
		}
		for _, gvr := range watched {
			if err := cache.AddGVRChangeHandler(gvr, handler); err != nil {
				// Non-fatal: index is still populated from the initial
				// build, just won't update until the next context switch.
				log.Printf("[policy-reports] Failed to register event handler for %s: %v", gvr, err)
			}
		}

		log.Printf("[policy-reports] Index initialized with %d subjects", idx.Size())
	})
}

// listPolicyReportsAll concatenates reports from every watched GVR.
// Used both for the initial index build and for each debounced rebuild.
func listPolicyReportsAll(gvrs []schema.GroupVersionResource) []*unstructured.Unstructured {
	cache := GetDynamicResourceCache()
	if cache == nil {
		return nil
	}
	var all []*unstructured.Unstructured
	for _, gvr := range gvrs {
		items, err := cache.List(gvr, "")
		if err != nil {
			log.Printf("[policy-reports] list %s: %v", gvr, err)
			continue
		}
		all = append(all, items...)
	}
	return all
}

// scheduleRebuild coalesces back-to-back informer events into a single
// rebuild. The first event in a burst arms a timer; subsequent events
// during the debounce window do nothing (the pending flag is already
// set). When the timer fires, we re-list and Replace the index contents.
//
// The debounce window (rebuildDebounce) is well under any realistic
// staleness budget: agents reading the index see at most ~500ms-stale
// data, which is well below Kyverno's own reconcile cadence.
func scheduleRebuild() {
	if !policyReportPending.CompareAndSwap(false, true) {
		return // rebuild already scheduled
	}
	time.AfterFunc(rebuildDebounce, func() {
		// Clear the pending flag BEFORE the rebuild, not after. The
		// hazard avoided: if we cleared after, an event arriving between
		// rebuild's List() snapshot and the final Store(false) would
		// neither be visible to the current rebuild nor able to arm a
		// fresh timer (CAS would fail while pending=true), and would
		// only be picked up when *some later* event happened to fire.
		// Clearing first means any event during the rebuild always
		// either lands in the current rebuild's snapshot OR arms a
		// fresh timer. The cost is one extra rebuild per event that
		// arrives during the rebuild window — cheaper than chasing
		// silent staleness.
		policyReportPending.Store(false)
		rebuildPolicyReportIndex()
	})
}

// rebuildPolicyReportIndex re-lists all watched PolicyReport GVRs from
// the dynamic cache and atomically swaps the index contents. Serialized
// by policyReportMu so concurrent triggers don't waste CPU rebuilding
// the same data.
func rebuildPolicyReportIndex() {
	policyReportMu.Lock()
	defer policyReportMu.Unlock()

	idx := policyReportIndex.Load()
	if idx == nil {
		return // index was reset (context switch) — drop event
	}
	idx.Replace(listPolicyReportsAll(policyReportWatched))
}

// ResetPolicyReportIndex clears the index and re-arms warmup-once. Called
// during context switch (alongside ResetDynamicResourceCache) so the new
// cluster gets a fresh detection pass. Safe to call when nothing was
// warmed up.
func ResetPolicyReportIndex() {
	policyReportMu.Lock()
	defer policyReportMu.Unlock()

	policyReportIndex.Store(nil)
	policyReportWatched = nil
	policyReportPending.Store(false)
	policyReportInit = sync.Once{}
}
