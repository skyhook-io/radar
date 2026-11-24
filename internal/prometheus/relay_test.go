package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/prom"
)

type fakeRelayTransport struct {
	mu       sync.Mutex
	paths    []string
	timeouts []time.Duration
	err      error
}

func (f *fakeRelayTransport) build(timeout time.Duration, _ int64) prom.Transport {
	f.mu.Lock()
	f.timeouts = append(f.timeouts, timeout)
	f.mu.Unlock()
	return relayTransportFunc{f}
}

type relayTransportFunc struct{ f *fakeRelayTransport }

func (t relayTransportFunc) Address() string { return "fake relay" }
func (t relayTransportFunc) Do(_ context.Context, _, path string, _ url.Values) ([]byte, error) {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	t.f.paths = append(t.f.paths, path)
	if t.f.err != nil {
		return nil, t.f.err
	}
	return []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"radar_cluster_uid":"uid-1"},"value":[1,"1"]}]}}`), nil
}

func (f *fakeRelayTransport) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

// withRelayTestClient installs a fresh global client pointed at a Prometheus
// that counts every request it receives.
func withRelayTestClient(t *testing.T) (*atomic.Int32, string) {
	t.Helper()
	var promHits atomic.Int32
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		promHits.Add(1)
		_, _ = w.Write([]byte(healthyProbeBody))
	}))
	t.Cleanup(promSrv.Close)

	clientMu.Lock()
	saved, savedRelay := globalClient, activeRelay
	activeRelay = nil
	clientMu.Unlock()
	t.Cleanup(func() {
		clientMu.Lock()
		globalClient, activeRelay = saved, savedRelay
		clientMu.Unlock()
	})
	Initialize(fake.NewClientset(), nil, "relay-test")
	return &promHits, promSrv.URL
}

func testRelayConfig(f *fakeRelayTransport, health func() (bool, string)) RelayConfig {
	return RelayConfig{
		Key:          "key-1",
		Endpoint:     "https://hub.example/api/agent/metrics/openobserve",
		Address:      "Radar Hub relay",
		Source:       prom.MetricsSource{Kind: "openobserve", Via: "hub", MatchedBy: "id", ClusterLabel: &prom.MetricsSourceLabel{Name: "radar_cluster_uid", Value: "uid-1"}},
		NewTransport: f.build,
		Health:       health,
	}
}

func TestRelayServesEveryPathAndNeverReachesPrometheus(t *testing.T) {
	promHits, promURL := withRelayTestClient(t)
	operatorScope := prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "prod"}}
	SetManualURL(promURL)
	if err := SetWorkloadMetricsScope(operatorScope, "beyla"); err != nil {
		t.Fatal(err)
	}

	f := &fakeRelayTransport{}
	if err := EnableRelay(testRelayConfig(f, nil)); err != nil {
		t.Fatal(err)
	}
	c := GetClient()
	scope, _, ok := c.workloadMetricsConfig()
	if !ok || !scope.SingleCluster || len(scope.ClusterLabels) != 0 {
		t.Fatalf("relay scope not bound: %+v ok=%v", scope, ok)
	}

	ctx := t.Context()
	if _, err := c.Query(ctx, "up"); err != nil {
		t.Fatalf("query through relay: %v", err)
	}
	if mcp := c.PromForMCP(); mcp == nil {
		t.Fatal("MCP client missing in relay mode")
	} else if _, err := mcp.Query(ctx, "up"); err != nil {
		t.Fatalf("MCP query through relay: %v", err)
	}
	q, err := c.newScanQuerier(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Query(ctx, "up"); err != nil {
		t.Fatalf("scan query through relay: %v", err)
	}
	// Reset (a context switch) reconnects to the relay, not by discovery.
	Reset()
	if _, err := c.Query(ctx, "up"); err != nil {
		t.Fatalf("query after reset: %v", err)
	}
	if f.calls() != 4 {
		t.Fatalf("relay calls = %d, want 4", f.calls())
	}
	if n := promHits.Load(); n != 0 {
		t.Fatalf("Prometheus received %d requests while the relay was active", n)
	}

	// A configuration change while relayed is kept for later, not applied.
	SetHeaders(map[string]string{"X-Scope-OrgID": "tenant"})
	if !RelayActive() || c.GetStatus().Address != "Radar Hub relay" {
		t.Fatal("configuration change replaced the relay")
	}
	SetHeaders(nil)

	if !DisableRelay() {
		t.Fatal("DisableRelay reported no relay")
	}
	if _, err := c.Query(ctx, "up"); err != nil {
		t.Fatalf("query after relay ended: %v", err)
	}
	if promHits.Load() == 0 {
		t.Fatal("Prometheus not used after the relay ended")
	}
	if st := c.GetStatus(); st.Source != nil || st.Address != promURL {
		t.Fatalf("status after relay: %+v", st)
	}
	scope, selector, ok := c.workloadMetricsConfig()
	if ok {
		t.Fatalf("operator scope should have been cleared by the header change made while relayed, got %+v", scope)
	}
	if selector != "" || c.beylaJobSelector != "beyla" {
		t.Fatalf("beyla selector not restored: %q", c.beylaJobSelector)
	}
}

func TestRelayRestoresOperatorScope(t *testing.T) {
	_, promURL := withRelayTestClient(t)
	SetManualURL(promURL)
	operatorScope := prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "prod"}}
	if err := SetWorkloadMetricsScope(operatorScope, ""); err != nil {
		t.Fatal(err)
	}
	if err := EnableRelay(testRelayConfig(&fakeRelayTransport{}, nil)); err != nil {
		t.Fatal(err)
	}
	DisableRelay()
	scope, _, ok := GetClient().workloadMetricsConfig()
	if !ok || scope.ClusterLabels["cluster"] != "prod" {
		t.Fatalf("operator scope not restored: %+v ok=%v", scope, ok)
	}
}

func TestRelayUnavailableIsReportedWithoutFallback(t *testing.T) {
	promHits, promURL := withRelayTestClient(t)
	SetManualURL(promURL)
	unavailable := errors.New("OpenObserve unavailable: Radar Hub answered 502")
	f := &fakeRelayTransport{err: unavailable}
	var health atomic.Value
	health.Store("")
	err := EnableRelay(testRelayConfig(f, func() (bool, string) {
		msg := health.Load().(string)
		return msg != "", msg
	}))
	if err != nil {
		t.Fatal(err)
	}
	c := GetClient()
	_, err = c.Query(t.Context(), "up")
	if err == nil || !strings.HasPrefix(err.Error(), "OpenObserve unavailable") {
		t.Fatalf("query error = %v", err)
	}
	health.Store(unavailable.Error())
	for range 3 {
		_, _ = c.Query(t.Context(), "up")
	}
	if n := promHits.Load(); n != 0 {
		t.Fatalf("fell back to Prometheus: %d requests", n)
	}
	st := c.GetStatus()
	if st.Source == nil || !st.Source.Unavailable || !strings.HasPrefix(st.Source.Error, "OpenObserve unavailable") || st.Error != st.Source.Error {
		t.Fatalf("status = %+v source=%+v", st, st.Source)
	}
	if st.Address != "Radar Hub relay" || strings.Contains(st.Address, "hub.example") {
		t.Fatalf("status address = %q", st.Address)
	}
	raw, _ := json.Marshal(st)
	if !strings.Contains(string(raw), `"source":{"kind":"openobserve","via":"hub","matchedBy":"id","clusterLabel":{"name":"radar_cluster_uid","value":"uid-1"}`) ||
		!strings.Contains(string(raw), `"unavailable":true`) {
		t.Fatalf("status JSON = %s", raw)
	}
}

func TestRelaySameKeyRefreshesSourceOnly(t *testing.T) {
	withRelayTestClient(t)
	f := &fakeRelayTransport{}
	cfg := testRelayConfig(f, nil)
	if err := EnableRelay(cfg); err != nil {
		t.Fatal(err)
	}
	c := GetClient()
	gen := c.DiscoveryGeneration()
	cfg.Source.Warning = "name match"
	if err := EnableRelay(cfg); err != nil {
		t.Fatal(err)
	}
	if c.DiscoveryGeneration() != gen {
		t.Fatal("same relay reconnected")
	}
	if st := c.GetStatus(); st.Source.Warning != "name match" {
		t.Fatalf("source not refreshed: %+v", st.Source)
	}
	cfg.Key = "key-2"
	if err := EnableRelay(cfg); err != nil {
		t.Fatal(err)
	}
	if c.DiscoveryGeneration() == gen {
		t.Fatal("changed relay kept the old connection generation")
	}
}

func TestRelaySurvivesReinitialize(t *testing.T) {
	withRelayTestClient(t)
	if err := EnableRelay(testRelayConfig(&fakeRelayTransport{}, nil)); err != nil {
		t.Fatal(err)
	}
	Reinitialize(fake.NewClientset(), nil, "relay-test")
	st := GetClient().GetStatus()
	if st.Source == nil || st.Address != "Radar Hub relay" {
		t.Fatalf("relay lost on reinitialize: %+v", st)
	}
	if _, _, ok := GetClient().workloadMetricsConfig(); !ok {
		t.Fatal("relay scope lost on reinitialize")
	}
}

func TestRelayUnavailableClearsThroughHealthProbe(t *testing.T) {
	withRelayTestClient(t)
	saved := relayHealthProbeInterval
	relayHealthProbeInterval = 200 * time.Millisecond
	t.Cleanup(func() { relayHealthProbeInterval = saved })

	// Health is set by the transports, as the real ones do: failures mark
	// the relay unavailable and any success clears it.
	var mu sync.Mutex
	down, message := true, "OpenObserve unavailable: Radar Hub answered 502"
	var probes []string
	build := func(time.Duration, int64) prom.Transport {
		return probeTransport(func(params url.Values) error {
			mu.Lock()
			defer mu.Unlock()
			probes = append(probes, params.Get("query"))
			if down {
				return errors.New(message)
			}
			message = ""
			return nil
		})
	}
	cfg := testRelayConfig(&fakeRelayTransport{}, func() (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		return message != "", message
	})
	cfg.NewTransport = build
	if err := EnableRelay(cfg); err != nil {
		t.Fatal(err)
	}
	probeCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(probes)
	}
	c := GetClient()

	if st := c.GetStatus(); !st.Source.Unavailable {
		t.Fatalf("status = %+v", st.Source)
	}
	waitFor(t, "first probe", func() bool { return probeCount() == 1 })

	// The upstream recovers; no chart query runs.
	mu.Lock()
	down = false
	mu.Unlock()
	for range 5 {
		if st := c.GetStatus(); !st.Source.Unavailable {
			t.Fatal("cleared before the next probe")
		}
	}
	if n := probeCount(); n != 1 {
		t.Fatalf("probes within the interval = %d", n)
	}

	time.Sleep(relayHealthProbeInterval)
	_ = c.GetStatus()
	waitFor(t, "status available", func() bool { return !c.GetStatus().Source.Unavailable })
	mu.Lock()
	defer mu.Unlock()
	if len(probes) != 2 || probes[1] != "vector(1)" {
		t.Fatalf("probes = %v", probes)
	}
}

type probeTransport func(url.Values) error

func (probeTransport) Address() string { return "probe" }
func (p probeTransport) Do(_ context.Context, _, path string, params url.Values) ([]byte, error) {
	if path != "/api/v1/query" {
		return nil, errors.New("unexpected path " + path)
	}
	if err := p(params); err != nil {
		return nil, err
	}
	return []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`), nil
}

