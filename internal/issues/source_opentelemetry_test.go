package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
	"time"
)

func TestCollectorIssuesDistinguishManagedAndStaleObservations(t *testing.T) {
	for _, tc := range []struct {
		name, group, management, reason string
		observed                        int64
		want                            int
	}{
		{"managed failure", "opentelemetry.io", "managed", "ReconcileError", 2, 1},
		{"unmanaged old failure", "opentelemetry.io", "unmanaged", "ReconcileError", 2, 0},
		{"stale failure", "opentelemetry.io", "managed", "ReconcileError", 1, 0},
		{"status read failure remains actionable", "opentelemetry.io", "managed", "StatusUpdateFailed", 2, 1},
		{"failure without generation witness", "opentelemetry.io", "managed", "ReconcileError", 0, 1},
		{"foreign kind remains generic", "other.example", "unmanaged", "ReconcileError", 2, 1},
		{"foreign status read stays generic", "other.example", "managed", "StatusUpdateFailed", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/v1beta1", "kind": "OpenTelemetryCollector", "metadata": map[string]any{"name": "test", "namespace": "test", "generation": int64(2)}, "spec": map[string]any{"managementState": tc.management}, "status": map[string]any{"observedGeneration": tc.observed, "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": tc.reason, "message": "Operator could not refresh the reported workload status", "lastTransitionTime": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}}}}}
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1beta1", Resource: "opentelemetrycollectors"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "OpenTelemetryCollector"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			got := Compose(p, Filters{})
			if len(got) != tc.want {
				t.Fatalf("got %+v; want %d", got, tc.want)
			}
			if tc.reason == "StatusUpdateFailed" && len(got) > 0 && got[0].Message != "Operator could not refresh the reported workload status" {
				t.Fatalf("status-read Issue must retain the observation problem: %+v", got[0])
			}
		})
	}
}
