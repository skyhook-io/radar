package context

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOpenTelemetrySummariesAreConciseAndGroupAware(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "opentelemetry.io/v1beta1", "kind": "OpenTelemetryCollector",
		"metadata": map[string]any{"name": "telemetry", "namespace": "test", "generation": int64(2)},
		"spec":     map[string]any{"mode": "deployment", "config": map[string]any{"exporters": map[string]any{"otlp": map[string]any{"headers": map[string]any{"x-custom-key": "private-config"}}}}},
		"status":   map[string]any{"observedGeneration": int64(2), "version": "0.162.0", "image": "collector:0.162.0", "scale": map[string]any{"statusReplicas": "0/1"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
	}}
	s := summarizeUnstructured(u)
	if s.Status != "Reconciled" || s.Type != "deployment" || s.Version != "0.162.0" {
		t.Fatalf("bad native summary: %+v", s)
	}
	if s.Ready != "" {
		t.Fatal("a reconciliation-time scale snapshot must not claim live readiness")
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "private-config") || strings.Contains(string(raw), "headers") {
		t.Fatal("summary copied arbitrary collector configuration")
	}
	u.SetAPIVersion("other.example/v1")
	if summarizeUnstructured(u).Status == "Reconciled" {
		t.Fatal("foreign kind received native interpretation")
	}
	u.SetAPIVersion("opentelemetry.io/v1alpha1")
	u.SetKind("Instrumentation")
	u.Object["spec"] = map[string]any{"env": []any{map[string]any{"name": "CUSTOM_TOKEN", "value": "private-env"}}}
	u.Object["status"] = map[string]any{"upgradeBlockedVersions": map[string]any{"java": "private-status"}}
	s = summarizeUnstructured(u)
	if s.Status != "Upgrade blocked" {
		t.Fatalf("bad instrumentation summary: %+v", s)
	}
	raw, _ = json.Marshal(s)
	if strings.Contains(string(raw), "private-") {
		t.Fatal("summary copied arbitrary instrumentation values")
	}
}
