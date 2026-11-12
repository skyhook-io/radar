package issues

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func upgradePlan(group string, conditions ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1", "kind": "Plan",
		"metadata": map[string]any{"name": "k3s-server", "namespace": "system-upgrade"},
		"status":   map[string]any{"conditions": conditions},
	}}
}

func TestUpgradePlanInvalidIssue(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "upgrade.cattle.io", Version: "v1", Resource: "plans"}
	invalid := map[string]any{"type": "Validated", "status": "False", "reason": "Invalid", "message": "spec.version or spec.channel required", "lastTransitionTime": "2026-11-01T00:00:00Z"}
	got := detectCuratedConditionIssues(gvr, "Plan", upgradePlan("upgrade.cattle.io", invalid))
	if len(got) != 1 || got[0].Severity != SeverityWarning || got[0].Reason != "UpgradePlanInvalid" || !strings.Contains(got[0].Message, "spec.version") || got[0].OnsetUnknown {
		t.Fatalf("issues = %+v", got)
	}
	empty := map[string]any{"type": "Validated", "status": "False"}
	if got := detectUpgradePlanIssues(gvr, upgradePlan("upgrade.cattle.io", empty)); len(got) != 1 || !strings.Contains(got[0].Message, "no Node is upgraded") {
		t.Fatalf("empty message = %+v", got)
	}
	for name, plan := range map[string]*unstructured.Unstructured{
		"valid, in progress": upgradePlan("upgrade.cattle.io", map[string]any{"type": "Validated", "status": "True"}, map[string]any{"type": "Complete", "status": "False"}),
		"no conditions":      upgradePlan("upgrade.cattle.io"),
	} {
		if got := detectUpgradePlanIssues(gvr, plan); len(got) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	deleting := upgradePlan("upgrade.cattle.io", invalid)
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	if got := detectUpgradePlanIssues(gvr, deleting); len(got) != 0 {
		t.Errorf("deleting: %+v", got)
	}
}

func TestUpgradePlanComposeIgnoresForeignPlan(t *testing.T) {
	invalid := map[string]any{"type": "Validated", "status": "False", "message": "bad"}
	ours := schema.GroupVersionResource{Group: "upgrade.cattle.io", Version: "v1", Resource: "plans"}
	foreign := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "plans"}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{ours: {upgradePlan("upgrade.cattle.io", invalid)}, foreign: {upgradePlan("example.com", invalid)}},
		kinds:      map[schema.GroupVersionResource]string{ours: "Plan", foreign: "Plan"},
		namespaced: map[schema.GroupVersionResource]bool{ours: true, foreign: true},
	}
	n := 0
	for _, iss := range Compose(p, Filters{}) {
		if iss.Reason == "UpgradePlanInvalid" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("UpgradePlanInvalid issues = %d, want 1", n)
	}
}
