package server

import (
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

	srv := New(Config{DevMode: true})
	testServer = httptest.NewServer(srv.Handler())

	code := m.Run()

	testServer.Close()
	srv.Stop()
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
