package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

var testServer *httptest.Server

func TestMain(m *testing.M) {
	replicas := int32(1)

	deployUID := "deploy-uid-1234"
	rsUID := "rs-uid-5678"

	fakeClient := fake.NewClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx",
				Namespace: "default",
				UID:       "deploy-uid-1234",
				Labels:    map[string]string{"app": "nginx"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "nginx"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
			Status: appsv1.DeploymentStatus{
				Replicas:      1,
				ReadyReplicas: 1,
			},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx-abc",
				Namespace: "default",
				UID:       "rs-uid-5678",
				Labels:    map[string]string{"app": "nginx"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "nginx",
					UID:        types.UID(deployUID),
					Controller: boolPtr(true),
				}},
			},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "nginx"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "nginx"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}}},
				},
			},
			Status: appsv1.ReplicaSetStatus{
				Replicas:      1,
				ReadyReplicas: 1,
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx-abc-xyz",
				Namespace: "default",
				Labels:    map[string]string{"app": "nginx"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       "nginx-abc",
					UID:        types.UID(rsUID),
					Controller: boolPtr(true),
				}},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "nginx",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx",
				Namespace: "default",
				Labels:    map[string]string{"app": "nginx"},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "nginx"},
				Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(80)}},
			},
		},
	)

	// Initialize cache from fake client (bypasses RBAC checks)
	if err := k8s.InitTestResourceCache(fakeClient); err != nil {
		panic("InitTestResourceCache: " + err.Error())
	}

	// Mark cluster as connected so requireConnected guards pass
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:   k8s.StateConnected,
		Context: "fake-test",
	})

	// Initialize the timeline store so /api/changes endpoints work
	if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
		panic("InitStore: " + err.Error())
	}

	srv := New(Config{DevMode: true})
	testServer = httptest.NewServer(srv.Handler())

	code := m.Run()

	testServer.Close()
	srv.Stop()
	timeline.ResetStore()
	k8s.ResetTestState()

	os.Exit(code)
}

// --- Smoke tests ---

func TestSmokeHealth(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["status"] != "healthy" {
		t.Errorf("expected status=healthy, got %v", body["status"])
	}

	count, _ := body["resourceCount"].(float64)
	if count < 1 {
		t.Errorf("expected resourceCount >= 1, got %v", count)
	}
}

func TestSmokeDashboard(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/dashboard")
	if err != nil {
		t.Fatalf("GET /api/dashboard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Verify expected top-level fields
	for _, key := range []string{"health", "resourceCounts", "cluster"} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing expected field %q", key)
		}
	}

	// Verify resource counts include at least 1 deployment
	rc, _ := body["resourceCounts"].(map[string]any)
	if rc == nil {
		t.Fatal("resourceCounts is nil")
	}
	deps, _ := rc["deployments"].(map[string]any)
	if deps == nil {
		t.Fatal("resourceCounts.deployments is nil")
	}
	total, _ := deps["total"].(float64)
	if total < 1 {
		t.Errorf("expected deployments.total >= 1, got %v", total)
	}

	// Verify helmReleases is NOT in resourceCounts (catches orphaned field regression)
	if _, hasHelm := rc["helmReleases"]; hasHelm {
		t.Error("resourceCounts should NOT contain helmReleases (it was moved to /api/dashboard/helm)")
	}
}

