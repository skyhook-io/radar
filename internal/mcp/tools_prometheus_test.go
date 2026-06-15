package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// These tests drive the MCP Prometheus handlers end-to-end against a fake
// Prometheus API. The handlers use the global internal/prometheus singleton,
// so tests re-Initialize it per test and must NOT use t.Parallel().
//
// Manual-URL discovery probes the endpoint with GET /api/v1/query?query=up
// and requires a success vector with at least one sample — the fake server
// answers that probe unconditionally so EnsureConnected succeeds.

const (
	probeOKBody       = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"__name__":"up"},"value":[1700000000,"1"]}]}}`
	emptyVectorBody   = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	defaultMatrixBody = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"pod":"a"},"values":[[1700000000,"1"],[1700000060,"2"]]}]}}`
	emptyLabelsBody   = `{"status":"success","data":[]}`
	emptyMetadataBody = `{"status":"success","data":{}}`
)

type labelCall struct {
	label  string
	params url.Values
}

type fakeProm struct {
	mu            sync.Mutex
	rangeParams   []url.Values
	labelCalls    []labelCall
	metadataCalls int
	probeCalls    int // GET /api/v1/query?query=up connectivity probes
	rulesParams   []url.Values

	queryStatus    int // 0 → 200
	queryBody      string
	rangeStatus    int
	rangeBody      string
	labelStatus    int
	labelBody      string
	metadataStatus int
	metadataBody   string
	rulesStatus    int
	rulesBody      string
	rulesDelay     time.Duration // sleep before answering /api/v1/rules
}

func (f *fakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := r.URL.Query()
	path := r.URL.Path
	switch {
	case path == "/api/v1/query":
		if q.Get("query") == "up" {
			f.probeCalls++
			writeFakeBody(w, 0, probeOKBody)
			return
		}
		writeFakeBody(w, f.queryStatus, orDefault(f.queryBody, emptyVectorBody))
	case path == "/api/v1/query_range":
		f.rangeParams = append(f.rangeParams, q)
		writeFakeBody(w, f.rangeStatus, orDefault(f.rangeBody, defaultMatrixBody))
	case strings.HasPrefix(path, "/api/v1/label/") && strings.HasSuffix(path, "/values"):
		label := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/label/"), "/values")
		f.labelCalls = append(f.labelCalls, labelCall{label: label, params: q})
		writeFakeBody(w, f.labelStatus, orDefault(f.labelBody, emptyLabelsBody))
	case path == "/api/v1/metadata":
		f.metadataCalls++
		writeFakeBody(w, f.metadataStatus, orDefault(f.metadataBody, emptyMetadataBody))
	case path == "/api/v1/rules":
		f.rulesParams = append(f.rulesParams, q)
		if f.rulesDelay > 0 {
			time.Sleep(f.rulesDelay)
		}
		writeFakeBody(w, f.rulesStatus, orDefault(f.rulesBody, `{"status":"success","data":{"groups":[]}}`))
	default:
		http.NotFound(w, r)
	}
}

func writeFakeBody(w http.ResponseWriter, status int, body string) {
	if status != 0 && status != http.StatusOK {
		w.WriteHeader(status)
	}
	_, _ = w.Write([]byte(body))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (f *fakeProm) lastRangeParams(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rangeParams) == 0 {
		t.Fatalf("no /api/v1/query_range request received")
	}
	return f.rangeParams[len(f.rangeParams)-1]
}

func (f *fakeProm) lastLabelCall(t *testing.T) labelCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelCalls) == 0 {
		t.Fatalf("no /api/v1/label/.../values request received")
	}
	return f.labelCalls[len(f.labelCalls)-1]
}

// setupFakeProm points the global prometheus client at a fake Prometheus
// server. Cleanup re-Initializes the singleton so later tests (in this file
// or others) never inherit a manual URL aimed at a closed server.
func setupFakeProm(t *testing.T) *fakeProm {
	t.Helper()
	f := &fakeProm{}
	srv := httptest.NewServer(f)
	prometheus.Initialize(nil, nil, "test-ctx")
	prometheus.SetManualURL(srv.URL)
	t.Cleanup(func() {
		srv.Close()
		prometheus.Reset()
		prometheus.Initialize(nil, nil, "")
	})
	return f
}

func decodeQueryResponse(t *testing.T, body string) promQueryResponse {
	t.Helper()
	var resp promQueryResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal query response: %v\nbody: %s", err, body)
	}
	return resp
}

func decodeDiscoverResponse(t *testing.T, body string) discoverMetricsResponse {
	t.Helper()
	var resp discoverMetricsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal discover response: %v\nbody: %s", err, body)
	}
	return resp
}

