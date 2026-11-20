package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"time"
)

func detectFlaggerCanaryIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	if u == nil || u.GetDeletionTimestamp() != nil {
		return nil
	}
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	if phase != "Failed" {
		return nil
	}
	// status.lastTransitionTime is rewritten on every scheduler tick, even
	// while Failed. Only the matching Promoted condition pins the failure onset.
	var transition time.Time
	known := false
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if ok && condition["type"] == "Promoted" && condition["status"] == "False" && condition["reason"] == "Failed" {
			transition, known = conditionTransitionTime(condition)
			break
		}
	}
	return []Issue{newConditionIssue(gvr, "Canary", u.GetNamespace(), u.GetName(), SeverityWarning, "FlaggerCanaryFailed", "Flagger reports a failed rollout. Inspect the Canary events for the cause; the primary may still be serving the previous version. This is the last recorded outcome, not a latest-configuration guarantee.", transition, known, "", u.GetCreationTimestamp().Time)}
}
