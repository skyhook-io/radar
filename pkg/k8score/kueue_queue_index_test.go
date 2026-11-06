package k8score

import (
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func kueueObject(kind, namespace, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": kind}}
	u.SetNamespace(namespace)
	u.SetName(name)
	for k, v := range fields {
		u.Object[k] = v
	}
	return u
}

func TestKueueQueueKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  *unstructured.Unstructured
		keys func(*unstructured.Unstructured) []string
		want []string
	}{
		{"submitted and reserved", kueueObject("Workload", "team", "w", map[string]any{
			"spec":   map[string]any{"queueName": "lq"},
			"status": map[string]any{"admission": map[string]any{"clusterQueue": "cq"}},
		}), workloadQueueKeys, []string{KueueSubmissionKey("team", "lq"), KueueReservationKey("cq")}},
		{"submitted only", kueueObject("Workload", "team", "w", map[string]any{"spec": map[string]any{"queueName": "lq"}}), workloadQueueKeys, []string{KueueSubmissionKey("team", "lq")}},
		{"reserved without a queue name", kueueObject("Workload", "team", "w", map[string]any{
			"status": map[string]any{"admission": map[string]any{"clusterQueue": "cq"}},
		}), workloadQueueKeys, []string{KueueReservationKey("cq")}},
		{"no queue at all", kueueObject("Workload", "team", "w", nil), workloadQueueKeys, nil},
		{"local queue", kueueObject("LocalQueue", "team", "lq", map[string]any{"spec": map[string]any{"clusterQueue": "cq"}}), localQueueTargetKeys, []string{KueueLocalQueueTargetKey("cq")}},
		{"local queue without a target", kueueObject("LocalQueue", "team", "lq", nil), localQueueTargetKeys, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.keys(tc.obj); !slices.Equal(got, tc.want) {
				t.Fatalf("keys = %q, want %q", got, tc.want)
			}
		})
	}
	if KueueReservationKey("x") == KueueLocalQueueTargetKey("x") || KueueSubmissionKey("", "x") == KueueReservationKey("x") {
		t.Fatal("keys of different roles collide")
	}
}

func TestKueueWorkloadsAndLocalQueuesAreIndexedByQueue(t *testing.T) {
	workloads := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}
	localQueues := schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "localqueues"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(apiruntime.NewScheme(), map[schema.GroupVersionResource]string{
		workloads: "WorkloadList", localQueues: "LocalQueueList",
	},
		kueueObject("Workload", "team", "submitted", map[string]any{"spec": map[string]any{"queueName": "lq"}}),
		kueueObject("Workload", "team", "both", map[string]any{
			"spec":   map[string]any{"queueName": "lq"},
			"status": map[string]any{"admission": map[string]any{"clusterQueue": "cq"}},
		}),
		kueueObject("Workload", "other", "reserved", map[string]any{
			"spec":   map[string]any{"queueName": "elsewhere"},
			"status": map[string]any{"admission": map[string]any{"clusterQueue": "cq"}},
		}),
		kueueObject("Workload", "other", "same-queue-name", map[string]any{"spec": map[string]any{"queueName": "lq"}}),
		kueueObject("LocalQueue", "team", "lq", map[string]any{"spec": map[string]any{"clusterQueue": "cq"}}),
		kueueObject("LocalQueue", "other", "lq", map[string]any{"spec": map[string]any{"clusterQueue": "another"}}),
	)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.startWatching(workloads, ""); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "workloads", func() bool { return d.IsClusterWideSynced(workloads) })
	if err := d.startWatching(localQueues, "team"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, "team's LocalQueues", func() bool { return d.IsNamespaceSynced(localQueues, "team") })

	for _, tc := range []struct {
		gvr       schema.GroupVersionResource
		namespace string
		keys      []string
		want      []string
	}{
		{workloads, "", []string{KueueSubmissionKey("team", "lq")}, []string{"both", "submitted"}},
		{workloads, "", []string{KueueReservationKey("cq")}, []string{"both", "reserved"}},
		// Read together, a Workload filed under both keys comes back once.
		{workloads, "", []string{KueueReservationKey("cq"), KueueSubmissionKey("team", "lq")}, []string{"both", "reserved", "submitted"}},
		{workloads, "other", []string{KueueSubmissionKey("other", "lq")}, []string{"same-queue-name"}},
		{localQueues, "team", []string{KueueLocalQueueTargetKey("cq")}, []string{"lq"}},
	} {
		got, ok := d.ReadableIndexed(tc.gvr, tc.namespace, KueueQueueIndex, tc.keys...)
		names := names(got)
		slices.Sort(names)
		if !ok || !slices.Equal(names, tc.want) {
			t.Fatalf("%s in %q under %q = %v, %v; want %v", tc.gvr.Resource, tc.namespace, tc.keys, names, ok, tc.want)
		}
	}
}