func TestHandleQueryPrometheus_InstantHappyPath(t *testing.T) {
	f := setupFakeProm(t)
	f.queryBody = `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"pod":"web-1"},"value":[1700000000,"0.5"]},` +
		`{"metric":{"pod":"web-2"},"value":[1700000000,"0.7"]}]}}`

	result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "test_cpu_usage"})
	if err != nil {
		t.Fatalf("handleQueryPrometheus: %v", err)
	}
	resp := decodeQueryResponse(t, extractText(t, result))

	if resp.Query != "test_cpu_usage" {
		t.Errorf("response should echo query, got %q", resp.Query)
	}
	if resp.Type != "instant" {
		t.Errorf("type = %q, want instant", resp.Type)
	}
	if resp.ResultType != "vector" {
		t.Errorf("resultType = %q, want vector", resp.ResultType)
	}
	if resp.SeriesCount != 2 || len(resp.Series) != 2 {
		t.Errorf("seriesCount = %d, len(series) = %d, want 2/2", resp.SeriesCount, len(resp.Series))
	}
	if resp.Truncated {
		t.Errorf("small result should not be truncated")
	}
}

func TestHandleQueryPrometheus_RangeStepAutoAdjust(t *testing.T) {
	t.Run("no step defaults to 15s minimum", func(t *testing.T) {
		f := setupFakeProm(t)

		result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
			Query: "test_metric", Type: "range", Since: "1h",
		})
		if err != nil {
			t.Fatalf("handleQueryPrometheus: %v", err)
		}

		params := f.lastRangeParams(t)
		if got := params.Get("step"); got != "15" {
			t.Errorf("step param = %q, want 15 (max(15s, 1h/300))", got)
		}

		start, _ := strconv.ParseInt(params.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(params.Get("end"), 10, 64)
		window := end - start
		if window != 3600 {
			t.Errorf("window = %ds, want 3600", window)
		}
		step, _ := strconv.ParseInt(params.Get("step"), 10, 64)
		// Prometheus range results include both endpoints.
		if points := window/step + 1; points > 300 {
			t.Errorf("points = %d, must not exceed default budget 300", points)
		}

		resp := decodeQueryResponse(t, extractText(t, result))
		if resp.Step != "15s" {
			t.Errorf("response step echo = %q, want 15s", resp.Step)
		}
		if resp.Start == "" || resp.End == "" {
			t.Errorf("range response must echo start/end, got start=%q end=%q", resp.Start, resp.End)
		}
	})

	t.Run("absurdly small step clamped to point budget", func(t *testing.T) {
		f := setupFakeProm(t)

		_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
			Query: "test_metric", Type: "range", Since: "1h", Step: "1s",
		})
		if err != nil {
			t.Fatalf("handleQueryPrometheus: %v", err)
		}
		// ceil(3600s / 299 intervals) = 13s floor (inclusive endpoints, whole
		// seconds); the requested 1s would be 3601 points.
		if got := f.lastRangeParams(t).Get("step"); got != "13" {
			t.Errorf("step param = %q, want 13", got)
		}
	})

	t.Run("fractional window never overshoots the budget", func(t *testing.T) {
		f := setupFakeProm(t)

		// 899s window: ns-precision floor would be 1.498s, which serializes
		// as step=1 and returns 900 points against a 600 budget.
		_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
			Query: "test_metric", Type: "range",
			Start: "2026-01-01T00:00:00Z", End: "2026-01-01T00:14:59Z",
			Step: "1s", MaxPoints: 600,
		})
		if err != nil {
			t.Fatalf("handleQueryPrometheus: %v", err)
		}
		params := f.lastRangeParams(t)
		step, _ := strconv.ParseInt(params.Get("step"), 10, 64)
		if step < 2 {
			t.Errorf("step = %d, fractional floor must round up to 2s", step)
		}
		if points := 899/step + 1; points > 600 {
			t.Errorf("points = %d, exceeds budget 600", points)
		}
	})

	t.Run("unparseable step rejected", func(t *testing.T) {
		setupFakeProm(t)
		_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
			Query: "test_metric", Type: "range", Since: "1h", Step: "30 sec",
		})
		if err == nil {
			t.Fatalf("expected step validation error")
		}
		if !strings.Contains(err.Error(), "invalid step") {
			t.Errorf("error = %v, want substring %q", err, "invalid step")
		}
	})
}

func TestHandleQueryPrometheus_MaxPointsClampedAt600(t *testing.T) {
	f := setupFakeProm(t)

	_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
		Query: "test_metric", Type: "range", Since: "1h", Step: "1s", MaxPoints: 10000,
	})
	if err != nil {
		t.Fatalf("handleQueryPrometheus: %v", err)
	}
	// max_points clamps to 600 → floor = ceil(3600/599) = 7s, not 1h/10000.
	if got := f.lastRangeParams(t).Get("step"); got != "7" {
		t.Errorf("step param = %q, want 7", got)
	}
}

