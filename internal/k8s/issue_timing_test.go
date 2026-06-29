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
			// ratio rule: product-catalog case — crashes 1min after 7min-old deploy
			// healthyFor=1min (14% of 7min) < 25% AND < 5min → started_at_resource_creation
			name:            "started_at_resource_creation: ratio rule — crash within 1min of 7min-old deploy",
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
			// ratio rule doesn't fire: healthyFor=7min (35% of 20min), above 25% ratio
			// AND above 5min cap → falls to gray zone (between 5min and 10min healthy)
			name:            "gray zone: 7min healthy out of 20min — ratio 35% above threshold",
			failingSince:    now.Add(-13 * time.Minute),
			resourceCreated: now.Add(-20 * time.Minute),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			// ratio rule cap: healthyFor=4min, resourceAge=15min (27%) but healthyFor
			// < 5min check only — wait, 4/15=26.7%>25% so no ratio rule → gray zone
			name:            "gray zone: ratio rule doesn't apply when ratio >= 25%",
			failingSince:    now.Add(-11 * time.Minute),
			resourceCreated: now.Add(-15 * time.Minute),
			basis:           "condition",
			wantIssueTiming: "",
			wantBasis:       "",
		},
		{
			// ratio rule doesn't fire when healthyFor >= 5min even with low ratio
			name:            "gray zone: ratio rule capped at 5min healthyFor",
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

// capiIssueTiming wraps IssueTimingFromConditionLTT behind a dur==0 guard: CAPI condition
// readers fall back to resource age when no LTT exists, and that fallback
// duration must never reach the classifier.
func TestCapiIssueTiming_ZeroDurationGuard(t *testing.T) {
	if timing, basis := capiIssueTiming(0, time.Now().Add(-3*time.Hour)); timing != "" || basis != "" {
		t.Errorf("dur=0 must omit issue_timing, got (%q, %q)", timing, basis)
	}
	if timing, basis := capiIssueTiming(time.Hour, time.Now().Add(-3*time.Hour)); timing != "started_after_resource_was_healthy" || basis != "condition" {
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
