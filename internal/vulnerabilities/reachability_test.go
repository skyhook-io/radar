package vulnerabilities

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Only the Services selecting the workload's own Pods are probed: an Ingress
// in front of several Services must not send probes to another workload.
func TestSelectingServices(t *testing.T) {
	yes := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "shop",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &yes}}}}
	pod := appPod("shop", "web")
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &yes}}
	other := appPod("shop", "api")
	objects := []any{
		rs, pod, other,
		service("shop", "web-lb", corev1.ServiceTypeLoadBalancer, map[string]string{"app": "web"}),
		service("shop", "web-internal", corev1.ServiceTypeClusterIP, map[string]string{"app": "web"}),
		service("shop", "api", corev1.ServiceTypeClusterIP, map[string]string{"app": "api"}),
		service("shop", "headless", corev1.ServiceTypeClusterIP, nil),
	}
	client := fake.NewClientset()
	for _, o := range objects {
		switch v := o.(type) {
		case *appsv1.ReplicaSet:
			_ = client.Tracker().Add(v)
		case *corev1.Pod:
			_ = client.Tracker().Add(v)
		case *corev1.Service:
			_ = client.Tracker().Add(v)
		}
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        client,
		ResourceTypes: map[string]bool{k8score.Services: true, k8score.Pods: true, k8score.ReplicaSets: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	for deadline := time.Now().Add(5 * time.Second); !cache.IsKindReady("services") || !cache.IsKindReady("pods") || !cache.IsKindReady("replicasets"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("cache never synced")
		}
	}
	got := SelectingServices(cache, WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"})
	if len(got) != 2 || got[0] != "web-internal" || got[1] != "web-lb" {
		t.Fatalf("selecting services: %v", got)
	}
	if got := SelectingServices(cache, WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "gone"}); len(got) != 0 {
		t.Fatalf("a workload with no Pods selects nothing: %v", got)
	}
}
