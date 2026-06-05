package k8s

import "time"

// OnsetResult is the outcome of an onset classification attempt.
// The zero value means "no confident signal; omit from the Issue."
type OnsetResult struct {
	Onset string // "initial" | "runtime" | ""
	Basis string // "condition" | "owner_condition" | "event" | "deletion" | "phase" | "spec" | ""
}

// OnsetFromConditionLTT classifies issue onset by comparing a condition's
// lastTransitionTime against the resource's creationTimestamp. The logic
// mirrors pkg/k8score/object_warnings.go:workloadHealthWarning:
//
//   - "initial"  the condition has been False for essentially the entire
//     resource lifetime (slop 30s for condition-propagation delay).
//     "This was broken when it was first applied."
//   - "runtime"  the resource was clearly healthy before the condition flipped —
//     at least 10 min of healthy operation before the failure.
//     "This was working and then broke."
//   - zero value the gap is in the gray zone, or timestamps are missing.
//     Caller must omit onset; do not infer from age alone.
func OnsetFromConditionLTT(failingSince, resourceCreated time.Time, basis string) OnsetResult {
	if failingSince.IsZero() || resourceCreated.IsZero() {
		return OnsetResult{}
	}
	now := time.Now()
	failingFor := now.Sub(failingSince)
	resourceAge := now.Sub(resourceCreated)
	if failingFor <= 0 || resourceAge <= 0 {
		return OnsetResult{}
	}
	healthyFor := resourceAge - failingFor
	// Slop of 30s: conditions take a moment to be written after creation.
	// If the resource was "healthy" for less than 30s it was never really healthy.
	if healthyFor < 30*time.Second {
		return OnsetResult{Onset: "initial", Basis: basis}
	}
	// At least 10 minutes of confirmed-healthy operation before the failure.
	if healthyFor > 10*time.Minute {
		return OnsetResult{Onset: "runtime", Basis: basis}
	}
	return OnsetResult{} // gray zone: omit
}
