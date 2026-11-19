package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestJava4ShellWorksWithoutTrivyAndChecksPodAccess(t *testing.T) {
	addPackageNamespaces(t, "legacy", "other")
	cache := k8s.GetResourceCache()
	for _, ns := range []string{"legacy", "other"} {
		pods := testFakeClient.CoreV1().Pods(ns)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "java-app", Namespace: ns},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:java21"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}}},
		}
		if _, err := pods.Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = pods.Delete(context.Background(), pod.Name, metav1.DeleteOptions{})
		})
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := cache.Pods().Pods(ns).Get(pod.Name); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never reached the cache", ns)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"legacy", "other"}}
	perms.SetCanI("list", "", "pods", "legacy", true)
	perms.SetCanI("list", "", "pods", "other", false)
	perms.SetCanI("list", "", "nodes", "", false)
	env.srv.permCache.Set("alice", nil, perms)
	path := "/api/vulnerabilities/java4shell?namespaces=legacy,other"
	res := env.authGet(t, path, "alice", "")
	body := readBody(t, res)
	var got struct {
		images.Java4ShellResult
		Coverage resourceapi.Coverage
	}
	if res.StatusCode != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("got %d %s", res.StatusCode, body)
	}
	if got.Containers != 1 || got.Unassessed["image_identity_unknown"] != 1 || got.Matches != 0 ||
		got.Coverage.Status != resourceapi.CoveragePartial || len(got.Coverage.Gaps) != 1 ||
		got.Coverage.Gaps[0].Code != readreason.UserForbidden {
		t.Fatalf("got %+v", got)
	}
	denied := env.authGet(t, "/api/vulnerabilities/java4shell?namespace=other", "alice", "")
	if body := readBody(t, denied); denied.StatusCode != 403 {
		t.Fatalf("denied scope: %d %s", denied.StatusCode, body)
	}
}

func TestJava4ShellOnSharedListenerRejectsCrossSite(t *testing.T) {
	s := &Server{listenAddress: "0.0.0.0"}
	router := chi.NewRouter()
	s.setupAppRoutes(router)
	path := "http://127.0.0.1/api/vulnerabilities/java4shell?namespace=default"

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("cross-site read: %d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, path, nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("direct read: %d %s", w.Code, w.Body.String())
	}
}

func TestJava4ShellCustomWorkloadUsesExactAccess(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "apps.example.com", Version: "v1", Resource: "javaworkloads"}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "JavaWorkloadList"})
	if err := k8s.InitTestDynamicResourceCache(dynamic, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "JavaWorkload", Namespaced: true, IsCRD: true,
		Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "worker-1", Namespace: "team", OwnerReferences: []metav1.OwnerReference{{
			APIVersion: gvr.GroupVersion().String(), Kind: "JavaWorkload", Name: "worker", Controller: new(true),
		}},
	}}
	s := &Server{permCache: auth.NewPermissionCache()}
	for _, tc := range []struct {
		user    string
		allowed bool
		want    resourceid.Ref
	}{
		{"allowed", true, resourceid.NewRef(gvr.Group, "JavaWorkload", "team", "worker")},
		{"denied", false, resourceid.NewRef("", "Pod", "team", "worker-1")},
	} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
		perms.SetCanI("list", gvr.Group, gvr.Resource, "team", tc.allowed)
		s.permCache.Set(tc.user, nil, perms)
		r := requestWithUser(http.MethodGet, "/", &auth.User{Username: tc.user})
		got := s.java4ShellWorkload(r, k8s.GetResourceCache(), pod)
		if got != tc.want {
			t.Errorf("%s got %v, want %v", tc.user, got, tc.want)
		}
	}
}