func TestHandleQueryPrometheus_EmptyResultHasNote(t *testing.T) {
	f := setupFakeProm(t)
	f.queryBody = emptyVectorBody

	result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "test_missing_metric"})
	if err != nil {
		t.Fatalf("empty result must not be an error: %v", err)
	}
	resp := decodeQueryResponse(t, extractText(t, result))

	if resp.SeriesCount != 0 || len(resp.Series) != 0 {
		t.Errorf("expected empty series, got count=%d len=%d", resp.SeriesCount, len(resp.Series))
	}
	if !strings.Contains(resp.Note, "discover_metrics") {
		t.Errorf("note should steer the model to discover_metrics, got %q", resp.Note)
	}
}

func TestHandleQueryPrometheus_OversizedResultSummary(t *testing.T) {
	t.Setenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES", "50")

	f := setupFakeProm(t)
	var items []string
	for i := 0; i < 5; i++ {
		ns := "ns-a"
		if i%2 == 1 {
			ns = "ns-b"
		}
		items = append(items, `{"metric":{"pod":"pod-`+strconv.Itoa(i)+`","namespace":"`+ns+`","container":"app"},"value":[1700000000,"1"]}`)
	}
	f.queryBody = `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(items, ",") + `]}}`

	const query = "test_wide_metric"
	result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: query})
	if err != nil {
		t.Fatalf("handleQueryPrometheus: %v", err)
	}
	resp := decodeQueryResponse(t, extractText(t, result))

	if !resp.Truncated {
		t.Fatalf("expected truncated=true, body: %s", extractText(t, result))
	}
	if len(resp.Series) != 0 {
		t.Errorf("summary path must never return raw series, got %d", len(resp.Series))
	}
	if resp.SeriesCount != 5 {
		t.Errorf("seriesCount = %d, want 5", resp.SeriesCount)
	}
	if len(resp.Summary) == 0 {
		t.Fatalf("expected summary payload")
	}

	var summary struct {
		SeriesCount      int            `json:"seriesCount"`
		TotalDataPoints  int            `json:"totalDataPoints"`
		LabelCardinality map[string]int `json:"labelCardinality"`
		Suggestion       string         `json:"suggestion"`
	}
	if err := json.Unmarshal(resp.Summary, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\nsummary: %s", err, resp.Summary)
	}
	if summary.SeriesCount != 5 || summary.TotalDataPoints != 5 {
		t.Errorf("summary counts = %d series / %d points, want 5/5", summary.SeriesCount, summary.TotalDataPoints)
	}
	if summary.LabelCardinality["pod"] != 5 || summary.LabelCardinality["namespace"] != 2 || summary.LabelCardinality["container"] != 1 {
		t.Errorf("labelCardinality = %v, want pod=5 namespace=2 container=1", summary.LabelCardinality)
	}
	if want := "topk(5, " + query + ")"; summary.Suggestion != want {
		t.Errorf("suggestion = %q, want %q", summary.Suggestion, want)
	}

	// The summary JSON is hand-built so labelCardinality keys stay in
	// descending cardinality order — the first key is the label that
	// explodes the result.
	raw := string(resp.Summary)
	podIdx := strings.Index(raw, `"pod":`)
	nsIdx := strings.Index(raw, `"namespace":`)
	ctrIdx := strings.Index(raw, `"container":`)
	if podIdx < 0 || nsIdx < 0 || ctrIdx < 0 {
		t.Fatalf("missing cardinality keys in summary: %s", raw)
	}
	if !(podIdx < nsIdx && nsIdx < ctrIdx) {
		t.Errorf("labelCardinality keys not in descending cardinality order (pod@%d namespace@%d container@%d): %s",
			podIdx, nsIdx, ctrIdx, raw)
	}
}

func TestHandleQueryPrometheus_PromQLErrorIncludesBody(t *testing.T) {
	f := setupFakeProm(t)
	f.queryStatus = http.StatusBadRequest
	f.queryBody = `{"status":"error","errorType":"bad_data","error":"parse error at char 12: unexpected end of input"}`

	const query = `sum(rate(test_metric[5m])`
	_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: query})
	if err == nil {
		t.Fatalf("expected PromQL error")
	}
	if !strings.Contains(err.Error(), query) {
		t.Errorf("error should include the query, got: %v", err)
	}
	if !strings.Contains(err.Error(), "parse error at char 12") {
		t.Errorf("error should include Prometheus's own parse error, got: %v", err)
	}
}

func TestHandleQueryPrometheus_NotConnected(t *testing.T) {
	prometheus.Initialize(nil, nil, "test-ctx")
	prometheus.SetManualURL("http://127.0.0.1:1")
	t.Cleanup(func() {
		prometheus.Reset()
		prometheus.Initialize(nil, nil, "")
	})

	_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "test_metric"})
	if err == nil {
		t.Fatalf("expected not-connected error")
	}
	if !strings.Contains(err.Error(), "--prometheus-url") {
		t.Errorf("error should mention --prometheus-url configuration, got: %v", err)
	}
}

func TestHandleQueryPrometheus_InvalidWindow(t *testing.T) {
	cases := []struct {
		name    string
		input   queryPrometheusInput
		wantErr string
	}{
		{
			name:    "start after end",
			input:   queryPrometheusInput{Query: "m", Type: "range", Start: "2026-01-02T00:00:00Z", End: "2026-01-01T00:00:00Z"},
			wantErr: "must be before",
		},
		{
			name:    "unparseable start",
			input:   queryPrometheusInput{Query: "m", Type: "range", Start: "yesterday"},
			wantErr: "RFC3339",
		},
		{
			name:    "unparseable end",
			input:   queryPrometheusInput{Query: "m", Type: "range", End: "not-a-time"},
			wantErr: "RFC3339",
		},
		{
			name:    "unparseable since",
			input:   queryPrometheusInput{Query: "m", Type: "range", Since: "banana"},
			wantErr: "invalid duration",
		},
		{
			name:    "negative day-suffix since",
			input:   queryPrometheusInput{Query: "m", Type: "range", Since: "-1d"},
			wantErr: "must be positive",
		},
		{
			name:    "NaN day-suffix since",
			input:   queryPrometheusInput{Query: "m", Type: "range", Since: "NaNd"},
			wantErr: "invalid duration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupFakeProm(t)
			_, _, err := handleQueryPrometheus(context.Background(), nil, tc.input)
			if err == nil {
				t.Fatalf("expected window validation error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestHandleDiscoverMetrics_RequiresMatchForNameListing(t *testing.T) {
	_, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{})
	if err == nil {
		t.Fatalf("expected match-required error")
	}
	if !strings.Contains(err.Error(), "match") {
		t.Errorf("error should explain match is required, got: %v", err)
	}
}

func TestHandleDiscoverMetrics_SendsMatchLimitAndLookback(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["node_cpu_seconds_total"]}`

	const match = `{__name__=~"node_cpu.*"}`
	_, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Match: match})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}

	call := f.lastLabelCall(t)
	if call.label != "__name__" {
		t.Errorf("label path = %q, want __name__", call.label)
	}
	if got := call.params["match[]"]; len(got) != 1 || got[0] != match {
		t.Errorf("match[] = %v, want [%s]", got, match)
	}
	// One past the default limit (100) is requested so a complete-at-limit
	// result can be told apart from a truncated one.
	if got := call.params.Get("limit"); got != "101" {
		t.Errorf("limit = %q, want default 100 + 1", got)
	}
	start, err := strconv.ParseInt(call.params.Get("start"), 10, 64)
	if err != nil {
		t.Fatalf("start param %q not unix seconds: %v", call.params.Get("start"), err)
	}
	end, err := strconv.ParseInt(call.params.Get("end"), 10, 64)
	if err != nil {
		t.Fatalf("end param %q not unix seconds: %v", call.params.Get("end"), err)
	}
	if window := end - start; window != 3600 {
		t.Errorf("lookback window = %ds, want 3600 (1h)", window)
	}
}

