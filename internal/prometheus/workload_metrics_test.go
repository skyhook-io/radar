package prometheus

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/rest"
)

type workloadQueryFunc func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error)

func (f workloadQueryFunc) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) (*prom.QueryResult, error) {
	return f(ctx, q, start, end, step)
}

func TestWorkloadPanelStates(t *testing.T) {
	end := time.Unix(10000, 0)
	for _, tc := range []struct {
		name   string
		points []prom.DataPoint
		err    error
		state  string
	}{
		{"missing", nil, nil, "unavailable"},
		{"zero is measured", []prom.DataPoint{{Timestamp: end.Unix(), Value: 0}}, nil, "available"},
		{"undefined ratio", []prom.DataPoint{{Timestamp: end.Unix(), Value: math.NaN()}}, nil, "unavailable"},
		{"historical only", []prom.DataPoint{{Timestamp: end.Add(-time.Hour).Unix(), Value: 2}}, nil, "stale"},
		{"idle tail is current evaluation", []prom.DataPoint{{Timestamp: end.Add(-time.Hour).Unix(), Value: 2}, {Timestamp: end.Unix(), Value: math.NaN()}}, nil, "available"},
		{"failure is not missing", nil, errors.New("query failed"), "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := workloadQueryFunc(func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error) {
				return &prom.QueryResult{Series: []prom.Series{{Labels: map[string]string{"pod": "api-0", "http_route": "/private"}, DataPoints: tc.points}}}, tc.err
			})
			panel := queryWorkloadPanel(context.Background(), client, "query", nil, "requests/s", end.Add(-time.Hour), end, time.Minute, false)
			if panel.State != tc.state {
				t.Fatalf("state %s, want %s", panel.State, tc.state)
			}
			for _, s := range panel.Series {
				if len(s.Labels) != 0 {
					t.Fatalf("raw labels escaped: %+v", s.Labels)
				}
			}
		})
	}
}

func TestWorkloadCoverageWithholdsMismatchedSamples(t *testing.T) {
	panel := workloadMetricPanel{State: "available", Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 0.2}, {Timestamp: 2, Value: 0.3}}}}}
	coverage := workloadMetricPanel{State: "available", Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 1}, {Timestamp: 2, Value: 0.5}}}}}
	got := withMetricCoverage(panel, coverage, "partial histograms")
	if got.State != "partial" || got.Series[0].DataPoints[0].Value != 0.2 || !math.IsNaN(got.Series[0].DataPoints[1].Value) {
		t.Fatalf("coverage not enforced: %+v", got)
	}
	got = withMetricCoverage(panel, workloadMetricPanel{State: "error"}, "partial histograms")
	if got.State != "error" || len(got.Series) != 0 {
		t.Fatal("coverage query failure must withhold metric")
	}
}

func TestObservedPodCoverage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts []prom.DataPoint
		state  string
		want   string
	}{
		{"complete", []prom.DataPoint{{Timestamp: 1, Value: 2}, {Timestamp: 2, Value: 2}}, "available", "available"},
		{"one reporting", []prom.DataPoint{{Timestamp: 1, Value: 1}, {Timestamp: 2, Value: 1}}, "available", "partial"},
		{"historical gap", []prom.DataPoint{{Timestamp: 2, Value: 2}}, "available", "partial"},
		{"coverage failed", nil, "available", "partial"},
		{"stale stays stale", nil, "stale", "stale"},
		{"error stays error", nil, "error", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panel := workloadMetricPanel{State: tc.state, Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 0}, {Timestamp: 2, Value: 7}}}}}
			got := withObservedPodCoverage(panel, workloadMetricPanel{Series: []prom.Series{{DataPoints: tc.counts}}}, 2)
			if got.State != tc.want || got.Series[0].DataPoints[1].Value != 7 || got.Series[0].DataPoints[0].Value != 0 {
				t.Fatalf("coverage must preserve measured values: %+v", got)
			}
			if tc.want == "partial" && !strings.Contains(got.Reason, "reporting Pods only") {
				t.Fatal("partial data must explain its population")
			}
		})
	}
}

func TestIdleSamplesAreNotCoverageFailures(t *testing.T) {
	panel := workloadMetricPanel{State: "available", Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 10}, {Timestamp: 2, Value: math.NaN()}}}}}
	coverage := workloadMetricPanel{State: "available", Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 1}, {Timestamp: 2, Value: math.NaN()}}}}}
	got := withMetricCoverage(panel, coverage, "mismatch")
	if got.State != "available" || got.Reason != "" || !math.IsNaN(got.Series[0].DataPoints[1].Value) {
		t.Fatalf("idle ratio mislabeled: %+v", got)
	}
}

