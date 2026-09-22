package issues

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func contextStrimzi(kind, ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kafka.strimzi.io/v1", "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns, "uid": name + "-uid", "generation": int64(1)}}}
}
func contextConnector(ns, name, cluster string) *unstructured.Unstructured {
	u := contextStrimzi("KafkaConnector", ns, name)
	u.SetLabels(map[string]string{"strimzi.io/cluster": cluster})
	u.Object["status"] = map[string]any{"observedGeneration": int64(1), "connectorStatus": map[string]any{"connector": map[string]any{"state": "RUNNING"}, "tasks": []any{map[string]any{"id": int64(0), "state": "FAILED", "trace": "secret-do-not-emit"}}}}
	return u
}
func TestStrimziContextExactAuthorizedAndImmutable(t *testing.T) {
	connect := contextStrimzi("KafkaConnect", "app", "connect")
	rows := []*unstructured.Unstructured{contextConnector("app", "yes", "connect"), contextConnector("other", "foreign", "connect"), contextConnector("app", "wrong", "broker"), contextConnector("app", "denied", "connect")}
	collision := contextConnector("app", "collision", "connect")
	collision.SetAPIVersion("other.io/v1")
	rows = append(rows, collision)
	before, _ := json.Marshal(rows)
	reader := strimziContextReader{list: func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
		return rows, schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}, false, nil
	}}
	seen := []string{}
	out := strimziEvidence(connect, func(r Ref) bool { seen = append(seen, r.Namespace+"/"+r.Name); return r.Name != "denied" }, reader)
	if out == nil || len(out.Findings) != 1 || out.Findings[0].Name != "yes" {
		t.Fatalf("unexpected %+v", out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret-do-not-emit") {
		t.Fatal("raw trace leaked")
	}
	for _, ref := range seen {
		if strings.HasPrefix(ref, "other/") {
			t.Fatal("foreign namespace authorized before filtering")
		}
	}
	after, _ := json.Marshal(rows)
	if string(before) != string(after) {
		t.Fatal("cache mutated")
	}
	direct := detectStrimziConnectorIssues(schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}, rows[0])
	if out.Findings[0].ID != direct[0].ID {
		t.Fatal("canonical finding identity changed")
	}
}
func TestStrimziContextOwnerUIDAndMissing(t *testing.T) {
	connect := contextStrimzi("KafkaConnect", "app", "connect")
	pod := contextStrimzi("Pod", "app", "worker")
	pod.SetAPIVersion("v1")
	yes := false
	pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "kafka.strimzi.io/v1", Kind: "KafkaConnect", Name: "connect", UID: types.UID("connect-uid"), Controller: &yes}})
	reader := strimziContextReader{get: func(Ref) (runtime.Object, error) { return connect, nil }, list: func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
		return nil, schema.GroupVersionResource{}, false, fmt.Errorf("not watched")
	}}
	if out := strimziEvidence(pod, nil, reader); out == nil || out.Coverage == "" {
		t.Fatal("valid UID path lost")
	}
	connect.SetUID("replacement")
	if out := strimziEvidence(pod, nil, reader); out != nil {
		t.Fatal("stale owner UID accepted")
	}
	connect.SetUID("connect-uid")
	if out := strimziEvidence(pod, func(Ref) bool { return false }, reader); out != nil {
		t.Fatal("denied owner exposed")
	}
	reader.get = func(Ref) (runtime.Object, error) { return nil, fmt.Errorf("not watched") }
	if out := strimziEvidence(pod, nil, reader); out != nil {
		t.Fatal("unwatched owner guessed")
	}
}
func TestStrimziContextBoundsAndGeneration(t *testing.T) {
	connect := contextStrimzi("KafkaConnect", "app", "connect")
	var rows []*unstructured.Unstructured
	for n := 0; n < 8; n++ {
		rows = append(rows, contextConnector("app", fmt.Sprintf("c%d", n), "connect"))
	}
	reader := strimziContextReader{list: func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
		return rows, schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}, false, nil
	}}
	out := strimziEvidence(connect, nil, reader)
	if len(out.Findings) != 5 || !out.Truncated {
		t.Fatalf("unbounded findings %+v", out)
	}
	rows = rows[:1]
	rows[0].SetGeneration(2)
	out = strimziEvidence(connect, nil, reader)
	if len(out.Findings) != 0 {
		t.Fatal("stale status emitted")
	}
	rows = nil
	for n := 0; n <= maxStrimziContextScan; n++ {
		rows = append(rows, contextConnector("app", fmt.Sprintf("c%d", n), "connect"))
	}
	out = strimziEvidence(connect, nil, reader)
	if !out.Truncated || len(out.Findings) != 0 {
		t.Fatal("scan cap missed")
	}
}

func TestStrimziCandidateBudgetPrecedesAuthorizationAndRelevance(t *testing.T) {
	for _, mode := range []string{"unrelated", "denied"} {
		t.Run(mode, func(t *testing.T) {
			connect := contextStrimzi("KafkaConnect", "app", "connect")
			var rows []*unstructured.Unstructured
			for n := 0; n < maxStrimziContextScan; n++ {
				cluster := "connect"
				if mode == "unrelated" {
					cluster = "another"
				}
				rows = append(rows, contextConnector("app", fmt.Sprintf("skip-%d", n), cluster))
			}
			rows = append(rows, contextConnector("app", "beyond-budget", "connect"))
			reader := strimziContextReader{list: func(ns string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
				if ns != "app" {
					t.Fatalf("wrong namespace %q", ns)
				}
				return rows, schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}, true, nil
			}}
			calls := 0
			out := strimziEvidence(connect, func(ref Ref) bool {
				if ref.Kind == "KafkaConnect" {
					return true
				}
				calls++
				if ref.Name == "beyond-budget" {
					t.Fatal("examined candidate beyond scan cap")
				}
				return false
			}, reader)
			if !out.Truncated || len(out.Findings) != 0 || strings.Contains(out.Coverage, "500") {
				t.Fatalf("unauthorized count leaked: %+v", out)
			}
			if calls != 0 {
				t.Fatalf("oversized namespace triggered permission checks: %d", calls)
			}
		})
	}
}
