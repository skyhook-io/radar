package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRayClusterGroupCannotIncludeHead(t *testing.T) {
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "head", Labels: map[string]string{"ray.io/node-type": "head", "ray.io/group": "headgroup"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "worker", Labels: map[string]string{"ray.io/node-type": "worker", "ray.io/group": "headgroup"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "unclassified"}},
	}
	group := filterRayClusterPods(pods, "", "headgroup")
	if len(group) != 1 || group[0].Name != "worker" {
		t.Fatalf("group selected %v", group)
	}
	if len(filterRayClusterPods(pods, "", "")) != 3 {
		t.Fatal("All omitted unclassified Pod")
	}
	if head := filterRayClusterPods(pods, "head", ""); len(head) != 1 || head[0].Name != "head" {
		t.Fatal("head selection incorrect")
	}
	if _, ok := workloadReadTargets["rayclusters"]; ok {
		t.Fatal("RayCluster must not enable generic workload log routing")
	}
}

func TestRayClusterPodsRequestContract(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "ray.io/v1", "kind": "RayCluster", "metadata": map[string]any{"name": "cluster", "namespace": "default", "uid": "current"}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), root)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: "ray.io", Version: "v1", Name: "rayclusters", Kind: "RayCluster", Namespaced: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	old := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(old) })
	s := &Server{}
	router := chi.NewRouter()
	router.Get("/api/workloads/{kind}/{namespace}/{name}/pods", s.handleWorkloadPods)
	router.Get("/api/workloads/{kind}/{namespace}/{name}/logs", s.handleWorkloadLogs)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", 400}, {"?ownerUID=old", 409}, {"?ownerUID=current&nodeType=invalid", 400}, {"?ownerUID=current&nodeType=head&workerGroup=headgroup", 400}, {"?ownerUID=current&limit=201", 400}, {"?ownerUID=current", 200},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods"+tc.query, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s: got %d %s want %d", tc.query, rec.Code, rec.Body.String(), tc.status)
		}
	}
	for _, path := range []string{"/api/workloads/raycluster/default/cluster/pods?ownerUID=current", "/api/workloads/rayclusters/default/cluster/logs"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		want := 200
		if strings.HasSuffix(path, "/logs") {
			want = 400
		}
		if rec.Code != want {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/missing/pods?ownerUID=uid", nil))
	if rec.Code != 404 {
		t.Fatalf("missing root: %d %s", rec.Code, rec.Body.String())
	}

	yes := true
	var objects []runtime.Object
	for i := 0; i < 205; i++ {
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%03d", i), Namespace: "default", OwnerReferences: []metav1.OwnerReference{{APIVersion: "ray.io/v1", Kind: "RayCluster", Name: "cluster", UID: "current", Controller: &yes}}, Labels: map[string]string{"ray.io/node-type": "worker", "ray.io/group": "workers"}}})
	}
	useTestResourceCache(t, fake.NewClientset(objects...))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods?ownerUID=current&workerGroup=workers", nil))
	var result struct {
		Pods      []WorkloadPodInfo
		Total     int
		Truncated bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != 200 || len(result.Pods) != 200 || result.Total != 205 || !result.Truncated {
		t.Fatalf("bounded response: %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	client.PrependReactor("get", "rayclusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		k8s.CancelOngoingOperations()
		return true, root.DeepCopy(), nil
	})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods?ownerUID=current", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "connection changed") {
		t.Fatalf("context changed during parent read: %d %s", rec.Code, rec.Body.String())
	}
	client.ReactionChain = client.ReactionChain[1:]
	client.PrependReactor("get", "rayclusters", func(action k8stesting.Action) (bool, runtime.Object, error) {
		k8s.ResetResourceCache()
		return true, root.DeepCopy(), nil
	})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods?ownerUID=current", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "connection changed") {
		t.Fatalf("cache replaced during parent read: %d %s", rec.Code, rec.Body.String())
	}
	client.ReactionChain = client.ReactionChain[1:]
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods?ownerUID=current", nil))
	if rec.Code != 503 {
		t.Fatalf("absent cache during switch: %d %s", rec.Code, rec.Body.String())
	}
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods?ownerUID=current", nil))
	if rec.Code != 403 {
		t.Fatalf("uncovered cache: %d %s", rec.Code, rec.Body.String())
	}
	k8s.ResetResourceCache()
	if err := k8s.InitTestPromotedSyncingCache(fake.NewClientset(), 5*time.Second, 5*time.Second, map[string]time.Duration{"pods": 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/workloads/rayclusters/default/cluster/pods?ownerUID=current", nil))
	if rec.Code != 503 {
		t.Fatalf("warming cache: %d %s", rec.Code, rec.Body.String())
	}
}

func TestProxyAuthRayClusterPodsDenied(t *testing.T) {
	for _, tc := range []struct {
		name, ns   string
		root, pods bool
	}{
		{"root denied", "default", false, true}, {"pods denied", "default", true, false}, {"namespace denied", "private", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newAuthTestServer(t)
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
			perms.SetCanI("get", "ray.io", "rayclusters", tc.ns, tc.root)
			perms.SetCanI("list", "", "pods", tc.ns, tc.pods)
			env.srv.permCache.Set("alice", nil, perms)
			resp := env.authGet(t, "/api/workloads/rayclusters/"+tc.ns+"/cluster/pods?ownerUID=uid", "alice", "")
			defer resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatalf("got %d", resp.StatusCode)
			}
		})
	}
}
