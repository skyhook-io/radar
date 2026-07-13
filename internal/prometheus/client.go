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
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

// Client is radar's application-scoped Prometheus client. It holds the
// K8s-aware state required for kubectl-like port-forward discovery, along
// with a pkg/prom.Client that performs the actual HTTP calls once an
// endpoint has been discovered.
type Client struct {
	mu sync.RWMutex

	// Effective connection (populated after discover succeeds).
	baseURL  string
	basePath string
	prom     *prom.Client // rebuilt whenever baseURL/basePath changes

	// Discovery state
	discoveryService *prom.ServiceInfo // discovered service info for port-forward
	manualURL        string            // --prometheus-url override
	headers          map[string]string

	// discoveryGen increments whenever configuration that invalidates a
	// discovery result changes (Reset, SetManualURL, SetHeaders). It keys the
	// singleflight and gates markConnected so a discovery started under an old
	// configuration can't publish its (now stale) endpoint after the change.
	discoveryGen uint64

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

	// K8s clients for discovery
	k8sClient   kubernetes.Interface
	k8sConfig   *rest.Config
	contextName string

	// Shared HTTP client used when constructing the underlying pkg/prom.Client.
	httpClient *http.Client

	// Dedicated HTTP client for the MCP path
	mcpHTTPClient *http.Client

	// discoverySF coalesces concurrent discovery attempts. A cold start fires
	// several EnsureConnected calls at once (opencost summary/nodes/trend,
	// traffic), and discovery drives the process-wide port-forward singleton;
	// without coalescing, parallel discoveries clobber each other's forwards.
	discoverySF singleflight.Group
}

// discoveryTimeout is a hang backstop for a single coalesced discovery, not a
// per-attempt budget. Discovery detaches from the caller's request context, so
// this bounds how long a run may take before every waiting caller gets an
// error: enough headroom for a few serial port-forward attempts (each ~10s)
// plus probes, without letting a wedged run block discovery indefinitely.
const discoveryTimeout = 60 * time.Second

// Global client instance
var (
	globalClient *Client
	clientMu     sync.RWMutex
)

// discoveryGate serializes Prometheus discovery across the whole process. Per-
// client singleflight coalesces concurrent callers of the same Client, but
// discovery drives the process-global port-forward singleton, and a client
// reinit on context setup can leave two Client instances discovering at once —
// e.g. the startup pre-warm and the first request landing on either side of the
// swap. Without this gate those independent Prometheus discoveries race the
// single port-forward, each tearing down the other's forward. It runs them one
// at a time; a re-check of the connection after acquiring it lets the loser
// return the winner's endpoint instead of rediscovering.
//
// A buffered channel (not a Mutex) so acquisition can be abandoned when the
// run's context is cancelled — a Reset/Reinitialize that supersedes this run
// must not leave it blocked on the gate. Scope note: this covers Prometheus
// discovery only; the traffic subsystem drives the same port-forward singleton
// without participating, so cross-subsystem arbitration remains a separate
// concern (tracked for a follow-up in the portforward package).
var discoveryGate = make(chan struct{}, 1)

type suppressDiscoveryDiagnosticsKey struct{}

func withSuppressedDiscoveryDiagnostics(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressDiscoveryDiagnosticsKey{}, true)
}

func discoveryDiagnosticsSuppressed(ctx context.Context) bool {
	suppressed, _ := ctx.Value(suppressDiscoveryDiagnosticsKey{}).(bool)
	return suppressed
}

// Initialize creates the global Prometheus client.
func Initialize(client kubernetes.Interface, config *rest.Config, contextName string) {
	clientMu.Lock()
	defer clientMu.Unlock()

	globalClient = &Client{
		k8sClient:     client,
		k8sConfig:     config,
		contextName:   contextName,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		mcpHTTPClient: newMCPHTTPClient(),
	}
}

// newMCPHTTPClient builds the HTTP client backing the MCP-only prom.Client.
// Its 200s timeout is a hang backstop, not a query budget: the MCP handlers
// enforce their own per-call ctx deadline (30s default, model-raisable to
// 180s), which must win so the timeout error the model sees is ours. The
// backstop only has to clear the largest per-call budget: 180s + 20s margin.
func newMCPHTTPClient() *http.Client {
	return &http.Client{Timeout: 200 * time.Second}
}

