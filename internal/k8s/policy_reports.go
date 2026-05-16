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
	policyReportIndex atomic.Pointer[policyreports.Index]
	// policyReportInit is a *sync.Once (pointer), not a value, because
	// ResetPolicyReportIndex replaces it on context switch. Overwriting a
	// value-type sync.Once whose mutex is currently held by a concurrent
	// Do() crashes with "unlock of unlocked mutex". Every other sync.Once
	// in internal/k8s/ uses this same pointer pattern for the same reason.
	policyReportInit    = new(sync.Once)
	policyReportWatched []schema.GroupVersionResource // guarded by policyReportMu
	policyReportMu      sync.Mutex                    // guards policyReportWatched + serializes rebuild
	policyReportPending atomic.Bool                   // true when a rebuild is already queued

	// debounceDelay is how long after an informer event we wait before
	// rebuilding the index. PolicyReport updates often arrive in bursts
	// (Kyverno re-evaluates all matched resources on a single Policy
	// change), so coalescing them avoids redundant rebuilds.
	rebuildDebounce = 500 * time.Millisecond
)

// GetPolicyReportIndex returns the singleton PolicyReport index, or nil
// when no findings are available for any reason.
//
// "Nil" collapses several distinct conditions today — discovery not
// available, Kyverno not installed, dynamic cache not initialized, no
// PolicyReport CRDs registered, RBAC denied on the count probe, or the
// aggregate report count exceeded the warmup cap (deferred). Callers
// that need to distinguish these — e.g. to emit the correct
// `resourcecontext.OmittedReason` (not_installed vs rbac_denied vs
// budget_exceeded vs cache_cold) — cannot do so today.
//
// TODO(T10): when the diagnostic `policySummary.kyverno` consumer
// arrives, introduce a sibling accessor that returns an enum status
// alongside the index so consumers can populate `omitted` faithfully.
// The reason isn't tracked yet because there's no consumer to need it;
// adding it speculatively is YAGNI surface.
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

		// Probe cluster size before starting informers. The index caps what we
		// keep in memory (MaxIndexedReports), but informers themselves
		// list/watch/cache every PolicyReport object cluster-wide — on a
		// Kyverno-heavy cluster with tens of thousands of reports, that's
		// exactly the high-cardinality cost we're trying to avoid. If the
		// aggregate count across watched GVRs exceeds the cap, leave reports
		// in the deferred-fetch tier so callers can resolve them on demand.
		var total int
		for _, gvr := range watched {
			count := cache.ProbeCount(gvr)
			if count < 0 {
				// -1 RBAC denied, -2 transient probe error. Either way, we
				// can't bound the warmup cost; defer rather than gamble.
				log.Printf("[policy-reports] Probe for %s returned %d; deferring PolicyReport warmup", gvr, count)
				return
			}
			total += count
		}
		if total > kyvernoReportWarmupCap {
			log.Printf("[policy-reports] Cluster has %d PolicyReports across %d CRDs (cap=%d); leaving deferred to avoid full-cluster watch cost", total, len(watched), kyvernoReportWarmupCap)
			return
		}

		log.Printf("[policy-reports] Kyverno detected; warming up %d PolicyReport CRDs (probed %d reports, cap=%d)", len(watched), total, kyvernoReportWarmupCap)
		cache.WarmupParallel(watched, 30*time.Second)

		// Initialize the index from current cache contents so the first
		// lookup after warmup is hot — without this, callers would race
		// with the informer's initial event burst.
		idx := policyreports.NewIndex()
		idx.Replace(listPolicyReportsAll(watched))
		// Publish index + watched-GVR list together under the mutex. The
		// rebuild path reads policyReportWatched while holding the same
		// mutex, and ResetPolicyReportIndex (context switch) takes it to
		// clear both. Without the lock here, a concurrent Reset would race
		// with this assignment and could leave the new context with stale
		// GVRs from the prior cluster.
		policyReportMu.Lock()
		policyReportIndex.Store(idx)
		policyReportWatched = watched
		policyReportMu.Unlock()

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
	// Replace the pointer rather than zeroing the value — see the comment
	// on policyReportInit's declaration. Any Do() lambda still running on
	// the old *sync.Once finishes against that instance without
	// corrupting the new one.
	policyReportInit = new(sync.Once)
}
