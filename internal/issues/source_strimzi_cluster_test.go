package issues

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var strimziKafkaGVR = schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: "kafkas"}

// Shaped like the status Strimzi 1.3 writes for a Kafka with no node pools.
func strimziKafkaFixture() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1", "kind": "Kafka",
		"metadata": map[string]any{"name": "main", "namespace": "kafka", "generation": int64(1)},
		"status": map[string]any{"observedGeneration": int64(1), "conditions": []any{
			map[string]any{"type": "NotReady", "status": "True", "reason": "InvalidConfigurationException", "message": "secret-sentinel"},
		}},
	}}
}

func TestStrimziKafkaIssues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
		want   bool
	}{
		{"not ready", func(*unstructured.Unstructured) {}, true},
		{"ready", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
		}, false},
		{"ready with warning", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Warning", "status": "True", "reason": "KafkaStorage"}}, "status", "conditions")
		}, false},
		// The initial status of a new Kafka: NotReady/Creating with no observed generation.
		{"creating", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, map[string]any{"conditions": []any{
				map[string]any{"type": "NotReady", "status": "True", "reason": "Creating"},
			}}, "status")
		}, false},
		{"missing status", func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, "status") }, false},
		{"stale generation", func(u *unstructured.Unstructured) { u.SetGeneration(2) }, false},
		{"paused annotation", func(u *unstructured.Unstructured) {
			u.SetAnnotations(map[string]string{"strimzi.io/pause-reconciliation": "true"})
		}, false},
		{"terminating", func(u *unstructured.Unstructured) { now := metav1.Now(); u.SetDeletionTimestamp(&now) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := strimziKafkaFixture()
			tc.change(u)
			got := StrimziKafkaIssues(strimziKafkaGVR, u)
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("unexpected issue: %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Reason != "StrimziKafkaNotReady" || got[0].Severity != SeverityWarning || got[0].Kind != "Kafka" {
				t.Fatalf("got %+v", got)
			}
			if strings.Contains(got[0].Message, "secret-sentinel") || !got[0].OnsetUnknown {
				t.Fatalf("message leaked condition text or inferred onset: %+v", got[0])
			}
		})
	}
}

func TestStrimziKafkaIssuesIgnoresOtherKinds(t *testing.T) {
	for _, kind := range []string{"KafkaNodePool", "KafkaConnect"} {
		u := strimziKafkaFixture()
		u.SetKind(kind)
		if got := StrimziKafkaIssues(strimziKafkaGVR, u); len(got) != 0 {
			t.Fatalf("%s: unexpected issue: %+v", kind, got)
		}
	}
}
