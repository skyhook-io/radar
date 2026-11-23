package mcp

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// wait_for reports the waited condition's message; a kagent discovery
// error's URL comes back redacted.
func TestWaitConditionStatusRedactsKagentURLs(t *testing.T) {
	const message = `rejected by transport: Post "http://user:***@no-such-mcp.radar-kagent-spike.svc.cluster.local:9999/mcp?token=FAKEQUERY": dial tcp`
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "api.kagent.dev/v1alpha3", "kind": "RemoteMCPServer",
		"metadata": map[string]any{"name": "spike-literal-header", "namespace": "kagent"},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "message": message}}},
	}}
	if _, got := conditionStatus(obj, "Accepted"); strings.Contains(got, "FAKEQUERY") || strings.Contains(got, "user:") {
		t.Errorf("wait_for message = %q", got)
	}
}
