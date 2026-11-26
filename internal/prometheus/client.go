package prometheus

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

// Client is radar's application-scoped Prometheus client. It holds the
// K8s-aware state required for kubectl-like port-forward discovery, along
// with a pkg/prom.Client that performs the actual HTTP calls once an
// endpoint has been discovered.
type Client struct {
	sourceNote    *prom.MetricsSource
	lifetime      context.Context
	cancel        context.CancelFunc
	handles       portforward.Handles
	discoveryOnce sync.Once
	discoveryGate chan struct{}
	gkeSF         singleflight.Group
	gkeServices   *serviceInventory
	gke           *gkeMetrics
	gkeGeneration uint64
	gkeRevision   uint64
	gkeChecked    time.Time
	mu            sync.RWMutex

	// Effective connection (populated after discover succeeds).
	baseURL  string
	basePath string
	prom     *prom.Client // rebuilt whenever baseURL/basePath changes
	// Keep logical backend identity across reconnects: a new local forwarding
	// port is not a new backend, but automatic failover must invalidate trust.
	connectedIdentity string
	connectionEpoch   uint64

	// Discovery state
	discoveryService           *prom.ServiceInfo // discovered service info for port-forward
	manualURL                  string            // --prometheus-url override
	headers                    map[string]string
	workloadScope              *prom.WorkloadMetricsScope
	workloadScopeEverSet       bool
	beylaJobSelector           string
	workloadAttributionEntries map[string]*workloadAttributionEntry
	workloadPartition          *workloadPartitionMemo
	workloadPartitionSF        singleflight.Group
	viewScopes                 map[string]viewScopeEntry
	viewScopeSF                singleflight.Group
	lastDiscoverErr            error
	lastDiscoverAt             time.Time

	// relay, when set, serves every request through a metrics relay instead
	// of a discovered or configured Prometheus; see relay.go.
	relay *relayState

	// discoveryGen increments whenever configuration that invalidates a
	// discovery result changes (Reset, SetManualURL, SetHeaders). It keys the
	// singleflight and gates markConnected so a discovery started under an old
	// configuration can't publish its (now stale) endpoint after the change.
	discoveryGen uint64

	// flightGen / flightActive are owned by the detached discovery run itself,
	// not by its waiters: callers come and go on their own deadlines while the
	// run continues, so "discovering" must follow the run's lifetime (gate wait
	// included) for the status endpoint to report it truthfully.
	flightGen    uint64
	flightActive bool
	// flightDone is closed when the most recently started run ends, so the
	// background rediscovery worker can wait for an in-flight run instead of
	// joining it: a run that began before a signal says nothing about it.
	flightDone chan struct{}

	// rediscovery is the event-driven background rediscovery worker's state;
	// see rediscover.go.
	rediscovery rediscoveryState

	// lastOutcome is the terminal result of the most recent run under the
	// current generation, for display only — retry throttling stays on
	// lastDiscoverErr, which deliberately ignores timeouts. Cleared when a run
	// starts so an old failure never sits next to an in-flight one, and on
	// success or any configuration change.
	lastOutcome    string
	lastOutcomeGen uint64

	// discoveryCancel aborts the in-flight detached discovery (if any). Reset /
	// Reinitialize call it so a superseded run — e.g. an old context's pre-warm
	// after a context switch — stops promptly and releases the discovery gate,
	// rather than holding it while the new context waits. discoveryCancelSeq
	// identifies the run that owns the handle so an ending run only clears its
	// own cancel, never a newer run's.
	discoveryCancel    context.CancelFunc
	discoveryCancelSeq uint64

	// retired is set by Reinitialize on the outgoing Client. A discovery whose
	// singleflight goroutine hadn't started yet when the swap happened (so
	// discoveryCancel was still nil and couldn't be cancelled) checks this at
	// launch and aborts — otherwise the orphaned run could grab the discovery
	// gate and drive the shared port-forward for the retired context.
	retired bool

	cache       *k8s.ResourceCache
	scans       *RightsizingScanManager
	unsubscribe func()

	// K8s clients for discovery
	forwards    *portforward.Manager
	forward     *portforward.Handle
	k8sClient   kubernetes.Interface
	k8sConfig   *rest.Config
	contextName string

	// inCluster records how Radar reaches the cluster, captured once at
	// construction from the same detection the connection context reports as
	// "in-cluster". In-cluster, discovery connects over the direct Service-address
	// probe and does not fall back to a port-forward it cannot open (pods/
	// portforward is normally denied); the port-forward fallback runs only when
	// local. The direct probe pass itself runs in both modes — off-cluster it is
	// bounded (see directProbeBudget) and also connects on routed networks.
	inCluster bool

	// Shared HTTP client used when constructing the underlying pkg/prom.Client.
	httpClient *http.Client

	// Dedicated HTTP client for the MCP path
	mcpHTTPClient *http.Client

	// discoverySF coalesces concurrent discovery attempts. A cold start fires
	// several EnsureConnected calls at once (opencost summary/nodes/trend,
	// traffic), which would otherwise repeat the same probes and service lookups.
	discoverySF singleflight.Group
}

