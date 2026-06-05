package k8s

import (
	"testing"
	"time"
)

func TestOnsetFromConditionLTT(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name            string
		failingSince    time.Time
		resourceCreated time.Time
		basis           string
		wantOnset       string
		wantBasis       string
	}{
		{
			name:            "initial: condition failed at creation (0s healthy)",
			failingSince:    now.Add(-2 * time.Hour),
			resourceCreated: now.Add(-2 * time.Hour),
			basis:           "condition",
			wantOnset:       "initial",
			wantBasis:       "condition",
		},
		{
			name: "initial: condition failed within slop window (10s healthy)",
			// Resource created 2h ago; condition failed 10s after creation (healthyFor=10s < 30s slop).
			failingSince:    now.Add(-2*time.Hour + 10*time.Second),
			resourceCreated: now.Add(-2 * time.Hour),
			basis:           "condition",
			wantOnset:       "initial",
			wantBasis:       "condition",
		},
		{
			name:            "runtime: clearly past initialization (2h healthy before failure)",
			failingSince:    now.Add(-30 * time.Minute),
			resourceCreated: now.Add(-3 * time.Hour),
			basis:           "owner_condition",
			wantOnset:       "runtime",
			wantBasis:       "owner_condition",
		},
		{
			name:            "gray zone: 5 min healthy on old resource — omit",
			failingSince:    now.Add(-55 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			// 1min healthy out of 1h (ratio 1.7%) — crashed right after deploy,
			// the ratio rule classifies initial regardless of current age.
			name:            "initial: ratio rule — 1min healthy out of 1h",
			failingSince:    now.Add(-59 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "initial",
			wantBasis:       "condition",
		},
		{
			// ratio rule: product-catalog case — crashes 1min after 7min-old deploy
			// healthyFor=1min (14% of 7min) < 25% AND < 5min → initial
			name:            "initial: ratio rule — crash within 1min of 7min-old deploy",
			failingSince:    now.Add(-6 * time.Minute),
			resourceCreated: now.Add(-7 * time.Minute),
			basis:           "owner_condition",
			wantOnset:       "initial",
			wantBasis:       "owner_condition",
		},
		{
			// ratio rule: 2min healthy out of 10min → 20% < 25%, under 5min → initial
			name:            "initial: ratio rule — 2min healthy out of 10min total",
			failingSince:    now.Add(-8 * time.Minute),
			resourceCreated: now.Add(-10 * time.Minute),
			basis:           "condition",
			wantOnset:       "initial",
			wantBasis:       "condition",
		},
		{
			// ratio rule doesn't fire: healthyFor=7min (35% of 20min), above 25% ratio
			// AND above 5min cap → falls to gray zone (between 5min and 10min healthy)
			name:            "gray zone: 7min healthy out of 20min — ratio 35% above threshold",
			failingSince:    now.Add(-13 * time.Minute),
			resourceCreated: now.Add(-20 * time.Minute),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			// ratio rule cap: healthyFor=4min, resourceAge=15min (27%) but healthyFor
			// < 5min check only — wait, 4/15=26.7%>25% so no ratio rule → gray zone
			name:            "gray zone: ratio rule doesn't apply when ratio >= 25%",
			failingSince:    now.Add(-11 * time.Minute),
			resourceCreated: now.Add(-15 * time.Minute),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			// ratio rule doesn't fire when healthyFor >= 5min even with low ratio
			name:            "gray zone: ratio rule capped at 5min healthyFor",
			failingSince:    now.Add(-55 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			name:            "zero failingSince: omit",
			failingSince:    time.Time{},
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			name:            "zero resourceCreated: omit",
			failingSince:    now.Add(-30 * time.Minute),
			resourceCreated: time.Time{},
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			name:            "both zero: omit",
			failingSince:    time.Time{},
			resourceCreated: time.Time{},
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			name:            "future failingSince (clock skew): omit",
			failingSince:    now.Add(5 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			// LTT predating creation = failing for this object's entire
			// lifetime — deliberately "initial", not omitted (adopted or
			// recreated resources whose condition survived).
			name:            "initial: LTT predates creation (negative healthyFor)",
			failingSince:    now.Add(-3 * time.Hour),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "initial",
			wantBasis:       "condition",
		},
		{
			name:            "initial: deletion basis preserved",
			failingSince:    now.Add(-3 * time.Hour),
			resourceCreated: now.Add(-3 * time.Hour),
			basis:           "deletion",
			wantOnset:       "initial",
			wantBasis:       "deletion",
		},
		{
			name:            "runtime: spec basis preserved",
			failingSince:    now.Add(-10 * time.Minute),
			resourceCreated: now.Add(-2 * time.Hour),
			basis:           "spec",
			wantOnset:       "runtime",
			wantBasis:       "spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OnsetFromConditionLTT(tt.failingSince, tt.resourceCreated, tt.basis)
			if got.Onset != tt.wantOnset {
				t.Errorf("Onset = %q, want %q (healthyFor = %v)",
					got.Onset, tt.wantOnset,
					(tt.resourceCreated.Sub(tt.failingSince)).String())
			}
			if got.Basis != tt.wantBasis {
				t.Errorf("Basis = %q, want %q", got.Basis, tt.wantBasis)
			}
		})
	}
}

// capiOnset wraps OnsetFromConditionLTT behind a dur==0 guard: CAPI condition
// readers fall back to resource age when no LTT exists, and that fallback
// duration must never reach the classifier.
func TestCapiOnset_ZeroDurationGuard(t *testing.T) {
	if onset, basis := capiOnset(0, time.Now().Add(-3*time.Hour)); onset != "" || basis != "" {
		t.Errorf("dur=0 must omit onset, got (%q, %q)", onset, basis)
	}
	if onset, basis := capiOnset(time.Hour, time.Now().Add(-3*time.Hour)); onset != "runtime" || basis != "condition" {
		t.Errorf("2h healthy then failing 1h must be runtime/condition, got (%q, %q)", onset, basis)
	}
}