func TestRelaySavedScopeDoesNotFollowAnotherContext(t *testing.T) {
	withRelayTestClient(t)
	launch := prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "ctx-a"}}
	if err := SetWorkloadMetricsScope(launch, `job="beyla"`); err != nil {
		t.Fatal(err)
	}
	if err := EnableRelay(testRelayConfig(&fakeRelayTransport{}, nil)); err != nil {
		t.Fatal(err)
	}
	Retire()
	Reinitialize(fake.NewClientset(), nil, "ctx-b")
	DisableRelay()
	c := GetClient()
	if scope, _, ok := c.workloadMetricsConfig(); ok {
		t.Fatalf("context A's scope applied to context B: %+v", scope)
	}
	c.mu.RLock()
	beyla, everSet := c.beylaJobSelector, c.workloadScopeEverSet
	c.mu.RUnlock()
	if beyla != "" {
		t.Fatalf("context A's Beyla selector applied to context B: %q", beyla)
	}
	if !everSet {
		t.Fatal("scopeEverSet lost")
	}
}

func TestRelaySavedScopeClearedOnReinitializeToAnotherContext(t *testing.T) {
	withRelayTestClient(t)
	launch := prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "ctx-a"}}
	if err := SetWorkloadMetricsScope(launch, ""); err != nil {
		t.Fatal(err)
	}
	if err := EnableRelay(testRelayConfig(&fakeRelayTransport{}, nil)); err != nil {
		t.Fatal(err)
	}
	Reinitialize(fake.NewClientset(), nil, "ctx-b")
	DisableRelay()
	if scope, _, ok := GetClient().workloadMetricsConfig(); ok {
		t.Fatalf("context A's scope applied to context B: %+v", scope)
	}
}

