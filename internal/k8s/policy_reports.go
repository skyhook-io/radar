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
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// KyvernoStatus reports why the PolicyReport index is (or isn't) populated.
// Callers that need lifecycle and coverage details use GetPolicyReportStatus;
// the bare enum only distinguishes absence, deferred warmup, active warmup,
// and a live index.
type KyvernoStatus string

const (
	// KyvernoStatusNotInstalled — Kyverno's Policy/ClusterPolicy CRDs are
	// not present in discovery. The PolicyReport source is unavailable;
	// nothing to do.
	KyvernoStatusNotInstalled KyvernoStatus = "not_installed"
	// KyvernoStatusDeferred — Kyverno is installed but warmup could not
	// track PolicyReports because the probe was denied or failed. Findings
	// are NOT being indexed.
	// Callers wanting them must fall back to a direct fetch.
	KyvernoStatusDeferred KyvernoStatus = "deferred"
	// KyvernoStatusWarmup — Kyverno is installed and warmup is presumed
	// in-flight (discovery has named the CRDs but the index hasn't been
	// published yet). Narrow window; expect to become "ready" shortly.
	KyvernoStatusWarmup KyvernoStatus = "warmup"
	// KyvernoStatusReady — PolicyReport index is populated and live. Consumers
	// must inspect its structured coverage fields before treating an empty
	// findings list as complete.
	KyvernoStatusReady KyvernoStatus = "ready"
)

// Reason codes explaining a non-ready status. The bare four-state enum
// collapses RBAC denial and probe failure into "deferred", which leaves an
// operator with an empty policy view and no way to tell "nothing is violating"
// from "Radar cannot see". The reason code makes that distinction sayable.
const (
	ReasonNoDiscovery  = "discovery_unavailable"
	ReasonNotInstalled = "kyverno_not_installed"
	ReasonNoReportCRDs = "no_report_crds"
	ReasonCacheDown    = "cache_unavailable"
	ReasonRBACDenied   = "rbac_denied"
	ReasonProbeFailed  = "probe_failed"
)

// PolicyReportStatus is the structured contract behind the PolicyReport
// index. Status alone cannot answer "why is this empty"; WatchedGroups names
// which API family the data actually came from, which is load-bearing on a
// cluster mid-migration from wgpolicyk8s.io to openreports.io.
type PolicyReportStatus struct {
	Status     KyvernoStatus `json:"status"`
	ReasonCode string        `json:"reasonCode,omitempty"`
	// ObservedCount is the number of report objects the pre-warmup probe
	// counted across the selected groups. -1 when the probe could not run.
	//
	// It is a boot-time snapshot for diagnostics and does not track growth.
	// Do not render it as a live count.
	ObservedCount int `json:"observedCount"`
	// WatchedGroups are the API groups actually being watched, e.g.
	// ["openreports.io"]. Empty when nothing is watched.
	WatchedGroups []string `json:"watchedGroups,omitempty"`
	// DeniedGroups are report families the probe could not read. Present
	// alongside a `ready` status: the index is real but incomplete, and a
	// consumer that renders "no violations" without mentioning this is
	// claiming coverage it does not have.
	DeniedGroups []string `json:"deniedGroups,omitempty"`
	// LiveUpdates is false when the index was built but change handlers could
	// not be registered, so it is frozen at its initial contents until the
	// next context switch. `ready` otherwise promises live data.
	LiveUpdates bool `json:"liveUpdates"`
}

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

// kyvernoWarmupReason carries the structured PolicyReportStatus alongside the
// bare decision enum. Same generation discipline as kyvernoWarmupDecision:
// writes go through setStatus, which checks the generation under the mutex so
// a pre-Reset warmup can't stamp the previous cluster's reason onto the new
// one.
var kyvernoWarmupReason atomic.Value // holds PolicyReportStatus