func TestSmokeDashboardHelm(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/dashboard/helm")
	if err != nil {
		t.Fatalf("GET /api/dashboard/helm: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Should have a releases array (empty is fine)
	if _, ok := body["releases"]; !ok {
		t.Error("missing expected field 'releases'")
	}
}

func TestSmokeDashboardCRDs(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/dashboard/crds")
	if err != nil {
		t.Fatalf("GET /api/dashboard/crds: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSmokeDashboardHelmRequiresConnection(t *testing.T) {
	// Temporarily set disconnected
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State: k8s.StateDisconnected,
	})
	defer k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:   k8s.StateConnected,
		Context: "fake-test",
	})

	resp, err := http.Get(testServer.URL + "/api/dashboard/helm")
	if err != nil {
		t.Fatalf("GET /api/dashboard/helm: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestSmokeTopology(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/topology")
	if err != nil {
		t.Fatalf("GET /api/topology: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, ok := body["nodes"]; !ok {
		t.Error("missing expected field 'nodes'")
	}
}

func TestSmokeNamespaces(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/namespaces")
	if err != nil {
		t.Fatalf("GET /api/namespaces: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body) < 1 {
		t.Error("expected at least 1 namespace")
	}
}

func TestSmokeListPods(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/resources/pods")
	if err != nil {
		t.Fatalf("GET /api/resources/pods: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body []any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body) < 1 {
		t.Error("expected at least 1 pod")
	}
}

func TestSmokeListDeployments(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/resources/deployments")
	if err != nil {
		t.Fatalf("GET /api/resources/deployments: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body []any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body) < 1 {
		t.Error("expected at least 1 deployment")
	}
}

func TestSmokeGetDeployment(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/resources/deployments/default/nginx")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Should have the resource and relationships wrapper
	if _, ok := body["resource"]; !ok {
		t.Error("missing 'resource' field in response")
	}
}

func TestSmokeGetResourceNotFound(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/resources/deployments/default/nonexistent")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestSmokeEvents(t *testing.T) {
	// Events use a deferred informer — this verifies that deferred sync
	// completed and the events lister is non-nil.
	resp, err := http.Get(testServer.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	// 200 with empty array is fine — the key thing is it doesn't 403 or 500
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func boolPtr(b bool) *bool { return &b }

// --- Helpers ---

// get is a small helper that issues a GET and returns the response, failing the
// test on a network error. The caller is responsible for closing the body.
func get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// put issues a PUT with a JSON body and returns the response.
func put(t *testing.T, path string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, testServer.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return resp
}

// assertOK checks for HTTP 200 and decodes the body as JSON into dst.
// dst may be a *map[string]any or *[]any; pass nil to skip decoding.
func assertOK(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if dst == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// assertNoError checks that a decoded map[string]any does not have an "error" field.
func assertNoError(t *testing.T, body map[string]any) {
	t.Helper()
	if errVal, ok := body["error"]; ok {
		t.Errorf("unexpected error field: %v", errVal)
	}
}

// assertKeys checks that all expected keys are present in body.
func assertKeys(t *testing.T, body map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := body[k]; !ok {
			t.Errorf("missing expected field %q", k)
		}
	}
}

// --- Topology ---

func TestSmokeTopologyTrafficView(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/topology?view=traffic"), &body)
	assertNoError(t, body)
	assertKeys(t, body, "nodes", "edges")
}

func TestSmokeTopologyResourcesView(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/topology?view=resources"), &body)
	assertNoError(t, body)
	assertKeys(t, body, "nodes", "edges")
}

func TestSmokeTopologyNamespaceFilter(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/topology?namespace=default"), &body)
	assertNoError(t, body)
	assertKeys(t, body, "nodes", "edges")

	nodes, _ := body["nodes"].([]any)
	if len(nodes) == 0 {
		t.Error("expected at least 1 node for namespace=default")
	}
}

// --- Resources (list) ---

func TestSmokeListResources(t *testing.T) {
	kinds := []string{
		"services",
		"replicasets",
		"configmaps",
		"secrets",
		"nodes",
		"namespaces",
	}
	for _, kind := range kinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			var body []any
			assertOK(t, get(t, "/api/resources/"+kind), &body)
		})
	}
}

func TestSmokeListResourcesCaseInsensitive(t *testing.T) {
	// handleListResources should normalize the kind parameter to lowercase,
	// matching the behavior of handleGetResource.
	kinds := []string{"Pods", "PODS", "Deployments", "Services"}
	for _, kind := range kinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			var body []any
			assertOK(t, get(t, "/api/resources/"+kind), &body)
		})
	}
}

func TestSmokeListResourcesNamespaceFilter(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/resources/deployments?namespace=default"), &body)
	if len(body) == 0 {
		t.Error("expected at least 1 deployment in namespace=default")
	}
}

func TestSmokeGetService(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/resources/services/default/nginx"), &body)
	assertNoError(t, body)
	if _, ok := body["resource"]; !ok {
		t.Error("missing 'resource' field")
	}
}

func TestSmokeGetResourceBadKind(t *testing.T) {
	resp := get(t, "/api/resources/doesnotexist/default/foo")
	defer resp.Body.Close()
	// Unknown kind returns some error status (400/404/500 depending on dynamic cache availability).
	// The important thing is it returns valid JSON, not a panic.
	if resp.StatusCode < 400 {
		t.Errorf("expected an error status for unknown kind, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("expected valid JSON error response, decode failed: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Error("expected 'error' field in error response")
	}
}

// --- Timeline / Changes ---

func TestSmokeChanges(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/changes"), &body)
	// Empty slice is fine — the store is fresh; just ensure no error
}

func TestSmokeChangesWithFilters(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/changes?namespace=default&kind=Deployment&limit=10"), &body)
}

func TestSmokeChangeChildren(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/changes/deployments/default/nginx/children"), &body)
}

// --- AI resources ---

func TestSmokeAIListDeployments(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/ai/resources/deployments"), &body)
	if len(body) == 0 {
		t.Error("expected at least 1 AI resource result")
	}
}

func TestSmokeAIListVerbosities(t *testing.T) {
	for _, v := range []string{"summary", "detail", "compact"} {
		v := v
		t.Run(v, func(t *testing.T) {
			var body []any
			assertOK(t, get(t, "/api/ai/resources/deployments?verbosity="+v), &body)
		})
	}
}

func TestSmokeAIGetDeployment(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/ai/resources/deployments/default/nginx"), &body)
	assertNoError(t, body)
}

func TestSmokeAIGetResourceNotFound(t *testing.T) {
	resp := get(t, "/api/ai/resources/deployments/default/nonexistent")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// --- Misc read endpoints ---

func TestSmokeConnection(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/connection"), &body)
	assertKeys(t, body, "state", "context", "contexts")
	if body["state"] != string(k8s.StateConnected) {
		t.Errorf("expected state=%q, got %v", k8s.StateConnected, body["state"])
	}
}

func TestSmokeSessions(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/sessions"), &body)
	assertKeys(t, body, "portForwards", "execSessions", "total")
}

func TestSmokePortForwards(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/portforwards"), &body)
	// Empty is fine; just ensure 200 + array
}

func TestSmokeContexts(t *testing.T) {
	var body []any
	assertOK(t, get(t, "/api/contexts"), &body)
}

// --- Debug endpoints ---

func TestSmokeDebugEvents(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/debug/events"), &body)
	assertKeys(t, body, "counters", "store_stats", "recent_drops")
}

func TestSmokeDebugEventsDiagnoseMissingParams(t *testing.T) {
	resp := get(t, "/api/debug/events/diagnose")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without required params, got %d", resp.StatusCode)
	}
}

func TestSmokeDebugEventsDiagnose(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/debug/events/diagnose?kind=Deployment&namespace=default&name=nginx"), &body)
}

func TestSmokeDebugInformers(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/debug/informers"), &body)
	assertKeys(t, body, "typedInformers", "dynamicInformers", "watchedResources")
}

// --- Workload sub-resources ---

func TestSmokeWorkloadPods(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/workloads/deployments/default/nginx/pods"), &body)
	assertNoError(t, body)
	if _, ok := body["pods"]; !ok {
		t.Error("missing 'pods' field")
	}
}

func TestSmokeWorkloadPodsNotFound(t *testing.T) {
	resp := get(t, "/api/workloads/deployments/default/nonexistent/pods")
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusInternalServerError {
		t.Errorf("unexpected 500 for nonexistent workload")
	}
}

// --- Metrics (nil dynamic client guard) ---

// TestSmokeMetricsNilDynamicClient verifies the metrics endpoints return a proper
// error (not a panic) when the dynamic client is not yet initialized.
// Regression test for: pkg/k8score/metrics.go calling client.Resource() on nil.
func TestSmokeMetricsNilDynamicClient(t *testing.T) {
	// In the test environment GetDynamicClient() returns nil (no dynamic cache was
	// initialized). A nil-guard bug causes a panic; chi's Recoverer converts it to
	// a 500 with no JSON body — we expect a proper JSON error response.
	for _, path := range []string{
		"/api/metrics/pods/default/nginx-abc-xyz",
		"/api/metrics/nodes/fake-node",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			resp := get(t, path)
			defer resp.Body.Close()
			// Must not be a panic-induced empty 500; JSON body required.
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("expected valid JSON response (not a panic), got decode error: %v (status=%d)", err, resp.StatusCode)
			}
		})
	}
}