func TestHandleDiscoverMetrics_Truncation(t *testing.T) {
	// The handler requests limit+1 so exactly-limit (complete) is distinct from
	// over-limit (truncated).
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["metric_a","metric_b","metric_c"]}`
	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{
		Match: `{__name__=~"metric_.*"}`, Limit: 3,
	})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}
	if got := f.lastLabelCall(t).params.Get("limit"); got != "4" {
		t.Errorf("limit param = %q, want 4 (limit+1)", got)
	}
	resp := decodeDiscoverResponse(t, extractText(t, result))
	if resp.Truncated {
		t.Errorf("a complete result of exactly limit must NOT be truncated")
	}
	if resp.Count != 3 {
		t.Errorf("count = %d, want 3", resp.Count)
	}

	// Below limit → not truncated.
	f.labelBody = `{"status":"success","data":["metric_a","metric_b"]}`
	result, _, err = handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{
		Match: `{__name__=~"metric_.*"}`, Limit: 3,
	})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}
	resp = decodeDiscoverResponse(t, extractText(t, result))
	if resp.Truncated {
		t.Errorf("fewer than limit results must not set truncated")
	}

	// More than limit (limit+1 from a honoring backend, or unbounded from an
	// older one) → truncated, capped to limit.
	f.labelBody = `{"status":"success","data":["m1","m2","m3","m4","m5"]}`
	result, _, err = handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{
		Match: `{__name__=~"m.*"}`, Limit: 3,
	})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}
	resp = decodeDiscoverResponse(t, extractText(t, result))
	if !resp.Truncated {
		t.Errorf("over-limit results must set truncated=true")
	}
	if !strings.Contains(resp.Note, "more specific match") {
		t.Errorf("truncation note should suggest narrowing, got %q", resp.Note)
	}
	if resp.Count != 3 || len(resp.Metrics) != 3 {
		t.Errorf("count = %d, len(metrics) = %d, want both capped at 3", resp.Count, len(resp.Metrics))
	}
}

func TestHandleQueryPrometheus_ScalarResult(t *testing.T) {
	f := setupFakeProm(t)
	f.queryBody = `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"42"]}}`

	result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "scalar(sum(up))"})
	if err != nil {
		t.Fatalf("scalar result must not error: %v", err)
	}
	resp := decodeQueryResponse(t, extractText(t, result))
	if resp.ResultType != "scalar" || resp.SeriesCount != 1 {
		t.Errorf("resultType = %q seriesCount = %d, want scalar/1", resp.ResultType, resp.SeriesCount)
	}
	if len(resp.Series) != 1 || len(resp.Series[0].DataPoints) != 1 || resp.Series[0].DataPoints[0].Value != 42 {
		t.Errorf("series = %+v, want one labelless series with value 42", resp.Series)
	}
}

func TestHandleDiscoverMetrics_LabelValuesMode(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["default","kube-system"]}`

	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Label: "namespace"})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}

	call := f.lastLabelCall(t)
	if call.label != "namespace" {
		t.Errorf("label path = %q, want namespace", call.label)
	}
	if _, hasMatch := call.params["match[]"]; hasMatch {
		t.Errorf("match[] should be omitted when no match given, got %v", call.params["match[]"])
	}

	resp := decodeDiscoverResponse(t, extractText(t, result))
	if len(resp.Values) != 2 || resp.Values[0] != "default" || resp.Values[1] != "kube-system" {
		t.Errorf("values = %v, want [default kube-system]", resp.Values)
	}
	if len(resp.Metrics) != 0 {
		t.Errorf("label mode should not return metrics entries, got %v", resp.Metrics)
	}

	f.mu.Lock()
	calls := f.metadataCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Errorf("label mode should not fetch metadata, got %d calls", calls)
	}
}

