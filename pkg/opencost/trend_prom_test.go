package opencost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

// matrixBody builds a Prometheus range-query (matrix) response for the
// given per-namespace series. Each series gets the same set of (ts, value)
// data points, with the last point used as the ranking value.
func matrixBody(series []namespaceSeries) string {
	type point = []interface{}
	type entry struct {
		Metric map[string]string `json:"metric"`
		Values []point           `json:"values"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string  `json:"resultType"`
			Result     []entry `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "matrix"
	for _, s := range series {
		values := make([]point, 0, len(s.points))
		for _, p := range s.points {
			values = append(values, point{float64(p.ts), formatFloat(p.v)})
		}
		body.Data.Result = append(body.Data.Result, entry{
			Metric: map[string]string{"namespace": s.ns},
			Values: values,
		})
	}
	b, _ := json.Marshal(body)
	return string(b)
}

type namespaceSeries struct {
	ns     string
	points []dpoint
}
type dpoint struct {
	ts int64
	v  float64
}

func rangeProm(t *testing.T, body string) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

func TestComputeCostTrendFromProm_TopNAndOther(t *testing.T) {
	// 5 namespaces, ranked by latest value: a=10, b=8, c=5, d=3, e=1.
	// MaxSeries=2 → top two (a, b) returned individually; c/d/e collapsed
	// into a single "other" series with summed per-timestamp values.
	client := rangeProm(t, matrixBody([]namespaceSeries{
		{"a", []dpoint{{1700000000, 9}, {1700003600, 10}}},
		{"b", []dpoint{{1700000000, 7}, {1700003600, 8}}},
		{"c", []dpoint{{1700000000, 4}, {1700003600, 5}}},
		{"d", []dpoint{{1700000000, 2}, {1700003600, 3}}},
		{"e", []dpoint{{1700000000, 1}, {1700003600, 1}}},
	}))

	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{
		Range:     "24h",
		MaxSeries: 2,
	})
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if got.Range != "24h" {
		t.Errorf("Range: got %q, want %q", got.Range, "24h")
	}
	if len(got.Series) != 3 {
		t.Fatalf("expected 3 series (2 top + other), got %d: %v", len(got.Series), namesOf(got.Series))
	}

	// First two series are top-N by last value.
	if got.Series[0].Namespace != "a" || got.Series[1].Namespace != "b" {
		t.Errorf("top series: got [%s, %s], want [a, b]", got.Series[0].Namespace, got.Series[1].Namespace)
	}

	// Third series is "other" — c+d+e summed per timestamp.
	other := got.Series[2]
	if other.Namespace != "other" {
		t.Errorf("third series namespace: got %q, want %q", other.Namespace, "other")
	}
	if len(other.DataPoints) != 2 {
		t.Fatalf("other should have 2 points, got %d", len(other.DataPoints))
	}
	// Points are sorted by Timestamp ascending.
	if other.DataPoints[0].Timestamp != 1700000000 {
		t.Errorf("other[0].Timestamp: got %d", other.DataPoints[0].Timestamp)
	}
	// c+d+e at ts=1700000000 = 4+2+1 = 7
	if other.DataPoints[0].Value != 7 {
		t.Errorf("other[0].Value: got %v, want 7", other.DataPoints[0].Value)
	}
	// c+d+e at ts=1700003600 = 5+3+1 = 9
	if other.DataPoints[1].Value != 9 {
		t.Errorf("other[1].Value: got %v, want 9", other.DataPoints[1].Value)
	}
}

func TestComputeCostTrendFromProm_AllUnderMaxSeriesNoOther(t *testing.T) {
	// 2 namespaces, MaxSeries=8 → no "other" series.
	client := rangeProm(t, matrixBody([]namespaceSeries{
		{"a", []dpoint{{1700000000, 1}}},
		{"b", []dpoint{{1700000000, 2}}},
	}))
	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{Range: "24h"})
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if len(got.Series) != 2 {
		t.Errorf("expected 2 series (no 'other'), got %d: %v", len(got.Series), namesOf(got.Series))
	}
	for _, s := range got.Series {
		if s.Namespace == "other" {
			t.Errorf("unexpected 'other' series with %d points: %+v", len(s.DataPoints), s.DataPoints)
		}
	}
}

func TestComputeCostTrendFromProm_EmptyNamespaceLabelSkipped(t *testing.T) {
	// A series with no namespace label must not appear in the output (it
	// can't be ranked or attributed). The implementation skips it during
	// the rank pass.
	client := rangeProm(t, matrixBody([]namespaceSeries{
		{"", []dpoint{{1700000000, 99}}}, // would be top by value, but unnamed
		{"a", []dpoint{{1700000000, 1}}},
	}))
	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{Range: "24h"})
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	for _, s := range got.Series {
		if s.Namespace == "" {
			t.Errorf("unexpected empty-namespace series in output: %+v", s)
		}
	}
}

func TestComputeCostTrendFromProm_NilClient(t *testing.T) {
	got := ComputeCostTrendFromProm(context.Background(), nil, TrendPromOptions{Range: "24h"})
	if got.Available {
		t.Errorf("expected Available=false with nil client")
	}
	if got.Reason != ReasonNoPrometheus {
		t.Errorf("Reason: got %q, want %q", got.Reason, ReasonNoPrometheus)
	}
}

func TestComputeCostTrendFromProm_NoSeries(t *testing.T) {
	emptyBody := `{"status":"success","data":{"resultType":"matrix","result":[]}}`
	client := rangeProm(t, emptyBody)
	got := ComputeCostTrendFromProm(context.Background(), client, TrendPromOptions{Range: "24h"})
	if got.Available {
		t.Errorf("expected Available=false on no series")
	}
	if got.Reason != ReasonNoMetrics {
		t.Errorf("Reason: got %q, want %q", got.Reason, ReasonNoMetrics)
	}
}

func namesOf(series []CostTrendSeries) []string {
	out := make([]string, len(series))
	for i, s := range series {
		out[i] = s.Namespace
	}
	return out
}
