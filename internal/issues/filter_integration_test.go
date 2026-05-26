package issues

import (
	"testing"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Filter integration tests — exercise ComposeWithStats with a compiled
// CEL filter, covering match/drop, eval-error stats, and the
// post-filter ordering invariant that limit applies last.

func TestCompose_WithCELFilter_MatchesAndDrops(t *testing.T) {
	// Two problem rows; a reason predicate should keep only the match.
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Namespace: "ns", Name: "crash", Severity: "critical", Reason: "CrashLoopBackOff"},
			{Kind: "Pod", Namespace: "ns", Name: "oom", Severity: "critical", Reason: "OOMKilled"},
		},
	}
	f, err := filter.CompileIssueFilter(`reason == "OOMKilled"`)
	if err != nil {
		t.Fatal(err)
	}
	out, stats := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 1 || out[0].Name != "oom" {
		t.Fatalf("expected single OOMKilled hit, got %+v", out)
	}
	if stats.FilterErrors != 0 {
		t.Errorf("clean filter, expected no eval errors, got %d", stats.FilterErrors)
	}
}

func TestCompose_FilterAppliedBeforeLimit(t *testing.T) {
	// Many problem issues + one filter-matching issue; limit=10 must
	// see all 50 critical problems, the filter narrows to a smaller
	// set, and limit caps that. Wrong order (limit-before-filter)
	// would discard issues silently.
	probs := make([]k8s.Problem, 0, 50)
	for i := 0; i < 50; i++ {
		probs = append(probs, k8s.Problem{Kind: "Pod", Namespace: "warn-ns", Name: "p", Severity: "high"})
	}
	probs = append(probs, k8s.Problem{Kind: "Pod", Namespace: "crit-ns", Name: "critical-one", Severity: "critical"})
	p := &fakeProvider{problems: probs}
	f, err := filter.CompileIssueFilter(`severity == "critical"`)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := ComposeWithStats(p, Filters{Filter: f, Limit: 10})
	if len(out) != 1 {
		t.Fatalf("filter should leave 1 critical issue, got %d", len(out))
	}
	if out[0].Name != "critical-one" {
		t.Errorf("filter dropped the critical one: %+v", out)
	}
}

func TestCompose_FilterEvalError_StatsPopulated(t *testing.T) {
	// Reference an unbound-but-syntactically-valid path that won't
	// resolve on any actual issue row — the dyn-typed env declares
	// these as known types, so the failure is at eval not compile.
	// (Using nonsense int comparison to force the error.)
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "p", Severity: "warning"},
		},
	}
	f, err := filter.CompileIssueFilter(`count > int(severity)`)
	if err != nil {
		t.Fatal(err)
	}
	out, stats := ComposeWithStats(p, Filters{Filter: f})
	if len(out) != 0 {
		t.Errorf("expected eval errors to drop the row, got %+v", out)
	}
	if stats.FilterErrors == 0 {
		t.Error("expected FilterErrors > 0 so agents can self-correct")
	}
	if stats.FilterErrorSample == "" {
		t.Error("expected FilterErrorSample populated")
	}
}
