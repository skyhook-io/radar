package context

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestFlaggerSummaryIsRecordedAndGroupAware(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"name": "app", "namespace": "test"},
		"spec":   map[string]any{"suspend": true, "targetRef": map[string]any{"kind": "Deployment", "name": "app"}, "analysis": map[string]any{"webhooks": []any{map[string]any{"metadata": map[string]any{"custom": "private-hook"}}}}},
		"status": map[string]any{"phase": "Failed", "canaryWeight": int64(0), "conditions": []any{map[string]any{"type": "Promoted", "status": "False", "message": "private-message"}}},
	}}
	s := summarizeUnstructured(u)
	if s.Status != "Failed" || s.Target != "Deployment/app" || s.Suspended == nil || !*s.Suspended || !strings.Contains(s.Issue, "last reported") && !strings.Contains(s.Issue, "Last reported") {
		t.Fatalf("bad Canary summary: %+v", s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("summary copied arbitrary hook/status values")
	}
	u.SetAPIVersion("other.example/v1")
	if summarizeUnstructured(u).Target != "" {
		t.Fatal("foreign Canary received native interpretation")
	}
	u.SetAPIVersion("flagger.app/v1beta1")
	u.SetKind("MetricTemplate")
	u.Object["spec"] = map[string]any{"provider": map[string]any{"type": "prometheus", "headers": map[string]any{"custom": "private-header"}}, "query": "private-query"}
	s = summarizeUnstructured(u)
	if s.Type != "prometheus" || s.Status != "Configured" {
		t.Fatalf("bad template summary: %+v", s)
	}
	raw, _ = json.Marshal(s)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("template summary copied query/credentials")
	}
}
