package prometheus

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

// errDiscoverySuperseded is returned when a configuration change (Reset /
// SetManualURL / SetHeaders) invalidated a discovery mid-flight. The result is
// dropped rather than published; the next request rediscovers under the new
// configuration.
var errDiscoverySuperseded = errors.New("prometheus: discovery superseded by a configuration change")

// maxConcurrentProbes bounds the direct probe fan-out so a cluster with many
// Prometheus-like services doesn't open an unbounded number of sockets at once.
const maxConcurrentProbes = 4

// directProbeBudget caps the whole direct-probe pass. It is deliberately larger
// than the per-probe timeout (pkg/prom.Client.Probe uses 3s) so a real but slow
// endpoint — a loaded Prometheus, or one reached over a high-latency VPN — gets
// a full probe attempt rather than being cut short and pushed to port-forward.
// Its real job is the off-cluster case: unresolvable *.svc.cluster.local names
// can wedge in the platform's cgo resolver past their context deadline, so
// without this ceiling the doomed pass would dead-wait for seconds before the
// fallback even starts. This bounds that to one probe window plus margin.
const directProbeBudget = 4 * time.Second

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
//
// gen is the discovery generation the singleflight was keyed with, threaded
// through so markConnected commits against *that* generation. Reading the live
// generation here instead would let a flight keyed at an old generation adopt a
// newer one after a Reset and commit under it, undoing the reset.
func (c *Client) discover(ctx context.Context, gen uint64) (string, string, error) {
	start := time.Now()
	startGen := gen

	// Layer 1: Manual URL override
	c.mu.RLock()
	manualURL := c.manualURL
	contextName := c.contextName
	k8sClient := c.k8sClient
	c.mu.RUnlock()

	if manualURL != "" {
		addr := strings.TrimRight(manualURL, "/")
		if c.probe(ctx, addr) {
			log.Printf("[prometheus] connected via manual URL %s (%s)", addr, took(start))
			if !c.markConnected(addr, "", startGen) {
				return "", "", errDiscoverySuperseded
			}
			return addr, "", nil
		}
		// A cancelled probe means the run was superseded (Reset / config change),
		// not that the operator's URL is bad — don't slander it in errorlog.
		if err := ctx.Err(); err != nil {
			logDiscoveryEnded(start, err)
			return "", "", err
		}
		if !discoveryDiagnosticsSuppressed(ctx) {
			errorlog.Record("prometheus", "error", "manual Prometheus URL %s not reachable", addr)
		}
		return "", "", fmt.Errorf("manual Prometheus URL %s not reachable", addr)
	}

	// Layer 2: Reuse traffic system's existing port-forward if present
	if pfAddr := portforward.GetAddress(portforward.OwnerPrometheus, contextName); pfAddr != "" {
		if c.probe(ctx, pfAddr) {
			log.Printf("[prometheus] connected via traffic port-forward %s (%s)", pfAddr, took(start))
			if !c.markConnected(pfAddr, "", startGen) {
				return "", "", errDiscoverySuperseded
			}
			return pfAddr, "", nil
		}
	}

	if k8sClient == nil {
		return "", "", fmt.Errorf("no Kubernetes client available for discovery")
	}

	// Layers 3 + 4: Enumerate candidates via the shared pkg/prom discovery
	// logic. Well-known first, then dynamic fallbacks.
	enumStart := time.Now()
	candidates, err := prom.Discover(ctx, k8sClient, prom.DiscoverOptions{
		IncludeDynamic: true,
		Logger: func(format string, args ...interface{}) {
			log.Printf("[prometheus] "+format, args...)
		},
	})
	if err != nil {
		log.Printf("[prometheus] Discover error: %v", err)
	}
	// A cancelled enumeration returns no candidates; that's supersession, not an
	// empty cluster — surface it instead of recording "no Prometheus found".
	if err := ctx.Err(); err != nil {
		logDiscoveryEnded(start, err)
		return "", "", err
	}
	if len(candidates) == 0 {
		if !discoveryDiagnosticsSuppressed(ctx) {
			errorlog.Record("prometheus", "warning", "no Prometheus service found in cluster")
		}
		return "", "", fmt.Errorf("no Prometheus service found in cluster")
	}

	log.Printf("[prometheus] enumerated %d candidate(s) in %s: %s", len(candidates), took(enumStart), summarizeCandidates(candidates))

	// Direct pass: probe each candidate at its in-cluster Service address, with
	// bounded concurrency. This connects immediately when Radar runs in-cluster,
	// and also when Radar runs outside the cluster on a network that routes
	// Service DNS / ClusterIPs (VPN, routed dev cluster) — the reachability of
	// the address decides, not how the kubeconfig was loaded. Off-cluster
	// without such routing, every probe fails fast and we fall through to
	// port-forwarding. The winner is the earliest candidate in priority order,
	// so endpoint selection is deterministic.
	directStart := time.Now()
	directCtx, cancelDirect := context.WithTimeout(ctx, directProbeBudget)
	idx := c.probeCandidatesConcurrently(directCtx, candidates)
	cancelDirect()
	if idx >= 0 {
		cand := candidates[idx]
		if !c.markConnected(cand.ClusterAddr, cand.BasePath, startGen) {
			return "", "", errDiscoverySuperseded
		}
		log.Printf("[prometheus] connected to %s/%s via direct probe at %s (source=%s, score=%d, direct %s, total %s)",
			cand.Namespace, cand.Name, cand.ClusterAddr, cand.Source, cand.Score, took(directStart), took(start))
		c.setDiscoveryServiceFromCandidate(cand)
		return cand.ClusterAddr, cand.BasePath, nil
	}
	// A context error here means the run ended before finding anything — either
	// superseded (Reset / context switch) or timed out (the hang backstop) — not
	// that the cluster has no Prometheus. Don't run the fallback or log a scary
	// failure.
	if err := ctx.Err(); err != nil {
		logDiscoveryEnded(start, err)
		return "", "", err
	}
	log.Printf("[prometheus] no candidate reachable via direct probe (%s); falling back to port-forward", took(directStart))

	// Fallback: try port-forwarding candidates in priority order. This is the
	// primary path off-cluster (where Service DNS can't resolve from the user's
	// machine) and a backstop in-cluster. Serial by necessity — port-forwarding
	// mutates the owner's shared forward state.
	var lastErr error
	for _, cand := range candidates {
		// Bail promptly if the run was superseded mid-fallback (Reset / context
		// switch) rather than churning the rest of the list while holding the gate.
		if err := ctx.Err(); err != nil {
			logDiscoveryEnded(start, err)
			return "", "", err
		}
		log.Printf("[prometheus] port-forward → %s/%s:%d (source=%s, score=%d)...",
			cand.Namespace, cand.Name, cand.TargetPort, cand.Source, cand.Score)
		c.setDiscoveryServiceFromCandidate(cand)

		connInfo, pfErr := portforward.Start(portforward.OwnerPrometheus, ctx, cand.Namespace, cand.Name, cand.TargetPort, contextName)
		if pfErr != nil {
			// A cancelled Start means the run was superseded, not that the
			// port-forward genuinely failed — surface it instead of recording one.
			if err := ctx.Err(); err != nil {
				logDiscoveryEnded(start, err)
				return "", "", err
			}
			lastErr = fmt.Errorf("port-forward to %s/%s failed: %w", cand.Namespace, cand.Name, pfErr)
			if !discoveryDiagnosticsSuppressed(ctx) {
				errorlog.Record("prometheus", "error", "port-forward to %s/%s failed: %v", cand.Namespace, cand.Name, pfErr)
			}
			continue
		}

		addr := connInfo.Address
		if c.probe(ctx, addr+cand.BasePath) {
			if !c.markConnected(addr, cand.BasePath, startGen) {
				// Superseded: don't leave the shared forward up for an endpoint
				// we're discarding.
				portforward.Stop(portforward.OwnerPrometheus)
				c.mu.Lock()
				c.discoveryService = nil
				c.mu.Unlock()
				return "", "", errDiscoverySuperseded
			}
			log.Printf("[prometheus] connected to %s/%s via port-forward at %s (%s)",
				cand.Namespace, cand.Name, addr, took(start))
			return addr, cand.BasePath, nil
		}

		portforward.Stop(portforward.OwnerPrometheus)
		// A failed probe on a cancelled context is supersession, not a dead
		// Prometheus — don't slander the endpoint in errorlog.
		if err := ctx.Err(); err != nil {
			logDiscoveryEnded(start, err)
			return "", "", err
		}
		lastErr = fmt.Errorf("Prometheus at %s/%s not responding after port-forward", cand.Namespace, cand.Name)
		if !discoveryDiagnosticsSuppressed(ctx) {
			errorlog.Record("prometheus", "error", "Prometheus at %s/%s not responding after port-forward", cand.Namespace, cand.Name)
		}
	}

	c.mu.Lock()
	c.discoveryService = nil
	c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		logDiscoveryEnded(start, err)
		return "", "", err
	}
	log.Printf("[prometheus] discovery failed after %s: no reachable Prometheus among %d candidate(s)", took(start), len(candidates))
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("no Prometheus service found in cluster")
}

