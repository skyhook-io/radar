package issues

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var strimziConnectorGVR = schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: "kafkaconnectors"}

func strimziConnectorFixture() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1", "kind": "KafkaConnector",
		"metadata": map[string]any{"name": "sink", "namespace": "connect", "generation": int64(2)},
		"status": map[string]any{"observedGeneration": int64(2), "connectorStatus": map[string]any{
			"connector": map[string]any{"state": "RUNNING"},
			"tasks":     []any{map[string]any{"id": int64(0), "state": "FAILED", "trace": "secret-sentinel"}},
		}, "conditions": []any{map[string]any{"type": "NotReady", "status": "True", "message": "secret-sentinel", "lastTransitionTime": "2026-01-01T00:00:00Z"}}},
		"spec": map[string]any{"config": map[string]any{"password": "secret-sentinel"}},
	}}
}

func TestStrimziConnectorFailureEvidence(t *testing.T) {
	u := strimziConnectorFixture()
	got := detectStrimziConnectorIssues(strimziConnectorGVR, u)
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	i := got[0]
	if i.Reason != "StrimziConnectorTasksFailed" || i.Severity != SeverityWarning || !strings.Contains(i.Message, "task IDs: 0") || !strings.Contains(i.Message, "operator snapshot") {
		t.Fatalf("wrong evidence: %+v", i)
	}
	if i.IssueTiming != "" || i.IssueTimingBasis != "" || !i.OnsetUnknown || !i.FirstSeen.IsZero() {
		t.Fatal("unknown onset must not infer timing")
	}
	data, _ := json.Marshal(i)
	if strings.Contains(string(data), "secret-sentinel") {
		t.Fatal("configuration or trace leaked")
	}
}

func TestStrimziConnectorConservativeScenarios(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
		want   string
	}{
		{"connector failed", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "FAILED", "status", "connectorStatus", "connector", "state")
			unstructured.RemoveNestedField(u.Object, "status", "connectorStatus", "tasks")
		}, "StrimziConnectorFailed"},
		{"not ready no runtime", func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "connectorStatus")
		}, "StrimziConnectorNotReady"},
		{"recovered", func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "conditions")
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"id": int64(0), "state": "RUNNING"}}, "status", "connectorStatus", "tasks")
		}, ""},
		{"no evidence", func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, "status") }, ""},
		{"stale generation", func(u *unstructured.Unstructured) { u.SetGeneration(3) }, ""},
		{"future generation", func(u *unstructured.Unstructured) { u.SetGeneration(1) }, ""},
		{"missing generation", func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "observedGeneration")
		}, ""},
		{"zero generation", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, int64(0), "status", "observedGeneration")
		}, ""},
		{"malformed generation", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "2", "status", "observedGeneration")
		}, ""},
		{"paused annotation", func(u *unstructured.Unstructured) {
			u.SetAnnotations(map[string]string{"strimzi.io/pause-reconciliation": "true"})
		}, ""},
		{"paused condition", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "ReconciliationPaused", "status": "True"}}, "status", "conditions")
		}, ""},
		{"terminating", func(u *unstructured.Unstructured) { now := metav1.Now(); u.SetDeletionTimestamp(&now) }, ""},
		{"malformed runtime", func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "conditions")
			_ = unstructured.SetNestedField(u.Object, "bad", "status", "connectorStatus")
		}, ""},
		{"partial explicit failure", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(u.Object, []any{nil, map[string]any{"state": "FAILED"}}, "status", "connectorStatus", "tasks")
		}, "StrimziConnectorTasksFailed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := strimziConnectorFixture()
			tc.change(u)
			got := detectStrimziConnectorIssues(strimziConnectorGVR, u)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected issue: %+v", got)
				}
			} else if len(got) != 1 || got[0].Reason != tc.want {
				t.Fatalf("got %+v want %s", got, tc.want)
			}
		})
	}
	for _, state := range []string{"PAUSED", "STOPPED", "UNASSIGNED", "RESTARTING", "UNKNOWN", ""} {
		t.Run(state, func(t *testing.T) {
			u := strimziConnectorFixture()
			unstructured.RemoveNestedField(u.Object, "status", "conditions")
			_ = unstructured.SetNestedField(u.Object, state, "status", "connectorStatus", "connector", "state")
			_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"state": state}}, "status", "connectorStatus", "tasks")
			if got := detectStrimziConnectorIssues(strimziConnectorGVR, u); len(got) != 0 {
				t.Fatalf("state alone is not a failure: %+v", got)
			}
		})
	}
}

func TestStrimziConnectorTaskIDsBounded(t *testing.T) {
	u := strimziConnectorFixture()
	tasks := []any{map[string]any{"id": int64(0), "state": "FAILED"}}
	for id := int64(19); id >= 0; id-- {
		tasks = append(tasks, map[string]any{"id": id, "state": "FAILED"})
	}
	_ = unstructured.SetNestedSlice(u.Object, tasks, "status", "connectorStatus", "tasks")
	got := detectStrimziConnectorIssues(strimziConnectorGVR, u)
	if len(got) != 1 || !strings.Contains(got[0].Message, "0, 1, 2, 3, 4, 5, 6, 7, 8, 9; 10 more") {
		t.Fatalf("unstable/unbounded task list: %+v", got)
	}
}

func TestStrimziConnectorCompositionRouting(t *testing.T) {
	for _, group := range []string{"kafka.strimzi.io", "other.example.io"} {
		gvr := strimziConnectorGVR
		gvr.Group = group
		p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {strimziConnectorFixture()}}, kinds: map[schema.GroupVersionResource]string{gvr: "KafkaConnector"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
		got := Compose(p, Filters{})
		if (len(got) == 1) != (group == "kafka.strimzi.io") {
			t.Fatalf("wrong group dispatch: %s %+v", group, got)
		}
		if got = Compose(p, Filters{Kinds: []string{"Pod"}}); len(got) != 0 {
			t.Fatal("kind filter ignored")
		}
		if got = Compose(strimziScopedProvider{p}, Filters{Namespaces: []string{"other"}}); len(got) != 0 {
			t.Fatal("namespace filter ignored")
		}
		// Suppression must not fall through to the generic False-condition reader.
		u := p.dynamic[gvr][0]
		u.SetGeneration(3)
		_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"type": "Ready", "status": "False"}}, "status", "conditions")
		if got = Compose(p, Filters{}); len(got) != 0 {
			t.Fatalf("stale condition escaped gate: %+v", got)
		}
	}
}

type strimziScopedProvider struct{ *fakeProvider }

func (p strimziScopedProvider) ListDynamic(gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, u := range p.dynamic[gvr] {
		if u.GetNamespace() == ns {
			out = append(out, u)
		}
	}
	return out, nil
}
