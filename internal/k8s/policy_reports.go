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
	// KyvernoStatusWarmup — discovery or PolicyReport index warmup is still running.
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

type policyReportSnapshot struct {
	index  *policyreports.Index
	status PolicyReportStatus
}

type connectionPolicyReports struct {
	once         sync.Once
	mu           sync.Mutex
	closed       bool
	published    atomic.Pointer[policyReportSnapshot]
	watched      []schema.GroupVersionResource
	timer        *time.Timer
	rebuilding   bool
	rebuildAgain bool
}

const rebuildDebounce = 500 * time.Millisecond

// GetPolicyReportIndex returns the installed connection's immutable index snapshot.
func GetPolicyReportIndex() *policyreports.Index { return Current().PolicyReportIndex() }

func (c *Connection) PolicyReportIndex() *policyreports.Index {
	return c.policyReportSnapshot().index
}

func (c *Connection) policyReportSnapshot() policyReportSnapshot {
	if c != nil {
		if state := c.dynamicCache.Load(); state != nil {
			if snap := state.reports.published.Load(); snap != nil {
				return *snap
			}
		}
	}
	return policyReportSnapshot{}
}

// Warmup runs once per build. A later Kyverno install or a failed probe is
// reconsidered on a rebuild (such as a context switch), not on re-admission.
func (c *Connection) warmupPolicyReports() {
	c.warmupPolicyReportsWithList((*DynamicResourceCache).listPolicyReports)
}