const failedDiscoveryCacheTTL = 5 * time.Second

// discoveryTimeout is a hang backstop for a single coalesced discovery, not a
// per-attempt budget. Discovery detaches from the caller's request context, so
// this bounds how long a run may take before every waiting caller gets an
// error: enough headroom for a few serial port-forward attempts (each ~10s)
// plus probes, without letting a wedged run block discovery indefinitely.
const discoveryTimeout = 60 * time.Second

func (c *Client) headersRequireURLLocked() bool {
	return c.relay == nil && prom.HeadersRequireURL(c.manualURL, c.headers)
}

type suppressDiscoveryDiagnosticsKey struct{}

func withSuppressedDiscoveryDiagnostics(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressDiscoveryDiagnosticsKey{}, true)
}

func discoveryDiagnosticsSuppressed(ctx context.Context) bool {
	suppressed, _ := ctx.Value(suppressDiscoveryDiagnosticsKey{}).(bool)
	return suppressed
}

func NewClient(client kubernetes.Interface, config *rest.Config, contextName string, forwards *portforward.Manager, inCluster bool, cache *k8s.ResourceCache) *Client {
	lifetime, cancel := context.WithCancel(context.Background())
	c := &Client{
		cache:         cache,
		lifetime:      lifetime,
		cancel:        cancel,
		forwards:      forwards,
		k8sClient:     client,
		k8sConfig:     config,
		contextName:   contextName,
		inCluster:     inCluster,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		mcpHTTPClient: newMCPHTTPClient(),
	}
	c.scans = c.newRightsizingScanManager()
	if cache != nil {
		c.unsubscribe = cache.SubscribeObservedChanges(c.observeCacheChange)
	}
	return c
}

// newMCPHTTPClient builds the HTTP client backing the MCP-only prom.Client.
// Its 200s timeout is a hang backstop, not a query budget: the MCP handlers
// enforce their own per-call ctx deadline (30s default, model-raisable to
// 180s), which must win so the timeout error the model sees is ours. The
// backstop only has to clear the largest per-call budget: 180s + 20s margin.
func newMCPHTTPClient() *http.Client {
	return &http.Client{Timeout: 200 * time.Second}
}

