package kargo

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// This file is the only reader of Freight.status.currentlyIn, verifiedIn and
// approvedFor. Kargo v1.12 keeps them on the Freight object; Kargo's plans
// for v2 serve them from its API instead, so a v2 adapter replaces this file.

// FreightStatus is what a Freight's status says about one Stage.
type FreightStatus struct {
	// In: the Freight is currently in the Stage, since Since (zero when the
	// status doesn't say).
	In    bool
	Since time.Time
	// Verified: the Freight passed verification in the Stage at VerifiedAt.
	Verified   bool
	VerifiedAt time.Time
	// LongestSoak is the longest completed stay in the Stage after
	// verification. A current stay counts through SoakedFor.
	LongestSoak time.Duration
	// Approved: a person approved the Freight for the Stage.
	Approved   bool
	ApprovedAt time.Time
}

// StatusIn reads what freight's status records for stage.
func StatusIn(freight *unstructured.Unstructured, stage string) FreightStatus {
	var out FreightStatus
	if freight == nil {
		return out
	}
	if in, ok := object(freight.Object, "status", "currentlyIn")[stage].(map[string]any); ok {
		out.In, out.Since = true, timeAt(in, "since")
	}
	if v, ok := object(freight.Object, "status", "verifiedIn")[stage].(map[string]any); ok {
		out.Verified, out.VerifiedAt = true, timeAt(v, "verifiedAt")
		out.LongestSoak, _ = duration(v, "longestSoak")
	}
	if a, ok := object(freight.Object, "status", "approvedFor")[stage].(map[string]any); ok {
		out.Approved, out.ApprovedAt = true, timeAt(a, "approvedAt")
	}
	return out
}

// SoakedFor is how long the Freight has soaked in the Stage as Kargo counts
// it: zero unless verified there, else the longer of its longest completed
// stay and its current one.
func (s FreightStatus) SoakedFor(now time.Time) time.Duration {
	if !s.Verified {
		return 0
	}
	soak := s.LongestSoak
	if s.In && !s.Since.IsZero() {
		soak = max(soak, now.Sub(s.Since))
	}
	return soak
}

// Since is when freight entered stage, from the Freight's status. Known is
// false when the status doesn't record it; callers must not substitute a
// promotion or verification time, which can differ.
func Since(freight *unstructured.Unstructured, stage string) (since time.Time, known bool) {
	s := StatusIn(freight, stage)
	return s.Since, s.In && !s.Since.IsZero()
}