// Report families. Kept here (not in supportedCRDFallbacks) because warmup
// is conditional — we only register informers for these CRDs when Kyverno is
// present in discovery.
//
// Two API groups can carry the same data. wgpolicyk8s.io is the original
// working-group API; openreports.io is the successor Kyverno 1.15+ writes to
// when started with --openreportsEnabled. Within a group the versions are the
// same resource at different versions, so exactly one is watched: we prefer
// v1alpha2 (the dominant version Kyverno emits) and fall back to v1beta1.
//
// reports.kyverno.io is deliberately absent. EphemeralReport /
// ClusterEphemeralReport are Kyverno's intermediate objects — the reports
// controller merges them into PolicyReports and deletes them — and they churn
// hard enough on busy clusters to be a real memory cost for no added
// information.
type reportGroup struct {
	group string
	// Version candidates, most-preferred first. Only the first served
	// candidate in each slice is watched.
	namespaced []schema.GroupVersionResource
	cluster    []schema.GroupVersionResource
}

var reportGroups = []reportGroup{
	{
		group: "wgpolicyk8s.io",
		namespaced: []schema.GroupVersionResource{
			{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"},
			{Group: "wgpolicyk8s.io", Version: "v1beta1", Resource: "policyreports"},
		},
		cluster: []schema.GroupVersionResource{
			{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"},
			{Group: "wgpolicyk8s.io", Version: "v1beta1", Resource: "clusterpolicyreports"},
		},
	},
	{
		group: "openreports.io",
		namespaced: []schema.GroupVersionResource{
			{Group: "openreports.io", Version: "v1alpha1", Resource: "reports"},
		},
		cluster: []schema.GroupVersionResource{
			{Group: "openreports.io", Version: "v1alpha1", Resource: "clusterreports"},
		},
	},
}

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
	policyReportInit         = new(sync.Once)
	policyReportWatched      []schema.GroupVersionResource // guarded by policyReportMu
	policyReportMu           sync.Mutex                    // guards policyReportWatched + warmup/reset publication
	policyReportRebuildMu    sync.Mutex
	policyReportRebuilding   bool        // guarded by policyReportRebuildMu
	policyReportRebuildAgain bool        // guarded by policyReportRebuildMu
	policyReportPending      atomic.Bool // true when a rebuild is already queued

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
// report probe was denied or failed. Callers that need to distinguish
// these — e.g. to emit the correct `resourcecontext.OmittedReason`
// (not_installed vs rbac_denied vs cache_cold) — cannot do so today.
//
// TODO(T10): when the diagnostic `policySummary.kyverno` consumer
// arrives, introduce a sibling accessor that returns an enum status
// alongside the index so consumers can populate `omitted` faithfully.
// The reason isn't tracked yet because there's no consumer to need it;
// adding it speculatively is YAGNI surface.
//
// Returned indexes are safe for concurrent reads. Rebuilds publish a new
// index snapshot atomically, so a caller that already loaded an index sees a
// stable snapshot for the rest of its synchronous computation.
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
		setStatus := func(s KyvernoStatus, reason string, observed int, groups []string) {
			policyReportMu.Lock()
			defer policyReportMu.Unlock()
			if kyvernoWarmupGen.Load() != myGen {
				return
			}
			kyvernoWarmupDecision.Store(s)
			kyvernoWarmupReason.Store(PolicyReportStatus{
				Status:        s,
				ReasonCode:    reason,
				ObservedCount: observed,
				WatchedGroups: groups,
			})
		}
		publishReady := func(idx *policyreports.Index, watched []schema.GroupVersionResource, observed int, groups []string, denied []string, live bool) {
			policyReportMu.Lock()
			defer policyReportMu.Unlock()
			if kyvernoWarmupGen.Load() != myGen {
				return
			}
			policyReportIndex.Store(idx)
			policyReportWatched = watched
			kyvernoWarmupDecision.Store(KyvernoStatusReady)
			kyvernoWarmupReason.Store(PolicyReportStatus{
				Status:        KyvernoStatusReady,
				ObservedCount: observed,
				WatchedGroups: groups,
				DeniedGroups:  denied,
				LiveUpdates:   live,
			})
		}

		discovery := GetResourceDiscovery()
		if discovery == nil || discovery.ResourceDiscovery == nil {
			log.Printf("[policy-reports] No resource discovery available; skipping Kyverno detection")
			// Discovery unavailable is operationally indistinguishable from
			// "not installed" for the consumer surface, but the reason code
			// keeps the two tellable apart in diagnostics.
			setStatus(KyvernoStatusNotInstalled, ReasonNoDiscovery, 0, nil)
			return
		}
		if !discovery.IsKyvernoInstalled() {
			log.Printf("[policy-reports] Kyverno not detected (neither kyverno.io nor policies.kyverno.io CRDs present); leaving PolicyReports deferred")
			setStatus(KyvernoStatusNotInstalled, ReasonNotInstalled, 0, nil)
			return
		}

		cache := GetDynamicResourceCache()
		if cache == nil || cache.DynamicResourceCache == nil {
			log.Printf("[policy-reports] Dynamic resource cache not initialized; cannot warm up PolicyReports")
			setStatus(KyvernoStatusDeferred, ReasonCacheDown, -1, nil)
			return
		}

		// Choose which report API groups actually carry data. See
		// selectReportGVRs — first-served-wins picks an empty API on any
		// cluster mid-migration between wgpolicyk8s.io and openreports.io.
		selection := selectReportGVRs(discovery.SupportsWatchGVR, cache.ProbeCount)
		watched := selection.gvrs

		if len(watched) == 0 {
			switch selection.reason {
			case ReasonRBACDenied, ReasonProbeFailed:
				log.Printf("[policy-reports] Could not probe report CRDs (%s); deferring PolicyReport warmup", selection.reason)
				setStatus(KyvernoStatusDeferred, selection.reason, selection.total, nil)
			default:
				log.Printf("[policy-reports] Kyverno detected but no PolicyReport CRDs are registered for watch; nothing to warm up")
				// Kyverno is installed but the reporting CRDs aren't —
				// Kyverno without the reporting shim. Surface as
				// not_installed because there is no report data to expose.
				setStatus(KyvernoStatusNotInstalled, ReasonNoReportCRDs, 0, nil)
			}
			return
		}

		log.Printf("[policy-reports] Kyverno detected; warming up %d report CRDs from %v (probed %d reports)", len(watched), selection.groups, selection.total)
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
		// once per burst is cheaper than rebuilding once per event.
		handlersRegistered := true
		handler := toolscache.ResourceEventHandlerFuncs{
			AddFunc:    func(_ any) { scheduleRebuild() },
			UpdateFunc: func(_, _ any) { scheduleRebuild() },
			DeleteFunc: func(_ any) { scheduleRebuild() },
		}
		for _, gvr := range watched {
			if err := cache.AddGVRChangeHandler(gvr, handler); err != nil {
				handlersRegistered = false
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
		publishReady(idx, watched, selection.total, selection.groups, selection.deniedGroups, handlersRegistered)
		log.Printf("[policy-reports] Index initialized with %d subjects", idx.Size())
	})
}

// GetKyvernoStatus reports the lifecycle phase of the PolicyReport index.
// Distinguishes the four cases that a nil GetPolicyReportIndex() return
// collapses today:
//
//	not_installed — discovery decided Kyverno is absent (or the reporting
//	                CRDs are missing); findings will never appear.
//	deferred      — warmup ran but skipped indexing after an RBAC/probe
//	                error. Findings won't appear from this index; callers
//	                may fall back to on-demand.
//	warmup        — warmup hasn't completed its decision pass yet (typical
//	                for the first second or two after subsystem init); the
//	                index is uninitialized.
//	ready         — index is live. GetPolicyReportStatus carries the
//	                coverage fields needed to interpret an empty lookup.
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

// GetPolicyReportStatus returns the structured status behind the PolicyReport
// index: the lifecycle phase plus why it is in that phase, how many report
// objects were observed at boot, and which API groups are actually being
// watched.
//
// Consumers rendering an empty policy view must use this rather than
// GetKyvernoStatus alone — "deferred" collapses RBAC denial and probe failure,
// and those need different words in front of an operator.
func GetPolicyReportStatus() PolicyReportStatus {
	status, _ := kyvernoWarmupReason.Load().(PolicyReportStatus)
	// Ready is authoritative for the same reason GetKyvernoStatus treats it
	// so: the index publishes before the status flag inside warmup.
	if policyReportIndex.Load() != nil {
		// The index and the metadata live in separate atomics, and
		// publishReady stores the index first. A reader that sampled the
		// metadata before that store and the index after it would otherwise
		// report `ready` with an empty ObservedCount and no WatchedGroups —
		// a status that describes no real cluster. Re-read once now that the
		// index is known to be published.
		if status.Status == "" {
			status, _ = kyvernoWarmupReason.Load().(PolicyReportStatus)
		}
		status.Status = KyvernoStatusReady
		status.ReasonCode = ""
		return status
	}
	if status.Status == "" {
		// Warmup hasn't recorded a decision. "Undecided" must not be reported
		// as warmup unconditionally: warmup only runs when the dynamic cache
		// initialized (subsystems.go), and its init failure is non-fatal — so
		// on a cluster where it never runs this state is permanent, not
		// transient. Reporting warmup there makes OmittedReason emit
		// `policySummary.kyverno: cache_cold` on every resource of every
		// non-Kyverno cluster, forever, which is exactly the noise the silent
		// not-installed path exists to avoid.
		//
		// Ask discovery the same question warmup would: only claim the index
		// is cold when Kyverno is actually present. The scan is confined to
		// this pre-decision branch — once warmup records an outcome the
		// stored status returns above.
		d := GetResourceDiscovery()
		if d == nil || d.ResourceDiscovery == nil {
			// No discovery yet — we haven't looked, so we can't claim absence.
			return PolicyReportStatus{Status: KyvernoStatusNotInstalled, ReasonCode: ReasonNoDiscovery}
		}
		if d.IsKyvernoInstalled() {
			return PolicyReportStatus{Status: KyvernoStatusWarmup}
		}
		return PolicyReportStatus{Status: KyvernoStatusNotInstalled, ReasonCode: ReasonNotInstalled}
	}
	return status
}

// reportSelection is the outcome of choosing which report GVRs to watch.
type reportSelection struct {
	gvrs   []schema.GroupVersionResource
	groups []string
	// total is the probed object count across the selected GVRs, or -1 when
	// no probe could complete.
	total  int
	reason string
	// deniedGroups are families we could not fully read. Watching proceeds
	// without them, so the resulting index is real but incomplete.
	deniedGroups []string
}

// firstServed returns the first candidate the cluster actually serves for
// watch. Within an API group the candidates are the same resource at
// different versions, so watching more than one would double-count.
func firstServed(served func(schema.GroupVersionResource) bool, candidates []schema.GroupVersionResource) (schema.GroupVersionResource, bool) {
	for _, candidate := range candidates {
		if served(candidate) {
			return candidate, true
		}
	}
	return schema.GroupVersionResource{}, false
}

// selectReportGVRs decides which report API groups to watch.
//
// The naive approach — take the first served GVR and stop — is wrong on any
// cluster that has migrated between report APIs. Both families can legally be
// served while only one is written to: enabling Kyverno's --openreportsEnabled
// leaves the wgpolicyk8s.io CRDs registered but empty while every report lands
// in openreports.io. First-served-wins then selects an empty API and reports
// zero findings on a cluster that has plenty, which is indistinguishable from
// a clean cluster.
//
// So we probe each group for actual objects and watch the ones that have any,
// watching several when several carry data (a migration can leave stale
// reports behind in the old family; identical findings are deduplicated when
// the index is built). When NO group has data we watch every readable group
// rather than just the first: on a cluster that has enabled openreports but
// hasn't produced a violation yet, both families are legitimately empty, and
// locking onto the first would leave the group Kyverno is about to write to
// unwatched — reports would appear in the cluster and never in Radar, with the
// status still claiming `ready`.
// The two cluster interactions are injected so the selection rules can be
// tested without a live apiserver: `served` reports whether a GVR is
// watchable, `probeCount` returns its object count (-1 RBAC denied, -2
// transient error).
func selectReportGVRs(served func(schema.GroupVersionResource) bool, probeCount func(schema.GroupVersionResource) int) reportSelection {
	var (
		selection      reportSelection
		fallback       []schema.GroupVersionResource
		fallbackGroups []string
		servedAnything bool
		anyDenied      bool
		anyProbeFailed bool
		deniedGroups   []string
	)
	selection.total = 0

	for _, family := range reportGroups {
		var candidates []schema.GroupVersionResource
		if gvr, ok := firstServed(served, family.namespaced); ok {
			candidates = append(candidates, gvr)
		}
		if gvr, ok := firstServed(served, family.cluster); ok {
			candidates = append(candidates, gvr)
		}
		if len(candidates) == 0 {
			continue
		}
		servedAnything = true

		// Probe per GVR, not per family. A denial on one sibling must not
		// discard the other: namespace-scoped RBAC routinely grants `list
		// policyreports` within the user's namespaces while denying the
		// cluster-scoped `clusterpolicyreports`, and dropping the whole
		// family there loses every finding the user could legitimately see.
		groupTotal := 0
		groupDenied := false
		var readable []schema.GroupVersionResource
		for _, gvr := range candidates {
			count := probeCount(gvr)
			switch {
			case count == -1:
				anyDenied = true
				groupDenied = true
			case count < 0:
				anyProbeFailed = true
				groupDenied = true
			default:
				// Readable, including a legitimately empty sibling — keep it
				// watched so a report created later still appears live.
				readable = append(readable, gvr)
				groupTotal += count
			}
		}
		if groupDenied {
			// Some of this family is unreadable. We still watch what we can,
			// but the coverage is partial and consumers must be able to say so.
			deniedGroups = append(deniedGroups, family.group)
		}
		// Readable but currently empty: remember it as a fallback so an
		// all-empty cluster still watches every family it could write to.
		if len(readable) > 0 {
			fallback = append(fallback, readable...)
			if len(fallbackGroups) == 0 || fallbackGroups[len(fallbackGroups)-1] != family.group {
				fallbackGroups = append(fallbackGroups, family.group)
			}
		}
		if groupTotal == 0 {
			continue
		}

		selection.gvrs = append(selection.gvrs, readable...)
		selection.groups = append(selection.groups, family.group)
		selection.total += groupTotal
	}
	selection.deniedGroups = deniedGroups

	if len(selection.gvrs) > 0 {
		return selection
	}

	// Nothing had data. Distinguish "we were blocked from looking" from
	// "we looked and there is genuinely nothing".
	switch {
	case anyDenied:
		return reportSelection{total: -1, reason: ReasonRBACDenied}
	case anyProbeFailed:
		return reportSelection{total: -1, reason: ReasonProbeFailed}
	case !servedAnything:
		return reportSelection{total: 0, reason: ReasonNoReportCRDs}
	}
	return reportSelection{gvrs: fallback, groups: fallbackGroups, total: 0, deniedGroups: deniedGroups}
}

// OmittedReason maps the PolicyReport index state onto the agent-facing
// omitted-field enum, or reports false when findings are being served
// normally.
//
// Lives here, in one place, because both the REST and MCP resource-context
// adapters need the identical mapping and a second copy would drift.
//
// "Not installed" deliberately returns false: there is no Kyverno on the
// cluster, so an omitted note on every resource of every non-Kyverno cluster
// would be pure noise rather than honesty.
func (s PolicyReportStatus) OmittedReason() (resourcecontext.OmittedReason, bool) {
	switch s.Status {
	case KyvernoStatusReady:
		return "", false
	case KyvernoStatusNotInstalled:
		// Genuine absence is silent — a note on every resource of every
		// non-Kyverno cluster is noise. But "discovery was unavailable" is
		// NOT absence: we never established whether Kyverno exists, and
		// reporting silence there would be concluding a negative from a
		// failed lookup.
		if s.ReasonCode == ReasonNoDiscovery {
			return resourcecontext.OmittedCacheCold, true
		}
		return "", false
	case KyvernoStatusWarmup:
		return resourcecontext.OmittedCacheCold, true
	}
	// Deferred — the reason code is what distinguishes the cases the bare
	// enum collapses.
	switch s.ReasonCode {
	case ReasonRBACDenied:
		return resourcecontext.OmittedRBACDenied, true
	}
	return resourcecontext.OmittedCacheCold, true
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
		items, err := cache.ListWatchedReadOnly(gvr)
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
// set). When the timer fires, we re-list and publish a replacement index
// snapshot.
//
// The first rebuild starts after rebuildDebounce. Under sustained churn, a
// follow-up also waits at least as long as the preceding rebuild took so index
// maintenance cannot monopolize a core.
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
		// either lands in the current rebuild's snapshot or arms a
		// fresh timer. Concurrent timer expirations collapse into one
		// follow-up rebuild.
		policyReportPending.Store(false)
		rebuildPolicyReportIndex()
	})
}

