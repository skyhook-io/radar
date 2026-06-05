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
			name:            "gray zone: 5 min healthy — omit",
			failingSince:    now.Add(-55 * time.Minute),
			resourceCreated: now.Add(-1 * time.Hour),
			basis:           "condition",
			wantOnset:       "",
			wantBasis:       "",
		},
		{
			name:            "gray zone: just over slop but under 10min — omit",
			failingSince:    now.Add(-59 * time.Minute),
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
