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

// KyvernoStatus reports why the PolicyReport index is (or isn't) populated.
// Callers that need to distinguish "Kyverno not installed" from "warmup
// deferred (cluster too large)" from "warmup in flight" from "ready but
// empty" use GetKyvernoStatus to surface the reason to the operator/agent
// — otherwise an empty PolicyReport response is indistinguishable from a
// transient one.
type KyvernoStatus string

const (
	// KyvernoStatusNotInstalled — Kyverno's Policy/ClusterPolicy CRDs are
	// not present in discovery. The PolicyReport source is unavailable;
	// nothing to do.
	KyvernoStatusNotInstalled KyvernoStatus = "not_installed"
	// KyvernoStatusDeferred — Kyverno is installed but warmup decided not
	// to track PolicyReports (cluster aggregate exceeds the warmup cap, or
	// the probe was denied/errored). Findings are NOT being indexed.
	// Callers wanting them must fall back to a direct fetch.
	KyvernoStatusDeferred KyvernoStatus = "deferred"
	// KyvernoStatusWarmup — Kyverno is installed and warmup is presumed
	// in-flight (discovery has named the CRDs but the index hasn't been
	// published yet). Narrow window; expect to become "ready" shortly.
	KyvernoStatusWarmup KyvernoStatus = "warmup"
	// KyvernoStatusReady — PolicyReport index is populated and live. An
	// empty findings list with this status means "no policy violations",
	// not "data unavailable".
	KyvernoStatusReady KyvernoStatus = "ready"
)

// kyvernoWarmupDecision is set by WarmupKyvernoPolicyReports to record
// the outcome of its single decision pass: empty (warmup hasn't run yet)
// vs not-installed vs deferred vs ready. Read by GetKyvernoStatus so
// callers can disambiguate a nil GetPolicyReportIndex() return. Writes
// from WarmupKyvernoPolicyReports go through setDecisionIfCurrent so a
// pre-Reset warmup goroutine that's still running can't stamp its
// outcome onto the new cluster's atomic.
var kyvernoWarmupDecision atomic.Value // holds KyvernoStatus, "" before first decision

// kyvernoWarmupGen serializes "is this warmup still the current one"
// against context-switch Resets. Each Warmup invocation captures the
// generation at the start of its lambda; Reset bumps the counter before
// clearing state. Stale warmup completions (e.g. an in-flight Old-cluster
// warmup that hasn't yet stored ready when Reset fires for the new
// cluster) see a mismatch and skip their writes, so the new cluster never
// inherits the old cluster's index or decision.
var kyvernoWarmupGen atomic.Int64

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
	// Snapshot BOTH the *sync.Once and the warmup generation under the
	// mutex BEFORE entering Do(). If we read policyReportInit unsynchronized
	// and captured myGen inside the lambda, a Reset interleaving between the
	// caller's read of policyReportInit and the lambda's Load of
	// kyvernoWarmupGen would let the lambda capture the POST-Reset gen,
	// and its subsequent writes (which check gen under the mutex) would
	// match — stamping the previous cluster's outcome onto the new cluster's
	// atomics. Capturing both under the same mutex Reset holds closes that
	// window: either we see the pre-Reset (once, gen) pair and Reset's
	// bump invalidates our writes, or we see the post-Reset (once', gen')
	// pair and run cleanly under the new sync.Once.
	policyReportMu.Lock()
	once := policyReportInit
	myGen := kyvernoWarmupGen.Load()
	policyReportMu.Unlock()
	once.Do(func() {
		setDecision := func(s KyvernoStatus) {
			policyReportMu.Lock()
			defer policyReportMu.Unlock()
			if kyvernoWarmupGen.Load() != myGen {
				return
			}
			kyvernoWarmupDecision.Store(s)
		}
		publishReady := func(idx *policyreports.Index, watched []schema.GroupVersionResource) {
			policyReportMu.Lock()
			defer policyReportMu.Unlock()
			if kyvernoWarmupGen.Load() != myGen {
				return
			}
			policyReportIndex.Store(idx)
			policyReportWatched = watched
			kyvernoWarmupDecision.Store(KyvernoStatusReady)
		}

		discovery := GetResourceDiscovery()
		if discovery == nil || discovery.ResourceDiscovery == nil {
			log.Printf("[policy-reports] No resource discovery available; skipping Kyverno detection")
			// Discovery unavailable is operationally indistinguishable from
			// "not installed" — the consumer surface only needs to know
			// findings won't appear.
			setDecision(KyvernoStatusNotInstalled)
			return
		}
		if !discovery.IsKyvernoInstalled() {
			log.Printf("[policy-reports] Kyverno not detected (no kyverno.io/Policy or ClusterPolicy); leaving PolicyReports deferred")
			setDecision(KyvernoStatusNotInstalled)
			return
		}

		cache := GetDynamicResourceCache()
		if cache == nil || cache.DynamicResourceCache == nil {
			log.Printf("[policy-reports] Dynamic resource cache not initialized; cannot warm up PolicyReports")
			setDecision(KyvernoStatusDeferred)
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
			// Kyverno is installed but the reporting CRDs aren't — operator
			// has Kyverno without the policy-reporter shim. Surface as
			// not_installed because there is no PolicyReport data to expose.
			setDecision(KyvernoStatusNotInstalled)
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
				setDecision(KyvernoStatusDeferred)
				return
			}
			total += count
		}
		if total > kyvernoReportWarmupCap {
			log.Printf("[policy-reports] Cluster has %d PolicyReports across %d CRDs (cap=%d); leaving deferred to avoid full-cluster watch cost", total, len(watched), kyvernoReportWarmupCap)
			setDecision(KyvernoStatusDeferred)
			return
		}

		log.Printf("[policy-reports] Kyverno detected; warming up %d PolicyReport CRDs (probed %d reports, cap=%d)", len(watched), total, kyvernoReportWarmupCap)
		cache.WarmupParallel(watched, 30*time.Second)

		// Initialize the index from current cache contents so the first
		// lookup after warmup is hot — without this, callers would race
		// with the informer's initial event burst.
		idx := policyreports.NewIndex()
		idx.Replace(listPolicyReportsAll(watched))

		// Register event handlers for live updates BEFORE publishing the
		// index so a debounced rebuild that fires during publishReady's
		// critical section sees the new policyReportWatched value. Each
		// handler does a debounced rebuild — PolicyReport events arrive
		// in bursts when Kyverno re-evaluates a policy, and rebuilding
		// once per burst is cheaper than per-event incremental updates
		// given how small the index is (≤500 reports).
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

		// Publish index + watched-GVR list + ready decision together
		// under the mutex. The rebuild path reads policyReportWatched
		// while holding the same mutex, and ResetPolicyReportIndex
		// (context switch) takes it to clear both. publishReady's
		// generation check ensures a Reset that fires before this point
		// causes the writes to be skipped, so the new cluster never
		// inherits the old cluster's index or decision.
		publishReady(idx, watched)
		log.Printf("[policy-reports] Index initialized with %d subjects", idx.Size())
	})
}