func TestHandleDiscoverMetrics_MetadataEnrichment(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["http_requests_total","my_rule:rate5m"]}`
	f.metadataBody = `{"status":"success","data":{"http_requests_total":[{"type":"counter","help":"Total HTTP requests"}]}}`

	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Match: `{__name__=~"http.*"}`})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}
	resp := decodeDiscoverResponse(t, extractText(t, result))

	if len(resp.Metrics) != 2 {
		t.Fatalf("metrics = %v, want 2 entries", resp.Metrics)
	}
	if resp.Metrics[0].Name != "http_requests_total" || resp.Metrics[0].Type != "counter" || resp.Metrics[0].Help != "Total HTTP requests" {
		t.Errorf("enriched entry = %+v, want counter/Total HTTP requests", resp.Metrics[0])
	}
	// Recording rules are absent from metadata — they keep empty type/help
	// but must never be dropped from the listing.
	if resp.Metrics[1].Name != "my_rule:rate5m" || resp.Metrics[1].Type != "" || resp.Metrics[1].Help != "" {
		t.Errorf("metadata-less entry = %+v, want bare name", resp.Metrics[1])
	}
	// A counter in the results triggers the JIT rate() usage hint.
	if !strings.Contains(resp.Usage, "rate(") {
		t.Errorf("usage hint should mention rate() when counters present, got %q", resp.Usage)
	}
}

func TestHandleDiscoverMetrics_NoCounterNoUsageHint(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["node_memory_Active_bytes"]}`
	f.metadataBody = `{"status":"success","data":{"node_memory_Active_bytes":[{"type":"gauge","help":"Active memory"}]}}`

	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Match: `{__name__=~"node_memory.*"}`})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}
	resp := decodeDiscoverResponse(t, extractText(t, result))
	if resp.Usage != "" {
		t.Errorf("gauge-only result must not carry a counter usage hint, got %q", resp.Usage)
	}
}

