package prometheus

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

// discover finds and connects to Prometheus using a multi-layer approach:
//  1. Manual URL override (--prometheus-url)
//  2. Existing traffic system port-forward
//  3. Well-known service locations (via pkg/prom.Discover)
//  4. Dynamic cluster-wide discovery with scoring (via pkg/prom.Discover)
//
// Well-known + dynamic candidate enumeration lives in pkg/prom.Discover so
// it can be shared by any consumer of the package. This function owns
// Radar's port-forward fallback, which is only needed when Radar runs
// outside the cluster and can't reach in-cluster Service DNS directly.
//
// The lock is only held briefly to read/write state, not during network I/O.
func (c *Client) discover(ctx context.Context) (string, string, error) {
	// Layer 1: Manual URL override
	c.mu.RLock()
	manualURL := c.manualURL
	contextName := c.contextName
	k8sClient := c.k8sClient
	c.mu.RUnlock()

	if manualURL != "" {
		addr := strings.TrimRight(manualURL, "/")
		if c.probe(ctx, addr) {
			log.Printf("[prometheus] Using manual URL: %s", addr)
			c.markConnected(addr, "")
			return addr, "", nil
		}
		if !discoveryDiagnosticsSuppressed(ctx) {
			errorlog.Record("prometheus", "error", "manual Prometheus URL %s not reachable", addr)
		}
		return "", "", fmt.Errorf("manual Prometheus URL %s not reachable", addr)
	}

	// Layer 2: Reuse traffic system's existing port-forward if present
	if pfAddr := portforward.GetAddress(contextName); pfAddr != "" {
		if c.probe(ctx, pfAddr) {
			log.Printf("[prometheus] Using traffic system port-forward: %s", pfAddr)
			c.markConnected(pfAddr, "")
			return pfAddr, "", nil
		}
	}

	if k8sClient == nil {
		return "", "", fmt.Errorf("no Kubernetes client available for discovery")
	}

	// Layers 3 + 4: Enumerate candidates via the shared pkg/prom discovery
	// logic. Well-known first, then dynamic fallbacks.
	candidates, err := prom.Discover(ctx, k8sClient, prom.DiscoverOptions{
		IncludeDynamic: true,
		Logger: func(format string, args ...interface{}) {
			log.Printf("[prometheus] "+format, args...)
		},
	})
	if err != nil {
		log.Printf("[prometheus] Discover error: %v", err)
	}
	if len(candidates) == 0 {
		if !discoveryDiagnosticsSuppressed(ctx) {
			errorlog.Record("prometheus", "warning", "no Prometheus service found in cluster")
		}
		return "", "", fmt.Errorf("no Prometheus service found in cluster")
	}

	log.Printf("[prometheus] Found %d candidate(s), probing...", len(candidates))

	// First pass: probe each candidate at its in-cluster address. Works when
	// radar is running in-cluster OR when the user's shell can route to the
	// cluster DNS (rare, but cheap to try).
	for _, cand := range candidates {
		addr := cand.ClusterAddr + cand.BasePath
		if c.probe(ctx, addr) {
			log.Printf("[prometheus] Connected to %s/%s at %s (source=%s, score=%d)",
				cand.Namespace, cand.Name, cand.ClusterAddr, cand.Source, cand.Score)
			c.setDiscoveryServiceFromCandidate(cand)
			c.markConnected(cand.ClusterAddr, cand.BasePath)
			return cand.ClusterAddr, cand.BasePath, nil
		}
	}

	// Fallback: try port-forwarding candidates in priority order. This path is
	// normally reached when Radar runs outside the cluster, where in-cluster
	// Service DNS cannot resolve from the user's machine.
	var lastErr error
	for _, cand := range candidates {
		log.Printf("[prometheus] No candidate reachable in-cluster, starting port-forward to %s/%s...",
			cand.Namespace, cand.Name)
		c.setDiscoveryServiceFromCandidate(cand)

		connInfo, pfErr := portforward.Start(ctx, cand.Namespace, cand.Name, cand.TargetPort, contextName)
		if pfErr != nil {
			lastErr = fmt.Errorf("port-forward to %s/%s failed: %w", cand.Namespace, cand.Name, pfErr)
			if !discoveryDiagnosticsSuppressed(ctx) {
				errorlog.Record("prometheus", "error", "port-forward to %s/%s failed: %v", cand.Namespace, cand.Name, pfErr)
			}
			continue
		}

		addr := connInfo.Address
		if c.probe(ctx, addr+cand.BasePath) {
			c.markConnected(addr, cand.BasePath)
			return addr, cand.BasePath, nil
		}

		portforward.Stop()
		lastErr = fmt.Errorf("Prometheus at %s/%s not responding after port-forward", cand.Namespace, cand.Name)
		if !discoveryDiagnosticsSuppressed(ctx) {
			errorlog.Record("prometheus", "error", "Prometheus at %s/%s not responding after port-forward", cand.Namespace, cand.Name)
		}
	}

	c.mu.Lock()
	c.discoveryService = nil
	c.mu.Unlock()
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("no Prometheus service found in cluster")
}

// setDiscoveryServiceFromCandidate records the discovered service metadata
// from a pkg/prom.Candidate.
func (c *Client) setDiscoveryServiceFromCandidate(cand prom.Candidate) {
	c.mu.Lock()
	c.discoveryService = &prom.ServiceInfo{
		Namespace: cand.Namespace,
		Name:      cand.Name,
		Port:      cand.Port,
		BasePath:  cand.BasePath,
	}
	c.mu.Unlock()
}

// markConnected records the active connection and marks discovery as
// complete. Also clears any cached pkg/prom.Client so the next
// getPromClient rebuilds against the (possibly new) address — otherwise
// a stale cached client could survive a discovery that landed on a
// different endpoint.
func (c *Client) markConnected(addr, basePath string) {
	c.mu.Lock()
	c.baseURL = addr
	c.basePath = basePath
	c.prom = nil
	c.discovered = true
	c.mu.Unlock()
}
