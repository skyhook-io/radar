package k8s

import "time"

// IssueTimingResult is the outcome of a timing classification attempt.
// The zero value means "no confident signal; omit from the Issue."
type IssueTimingResult struct {
	IssueTiming string // "started_at_resource_creation" | "started_after_resource_was_healthy" | ""
	Basis       string // "condition" | "owner_condition" | "pod_creation" | "deletion" | "phase" | "spec" | ""
}

// IssueTimingFromConditionLTT classifies issue timing by comparing a condition's
// lastTransitionTime against the resource's creationTimestamp.
//
//   - "started_at_resource_creation" means evidence places the failing state
//     during resource creation or first reconciliation.
//   - "started_after_resource_was_healthy" means evidence shows a meaningful
//     healthy window before the failing condition appeared.
//   - zero value the gap is in the gray zone, or timestamps are missing.
//     Caller must omit issue_timing; do not infer timing from age alone.
//
// Two started-at-resource-creation rules:
//  1. Absolute slop (30s): conditions take a moment to be written after creation.
//  2. Ratio rule: healthyFor < 5min AND resource was failing for ≥75% of its
//     lifetime. Catches misconfigured workloads that crash within ~1-2 min of
//     deploy — the Kubernetes controller reconciliation loop means Available=False
//     is set 60-120s after the first crash, which exceeds the 30s slop but is
//     still clearly a deploy-time misconfiguration.
func IssueTimingFromConditionLTT(failingSince, resourceCreated time.Time, basis string) IssueTimingResult {
	if failingSince.IsZero() || resourceCreated.IsZero() {
		return IssueTimingResult{}
	}
	now := time.Now()
	failingFor := now.Sub(failingSince)
	resourceAge := now.Sub(resourceCreated)
	if failingFor <= 0 || resourceAge <= 0 {
		return IssueTimingResult{}
	}
	// Negative healthyFor (LTT predates creation — adopted or recreated
	// resources whose condition survived) means failing for this object's
	// entire lifetime, which is exactly what "started_at_resource_creation" asserts.
	// Deliberately classified, not omitted.
	healthyFor := resourceAge - failingFor

	// Rule 1: absolute slop — condition propagation takes a moment after creation.
	if healthyFor < 30*time.Second {
		return IssueTimingResult{IssueTiming: "started_at_resource_creation", Basis: basis}
	}
	// Rule 2: ratio — if the resource was healthy for < 25% of its lifetime and
	// that window is under 5 minutes, the healthy period is noise not a clean bill
	// of health. Handles the common case of a misconfigured workload that crashes
	// within 1-2 minutes of first deploy (controller reconciliation lag keeps
	// healthyFor above the 30s slop even though the failure is deploy-time).
	if healthyFor < 5*time.Minute && resourceAge > 0 {
		ratio := float64(healthyFor) / float64(resourceAge)
		if ratio < 0.25 {
			return IssueTimingResult{IssueTiming: "started_at_resource_creation", Basis: basis}
		}
	}
	// Rule 3: confirmed healthy — at least 10 minutes of healthy operation.
	if healthyFor > 10*time.Minute {
		return IssueTimingResult{IssueTiming: "started_after_resource_was_healthy", Basis: basis}
	}
	return IssueTimingResult{} // gray zone: omit
}