func TestHandleDiscoverMetrics_MetadataFailureDegradesToBareNames(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["http_requests_total","node_cpu_seconds_total"]}`
	f.metadataStatus = http.StatusInternalServerError
	f.metadataBody = "internal error"

	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Match: `{__name__=~".*_total"}`})
	if err != nil {
		t.Fatalf("metadata failure must not fail discovery: %v", err)
	}
	resp := decodeDiscoverResponse(t, extractText(t, result))

	if len(resp.Metrics) != 2 {
		t.Fatalf("metrics = %v, want 2 bare entries", resp.Metrics)
	}
	for _, m := range resp.Metrics {
		if m.Type != "" || m.Help != "" {
			t.Errorf("expected bare name after metadata failure, got %+v", m)
		}
	}
	if resp.Metrics[0].Name != "http_requests_total" || resp.Metrics[1].Name != "node_cpu_seconds_total" {
		t.Errorf("names = %v, want both metric names preserved", resp.Metrics)
	}
}

const testRulesBody = `{"status":"success","data":{"groups":[
  {"name":"kubernetes-apps","rules":[
    {"type":"alerting","name":"KubePodCrashLooping","query":"rate(restarts[5m]) > 0","duration":900,"state":"firing","health":"ok","labels":{"severity":"warning"}},
    {"type":"alerting","name":"KubeDeploymentReplicasMismatch","query":"kube_deployment_spec_replicas != kube_deployment_status_replicas_available","state":"inactive","health":"ok"},
    {"type":"recording","name":"namespace:container_cpu:sum","query":"sum(rate(container_cpu_usage_seconds_total[5m])) by (namespace)","health":"ok"}
  ]}
]}}`

func decodeRulesResponse(t *testing.T, body string) promRulesResponse {
	t.Helper()
	var resp promRulesResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal rules response: %v\nbody: %s", err, body)
	}
	return resp
}

func TestHandleGetPrometheusRules_FlattensAndFilters(t *testing.T) {
	t.Run("no filters returns all, group attached", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rulesBody = testRulesBody

		result, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{})
		if err != nil {
			t.Fatalf("handleGetPrometheusRules: %v", err)
		}
		resp := decodeRulesResponse(t, extractText(t, result))
		if resp.Count != 3 || len(resp.Rules) != 3 {
			t.Fatalf("count = %d, len = %d, want 3/3", resp.Count, len(resp.Rules))
		}
		if resp.Rules[0].Group != "kubernetes-apps" {
			t.Errorf("group = %q, want kubernetes-apps", resp.Rules[0].Group)
		}
	})

	t.Run("type filter applies client-side even when backend ignores the param", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rulesBody = testRulesBody // backend returns all types regardless

		result, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{Type: "record"})
		if err != nil {
			t.Fatalf("handleGetPrometheusRules: %v", err)
		}
		f.mu.Lock()
		sentType := f.rulesParams[len(f.rulesParams)-1].Get("type")
		f.mu.Unlock()
		if sentType != "record" {
			t.Errorf("server-side type param = %q, want record", sentType)
		}
		resp := decodeRulesResponse(t, extractText(t, result))
		if resp.Count != 1 || resp.Rules[0].Type != "recording" {
			t.Errorf("rules = %+v, want only the recording rule", resp.Rules)
		}
	})

	t.Run("state and name filters", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rulesBody = testRulesBody

		result, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{State: "firing"})
		if err != nil {
			t.Fatalf("handleGetPrometheusRules: %v", err)
		}
		resp := decodeRulesResponse(t, extractText(t, result))
		if resp.Count != 1 || resp.Rules[0].Name != "KubePodCrashLooping" {
			t.Errorf("state=firing should match exactly KubePodCrashLooping, got %+v", resp.Rules)
		}

		result, _, err = handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{Name: "crashloop"})
		if err != nil {
			t.Fatalf("handleGetPrometheusRules: %v", err)
		}
		resp = decodeRulesResponse(t, extractText(t, result))
		if resp.Count != 1 || resp.Rules[0].Name != "KubePodCrashLooping" {
			t.Errorf("case-insensitive substring should match KubePodCrashLooping, got %+v", resp.Rules)
		}
	})

	t.Run("limit truncates with note", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rulesBody = testRulesBody

		result, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{Limit: 2})
		if err != nil {
			t.Fatalf("handleGetPrometheusRules: %v", err)
		}
		resp := decodeRulesResponse(t, extractText(t, result))
		if resp.Count != 2 || !resp.Truncated || resp.Note == "" {
			t.Errorf("want 2 rules + truncated + note, got count=%d truncated=%v note=%q", resp.Count, resp.Truncated, resp.Note)
		}
	})

	t.Run("zero matches returns guidance note", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rulesBody = testRulesBody

		result, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{Name: "doesnotexist"})
		if err != nil {
			t.Fatalf("handleGetPrometheusRules: %v", err)
		}
		resp := decodeRulesResponse(t, extractText(t, result))
		if resp.Count != 0 || resp.Note == "" {
			t.Errorf("want 0 rules with note, got %+v", resp)
		}
	})

	t.Run("404 backend yields actionable error", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rulesStatus = http.StatusNotFound
		f.rulesBody = "not found"

		_, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{})
		if err == nil {
			t.Fatalf("expected error for rules-less backend")
		}
		if !strings.Contains(err.Error(), "/api/v1/rules") {
			t.Errorf("error should explain the backend lacks the rules API, got: %v", err)
		}
	})

	t.Run("invalid type and state rejected", func(t *testing.T) {
		_, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{Type: "alerts"})
		if err == nil || !strings.Contains(err.Error(), "alert or record") {
			t.Errorf("invalid type: %v", err)
		}
		_, _, err = handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{State: "active"})
		if err == nil || !strings.Contains(err.Error(), "firing") {
			t.Errorf("invalid state: %v", err)
		}
	})
}

func TestHandleDiscoverMetrics_SingleConnectivityProbe(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["http_requests_total"]}`

	_, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Match: `{__name__=~"http.*"}`})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}
	f.mu.Lock()
	probes := f.probeCalls
	f.mu.Unlock()
	// Name-mode runs LabelValues + Metadata; before the Prom() switch each of
	// those re-ran EnsureConnected, for 3 probes total. Now: one.
	if probes != 1 {
		t.Errorf("connectivity probes = %d, want 1 (no redundant re-probing per wrapper call)", probes)
	}
}