// GetKyvernoStatus reports the lifecycle phase of the PolicyReport index.
// Distinguishes the four cases that a nil GetPolicyReportIndex() return
// collapses today:
//
//	not_installed — discovery decided Kyverno is absent (or the reporting
//	                CRDs are missing); findings will never appear.
//	deferred      — warmup ran but skipped indexing (cluster exceeded the
//	                cap, or RBAC/probe error). Findings won't appear from
//	                this index; callers may fall back to on-demand.
//	warmup        — warmup hasn't completed its decision pass yet (typical
//	                for the first second or two after subsystem init); the
//	                index is uninitialized.
//	ready         — index is live. An empty findings list under this
//	                status means "no violations", not "data unavailable".
//
// Cheap: a single atomic.Value Load. Safe to call from request paths.
func GetKyvernoStatus() KyvernoStatus {
	// Ready is authoritative: even if a stale "warmup" decision is in the
	// atomic (legal during a window in WarmupKyvernoPolicyReports where
	// the index publishes before the decision flag), the presence of an
	// index means we are ready.
	if policyReportIndex.Load() != nil {
		return KyvernoStatusReady
	}
	v, _ := kyvernoWarmupDecision.Load().(KyvernoStatus)
	if v == "" {
		return KyvernoStatusWarmup
	}
	return v
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
		items, err := cache.ListWatched(gvr)
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

	// Bump the generation inside the critical section so any in-flight
	// warmup goroutine's setDecision/publishReady call (which checks gen
	// under the same mutex) skips its writes instead of stamping the old
	// cluster's outcome onto the new cluster's atomic.
	kyvernoWarmupGen.Add(1)
	policyReportIndex.Store(nil)
	policyReportWatched = nil
	policyReportPending.Store(false)
	// Clear the warmup decision too — the new cluster gets a fresh
	// detection pass, and GetKyvernoStatus should report "warmup" until
	// the new pass completes (not whatever the previous cluster decided).
	kyvernoWarmupDecision.Store(KyvernoStatus(""))
	// Replace the pointer rather than zeroing the value — see the comment
	// on policyReportInit's declaration. Any Do() lambda still running on
	// the old *sync.Once finishes against that instance without
	// corrupting the new one.
	policyReportInit = new(sync.Once)
}
