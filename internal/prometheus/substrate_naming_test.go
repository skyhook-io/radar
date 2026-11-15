package prometheus

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func (f *fakeSubstrateProm) MetricNames(context.Context, []string, time.Time, time.Time) ([]string, error) {
	return f.names, f.namesErr
}

func TestSubstrateInstrumentStems(t *testing.T) {
	cases := []struct {
		inst            substrateInstrument
		escaped, dotted []string
	}{
		{instWorkers, []string{"ate_workerpool_workers"}, []string{"ate.workerpool.workers"}},
		{instCrashes, []string{"ate_actor_crashes_total", "ate_actor_crashes"}, []string{"ate.actor.crashes_total", "ate.actor.crashes"}},
		{instLifecycle, []string{"ate_actor_lifecycle_operation_duration_seconds", "ate_actor_lifecycle_operation_duration"}, []string{"ate.actor.lifecycle.operation.duration_seconds", "ate.actor.lifecycle.operation.duration"}},
		{instUndurable, []string{"atelet_snapshot_undurable_age_max_seconds", "atelet_snapshot_undurable_age_max"}, []string{"atelet.snapshot.undurable.age.max_seconds", "atelet.snapshot.undurable.age.max"}},
	}
	for _, tc := range cases {
		if got := tc.inst.stems(false); !slices.Equal(got, tc.escaped) {
			t.Errorf("%s escaped = %v, want %v", tc.inst.name, got, tc.escaped)
		}
		if got := tc.inst.stems(true); !slices.Equal(got, tc.dotted) {
			t.Errorf("%s dotted = %v, want %v", tc.inst.name, got, tc.dotted)
		}
	}
}

func TestVectorSelectorQuotesUTF8Names(t *testing.T) {
	if got := vectorSelector("ate_actor_crashes_total", `a="b"`); got != `ate_actor_crashes_total{a="b"}` {
		t.Fatalf("legacy = %s", got)
	}
	if got := vectorSelector("ate.actor.crashes", `"ate.workerpool.name"="p"`); got != `{"ate.actor.crashes","ate.workerpool.name"="p"}` {
		t.Fatalf("utf8 = %s", got)
	}
	if got := vectorSelector("ate.actor.crashes", ""); got != `{"ate.actor.crashes"}` {
		t.Fatalf("utf8 bare = %s", got)
	}
}

// GMP's names: dotted, histograms without the unit suffix.
var gmpNames = []string{
	"ate.workerpool.workers", "ate.workerpool.desired_workers", "ate.workerpool.ready_workers",
	"ate.actor.lifecycle.operation.duration_count", "ate.actor.lifecycle.operation.duration_bucket",
	"ate.actor.crashes",
}