func TestParsePromDuration(t *testing.T) {
	ok := map[string]time.Duration{
		"30m":   30 * time.Minute,
		"6h":    6 * time.Hour,
		"1h30m": 90 * time.Minute,
		"100ms": 100 * time.Millisecond,
		"7d":    7 * 24 * time.Hour,
		"1d12h": 36 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		"1.5d":  36 * time.Hour,
		" 1h ":  time.Hour,
	}
	for in, want := range ok {
		got, err := parsePromDuration(in)
		if err != nil {
			t.Errorf("parsePromDuration(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parsePromDuration(%q) = %s, want %s", in, got, want)
		}
	}

	bad := map[string]string{
		"banana": "invalid duration",
		"-1d":    "must be positive",
		"NaNd":   "invalid duration",
		"0":      "must be positive",
	}
	for in, wantSub := range bad {
		_, err := parsePromDuration(in)
		if err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Errorf("parsePromDuration(%q) error = %v, want substring %q", in, err, wantSub)
		}
	}
}

func TestPromQueryError_StatusMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{400, "parse error at char 12"},
		{422, "too many samples"},
		{429, "slow down"},
		{503, "upstream connect error"},
	}
	const internalURL = "http://prometheus.monitoring.svc:9090/api/v1/query"
	for _, tc := range cases {
		err := promQueryError(context.Background(), "some_query", 30*time.Second,
			&prom.HTTPError{StatusCode: tc.status, URL: internalURL, Body: []byte(tc.body)})
		if err == nil {
			t.Fatalf("status %d: expected error", tc.status)
		}
		// Status code, the echoed query, and Prometheus's own body all surface
		// so the model can self-correct...
		if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
			t.Errorf("status %d: error should carry the status code, got %v", tc.status, err)
		}
		if !strings.Contains(err.Error(), tc.body) {
			t.Errorf("status %d: error should carry prometheus body %q, got %v", tc.status, tc.body, err)
		}
		if !strings.Contains(err.Error(), "some_query") {
			t.Errorf("status %d: error should echo the query, got %v", tc.status, err)
		}
		// ...but the raw HTTPError's embedded internal backend URL must not leak.
		if strings.Contains(err.Error(), internalURL) {
			t.Errorf("status %d: error leaked the internal backend URL: %v", tc.status, err)
		}
	}
}

func TestHandleGetPrometheusRules_RecordWithStateRejected(t *testing.T) {
	_, _, err := handleGetPrometheusRules(context.Background(), nil,
		getPrometheusRulesInput{Type: "record", State: "firing"})
	if err == nil {
		t.Fatalf("type=record + state must be rejected as contradictory")
	}
	if !strings.Contains(err.Error(), "recording rules have no state") {
		t.Errorf("error should explain the contradiction, got: %v", err)
	}
}

func TestResolveQueryTimeout(t *testing.T) {
	cases := []struct {
		in   int
		want time.Duration
	}{
		{0, promQueryTimeout},
		{-5, promQueryTimeout},
		{60, 60 * time.Second},
		{180, promMaxQueryTimeout},
		{9999, promMaxQueryTimeout},
		{10_000_000_000, promMaxQueryTimeout},
		{1 << 62, promMaxQueryTimeout},
	}
	for _, tc := range cases {
		if got := resolveQueryTimeout(tc.in); got != tc.want {
			t.Errorf("resolveQueryTimeout(%d) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// Guards the "which namespaces have metric X" case: both match and label set.
// A refactor that gated match[] construction on label=="" would silently
// return UNSCOPED label values — this asserts match[] is actually sent.
func TestHandleDiscoverMetrics_MatchAndLabel(t *testing.T) {
	f := setupFakeProm(t)
	f.labelBody = `{"status":"success","data":["dev","staging"]}`

	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{
		Label: "namespace", Match: "up",
	})
	if err != nil {
		t.Fatalf("handleDiscoverMetrics: %v", err)
	}

	call := f.lastLabelCall(t)
	if call.label != "namespace" {
		t.Errorf("label path = %q, want namespace", call.label)
	}
	if got := call.params["match[]"]; len(got) != 1 || got[0] != "up" {
		t.Errorf("match[] = %v, want [up] — match must be scoped, not dropped", got)
	}
	resp := decodeDiscoverResponse(t, extractText(t, result))
	if len(resp.Values) != 2 {
		t.Errorf("values = %v, want the scoped namespace list", resp.Values)
	}
	f.mu.Lock()
	calls := f.metadataCalls
	f.mu.Unlock()
	if calls != 0 {
		t.Errorf("label mode must not fetch metadata, got %d calls", calls)
	}
}

func TestHandleQueryPrometheus_RangeErrorMapping(t *testing.T) {
	t.Run("422 maps to execution guidance", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rangeStatus = http.StatusUnprocessableEntity
		f.rangeBody = `{"status":"error","errorType":"execution","error":"query processing would load too many samples"}`

		_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
			Query: "sum(rate(x[5m]))", Type: "range", Since: "1h",
		})
		if err == nil {
			t.Fatal("expected 422 error")
		}
		if !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "too many samples") {
			t.Errorf("422 error should carry status + body, got: %v", err)
		}
	})

	t.Run("503 surfaces status+body but never the internal address", func(t *testing.T) {
		f := setupFakeProm(t)
		f.rangeStatus = http.StatusServiceUnavailable
		f.rangeBody = "upstream connect error"

		_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
			Query: "up", Type: "range", Since: "1h",
		})
		if err == nil {
			t.Fatal("expected 503 error")
		}
		if !strings.Contains(err.Error(), "503") {
			t.Errorf("503 error should mention status, got: %v", err)
		}
		// The raw *prom.HTTPError embeds the backend URL + full query; the
		// mapped message must not leak either.
		if strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("503 error leaked the internal backend address: %v", err)
		}
	})
}