func (c *Client) CurrentConfig() (string, map[string]string) {
	if c == nil {
		return "", nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.manualURL, copyHeaders(c.headers)
}

// ConnectionSource says how Prometheus is reached: "manual" when a URL is
// configured, "discovered" when discovery found one in the cluster, otherwise
// "none".
func (c *Client) ConnectionSource() string {
	if c == nil {
		return "none"
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch {
	case c.manualURL != "":
		return "manual"
	case c.baseURL != "":
		return "discovered"
	}
	return "none"
}

func (c *Client) Configure(rawURL string, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configureLocked(rawURL, headers)
}

func (c *Client) configureLocked(rawURL string, headers map[string]string) {
	normalized := strings.TrimRight(rawURL, "/")
	if c.relay != nil {
		// Kept for when the relay ends; the relay's connection and scope stay.
		if c.manualURL != normalized || !maps.Equal(c.headers, headers) {
			c.relay.setAside(func(s *prometheusSettings) { s.scope = nil })
		}
		c.manualURL = normalized
		c.headers = copyHeaders(headers)
		return
	}
	if c.manualURL != normalized || !maps.Equal(c.headers, headers) {
		if c.workloadScope != nil {
			log.Print("[prometheus] Workload metrics scope cleared: endpoint or headers changed; restart with a fresh scope assertion")
		}
		c.workloadScope = nil
	}
	c.manualURL = normalized
	c.headers = copyHeaders(headers)
	c.dropConnectionLocked()
	c.discoveryGen++
	c.cancelWorkloadAttributionsLocked()
	// Abort any in-flight discovery started under the old config so it releases
	// the discovery gate promptly rather than stalling rediscovery. Safe under
	// the lock: cancel only signals the context.
	if c.discoveryCancel != nil {
		c.discoveryCancel()
	}
}

// dropConnectionLocked forgets the current endpoint and every result derived
// from it, so the next request rediscovers under the current configuration.
func (c *Client) dropConnectionLocked() {
	c.forward.Stop()
	c.forward = nil
	c.baseURL = ""
	c.basePath = ""
	c.prom = nil
	c.discoveryService = nil
	c.lastDiscoverErr = nil
	c.lastDiscoverAt = time.Time{}
	c.lastOutcome = ""
}

func copyHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	maps.Copy(out, h)
	return out
}

// Prewarm starts discovery in the background so charts are ready by the time
// a user opens one, at boot and after every context switch. It never blocks
// the caller and waits a little past the run's own hang backstop so its log
// line describes a real outcome. Diagnostics are suppressed: a cluster that
// simply has no Prometheus should not record a warning on every start — the
// status endpoint carries the reason where it is useful.
func (c *Client) Prewarm() {
	if c != nil {
		go c.connectInBackground("Auto-discovery")
	}
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.retired = true
	if c.cancel != nil {
		c.cancel()
	}
	c.cancelWorkloadAttributionsLocked()
	if c.discoveryCancel != nil {
		c.discoveryCancel()
	}
	c.mu.Unlock()
	c.handles.Close()
	if c.scans != nil {
		c.scans.Invalidate()
	}
	if c.unsubscribe != nil {
		c.unsubscribe()
	}
	return nil
}

func (c *Client) connectInBackground(label string) error {
	start := time.Now()
	ctx, release := portforward.WithLifetime(context.Background(), c.lifetime)
	defer release()
	ctx, cancel := context.WithTimeout(withSuppressedDiscoveryDiagnostics(ctx), discoveryTimeout+5*time.Second)
	defer cancel()
	addr, _, err := c.connectWithGKE(ctx)
	switch {
	case err == nil:
		log.Printf("[prometheus] %s connected to %s (%s)", label, addr, took(start))
	case c.gkeChartStore() != nil:
		log.Printf("[prometheus] %s connected to GKE system metrics (%s)", label, took(start))
		return nil
	case errors.Is(err, prom.ErrHeadersRequireURL):
		log.Printf("[prometheus] %s skipped: %v", label, err)
	default:
		log.Printf("[prometheus] %s failed (%s): %v", label, took(start), err)
	}
	return err
}

// DiscoveryGeneration identifies the current connection configuration. It
// moves on Reset, a manual URL change and a header change, so callers that
// cache anything derived from the connected endpoint can key on it.
func (c *Client) DiscoveryGeneration() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.discoveryGen
}

func (c *Client) BackendEpoch() uint64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connectionEpoch
}

