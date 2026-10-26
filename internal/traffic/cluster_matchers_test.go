package traffic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func setClusterMatchers(t *testing.T, matchers string) {
	t.Helper()
	SetClusterMatchers(matchers)
	t.Cleanup(func() { SetClusterMatchers("") })
}

// recordingProm answers every query with one Caretta-shaped sample and keeps the
// queries it was sent.
func recordingProm(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("query"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "vector", "result": []any{map[string]any{
				"metric": map[string]string{"client_name": "frontend", "server_name": "backend", "server_port": "80"},
				"value":  []any{1.0, "3"},
			}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

func TestWithClusterMatchers(t *testing.T) {
	if got := withClusterMatchers(`job="x"`); got != `job="x"` {
		t.Errorf("unset matchers changed the selector: %q", got)
	}
	setClusterMatchers(t, `cluster="a"`)
	if got := withClusterMatchers(`job="x"`); got != `job="x",cluster="a"` {
		t.Errorf("got %q", got)
	}
	if got := withClusterMatchers(""); got != `cluster="a"` {
		t.Errorf("empty selector: got %q", got)
	}
}

// A backend shared by several clusters holds every cluster's Caretta links; each
// query has to keep to this cluster's series or the graph merges clusters.
func TestCarettaQueriesCarryClusterMatchers(t *testing.T) {
	setClusterMatchers(t, `cluster="a"`)
	srv, sent := recordingProm(t)
	c := &CarettaSource{
		httpClient:      &http.Client{Timeout: 2 * time.Second},
		prometheusAddr:  srv.URL,
		isConnected:     true,
		backendVerified: true,
	}

	c.mu.Lock()
	c.carettaMetricsPresentLocked(t.Context(), srv.URL)
	c.mu.Unlock()
	for _, opts := range []FlowOptions{{}, {Namespace: "shop"}} {
		if _, err := c.GetFlows(t.Context(), opts); err != nil {
			t.Fatalf("GetFlows(%+v): %v", opts, err)
		}
	}

	queries := sent()
	flowQueries := 0
	for _, q := range queries {
		if strings.HasPrefix(q, "caretta_links_observed") {
			flowQueries++
		}
	}
	if flowQueries != 2 {
		t.Fatalf("expected one flow query per GetFlows call, got %d in %q", flowQueries, queries)
	}
	for _, q := range queries {
		if want := strings.Count(q, "{"); strings.Count(q, `cluster="a"`) != want {
			t.Errorf("every selector must carry the cluster matcher: %s", q)
		}
	}
}

func TestBeylaQueriesCarryClusterMatchers(t *testing.T) {
	setClusterMatchers(t, `cluster="a"`)
	w := beylaWindow(0)
	for name, q := range map[string]string{
		"l4":         beylaRateQuery(beylaL4GroupBy, beylaFlowMetric, "shop", beylaL4DirectionFilter, w),
		"l7 rate":    beylaL7RateQuery("shop", w),
		"l7 latency": beylaL7LatencyQuery("shop", w),
		"l7 errors":  beylaL7ErrorQuery("", w),
	} {
		if want := strings.Count(q, "{"); strings.Count(q, `cluster="a"`) != want {
			t.Errorf("%s: every selector must carry the cluster matcher: %s", name, q)
		}
	}
}

func TestBeylaQueriesUnchangedWithoutClusterMatchers(t *testing.T) {
	q := beylaL7RateQuery("", beylaWindow(0))
	if !strings.Contains(q, "{"+defaultBeylaJobSelector+"}") {
		t.Errorf("selector changed with no cluster matchers configured: %s", q)
	}
}

// The labels describe the backend Radar was started against; a different
// connection may label clusters differently or not at all.
func TestSetMetricsConfigClearsClusterMatchers(t *testing.T) {
	t.Cleanup(func() { SetMetricsConfig("", nil) })
	SetMetricsConfig("http://prom:9090", map[string]string{"X-Scope-OrgID": "a"})
	setClusterMatchers(t, `cluster="a"`)
	SetMetricsConfig("http://other:9090", map[string]string{"X-Scope-OrgID": "a"})
	if got := clusterMatchers(); got != "" {
		t.Errorf("cluster matchers survived a connection change: %q", got)
	}
}

// Local mode re-applies the same connection on startup and on a namespace
// rescope; that is not a change of backend.
func TestSetMetricsConfigKeepsClusterMatchersForSameConnection(t *testing.T) {
	t.Cleanup(func() { SetMetricsConfig("", nil) })
	SetMetricsConfig("http://prom:9090", map[string]string{"X-Scope-OrgID": "a"})
	setClusterMatchers(t, `cluster="a"`)
	SetMetricsConfig("http://prom:9090/", map[string]string{"X-Scope-OrgID": "a"})
	if got := clusterMatchers(); got != `cluster="a"` {
		t.Errorf("re-applying the same connection cleared the matchers: %q", got)
	}
}

// Startup runs the traffic init callback before and after connecting, on the
// same cluster; only a context switch to another cluster drops the matchers.
func TestReinitializeKeepsClusterMatchersUntilContextChanges(t *testing.T) {
	setClusterMatchers(t, `cluster="a"`)
	t.Cleanup(Reset)
	cfg := &rest.Config{Host: "https://a.example"}

	for range 2 {
		if err := ReinitializeWithConfig(fake.NewSimpleClientset(), cfg, "ctx-a"); err != nil {
			t.Fatal(err)
		}
		if got := clusterMatchers(); got != `cluster="a"` {
			t.Fatalf("matchers dropped on re-initializing the same cluster: %q", got)
		}
	}
	if err := ReinitializeWithConfig(fake.NewSimpleClientset(), &rest.Config{Host: "https://b.example"}, "ctx-b"); err != nil {
		t.Fatal(err)
	}
	if got := clusterMatchers(); got != "" {
		t.Errorf("matchers survived a switch to another cluster: %q", got)
	}
}
