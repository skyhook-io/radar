package resourcecontext

import (
	"encoding/json"
	"os"
	"testing"

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
		}
		if err := json.Unmarshal(raw, &cases); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(tc.Name, func(t *testing.T) {
				if got := unstructuredHealth(&tc.Object); got != levelToString(tc.Level) {
					t.Fatalf("got %q, want %q", got, levelToString(tc.Level))
				}
			})
		}
	}
}
