package prometheus

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// fakeSubstrateProm answers each query from the first rule whose fragment it contains.
type fakeSubstrateProm struct {
	rules    []fakeRule
	names    []string
	namesErr error
	mu       sync.Mutex
	queries  []string
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
	base := func(sp substrateSpelling) string { return substratePoolSelector(sp, "agents", "pool") }
	return substrateRequest{base: base, charts: charts, start: end.Add(-time.Hour), end: end, step: time.Minute}
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
	if status, _ := substrateResponseStatus(charts); status != SubstrateAvailable {
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
		resolve := func(context.Context, scopeProbe) (string, SeriesIsolation, error) {
			return "", SeriesIsolation{}, tc.err
		}
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
			got, _, err := substrateNodeAttribution(context.Background(), &fakeSubstrateProm{rules: tc.rules}, substrateSpelling{}, "", "worker-1", time.Hour)
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
		if strings.Contains(q, "rate(") && !strings.Contains(q, "max without (prometheus, prometheus_replica, replica, __replica__) (rate(") {
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

// With a verified scope, a paired family that exists only outside it is a mismatch, not half a chart.
func TestSubstratePairedFamilyOutsideVerifiedScope(t *testing.T) {
	resolve := func(context.Context, scopeProbe) (string, SeriesIsolation, error) {
		return `cluster="a"`, SeriesIsolation{Mode: SeriesIsolationVerified}, nil
	}
	f := &fakeSubstrateProm{rules: []fakeRule{
		{`count(count_over_time(ate_workerpool_desired_workers{ate_workerpool_namespace="agents",ate_workerpool_name="pool",cluster="a"}`, result(series(nil, 1))},
		{`count(count_over_time(ate_workerpool_ready_workers{ate_workerpool_namespace="agents",ate_workerpool_name="pool"}`, result(series(nil, 1))},
	}}
	charts := querySubstrateCharts(context.Background(), f, resolve, poolRequest(substratePoolCharts()))
	if c := chartByID(charts, "readiness"); c.State != SubstrateChartMismatch || !strings.Contains(c.Reason, "ate_workerpool_ready_workers") {
		t.Fatalf("readiness = %s %q, want a mismatch naming the ready family", c.State, c.Reason)
	}
}

func TestSubstrateWorkerAverageCollapsesHACopies(t *testing.T) {
	f := &fakeSubstrateProm{}
	querySubstrateCharts(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts()))
	for _, q := range f.queries {
		if strings.Contains(q, "avg by") && !strings.Contains(q, "(max without (prometheus, prometheus_replica, replica, __replica__) (ate_workerpool_workers{") {
			t.Fatalf("worker average without HA collapse: %s", q)
		}
	}
}

func TestSubstrateResponseStatus(t *testing.T) {
	chart := func(state, reason string) SubstrateChart { return SubstrateChart{State: state, Reason: reason} }
	for _, tc := range []struct {
		name   string
		charts []SubstrateChart
		want   string
	}{
		{"nothing scraped", []SubstrateChart{chart(SubstrateChartNoSeries, ""), chart(SubstrateChartNoSeries, "")}, SubstrateNotFound},
		{"every chart failed", []SubstrateChart{chart(SubstrateChartError, "boom"), chart(SubstrateChartAmbiguous, "two clusters")}, SubstrateChartError},
		{"some drawn", []SubstrateChart{chart(SubstrateChartError, "boom"), chart(SubstrateChartOK, "")}, SubstrateAvailable},
	} {
		if got, reason := substrateResponseStatus(tc.charts); got != tc.want || (got == SubstrateChartError && reason != "boom") {
			t.Fatalf("%s: got %s %q, want %s", tc.name, got, reason, tc.want)
		}
	}
}

func TestSubstrateChartGaps(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		// Desired is reported, ready is not.
		{"max(ate_workerpool_desired_workers", result(series(nil, 5))},
		// Resumes counted, but no classic buckets.
		{"count(last_over_time(ate_actor_lifecycle_operation_duration_seconds_count", result(series(nil, 4))},
	}}
	charts := querySubstrateCharts(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts()))
	if c := chartByID(charts, "readiness"); c.State != SubstrateChartOK || c.Reason != "no data for ready" {
		t.Fatalf("readiness = %s %q, want ok noting the missing ready series", c.State, c.Reason)
	}
	if c := chartByID(charts, "resumeTime"); c.State != SubstrateChartNoSeries || !strings.Contains(c.Reason, "has ate_actor_lifecycle_operation_duration_seconds_count but none of the classic histogram buckets") {
		t.Fatalf("resumeTime = %s %q, want missing buckets named", c.State, c.Reason)
	}
}

func TestSubstrateNodeChartsWithoutNodeLabels(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		{`count(last_over_time(atelet_snapshot_upload_duration_seconds_count{node=""}`, result(series(nil, 1))},
	}}
	charts := []SubstrateChart{}
	for _, d := range substrateNodeCharts() {
		charts = append(charts, SubstrateChart{ID: d.id, State: SubstrateChartNoSeries})
	}
	markUnattributedNodeCharts(context.Background(), f, substrateSpelling{}, "", time.Hour, charts)
	if c := chartByID(charts, "uploadTime"); c.State != SubstrateChartNotAttributable {
		t.Fatalf("uploadTime = %s, want notAttributable", c.State)
	}
	if c := chartByID(charts, "unsavedSnapshotAge"); c.State != SubstrateChartNoSeries {
		t.Fatalf("unsavedSnapshotAge = %s, want noSeries", c.State)
	}
}

