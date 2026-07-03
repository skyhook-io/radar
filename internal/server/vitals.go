package server

import (
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"golang.org/x/sync/singleflight"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// GET /api/vitals — a cluster's vital signs: a compact scale + capacity
// snapshot (node/pod phase counts, CPU/memory capacity vs requests vs live
// usage) meant to be polled cheaply, distinct from /api/dashboard's full
// home payload. Purpose-built for
// Radar Cloud's fleet fan-out (the hub previously projected this corner out
// of the full /api/dashboard payload and discarded the rest); equally usable
// by any consumer that wants counts + capacity without the home-page
// kitchen sink.
//
// Completeness is TYPED, never stringly: the sentinel fields carry k8score
// group-qualified Kind values ("Pod", "Node") and an explicit derived `complete`,
// so consumers don't re-derive authoritativeness from zeros.

type VitalsResponse struct {
	Nodes  NodeCount      `json:"nodes"`
	Pods   VitalsPodCount `json:"pods"`
	CPU    *MetricSummary `json:"cpu,omitempty"`
	Memory *MetricSummary `json:"memory,omitempty"`
	// True only when live usage came from metrics-server; requests/capacity
	// are informer-derived and present regardless (same semantics as the
	// dashboard's flag).
	MetricsServerAvailable bool               `json:"metricsServerAvailable"`
	Completeness           VitalsCompleteness `json:"completeness"`
}

// VitalsPodCount is ResourceCount without omitempty — fleet consumers want
// stable JSON shape, not fields that vanish at zero.
type VitalsPodCount struct {
	Total     int `json:"total"`
	Running   int `json:"running"`
	Pending   int `json:"pending"`
	Failed    int `json:"failed"`
	Succeeded int `json:"succeeded"`
}

// VitalsCompleteness is a PROVISIONAL contract. `complete` and
// `accessRestricted` are stable primitives (a future shared completeness
// envelope keeps both meanings unchanged); `pending`/`restricted` are
// coarse per-kind hints that the shared envelope will subsume into a
// {kind, reason} model (rbac_denied vs unavailable vs syncing). No consumer
// reads the detail lists today — only `complete` — so that later change is
// non-breaking. Kind values use group-qualified Kind casing ("Pod") to
// match /api/resource-counts, the closest existing shape.
type VitalsCompleteness struct {
	// Caller's identity has no namespace access at all — counts are
	// meaningless, not zero.
	AccessRestricted bool `json:"accessRestricted"`
	// Vitals-relevant kinds ("Pod", "Node") whose critical informers are
	// still syncing. Other kinds' warm-up doesn't gate vitals.
	Pending []string `json:"pending,omitempty"`
	// Vitals-relevant kinds the current identity (or the SA cache) cannot
	// list. Includes "Node" when node RBAC is denied — the dashboard has no
	// such sentinel and consumers had to infer it from zero totals.
	Restricted []string `json:"restricted,omitempty"`
	// Derived: !accessRestricted && no pending && no restricted. Metrics
	// availability is deliberately NOT part of completeness — it's a
	// capability, reported separately.
	Complete bool `json:"complete"`
}

// The live metrics-server probe (fetchNodeUsage) can block up to 8s. This
// 15s memo absorbs BURSTS — retries, concurrent consumers, page refreshes,
// and repeated 8s waits when metrics-server is slow/absent — rather than
// materially cutting steady-state load at the 30–60s fleet poll cadence.
// Keyed by contextName+username (the probe impersonates the caller, so
// results must never cross identities or clusters) and cleared on context
// switch (finalizePostContextSwitch). Only the live-usage probe is memoized;
// capacity/requests are recomputed fresh every call.
type vitalsMetricsMemo struct {
	mu      sync.Mutex
	entries map[string]vitalsMetricsEntry
	// Collapses concurrent same-key misses into a single probe — without it
	// a burst of /api/vitals requests (the case this memo exists to absorb)
	// would each run the 8s metrics-server probe and last-writer-wins could
	// leave a stale metricsServerAvailable for the TTL.
	group singleflight.Group
}

type vitalsMetricsEntry struct {
	cpuMillis int64
	memBytes  int64
	ok        bool
	at        time.Time
}

const vitalsMetricsTTL = 15 * time.Second

func (m *vitalsMetricsMemo) get(key string) (vitalsMetricsEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || time.Since(e.at) > vitalsMetricsTTL {
		return vitalsMetricsEntry{}, false
	}
	return e, true
}

func (m *vitalsMetricsMemo) put(key string, e vitalsMetricsEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]vitalsMetricsEntry{}
	}
	// Bound the map: keys are per-user; a runaway cardinality here would
	// indicate a bug, not a workload. Reset rather than grow.
	if len(m.entries) > 256 {
		m.entries = map[string]vitalsMetricsEntry{}
	}
	e.at = time.Now()
	m.entries[key] = e
}

