package prometheus

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
)

// RelayConfig sends every metrics request through a relay that holds the
// backend's credentials and enforces this cluster's filter (Radar Hub's
// OpenObserve relay). While a relay is active, discovery never runs and the
// configured Prometheus URL is kept but not used: when the relay cannot answer,
// metrics fail with the relay's error instead of reading another backend.
type RelayConfig struct {
	// Key identifies everything the connection depends on. Applying a config
	// with the active key only refreshes Source.
	Key string
	// Endpoint identifies the relay as the connection's address internally.
	Endpoint string
	// Address is what status shows in place of a URL.
	Address string
	Source  prom.MetricsSource
	// NewTransport builds a transport for one use; timeout 0 means none and
	// maxResponseBytes 0 means the transport's default.
	NewTransport func(timeout time.Duration, maxResponseBytes int64) prom.Transport
	// Health reports the relay's most recent failure, if any.
	Health func() (unavailable bool, message string)
}

type relayState struct {
	cfg RelayConfig

	mu     sync.Mutex
	source prom.MetricsSource
	// saved is the operator's Prometheus-side scope, set aside while the
	// relay is active and restored when it ends.
	saved prometheusSettings
	// lastProbe is when status last sent a health probe; guarded by mu.
	lastProbe time.Time
}

// relayHealthProbeInterval spaces the health probes status sends while the
// relay is unavailable; a variable for tests.
var relayHealthProbeInterval = 30 * time.Second

const relayHealthProbeTimeout = 10 * time.Second

type prometheusSettings struct {
	captured         bool
	scope            *prom.WorkloadMetricsScope
	scopeEverSet     bool
	beylaJobSelector string
}

func (cfg *RelayConfig) Validate() error {
	if cfg != nil && (cfg.NewTransport == nil || cfg.Endpoint == "" || cfg.Key == "") {
		return errors.New("metrics relay requires a key, an endpoint and a transport")
	}
	return nil
}

func (c *Client) EnableRelay(cfg RelayConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg.Source.MissingFamilies = slices.Clone(cfg.Source.MissingFamilies)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired {
		return errDiscoverySuperseded
	}
	if c.relay != nil && c.relay.cfg.Key == cfg.Key {
		c.relay.mu.Lock()
		c.relay.source = cfg.Source
		c.relay.mu.Unlock()
		return nil
	}
	st := &relayState{cfg: cfg, source: cfg.Source}
	if c.relay != nil {
		st.saved = c.relay.saved
	} else {
		st.saved = c.prometheusSettingsLocked()
	}
	c.applyRelayLocked(st)
	return nil
}

func (c *Client) DisableRelay() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.relay == nil || c.retired {
		return false
	}
	saved := c.relay.saved
	c.applyRelayLocked(nil)
	c.restorePrometheusSettingsLocked(saved)
	return true
}

