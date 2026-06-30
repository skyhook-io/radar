package prometheus

import (
	"context"
	"errors"
	"log"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/errorlog"
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
	discovered       bool
	discoveryService *prom.ServiceInfo // discovered service info for port-forward
	manualURL        string            // --prometheus-url override
	headers          map[string]string

	// K8s clients for discovery
	k8sClient   kubernetes.Interface
	k8sConfig   *rest.Config
	contextName string

	// Shared HTTP client used when constructing the underlying pkg/prom.Client.
	httpClient *http.Client

	// Dedicated HTTP client for the MCP path
	mcpHTTPClient *http.Client
}

// Global client instance
var (
	globalClient *Client
	clientMu     sync.RWMutex
)

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
		globalClient.discovered = false
		globalClient.discoveryService = nil
		globalClient.mu.Unlock()
	}
}

// Reinitialize recreates the client with new K8s connection info.
func Reinitialize(client kubernetes.Interface, config *rest.Config, contextName string) {
	clientMu.Lock()
	defer clientMu.Unlock()

	manualURL := ""
	var headers map[string]string
	if globalClient != nil {
		// SetManualURL / SetHeaders write these under the per-client mutex
		// after dropping clientMu, so reading without c.mu here would race
		// even though we hold clientMu exclusively. copyHeaders also detaches
		// the map from the old client so a late mutation can't bleed through.
		globalClient.mu.RLock()
		manualURL = globalClient.manualURL
		headers = copyHeaders(globalClient.headers)
		globalClient.mu.RUnlock()
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
			c.discovered = false
			c.mu.Unlock()
		}
	}

	return c.discover(ctx)
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
	c.mu.RLock()
	httpC := c.httpClient
	headers := copyHeaders(c.headers)
	c.mu.RUnlock()
	tr := prom.NewHTTPTransport(addr, "", httpC)
	tr.Headers = headers
	ok, reason := prom.NewClient(tr).Probe(ctx)
	if !ok {
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
