package issues

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func loggingObj(kind string, status map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": "logging.banzaicloud.io/v1beta1", "kind": kind, "metadata": map[string]any{"name": "x", "namespace": "logging"}, "spec": map[string]any{}}
	if status != nil {
		o["status"] = status
	}
	return &unstructured.Unstructured{Object: o}
}

func TestLoggingIssues(t *testing.T) {
	gvr := func(resource string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: "logging.banzaicloud.io", Version: "v1beta1", Resource: resource}
	}
	problems := map[string]any{"active": true, "problemsCount": int64(2), "problems": []any{"secret not found", "x"}}
	for _, c := range []struct {
		name, kind, resource, reason, contains string
		status                                 map[string]any
	}{
		{"output with problems", "Output", "outputs", "LoggingOutputProblem", "secret not found", problems},
		{"cluster output with problems", "ClusterOutput", "clusteroutputs", "LoggingOutputProblem", "and 1 more", problems},
		{"inactive flow", "Flow", "flows", "LoggingFlowInactive", "inactive", map[string]any{"active": false}},
		{"cluster flow problem wins over inactive", "ClusterFlow", "clusterflows", "LoggingFlowProblem", "secret not found", map[string]any{"active": false, "problemsCount": int64(1), "problems": []any{"secret not found"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := detectCuratedConditionIssues(gvr(c.resource), c.kind, loggingObj(c.kind, c.status))
			if len(got) != 1 || got[0].Reason != c.reason || got[0].Severity != SeverityWarning || !strings.Contains(got[0].Message, c.contains) {
				t.Fatalf("issues = %+v", got)
			}
		})
	}
	for _, c := range []struct {
		name, kind, resource string
		u                    *unstructured.Unstructured
	}{
		{"healthy flow", "Flow", "flows", loggingObj("Flow", map[string]any{"active": true})},
		{"flow without status", "Flow", "flows", loggingObj("Flow", nil)},
		{"logging", "Logging", "loggings", loggingObj("Logging", problems)},
		{"fluentbit agent", "FluentbitAgent", "fluentbitagents", loggingObj("FluentbitAgent", problems)},
	} {
		if got := detectCuratedConditionIssues(gvr(c.resource), c.kind, c.u); len(got) != 0 {
			t.Errorf("%s: unexpected issues %+v", c.name, got)
		}
	}
	deleting := loggingObj("Output", problems)
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	if got := detectCuratedConditionIssues(gvr("outputs"), "Output", deleting); len(got) != 0 {
		t.Errorf("deleting object: %+v", got)
	}
	foreign := loggingObj("Flow", map[string]any{"active": false})
	if got := detectCuratedConditionIssues(schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "flows"}, "Flow", foreign); len(got) != 0 {
		t.Errorf("foreign Flow: %+v", got)
	}
}
