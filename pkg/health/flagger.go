package health

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// FlaggerCanary describes the recorded rollout outcome and requested suspension.
// CanaryStatus has no observedGeneration; this is not a latest-spec guarantee.
// Promoted=False is normal during analysis, so it is not a failure on its own.
func FlaggerCanary(u *unstructured.Unstructured) Verdict {
	if u == nil {
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	if phase == "Failed" {
		return Verdict{Level: LevelUnhealthy, Reason: "Failed"}
	}
	suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	if suspended && phase == "" {
		return Verdict{Level: LevelNeutral, Reason: "Suspend requested"}
	}
	switch phase {
	case "Succeeded":
		if suspended {
			return Verdict{Level: LevelNeutral, Reason: phase}
		}
		return Verdict{Level: LevelHealthy, Reason: phase}
	case "Initializing", "Initialized", "Waiting", "Progressing", "WaitingPromotion", "Promoting", "Finalising", "Terminating", "Terminated":
		return Verdict{Level: LevelNeutral, Reason: phase}
	case "":
		return Verdict{Level: LevelUnknown, Reason: "Not reported"}
	default:
		return Verdict{Level: LevelUnknown, Reason: phase}
	}
}
