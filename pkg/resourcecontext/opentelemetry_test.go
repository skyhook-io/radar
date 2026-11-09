package resourcecontext

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/skyhook-io/radar/pkg/health"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOpenTelemetryAgentHealthContract(t *testing.T) {
	raw, err := os.ReadFile("../health/testdata/opentelemetry.json")
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
				t.Fatalf("got %s, want %s", got, tc.Level)
			}
		})
	}
}
