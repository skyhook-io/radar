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
		{contains: "node_gpu_count", body: scalarBody(0)},
	})

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{Currency: "GBP"})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}
	if got.Currency != "GBP" || got.Window != "1h" {
		t.Errorf("currency/window: %+v", got)
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
	// Node cost won, so hourlyCost is node capacity: it already holds the
	// 8 - 6.5 of compute nobody allocated, and the rows' storage is not in it.
	if got.HourlyCostBasis != HourlyCostBasisNodeCapacity {
		t.Errorf("HourlyCostBasis=%q, want node_capacity", got.HourlyCostBasis)
	}
	if got.TotalUnallocatedCost == nil || *got.TotalUnallocatedCost != 1.5 {
		t.Errorf("TotalUnallocatedCost=%v, want 1.5", got.TotalUnallocatedCost)
	}
	if got.TotalNodeCost == nil || *got.TotalNodeCost != 8 {
		t.Fatalf("node capacity cost must be independently retained: %+v", got.TotalNodeCost)
	}
	if got.TotalAllocatedCost != 6.55 {
		t.Errorf("allocated=%v, want 6.55", got.TotalAllocatedCost)
	}
	var rowIdle float64
	for _, row := range got.Namespaces {
		rowIdle += row.IdleCost
	}
	if got.TotalUnusedRequestCost != roundTo(rowIdle, 4) {
		t.Errorf("unused request cost=%v, want the rows' sum %v", got.TotalUnusedRequestCost, roundTo(rowIdle, 4))
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

func TestComputeCostSummary_NilClientIncludesCurrency(t *testing.T) {
	got := ComputeCostSummaryFromProm(context.Background(), nil, SummaryOptions{Currency: "GBP"})
	if got.Available || got.Reason != ReasonNoPrometheus || got.Currency != "GBP" {
		t.Fatalf("unexpected unavailable summary: %+v", got)
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

func TestComputeCostSummary_DedupesPersistentVolumeClaimRefs(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(q, "container_cpu_allocation"):
			_, _ = w.Write([]byte(vectorBody(map[string]float64{"checkout": 1.0})))
		case strings.Contains(q, "container_memory_allocation_bytes"):
			_, _ = w.Write([]byte(vectorBody(map[string]float64{"checkout": 1.0})))
		default:
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		}
	}))
	defer srv.Close()
	client := prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}

	var storageQuery string
	for _, q := range queries {
		if strings.Contains(q, "pv_hourly_cost") {
			storageQuery = q
			break
		}
	}
	if storageQuery == "" {
		t.Fatal("storage query was not issued")
	}
	if !strings.Contains(storageQuery, "max by (persistentvolume) (pv_hourly_cost)") {
		t.Fatalf("storage query must dedupe duplicate PV cost series:\n%s", storageQuery)
	}
	if !strings.Contains(storageQuery, "max by (persistentvolume, namespace)") {
		t.Fatalf("storage query must dedupe duplicate PVC ref series:\n%s", storageQuery)
	}
	if !strings.Contains(storageQuery, `label_replace(kube_persistentvolume_claim_ref, "namespace", "$1", "claim_namespace", "(.+)")`) {
		t.Fatalf("storage query must normalize claim_namespace before namespace aggregation:\n%s", storageQuery)
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

func TestEfficiencyPercent(t *testing.T) {
	tests := []struct {
		name         string
		usage, alloc float64
		want         float64
	}{
		{name: "unavailable usage", usage: 0, alloc: 1, want: 0},
		{name: "unavailable allocation", usage: 1, alloc: 0, want: 0},
		{name: "rounded", usage: 1, alloc: 3, want: 33.3},
		{name: "capped", usage: 2, alloc: 1, want: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EfficiencyPercent(test.usage, test.alloc); got != test.want {
				t.Fatalf("EfficiencyPercent(%v, %v) = %v, want %v", test.usage, test.alloc, got, test.want)
			}
		})
	}
}

func TestPartialUsageFailureIsFlaggedNotUnderstated(t *testing.T) {
	// Partial usage evidence must not yield a plausible efficiency measurement:
	// an efficiency derived from one of the two usage queries is indistinguishable
	// from a real one, so the row has to report that usage was not collected.
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"checkout": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"checkout": 3.0})},
		{contains: "container_cpu_usage_seconds_total", body: `{"status":"error","errorType":"bad_data","error":"boom"}`},
		{contains: "container_memory_working_set_bytes", body: vectorBody(map[string]float64{"checkout": 2.4})},
	})

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{Currency: "USD"})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}
	if len(got.Namespaces) != 1 {
		t.Fatalf("namespaces = %+v, want one row", got.Namespaces)
	}

	row := got.Namespaces[0]
	if !row.UsageUnavailable {
		t.Error("a failed usage query must mark the row, or a reader treats 0 as measured")
	}
	if row.Efficiency != 0 {
		t.Errorf("Efficiency=%v, want 0 — memory usage alone understates it", row.Efficiency)
	}
	if row.IdleCost != 0 {
		t.Errorf("IdleCost=%v, want 0 — idle is unknown, not the full allocation", row.IdleCost)
	}
	if got.ClusterEfficiency != 0 {
		t.Errorf("ClusterEfficiency=%v, want 0 — no row contributed usable evidence", got.ClusterEfficiency)
	}

	// The costs themselves are unaffected: only the usage-derived fields are.
	if row.HourlyCost != 5.0 {
		t.Errorf("HourlyCost=%v, want 5.0 — allocation is independent of the usage query", row.HourlyCost)
	}
}