// loadOrFetch returns the memoized usage for key, running fetch at most once
// per key across concurrent callers. Cached entries within the TTL skip the
// probe entirely.
func (m *vitalsMetricsMemo) loadOrFetch(key string, fetch func() vitalsMetricsEntry) vitalsMetricsEntry {
	if e, ok := m.get(key); ok {
		return e
	}
	v, _, _ := m.group.Do(key, func() (any, error) {
		// Re-check under the flight: a prior leader may have just filled it.
		if e, ok := m.get(key); ok {
			return e, nil
		}
		e := fetch()
		m.put(key, e)
		return e, nil
	})
	return v.(vitalsMetricsEntry)
}

func (m *vitalsMetricsMemo) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
}

func (s *Server) handleVitals(w http.ResponseWriter, r *http.Request) {
	// Connection guards FIRST, matching /api/dashboard: while disconnected,
	// per-user namespace discovery fails closed, and answering that state
	// with accessRestricted would disguise an infrastructure problem as an
	// RBAC denial. 503 is the honest signal until the cluster is reachable.
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster cache not ready")
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, VitalsResponse{
			Completeness: VitalsCompleteness{AccessRestricted: true},
		})
		return
	}

	resp := VitalsResponse{}
	var restricted []string

	// Pods — namespace VISIBILITY (parseNamespacesForUser) is not pod-READ
	// authority: discovery admits a namespace on list-pods OR
	// list-deployments, so a deployments-only user must not learn pod
	// counts, phases, or request totals from the SA cache. Authorize the
	// pods read explicitly per scope.
	podNamespaces := namespaces
	podsReadable := true
	podsPartial := false
	if namespaces == nil {
		podsReadable = s.canRead(r, "", "pods", "", "list")
	} else {
		podNamespaces = s.filterNamespacesByCanRead(r, "", "pods", "list", namespaces)
		podsReadable = len(podNamespaces) > 0
		// Some visible namespaces dropped at the pod-read gate: whatever we
		// count below is a subset — flag it so consumers don't treat the
		// numbers as the caller's whole world.
		podsPartial = len(podNamespaces) < len(namespaces)
	}
	// The SA cache itself can be namespace-scoped (startup RBAC fallback);
	// a cluster-wide caller reading a scoped cache is partial too.
	if perm := k8s.GetCachedPermissionResult(); perm != nil {
		if scope, ok := perm.Scopes[k8score.Pods]; ok && scope.Enabled && scope.Namespace != "" {
			podsPartial = true
		}
	}
	var scopedPods []*corev1.Pod
	if podsReadable {
		scopedPods = listPodsScoped(cache.Pods(), podNamespaces)
	}
	if podsReadable && cache.Pods() != nil {
		pods := scopedPods
		resp.Pods.Total = len(pods)
		for _, pod := range pods {
			switch pod.Status.Phase {
			case corev1.PodRunning:
				resp.Pods.Running++
			case corev1.PodPending:
				resp.Pods.Pending++
			case corev1.PodFailed:
				resp.Pods.Failed++
			case corev1.PodSucceeded:
				resp.Pods.Succeeded++
			}
		}
	} else {
		restricted = append(restricted, "Pod")
	}
	if podsReadable && podsPartial {
		restricted = append(restricted, "Pod")
	}

	// Nodes — cluster-scoped; gate on the caller's own RBAC like the
	// dashboard does, then on the SA cache.
	canReadNodes := s.canRead(r, "", "nodes", "", "list")
	if canReadNodes && cache.Nodes() != nil {
		nodes, _ := cache.Nodes().List(labels.Everything())
		resp.Nodes.Total = len(nodes)
		for _, n := range nodes {
			h := health.Node(n)
			if h.Ready {
				if h.Unschedulable {
					resp.Nodes.Cordoned++
				} else {
					resp.Nodes.Ready++
				}
			} else {
				resp.Nodes.NotReady++
			}
		}
	} else {
		restricted = append(restricted, "Node")
	}

	// Pending critical informers, filtered to the two vitals-relevant kinds.
	// PendingPromotedKinds already emits group-qualified Kind names ("Pod",
	// "Node") — the same vocabulary restricted uses above.
	for _, k := range cache.PendingPromotedKinds() {
		if k == "Pod" || k == "Node" {
			resp.Completeness.Pending = append(resp.Completeness.Pending, k)
		}
	}

	// Capacity + usage — node-derived data, so it sits behind the same node
	// RBAC gate as the counts (the dashboard computes metrics inside its
	// canReadNodes branch; a caller denied node list must not learn cluster
	// capacity here either). Requests/capacity are informer-derived; live
	// usage is the metrics-server probe, memoized (see vitalsMetricsMemo).
	if canReadNodes && cache.Nodes() != nil {
		// Capacity + requests are informer-derived — computed FRESH on every
		// call (memoizing them would freeze counts for the TTL). Only the
		// live metrics-server probe is memoized, keyed by user (the probe
		// impersonates the caller; results must never cross identities).
		// Context switches clear the memo wholesale (finalizePostContextSwitch).
		nodes, _ := cache.Nodes().List(labels.Everything())
		cr := computeCapacityRequests(nodes, scopedPods)
		cpuCap, memCapBytes := cr.cpuCapMillis, cr.memCapBytes
		cpuReq, memReqBytes := cr.cpuReqMillis, cr.memReqBytes

		username := ""
		if user := auth.UserFromContext(r.Context()); user != nil {
			username = user.Username
		}
		// Key carries the kube context too: finalizePostContextSwitch clears
		// the memo, but a request racing the switch window could otherwise
		// resurrect the previous cluster's usage for this user (same class
		// of race PermissionCache stamps against).
		memoKey := k8s.GetContextName() + "\x00" + username
		usage := s.vitalsMetrics.loadOrFetch(memoKey, func() vitalsMetricsEntry {
			cpu, mem, avail := s.fetchNodeUsage(r.Context())
			return vitalsMetricsEntry{cpuMillis: cpu, memBytes: mem, ok: avail}
		})

		if cpuCap > 0 {
			resp.CPU = &MetricSummary{
				UsageMillis:    usage.cpuMillis,
				RequestsMillis: cpuReq,
				CapacityMillis: cpuCap,
				UsagePercent:   int(usage.cpuMillis * 100 / cpuCap),
				RequestPercent: int(cpuReq * 100 / cpuCap),
			}
		}
		if memCapBytes > 0 {
			memCapMiB := memCapBytes / (1024 * 1024)
			memUseMiB := usage.memBytes / (1024 * 1024)
			memReqMiB := memReqBytes / (1024 * 1024)
			m := &MetricSummary{
				UsageMillis:    memUseMiB,
				RequestsMillis: memReqMiB,
				CapacityMillis: memCapMiB,
				UsagePercent:   int(memUseMiB * 100 / memCapMiB),
				RequestPercent: int(memReqMiB * 100 / memCapMiB),
			}
			resp.Memory = m
		}
		resp.MetricsServerAvailable = usage.ok
	}

	resp.Completeness.Restricted = restricted
	resp.Completeness.Complete = !resp.Completeness.AccessRestricted &&
		len(resp.Completeness.Pending) == 0 && len(restricted) == 0

	s.writeJSON(w, resp)
}
