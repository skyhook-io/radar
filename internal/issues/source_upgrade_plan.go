package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// detectUpgradePlanIssues reports a system-upgrade-controller Plan the
// controller rejected (Validated=False): it upgrades no Node until fixed.
// The generic condition pass doesn't look at Validated, and a failed upgrade
// Job is already reported by the Job checks, so this is the only Plan issue.
func detectUpgradePlanIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	if u == nil || u.GetDeletionTimestamp() != nil {
		return nil
	}
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if !ok || condition["type"] != "Validated" || condition["status"] != "False" {
			continue
		}
		message, _ := condition["message"].(string)
		if message == "" {
			message = "The system-upgrade-controller rejected this Plan; no Node is upgraded while it is invalid."
		}
		transition, known := conditionTransitionTime(condition)
		return []Issue{newConditionIssue(gvr, "Plan", u.GetNamespace(), u.GetName(), SeverityWarning, "UpgradePlanInvalid", message, transition, known, "", u.GetCreationTimestamp().Time)}
	}
	return nil
}