// SetManualURL sets the --prometheus-url override on the global client.
// manualURL is read under the per-client c.mu (discover, Reinitialize), so the
// write takes c.mu too. clientMu is held (read) across the whole write so a
// concurrent Reinitialize can't swap globalClient out from under us and leave the
// new pointer with stale settings. Lock order clientMu→c.mu matches Reinitialize.
func SetManualURL(rawURL string) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if globalClient == nil {
		return
	}
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	globalClient.manualURL = strings.TrimRight(rawURL, "/")
	globalClient.discoveryGen++
	// Abort any in-flight discovery started under the old config so it releases
	// the discovery gate promptly rather than stalling rediscovery. Safe under
	// the lock: cancel only signals the context.
	if globalClient.discoveryCancel != nil {
		globalClient.discoveryCancel()
	}
}

// SetHeaders sets HTTP headers attached to every Prometheus request on the
// global client. Pass nil or an empty map to clear. Holds clientMu (read) across
// the write for the same reason as SetManualURL.
func SetHeaders(h map[string]string) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if globalClient == nil {
		return
	}
	globalClient.mu.Lock()
	defer globalClient.mu.Unlock()
	globalClient.headers = copyHeaders(h)
	// Drop the cached prom.Client so the next request rebuilds its transport
	// with the new headers.
	globalClient.prom = nil
	globalClient.discoveryGen++
	if globalClient.discoveryCancel != nil {
		globalClient.discoveryCancel() // release the gate for the new config
	}
}

func copyHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	maps.Copy(out, h)
	return out
}

// GetClient returns the global Prometheus client (may be nil).
func GetClient() *Client {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return globalClient
}

// Reset clears connection state so the next query triggers rediscovery (used on context switch).
func Reset() {
	clientMu.Lock()
	defer clientMu.Unlock()
	if globalClient != nil {
		globalClient.mu.Lock()
		globalClient.baseURL = ""
		globalClient.basePath = ""
		globalClient.prom = nil
		globalClient.discoveryService = nil
		globalClient.discoveryGen++
		cancel := globalClient.discoveryCancel
		globalClient.mu.Unlock()
		if cancel != nil {
			cancel() // abort any in-flight discovery started under the old config
		}
		// Tear down our own metrics forward. With per-owner forwards, traffic's
		// Reset no longer stops ours incidentally, so we must clean it up here
		// (context switch and config change both route through Reset).
		portforward.Stop(portforward.OwnerPrometheus)
	}
}

// Reinitialize recreates the client with new K8s connection info.
func Reinitialize(client kubernetes.Interface, config *rest.Config, contextName string) {
	clientMu.Lock()
	defer clientMu.Unlock()

	manualURL := ""
	var headers map[string]string
	var oldCancel context.CancelFunc
	if globalClient != nil {
		// SetManualURL / SetHeaders write these under the per-client mutex
		// after dropping clientMu, so reading without c.mu here would race
		// even though we hold clientMu exclusively. copyHeaders also detaches
		// the map from the old client so a late mutation can't bleed through.
		globalClient.mu.Lock()
		manualURL = globalClient.manualURL
		headers = copyHeaders(globalClient.headers)
		oldCancel = globalClient.discoveryCancel
		globalClient.retired = true // abort even a flight that hasn't started yet
		globalClient.mu.Unlock()
	}
	if oldCancel != nil {
		oldCancel() // stop the outgoing client's detached discovery, if any
	}

	globalClient = &Client{
		k8sClient:     client,
		k8sConfig:     config,
		contextName:   contextName,
		manualURL:     manualURL,
		headers:       headers,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		mcpHTTPClient: newMCPHTTPClient(),
	}
}

// GetStatus returns the current Prometheus connection status.
func (c *Client) GetStatus() prom.Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var svc *prom.ServiceInfo
	if c.discoveryService != nil {
		cp := *c.discoveryService
		svc = &cp
	}

	return prom.Status{
		Available:   c.baseURL != "",
		Connected:   c.baseURL != "",
		Address:     c.baseURL,
		Service:     svc,
		ContextName: c.contextName,
	}
}

// EnsureConnected attempts to discover and connect to Prometheus if not
// already connected. Returns the base URL and base path, or an error.
func (c *Client) EnsureConnected(ctx context.Context) (string, string, error) {
	c.mu.RLock()
	base := c.baseURL
	bp := c.basePath
	c.mu.RUnlock()

	if base != "" {
		// Probe whatever we already have, building the pkg/prom.Client
		// on-demand. The cached client may be nil here for two reasons:
		// (a) a concurrent request hasn't yet primed getPromClient, or
		// (b) SetHeaders cleared the cache to force a header reload.
		// In both cases the connection itself is still valid; only the
		// cached client wrapper needs rebuilding. Pre-extraction probed
		// solely on base!="", so this preserves that behavior.
		if p := c.getPromClient(); p != nil {
			ok, reason := p.Probe(ctx)
			if ok {
				return base, bp, nil
			}
			log.Printf("[prometheus] cached connection to %s failed probe (reason=%s), rediscovering", base, reason)
			c.mu.Lock()
			c.baseURL = ""
			c.basePath = ""
			c.prom = nil
			c.mu.Unlock()
		}
	}

	return c.discoverShared(ctx)
}