// GetStatus returns the current Prometheus connection status.
func (c *Client) GetStatus() prom.Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.retired {
		return prom.Status{ContextName: c.contextName}
	}

	var svc *prom.ServiceInfo
	if c.discoveryService != nil {
		cp := *c.discoveryService
		svc = &cp
	}

	if c.relay != nil {
		src := c.relay.status(c.lifetime)
		// An unavailable relay source reports not connected, so status
		// never says "Connected" while every chart fails; Address and
		// Source still say which backend it is and why.
		up := !src.Unavailable
		return prom.Status{
			Available:   up,
			Connected:   up,
			Address:     c.relay.cfg.Address,
			ContextName: c.contextName,
			Error:       src.Error,
			Source:      &src,
		}
	}

	address := c.baseURL
	if c.forward != nil && !c.forward.Alive() {
		address = ""
	}
	connected := address != ""
	gkeConnected := c.gkeActiveLocked()
	discovering := !gkeConnected && !connected && c.flightActive && c.flightGen == c.discoveryGen
	st := prom.Status{
		GKESystemMetrics: gkeConnected,
		Available:        connected,
		Connected:        connected,
		Discovering:      discovering,
		Address:          prom.SafeAddress(address),
		Service:          svc,
		ContextName:      c.contextName,
	}
	switch {
	case c.headersRequireURLLocked():
		st.Error = prom.ErrHeadersRequireURL.Error()
	case !gkeConnected && !connected && !discovering && c.lastOutcomeGen == c.discoveryGen:
		st.Error = prom.RedactURLs(c.lastOutcome)
	}
	st.Source = cloneSourceNote(c.sourceNote)
	return st
}

func (c *Client) ResourceCache() *k8s.ResourceCache {
	if c == nil {
		return nil
	}
	return c.cache
}

func (c *Client) Closed() bool { return c.lifetime != nil && c.lifetime.Err() != nil }

func (c *Client) Lifetime() context.Context { return c.lifetime }

func (c *Client) HasManualURL() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.manualURL != ""
}

// EnsureConnected attempts to discover and connect to Prometheus if not
// already connected. Returns the base URL and base path, or an error.
func (c *Client) EnsureConnected(ctx context.Context) (string, string, error) {
	ctx, release := portforward.WithLifetime(ctx, c.lifetime)
	defer release()
	c.mu.RLock()
	if c.retired {
		c.mu.RUnlock()
		return "", "", portforward.ErrClosed
	}
	base, forward := c.baseURL, c.forward
	bp := c.basePath
	gen := c.discoveryGen
	manual := c.manualURL != ""
	relay := c.relay != nil
	c.mu.RUnlock()

	// A relay reports its own failures per request; probing it here would
	// spend the relay's request budget on every query.
	if base != "" && relay {
		return base, bp, nil
	}
	if base != "" {
		// Probe whatever we already have, building the pkg/prom.Client
		// on-demand. The cached client may be nil here for two reasons:
		// (a) a concurrent request hasn't yet primed getPromClient, or
		// (b) SetHeaders cleared the cache to force a header reload.
		// In both cases the connection itself is still valid; only the
		// cached client wrapper needs rebuilding. Pre-extraction probed
		// solely on base!="", so this preserves that behavior.
		if p := c.getPromClient(); p != nil {
			probe := p.Probe
			if manual {
				probe = p.ProbeQueryAPI
			}
			ok, reason := false, prom.ProbeReasonTransportError
			if forward == nil || forward.Alive() {
				ok, reason = probe(withoutFailureReport(ctx))
			}
			// The probe ran unlocked; a configuration change meanwhile makes
			// its answer describe a superseded endpoint. Neither return it nor
			// tear down whatever the new configuration has since connected.
			c.mu.Lock()
			current := !c.retired && c.discoveryGen == gen && c.baseURL == base && c.forward == forward
			if ok && (forward == nil || forward.Alive()) {
				c.mu.Unlock()
				if current {
					return base, bp, nil
				}
				return c.discoverShared(ctx)
			}
			if err := ctx.Err(); err != nil {
				c.mu.Unlock()
				return "", "", err
			}
			if current {
				forward.Stop()
				c.forward = nil
				c.baseURL = ""
				c.basePath = ""
				c.prom = nil
			}
			c.mu.Unlock()
			log.Printf("[prometheus] cached connection to %s failed probe (reason=%s), rediscovering", base, reason)
		}
	}

	return c.discoverShared(ctx)
}

