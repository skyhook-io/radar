package server

import (
	"context"
	"log"
	"net/netip"
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
	// vulnTrafficStale is how long an older answer still stands in while a
	// new fetch runs past vulnTrafficWait.
	vulnTrafficStale = 5 * time.Minute
	// vulnTrafficWait is the most a vulnerabilities read waits on a fetch:
	// past it the page loads without the marks, which arrive on a later
	// refresh.
	vulnTrafficWait = 3 * time.Second
	// vulnTrafficFetchTimeout bounds one fetch, detection included.
	vulnTrafficFetchTimeout = 30 * time.Second
)

type vulnTrafficMemo struct {
	mu sync.Mutex
	// manager is the traffic manager the answer came from; a context
	// switch builds a new one, so an answer never outlives its cluster.
	manager *traffic.Manager
	at      time.Time
	// ev is nil when no source answered; that is remembered too, so a
	// cluster without one isn't asked on every read.
	ev *vulnerabilities.TrafficEvidence
	// fetching is closed when the running fetch ends; nil when none runs.
	fetching chan struct{}
}

var vulnTraffic vulnTrafficMemo

// trafficFor is vulnerabilityTraffic for a view with rows to mark; the
// summary view's counts never read the source.
func trafficFor(ctx context.Context, view vulnerabilities.View) *vulnerabilities.TrafficEvidence {
	if view == vulnerabilities.ViewSummary {
		return nil
	}
	return vulnerabilityTraffic(ctx)
}

// vulnerabilityTraffic is what Live Traffic saw for the Vulnerabilities
// view, or nil when no source is active and connected (or it is still
// being read).
func vulnerabilityTraffic(ctx context.Context) *vulnerabilities.TrafficEvidence {
	mgr := traffic.GetManager()
	if mgr == nil {
		return nil
	}
	return vulnTraffic.get(ctx, mgr, func(ctx context.Context) *vulnerabilities.TrafficEvidence {
		return fetchVulnerabilityTraffic(ctx, mgr)
	})
}

func (m *vulnTrafficMemo) get(ctx context.Context, mgr *traffic.Manager, fetch func(context.Context) *vulnerabilities.TrafficEvidence) *vulnerabilities.TrafficEvidence {
	m.mu.Lock()
	if m.manager != mgr {
		m.manager, m.at, m.ev, m.fetching = mgr, time.Time{}, nil, nil
	}
	if !m.at.IsZero() && time.Since(m.at) < vulnTrafficFresh {
		ev := m.ev
		m.mu.Unlock()
		return ev
	}
	if m.fetching == nil {
		done := make(chan struct{})
		m.fetching = done
		go func() {
			fctx, cancel := context.WithTimeout(context.Background(), vulnTrafficFetchTimeout)
			defer cancel()
			ev := fetch(fctx)
			m.mu.Lock()
			if m.manager == mgr {
				m.ev, m.at = ev, time.Now()
			}
			if m.fetching == done {
				m.fetching = nil
			}
			m.mu.Unlock()
			close(done)
		}()
	}
	done := m.fetching
	var stale *vulnerabilities.TrafficEvidence
	if !m.at.IsZero() && time.Since(m.at) < vulnTrafficStale {
		stale = m.ev
	}
	m.mu.Unlock()

	timer := time.NewTimer(vulnTrafficWait)
	defer timer.Stop()
	select {
	case <-done:
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.manager != mgr {
			return nil
		}
		return m.ev
	case <-timer.C:
		return stale
	case <-ctx.Done():
		return stale
	}
}

// fetchVulnerabilityTraffic reads the active source's window. It reuses a
// recent detection, as the workload pages do, and answers nil when no
// source is active or it answered nothing usable (Hubble before the Traffic
// view connected to it, a failed query).
func fetchVulnerabilityTraffic(ctx context.Context, mgr *traffic.Manager) *vulnerabilities.TrafficEvidence {
	name, idle := mgr.ActiveSourceNameIfIdle()
	if !idle {
		return nil
	}
	if name == "" {
		if _, err := mgr.RecentSources(ctx, recentSourcesMaxAge); err != nil {
			return nil
		}
		if mgr.GetActiveSourceName() == "" {
			return nil
		}
	}
	resp, err := mgr.GetFlows(ctx, traffic.FlowOptions{Since: vulnTrafficWindow, FromOutsideOnly: true, RecentOnly: true})
	if err != nil || resp == nil {
		return nil
	}
	// An empty answer with a warning is a source that couldn't read: not
	// connected yet, or its query failed. With flows, a warning only says
	// some may be missing, and what was seen still was.
	if len(resp.Flows) == 0 && resp.Warning != "" {
		return nil
	}
	cache := k8s.GetResourceCache()
	flows := resolveFlowWorkloads(cache, resp.Flows)
	window, limited := vulnTrafficWindow, resp.Source == "hubble"
	if resp.CoveredSince != nil {
		limited = true
		if span := time.Since(*resp.CoveredSince).Truncate(time.Second); span < window {
			window = span
		}
	}
	ev := vulnerabilities.NewTrafficEvidence(resp.Source, window, limited, flows, clusterNodes(cache))
	log.Printf("[vulnerabilities] Live Traffic (%s) saw %d workloads receive connections in %s", resp.Source, ev.Len(), window)
	return ev
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
		// Caretta files nodes and outside addresses under these namespaces.
		if e.Namespace != "" && e.Namespace != "node" && e.Namespace != "external" {
			return false
		}
		return names[e.Name] || addrs[e.Name] || (e.IP != "" && addrs[e.IP]) || inPodCIDR(e.Name) || inPodCIDR(e.IP)
	}
}
