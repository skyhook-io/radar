package issues

import (
	"github.com/skyhook-io/radar/pkg/datum"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"time"
)

func detectDatumIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	var out []Issue
	for _, c := range datum.Failures(u) {
		transition, err := time.Parse(time.RFC3339, c.Transition)
		first, last, unknown := conditionIssueTimes(time.Now(), transition, err == nil)
		message := c.Message
		if c.Scope != "" {
			message = c.Scope + ": " + message
		}
		issue := Issue{Severity: SeverityWarning, Source: SourceCondition, Group: gvr.Group, Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName(), Reason: condTypeReason(c.Type, c.Reason), Message: message, FirstSeen: first, LastSeen: last, OnsetUnknown: unknown, ResourceCreatedAt: u.GetCreationTimestamp().Time, Count: 1, Fingerprint: c.Path}
		classifyIssue(&issue)
		enrichIdentity(&issue)
		out = append(out, issue)
	}
	return out
}
