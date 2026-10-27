package summarycontext

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestStatusBuilderKeepsPreparedCountsWithoutNavigation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	build := StatusBuilderFromIndexes(ctx,
		IssueIndex{issueIndexKey("", "Pod", "shop", "web"): 7},
		IssueIndex{issueIndexKey("", "Node", "", "worker"): 3},
	)
	for _, tc := range []struct {
		kind, namespace, name string
		obj                   runtime.Object
		issues                int
	}{
		{"Pod", "shop", "web", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}}, 7},
		{"Node", "", "worker", &corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}, 3},
	} {
		got := build(tc.obj, nil, "", tc.kind, tc.namespace, tc.name)
		if got == nil || got.Health == "" || got.IssueCount != tc.issues {
			t.Fatalf("%s: lost status after deadline: %+v", tc.kind, got)
		}
		if got.ManagedBy != nil || got.Scanned != nil || len(got.Omitted) != 0 {
			t.Fatalf("%s: unrequested navigation: %+v", tc.kind, got)
		}
	}
}

func TestStatusBudgetQualifiesOnlyRequestedFields(t *testing.T) {
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if expired {
			cancel()
		}
		build, stop := WithStatusBudget(ctx, func(context.Context) Builder { return nil })
		report := &unstructured.Unstructured{}
		report.SetLabels(map[string]string{"trivy-operator.resource.kind": "ReplicaSet", "trivy-operator.resource.name": "web-old"})
		got := build(nil, report, aicontext.TrivyGroup, "VulnerabilityReport", "shop", "report")
		stop()
		cancel()
		want := resourcecontext.OmittedUnavailable
		if expired {
			want = resourcecontext.OmittedBudgetExceeded
		}
		if got == nil || len(got.Omitted) != 1 || got.Omitted[0] != (resourcecontext.OmittedField{Field: "issueCount", Reason: want}) {
			t.Fatalf("expired=%v: %+v", expired, got)
		}
		if got.ManagedBy != nil || got.Scanned != nil {
			t.Fatalf("status fallback projected scan navigation: %+v", got)
		}
	}
}

func TestStatusBuilderDoesNotResolveScanTargets(t *testing.T) {
	report := &unstructured.Unstructured{}
	report.SetLabels(map[string]string{"trivy-operator.resource.kind": "ReplicaSet", "trivy-operator.resource.name": "web-old"})
	build := StatusBuilderFromIndexes(context.Background(), IssueIndex{issueIndexKey(aicontext.TrivyGroup, "VulnerabilityReport", "shop", "report"): 2}, nil)
	got := build(nil, report, aicontext.TrivyGroup, "VulnerabilityReport", "shop", "report")
	if got == nil || got.IssueCount != 2 || got.ManagedBy != nil || got.Scanned != nil || len(got.Omitted) != 0 {
		t.Fatalf("status projected scan navigation: %+v", got)
	}
}