func TestResolveSubstrateSpelling(t *testing.T) {
	base := func(sp substrateSpelling) string { return substratePoolSelector(sp, "agents", "pool") }
	resolve := func(f *fakeSubstrateProm) (substrateSpelling, string, error) {
		return resolveSubstrateSpelling(context.Background(), f, unverifiedScope, substratePoolInstruments, base, time.Hour)
	}

	sp, amb, err := resolve(&fakeSubstrateProm{})
	if err != nil || amb != "" || sp.dotted || sp.stem(instLifecycle) != "ate_actor_lifecycle_operation_duration_seconds" {
		t.Fatalf("nothing found: %+v %q %v, want the escaped default", sp, amb, err)
	}

	sp, _, _ = resolve(&fakeSubstrateProm{names: gmpNames})
	if !sp.dotted || sp.stem(instLifecycle) != "ate.actor.lifecycle.operation.duration" || sp.stem(instCrashes) != "ate.actor.crashes" {
		t.Fatalf("GMP: %+v", sp)
	}
	if got := sp.metric(instLifecycle, "_bucket"); got != "ate.actor.lifecycle.operation.duration_bucket" {
		t.Fatalf("GMP bucket = %s", got)
	}
	// Not exported here: named in the style of the instruments that are.
	if got := sp.stem(instAssignment); got != "ate.scheduler.assignment.duration" {
		t.Fatalf("missing instrument = %s, want the unsuffixed GMP name", got)
	}

	both := append([]string{"ate_workerpool_workers", "ate_actor_crashes_total"}, gmpNames...)
	sp, amb, _ = resolve(&fakeSubstrateProm{names: both, rules: []fakeRule{
		{`{"ate.workerpool.workers","ate.workerpool.namespace"="agents"`, result(series(nil, 1))},
	}})
	if amb != "" || !sp.dotted {
		t.Fatalf("both held, only dotted for this pool: %+v %q", sp, amb)
	}

	_, amb, _ = resolve(&fakeSubstrateProm{names: both, rules: []fakeRule{{"count(", result(series(nil, 1))}}})
	if amb == "" {
		t.Fatal("both spellings with series for this pool must be ambiguous")
	}

	// Two suffix styles of one instrument, both with series for this pool.
	suffixes := []string{"ate_actor_lifecycle_operation_duration_seconds_count", "ate_actor_lifecycle_operation_duration_count"}
	_, amb, _ = resolve(&fakeSubstrateProm{names: suffixes, rules: []fakeRule{{"count(", result(series(nil, 1))}}})
	if !strings.Contains(amb, "appears as both") {
		t.Fatalf("two suffix styles with series = %q, want ambiguous", amb)
	}
	// Only one of them has series for this pool: it is read.
	sp, amb, _ = resolve(&fakeSubstrateProm{names: suffixes, rules: []fakeRule{{"ate_actor_lifecycle_operation_duration_count{", result(series(nil, 1))}}})
	if amb != "" || sp.stem(instLifecycle) != "ate_actor_lifecycle_operation_duration" {
		t.Fatalf("one suffix style live = %+v %q", sp, amb)
	}

	// The dotted series belong to another cluster: this pool reads the escaped ones.
	otherCluster := func(_ context.Context, p scopeProbe) (string, SeriesIsolation, error) {
		if strings.Contains(p.metric, ".") {
			return "", SeriesIsolation{}, ErrScopeMismatch
		}
		return "", SeriesIsolation{}, nil
	}
	sp, amb, err = resolveSubstrateSpelling(context.Background(), &fakeSubstrateProm{names: both, rules: []fakeRule{{"count(", result(series(nil, 1))}}}, otherCluster, substratePoolInstruments, base, time.Hour)
	if err != nil || amb != "" || sp.dotted {
		t.Fatalf("dotted series of another cluster: %+v %q %v", sp, amb, err)
	}

	// The suffixed name is another cluster's; the unsuffixed one is this pool's.
	onlyUnsuffixed := func(_ context.Context, p scopeProbe) (string, SeriesIsolation, error) {
		if strings.Contains(p.metric, "_seconds_count") {
			return "", SeriesIsolation{}, ErrScopeMismatch
		}
		return "", SeriesIsolation{}, nil
	}
	sp, amb, err = resolveSubstrateSpelling(context.Background(), &fakeSubstrateProm{names: suffixes, rules: []fakeRule{{"count(", result(series(nil, 1))}}}, onlyUnsuffixed, substratePoolInstruments, base, time.Hour)
	if err != nil || amb != "" || sp.stem(instLifecycle) != "ate_actor_lifecycle_operation_duration" {
		t.Fatalf("per-name scope: %+v %q %v", sp, amb, err)
	}

	if _, _, err = resolve(&fakeSubstrateProm{namesErr: errors.New("boom")}); err == nil {
		t.Fatal("a failed name lookup must be an error")
	}
}

// Under the dotted spelling every query selects dotted names and labels, and
// no by clause names a dotted label (GMP rejects them).
func TestSubstrateDottedQueries(t *testing.T) {
	f := &fakeSubstrateProm{names: gmpNames, rules: []fakeRule{
		{"count by (job)", result(series(map[string]string{"job": "ateapi"}, 1))},
		{"sum by (ate_worker_state, ate_sandbox_class) (avg by", result(series(map[string]string{"ate_worker_state": "idle"}, 2))},
	}}
	end := time.Unix(10_000*60, 0)
	resp := collectSubstratePool(context.Background(), f, unverifiedScope, "agents", "pool", "1h", end.Add(-time.Hour), end, time.Minute)
	if resp.Status == SubstrateChartError {
		t.Fatalf("status = %s %q", resp.Status, resp.Reason)
	}
	if resp.Verdict == nil {
		t.Fatal("no verdict on an available pool")
	}
	byClause := regexp.MustCompile(`(?:by|without) \(([^)]*)\)`)
	sawWorkers := false
	for _, q := range f.queries {
		if strings.Contains(q, "ate_workerpool_workers{") || strings.Contains(q, "ate_actor_") {
			t.Errorf("escaped name in a dotted query: %s", q)
		}
		for _, m := range byClause.FindAllStringSubmatch(q, -1) {
			if strings.Contains(m[1], ".") {
				t.Errorf("dotted label in a by clause: %s", q)
			}
		}
		if strings.Contains(q, `{"ate.workerpool.workers","ate.workerpool.namespace"="agents","ate.workerpool.name"="pool"}`) && strings.Contains(q, `label_replace(`) {
			sawWorkers = true
		}
	}
	if !sawWorkers {
		t.Fatalf("no normalized dotted workers query in %v", f.queries)
	}
	if c := chartByID(resp.Charts, "resumeTime"); !strings.Contains(c.Source, "ate.actor.lifecycle.operation.duration") {
		t.Fatalf("resumeTime source = %q", c.Source)
	}
}