func (c *Client) OperatorScopeDuringRelay() (prom.WorkloadMetricsScope, bool) {
	if c == nil {
		return prom.WorkloadMetricsScope{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.relay.operatorScope()
}

func (st *relayState) operatorScope() (scope prom.WorkloadMetricsScope, ok bool) {
	if st == nil {
		return prom.WorkloadMetricsScope{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.saved.scope == nil {
		return prom.WorkloadMetricsScope{}, false
	}
	scope = *st.saved.scope
	scope.ClusterLabels = maps.Clone(scope.ClusterLabels)
	return scope, true
}

func (c *Client) prometheusSettingsLocked() prometheusSettings {
	s := prometheusSettings{captured: true, scopeEverSet: c.workloadScopeEverSet, beylaJobSelector: c.beylaJobSelector}
	if c.workloadScope != nil {
		scope := *c.workloadScope
		scope.ClusterLabels = maps.Clone(scope.ClusterLabels)
		s.scope = &scope
	}
	return s
}

// applyRelayLocked switches the client into (st != nil) or out of relay mode.
// Either way the connection, its generation and every trust result derived from
// it are replaced: answers read through one backend say nothing about the
// other. Caller holds c.mu.
func (c *Client) applyRelayLocked(st *relayState) {
	c.relay = st
	c.dropConnectionLocked()
	c.discoveryGen++
	c.connectionEpoch++
	c.connectedIdentity = ""
	c.workloadPartition = nil
	c.cancelWorkloadAttributionsLocked()
	if c.discoveryCancel != nil {
		c.discoveryCancel()
	}
	if st != nil {
		c.workloadScope = relayWorkloadScope()
		c.baseURL = st.cfg.Endpoint
		c.connectedIdentity = relayIdentity(st)
		return
	}
	c.workloadScope = nil
}

func (c *Client) restorePrometheusSettingsLocked(saved prometheusSettings) {
	if !saved.captured {
		return
	}
	c.workloadScope = saved.scope
	c.workloadScopeEverSet = saved.scopeEverSet
	c.beylaJobSelector = saved.beylaJobSelector
}

// setAside records a Prometheus-side change made while the relay is active, so
// it applies when the relay ends instead of disturbing the relay's scope.
func (st *relayState) setAside(update func(*prometheusSettings)) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.saved.captured = true
	update(&st.saved)
}

// relayWorkloadScope asserts a single cluster rather than a label to read
// back: the relay forces the cluster matchers into every selector and drops
// returned series without them, and the matching label differs per metric
// family (collector data labels Pod metrics and kube_* metrics differently),
// so a single-label read-back would withhold views that are correctly scoped.
func relayWorkloadScope() *prom.WorkloadMetricsScope {
	return &prom.WorkloadMetricsScope{SingleCluster: true}
}

// relaySourceScopeNote says how an OpenObserve relay (Radar Hub's or the
// local one) keeps charts to this cluster, without a trailing period; "" when
// no OpenObserve relay is active. The scope state stays single-cluster
// asserted for older UIs, which show this note in its place.
// relayMissingThrottlingNote explains an empty throttling panel when the
// active OpenObserve source found no throttling data for this cluster.
func (c *Client) relayMissingThrottlingNote() string {
	c.mu.RLock()
	st := c.relay
	c.mu.RUnlock()
	if st == nil {
		return ""
	}
	st.mu.Lock()
	src := st.source
	st.mu.Unlock()
	if src.Kind != "openobserve" || !slices.Contains(src.MissingFamilies, "throttling") {
		return ""
	}
	if src.Dialect == "collector" {
		return "OpenObserve has no data for this chart: its collector drops CPU throttling by default. Keep container_cpu_cfs_throttled_periods_total and container_cpu_cfs_periods_total in its cadvisor scrape job to see it here."
	}
	return "OpenObserve has no data for this chart."
}

func (c *Client) relaySourceScopeNote() string {
	c.mu.RLock()
	st := c.relay
	c.mu.RUnlock()
	if st == nil {
		return ""
	}
	st.mu.Lock()
	src := st.source
	st.mu.Unlock()
	if src.Kind != "openobserve" {
		return ""
	}
	if src.MatchedBy == "name" {
		return "Scoped to this cluster through OpenObserve (matched by confirmed name)"
	}
	return "Scoped to this cluster through OpenObserve"
}

func relayIdentity(st *relayState) string {
	return "relay:" + st.cfg.Endpoint
}

// newTransportLocked is the one place a metrics transport is built, so relay
// mode reaches every query path: charts, MCP, the cluster-scope probes and
// rightsizing scans. Caller holds c.mu (read or write).
func (c *Client) newTransportLocked(addr, basePath string, httpClient *http.Client, maxResponseBytes int64) prom.Transport {
	return c.bindTransport(c.rawTransportLocked(addr, basePath, httpClient, maxResponseBytes))
}

func (c *Client) rawTransportLocked(addr, basePath string, httpClient *http.Client, maxResponseBytes int64) prom.Transport {
	if c.relay != nil {
		var timeout time.Duration
		if httpClient != nil {
			timeout = httpClient.Timeout
		}
		return c.relay.cfg.NewTransport(timeout, maxResponseBytes)
	}
	tr := prom.NewHTTPTransport(addr, basePath, httpClient)
	tr.Headers = copyHeaders(c.headers)
	tr.MaxResponseBytes = maxResponseBytes
	return tr
}

type memberTransport struct {
	prom.Transport
	lifetime context.Context
}

func (t memberTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	ctx, release := portforward.WithLifetime(ctx, t.lifetime)
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return t.Transport.Do(ctx, method, path, params)
}

func (c *Client) bindTransport(t prom.Transport) prom.Transport {
	return BindTransport(c.lifetime, t)
}

func BindTransport(lifetime context.Context, t prom.Transport) prom.Transport {
	if lifetime == nil {
		return t
	}
	return memberTransport{Transport: t, lifetime: lifetime}
}

// status returns the relay's source with its latest health.
func (st *relayState) status(lifetime context.Context) prom.MetricsSource {
	st.mu.Lock()
	src := st.source
	st.mu.Unlock()
	src.MissingFamilies = slices.Clone(src.MissingFamilies)
	if src.ClusterLabel != nil {
		label := *src.ClusterLabel
		src.ClusterLabel = &label
	}
	if st.cfg.Health != nil {
		src.Unavailable, src.Error = st.cfg.Health()
	}
	if src.Unavailable {
		st.probeHealth(lifetime)
	}
	return src
}

// probeHealth sends one cheap query through the relay, in the background and
// at most once per relayHealthProbeInterval. Health clears only when a query
// succeeds, and the UI stops querying while status says unavailable, so
// without this probe the state would never clear.
func (st *relayState) probeHealth(lifetime context.Context) {
	now := time.Now()
	st.mu.Lock()
	if !st.lastProbe.IsZero() && now.Sub(st.lastProbe) < relayHealthProbeInterval {
		st.mu.Unlock()
		return
	}
	st.lastProbe = now
	st.mu.Unlock()
	go func() {
		ctx, release := portforward.WithLifetime(context.Background(), lifetime)
		defer release()
		ctx, cancel := context.WithTimeout(ctx, relayHealthProbeTimeout)
		defer cancel()
		t := st.cfg.NewTransport(relayHealthProbeTimeout, 0)
		// The OpenObserve transports scope vector(1) to the cluster; the
		// answer only matters for the health it records.
		_, _ = t.Do(ctx, http.MethodGet, "/api/v1/query", url.Values{"query": {"vector(1)"}})
	}()
}
