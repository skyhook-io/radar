package k8s

import "time"

// OnsetResult is the outcome of an onset classification attempt.
// The zero value means "no confident signal; omit from the Issue."
type OnsetResult struct {
	Onset string // "initial" | "runtime" | ""
	Basis string // "condition" | "owner_condition" | "deletion" | "phase" | "spec" | ""
}

// OnsetFromConditionLTT classifies issue onset by comparing a condition's
// lastTransitionTime against the resource's creationTimestamp.
//
//   - "initial"  evidence shows the resource has been failing since shortly
//     after creation — it was never meaningfully healthy.
//     "This was broken when it was first applied."
//   - "runtime"  the resource was clearly healthy before the condition flipped —
//     at least 10 min of healthy operation before the failure.
//     "This was working and then broke."
//   - zero value the gap is in the gray zone, or timestamps are missing.
//     Caller must omit onset; do not infer from age alone.
//
// Two "initial" rules:
//  1. Absolute slop (30s): conditions take a moment to be written after creation.
//  2. Ratio rule: healthyFor < 5min AND resource was failing for ≥75% of its
//     lifetime. Catches misconfigured workloads that crash within ~1-2 min of
//     deploy — the Kubernetes controller reconciliation loop means Available=False
//     is set 60-120s after the first crash, which exceeds the 30s slop but is
//     still clearly a deploy-time misconfiguration.
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
	// Negative healthyFor (LTT predates creation — adopted or recreated
	// resources whose condition survived) means failing for this object's
	// entire lifetime, which is exactly what "initial" asserts. Deliberately
	// classified, not omitted.
	healthyFor := resourceAge - failingFor

	// Rule 1: absolute slop — condition propagation takes a moment after creation.
	if healthyFor < 30*time.Second {
		return OnsetResult{Onset: "initial", Basis: basis}
	}
	// Rule 2: ratio — if the resource was healthy for < 25% of its lifetime and
	// that window is under 5 minutes, the healthy period is noise not a clean bill
	// of health. Handles the common case of a misconfigured workload that crashes
	// within 1-2 minutes of first deploy (controller reconciliation lag keeps
	// healthyFor above the 30s slop even though the failure is deploy-time).
	if healthyFor < 5*time.Minute && resourceAge > 0 {
		ratio := float64(healthyFor) / float64(resourceAge)
		if ratio < 0.25 {
			return OnsetResult{Onset: "initial", Basis: basis}
		}
	}
	// Rule 3: confirmed healthy — at least 10 minutes of healthy operation.
	if healthyFor > 10*time.Minute {
		return OnsetResult{Onset: "runtime", Basis: basis}
	}
	return OnsetResult{} // gray zone: omit
}
