package issues

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/topology"
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
func contextOwnerResolver(t *testing.T, objects ...runtime.Object) func(runtime.Object) *topology.ResourceRef {
	t.Helper()
	return func(subject runtime.Object) *topology.ResourceRef {
		world := ownershiptest.World{}
		for _, obj := range objects {
			observed, ok := k8s.OwnershipObservation(obj)
			if !ok {
				t.Fatal("invalid ownership fixture")
			}
			gvk := obj.GetObjectKind().GroupVersionKind()
			world.Resources = append(world.Resources, ownership.ServedResource{Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind, Resource: strings.ToLower(gvk.Kind) + "s", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}})
			world.Objects = append(world.Objects, ownershiptest.Object{Ref: observed.Ref, APIVersion: gvk.GroupVersion().String(), UID: observed.UID, OwnerReferences: observed.OwnerReferences})
		}
		session, err := ownershiptest.Scan(world).Begin()
		if err != nil {
			t.Fatal(err)
		}
		observed, _ := k8s.OwnershipObservation(subject)
		rel := &topology.Relationships{}
		topology.ApplyOwner(rel, ownership.NewView(session, ownership.Unrestricted()), observed)
		return rel.Owner
	}
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
	canonical := Compose(&fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}: {rows[0]}}, kinds: map[schema.GroupVersionResource]string{schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}: "KafkaConnector"}, namespaced: map[schema.GroupVersionResource]bool{schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}: true}}, Filters{})
	if len(canonical) != 1 {
		t.Fatalf("canonical findings: %+v", canonical)
	}
	// Each projection records its own read time.
	canonical[0].LastSeen = out.Findings[0].LastSeen
	if !reflect.DeepEqual(out.Findings[0], canonical[0]) {
		t.Fatalf("canonical shape differs: related=%+v issues=%+v", out.Findings, canonical)
	}

}
func TestStrimziContextOwnerUIDAndMissing(t *testing.T) {
	connect := contextStrimzi("KafkaConnect", "app", "connect")
	pod := contextStrimzi("Pod", "app", "worker")
	pod.SetAPIVersion("v1")
	yes := false
	pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "kafka.strimzi.io/v1", Kind: "KafkaConnect", Name: "connect", UID: types.UID("connect-uid"), Controller: &yes}})
	reader := strimziContextReader{owner: contextOwnerResolver(t, pod, connect), get: func(Ref) (runtime.Object, error) { return connect, nil }, list: func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
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
	if len(out.Findings) != 5 || !out.Truncated || out.Checked != len(rows) {
		t.Fatalf("unbounded findings %+v", out)
	}
	rows = rows[:1]
	rows[0].SetGeneration(2)
	out = strimziEvidence(connect, nil, reader)
	if len(out.Findings) != 0 {
		t.Fatal("stale status emitted")
	}
	if out.Checked != 1 || out.Unknown != 1 || out.CoverageState != "limited" {
		t.Fatalf("stale inspection looked verified: %+v", out)
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
			if !out.Truncated || len(out.Findings) != 0 || strings.Contains(out.Coverage, "500") || !strings.Contains(out.Coverage, "were not checked") {
				t.Fatalf("unauthorized count leaked: %+v", out)
			}
			if calls != 0 {
				t.Fatalf("oversized namespace triggered permission checks: %d", calls)
			}
		})
	}
}

func TestStrimziContextModernOwnerChain(t *testing.T) {
	connect := contextStrimzi("KafkaConnect", "app", "connect")
	podset := contextStrimzi("StrimziPodSet", "app", "workers")
	podset.SetAPIVersion("core.strimzi.io/v1beta2")
	yes, no := true, false
	podset.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: connect.GetAPIVersion(), Kind: "KafkaConnect", Name: "connect", UID: connect.GetUID(), Controller: &no}})
	pod := contextStrimzi("Pod", "app", "worker")
	pod.SetAPIVersion("v1")
	pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: podset.GetAPIVersion(), Kind: "StrimziPodSet", Name: "workers", UID: podset.GetUID(), Controller: &yes}})
	refs := map[Ref]runtime.Object{
		{Group: "core.strimzi.io", Kind: "StrimziPodSet", Namespace: "app", Name: "workers"}: podset,
		{Group: strimziGroup, Kind: "KafkaConnect", Namespace: "app", Name: "connect"}:       connect,
	}
	reader := strimziContextReader{owner: contextOwnerResolver(t, pod, podset, connect), get: func(ref Ref) (runtime.Object, error) {
		if obj, ok := refs[ref]; ok {
			return obj, nil
		}
		return nil, fmt.Errorf("not watched")
	}, list: func(string) ([]*unstructured.Unstructured, schema.GroupVersionResource, bool, error) {
		return []*unstructured.Unstructured{contextConnector("app", "sink", "connect")}, schema.GroupVersionResource{Group: strimziGroup, Version: "v1", Resource: "kafkaconnectors"}, false, nil
	}}
	if got := strimziEvidence(pod, nil, reader); got == nil || len(got.Findings) != 1 {
		t.Fatalf("real chain lost: %+v", got)
	}
	podset.SetUID("replacement")
	if got := strimziEvidence(pod, nil, reader); got != nil {
		t.Fatal("stale intermediary UID accepted")
	}
	podset.SetUID("workers-uid")
	owners := pod.GetOwnerReferences()
	owners[0].APIVersion = "collision.io/v1"
	pod.SetOwnerReferences(owners)
	if got := strimziEvidence(pod, nil, reader); got != nil {
		t.Fatal("wrong owner group accepted")
	}
	pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: podset.GetAPIVersion(), Kind: "StrimziPodSet", Name: "workers", UID: podset.GetUID(), Controller: &yes}})
	owners = podset.GetOwnerReferences()
	owners = append(owners, metav1.OwnerReference{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other", Controller: &no})
	podset.SetOwnerReferences(owners)
	if got := strimziEvidence(pod, nil, reader); got != nil {
		t.Fatal("ambiguous non-controller ownership accepted")
	}
	connector := contextConnector("app", "sink", "connect")
	if got := strimziEvidence(connector, nil, reader); got != nil {
		t.Fatalf("direct connector invented unavailable related evidence: %+v", got)
	}
}