// discoverShared runs discover() under a singleflight so a burst of concurrent
// EnsureConnected callers triggers exactly one discovery and all share its
// result.
//
// The flight key includes the discovery generation, so a caller arriving after
// a config change (Reset / SetManualURL / SetHeaders) never joins a flight
// started under the old configuration — it runs a fresh discovery instead.
//
// The shared discovery detaches from the leader's request context
// (context.WithoutCancel + its own timeout) so it completes for every waiter
// even if the caller that happened to start it goes away. Each caller then
// selects on its OWN context, so a cancelled request returns promptly instead
// of blocking on the detached discovery. Context values (e.g. suppressed
// discovery diagnostics) are preserved.
func (c *Client) discoverShared(ctx context.Context) (string, string, error) {
	// Loop so a run superseded by a mid-flight config change (Reset /
	// SetManualURL / SetHeaders) rediscovers under the new generation instead of
	// returning a spurious failure to callers like opencost. Every supersession
	// bumps discoveryGen, so each retry keys a fresh flight and can't spin without
	// progress; the caller's own context bounds the loop.
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if err := c.recentDiscoveryError(time.Now()); err != nil {
			return "", "", err
		}

		c.mu.RLock()
		gen := c.discoveryGen
		key := fmt.Sprintf("%s#%d", c.contextName, gen)
		gated := c.headersRequireURLLocked()
		c.mu.RUnlock()
		// Checked per iteration, not once at entry: a supersession retry must
		// see a configuration change that introduced headers without a URL.
		if gated {
			return "", "", prom.ErrHeadersRequireURL
		}

		ch := c.discoverySF.DoChan(key, func() (any, error) {
			dctx, release := portforward.WithLifetime(context.WithoutCancel(ctx), c.lifetime)
			defer release()
			dctx, cancel := context.WithTimeout(dctx, discoveryTimeout)
			defer cancel()

			// Publish the cancel so Reset/Reinitialize can abort this detached run.
			// A sequence number identifies this run's handle: two runs can overlap
			// on one Client (an old-gen run cancelled by Reset while a new-gen run
			// starts), and the ending run must not clear the newer run's cancel.
			// Abort here if this run was already superseded before it could publish
			// its handle: retirement (Reinitialize) or a generation bump
			// (Reset/SetManualURL/SetHeaders) whose cancel() no-op'd because the
			// handle was still nil. Without the gen check such a run would hold the
			// discovery gate and a live detached context for up to discoveryTimeout,
			// blocking rediscovery under the new configuration.
			c.mu.Lock()
			if c.retired || c.discoveryGen != gen {
				c.mu.Unlock()
				return nil, context.Canceled
			}
			c.discoveryCancelSeq++
			mySeq := c.discoveryCancelSeq
			c.discoveryCancel = cancel
			c.flightGen = gen
			c.flightActive = true
			c.lastOutcome = ""
			done := make(chan struct{})
			c.flightDone = done
			c.mu.Unlock()
			var flightErr error
			defer func() {
				c.mu.Lock()
				close(done)
				if c.flightDone == done {
					c.flightDone = nil
				}
				if c.discoveryCancelSeq == mySeq {
					c.discoveryCancel = nil
				}
				if c.flightGen == gen {
					c.flightActive = false
				}
				c.recordOutcomeLocked(gen, flightErr)
				c.mu.Unlock()
			}()

			c.discoveryOnce.Do(func() { c.discoveryGate = make(chan struct{}, 1) })
			gate := c.discoveryGate
			select {
			case gate <- struct{}{}:
				defer func() { <-gate }()
			case <-dctx.Done():
				flightErr = dctx.Err()
				return nil, flightErr
			}

			// Another discovery may have connected this client while we waited on
			// the gate; if so, adopt its result instead of rediscovering.
			c.mu.Lock()
			cached := discoveredEndpoint{c.baseURL, c.basePath, c.forward}
			live := c.connectionLiveLocked(cached, gen)
			c.mu.Unlock()
			if live {
				return cached, nil
			}

			endpoint, derr := c.discover(dctx, gen)
			flightErr = derr
			if derr != nil {
				if !errors.Is(derr, portforward.ErrClosed) && !errors.Is(derr, context.Canceled) && !errors.Is(derr, context.DeadlineExceeded) {
					c.mu.Lock()
					if !c.retired && c.discoveryGen == gen {
						c.lastDiscoverErr = derr
						c.lastDiscoverAt = time.Now()
					}
					c.mu.Unlock()
				}
				return nil, derr
			}
			c.mu.Lock()
			live = c.connectionLiveLocked(endpoint, gen)
			c.mu.Unlock()
			if !live {
				flightErr = errDiscoverySuperseded
				return nil, flightErr
			}
			return endpoint, nil
		})

		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case res := <-ch:
			if c.Closed() {
				return "", "", portforward.ErrClosed
			}

			if res.Err != nil {
				// A supersession — a config change cancelled the detached flight —
				// is not the caller's failure. While the caller's own context is
				// live, retry under the new
				// generation. context.Canceled here comes only from the flight's
				// detached context (Reset/Reinitialize), never the caller's; a
				// genuine hang surfaces as DeadlineExceeded and is returned.
				if ctx.Err() == nil && (errors.Is(res.Err, errDiscoverySuperseded) || errors.Is(res.Err, context.Canceled)) {
					continue
				}
				return "", "", res.Err
			}
			r := res.Val.(discoveredEndpoint)
			// Guard against a hollow success: between the run committing and our
			// return, a Reset can clear baseURL, or Reinitialize can retire this
			// client (leaving baseURL set) — either way the result belongs to a
			// superseded context. Retry under the new generation while our own
			// context is live; give up only if the caller itself is done or retired.
			c.mu.Lock()
			live := c.connectionLiveLocked(r, gen)
			c.mu.Unlock()
			if !live {
				if err := ctx.Err(); err != nil {
					return "", "", err
				}
				if !c.Closed() {
					continue
				}
				return "", "", portforward.ErrClosed
			}
			return r.addr, r.basePath, nil
		}
	}
}

