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
	manualURL        string       // --prometheus-url override
	headers          map[string]string

	// K8s clients for discovery
	k8sClient   kubernetes.Interface
	k8sConfig   *rest.Config
	contextName string

	// Shared HTTP client used when constructing the underlying pkg/prom.Client.
	httpClient *http.Client
}

// Global client instance
var (
	globalClient *Client
	clientMu     sync.RWMutex
)

// Initialize creates the global Prometheus client.
func Initialize(client kubernetes.Interface, config *rest.Config, contextName string) {
	clientMu.Lock()
	defer clientMu.Unlock()

	globalClient = &Client{
		k8sClient:   client,
		k8sConfig:   config,
		contextName: contextName,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// SetManualURL sets the --prometheus-url override on the global client.
func SetManualURL(rawURL string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	if globalClient != nil {
		globalClient.manualURL = strings.TrimRight(rawURL, "/")
	}
}

// SetHeaders sets HTTP headers attached to every Prometheus request on the
// global client. Pass nil or an empty map to clear.
func SetHeaders(h map[string]string) {
	clientMu.RLock()
	c := globalClient
	clientMu.RUnlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headers = copyHeaders(h)
	// Drop the cached prom.Client so the next request rebuilds its transport
	// with the new headers.
	c.prom = nil
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
		k8sClient:   client,
		k8sConfig:   config,
		contextName: contextName,
		manualURL:   manualURL,
		headers:     headers,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
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

// getPromClient returns a pkg/prom.Client pointed at the current baseURL/basePath,
// building (and caching) one if necessary. Callers must hold the read or
// write lock appropriately; see QueryRange/Query.
func (c *Client) getPromClient() *prom.Client {
	c.mu.RLock()
	if c.prom != nil {
		p := c.prom
		c.mu.RUnlock()
		return p
	}
	base, bp, httpC := c.baseURL, c.basePath, c.httpClient
	headers := copyHeaders(c.headers)
	c.mu.RUnlock()

	if base == "" {
		return nil
	}

	tr := prom.NewHTTPTransport(base, bp, httpC)
	tr.Headers = headers
	p := prom.NewClient(tr)
	c.mu.Lock()
	defer c.mu.Unlock()
	// Three things can have changed under us while the RLock was released:
	//   1. Another goroutine cached its own client (c.prom != nil) — use theirs.
	//   2. baseURL/basePath changed under us (concurrent markConnected/Reset).
	//      Our client points at the old address — discard it. Return nil so
	//      the caller can re-snapshot on its next attempt; the next request
	//      will rebuild against the new address.
	if c.prom != nil {
		return c.prom
	}
	if c.baseURL != base || c.basePath != bp {
		return nil
	}
	c.prom = p
	return p
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
		logProbeRejection(addr, reason)
	}
	return ok
}

// logProbeRejection records an appropriate log entry for each rejection
// reason. Auth failures get errorlog at error level (likely operator
// misconfiguration); empty instances get warning level (cluster state);
// other failures use stdlib log so they appear in the discovery audit
// trail without flooding errorlog.
func logProbeRejection(addr string, reason prom.ProbeReason) {
	switch reason {
	case prom.ProbeReasonAuthError:
		errorlog.Record("prometheus", "error",
			"endpoint %s rejected credentials (HTTP 401/403, check --prometheus-header)", addr)
	case prom.ProbeReasonEmptyInstance:
		errorlog.Record("prometheus", "warning",
			"endpoint %s has no active scrape targets (empty instance), skipping", addr)
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
