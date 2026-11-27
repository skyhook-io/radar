package server

import (
	"context"
	"log"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
)

// The Vulnerabilities view's Traffic seen marks read what the active Live
// Traffic source already collects: one fetch of its window, shared by every
// request and user for vulnTrafficFresh, never a query per row. Each answer
// lists only the workloads the caller's read returned, so the shared fetch
// names nothing they couldn't see.
const (
	// vulnTrafficWindow is how far back a fetch asks the source to look.
	vulnTrafficWindow = 15 * time.Minute
	// vulnTrafficFresh is how long one fetch answers every request.
	vulnTrafficFresh = time.Minute
	// vulnTrafficStale is how old an answer may be and still stand in, while
	// a new fetch runs past the wait or after one failed. The window the
	// page states is counted from the fetch, so this bounds how far the
	// claim can lag.
	vulnTrafficStale = 2 * time.Minute
	// vulnTrafficWait is the most a page read waits on a fetch: past it the
	// page loads without the marks, which arrive on a later refresh.
	vulnTrafficWait = 3 * time.Second
	// vulnTrafficFetchTimeout bounds one fetch, detection included. A
	// filtered export waits this long, since its rows depend on the answer.
	vulnTrafficFetchTimeout = 30 * time.Second
)

type vulnTrafficMemo struct {
	mu sync.Mutex
	// manager and source are what the answer came from: a context switch
	// builds a new manager, and the Traffic view can switch the active
	// source on the same one, so an answer never outlives either.
	manager *traffic.Manager
	source  string
	// gen changes with manager or source, so a fetch that started before
	// the change doesn't publish after it.
	gen uint64
	// at is when ev was read; tried is when the last fetch ended, answered
	// or not, so a failing source is asked once per vulnTrafficFresh.
	at, tried time.Time
	// ev is nil when no source answered; that is remembered too, so a
	// cluster without one isn't asked on every read.
	ev *vulnerabilities.TrafficEvidence
	// fetching is closed when the running fetch ends; nil when none runs.
	fetching chan struct{}
}

var vulnTraffic vulnTrafficMemo

// trafficFor is vulnerabilityTraffic for a view with rows to mark; the
// summary view's counts never read the source.
func trafficFor(ctx context.Context, view vulnerabilities.View, wait time.Duration) *vulnerabilities.TrafficEvidence {
	if view == vulnerabilities.ViewSummary {
		return nil
	}
	return vulnerabilityTraffic(ctx, wait)
}

// vulnerabilityTraffic is what Live Traffic saw for the Vulnerabilities
// view, or nil when no source is active and connected (or it is still
// being read).
func vulnerabilityTraffic(ctx context.Context, wait time.Duration) *vulnerabilities.TrafficEvidence {
	mgr := traffic.GetManager()
	if mgr == nil {
		return nil
	}
	return vulnTraffic.get(ctx, mgr, mgr.ActiveSourceNameIfIdle, mgr.GetActiveSourceName, wait, func(ctx context.Context) (*vulnerabilities.TrafficEvidence, bool) {
		return fetchVulnerabilityTraffic(ctx, mgr)
	})
}