func TestRelaySavedScopeKeptOnSameContextReinitialize(t *testing.T) {
	withRelayTestClient(t)
	launch := prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "ctx-a"}}
	if err := SetWorkloadMetricsScope(launch, `job="beyla"`); err != nil {
		t.Fatal(err)
	}
	if err := EnableRelay(testRelayConfig(&fakeRelayTransport{}, nil)); err != nil {
		t.Fatal(err)
	}
	Reinitialize(fake.NewClientset(), nil, "relay-test")
	DisableRelay()
	c := GetClient()
	scope, _, ok := c.workloadMetricsConfig()
	if !ok || scope.ClusterLabels["cluster"] != "ctx-a" {
		t.Fatalf("same-context scope lost: %+v ok=%v", scope, ok)
	}
	c.mu.RLock()
	beyla := c.beylaJobSelector
	c.mu.RUnlock()
	if beyla != `job="beyla"` {
		t.Fatalf("beyla = %q", beyla)
	}
}

// TestRelayScopeNoteNamesOpenObserve: through an OpenObserve relay the chart
// scope keeps the single-cluster state older UIs know, with a note that says
// OpenObserve scopes the charts rather than that an operator declared it.
func TestRelayScopeNoteNamesOpenObserve(t *testing.T) {
	withRelayTestClient(t)
	cfg := testRelayConfig(&fakeRelayTransport{}, nil)
	if err := EnableRelay(cfg); err != nil {
		t.Fatal(err)
	}
	req := ViewScopeRequest{View: "test", Metrics: []ViewMetric{{Name: "container_cpu_usage_seconds_total"}}}
	scope, err := GetClient().ResolveViewScope(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scope.State != ViewScopeSingleClusterAsserted || scope.Note != "Scoped to this cluster through OpenObserve." {
		t.Fatalf("scope = %s %q", scope.State, scope.Note)
	}
	cfg.Source.MatchedBy = "name"
	if err := EnableRelay(cfg); err != nil {
		t.Fatal(err)
	}
	scope, _ = GetClient().ResolveViewScope(context.Background(), req, nil)
	if scope.Note != "Scoped to this cluster through OpenObserve (matched by confirmed name)." {
		t.Fatalf("name-matched note = %q", scope.Note)
	}
	DisableRelay()
	if note := GetClient().relaySourceScopeNote(); note != "" {
		t.Fatalf("note without a relay = %q", note)
	}
}
