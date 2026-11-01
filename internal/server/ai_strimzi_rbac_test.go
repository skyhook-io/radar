package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAIResourceContextStrimziSeparatesOwnerAndIssueReadGrants(t *testing.T) {
	connect := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1", "kind": "KafkaConnect",
		"metadata": map[string]any{"name": "connect", "namespace": "app", "uid": "connect-uid"},
	}}
	podset := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "core.strimzi.io/v1beta2", "kind": "StrimziPodSet",
		"metadata": map[string]any{"name": "workers", "namespace": "app", "uid": "workers-uid"},
	}}
	podset.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: connect.GetAPIVersion(), Kind: connect.GetKind(), Name: connect.GetName(), UID: connect.GetUID()}})
	connector := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kafka.strimzi.io/v1", "kind": "KafkaConnector",
		"metadata": map[string]any{"name": "sink", "namespace": "app", "uid": "sink-uid", "generation": int64(1), "labels": map[string]any{"strimzi.io/cluster": "connect"}},
		"status":   map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}, "connectorStatus": map[string]any{"connector": map[string]any{"state": "RUNNING"}, "tasks": []any{map[string]any{"id": int64(0), "state": "FAILED"}}}},
	}}
	yes := true
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "app", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: podset.GetAPIVersion(), Kind: podset.GetKind(), Name: podset.GetName(), UID: podset.GetUID(), Controller: &yes}}}}
	useTestResourceCache(t, fake.NewClientset(pod))
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	resources := []k8s.APIResource{
		{Group: "", Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
		{Group: "core.strimzi.io", Version: "v1beta2", Kind: "StrimziPodSet", Name: "strimzipodsets", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
		{Group: "kafka.strimzi.io", Version: "v1", Kind: "KafkaConnect", Name: "kafkaconnects", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
		{Group: "kafka.strimzi.io", Version: "v1", Kind: "KafkaConnector", Name: "kafkaconnectors", Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}},
	}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, r := range resources[1:] {
		listKinds[schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Name}] = r.Kind + "List"
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, connect, podset, connector)
	if err := k8s.InitTestDynamicResourceCache(client, resources); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	for gvr := range listKinds {
		if err := cache.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !cache.WaitForSync(gvr, 5*time.Second) {
			t.Fatal("fixture cache not synchronized")
		}
	}
	env := newAuthTestServer(t)
	for _, denied := range []string{"", "strimzipodsets", "kafkaconnects", "kafkaconnectors"} {
		t.Run("denied="+denied, func(t *testing.T) {
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
			for _, r := range resources {
				perms.SetCanI("get", r.Group, r.Name, "app", r.Name != denied)
			}
			env.srv.permCache.Set("reader", nil, perms)
			req := httptest.NewRequest("GET", "/api/ai/resources/pods/app/worker", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "reader"}))
			got := env.srv.buildAIResourceContext(req, pod, "Pod", "app", "worker").RelatedApplicationFindings
			if denied == "" || denied == "kafkaconnectors" {
				if got == nil || len(got.Findings) != 1 {
					t.Fatalf("authorized findings missing: %+v", got)
				}
			} else if got != nil {
				t.Fatalf("denied ancestor exposed: %+v", got)
			}
		})
	}
}