// get answers from the memo, fetching when it is older than vulnTrafficFresh.
// fetch reports ok=false for an answer that failed, which keeps the last good
// one standing until it is vulnTrafficStale old. active must not block: while
// the manager is busy (detecting sources), a read gets the standing answer
// rather than waiting on it. settled is read once a fetch ends, off the
// request path, to name the source it published under.
func (m *vulnTrafficMemo) get(ctx context.Context, mgr *traffic.Manager, active func() (string, bool), settled func() string, wait time.Duration, fetch func(context.Context) (*vulnerabilities.TrafficEvidence, bool)) *vulnerabilities.TrafficEvidence {
	m.mu.Lock()
	source, idle := active()
	if !idle {
		var ev *vulnerabilities.TrafficEvidence
		if m.manager == mgr {
			ev = m.standing()
		}
		m.mu.Unlock()
		return ev
	}
	if m.manager != mgr || m.source != source {
		m.manager, m.source, m.gen = mgr, source, m.gen+1
		m.at, m.tried, m.ev, m.fetching = time.Time{}, time.Time{}, nil, nil
	}
	if !m.tried.IsZero() && time.Since(m.tried) < vulnTrafficFresh {
		ev := m.standing()
		m.mu.Unlock()
		return ev
	}
	gen := m.gen
	if m.fetching == nil {
		done := make(chan struct{})
		m.fetching = done
		go func() {
			fctx, cancel := context.WithTimeout(context.Background(), vulnTrafficFetchTimeout)
			defer cancel()
			ev, ok := fetch(fctx)
			// A fetch that detected the source publishes under it; one that
			// read a source since switched away from publishes nothing.
			after := settled()
			m.mu.Lock()
			if m.gen == gen && (m.source == after || m.source == "") && (ev == nil || ev.Source == after) {
				m.source, m.tried = after, time.Now()
				if ok {
					m.ev, m.at = ev, m.tried
				}
			}
			if m.fetching == done {
				m.fetching = nil
			}
			m.mu.Unlock()
			close(done)
		}()
	}
	done := m.fetching
	stale := m.standing()
	m.mu.Unlock()

	// The source can switch while a fetch runs, before any read resets the
	// memo: an answer from another source than the active one isn't given.
	current := func(ev *vulnerabilities.TrafficEvidence) *vulnerabilities.TrafficEvidence {
		if name, idle := active(); ev != nil && idle && name != ev.Source {
			return nil
		}
		return ev
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		m.mu.Lock()
		ev := m.standing()
		if m.gen != gen {
			ev = nil
		}
		m.mu.Unlock()
		return current(ev)
	case <-timer.C:
		return current(stale)
	case <-ctx.Done():
		return current(stale)
	}
}

// trafficSourceSwitched is whether Live Traffic's active source changed
// since ev was read, as a read of the reports can take a while. A busy
// manager (detecting sources) can't say, and isn't waited on.
func trafficSourceSwitched(ev *vulnerabilities.TrafficEvidence) bool {
	mgr := traffic.GetManager()
	if mgr == nil {
		return true
	}
	name, idle := mgr.ActiveSourceNameIfIdle()
	return idle && name != ev.Source
}

// standing is the answer while it is younger than vulnTrafficStale. Callers
// hold mu.
func (m *vulnTrafficMemo) standing() *vulnerabilities.TrafficEvidence {
	if m.at.IsZero() || time.Since(m.at) >= vulnTrafficStale {
		return nil
	}
	return m.ev
}

// fetchVulnerabilityTraffic reads the active source's window. It reuses a
// recent detection, as the workload pages do. It answers nil, ok when no
// source is active, and ok=false when the source couldn't answer this time
// (busy, Hubble before the Traffic view connected to it, a failed query).
func fetchVulnerabilityTraffic(ctx context.Context, mgr *traffic.Manager) (*vulnerabilities.TrafficEvidence, bool) {
	name, idle := mgr.ActiveSourceNameIfIdle()
	if !idle {
		return nil, false
	}
	if name == "" {
		if _, err := mgr.RecentSources(ctx, recentSourcesMaxAge); err != nil {
			return nil, false
		}
		if mgr.GetActiveSourceName() == "" {
			return nil, true
		}
	}
	resp, err := mgr.GetFlows(ctx, traffic.FlowOptions{Since: vulnTrafficWindow, FromOutsideOnly: true, RecentOnly: true})
	if err != nil || resp == nil {
		return nil, false
	}
	// A metrics store holding several clusters' traffic that Radar couldn't
	// narrow to this one: a workload's name alone can't say whose traffic it
	// was.
	if resp.WarningClusterScope {
		return nil, true
	}
	// An empty answer with a warning is a source that couldn't read: not
	// connected yet, or its query failed. With flows, a warning only says
	// some may be missing, and what was seen still was.
	if len(resp.Flows) == 0 && resp.Warning != "" {
		return nil, false
	}
	cache := k8s.GetResourceCache()
	flows := resp.Flows
	// The window shown is cut to where every node's history reaches; a
	// quieter node's older flows would otherwise count from before it.
	if resp.CoveredSince != nil {
		flows = slices.DeleteFunc(slices.Clone(flows), func(f traffic.Flow) bool {
			return !f.LastSeen.IsZero() && f.LastSeen.Before(*resp.CoveredSince)
		})
	}
	flows = resolveFlowWorkloads(cache, flows)
	window, limited := vulnTrafficWindow, resp.Source == "hubble"
	if resp.CoveredSince != nil {
		limited = true
		if span := time.Since(*resp.CoveredSince).Truncate(time.Second); span < window {
			window = span
		}
	}
	ev := vulnerabilities.NewTrafficEvidence(resp.Source, window, limited, flows, clusterNodes(cache))
	ev.MarkSharedNames(workloadNameShared(cache))
	log.Printf("[vulnerabilities] Live Traffic (%s) saw %d workloads receive connections in %s", resp.Source, ev.Len(), window)
	return ev, true
}