// The API server scraped under escaped names and the controller pushing dotted
// names over OTLP: each is read in its own spelling, and the shortfall query
// combines both.
func TestSubstrateSpellingPerComponent(t *testing.T) {
	names := []string{"ate_workerpool_workers", "ate_actor_crashes_total", "ate.workerpool.desired_workers", "ate.workerpool.ready_workers"}
	base := func(sp substrateSpelling) string { return substratePoolSelector(sp, "agents", "pool") }
	sps, err := resolveSubstrateSpellings(context.Background(), &fakeSubstrateProm{names: names}, unverifiedScope, substratePoolInstruments, base, time.Hour)
	if err != nil || sps.of(instWorkers).dotted || !sps.of(instDesired).dotted || sps.of(instWorkers).ambiguous != "" || sps.of(instDesired).ambiguous != "" {
		t.Fatalf("spellings = %+v %v", sps, err)
	}

	f := &fakeSubstrateProm{names: names, rules: []fakeRule{
		{"count by (job)", result(series(map[string]string{"job": "ateapi"}, 1))},
		{"sum by (ate_worker_state, ate_sandbox_class) (avg by", result(series(map[string]string{"ate_worker_state": "idle"}, 2))},
		{verdictTotal, result(series(nil, 2))},
	}}
	end := time.Unix(10_000*60, 0)
	resp := collectSubstratePool(context.Background(), f, unverifiedScope, "agents", "pool", "1h", end.Add(-time.Hour), end, time.Minute)
	if resp.Status == SubstrateChartError {
		t.Fatalf("status = %s %q", resp.Status, resp.Reason)
	}
	sawShort := false
	for _, q := range f.queries {
		if strings.Contains(q, "< bool") {
			sawShort = strings.Contains(q, `ate_workerpool_workers{ate_workerpool_namespace="agents"`) && strings.Contains(q, `{"ate.workerpool.desired_workers","ate.workerpool.namespace"="agents"`)
			break
		}
	}
	if !sawShort {
		t.Fatalf("no shortfall query combining both spellings in %v", f.queries)
	}
	if c := chartByID(resp.Charts, "readiness"); !strings.Contains(c.Source, "ate.workerpool.desired_workers") {
		t.Fatalf("readiness source = %q", c.Source)
	}
}

// One component under two names refuses only the charts that read it.
func TestSubstrateAmbiguousComponentRefusesOnlyItsCharts(t *testing.T) {
	names := []string{"ate_workerpool_workers", "ate_workerpool_desired_workers", "ate.workerpool.desired_workers", "ate_workerpool_ready_workers"}
	f := &fakeSubstrateProm{names: names, rules: []fakeRule{{"count by (job)", result(series(map[string]string{"job": "ateapi"}, 1))}, {"count(", result(series(nil, 1))}}}
	end := time.Unix(10_000*60, 0)
	resp := collectSubstratePool(context.Background(), f, unverifiedScope, "agents", "pool", "1h", end.Add(-time.Hour), end, time.Minute)
	if c := chartByID(resp.Charts, "readiness"); c.State != SubstrateChartAmbiguous || !strings.Contains(c.Reason, "atecontroller") {
		t.Fatalf("readiness = %s %q, want ambiguous", c.State, c.Reason)
	}
	if c := chartByID(resp.Charts, "workers"); c.State == SubstrateChartAmbiguous {
		t.Fatalf("workers refused for the controller's ambiguity: %q", c.Reason)
	}
}

// Two suffix styles of the lifecycle histogram refuse it, not the worker gauge
// the same component reports under one name.
func TestSubstrateSuffixCollisionRefusesOnlyItsInstrument(t *testing.T) {
	names := []string{"ate_workerpool_workers", "ate_actor_lifecycle_operation_duration_seconds_count", "ate_actor_lifecycle_operation_duration_count"}
	f := &fakeSubstrateProm{names: names, rules: []fakeRule{{"count by (job)", result(series(map[string]string{"job": "ateapi"}, 1))}, {"count(", result(series(nil, 1))}}}
	base := func(sp substrateSpelling) string { return substratePoolSelector(sp, "agents", "pool") }
	sps, err := resolveSubstrateSpellings(context.Background(), f, unverifiedScope, substratePoolInstruments, base, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkSubstrateFamilies(context.Background(), f, unverifiedScope, sps, []substrateInstrument{instWorkers}, base, time.Hour, false); got.state != "" {
		t.Fatalf("workers = %s %q, want readable", got.state, got.reason)
	}
	if got := checkSubstrateFamilies(context.Background(), f, unverifiedScope, sps, []substrateInstrument{instLifecycle}, base, time.Hour, false); got.state != SubstrateChartAmbiguous {
		t.Fatalf("lifecycle = %s, want ambiguous", got.state)
	}
}