// Any of atelet's node families shows the node runs Substrate, not just one.
func TestSubstrateNodePresenceUsesEveryFamily(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		{`last_over_time(atelet_snapshot_upload_overdue_actors{node="worker-1"}`, result(series(nil, 1))},
		{`last_over_time(atelet_snapshot_upload_overdue_actors{node!=""}`, result(series(nil, 1))},
		{`last_over_time(atelet_snapshot_upload_overdue_actors{}`, result(series(nil, 1))},
	}}
	got, _, err := substrateNodeAttribution(context.Background(), f, substrateSpelling{}, "", "worker-1", time.Hour)
	if err != nil || got != SubstrateAvailable {
		t.Fatalf("got %s, %v; want available", got, err)
	}
}

// The restore chart also reads checkpoints: either family counts for presence and attribution.
func TestSubstrateRestoreChartReadsCheckpointsToo(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		{`count(last_over_time(ate_actor_checkpoint_duration_seconds_count{node=""}`, result(series(nil, 1))},
	}}
	charts := []SubstrateChart{}
	for _, d := range substrateNodeCharts() {
		charts = append(charts, SubstrateChart{ID: d.id, State: SubstrateChartNoSeries})
	}
	markUnattributedNodeCharts(context.Background(), f, substrateSpelling{}, "", time.Hour, charts)
	if c := chartByID(charts, "restoreTime"); c.State != SubstrateChartNotAttributable || !strings.Contains(c.Reason, "ate_actor_checkpoint_duration_seconds_count") {
		t.Fatalf("restoreTime = %s %q, want checkpoints named as unattributable", c.State, c.Reason)
	}
}

// Scope for a node's charts does not require every atelet family to be present here.
func TestSubstrateNodeScopeIgnoresAbsentFamilies(t *testing.T) {
	families := []substrateInstrument{instUndurable, instCheckpoint}
	resolve := func(context.Context, scopeProbe) (string, SeriesIsolation, error) {
		return `cluster="a"`, SeriesIsolation{Mode: SeriesIsolationVerified}, nil
	}
	f := &fakeSubstrateProm{rules: []fakeRule{
		{`count(count_over_time(ate_actor_checkpoint_duration_seconds_count{}`, result(series(nil, 1))},
	}}
	anywhere := func(substrateSpelling) string { return "" }
	if check := checkSubstrateFamilies(context.Background(), f, resolve, substrateSpellings{}, families, anywhere, time.Hour, true); check.state != "" {
		t.Fatalf("node scope = %s %q, want no refusal", check.state, check.reason)
	}
	if check := checkSubstrateFamilies(context.Background(), f, resolve, substrateSpellings{}, families, anywhere, time.Hour, false); check.state != SubstrateChartMismatch {
		t.Fatalf("paired check = %s, want mismatch", check.state)
	}
}

// Ready and desired workers are the controller's whole-number counts; worker
// states are averaged across API replicas and can be fractional.
func TestSubstrateChartUnits(t *testing.T) {
	units := map[string]string{}
	for _, d := range append(substratePoolCharts(), substrateNodeCharts()...) {
		units[d.id] = d.unit
	}
	if units["readiness"] != "count" || units["overdueUploads"] != "count" || units["workers"] == "count" {
		t.Fatalf("units = %v", units)
	}
}

// An empty chart always says why.
func TestSubstrateChartsExplainEmpty(t *testing.T) {
	for _, d := range append(substratePoolCharts(), substrateNodeCharts()...) {
		if d.emptyReason == "" {
			t.Errorf("%s has no empty reason", d.id)
		}
	}
}

// Another node reported through two jobs does not blank this node: duplicates
// are judged within the node's own series.
func TestSubstrateNodeDuplicatesJudgedPerNode(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		{`count by (job) (count_over_time(atelet_snapshot_upload_duration_seconds_count{}`, result(series(map[string]string{"job": "a"}, 1), series(map[string]string{"job": "b"}, 1))},
		{"count by (job)", result(series(map[string]string{"job": "a"}, 1))},
		{"count(", result(series(nil, 1))},
	}}
	end := time.Unix(10_000*60, 0)
	resp := collectSubstrateNode(context.Background(), f, unverifiedScope, "worker-1", "1h", end.Add(-time.Hour), end, time.Minute)
	if resp.Status != SubstrateAvailable {
		t.Fatalf("status = %s %q, want available", resp.Status, resp.Reason)
	}
	if c := chartByID(resp.Charts, "uploadTime"); c.State == SubstrateChartDuplicateSources {
		t.Fatalf("uploadTime refused for another node's duplicate: %q", c.Reason)
	}
}

func TestSubstrateRequestBoundsEchoesTheServedRange(t *testing.T) {
	for query, want := range map[string]string{"": "1h", "?range=bogus": "1h", "?range=6h": "6h"} {
		if got, _, _, _ := substrateRequestBounds(httptest.NewRequest("GET", "/x"+query, nil)); got != want {
			t.Errorf("%q: range = %s, want %s", query, got, want)
		}
	}
}