// logDiscoveryEnded logs a discovery that ended on a context error, telling a
// supersession (Reset / config change / context switch → Canceled) apart from
// the hang backstop (discoveryTimeout → DeadlineExceeded) so the trail is honest.
func logDiscoveryEnded(start time.Time, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		log.Printf("[prometheus] discovery timed out after %s", took(start))
		return
	}
	log.Printf("[prometheus] discovery superseded after %s", took(start))
}

// took formats elapsed time for discovery log lines. Go's stdlib logger stamps
// only whole seconds, so the timeline of sub-second discovery steps is only
// legible when each line carries its own elapsed measurement.
func took(start time.Time) string {
	return time.Since(start).Round(time.Millisecond).String()
}

// summarizeCandidates renders the candidate list for a single log line:
// "ns/name(wk)" for well-known, "ns/name(dyn:score)" for dynamically scored —
// enough to see what discovery found and in what priority order without a line
// per candidate.
func summarizeCandidates(candidates []prom.Candidate) string {
	parts := make([]string, len(candidates))
	for i, cand := range candidates {
		src := "wk"
		if cand.Source == prom.CandidateSourceDynamic {
			src = fmt.Sprintf("dyn:%d", cand.Score)
		}
		parts[i] = fmt.Sprintf("%s/%s(%s)", cand.Namespace, cand.Name, src)
	}
	return strings.Join(parts, ", ")
}

