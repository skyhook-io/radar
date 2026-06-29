package opencost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// fakeOpenCost returns a RESTClient backed by a httptest server that serves
// canned JSON for /allocation. Caller provides the raw response body.
func fakeOpenCost(t *testing.T, bodyForAllocation string) *RESTClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/allocation":
			_, _ = w.Write([]byte(bodyForAllocation))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	tr := &httpTransport{baseURL: srv.URL, client: srv.Client()}
	return NewRESTClient(tr)
}

// httpTransport is a minimal Transport backed by net/http for tests.
type httpTransport struct {
	baseURL string
	client  *http.Client
}

func (t *httpTransport) Do(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	u := t.baseURL + path
	if len(params) > 0 {
		u = u + "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return buf, nil
}

func (t *httpTransport) Address() string { return t.baseURL }

// buildAllocationResponse builds a valid OpenCost /allocation body from a
// namespace→totalCost map, filling CPU and RAM with a 60/40 split so the
// test can verify splits too. Efficiency defaults to 50%.
func buildAllocationResponse(t *testing.T, rows map[string]float64) string {
	t.Helper()
	window := make(map[string]*Allocation, len(rows))
	for ns, total := range rows {
		window[ns] = &Allocation{
			Name:            ns,
			CPUCost:         total * 0.6,
			RAMCost:         total * 0.4,
			TotalCost:       total,
			TotalEfficiency: 0.5,
		}
	}
	resp := AllocationResponse{
		Code: 200,
		Data: []map[string]*Allocation{window},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestComputeCostSummary_REST_HappyPath(t *testing.T) {
	body := buildAllocationResponse(t, map[string]float64{
		"checkout": 5.00,
		"payments": 2.00,
		"user-svc": 0.75,
	})
	client := fakeOpenCost(t, body)

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("expected Available=true; got %+v", got)
	}
	if got.Currency != "USD" || got.Window != "1h" {
		t.Errorf("defaults not applied: currency=%q window=%q", got.Currency, got.Window)
	}
	if len(got.Namespaces) != 3 {
		t.Fatalf("want 3 namespaces, got %d", len(got.Namespaces))
	}
	// Sorted by HourlyCost desc.
	if got.Namespaces[0].Name != "checkout" {
		t.Errorf("want checkout first, got %s", got.Namespaces[0].Name)
	}
	if got.Namespaces[0].HourlyCost != 5.00 {
		t.Errorf("checkout HourlyCost=%v, want 5.00", got.Namespaces[0].HourlyCost)
	}
	if got.Namespaces[0].CPUCost != 3.00 { // 60% of 5
		t.Errorf("checkout CPUCost=%v, want 3.00", got.Namespaces[0].CPUCost)
	}
	// Efficiency 50% roundtrip
	if got.Namespaces[0].Efficiency != 50 {
		t.Errorf("efficiency=%v, want 50", got.Namespaces[0].Efficiency)
	}
	// Cluster totals: sum of 5+2+0.75 = 7.75
	if got.TotalHourlyCost != 7.75 {
		t.Errorf("TotalHourlyCost=%v, want 7.75", got.TotalHourlyCost)
	}
}

func TestComputeCostSummary_REST_IdleRowSurfaced(t *testing.T) {
	// OpenCost emits __idle__ for unallocated node capacity. We surface it
	// as TotalIdleCost (not a namespace row), and do NOT roll it into
	// TotalHourlyCost — total hourly is the sum of *allocated* spend, so
	// the UI can render idle as a separate cell without double-counting.
	window := map[string]*Allocation{
		"checkout": {Name: "checkout", CPUCost: 1.0, RAMCost: 0.5, TotalCost: 1.5, TotalEfficiency: 0.6},
		"__idle__": {Name: "__idle__", CPUCost: 0.8, RAMCost: 0.2, TotalCost: 1.0},
	}
	body, _ := json.Marshal(AllocationResponse{Code: 200, Data: []map[string]*Allocation{window}})
	client := fakeOpenCost(t, string(body))

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatal("want Available=true")
	}
	// TotalIdleCost is the sum of __idle__ (1.0, cluster-level unused
	// capacity) + per-namespace idle (checkout: alloc 1.5 × (1 - eff 0.6)
	// = 0.6). The UI surfaces both together as "waste".
	if got.TotalIdleCost != 1.6 {
		t.Errorf("TotalIdleCost=%v, want 1.6 (__idle__ 1.0 + checkout ns-idle 0.6)", got.TotalIdleCost)
	}
	for _, ns := range got.Namespaces {
		if ns.Name == "__idle__" {
			t.Error("__idle__ must not appear as a regular namespace row")
		}
	}
	// Allocated-only total = 1.5 for checkout; __idle__ excluded.
	if got.TotalHourlyCost != 1.5 {
		t.Errorf("TotalHourlyCost=%v, want 1.5 (allocated only; __idle__ goes to TotalIdleCost)", got.TotalHourlyCost)
	}
}

func TestComputeCostSummary_REST_NegativeIdleClampedToZero(t *testing.T) {
	// Real-world: OpenCost can report a negative __idle__ totalCost when
	// burstable workloads over-consume vs node pricing. The __idle__
	// contribution clamps to 0; per-namespace idle (positive, from
	// under-utilization) still counts in the total.
	window := map[string]*Allocation{
		"app":      {Name: "app", CPUCost: 0.5, RAMCost: 0.1, TotalCost: 0.6, TotalEfficiency: 0.4},
		"__idle__": {Name: "__idle__", CPUCost: -0.3, RAMCost: -0.1},
	}
	body, _ := json.Marshal(AllocationResponse{Code: 200, Data: []map[string]*Allocation{window}})
	client := fakeOpenCost(t, string(body))

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{})
	// Expect: __idle__ clamped to 0, app ns-idle = 0.6 × (1 - 0.4) = 0.36.
	if got.TotalIdleCost != 0.36 {
		t.Errorf("TotalIdleCost=%v, want 0.36 (__idle__ clamped, app ns-idle 0.36)", got.TotalIdleCost)
	}
	if got.TotalHourlyCost != 0.6 {
		t.Errorf("TotalHourlyCost should still be 0.6 (allocated only); got %v", got.TotalHourlyCost)
	}
}