func TestMetricCoveragePreservesStaleness(t *testing.T) {
	panel := workloadMetricPanel{State: "stale", Reason: "Only historical samples are available.", Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 1, Value: 10}}}}}
	got := withMetricCoverage(panel, workloadMetricPanel{State: "unavailable"}, "Histogram coverage incomplete.")
	if got.State != "stale" || !strings.Contains(got.Reason, "historical") || !strings.Contains(got.Reason, "coverage incomplete") || !math.IsNaN(got.Series[0].DataPoints[0].Value) {
		t.Fatalf("lost staleness or coverage evidence: %+v", got)
	}
}

func TestWorkloadSetupRequiredResponse(t *testing.T) {
	previous := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	Initialize(nil, nil, "setup-test")
	SetAuthGate(nil)
	router := chi.NewRouter()
	RegisterRoutes(router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/prometheus/workload/Deployment/shop/api", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"setupRequired":true`) {
		t.Fatalf("unexpected setup response: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkloadCollectionKeepsObserversSeparate(t *testing.T) {
	end := time.Unix(10000, 0)
	client := workloadQueryFunc(func(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
		value := 7.0
		if strings.Contains(query, "http_server_request") {
			value = 11
		}
		if strings.Contains(query, `le="+Inf"`) || strings.Contains(query, `[1-5][0-9][0-9]`) {
			value = 1
		}
		return &prom.QueryResult{Series: []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: end.Unix(), Value: value}}}}}, nil
	})
	scope := PodScope{CurrentPods: []string{"api-0"}, CurrentTotal: 2, Selection: prom.SelectPods("shop", []string{"api-0"})}
	for _, source := range []prom.RequestSource{"", prom.RequestSourceBeyla} {
		got := collectWorkloadMetrics(context.Background(), client, scope, prom.WorkloadMetricsScope{SingleCluster: true}, "", source, end.Add(-time.Hour), end, time.Minute)
		wantSource, wantValue := prom.RequestSourceIstio, 7.0
		if source == prom.RequestSourceBeyla {
			wantSource, wantValue = source, 11
		}
		if got.Source != wantSource || got.Panels["requests"].Series[0].DataPoints[0].Value != wantValue {
			t.Fatalf("observer mixed or incorrect priority: %+v", got)
		}
		if got.State != "partial" || got.Pods != 1 || got.PodsTotal != 2 {
			t.Fatalf("lost population cap: %+v", got)
		}
	}
}

func TestWorkloadMetricsGateBeforeCacheOrPrometheus(t *testing.T) {
	previous := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { SetAuthGate(nil) })
	for _, denied := range []string{"deployments", "pods"} {
		SetAuthGate(func(_ *http.Request, _, resource, namespace, verb string) bool {
			if namespace != "shop" {
				t.Errorf("wrong namespace: %s", namespace)
			}
			return resource != denied
		})
		router := chi.NewRouter()
		RegisterRoutes(router)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/prometheus/workload/Deployment/shop/api", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("denied %s returned %d", denied, w.Code)
		}
	}
}

func TestWorkloadMetricsDisconnected(t *testing.T) {
	previous := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected})
	router := chi.NewRouter()
	RegisterRoutes(router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/prometheus/workload/Deployment/shop/api", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("disconnected response: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkloadScopeDoesNotFollowConnectionChanges(t *testing.T) {
	for _, change := range []struct {
		name string
		run  func()
	}{
		{"url", func() { SetManualURL("http://different:9090") }},
		{"headers", func() { SetHeaders(map[string]string{"X-Scope-OrgID": "other"}) }},
		{"context", func() { Reinitialize(nil, nil, "other") }},
	} {
		t.Run(change.name, func(t *testing.T) {
			Initialize(nil, nil, "original")
			if err := SetWorkloadMetricsScope(prom.WorkloadMetricsScope{SingleCluster: true}, ""); err != nil {
				t.Fatal(err)
			}
			change.run()
			if _, _, ok := GetClient().workloadMetricsConfig(); ok {
				t.Fatal("scope followed connection change")
			}
		})
	}
}

func TestWorkloadScopeSurvivesUnchangedStartupAndSettings(t *testing.T) {
	config := &rest.Config{Host: "https://cluster-a"}
	Initialize(nil, config, "original")
	SetManualURL("http://metrics:9090")
	SetHeaders(map[string]string{"X-Scope-OrgID": "tenant-a"})
	if err := SetWorkloadMetricsScope(prom.WorkloadMetricsScope{SingleCluster: true}, ""); err != nil {
		t.Fatal(err)
	}
	Reinitialize(nil, rest.CopyConfig(config), "original")
	SetManualURL("http://metrics:9090/")
	SetHeaders(map[string]string{"X-Scope-OrgID": "tenant-a"})
	if _, _, ok := GetClient().workloadMetricsConfig(); !ok {
		t.Fatal("startup or unchanged settings discarded scope")
	}
	Reinitialize(nil, &rest.Config{Host: "https://cluster-b"}, "original")
	if _, _, ok := GetClient().workloadMetricsConfig(); ok {
		t.Fatal("same display name carried scope to another cluster")
	}
}
