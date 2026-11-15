package prometheus

import (
	"context"
	"strings"
	"testing"
)

const (
	verdictShort   = "< bool"
	verdictAnyFree = `max(ate_workerpool_workers{`
	verdictFree    = `ate_worker_state=~"idle|partial",ate_sandbox_class!="unknown"}))`
	verdictTotal   = `ate_worker_state=~"idle|partial|at_capacity",ate_sandbox_class!="unknown"}))`
	verdictFresh   = `count(ate_actor_lifecycle_operation_duration_seconds_count{ate_workerpool_namespace="agents",ate_workerpool_name="pool",ate_actor_operation_name="resume"`
)

func okCharts(ids ...string) []SubstrateChart {
	out := make([]SubstrateChart, len(ids))
	for i, id := range ids {
		out[i] = SubstrateChart{ID: id, State: SubstrateChartOK}
	}
	return out
}

func poolVerdict(t *testing.T, rules []fakeRule, charts []SubstrateChart) (*SubstrateVerdict, *fakeSubstrateProm) {
	t.Helper()
	f := &fakeSubstrateProm{rules: append([]fakeRule{{"count by (job)", result(series(map[string]string{"job": "ateapi"}, 1))}}, rules...)}
	return newSubstrateRun(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts())).poolVerdict(charts), f
}

func fact(v *SubstrateVerdict, id string) SubstrateFact {
	for _, f := range v.Facts {
		if f.ID == id {
			return f
		}
	}
	return SubstrateFact{}
}

func TestSubstratePoolVerdictStates(t *testing.T) {
	charts := okCharts("resumeTime", "resumeFailures", "crashes")
	cases := []struct {
		name  string
		rules []fakeRule
		want  string
	}{
		{"short", []fakeRule{{verdictShort, result(series(nil, 1))}, {verdictFree, result(series(nil, 0))}, {verdictTotal, result(series(nil, 2))}}, SubstrateVerdictShort},
		{"full", []fakeRule{{verdictShort, result(series(nil, 0))}, {verdictAnyFree, result(series(nil, 0))}, {verdictFree, result(series(nil, 0))}, {verdictTotal, result(series(nil, 4))}}, SubstrateVerdictFull},
		// No idle or partial series at all while workers exist: nothing free.
		{"full without free series", []fakeRule{{verdictShort, result(series(nil, 0))}, {verdictTotal, result(series(nil, 4))}}, SubstrateVerdictFull},
		{"serving", []fakeRule{{verdictShort, result(series(nil, 0))}, {verdictAnyFree, result(series(nil, 1))}, {verdictFree, result(series(nil, 1.5))}, {verdictTotal, result(series(nil, 4))}}, SubstrateVerdictServing},
		{"no workers", []fakeRule{{verdictShort, result(series(nil, 0))}, {verdictTotal, result(series(nil, 0))}}, SubstrateVerdictNoWorkers},
		{"no counts", nil, SubstrateVerdictUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := poolVerdict(t, tc.rules, charts)
			if v.State != tc.want {
				t.Fatalf("state = %s (%q), want %s", v.State, v.Reason, tc.want)
			}
		})
	}
}

// Short needs every point of the five minutes, and the condition now.
func TestSubstrateShortRequiresAFullHold(t *testing.T) {
	_, f := poolVerdict(t, []fakeRule{{verdictTotal, result(series(nil, 2))}}, okCharts())
	for _, q := range f.queries {
		if strings.Contains(q, verdictShort) {
			if !strings.Contains(q, "count_over_time(") || !strings.Contains(q, ">= bool 6") || strings.Count(q, "< bool") != 3 {
				t.Fatalf("short query = %s", q)
			}
			return
		}
	}
	t.Fatal("no short query")
}

func TestSubstratePoolVerdictFreeWorkers(t *testing.T) {
	v, _ := poolVerdict(t, []fakeRule{{verdictShort, result(series(nil, 0))}, {verdictAnyFree, result(series(nil, 1))}, {verdictFree, result(series(nil, 1.5))}, {verdictTotal, result(series(nil, 4))}}, okCharts())
	free := fact(v, "freeWorkers")
	if free.State != SubstrateFactOK || *free.Value != 1.5 || *free.Of != 4 {
		t.Fatalf("free = %+v", free)
	}

	v, _ = poolVerdict(t, nil, okCharts())
	if free := fact(v, "freeWorkers"); free.State != SubstrateFactUnavailable || free.Value != nil || free.Reason == "" {
		t.Fatalf("free with no counts = %+v, want unavailable with a reason, never 0", free)
	}
}

