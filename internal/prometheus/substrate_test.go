package prometheus

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// fakeSubstrateProm answers each query from the first rule whose fragment it contains.
type fakeSubstrateProm struct {
	rules   []fakeRule
	mu      sync.Mutex
	queries []string
}

type fakeRule struct {
	fragment string
	result   *prom.QueryResult
}

func (f *fakeSubstrateProm) answer(query string) (*prom.QueryResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	for _, r := range f.rules {
		if strings.Contains(query, r.fragment) {
			return r.result, nil
		}
	}
	return &prom.QueryResult{}, nil
}

func (f *fakeSubstrateProm) Query(_ context.Context, query string) (*prom.QueryResult, error) {
	return f.answer(query)
}

func (f *fakeSubstrateProm) QueryRange(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
	return f.answer(query)
}

func series(labels map[string]string, values ...float64) prom.Series {
	points := make([]prom.DataPoint, len(values))
	for i, v := range values {
		points[i] = prom.DataPoint{Timestamp: int64(1000 + 60*i), Value: v}
	}
	return prom.Series{Labels: labels, DataPoints: points}
}

func result(s ...prom.Series) *prom.QueryResult { return &prom.QueryResult{Series: s} }

func unverifiedScope(context.Context, scopeProbe) (string, SeriesIsolation, error) {
	return "", SeriesIsolation{Mode: SeriesIsolationUnverified, Note: "unverified"}, nil
}

func poolRequest(charts []substrateChartDef) substrateRequest {
	end := time.Unix(10_000*60, 0)
	return substrateRequest{sel: substratePoolSelector("agents", "pool"), charts: charts, start: end.Add(-time.Hour), end: end, step: time.Minute}
}

func chartByID(charts []SubstrateChart, id string) SubstrateChart {
	for _, c := range charts {
		if c.ID == id {
			return c
		}
	}
	return SubstrateChart{}
}

func TestSubstratePoolCharts(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		// The lifecycle family arrives through two scrape jobs.
		{"count by (job) (count_over_time(ate_actor_lifecycle_operation_duration_seconds_count", result(series(map[string]string{"job": "ateapi"}, 1), series(map[string]string{"job": "otel-collector"}, 1))},
		{"count by (job)", result(series(map[string]string{"job": "ateapi"}, 1))},
		{"avg by (ate_workerpool_namespace, ate_workerpool_name, ate_worker_state, ate_sandbox_class)", result(
			series(map[string]string{"ate_worker_state": "idle", "ate_sandbox_class": "gvisor"}, 1.5),
			series(map[string]string{"ate_worker_state": "idle", "ate_sandbox_class": "unknown"}, 1),
		)},
		// Crashes are scraped but none happened: the family is present, the chart empty.
		{"count(last_over_time(ate_actor_crashes_total", result(series(nil, 3))},
	}}
	charts := querySubstrateCharts(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts()))

	workers := chartByID(charts, "workers")
	if workers.State != SubstrateChartOK || len(workers.Series) != 2 || workers.Series[0].Labels["series"] != "idle (gvisor)" || workers.Series[1].Labels["series"] != "idle (unknown)" || workers.Isolation.Mode != SeriesIsolationUnverified {
		t.Fatalf("workers = %+v", workers)
	}
	for _, id := range []string{"resumeTime", "resumeFailures"} {
		if c := chartByID(charts, id); c.State != SubstrateChartDuplicateSources || !strings.Contains(c.Reason, "ateapi, otel-collector") {
			t.Fatalf("%s = %s %q, want duplicateSources", id, c.State, c.Reason)
		}
	}
	if c := chartByID(charts, "crashes"); c.State != SubstrateChartEmpty {
		t.Fatalf("crashes = %s, want empty", c.State)
	}
	if c := chartByID(charts, "placementTime"); c.State != SubstrateChartNoSeries {
		t.Fatalf("placementTime = %s, want noSeries", c.State)
	}
	for _, q := range f.queries {
		if strings.Contains(q, "ate_scheduler_outcome=\"no_capacity\"") {
			t.Fatalf("queried no_capacity, which names no pool: %s", q)
		}
	}
	if substrateResponseStatus(charts) != SubstrateAvailable {
		t.Fatal("status should be available")
	}
}

func TestSubstrateScopeStates(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrScopeAmbiguous, SubstrateChartAmbiguous},
		{ErrScopeMismatch, SubstrateChartMismatch},
		{prom.ErrPartialResponse, SubstrateChartError},
	} {
		resolve := func(context.Context, scopeProbe) (string, SeriesIsolation, error) { return "", SeriesIsolation{}, tc.err }
		f := &fakeSubstrateProm{}
		charts := querySubstrateCharts(context.Background(), f, resolve, poolRequest(substratePoolCharts()))
		for _, c := range charts {
			if c.State != tc.want {
				t.Fatalf("%v: %s = %s, want %s", tc.err, c.ID, c.State, tc.want)
			}
		}
		if len(f.queries) != 0 {
			t.Fatalf("%v: charts queried Prometheus after the scope check failed: %v", tc.err, f.queries)
		}
	}
}