// discoverShared runs discover() under a singleflight so a burst of concurrent
// EnsureConnected callers triggers exactly one discovery and all share its
// result. This is what stops parallel discoveries from fighting over the shared
// port-forward singleton.
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
	type result struct{ addr, basePath string }

	// Loop so a run superseded by a mid-flight config change (Reset /
	// SetManualURL / SetHeaders) rediscovers under the new generation instead of
	// returning a spurious failure to callers like opencost. Every supersession
	// bumps discoveryGen, so each retry keys a fresh flight and can't spin without
	// progress; the caller's own context bounds the loop.
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}

		c.mu.RLock()
		gen := c.discoveryGen
		key := fmt.Sprintf("%s#%d", c.contextName, gen)
		c.mu.RUnlock()

		ch := c.discoverySF.DoChan(key, func() (any, error) {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout)
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
			c.mu.Unlock()
			defer func() {
				c.mu.Lock()
				if c.discoveryCancelSeq == mySeq {
					c.discoveryCancel = nil
				}
				c.mu.Unlock()
			}()

			// Serialize across the process, but abandon the wait if this run is
			// cancelled (superseded) so it can't wedge a newer discovery.
			select {
			case discoveryGate <- struct{}{}:
				defer func() { <-discoveryGate }()
			case <-dctx.Done():
				return nil, dctx.Err()
			}

			// Another discovery may have connected this client while we waited on
			// the gate; if so, adopt its result instead of rediscovering.
			c.mu.RLock()
			addr, basePath := c.baseURL, c.basePath
			c.mu.RUnlock()
			if addr != "" {
				return result{addr, basePath}, nil
			}

			addr, basePath, derr := c.discover(dctx, gen)
			if derr != nil {
				return nil, derr
			}
			return result{addr, basePath}, nil
		})

		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case res := <-ch:
			// A retired client (replaced by Reinitialize) can never make progress,
			// so it must return the error rather than retry — retrying would spin
			// forever since retirement is permanent and gen never advances.
			c.mu.RLock()
			retired := c.retired
			c.mu.RUnlock()

			if res.Err != nil {
				// A supersession — a config change cancelled the detached flight —
				// is not the caller's failure. While the caller's own context is
				// live (and this client isn't retired), retry under the new
				// generation. context.Canceled here comes only from the flight's
				// detached context (Reset/Reinitialize), never the caller's; a
				// genuine hang surfaces as DeadlineExceeded and is returned.
				if ctx.Err() == nil && !retired && (errors.Is(res.Err, errDiscoverySuperseded) || errors.Is(res.Err, context.Canceled)) {
					continue
				}
				return "", "", res.Err
			}
			r := res.Val.(result)
			// Guard against a hollow success: between the run committing and our
			// return, a Reset can clear baseURL, or Reinitialize can retire this
			// client (leaving baseURL set) — either way the result belongs to a
			// superseded context. Retry under the new generation while our own
			// context is live; give up only if the caller itself is done or retired.
			if !c.connectionLive(r.addr) {
				if ctx.Err() == nil && !retired {
					continue
				}
				return "", "", errDiscoverySuperseded
			}
			return r.addr, r.basePath, nil
		}
	}
}

// connectionLive reports whether addr is the client's current, non-retired
// connection.
func (c *Client) connectionLive(addr string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL == addr && !c.retired
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
	tr := prom.NewHTTPTransport(c.baseURL, c.basePath, c.httpClient)
	tr.Headers = copyHeaders(c.headers)
	c.prom = prom.NewClient(tr)
	return c.prom
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
	tr := prom.NewHTTPTransport(c.baseURL, c.basePath, c.mcpHTTPClient)
	tr.Headers = copyHeaders(c.headers)
	return prom.NewClient(tr)
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
	c.mu.RLock()
	httpC := c.httpClient
	headers := copyHeaders(c.headers)
	c.mu.RUnlock()
	tr := prom.NewHTTPTransport(addr, "", httpC)
	tr.Headers = headers
	ok, reason := prom.NewClient(tr).Probe(ctx)
	if !ok && logReject {
		logProbeRejection(addr, reason, !discoveryDiagnosticsSuppressed(ctx))
	}
	return ok
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