// The workers family through two scrape jobs leaves the verdict unknown, for the chart's reason.
func TestSubstratePoolVerdictRefusesDuplicateWorkers(t *testing.T) {
	f := &fakeSubstrateProm{rules: []fakeRule{
		{"count by (job) (count_over_time(ate_workerpool_workers", result(series(map[string]string{"job": "a"}, 1), series(map[string]string{"job": "b"}, 1))},
		{verdictShort, result(series(nil, 1))},
	}}
	v := newSubstrateRun(context.Background(), f, unverifiedScope, poolRequest(substratePoolCharts())).poolVerdict(okCharts())
	if v.State != SubstrateVerdictUnknown || !strings.Contains(v.Reason, "more than one scrape job") {
		t.Fatalf("verdict = %+v", v)
	}
	for _, q := range f.queries {
		if strings.Contains(q, verdictShort) {
			t.Fatalf("queried the verdict after a refused check: %s", q)
		}
	}
}

func TestSubstratePoolVerdictFacts(t *testing.T) {
	charts := []SubstrateChart{
		{ID: "resumeTime", State: SubstrateChartOK},
		{ID: "resumeFailures", State: SubstrateChartEmpty},
		{ID: "crashes", State: SubstrateChartDuplicateSources, Reason: "reported through more than one scrape job"},
	}
	v, f := poolVerdict(t, []fakeRule{{"histogram_quantile(0.95", result(series(nil, 0.8))}, {verdictFresh, result(series(nil, 3))}}, charts)
	if p95 := fact(v, "resumeP95"); p95.State != SubstrateFactOK || *p95.Value != 0.8 {
		t.Fatalf("p95 = %+v", p95)
	}
	if failed := fact(v, "resumeFailures"); failed.State != SubstrateFactEmpty || failed.Reason == "" {
		t.Fatalf("failures on an empty chart = %+v", failed)
	}
	if crashes := fact(v, "crashes"); crashes.State != SubstrateFactUnavailable || crashes.Reason != "reported through more than one scrape job" {
		t.Fatalf("crashes on a refused chart = %+v", crashes)
	}
	for _, q := range f.queries {
		if strings.Contains(q, "histogram_quantile(0.95") && !strings.Contains(q, "[1h0m0s]") {
			t.Fatalf("fact not read over the request's range: %s", q)
		}
		if strings.Contains(q, "increase(") {
			t.Fatalf("queried the fact of a refused chart: %s", q)
		}
	}
}

func nodeRun(f *fakeSubstrateProm) *substrateRun {
	req := poolRequest(substrateNodeCharts())
	req.base = func(sp substrateSpelling) string { return substrateNodeSelector(sp, "worker-1") }
	return newSubstrateRun(context.Background(), f, unverifiedScope, req)
}

func TestSubstrateNodeVerdict(t *testing.T) {
	charts := okCharts("unsavedSnapshotAge", "overdueUploads")
	band := func(b string) map[string]string { return map[string]string{"ate_snapshot_upload_overdue_band": b} }
	f := &fakeSubstrateProm{rules: []fakeRule{
		{"max by (ate_snapshot_upload_overdue_band)", result(series(band("under_1m"), 0), series(band("1m_to_5m"), 2), series(band("over_15m"), 1))},
		{"max(atelet_snapshot_undurable_age_max_seconds", result(series(nil, 420))},
		{"count(atelet_snapshot_undurable_age_max_seconds{", result(series(nil, 1))},
	}}
	v := nodeRun(f).nodeVerdict(charts)
	overdue := fact(v, "overdueUploads")
	if v.State != SubstrateVerdictBehind || *overdue.Value != 3 || len(overdue.Parts) != 2 || overdue.Parts[0].Label != "over_15m" {
		t.Fatalf("verdict = %+v, overdue = %+v", v, overdue)
	}
	if oldest := fact(v, "oldestUnsaved"); *oldest.Value != 420 {
		t.Fatalf("oldest = %+v", oldest)
	}

	f = &fakeSubstrateProm{rules: []fakeRule{{"max by (ate_snapshot_upload_overdue_band)", result(series(band("under_1m"), 0))}}}
	v = nodeRun(f).nodeVerdict(charts)
	if v.State != SubstrateVerdictCurrent {
		t.Fatalf("all bands zero = %+v", v)
	}
	// The age gauge has data in the range and none now: unknown, not "None".
	if oldest := fact(v, "oldestUnsaved"); oldest.State != SubstrateFactUnavailable {
		t.Fatalf("stopped age gauge = %+v", oldest)
	}

	// The chart has series in the range, none now: atelet stopped reporting.
	if v := nodeRun(&fakeSubstrateProm{}).nodeVerdict(charts); v.State != SubstrateVerdictUnknown || v.Reason == "" {
		t.Fatalf("stale = %+v", v)
	}

	// No overdue series for this node while the family exists in the cluster.
	if v := nodeRun(&fakeSubstrateProm{}).nodeVerdict([]SubstrateChart{{ID: "unsavedSnapshotAge", State: SubstrateChartOK}, {ID: "overdueUploads", State: SubstrateChartEmpty}}); v.State != SubstrateVerdictCurrent || fact(v, "overdueUploads").State != SubstrateFactEmpty {
		t.Fatalf("empty = %+v", v)
	}

	refused := []SubstrateChart{{ID: "overdueUploads", State: SubstrateChartMismatch, Reason: "this cluster's identity labels do not appear on these series"}}
	if v := nodeRun(&fakeSubstrateProm{}).nodeVerdict(refused); v.State != SubstrateVerdictUnknown || v.Reason != refused[0].Reason {
		t.Fatalf("refused = %+v", v)
	}
}

