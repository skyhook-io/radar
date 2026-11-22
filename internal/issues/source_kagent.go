package issues

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/kagent"
)

// detectKagentIssues reports the findings pkg/kagent reads on a kagent Agent,
// SandboxTemplate, ModelConfig, ModelProviderConfig or RemoteMCPServer: its
// readiness verdict and, separately, egress grants kagent did not apply.
func detectKagentIssues(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, now time.Time) []Issue {
	r, ok := kagent.Read(u, now)
	if !ok {
		return nil
	}
	var out []Issue
	for _, f := range r.Findings() {
		severity := SeverityWarning
		if f.Severity == kagent.SeverityCritical {
			severity = SeverityCritical
		}
		iss := newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), severity, f.Reason, f.Message, f.Since, f.SinceKnown, f.Fingerprint, u.GetCreationTimestamp().Time)
		iss.Action = f.Action
		out = append(out, iss)
	}
	return out
}

// kagentSuppressesGenericConditions keeps the generic Ready-condition walk off
// the kagent kinds detectKagentIssues reads, so one problem is reported once
// and a Preparing agent inside its grace period is not a warning.
func kagentSuppressesGenericConditions(group, kind string) bool {
	return group == kagent.Group && kagent.ReportsStatus(kind)
}