// --- Settings & Config endpoints ---

func TestSmokeGetSettings(t *testing.T) {
	var body map[string]any
	assertOK(t, get(t, "/api/settings"), &body)
	// Should return valid JSON (may have theme, pinnedKinds, or be empty)
}

func TestSmokePutSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	payload := `{"theme":"light"}`
	resp := put(t, "/api/settings", payload)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["theme"] != "light" {
		t.Errorf("theme = %v, want light", body["theme"])
	}

	// Verify it persists — GET should return the same values
	var loaded map[string]any
	assertOK(t, get(t, "/api/settings"), &loaded)
	if loaded["theme"] != "light" {
		t.Errorf("persisted theme = %v, want light", loaded["theme"])
	}
}

func TestSmokePutSettingsPreservesExisting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Set theme first
	put(t, "/api/settings", `{"theme":"dark"}`)

	// Now set pinnedKinds without theme — theme should be preserved
	resp := put(t, "/api/settings", `{"pinnedKinds":[{"name":"pods","kind":"Pod","group":""}]}`)
	defer resp.Body.Close()

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["theme"] != "dark" {
		t.Errorf("theme was overwritten: got %v", body["theme"])
	}
	pinnedKinds, ok := body["pinnedKinds"].([]any)
	if !ok || len(pinnedKinds) != 1 {
		t.Errorf("pinnedKinds = %v, want 1 entry", body["pinnedKinds"])
	}
}

func TestSmokeGetConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var body map[string]any
	assertOK(t, get(t, "/api/config"), &body)
	assertKeys(t, body, "file", "effective", "isDesktop")
}

func TestSmokePutConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	resp := put(t, "/api/config", `{"kubeconfig":"/tmp/test-kube","port":9999,"namespace":"staging"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var saved map[string]any
	json.NewDecoder(resp.Body).Decode(&saved)
	if saved["kubeconfig"] != "/tmp/test-kube" {
		t.Errorf("kubeconfig = %v, want /tmp/test-kube", saved["kubeconfig"])
	}
	if saved["port"] != float64(9999) {
		t.Errorf("port = %v, want 9999", saved["port"])
	}

	// Verify persisted via GET
	var got map[string]any
	assertOK(t, get(t, "/api/config"), &got)
	file, _ := got["file"].(map[string]any)
	if file["kubeconfig"] != "/tmp/test-kube" {
		t.Errorf("persisted kubeconfig = %v", file["kubeconfig"])
	}
}

func TestSmokePutConfigReplaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// First write with kubeconfig and port
	put(t, "/api/config", `{"kubeconfig":"/a","port":1111}`)

	// Second write with only namespace — kubeconfig and port should be gone (full replace)
	put(t, "/api/config", `{"namespace":"prod"}`)

	var got map[string]any
	assertOK(t, get(t, "/api/config"), &got)
	file, _ := got["file"].(map[string]any)
	if file["kubeconfig"] != nil {
		t.Errorf("kubeconfig should be cleared after full replace, got %v", file["kubeconfig"])
	}
	if file["namespace"] != "prod" {
		t.Errorf("namespace = %v, want prod", file["namespace"])
	}
}

func TestSmokePutSettingsInvalidBody(t *testing.T) {
	resp := put(t, "/api/settings", "not json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid body, got %d", resp.StatusCode)
	}
}

func TestSmokePutConfigInvalidBody(t *testing.T) {
	resp := put(t, "/api/config", "not json")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid body, got %d", resp.StatusCode)
	}
}

// --- requireConnected guard (table-driven) ---

func TestSmokeRequireConnected(t *testing.T) {
	endpoints := []string{
		"/api/topology",
		"/api/namespaces",
		"/api/resources/pods",
		"/api/resources/deployments/default/nginx",
		"/api/changes",
		"/api/ai/resources/deployments",
	}

	// Temporarily disconnect
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected})
	defer k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:   k8s.StateConnected,
		Context: "fake-test",
	})

	for _, path := range endpoints {
		path := path
		t.Run(path, func(t *testing.T) {
			resp := get(t, path)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("expected 503 when disconnected, got %d", resp.StatusCode)
			}
		})
	}
}