// A summary over the hour outlives the series it summarizes: once they stop,
// the fact is unavailable, not the hour's stale number.
func TestSubstrateRangeFactRequiresCurrentSeries(t *testing.T) {
	v, _ := poolVerdict(t, []fakeRule{{"histogram_quantile(0.95", result(series(nil, 0.8))}}, okCharts("resumeTime"))
	if p95 := fact(v, "resumeP95"); p95.State != SubstrateFactUnavailable || !strings.Contains(p95.Reason, "stopped reporting") {
		t.Fatalf("p95 after the series stopped = %+v", p95)
	}
}

// Without the controller's desired count the pool is still judged from the
// API server's counts, and says the shortfall was not.
func TestSubstratePoolVerdictWithoutDesiredWorkers(t *testing.T) {
	v, _ := poolVerdict(t, []fakeRule{{verdictAnyFree, result(series(nil, 1))}, {verdictFree, result(series(nil, 2))}, {verdictTotal, result(series(nil, 2))}}, okCharts())
	if v.State != SubstrateVerdictServing || !strings.Contains(v.Reason, "desired worker count") {
		t.Fatalf("verdict = %s %q, want serving qualified by the missing desired count", v.State, v.Reason)
	}
	if free := fact(v, "freeWorkers"); free.State != SubstrateFactOK || *free.Value != 2 || *free.Of != 2 {
		t.Fatalf("free = %+v", free)
	}
}

// A worker state this reading does not know could be capacity or not.
func TestSubstratePoolVerdictUnknownWorkerState(t *testing.T) {
	v, _ := poolVerdict(t, []fakeRule{
		{`!~"idle|partial|at_capacity|unschedulable"`, result(series(map[string]string{"ate_worker_state": "draining"}, 1))},
		{verdictTotal, result(series(nil, 2))},
	}, okCharts())
	if v.State != SubstrateVerdictUnknown || !strings.Contains(v.Reason, "draining") {
		t.Fatalf("verdict = %s %q", v.State, v.Reason)
	}
}

// A suspend still reported says nothing about resumes that stopped.
func TestSubstrateRangeFactFreshnessIsPerPopulation(t *testing.T) {
	v, f := poolVerdict(t, []fakeRule{
		{"histogram_quantile(0.95", result(series(nil, 0.8))},
		{`count(ate_actor_lifecycle_operation_duration_seconds_count{ate_workerpool_namespace="agents",ate_workerpool_name="pool"})`, result(series(nil, 3))},
	}, okCharts("resumeTime"))
	if p95 := fact(v, "resumeP95"); p95.State != SubstrateFactUnavailable {
		t.Fatalf("p95 kept fresh by another operation = %+v", p95)
	}
	for _, q := range f.queries {
		if strings.HasPrefix(q, "count(ate_actor_lifecycle_operation_duration_seconds_count{") && strings.Contains(q, `ate_actor_operation_name="resume"`) && strings.Contains(q, `error_type=""`) {
			return
		}
	}
	t.Fatalf("no freshness check of successful resumes in %v", f.queries)
}
