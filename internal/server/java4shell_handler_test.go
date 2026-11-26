package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestJava4ShellWorksWithoutTrivyAndChecksPodAccess(t *testing.T) {
	addPackageNamespaces(t, "legacy", "other")
	cache := k8s.GetResourceCache()
	for _, ns := range []string{"legacy", "other"} {
		pods := testFakeClient.CoreV1().Pods(ns)
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "java-app", Namespace: ns, OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "java", Controller: new(true),
			}}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:java21"}}},
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
	perms.SetCanI("list", "apps", "deployments", "legacy", false)
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
		len(got.UnassessedRows) != 1 || got.UnassessedRows[0].Workload != resourceid.NewRef("", "Pod", "legacy", "java-app") ||
		got.Coverage.Status != resourceapi.CoveragePartial || len(got.Coverage.Gaps) != 1 ||
		got.Coverage.Gaps[0].Code != readreason.UserForbidden {
		t.Fatalf("got %+v", got)
	}
	denied := env.authGet(t, "/api/vulnerabilities/java4shell?namespace=other", "alice", "")
	if body := readBody(t, denied); denied.StatusCode != 403 {
		t.Fatalf("denied scope: %d %s", denied.StatusCode, body)
	}
	stream := env.authGet(t, "/api/vulnerabilities/java4shell/stream?namespaces=legacy,other", "alice", "")
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("stream: %d %s", stream.StatusCode, readBody(t, stream))
	}
	dec := json.NewDecoder(strings.NewReader(readBody(t, stream)))
	var update, terminal java4ShellStreamMessage
	if err := dec.Decode(&update); err != nil || update.Type != "progress" || update.Progress == nil || update.Progress.Total != 0 {
		t.Fatalf("first progress: %+v, %v", update, err)
	}
	if err := dec.Decode(&terminal); err != nil || terminal.Type != "result" || terminal.Report == nil ||
		terminal.Report.Containers != 1 || terminal.Report.Unassessed["image_identity_unknown"] != 1 ||
		terminal.Report.Coverage.Status != resourceapi.CoveragePartial || dec.Decode(&update) != io.EOF {
		t.Fatalf("terminal result: %+v, %v", terminal, err)
	}
	deniedStream := env.authGet(t, "/api/vulnerabilities/java4shell/stream?namespace=other", "alice", "")
	if body := readBody(t, deniedStream); deniedStream.StatusCode != http.StatusForbidden {
		t.Fatalf("stream must enforce scope before emitting progress: %d %s", deniedStream.StatusCode, body)
	}
}

func TestJava4ShellScopesToOwnedPodsAndRequiresWorkloadRead(t *testing.T) {
	const ns = "java4shell-test"
	owner := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "java", UID: "deployment-current"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: "java-rs", UID: "replicaset-current",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: owner.Name, UID: owner.UID, Controller: new(true)}},
	}}
	pod := func(name string, uid types.UID) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns, Labels: map[string]string{"app": "java"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rs.Name, UID: uid, Controller: new(true)}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "java", Image: "java:21"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "java", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}}},
		}
	}
	objects := append(testNamespaces(ns), owner, rs, pod("current-pod", rs.UID), pod("stale-pod", "replicaset-previous"))
	useTestResourceCache(t, fake.NewClientset(objects...))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{ns}}
	perms.SetCanI("list", "", "pods", ns, true)
	perms.SetCanI("list", "", "nodes", "", false)
	perms.SetCanI("get", "apps", "deployments", ns, true)
	env.srv.permCache.Set("alice", nil, perms)
	path := "/api/vulnerabilities/java4shell?workloadKind=Deployment&workloadNamespace=" + ns + "&workloadName=java"
	resp := env.authGet(t, path, "alice", "")
	body := readBody(t, resp)
	var got struct {
		images.Java4ShellResult
		Workload resourceid.Ref
		Coverage resourceapi.Coverage
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	want := resourceid.NewRef("apps", "Deployment", ns, "java")
	if got.Workload != want || got.Containers != 1 || got.Coverage.Status != resourceapi.CoverageComplete ||
		len(got.UnassessedRows) != 1 || got.UnassessedRows[0].Workload != want || got.UnassessedRows[0].Reason != "image_identity_unknown" {
		t.Fatalf("unexpected scoped response: %+v", got)
	}
	perms.SetCanI("get", "apps", "deployments", ns, false)
	denied := env.authGet(t, path, "alice", "")
	if body := readBody(t, denied); denied.StatusCode != http.StatusForbidden {
		t.Fatalf("denied workload read: %d %s", denied.StatusCode, body)
	}
	invalid := env.authGet(t, path+"&namespaces=other", "alice", "")
	if body := readBody(t, invalid); invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched scope: %d %s", invalid.StatusCode, body)
	}
}