func (c *Connection) warmupPolicyReportsWithList(initialList func(*DynamicResourceCache, []schema.GroupVersionResource) []*unstructured.Unstructured) {
	s := &c.dynamicCacheState().reports
	s.once.Do(func() {
		discovery, cache := c.Discovery(), c.DynamicResourceCache()
		setStatus := func(status KyvernoStatus, reason string, observed int) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closed {
				s.published.Store(&policyReportSnapshot{status: PolicyReportStatus{Status: status, ReasonCode: reason, ObservedCount: observed}})
			}
		}

		if discovery == nil || discovery.ResourceDiscovery == nil {
			log.Printf("[policy-reports] No resource discovery available; skipping Kyverno detection")
			// Discovery unavailable is operationally indistinguishable from
			// "not installed" for the consumer surface, but the reason code
			// keeps the two tellable apart in diagnostics.
			setStatus(KyvernoStatusNotInstalled, ReasonNoDiscovery, 0)
			return
		}
		if !discovery.IsKyvernoInstalled() {
			log.Printf("[policy-reports] Kyverno not detected (neither kyverno.io nor policies.kyverno.io CRDs present); leaving PolicyReports deferred")
			setStatus(KyvernoStatusNotInstalled, ReasonNotInstalled, 0)
			return
		}

		if cache == nil || cache.DynamicResourceCache == nil {
			log.Printf("[policy-reports] Dynamic resource cache not initialized; cannot warm up PolicyReports")
			setStatus(KyvernoStatusDeferred, ReasonCacheDown, -1)
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
				setStatus(KyvernoStatusDeferred, selection.reason, selection.total)
			default:
				log.Printf("[policy-reports] Kyverno detected but no PolicyReport CRDs are registered for watch; nothing to warm up")
				// Kyverno is installed but the reporting CRDs aren't —
				// Kyverno without the reporting shim. Surface as
				// not_installed because there is no report data to expose.
				setStatus(KyvernoStatusNotInstalled, ReasonNoReportCRDs, 0)
			}
			return
		}

		log.Printf("[policy-reports] Kyverno detected; warming up %d report CRDs from %v (probed %d reports)", len(watched), selection.groups, selection.total)
		cache.WarmupParallel(watched, 30*time.Second)

		// Initialize the index from current cache contents so the first
		// lookup after warmup is hot — without this, callers would race
		// with the informer's initial event burst.
		idx := policyreports.NewIndex()
		idx.Replace(initialList(cache, watched))

		// Registration replays informer contents. Publish before a replay's
		// debounced rebuild can read the watched GVRs.
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		schedule := func() { s.scheduleRebuild(cache.listPolicyReports) }
		handlersRegistered := true
		handler := toolscache.ResourceEventHandlerFuncs{
			AddFunc:    func(_ any) { schedule() },
			UpdateFunc: func(_, _ any) { schedule() },
			DeleteFunc: func(_ any) { schedule() },
		}
		for _, gvr := range watched {
			if err := cache.AddGVRChangeHandler(gvr, handler); err != nil {
				handlersRegistered = false
				// Non-fatal: index is still populated from the initial
				// build, just won't update until the next context switch.
				log.Printf("[policy-reports] Failed to register event handler for %s: %v", gvr, err)
			}
		}

		s.watched = watched
		s.published.Store(&policyReportSnapshot{index: idx, status: PolicyReportStatus{
			Status:        KyvernoStatusReady,
			ObservedCount: selection.total,
			WatchedGroups: selection.groups,
			DeniedGroups:  selection.deniedGroups,
			LiveUpdates:   handlersRegistered,
		}})
		s.mu.Unlock()
		// Registration replays existing reports, so a deletion since the initial list can be missed.
		schedule()
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
func GetKyvernoStatus() KyvernoStatus { return Current().KyvernoStatus() }

func (c *Connection) KyvernoStatus() KyvernoStatus {
	snapshot := c.policyReportSnapshot()
	if snapshot.index != nil {
		return KyvernoStatusReady
	}
	status := snapshot.status
	if status.Status == "" {
		return KyvernoStatusWarmup
	}
	return status.Status
}

// GetPolicyReportStatus returns the structured status behind the PolicyReport
// index: the lifecycle phase plus why it is in that phase, how many report
// objects were observed at boot, and which API groups are actually being
// watched.
//
// Consumers rendering an empty policy view must use this rather than
// GetKyvernoStatus alone — "deferred" collapses RBAC denial and probe failure,
// and those need different words in front of an operator.
func GetPolicyReportStatus() PolicyReportStatus { return Current().PolicyReportStatus() }

func (c *Connection) PolicyReportStatus() PolicyReportStatus {
	status := c.policyReportSnapshot().status
	if status.Status == "" {
		// Dynamic cache init can also fail permanently. Only report warmup
		// while discovery is running or has actually found Kyverno.
		if c.ResourceCache().customResourcesPending(c.DynamicResourceCache()) {
			return PolicyReportStatus{Status: KyvernoStatusWarmup, ReasonCode: ReasonNoDiscovery, ObservedCount: -1}
		}
		d := c.Discovery()
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

func (cache *DynamicResourceCache) listPolicyReports(gvrs []schema.GroupVersionResource) []*unstructured.Unstructured {
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

// Kyverno re-evaluates many reports together. Coalesce each burst, allowing
// events after a list starts to schedule a fresh snapshot.
func (s *connectionPolicyReports) scheduleRebuild(list func([]schema.GroupVersionResource) []*unstructured.Unstructured) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(rebuildDebounce, func() {
		s.mu.Lock()
		s.timer = nil
		s.mu.Unlock()
		s.rebuild(list)
	})
}

func (s *connectionPolicyReports) rebuild(list func([]schema.GroupVersionResource) []*unstructured.Unstructured) {
	s.mu.Lock()
	snapshot := s.published.Load()
	if snapshot == nil || snapshot.index == nil {
		s.mu.Unlock()
		return
	}
	if s.rebuilding {
		s.rebuildAgain = true
		s.mu.Unlock()
		return
	}
	s.rebuilding = true
	watched := s.watched
	for !s.closed {
		s.rebuildAgain = false
		s.mu.Unlock()
		start := time.Now()
		next := policyreports.BuildIndex(list(watched))
		duration := time.Since(start)
		s.mu.Lock()
		if s.closed {
			break
		}
		s.published.Store(&policyReportSnapshot{index: next, status: snapshot.status})
		if !s.rebuildAgain {
			break
		}

		// A sustained report storm must not consume more than roughly half
		// of one core rebuilding the same full snapshot repeatedly.
		s.mu.Unlock()
		time.Sleep(max(rebuildDebounce, duration))
		s.mu.Lock()
	}
	s.rebuilding = false
	s.mu.Unlock()
}

func (s *connectionPolicyReports) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.published.Store(nil)
	s.watched = nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// PolicyReportFamily is one report family Radar indexes, with the resource name
// a finding is read from at each scope.
type PolicyReportFamily struct {
	Group         string
	Namespaced    string
	ClusterScoped string
}

// PolicyReportFamilies are the two families Radar indexes. They share no
// resource names, and a cluster may serve either or both — Kyverno 1.15+ writes
// to openreports.io — so authorizing against one name asks about a resource that
// may not exist here. The namespaced/cluster-scoped split matters for the same
// reason: a grant of `policyreports` cluster-wide does not imply
// `clusterpolicyreports`.
var PolicyReportFamilies = []PolicyReportFamily{
	{Group: "wgpolicyk8s.io", Namespaced: "policyreports", ClusterScoped: "clusterpolicyreports"},
	{Group: "openreports.io", Namespaced: "reports", ClusterScoped: "clusterreports"},
}

// ResourceAt picks the resource a finding about this namespace would have been
// read from. An empty namespace means a cluster-scoped subject, whose findings
// live in the cluster-scoped report kind.
func (f PolicyReportFamily) ResourceAt(namespace string) string {
	if namespace == "" {
		return f.ClusterScoped
	}
	return f.Namespaced
}

// ReadablePolicyReportFamilies reports which families an identity may read at
// the scope implied by namespace. Empty means none, which is a denial.
//
// Lives here for the same reason OmittedReason does: the REST handlers, the AI
// resource context and the MCP tools must all answer identically about who may
// see what, and the per-resource view already drifted from the per-policy one
// once. `canList` is the caller's own authorization primitive — Server.canRead
// over a request, canReadInNamespace over an MCP context.
func ReadablePolicyReportFamilies(namespace string, canList func(group, resource, namespace string) bool) map[string]bool {
	out := map[string]bool{}
	for _, f := range PolicyReportFamilies {
		if canList(f.Group, f.ResourceAt(namespace), namespace) {
			out[f.Group] = true
		}
	}
	return out
}

// PolicyOutcomeReadable reports whether a caller may see an outcome, based on
// the report families it was read from.
//
// An outcome seen in both families carries both, so a caller who may read
// either keeps it. No provenance at all is a denial rather than a pass: the
// index could not say where it came from, and guessing in the permissive
// direction hands over exactly what the family check exists to withhold.
func PolicyOutcomeReadable(o policyreports.PolicyOutcome, readable map[string]bool) bool {
	if len(o.Groups) == 0 {
		return false
	}
	for _, g := range o.Groups {
		if readable[g] {
			return true
		}
	}
	return false
}

// ReadableKyvernoFindings returns the Kyverno-attributable findings for a
// subject that this caller is authorized to see.
//
// Both filters at once, because either alone is wrong for an agent-facing
// rollup: the engine filter keeps another producer's findings out of a total
// labelled "kyverno", and the family filter keeps back what this identity may
// not read. The REST resource context and the MCP tools share it so the two
// cannot answer differently about the same resource.
func ReadableKyvernoFindings(idx *policyreports.Index, families map[string]bool, group, kind, namespace, name string) []policyreports.Finding {
	if idx == nil {
		return nil
	}
	outcomes := idx.SourcedFindingsForAnyEngine(group, kind, namespace, name, policyreports.EnginesAttributableToKyverno...)
	out := make([]policyreports.Finding, 0, len(outcomes))
	for _, o := range outcomes {
		if PolicyOutcomeReadable(o, families) {
			out = append(out, o.Finding)
		}
	}
	return out
}

// PolicyFindingsWithheld reports whether any finding for this subject was
// filtered out as unreadable by the caller.
//
// The distinction the plain finding list cannot make: no findings because the
// resource is clean, versus no findings because the ones it has live in a
// family this identity may not read. Both render as silence otherwise.
func PolicyFindingsWithheld(idx *policyreports.Index, families map[string]bool, group, kind, namespace, name string) bool {
	if idx == nil {
		return false
	}
	for _, o := range idx.SourcedFindingsForAnyEngine(group, kind, namespace, name, policyreports.EnginesAttributableToKyverno...) {
		if !PolicyOutcomeReadable(o, families) {
			return true
		}
	}
	return false
}