func TestComputeCostSummary_REST_WindowNormalization(t *testing.T) {
	// OpenCost's /allocation returns totalCost summed over the whole
	// window. We must divide by the window's hours to present a rate so
	// the UI can multiply by 730 for monthly projection without
	// ballooning the numbers when the user picks 24h / 7d / 30d.
	window := map[string]*Allocation{
		"svc": {Name: "svc", CPUCost: 24.0, RAMCost: 0, TotalCost: 24.0, TotalEfficiency: 0.5},
	}
	body, _ := json.Marshal(AllocationResponse{Code: 200, Data: []map[string]*Allocation{window}})
	client := fakeOpenCost(t, string(body))

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{Window: "24h"})
	if !got.Available {
		t.Fatal("want Available=true")
	}
	// 24.0 total over 24h → $1/hr.
	if got.TotalHourlyCost != 1.0 {
		t.Errorf("TotalHourlyCost=%v, want 1.0 ($24 total / 24h = $1/hr)", got.TotalHourlyCost)
	}
	if got.Namespaces[0].HourlyCost != 1.0 {
		t.Errorf("svc.HourlyCost=%v, want 1.0", got.Namespaces[0].HourlyCost)
	}
}

func TestComputeCostSummary_REST_EfficiencyCappedBeforeAveraging(t *testing.T) {
	// OpenCost TotalEfficiency can exceed 1 for burstable workloads. A
	// single runaway row must not dominate the fleet average.
	window := map[string]*Allocation{
		"normal":    {Name: "normal", CPUCost: 1.0, RAMCost: 0, TotalCost: 1.0, TotalEfficiency: 0.2},
		"burstable": {Name: "burstable", CPUCost: 1.0, RAMCost: 0, TotalCost: 1.0, TotalEfficiency: 100.0},
	}
	body, _ := json.Marshal(AllocationResponse{Code: 200, Data: []map[string]*Allocation{window}})
	client := fakeOpenCost(t, string(body))

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{})
	// Burstable capped at 100%. Normal = 20%. Mean = 60%.
	if got.ClusterEfficiency < 58 || got.ClusterEfficiency > 62 {
		t.Errorf("ClusterEfficiency=%v, want ~60 (cap+avg)", got.ClusterEfficiency)
	}
	// Per-row caps too.
	for _, ns := range got.Namespaces {
		if ns.Efficiency > 100 {
			t.Errorf("%s efficiency=%v exceeds cap", ns.Name, ns.Efficiency)
		}
	}
}

func TestComputeCostSummary_REST_NoMetricsReason(t *testing.T) {
	body, _ := json.Marshal(AllocationResponse{Code: 200, Data: []map[string]*Allocation{{}}})
	client := fakeOpenCost(t, string(body))

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{})
	if got.Available {
		t.Error("expected Available=false for empty allocation data")
	}
	if got.Reason != ReasonNoMetrics {
		t.Errorf("Reason=%q, want %q", got.Reason, ReasonNoMetrics)
	}
}

func TestComputeCostSummary_REST_QueryErrorReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	client := NewRESTClient(&httpTransport{baseURL: srv.URL, client: srv.Client()})

	got := ComputeCostSummary(context.Background(), client, SummaryOptions{})
	if got.Available {
		t.Error("expected Available=false on 502")
	}
	// Any non-2xx yields parse-failure on empty body or json error → Reason maps to query_error.
	if got.Reason != ReasonQueryError {
		t.Errorf("Reason=%q, want %q", got.Reason, ReasonQueryError)
	}
}

func TestComputeCostSummary_REST_ForwardsWindow(t *testing.T) {
	var capturedQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":[{}]}`))
	}))
	defer srv.Close()
	client := NewRESTClient(&httpTransport{baseURL: srv.URL, client: srv.Client()})

	_ = ComputeCostSummary(context.Background(), client, SummaryOptions{Window: "7d"})
	if capturedQuery.Get("window") != "7d" {
		t.Errorf("window not forwarded: got %q", capturedQuery.Get("window"))
	}
	if capturedQuery.Get("aggregate") != "namespace" {
		t.Errorf("aggregate not set: got %q", capturedQuery.Get("aggregate"))
	}
	if capturedQuery.Get("includeIdle") != "true" {
		t.Errorf("includeIdle not set: got %q", capturedQuery.Get("includeIdle"))
	}
}