func TestHandleQueryPrometheus_RangePointsDominateSummary(t *testing.T) {
	t.Setenv("RADAR_MCP_PROM_MAX_RESPONSE_BYTES", "50")
	f := setupFakeProm(t)
	// One series, many points → oversized but topk can't help; the range
	// summary branch must say so instead of suggesting topk.
	f.rangeBody = `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"pod":"a"},"values":[[1,"1"],[2,"2"],[3,"3"],[4,"4"]]}]}}`

	result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{
		Query: "rate(x[5m])", Type: "range", Since: "1h",
	})
	if err != nil {
		t.Fatalf("handleQueryPrometheus: %v", err)
	}
	resp := decodeQueryResponse(t, extractText(t, result))
	if !resp.Truncated || len(resp.Summary) == 0 {
		t.Fatalf("expected truncated summary, got %s", extractText(t, result))
	}
	var summary struct {
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal(resp.Summary, &summary); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if !strings.Contains(summary.Suggestion, "points-per-series dominate") {
		t.Errorf("range+low-cardinality summary should explain points dominate, got %q", summary.Suggestion)
	}
}

func TestHandleGetPrometheusRules_Timeout(t *testing.T) {
	orig := promQueryTimeout
	promQueryTimeout = 100 * time.Millisecond
	t.Cleanup(func() { promQueryTimeout = orig })

	f := setupFakeProm(t)
	f.rulesDelay = 500 * time.Millisecond // exceeds the shrunk budget

	_, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{})
	if err == nil {
		t.Fatal("expected rules timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("rules timeout should say so, got: %v", err)
	}
}

func TestHandleDiscoverMetrics_TruncatedNoteFraming(t *testing.T) {
	t.Run("name mode frames count as a floor", func(t *testing.T) {
		f := setupFakeProm(t)
		f.labelBody = `{"status":"success","data":["m1","m2","m3","m4"]}`
		result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{
			Match: `{__name__=~"m.*"}`, Limit: 3,
		})
		if err != nil {
			t.Fatalf("handleDiscoverMetrics: %v", err)
		}
		resp := decodeDiscoverResponse(t, extractText(t, result))
		if !resp.Truncated || !strings.Contains(resp.Note, "more exist") {
			t.Errorf("truncated count should be framed as a floor, got note %q", resp.Note)
		}
	})

	t.Run("label mode note offers raise-limit, not just match", func(t *testing.T) {
		f := setupFakeProm(t)
		f.labelBody = `{"status":"success","data":["a","b","c","d"]}`
		result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{
			Label: "pod", Match: "up", Limit: 3,
		})
		if err != nil {
			t.Fatalf("handleDiscoverMetrics: %v", err)
		}
		resp := decodeDiscoverResponse(t, extractText(t, result))
		if !resp.Truncated || !strings.Contains(resp.Note, "raise limit") {
			t.Errorf("label-mode truncation note should mention raising limit, got %q", resp.Note)
		}
	})
}

// Locks in finding #1: a non-404 HTTP error from the rules backend must be
// sanitized (status + body) and must NOT leak the internal backend URL that the
// raw *prom.HTTPError embeds.
func TestHandleGetPrometheusRules_HTTPErrorNoLeak(t *testing.T) {
	f := setupFakeProm(t)
	f.rulesStatus = http.StatusServiceUnavailable
	f.rulesBody = "upstream connect error"

	_, _, err := handleGetPrometheusRules(context.Background(), nil, getPrometheusRulesInput{})
	if err == nil {
		t.Fatal("expected error for rules 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("rules error should carry the status code, got: %v", err)
	}
	if strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("rules 503 leaked the internal backend URL: %v", err)
	}
}