func (c *Client) connectionLiveLocked(endpoint discoveredEndpoint, gen uint64) bool {
	if c.retired || c.discoveryGen != gen || c.baseURL == "" || c.baseURL != endpoint.addr ||
		c.basePath != endpoint.basePath || c.forward != endpoint.forward {
		return false
	}
	if c.forward != nil && !c.forward.Alive() {
		c.dropConnectionLocked()
		return false
	}
	return true
}

// recordOutcomeLocked publishes a run's terminal result for the status
// endpoint. Only the run's own generation may write: an older run ending after
// a configuration change must not describe the new configuration. A cancelled
// run was superseded, which is not an outcome of anything the user configured.
func (c *Client) recordOutcomeLocked(gen uint64, err error) {
	if c.retired || c.discoveryGen != gen {
		return
	}
	switch {
	case err == nil:
		c.lastOutcome = ""
	case errors.Is(err, context.Canceled), errors.Is(err, errDiscoverySuperseded), errors.Is(err, portforward.ErrClosed):
		return
	case errors.Is(err, context.DeadlineExceeded):
		c.lastOutcome = fmt.Sprintf("Prometheus discovery timed out after %s", discoveryTimeout)
	default:
		c.lastOutcome = err.Error()
	}
	c.lastOutcomeGen = gen
}

func (c *Client) recentDiscoveryError(now time.Time) error {
	c.mu.RLock()
	err := c.lastDiscoverErr
	at := c.lastDiscoverAt
	c.mu.RUnlock()
	if err == nil || at.IsZero() || now.Sub(at) >= failedDiscoveryCacheTTL {
		return nil
	}
	return err
}