func TestTotalUsageFailureLeavesUsageFieldsAtZeroAndFlagsThem(t *testing.T) {
	// With both usage queries failing, efficiency and idle stay zero and the row
	// marks usage unavailable. Zero here means "not measured", and only the flag
	// separates that from a genuine 0% — no data is not 100% idle.
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"checkout": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"checkout": 3.0})},
		{contains: "container_cpu_usage_seconds_total", body: `{"status":"error","errorType":"bad_data","error":"boom"}`},
		{contains: "container_memory_working_set_bytes", body: `{"status":"error","errorType":"bad_data","error":"boom"}`},
	})

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{Currency: "USD"})
	row := got.Namespaces[0]
	if row.Efficiency != 0 || row.IdleCost != 0 || got.ClusterEfficiency != 0 || got.TotalIdleCost != 0 {
		t.Errorf("totals changed for a full usage failure: row=%+v clusterEff=%v idle=%v", row, got.ClusterEfficiency, got.TotalIdleCost)
	}
	if !row.UsageUnavailable {
		t.Error("the row must still be flagged")
	}
	if row.HourlyCost != 5.0 {
		t.Errorf("HourlyCost=%v, want 5.0", row.HourlyCost)
	}
}

// A namespace the usage queries never reported is not a namespace that used
// nothing. Both arrive as zero from the usage maps, and reporting the first as
// 0% efficiency ranks the one namespace nobody measured as the worst waste in
// the cluster.
func TestUnmeasuredNamespaceIsFlagged_MeasuredZeroIsNot(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"measured": 2.0, "unmeasured": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"measured": 1.0, "unmeasured": 1.0})},
		// Both usage queries answer successfully, and both carry a real zero for
		// "measured" while omitting "unmeasured" entirely.
		{contains: "rate(container_cpu_usage_seconds_total", body: vectorBody(map[string]float64{"measured": 0})},
		{contains: "container_memory_working_set_bytes", body: vectorBody(map[string]float64{"measured": 0})},
	})

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}
	rows := map[string]NamespaceCost{}
	for _, ns := range got.Namespaces {
		rows[ns.Name] = ns
	}

	measured, ok := rows["measured"]
	if !ok {
		t.Fatalf("measured namespace missing: %+v", got.Namespaces)
	}
	if measured.UsageUnavailable {
		t.Error("a namespace whose usage series reported 0 was flagged unavailable; a real measurement of zero must survive")
	}
	if measured.Efficiency != 0 {
		t.Errorf("measured.Efficiency=%v, want 0 — it genuinely used nothing", measured.Efficiency)
	}

	unmeasured, ok := rows["unmeasured"]
	if !ok {
		t.Fatalf("unmeasured namespace missing: %+v", got.Namespaces)
	}
	if !unmeasured.UsageUnavailable {
		t.Error("a namespace absent from both usage results was not flagged UsageUnavailable, so it reports 0% efficiency as if measured")
	}
	if unmeasured.IdleCost != 0 {
		t.Errorf("unmeasured.IdleCost=%v, want 0 — idle cannot be derived from usage that was never collected", unmeasured.IdleCost)
	}

	// Cluster efficiency must be built only from rows that were measured:
	// alloc 2+1=3, usage 0 => 0%. Folding the unmeasured namespace's allocation
	// in would double the denominator and halve the reported efficiency.
	if got.ClusterEfficiency != 0 {
		t.Errorf("ClusterEfficiency=%v, want 0 from the measured namespace alone", got.ClusterEfficiency)
	}
}

// One usage query answering and the other not is still incomplete evidence:
// efficiency built from half the inputs understates use on every row.
func TestNamespaceMissingFromOneUsageResultIsFlagged(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"half": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"half": 1.0})},
		{contains: "rate(container_cpu_usage_seconds_total", body: vectorBody(map[string]float64{"half": 1.0})},
		{contains: "container_memory_working_set_bytes", body: vectorBody(map[string]float64{})},
	})

	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if len(got.Namespaces) != 1 {
		t.Fatalf("namespaces=%+v", got.Namespaces)
	}
	if !got.Namespaces[0].UsageUnavailable {
		t.Error("namespace present in the CPU usage result but absent from memory was not flagged; partial evidence is still incomplete")
	}
}

