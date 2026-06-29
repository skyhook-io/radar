package opencost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

// scriptedProm returns a prom.Client backed by a httptest server that
// serves canned responses keyed by a predicate applied to the PromQL query.
// Predicates are tried in order; the first matching one wins.
func scriptedProm(t *testing.T, cases []scriptedCase) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		for _, c := range cases {
			if c.matches(q) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(c.body))
				return
			}
		}
		// Default: success with empty result.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

type scriptedCase struct {
	contains string
	body     string
}

func (c scriptedCase) matches(q string) bool {
	return strings.Contains(q, c.contains)
}

// vectorBody helps build a minimal Prometheus vector response.
func vectorBody(samples map[string]float64) string {
	type result struct {
		Metric map[string]string `json:"metric"`
		Value  []interface{}     `json:"value"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []result `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "vector"
	for ns, v := range samples {
		body.Data.Result = append(body.Data.Result, result{
			Metric: map[string]string{"namespace": ns},
			Value:  []interface{}{1700000000.0, formatFloat(v)},
		})
	}
	b, _ := json.Marshal(body)
	return string(b)
}

func scalarBody(v float64) string {
	type result struct {
		Metric map[string]string `json:"metric"`
		Value  []interface{}     `json:"value"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []result `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "vector"
	body.Data.Result = []result{{Metric: map[string]string{}, Value: []interface{}{1700000000.0, formatFloat(v)}}}
	b, _ := json.Marshal(body)
	return string(b)
}

// formatFloat renders a value the way Prometheus does — a numeric string
// with enough precision to round-trip the test inputs exactly.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func TestComputeCostSummary_HappyPath(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"checkout": 2.0, "payments": 1.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"checkout": 3.0, "payments": 0.5})},
		{contains: "container_cpu_usage_seconds_total", body: vectorBody(map[string]float64{"checkout": 0.8, "payments": 0.6})},
		{contains: "container_memory_working_set_bytes", body: vectorBody(map[string]float64{"checkout": 1.2, "payments": 0.25})},
		{contains: "pv_hourly_cost", body: vectorBody(map[string]float64{"checkout": 0.05})},
		{contains: "node_total_hourly_cost", body: scalarBody(8.0)}, // exceeds sum of namespaces, so it wins
	})

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}
	if got.Currency != "USD" || got.Window != "1h" {
		t.Errorf("currency/window defaults: %+v", got)
	}
	if got.TotalHourlyCost != 8.0 {
		t.Errorf("TotalHourlyCost=%v, want 8.0 (node_total_hourly_cost ceiling)", got.TotalHourlyCost)
	}
	if got.TotalStorageCost != 0.05 {
		t.Errorf("TotalStorageCost=%v, want 0.05", got.TotalStorageCost)
	}
	// totalAlloc = (2+3) + (1+0.5) = 6.5; totalUsage = (0.8+1.2) + (0.6+0.25) = 2.85
	// clusterEff = 2.85/6.5 * 100 = 43.85 → 43.8 at 1 dp
	if got.ClusterEfficiency < 43 || got.ClusterEfficiency > 44 {
		t.Errorf("ClusterEfficiency=%v, want ~43.8", got.ClusterEfficiency)
	}
	// totalIdle = 6.5 - 2.85 = 3.65
	if got.TotalIdleCost < 3.5 || got.TotalIdleCost > 3.8 {
		t.Errorf("TotalIdleCost=%v, want ~3.65", got.TotalIdleCost)
	}
	if len(got.Namespaces) != 2 {
		t.Fatalf("expected 2 namespaces, got %d", len(got.Namespaces))
	}
	// Sorted by HourlyCost desc; checkout = 2+3+0.05 = 5.05 > payments = 1+0.5 = 1.5
	if got.Namespaces[0].Name != "checkout" {
		t.Errorf("first namespace should be checkout (higher cost); got %s", got.Namespaces[0].Name)
	}
	if got.Namespaces[0].HourlyCost != 5.05 {
		t.Errorf("checkout.HourlyCost=%v, want 5.05", got.Namespaces[0].HourlyCost)
	}
}

func TestComputeCostSummary_NoMetricsReason(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		// All queries return empty vector results.
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if got.Available {
		t.Error("expected Available=false when no metrics")
	}
	if got.Reason != ReasonNoMetrics {
		t.Errorf("Reason=%q, want %q", got.Reason, ReasonNoMetrics)
	}
}

func TestComputeCostSummary_QueryErrorReason(t *testing.T) {
	// Both primary and opencost_* fallback fail with HTTP error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	client := prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if got.Available {
		t.Error("expected Available=false on query error")
	}
	if got.Reason != ReasonQueryError {
		t.Errorf("Reason=%q", got.Reason)
	}
}

func TestComputeCostSummary_FallsBackToOpencostMetricNames(t *testing.T) {
	// First query (container_cpu_allocation) returns an error, then
	// the fallback (opencost_container_cpu_cost_total) succeeds.
	//
	// Simulated with a counter that errors the first time and succeeds the
	// second. The test uses an HTTP handler that inspects the query string
	// and returns accordingly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.Contains(q, "container_cpu_allocation"):
			w.WriteHeader(http.StatusBadGateway)
		case strings.Contains(q, "opencost_container_cpu_cost_total"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(vectorBody(map[string]float64{"checkout": 2.0})))
		case strings.Contains(q, "container_memory_allocation_bytes"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(vectorBody(map[string]float64{"checkout": 1.0})))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		}
	}))
	defer srv.Close()
	client := prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("expected Available=true with fallback metrics; %+v", got)
	}
	if len(got.Namespaces) != 1 || got.Namespaces[0].Name != "checkout" {
		t.Errorf("unexpected namespaces: %+v", got.Namespaces)
	}
}

func TestComputeCostSummary_RoundsValues(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"x": 1.123456789})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"x": 2.987654321})},
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}
	nc := got.Namespaces[0]
	if nc.CPUCost != 1.1235 {
		t.Errorf("CPU rounding: got %v, want 1.1235", nc.CPUCost)
	}
	if nc.MemoryCost != 2.9877 {
		t.Errorf("Memory rounding: got %v, want 2.9877", nc.MemoryCost)
	}
}

func TestWindowHours(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		// Standard units
		{"1h", 1},
		{"24h", 24},
		{"7d", 168},
		{"1w", 168},
		{"30d", 720},
		// Decimal hours (rare but accepted)
		{"1.5h", 1.5},
		// Minutes — documented decision to treat lone "m" as minutes,
		// not months. Pinned here so the windowHours("m") comment can't
		// be quietly "fixed" to mean months.
		{"5m", 5.0 / 60},
		// Fallbacks: empty, missing unit, parse error, non-positive
		{"", 1},
		{"h", 1},
		{"-5h", 1},
		{"0h", 1},
		{"abch", 1},
		// Unknown unit
		{"3y", 1},
	}
	for _, tc := range cases {
		got := windowHours(tc.in)
		if got != tc.want {
			t.Errorf("windowHours(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
