package issues

import (
	"github.com/skyhook-io/radar/pkg/datum"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestDatumHostnameFailuresHaveDistinctStableIdentity(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": datum.NetworkGroup + "/v1alpha", "kind": "HTTPProxy", "metadata": map[string]any{"name": "web", "namespace": "demo"}, "status": map[string]any{"hostnameStatuses": []any{map[string]any{"hostname": "a.example.test", "conditions": []any{map[string]any{"type": "CertificateReady", "status": "False", "reason": "IssuerRejected"}}}, map[string]any{"hostname": "b.example.test", "conditions": []any{map[string]any{"type": "CertificateReady", "status": "False", "reason": "IssuerRejected"}}}}}}}
	got := detectDatumIssues(schema.GroupVersionResource{Group: datum.NetworkGroup, Version: "v1alpha", Resource: "httpproxies"}, u)
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("hostname failures collapsed: %+v", got)
	}
	again := detectDatumIssues(schema.GroupVersionResource{Group: datum.NetworkGroup, Version: "v1alpha", Resource: "httpproxies"}, u)
	if got[0].ID != again[0].ID {
		t.Fatal("unstable issue identity")
	}
}
