package issues

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func strimziClusterFixture(kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1", "kind": kind,
		"metadata": map[string]any{"name": "main", "namespace": "kafka", "generation": int64(4)},
		"status": map[string]any{"observedGeneration": int64(4), "conditions": []any{
			map[string]any{"type": "NotReady", "status": "True", "reason": "Timeout", "message": "secret-sentinel"},
		}},
	}}
}

func TestStrimziClusterIssues(t *testing.T) {
	for _, kind := range []string{"Kafka", "KafkaNodePool"} {
		gvr := schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: strings.ToLower(kind) + "s"}
		for _, tc := range []struct {
			name   string
			change func(*unstructured.Unstructured)
			want   bool
		}{
			{"not ready", func(*unstructured.Unstructured) {}, true},
			{"ready", func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
			}, false},
			{"warning only", func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Warning", "status": "True"}}, "status", "conditions")
			}, false},
			{"not ready false", func(u *unstructured.Unstructured) {
				_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "NotReady", "status": "False"}}, "status", "conditions")
			}, false},
			{"missing status", func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, "status") }, false},
			{"stale generation", func(u *unstructured.Unstructured) { u.SetGeneration(5) }, false},
			{"paused annotation", func(u *unstructured.Unstructured) {
				u.SetAnnotations(map[string]string{"strimzi.io/pause-reconciliation": "true"})
			}, false},
			{"terminating", func(u *unstructured.Unstructured) { now := metav1.Now(); u.SetDeletionTimestamp(&now) }, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				u := strimziClusterFixture(kind)
				tc.change(u)
				got := StrimziClusterIssues(gvr, u)
				if !tc.want {
					if len(got) != 0 {
						t.Fatalf("unexpected issue: %+v", got)
					}
					return
				}
				if len(got) != 1 || got[0].Reason != "Strimzi"+kind+"NotReady" || got[0].Severity != SeverityWarning || got[0].Kind != kind {
					t.Fatalf("got %+v", got)
				}
				if strings.Contains(got[0].Message, "secret-sentinel") || !got[0].OnsetUnknown {
					t.Fatalf("message leaked condition text or inferred onset: %+v", got[0])
				}
			})
		}
	}
}

func TestStrimziClusterIssuesIgnoresOtherKinds(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: "kafkaconnects"}
	if got := StrimziClusterIssues(gvr, strimziClusterFixture("KafkaConnect")); len(got) != 0 {
		t.Fatalf("unexpected issue: %+v", got)
	}
}