func TestJava4ShellRegistryAccessFollowsImageInspection(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "java"},
		Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{
			{Name: "allowed"}, {Name: "forbidden"},
		}},
	}
	allowed := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "allowed"}, Type: corev1.SecretTypeDockerConfigJson}
	forbidden := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "forbidden"}, Type: corev1.SecretTypeDockerConfigJson}
	useTestResourceCache(t, fake.NewClientset(pod, allowed, forbidden))
	s := &Server{authConfig: auth.Config{Mode: "proxy"}, permCache: auth.NewPermissionCache().WithConnectionKey(currentPermissionConnection), listenAddress: "0.0.0.0"}
	for _, tc := range []struct {
		name              string
		getPod, getSecret bool
		exec              bool
		wantSecrets       []string
	}{
		{name: "list only", getSecret: true},
		{name: "get Pod only", getPod: true},
		{name: "exact Secret grant", getPod: true, getSecret: true, wantSecrets: []string{"allowed"}},
		{name: "exec on readable Pod", getPod: true, exec: true, wantSecrets: []string{"allowed", "forbidden"}},
		{name: "exec without get Pod", exec: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
			perms.SetCanI("get", "", "pods", "team", tc.getPod)
			perms.SetCanI("get", "", "secrets/allowed", "team", tc.getSecret)
			perms.SetCanI("get", "", "secrets/forbidden", "team", false)
			perms.SetCanI("create", "", "pods/exec", "team", tc.exec)
			s.permCache.Set(tc.name, nil, perms)
			r := requestWithUser(http.MethodGet, "/", &auth.User{Username: tc.name})
			access := s.java4ShellRegistryAccess(r, k8s.GetResourceCache(), pod)
			var names []string
			for _, secret := range access.PullSecrets {
				names = append(names, secret.Name)
			}
			if !slices.Equal(names, tc.wantSecrets) || access.RadarCredentials != (tc.getPod && tc.exec) {
				t.Fatalf("secrets %v, ambient credentials %v", names, access.RadarCredentials)
			}
		})
	}
	s.authConfig = auth.Config{}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	access := s.java4ShellRegistryAccess(request, k8s.GetResourceCache(), pod)
	if access.RadarCredentials || len(access.PullSecrets) != 2 {
		t.Fatalf("shared listener received wrong scope: %d pull secrets, ambient credentials %v", len(access.PullSecrets), access.RadarCredentials)
	}
}

func TestJava4ShellOnSharedListenerRejectsCrossSite(t *testing.T) {
	s := &Server{listenAddress: "0.0.0.0"}
	router := chi.NewRouter()
	s.setupAppRoutes(router)
	for _, suffix := range []string{"", "/stream"} {
		path := "http://127.0.0.1/api/vulnerabilities/java4shell" + suffix + "?namespace=default"
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatalf("cross-site read %s: %d %s", suffix, w.Code, w.Body.String())
		}
		req = httptest.NewRequest(http.MethodGet, path, nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("direct read %s: %d %s", suffix, w.Code, w.Body.String())
		}
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
	s := &Server{permCache: auth.NewPermissionCache().WithConnectionKey(currentPermissionConnection)}
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