// Without node cost the source never measured unallocated capacity, and a zero
// there would claim the nodes are fully packed.
func TestComputeCostSummaryWithoutNodeCostLeavesUnallocatedUnknown(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"checkout": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"checkout": 1.0})},
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available {
		t.Fatalf("summary unavailable: %+v", got)
	}
	if got.TotalUnallocatedCost != nil {
		t.Errorf("TotalUnallocatedCost=%v, want nil when node cost is absent", *got.TotalUnallocatedCost)
	}
	if got.HourlyCostBasis != HourlyCostBasisAllocated || got.TotalHourlyCost != got.TotalAllocatedCost {
		t.Errorf("basis=%q hourly=%v allocated=%v, want allocated and equal totals", got.HourlyCostBasis, got.TotalHourlyCost, got.TotalAllocatedCost)
	}
}

func TestComputeCostSummaryClampsUnallocatedAtZero(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"checkout": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"checkout": 1.0})},
		{contains: "node_total_hourly_cost", body: scalarBody(2.9)},
		{contains: "node_gpu_count", body: scalarBody(0)},
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if got.TotalUnallocatedCost == nil || *got.TotalUnallocatedCost != 0 {
		t.Errorf("TotalUnallocatedCost=%v, want 0 when price rounding puts allocation above node cost", got.TotalUnallocatedCost)
	}
}

// node_total_hourly_cost includes GPU spend the CPU and memory allocation does
// not, so on GPU nodes the difference would report allocated GPUs as idle.
func TestComputeCostSummaryLeavesUnallocatedUnknownOnGPUNodes(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"training": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"training": 1.0})},
		{contains: "node_total_hourly_cost", body: scalarBody(9.0)},
		{contains: "node_gpu_count", body: scalarBody(6.0)},
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if got.TotalUnallocatedCost != nil {
		t.Errorf("TotalUnallocatedCost=%v, want nil when nodes carry GPU spend", *got.TotalUnallocatedCost)
	}
	if got.HourlyCostBasis != HourlyCostBasisNodeCapacity {
		t.Errorf("HourlyCostBasis=%q, want node_capacity", got.HourlyCostBasis)
	}
}

// OpenCost can be configured not to emit its GPU metrics while node cost still
// includes GPU spend, so an absent GPU result cannot be read as no GPUs.
func TestComputeCostSummaryTreatsMissingGPUMetricsAsUnknown(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"a": 2.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"a": 1.0})},
		{contains: "node_total_hourly_cost", body: scalarBody(9.0)},
	})
	if got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{}); got.TotalUnallocatedCost != nil {
		t.Errorf("TotalUnallocatedCost=%v, want nil without GPU metrics", *got.TotalUnallocatedCost)
	}
}

func TestComputeCostSummaryWithholdsUnallocatedWhenAnAllocationFamilyIsMissing(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"a": 2.0})},
		{contains: "node_total_hourly_cost", body: scalarBody(9.0)},
		{contains: "node_gpu_count", body: scalarBody(0)},
	})
	if got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{}); got.TotalUnallocatedCost != nil {
		t.Errorf("TotalUnallocatedCost=%v, want nil when memory allocation is absent", *got.TotalUnallocatedCost)
	}
}

// One namespace using more than it was allocated must not cancel another's
// unused allocation in the total, as it does in the aggregate idle figure.
func TestUnusedRequestCostDoesNotNetOveruseAgainstWaste(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"idle": 4.0, "busy": 1.0})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"idle": 0.0, "busy": 0.0})},
		{contains: "container_cpu_usage_seconds_total", body: vectorBody(map[string]float64{"idle": 1.0, "busy": 4.0})},
		{contains: "container_memory_working_set_bytes", body: vectorBody(map[string]float64{"idle": 0.0, "busy": 0.0})},
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if got.TotalUnusedRequestCost != 3 {
		t.Errorf("TotalUnusedRequestCost=%v, want 3 from the idle namespace alone", got.TotalUnusedRequestCost)
	}
}

func TestSummaryRetainsNodeCostBelowAllocatedCost(t *testing.T) {
	client := scriptedProm(t, []scriptedCase{
		{contains: "container_cpu_allocation", body: vectorBody(map[string]float64{"app": 2})},
		{contains: "container_memory_allocation_bytes", body: vectorBody(map[string]float64{"app": 1})},
		{contains: "node_total_hourly_cost", body: scalarBody(1.5)},
	})
	got := ComputeCostSummaryFromProm(context.Background(), client, SummaryOptions{})
	if !got.Available || got.TotalAllocatedCost != 3 || got.TotalNodeCost == nil || *got.TotalNodeCost != 1.5 {
		t.Fatalf("independent allocation/capacity totals lost: %+v", got)
	}
}