// Prom returns the underlying pkg/prom.Client for callers that compose
// cost math on top of raw Query/QueryRange (e.g.,
// pkg/opencost.ComputeCostSummaryFromProm). Unlike Query/QueryRange this
// does NOT call EnsureConnected; callers must have done so to ensure a
// baseURL is set. Returns nil if discovery has not run.
func (c *Client) Prom() *prom.Client {
	return c.getPromClient()
}

// getPromClient returns a pkg/prom.Client pointed at the current
// baseURL/basePath, building (and caching) one if necessary.
//
// Fast path: cached client under RLock. Slow path: take the write lock and
// build from the live state, which guarantees baseURL/basePath/headers all
// reflect the same point-in-time view. Transport construction is just
// struct-field assignments (no I/O) so holding the write lock across it
// is cheap, and avoids the read-then-rebuild-then-recheck race entirely.
func (c *Client) getPromClient() *prom.Client {
	c.mu.RLock()
	if c.prom != nil {
		p := c.prom
		c.mu.RUnlock()
		return p
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prom != nil {
		return c.prom
	}
	if c.baseURL == "" {
		return nil
	}
	c.prom = c.newConnectedPromClientLocked(c.httpClient)
	return c.prom
}

// ConnectionSnapshot identifies the connection a store handle was taken
// from, so work that pairs a cluster scope check with data queries on that
// handle can tell when the connection moved between them.
type ConnectionSnapshot struct {
	generation uint64
	epoch      uint64
	bound      bool
}

// PromSnapshot is Prom with the connection it belongs to, read together.
func (c *Client) PromSnapshot() (*prom.Client, ConnectionSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prom == nil && c.baseURL != "" {
		c.prom = c.newConnectedPromClientLocked(c.httpClient)
	}
	return c.prom, c.snapshotLocked()
}

// PromForMCPSnapshot is PromForMCP with the connection it belongs to, read
// together.
func (c *Client) PromForMCPSnapshot() (*prom.Client, ConnectionSnapshot) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.baseURL == "" {
		return nil, c.snapshotLocked()
	}
	return c.newConnectedPromClientLocked(c.mcpHTTPClient), c.snapshotLocked()
}

func (c *Client) snapshotLocked() ConnectionSnapshot {
	return ConnectionSnapshot{generation: c.discoveryGen, epoch: c.connectionEpoch, bound: true}
}

// ConnectionCurrent reports whether the connection snap was taken from is
// still the current one: same configuration, same backend, client not
// retired. A zero snapshot is never current.
func (c *Client) ConnectionCurrent(snap ConnectionSnapshot) bool {
	if c == nil || !snap.bound {
		return false
	}
	return c.viewScopeConnectionCurrent(snap.generation, snap.epoch)
}

// PromForMCP returns a prom.Client backed by the MCP-only http.Client, whose
// 200s socket backstop accommodates the MCP path's model-settable per-query
// timeout (up to 180s). The shared Prom() client keeps a 10s backstop that
// bounds REST/opencost callers. Built per call from the current discovery
// state (not cached), so it can never serve a stale endpoint after a reconnect
// or context switch; the underlying mcpHTTPClient connection pool is reused.
// Returns nil if discovery has not run.
func (c *Client) PromForMCP() *prom.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.baseURL == "" {
		return nil
	}
	return c.newConnectedPromClientLocked(c.mcpHTTPClient)
}

// probe checks if a Prometheus endpoint at `addr` is reachable and has at
// least one active scrape target, using pkg/prom.Client.Probe. Records a
// targeted log entry for every non-OK outcome so operators can see why a
// candidate was rejected — particularly important for auth failures (401/403)
// and empty instances, which would otherwise silently fall through the
// discovery candidate list.
func (c *Client) probe(ctx context.Context, addr string) bool {
	return c.probeReachable(ctx, addr, true)
}

// probeReachable is probe with control over rejection logging. The concurrent
// direct-probe pass passes logReject=false and summarizes the whole pass in a
// single line, rather than emitting one "unreachable" entry per candidate — the
// noisy tail that dominated cold-start logs on clusters with several
// Prometheus-like services.
func (c *Client) probeReachable(ctx context.Context, addr string, logReject bool) bool {
	ok, _ := c.probeWithReason(ctx, addr, logReject)
	return ok
}