func rebuildPolicyReportIndex() {
	rebuildPolicyReportIndexUsing(listPolicyReportsAll)
}

func rebuildPolicyReportIndexUsing(listReports func([]schema.GroupVersionResource) []*unstructured.Unstructured) {
	policyReportRebuildMu.Lock()
	if policyReportRebuilding {
		policyReportRebuildAgain = true
		policyReportRebuildMu.Unlock()
		return
	}
	policyReportRebuilding = true
	policyReportRebuildMu.Unlock()

	for {
		started := time.Now()
		rebuildPolicyReportIndexOnce(listReports)
		rebuildDuration := time.Since(started)

		policyReportRebuildMu.Lock()
		if !policyReportRebuildAgain {
			policyReportRebuilding = false
			policyReportRebuildMu.Unlock()
			return
		}
		policyReportRebuildMu.Unlock()

		// A sustained report storm must not consume more than roughly half
		// of one core rebuilding the same full snapshot repeatedly.
		time.Sleep(max(rebuildDebounce, rebuildDuration))
		policyReportRebuildMu.Lock()
		policyReportRebuildAgain = false
		policyReportRebuildMu.Unlock()
	}
}

func rebuildPolicyReportIndexOnce(listReports func([]schema.GroupVersionResource) []*unstructured.Unstructured) {
	policyReportMu.Lock()
	idx := policyReportIndex.Load()
	gvrs := append([]schema.GroupVersionResource(nil), policyReportWatched...)
	generation := kyvernoWarmupGen.Load()
	policyReportMu.Unlock()

	if idx == nil {
		return
	}
	reports := listReports(gvrs)
	next := policyreports.BuildIndex(reports)

	policyReportMu.Lock()
	defer policyReportMu.Unlock()
	if kyvernoWarmupGen.Load() != generation || policyReportIndex.Load() != idx {
		return
	}
	policyReportIndex.Store(next)
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
	// The rebuild coordinator deliberately survives reset. Clearing its active
	// flag while the old loop still owns it could start a second loop in parallel;
	// generation and pointer validation make the old iteration harmless.
	// Clear the warmup decision too — the new cluster gets a fresh
	// detection pass, and GetKyvernoStatus should report "warmup" until
	// the new pass completes (not whatever the previous cluster decided).
	kyvernoWarmupDecision.Store(KyvernoStatus(""))
	kyvernoWarmupReason.Store(PolicyReportStatus{})
	// Replace the pointer rather than zeroing the value — see the comment
	// on policyReportInit's declaration. Any Do() lambda still running on
	// the old *sync.Once finishes against that instance without
	// corrupting the new one.
	policyReportInit = new(sync.Once)
}