// probe outcome states, published atomically per candidate.
const (
	probePending int32 = iota
	probeFailed
	probeSucceeded
)

// probeCandidatesConcurrently probes candidate addresses with bounded
// concurrency and returns the index of the earliest candidate (in priority
// order) that responded, or -1 if none did.
//
// Launching (in priority order, one slot per candidate) runs in its own
// goroutine concurrently with collection, so a fast high-priority hit returns
// immediately instead of waiting for the launcher to work through lower-priority
// candidates that have wedged the semaphore. Each probe publishes its outcome in
// a single atomic Store, so on budget expiry — which happens when a name wedges
// in the platform cgo resolver past its context deadline — the scan for an
// already-succeeded candidate is both race-free and complete (no success can be
// recorded-but-unpublished). Selection stays deterministic: a lower-priority
// success never wins while a higher-priority probe is still pending.
func (c *Client) probeCandidatesConcurrently(ctx context.Context, candidates []prom.Candidate) int {
	n := len(candidates)
	if n == 0 {
		return -1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops in-flight/queued probes once we return

	state := make([]atomic.Int32, n)
	sem := make(chan struct{}, maxConcurrentProbes)
	woke := make(chan struct{}, n) // wake-ups; buffered so a worker never blocks

	go func() {
		for i := range candidates {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(i int) {
				defer func() { <-sem }()
				outcome := probeFailed
				if c.probeReachable(ctx, candidates[i].ClusterAddr+candidates[i].BasePath, false) {
					outcome = probeSucceeded
				}
				state[i].Store(outcome)
				select {
				case woke <- struct{}{}:
				default:
				}
			}(i)
		}
	}()

	earliestSuccess := func() int {
		for i := range n {
			if state[i].Load() == probeSucceeded {
				return i
			}
		}
		return -1
	}

	frontier := 0
	for {
		for frontier < n {
			s := state[frontier].Load()
			if s == probePending {
				break
			}
			if s == probeSucceeded {
				return frontier
			}
			frontier++
		}
		if frontier == n {
			return -1 // every candidate resolved, none reachable
		}
		select {
		case <-woke:
		case <-ctx.Done():
			return earliestSuccess()
		}
	}
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
//
// gen is the discovery generation captured when this discovery began. If the
// generation has since changed (Reset / SetManualURL / SetHeaders), the result
// is stale — a config change raced this discovery — and is dropped rather than
// published over the newer configuration. Returns whether it committed, so the
// caller can surface a retryable error instead of a hollow success (a connected
// address with an empty baseURL).
func (c *Client) markConnected(addr, basePath string, gen uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	// A stale generation, or a client retired by Reinitialize, means the result
	// belongs to a superseded run — don't publish it (and, via the caller's
	// superseded path, release any port-forward it opened for the old context).
	if c.discoveryGen != gen || c.retired {
		return false
	}
	c.baseURL = addr
	c.basePath = basePath
	c.prom = nil
	return true
}
