package resourcecontext

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/health"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AI and search summaries read Agent Sandbox and Substrate health the same way
// Issues and the UI do.
func TestAgentRuntimeSummaryHealthContract(t *testing.T) {
	for _, file := range []string{"../health/testdata/agent_sandbox.json", "../health/testdata/substrate_workerpool.json"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var cases []struct {
			Name   string
			Object unstructured.Unstructured
			Level  health.Level
			Now    string
		}
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(tc.Name, func(t *testing.T) {
				if tc.Now != "" {
					t.Skip("summaries judge at the current time; pkg/health pins this vector's clock")
				}
				if got := unstructuredHealth(&tc.Object); got != levelToString(tc.Level) {
					t.Fatalf("got %q, want %q", got, levelToString(tc.Level))
				}
			})
		}
	}
}

// Summaries judge at the current time, so a pool whose worker Deployment never
// appeared reads unhealthy here as it does in Issues and the UI.
func TestAgentRuntimeSummaryReadsStalledWorkerPool(t *testing.T) {
	resolvedAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ate.dev/v1",
		"kind":       "WorkerPool",
		"metadata":   map[string]any{"name": "p", "namespace": "agents", "generation": int64(1), "creationTimestamp": resolvedAt},
		"spec":       map[string]any{"replicas": int64(2)},
		"status": map[string]any{
			"replicas": int64(0),
			"sandboxClasses": []any{map[string]any{
				"name": "gvisor",
				"conditions": []any{map[string]any{
					"type": "Resolved", "status": "True", "reason": "Resolved",
					"observedGeneration": int64(1), "lastTransitionTime": resolvedAt,
				}},
			}},
		},
	}}
	if got := unstructuredHealth(u); got != levelToString(health.LevelUnhealthy) {
		t.Fatalf("got %q, want %q", got, levelToString(health.LevelUnhealthy))
	}
}