func TestSubstrateScopeProbesAreStrictAndRanged(t *testing.T) {
	var probes []scopeProbe
	resolve := func(_ context.Context, p scopeProbe) (string, SeriesIsolation, error) {
		probes = append(probes, p)
		return `cluster="a"`, SeriesIsolation{Mode: SeriesIsolationVerified}, nil
	}
	f := &fakeSubstrateProm{}
	querySubstrateCharts(context.Background(), f, resolve, poolRequest(substratePoolCharts()))
	if len(probes) == 0 {
		t.Fatal("no scope probes")
	}
	for _, p := range probes {
		// One hour of chart, a step after its aligned end, and the lookback before its start.
		if !p.strict || p.window != time.Hour+time.Minute+5*time.Minute {
			t.Fatalf("probe %s: strict=%v window=%v, want strict over everything the chart reads", p.metric, p.strict, p.window)
		}
	}
	for _, q := range f.queries {
		if strings.Contains(q, "ate_workerpool_workers{") && !strings.Contains(q, `cluster="a"`) {
			t.Fatalf("chart query lost the scope matchers: %s", q)
		}
	}
}

func TestSubstratePartialChartAnswerIsAnError(t *testing.T) {
	partial := result(series(map[string]string{"ate_worker_state": "idle"}, 2))
	partial.Annotations.Partial = true
	f := &fakeSubstrateProm{rules: []fakeRule{{"avg by", partial}}}
	charts := querySubstrateCharts(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts()))
	if c := chartByID(charts, "workers"); c.State != SubstrateChartError || len(c.Series) != 0 {
		t.Fatalf("workers = %s with %d series, want an error and no series", c.State, len(c.Series))
	}
}

func TestSubstrateNodeAttribution(t *testing.T) {
	const family = "count(last_over_time(atelet_snapshot_undurable_age_max_seconds{"
	for _, tc := range []struct {
		name  string
		rules []fakeRule
		want  string
	}{
		{"no atelet series", nil, SubstrateNotFound},
		{"series without node labels", []fakeRule{{family + "}", result(series(nil, 1))}}, SubstrateChartNotAttributable},
		{"labelled for other nodes", []fakeRule{{family + `node!=""}`, result(series(nil, 1))}, {family + "}", result(series(nil, 1))}}, SubstrateNotOnNode},
		{"unlabelled series could be this node's", []fakeRule{{family + `node=""}`, result(series(nil, 1))}, {family + `node!=""}`, result(series(nil, 1))}, {family + "}", result(series(nil, 1))}}, SubstrateChartNotAttributable},
		{"labelled for this node", []fakeRule{{family + `node="worker-1"}`, result(series(nil, 1))}, {family + `node!=""}`, result(series(nil, 1))}, {family + "}", result(series(nil, 1))}}, SubstrateAvailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := substrateNodeAttribution(context.Background(), &fakeSubstrateProm{rules: tc.rules}, "", "worker-1", time.Hour)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestAlignSubstrateRangeIsWholeSeconds(t *testing.T) {
	end := time.Date(2026, 11, 15, 12, 34, 56, 789_000_000, time.UTC)
	start, alignedEnd, step := alignSubstrateRange(end.Add(-10*time.Minute), end, 15*time.Second)
	window := alignedEnd.Sub(start)
	if window != 10*time.Minute || alignedEnd.Truncate(step) != alignedEnd || strings.Contains(window.String(), ".") {
		t.Fatalf("window %v ending %v, want a whole 10m ending on a step", window, alignedEnd)
	}
}

// A chart that reads two families probes them together, so series of the two
// from different clusters are caught even when no cluster label proves either.
func TestSubstrateChartFamiliesShareOneScopeProbe(t *testing.T) {
	resolve := func(_ context.Context, p scopeProbe) (string, SeriesIsolation, error) {
		if p.metric == "ate_workerpool_desired_workers" {
			if len(p.additionalMetrics) != 1 || p.additionalMetrics[0] != "ate_workerpool_ready_workers" {
				t.Fatalf("readiness probe metrics = %s + %v", p.metric, p.additionalMetrics)
			}
			return "", SeriesIsolation{}, ErrScopeAmbiguous
		}
		return "", SeriesIsolation{Mode: SeriesIsolationUnverified}, nil
	}
	charts := querySubstrateCharts(context.Background(), &fakeSubstrateProm{}, resolve, poolRequest(substratePoolCharts()))
	if c := chartByID(charts, "readiness"); c.State != SubstrateChartAmbiguous {
		t.Fatalf("readiness = %s, want ambiguous", c.State)
	}
	if c := chartByID(charts, "workers"); c.State == SubstrateChartAmbiguous {
		t.Fatal("a single-family chart should not inherit another chart's conflict")
	}
}

func TestSubstrateRatesCollapseHACopies(t *testing.T) {
	f := &fakeSubstrateProm{}
	querySubstrateCharts(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts()))
	for _, q := range f.queries {
		if strings.Contains(q, "rate(") && !strings.Contains(q, "max without (prometheus, prometheus_replica, replica) (rate(") {
			t.Fatalf("rate without HA collapse: %s", q)
		}
	}
}

// Averaging across API replicas weighs a replica scraped through two jobs twice.
func TestSubstrateWorkersRefuseDuplicateSources(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		{"count by (job) (count_over_time(ate_workerpool_workers", result(series(map[string]string{"job": "ateapi"}, 1), series(map[string]string{"job": "otel-collector"}, 1))},
	}}
	charts := querySubstrateCharts(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts()))
	if c := chartByID(charts, "workers"); c.State != SubstrateChartDuplicateSources {
		t.Fatalf("workers = %s, want duplicateSources", c.State)
	}
	if c := chartByID(charts, "readiness"); c.State == SubstrateChartDuplicateSources {
		t.Fatal("readiness takes a maximum and should not be refused")
	}
}