// probeWithReason is probeReachable that also reports why a probe was
// rejected, so discovery can tell a network-level failure apart from an
// endpoint that answered and was refused.
func (c *Client) probeWithReason(ctx context.Context, addr string, logReject bool) (bool, prom.ProbeReason) {
	c.mu.RLock()
	tr := c.newTransportLocked(addr, "", c.httpClient, 0)
	c.mu.RUnlock()
	ok, reason := prom.NewClient(tr).Probe(ctx)
	if !ok && logReject {
		logProbeRejection(addr, reason, !discoveryDiagnosticsSuppressed(ctx))
	}
	return ok, reason
}

// logProbeRejection records an appropriate log entry for each rejection
// reason. Auth failures get errorlog at error level (likely operator
// misconfiguration); empty instances get warning level (cluster state);
// other failures use stdlib log so they appear in the discovery audit
// trail without flooding errorlog.
func logProbeRejection(addr string, reason prom.ProbeReason, recordDiagnostics bool) {
	switch reason {
	case prom.ProbeReasonAuthError:
		if recordDiagnostics {
			errorlog.Record("prometheus", "error",
				"endpoint %s rejected credentials (HTTP 401/403, check --prometheus-header)", addr)
		}
	case prom.ProbeReasonEmptyInstance:
		if recordDiagnostics {
			errorlog.Record("prometheus", "warning",
				"endpoint %s has no active scrape targets (empty instance), skipping", addr)
		}
	case prom.ProbeReasonNotPrometheus:
		log.Printf("[prometheus] endpoint %s responded but not in Prometheus format, skipping", addr)
	case prom.ProbeReasonPromError:
		log.Printf("[prometheus] endpoint %s returned Prometheus error status, skipping", addr)
	case prom.ProbeReasonTransportError:
		log.Printf("[prometheus] endpoint %s unreachable, skipping", addr)
	case prom.ProbeReasonHTTPError:
		log.Printf("[prometheus] endpoint %s answered with an HTTP error, skipping", addr)
	}
}

// QueryRange executes a Prometheus range query via the underlying pkg/prom.Client.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		// Concurrent Reset cleared baseURL between EnsureConnected returning
		// and getPromClient — the connection was reset under us.
		return nil, errors.New("prometheus connection was reset")
	}
	return p.QueryRange(ctx, query, start, end, step)
}

// MetricNames lists the metric names matching the selectors that hold
// samples between start and end.
func (c *Client) MetricNames(ctx context.Context, matches []string, start, end time.Time) ([]string, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		return nil, errors.New("prometheus connection was reset")
	}
	names, warnings, err := p.LabelValuesWithWarnings(ctx, "__name__", matches, start, end, 2000)
	if err == nil && len(warnings) > 0 {
		return nil, fmt.Errorf("Prometheus returned an incomplete list of metric names: %s", truncate(strings.Join(warnings, "; "), 200))
	}
	return names, err
}

// Query executes a Prometheus instant query via the underlying pkg/prom.Client.
func (c *Client) Query(ctx context.Context, query string) (*prom.QueryResult, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		return nil, errors.New("prometheus connection was reset")
	}
	return p.Query(ctx, query)
}

// QueryAt is Query evaluated at a fixed time.
func (c *Client) QueryAt(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		return nil, errors.New("prometheus connection was reset")
	}
	return p.QueryAt(ctx, query, at)
}

// QueryAtPost is QueryAt with the query sent in a POST body rather than the
// URL.
func (c *Client) QueryAtPost(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		return nil, errors.New("prometheus connection was reset")
	}
	return p.QueryAtPost(ctx, query, at)
}

// QueryPost is Query with the query sent in a POST body rather than the URL.
func (c *Client) QueryPost(ctx context.Context, query string) (*prom.QueryResult, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		return nil, errors.New("prometheus connection was reset")
	}
	return p.QueryPost(ctx, query)
}