// workloadNameShared says whether more than one workload kind in a
// namespace has a name, by Radar's own cache. A kind the cache doesn't hold
// isn't counted.
func workloadNameShared(cache *k8s.ResourceCache) func(namespace, name string) bool {
	if cache == nil {
		return nil
	}
	return func(namespace, name string) bool {
		n := 0
		count := func(err error) {
			if err == nil {
				n++
			}
		}
		if l := cache.Deployments(); l != nil {
			_, err := l.Deployments(namespace).Get(name)
			count(err)
		}
		if l := cache.StatefulSets(); l != nil {
			_, err := l.StatefulSets(namespace).Get(name)
			count(err)
		}
		if l := cache.DaemonSets(); l != nil {
			_, err := l.DaemonSets(namespace).Get(name)
			count(err)
		}
		if l := cache.CronJobs(); l != nil {
			_, err := l.CronJobs(namespace).Get(name)
			count(err)
		}
		if l := cache.Jobs(); l != nil {
			_, err := l.Jobs(namespace).Get(name)
			count(err)
		}
		if l := cache.Pods(); l != nil {
			_, err := l.Pods(namespace).Get(name)
			count(err)
		}
		return n > 1
	}
}

// nodeAddressAnnotations are where CNIs record the address a node's own
// traffic into its Pods comes from (its tunnel or host interface), which
// Node.Status doesn't list.
var nodeAddressAnnotations = []string{
	"projectcalico.org/IPv4IPIPTunnelAddr", "projectcalico.org/IPv4VXLANTunnelAddr", "projectcalico.org/IPv6VXLANTunnelAddr",
	"projectcalico.org/IPv4WireguardInterfaceAddr", "network.cilium.io/ipv4-cilium-host", "network.cilium.io/ipv6-cilium-host",
}

// clusterNodes says whether an endpoint is one of the cluster's nodes, for
// sources that report a node like any address outside the cluster: by name,
// by an address the Node lists or a CNI recorded, or by an address inside a
// node's Pod CIDR that no Pod holds. That last one is the node's gateway on
// the Pod network (10.244.0.1 on kind), where kubelet probes come from.
func clusterNodes(cache *k8s.ResourceCache) func(traffic.Endpoint) bool {
	var nodes []*corev1.Node
	if cache != nil && cache.Nodes() != nil {
		nodes, _ = cache.Nodes().List(labels.Everything())
	}
	return nodeMatcher(nodes)
}

func nodeMatcher(nodes []*corev1.Node) func(traffic.Endpoint) bool {
	names, addrs := map[string]bool{}, map[string]bool{}
	var podCIDRs []netip.Prefix
	for _, n := range nodes {
		names[n.Name] = true
		for _, a := range n.Status.Addresses {
			addrs[a.Address] = true
		}
		for _, key := range nodeAddressAnnotations {
			if a := n.Annotations[key]; a != "" {
				addrs[strings.Split(a, "/")[0]] = true
			}
		}
		for _, c := range append([]string{n.Spec.PodCIDR}, n.Spec.PodCIDRs...) {
			if p, err := netip.ParsePrefix(c); err == nil {
				podCIDRs = append(podCIDRs, p)
			}
		}
	}
	inPodCIDR := func(s string) bool {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return false
		}
		for _, p := range podCIDRs {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(e traffic.Endpoint) bool {
		// A Pod or workload is never a node, whatever its namespace is called.
		if e.Workload != "" || strings.EqualFold(e.Kind, traffic.EndpointKindPod) || strings.EqualFold(e.Kind, traffic.EndpointKindWorkload) {
			return false
		}
		// Caretta files nodes and outside addresses under these namespaces.
		if e.Namespace != "" && e.Namespace != "node" && e.Namespace != "external" {
			return false
		}
		return names[e.Name] || addrs[e.Name] || (e.IP != "" && addrs[e.IP]) || inPodCIDR(e.Name) || inPodCIDR(e.IP)
	}
}
