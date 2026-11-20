package issues

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// detectLoggingIssues reads what the Logging operator reports on Flows and
// outputs: status.problems (with problemsCount) and status.active. These kinds
// carry no conditions, so the generic pass never sees them. A missing
// status.active is unknown, not inactive.
func detectLoggingIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured) []Issue {
	if u == nil || u.GetDeletionTimestamp() != nil {
		return nil
	}
	var problemReason string
	switch kind {
	case "Output", "ClusterOutput":
		problemReason = "LoggingOutputProblem"
	case "Flow", "ClusterFlow":
		problemReason = "LoggingFlowProblem"
	default:
		return nil
	}
	count, _, _ := unstructured.NestedInt64(u.Object, "status", "problemsCount")
	problems, _, _ := unstructured.NestedStringSlice(u.Object, "status", "problems")
	if count > 0 || len(problems) > 0 {
		message := "The Logging operator reports a problem"
		if len(problems) > 0 {
			message += ": " + problems[0]
			if more := max(int(count), len(problems)) - 1; more > 0 {
				message += fmt.Sprintf(" (and %d more)", more)
			}
		}
		return []Issue{newLoggingIssue(gvr, kind, u, problemReason, message)}
	}
	if active, found, _ := unstructured.NestedBool(u.Object, "status", "active"); found && !active && problemReason == "LoggingFlowProblem" {
		return []Issue{newLoggingIssue(gvr, kind, u, "LoggingFlowInactive", "The Logging operator reports this Flow as inactive; its logs are not forwarded.")}
	}
	return nil
}

func newLoggingIssue(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, reason, message string) Issue {
	return newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityWarning, reason, message, time.Time{}, false, "", u.GetCreationTimestamp().Time)
}
