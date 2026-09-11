package k8s

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIssueTimingFromConditionLTT(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name            string
		failingSince    time.Time
		resourceCreated time.Time
		basis           string
		wantIssueTiming string
		wantBasis       string
	}{
		{
			name:            "started_at_resource_creation: condition failed at creation (0s healthy)",
			failingSince:    now.Add(-2 * time.Hour),
			resourceCreated: now.Add(-2 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "condition",
		},
		{
			name: "started_at_resource_creation: condition failed within slop window (10s healthy)",
			// Resource created 2h ago; condition failed 10s after creation (healthyFor=10s < 30s slop).
			failingSince:    now.Add(-2*time.Hour + 10*time.Second),
			resourceCreated: now.Add(-2 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "condition",
		},
		{
			name:            "started_after_resource_was_healthy: clearly past initialization (2h healthy before failure)",
			failingSince:    now.Add(-30 * time.Minute),
			resourceCreated: now.Add(-3 * time.Hour),
			basis:           "owner_condition",
			wantIssueTiming: "started_after_resource_was_healthy",
			wantBasis:       "owner_condition",
		},
		{
			name:            "gray zone: 5 min healthy on old resource — omit",
			failingSince:    now.Add(-55 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			// 1min healthy out of 1h (ratio 1.7%) — crashed right after deploy,
			// the ratio rule classifies started_at_resource_creation regardless of current age.
			name:            "started_at_resource_creation: ratio rule — 1min healthy out of 1h",
			failingSince:    now.Add(-59 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "condition",
		},
		{
			// healthyFor=1min — inside the deploy window, so the healthy period is
			// reconciliation lag rather than a clean bill of health.
			name:            "started_at_resource_creation: crash within 1min of deploy",
			failingSince:    now.Add(-6 * time.Minute),
			resourceCreated: now.Add(-7 * time.Minute),
			basis:           "owner_condition",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "owner_condition",
		},
		{
			// ratio rule: 2min healthy out of 10min → 20% < 25%, under 5min → started_at_resource_creation
			name:            "started_at_resource_creation: ratio rule — 2min healthy out of 10min total",
			failingSince:    now.Add(-8 * time.Minute),
			resourceCreated: now.Add(-10 * time.Minute),
			basis:           "condition",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "condition",
		},
		{
			// healthyFor=7min — past the deploy window, short of confirmed-healthy.
			name:            "gray zone: 7min healthy is neither deploy-time nor confirmed",
			failingSince:    now.Add(-13 * time.Minute),
			resourceCreated: now.Add(-20 * time.Minute),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			// healthyFor=4min. The verdict must not depend on the resource's age,
			// which is what the removed ratio term made it do.
			name:            "gray zone: 4min healthy, whatever the resource's age",
			failingSince:    now.Add(-11 * time.Minute),
			resourceCreated: now.Add(-15 * time.Minute),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			// healthyFor=5min on an hour-old resource: past the deploy window.
			name:            "gray zone: 5min healthy on an old resource",
			failingSince:    now.Add(-55 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			name:            "zero failingSince: omit",
			failingSince:    time.Time{},
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			name:            "zero resourceCreated: omit",
			failingSince:    now.Add(-30 * time.Minute),
			resourceCreated: time.Time{},
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			name:            "both zero: omit",
			failingSince:    time.Time{},
			resourceCreated: time.Time{},
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			name:            "future failingSince (clock skew): omit",
			failingSince:    now.Add(5 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			// LTT predating creation = failing for this object's entire
			// lifetime — deliberately "started_at_resource_creation", not omitted (adopted or
			// recreated resources whose condition survived).
			name:            "started_at_resource_creation: LTT predates creation (negative healthyFor)",
			failingSince:    now.Add(-3 * time.Hour),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "condition",
		},
		{
			name:            "started_at_resource_creation: deletion basis preserved",
			failingSince:    now.Add(-3 * time.Hour),
			resourceCreated: now.Add(-3 * time.Hour),
			basis:           "deletion",
			wantIssueTiming: "started_at_resource_creation",
			wantBasis:       "deletion",
		},
		{
			name:            "started_after_resource_was_healthy: spec basis preserved",
			failingSince:    now.Add(-10 * time.Minute),
			resourceCreated: now.Add(-2 * time.Hour),
			basis:           "spec",
			wantIssueTiming: "started_after_resource_was_healthy",
			wantBasis:       "spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IssueTimingFromConditionLTT(tt.failingSince, tt.resourceCreated, tt.basis)
			if got.IssueTiming != tt.wantIssueTiming {
				t.Errorf("IssueTiming = %q, want %q (healthyFor = %v)",
					got.IssueTiming, tt.wantIssueTiming,
					(tt.resourceCreated.Sub(tt.failingSince)).String())
			}
			if got.Basis != tt.wantBasis {
				t.Errorf("Basis = %q, want %q", got.Basis, tt.wantBasis)
			}
		})
	}
}

func TestSetDetectionOnsetRequiresNonFutureTimestamp(t *testing.T) {
	now := time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC)

	for _, onset := range []time.Time{time.Time{}, now.Add(time.Second)} {
		detection := Detection{Duration: "old", DurationSeconds: 10, OnsetAt: now.Add(-time.Hour)}
		setDetectionOnset(&detection, now, onset)
		if !detection.OnsetUnknown || !detection.OnsetAt.IsZero() || detection.Duration != "" || detection.DurationSeconds != 0 {
			t.Fatalf("invalid onset %v was accepted: %+v", onset, detection)
		}
	}

	detection := Detection{}
	setDetectionOnset(&detection, now, now)
	if detection.OnsetUnknown || !detection.OnsetAt.Equal(now) || detection.DurationSeconds != 0 {
		t.Fatalf("exact-now onset was lost: %+v", detection)
	}
}

// CAPI condition readers must not classify timing when no exact transition
// timestamp exists.
func TestCapiIssueTiming_MissingTimestampGuard(t *testing.T) {
	if timing, basis := capiIssueTiming(time.Time{}, false, time.Now().Add(-3*time.Hour)); timing != "" || basis != "" {
		t.Errorf("missing transition must omit issue_timing, got (%q, %q)", timing, basis)
	}
	if timing, basis := capiIssueTiming(time.Now().Add(-time.Hour), true, time.Now().Add(-3*time.Hour)); timing != "started_after_resource_was_healthy" || basis != "condition" {
		t.Errorf("2h healthy then failing 1h must be started_after_resource_was_healthy/condition, got (%q, %q)", timing, basis)
	}
}

// terminatingProblem runs the classifier against deletionTimestamp: deletion
// right after creation is at-creation (create-then-delete churn), deletion
// after a real existence window is post-healthy. A hardcoded post-healthy
// label would overstate the evidence for the former.
func TestTerminatingProblemIssueTiming(t *testing.T) {
	now := time.Now()
	obj := func(created, deleted time.Time) metav1.Object {
		dt := metav1.NewTime(deleted)
		return &metav1.ObjectMeta{Name: "x", Namespace: "ns", CreationTimestamp: metav1.NewTime(created), DeletionTimestamp: &dt}
	}

	healthyThenDeleted, ok := terminatingProblem("ConfigMap", "", obj(now.Add(-2*time.Hour), now.Add(-30*time.Minute)), now)
	if !ok || healthyThenDeleted.IssueTiming != "started_after_resource_was_healthy" || healthyThenDeleted.IssueTimingBasis != "deletion" {
		t.Errorf("2h-old resource stuck 30m = (%q, %q), want (started_after_resource_was_healthy, deletion); ok=%v",
			healthyThenDeleted.IssueTiming, healthyThenDeleted.IssueTimingBasis, ok)
	}

	createdThenDeleted, ok := terminatingProblem("ConfigMap", "", obj(now.Add(-1*time.Hour), now.Add(-1*time.Hour).Add(15*time.Second)), now)
	if !ok || createdThenDeleted.IssueTiming != "started_at_resource_creation" {
		t.Errorf("deleted 15s after creation = (%q, %q), want started_at_resource_creation; ok=%v",
			createdThenDeleted.IssueTiming, createdThenDeleted.IssueTimingBasis, ok)
	}
}

// The classification must depend only on how long the resource was healthy, not
// on how long ago that was. Comparing the healthy window against the resource's
// current age decays as the resource ages, so one unchanged pair of timestamps
// answered "no verdict" early in a resource's life and "started at resource
// creation" later, with nothing about the evidence having changed.
func TestIssueTimingIsStableAsTheResourceAges(t *testing.T) {
	const healthyFor = 4 * time.Minute

	// A cluster that came up, served for four minutes, then broke — asked at
	// five minutes old, at an hour old, and at a week old.
	for _, age := range []time.Duration{5 * time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		now := time.Now()
		created := now.Add(-age)
		got := IssueTimingFromConditionLTT(created.Add(healthyFor), created, "condition")
		if got.IssueTiming == "started_at_resource_creation" {
			t.Errorf("age %v: claimed the issue started at creation, but the resource was healthy for %v first", age, healthyFor)
		}
	}
}

// The same property for a window short enough to classify: it must classify at
// every age, not only once the resource is old enough.
func TestIssueTimingClassifiesTheDeployWindowImmediately(t *testing.T) {
	const healthyFor = 90 * time.Second

	for _, age := range []time.Duration{2 * time.Minute, 10 * time.Minute, 48 * time.Hour} {
		now := time.Now()
		created := now.Add(-age)
		got := IssueTimingFromConditionLTT(created.Add(healthyFor), created, "condition")
		if got.IssueTiming != "started_at_resource_creation" {
			t.Errorf("age %v: IssueTiming = %q, want started_at_resource_creation", age, got.IssueTiming)
		}
	}
}

// The rule-3 boundary is inclusive, matching what its comment claims. A
// comment that misdescribes its own code is how the wrong behaviour survives
// review, which is the same shape of defect this function was fixed for.
func TestIssueTimingConfirmedHealthyBoundaryIsInclusive(t *testing.T) {
	now := time.Now()
	created := now.Add(-3 * time.Hour)
	// Exactly 10 minutes of healthy operation before the failure.
	got := IssueTimingFromConditionLTT(created.Add(10*time.Minute), created, "condition")
	if got.IssueTiming != "started_after_resource_was_healthy" {
		t.Errorf("IssueTiming = %q, want started_after_resource_was_healthy at exactly 10m healthy", got.IssueTiming)
	}
}
