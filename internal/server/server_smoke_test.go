package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
)

var (
	testServer    *httptest.Server
	testServerSrv *Server
)

func TestMain(m *testing.M) {
	replicas := int32(1)
	brokenReplicas := int32(3)

	deployUID := "deploy-uid-1234"
	rsUID := "rs-uid-5678"

	fakeClient := fake.NewClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "broken"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		// Broken Deployment in its own namespace so it doesn't perturb the
		// "default" fixture used by every other smoke test. Used by
		// TestAIGetResource_IssueSummaryCountsURLPluralKind to assert the
		// composer's URL-plural-kind filter actually matches the canonical
		// Pascal-singular Issue.Kind values — pre-fix, count was 0.
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "stuck-app",
				Namespace: "broken",
				Labels:    map[string]string{"app": "stuck"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &brokenReplicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "stuck"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "stuck"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "stuck", Image: "registry.example/stuck:1"}}},
				},
			},
			Status: appsv1.DeploymentStatus{
				Replicas:            3,
				AvailableReplicas:   0,
				UnavailableReplicas: 3,
			},
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
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "nginx", Image: "nginx:1.25"}},
						// Reference nginx-tls so the topology builder includes
						// the Secret node when IncludeSecrets=true. The
						// neighborhood handler's Secret-root tests depend on
						// this — without a reference the Secret would be
						// elided regardless of IncludeSecrets.
						Volumes: []corev1.Volume{{
							Name: "tls",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "nginx-tls"},
							},
						}},
					},
				},
			},
			Status: appsv1.DeploymentStatus{
				Replicas:      1,
				ReadyReplicas: 1,
			},
		},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "nginx-hpa", Namespace: "default", Generation: 1},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "nginx"},
				MinReplicas:    &replicas,
				MaxReplicas:    2,
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{
				ObservedGeneration: ptrInt64(1),
				CurrentReplicas:    2,
				DesiredReplicas:    2,
				Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
					{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "the desired replica count is more than the maximum replica count"},
				},
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
		// Ingress routing to the core Service "nginx". Used by
		// TestAIGetResource_GroupRoutesRelationshipsToKnative to give the
		// core Service a distinct incoming edge (EdgeRoutesTo) that the
		// Knative Service node does NOT inherit — the test compares whether
		// the AI GET handler picks up that edge under ?group=serving.knative.dev
		// (regression for the kind-passed-to-relationship-lookup bug).
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nginx-ingress",
				Namespace: "default",
			},
			Spec: networkingv1.IngressSpec{
				Rules: []networkingv1.IngressRule{{
					Host: "nginx.example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{{
								Path:     "/",
								PathType: func() *networkingv1.PathType { p := networkingv1.PathTypePrefix; return &p }(),
								Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: "nginx",
										Port: networkingv1.ServiceBackendPort{Number: 80},
									},
								},
							}},
						},
					},
				}},
			},
		},
		// Seed Secrets in two namespaces so per-user RBAC tests can
		// distinguish "gate denied → []" from "no secrets in cache" and can
		// exercise the partial-allow case (one ns allowed, the other denied).
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "nginx-tls", Namespace: "default"},
			Type:       corev1.SecretTypeOpaque,
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "system-token", Namespace: "kube-system"},
			Type:       corev1.SecretTypeOpaque,
		},
		// RBAC fixtures: one SA, a Role/RoleBinding pair binding it, a
		// ClusterRole/ClusterRoleBinding grant to system:authenticated so
		// rbac_handlers_test can exercise both direct + inherited paths.
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: "app-sa", Namespace: "default"},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "app-reader", Namespace: "default"},
			Rules: []rbacv1.PolicyRule{{
				Verbs: []string{"get", "list"}, APIGroups: []string{""}, Resources: []string{"pods"},
			}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "app-binding", Namespace: "default"},
			RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "app-reader", APIGroup: "rbac.authorization.k8s.io"},
			Subjects: []rbacv1.Subject{{
				Kind: "ServiceAccount", Namespace: "default", Name: "app-sa",
			}},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "rbac-test-view"},
			Rules: []rbacv1.PolicyRule{{
				Verbs: []string{"list"}, APIGroups: []string{""}, Resources: []string{"namespaces"},
			}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "rbac-test-auth-view"},
			RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "rbac-test-view", APIGroup: "rbac.authorization.k8s.io"},
			Subjects: []rbacv1.Subject{{
				Kind: "Group", Name: "system:authenticated", APIGroup: "rbac.authorization.k8s.io",
			}},
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
	testServerSrv = srv
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

	timelineStats, ok := body["timeline"].(map[string]any)
	if !ok {
		t.Fatalf("expected timeline stats object, got %T", body["timeline"])
	}
	if _, ok := timelineStats["total_events"]; ok {
		t.Error("health endpoint should not run live timeline event counts")
	}
	if timelineStats["store_present"] != true {
		t.Errorf("expected store_present=true, got %v", timelineStats["store_present"])
	}
}

func TestSmokeMetricsEndpoint(t *testing.T) {
	resp := get(t, "/metrics")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected Prometheus text content type, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	for _, want := range []string{
		"radar_sse_connected_clients",
		"radar_sse_topology_broadcasts_total",
		"radar_sse_dropped_events_total",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("expected %s in metrics output, got:\n%s", want, body)
		}
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

func TestSmokeTopPodsHonorsNamespaceFilter(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/metrics/top/pods?namespaces=default")
	if err != nil {
		t.Fatalf("GET /api/metrics/top/pods?namespaces=default: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body []k8s.TopPodMetrics
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body) < 1 {
		t.Fatal("expected at least 1 pod metric row")
	}
	for _, pod := range body {
		if pod.Namespace != "default" {
			t.Fatalf("expected only default namespace rows, got %s/%s", pod.Namespace, pod.Name)
		}
	}

	resp, err = http.Get(testServer.URL + "/api/metrics/top/pods?namespaces=missing")
	if err != nil {
		t.Fatalf("GET /api/metrics/top/pods?namespaces=missing: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for missing namespace filter, got %d", resp.StatusCode)
	}

	body = nil
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode missing namespace response: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("expected missing namespace filter to return no pod metrics, got %d rows", len(body))
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

func TestSmokeGetHPAIncludesDiagnosis(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/resources/hpas/default/nginx-hpa")
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
	diagnosis, ok := body["hpaDiagnosis"].(map[string]any)
	if !ok {
		t.Fatalf("missing hpaDiagnosis in response: %+v", body)
	}
	if diagnosis["state"] != "limited_max" {
		t.Fatalf("hpaDiagnosis.state = %v, want limited_max", diagnosis["state"])
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

func ptrInt64(v int64) *int64 { return &v }

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

func TestSmokeChangesNameFilter(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	clusterContext := k8s.ActiveClusterContext()
	events := []timeline.TimelineEvent{
		{
			ID:             "smoke-name-filter-keep",
			Timestamp:      now,
			Source:         timeline.SourceInformer,
			Kind:           "Deployment",
			Namespace:      "default",
			Name:           "smoke-name-filter-keep",
			EventType:      timeline.EventTypeUpdate,
			ClusterContext: clusterContext,
		},
		{
			ID:             "smoke-name-filter-drop",
			Timestamp:      now,
			Source:         timeline.SourceInformer,
			Kind:           "Deployment",
			Namespace:      "default",
			Name:           "smoke-name-filter-drop",
			EventType:      timeline.EventTypeUpdate,
			ClusterContext: clusterContext,
		},
	}
	if err := timeline.RecordEvents(ctx, events); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}

	var body []timeline.TimelineEvent
	assertOK(t, get(t, "/api/changes?namespace=default&kind=Deployment&name=smoke-name-filter-keep&include_managed=true&filter=all&limit=10"), &body)
	if len(body) != 1 {
		t.Fatalf("expected exactly one name-filtered event, got %d: %+v", len(body), body)
	}
	if body[0].Name != "smoke-name-filter-keep" {
		t.Fatalf("expected smoke-name-filter-keep, got %q", body[0].Name)
	}
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

func TestMetricsAPIUnavailable(t *testing.T) {
	// These cases cover typed Kubernetes API errors first, then message shapes
	// emitted by apimachinery discovery/restmapper, kubectl top-style metrics
	// failures, and API aggregation 503s. Keep broad phrases gated by a
	// metrics-specific signal so unrelated NotFound/Unavailable errors stay visible.
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "bare metrics API absence from apiserver",
			err:  errors.New("failed to get node metrics: the server could not find the requested resource"),
			want: true,
		},
		{
			name: "metrics not found",
			err:  errors.New(`failed to get pod metrics: pods.metrics.k8s.io "api" not found`),
			want: true,
		},
		{
			name: "metrics no matches for kind",
			err:  errors.New(`failed to get pod metrics: no matches for kind "PodMetrics" in version "metrics.k8s.io/v1beta1"`),
			want: true,
		},
		{
			name: "metrics no resource matches",
			err:  errors.New(`failed to get pod metrics: no resource matches "pods.metrics.k8s.io"`),
			want: true,
		},
		{
			name: "metrics not available",
			err:  errors.New(`failed to get pod metrics: pods.metrics.k8s.io not available`),
			want: true,
		},
		{
			name: "no metrics known",
			err:  errors.New(`failed to get pod metrics: no metrics known for pod "api" in pods.metrics.k8s.io`),
			want: true,
		},
		{
			name: "unable to fetch metrics",
			err:  errors.New(`failed to get pod metrics: unable to fetch metrics from pods.metrics.k8s.io`),
			want: true,
		},
		{
			name: "metrics APIService unavailable",
			err:  errors.New(`failed to get pod metrics: the server is currently unable to handle the request (get pods.metrics.k8s.io)`),
			want: true,
		},
		{
			name: "non-metrics missing resource stays visible",
			err:  errors.New("the server could not find the requested resource"),
			want: false,
		},
		{
			name: "metrics forbidden stays visible",
			err:  errors.New("failed to get node metrics: forbidden"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := k8score.MetricsAPIUnavailable(tt.err); got != tt.want {
				t.Fatalf("MetricsAPIUnavailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMetricsHistoryResponseCarriesCollectionErrorWithBufferedHistory(t *testing.T) {
	health := k8s.MetricsCollectionHealth{
		PodMetrics: k8s.MetricsSourceHealth{
			ConsecutiveErrors: 1,
			LastError:         "the server could not find the requested resource",
		},
		NodeMetrics: k8s.MetricsSourceHealth{
			ConsecutiveErrors: 1,
			LastError:         "the server could not find the requested resource",
		},
	}

	podHistory := podMetricsHistoryResponse(context.Background(), &k8s.PodMetricsHistory{
		Namespace: "default",
		Name:      "api",
		Containers: []k8s.ContainerMetricsHistory{{
			Name: "api",
			DataPoints: []k8s.MetricsDataPoint{{
				Timestamp: time.Now(),
				CPU:       100000000,
				Memory:    268435456,
			}},
		}},
	}, "default", "api", health, true)
	if podHistory.CollectionError != "Pod metrics not found (metrics-server may not be installed)" {
		t.Fatalf("pod collection error = %q", podHistory.CollectionError)
	}
	if podHistory.RawCollectionError != health.PodMetrics.LastError {
		t.Fatalf("pod raw collection error = %q, want %q", podHistory.RawCollectionError, health.PodMetrics.LastError)
	}
	if !podHistory.MetricsUnavailable {
		t.Fatalf("pod metrics unavailable = false, want true")
	}

	nodeHistory := nodeMetricsHistoryResponse(context.Background(), &k8s.NodeMetricsHistory{
		Name: "kind-worker",
		DataPoints: []k8s.MetricsDataPoint{{
			Timestamp: time.Now(),
			CPU:       100000000,
			Memory:    268435456,
		}},
	}, "kind-worker", health, true)
	if nodeHistory.CollectionError != "Node metrics not found (metrics-server may not be installed)" {
		t.Fatalf("node collection error = %q", nodeHistory.CollectionError)
	}
	if nodeHistory.RawCollectionError != health.NodeMetrics.LastError {
		t.Fatalf("node raw collection error = %q, want %q", nodeHistory.RawCollectionError, health.NodeMetrics.LastError)
	}
	if !nodeHistory.MetricsUnavailable {
		t.Fatalf("node metrics unavailable = false, want true")
	}
}

func TestMetricsHistoryResponseKeepsNonAbsenceCollectionErrors(t *testing.T) {
	health := k8s.MetricsCollectionHealth{
		PodMetrics: k8s.MetricsSourceHealth{
			ConsecutiveErrors: 1,
			LastError:         "forbidden: no access to pods.metrics.k8s.io",
		},
		NodeMetrics: k8s.MetricsSourceHealth{
			ConsecutiveErrors: 1,
			LastError:         "forbidden: no access to nodes.metrics.k8s.io",
		},
	}

	podHistory := podMetricsHistoryResponse(context.Background(), nil, "default", "api", health, true)
	if podHistory.CollectionError != health.PodMetrics.LastError {
		t.Fatalf("pod collection error = %q, want %q", podHistory.CollectionError, health.PodMetrics.LastError)
	}
	if podHistory.RawCollectionError != "" {
		t.Fatalf("pod raw collection error = %q, want empty", podHistory.RawCollectionError)
	}
	if podHistory.MetricsUnavailableDiagnosis != "" {
		t.Fatalf("pod metrics unavailable diagnosis = %q, want empty", podHistory.MetricsUnavailableDiagnosis)
	}
	if podHistory.MetricsUnavailable {
		t.Fatalf("pod metrics unavailable = true, want false")
	}

	nodeHistory := nodeMetricsHistoryResponse(context.Background(), nil, "kind-worker", health, true)
	if nodeHistory.CollectionError != health.NodeMetrics.LastError {
		t.Fatalf("node collection error = %q, want %q", nodeHistory.CollectionError, health.NodeMetrics.LastError)
	}
	if nodeHistory.RawCollectionError != "" {
		t.Fatalf("node raw collection error = %q, want empty", nodeHistory.RawCollectionError)
	}
	if nodeHistory.MetricsUnavailableDiagnosis != "" {
		t.Fatalf("node metrics unavailable diagnosis = %q, want empty", nodeHistory.MetricsUnavailableDiagnosis)
	}
	if nodeHistory.MetricsUnavailable {
		t.Fatalf("node metrics unavailable = true, want false")
	}
}

func TestMetricsAPIServiceDiagnosis(t *testing.T) {
	tests := []struct {
		name      string
		condition map[string]any
		want      string
	}{
		{
			name: "available true points away from installation",
			condition: map[string]any{
				"type":   "Available",
				"status": "True",
			},
			want: "The v1beta1.metrics.k8s.io APIService is Available, but metrics reads still fail. Check metrics-server logs and API aggregation errors.",
		},
		{
			name: "available false includes reason",
			condition: map[string]any{
				"type":   "Available",
				"status": "False",
				"reason": "FailedDiscoveryCheck",
			},
			want: "The v1beta1.metrics.k8s.io APIService is not Available (FailedDiscoveryCheck). Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.",
		},
		{
			name: "available false includes compact condition message",
			condition: map[string]any{
				"type":    "Available",
				"status":  "False",
				"reason":  "FailedDiscoveryCheck",
				"message": "failing or missing response from https://10.96.142.7:443/apis/metrics.k8s.io/v1beta1: Get \"https://10.96.142.7\": connect: connection refused",
			},
			want: "The v1beta1.metrics.k8s.io APIService is not Available (FailedDiscoveryCheck): failing or missing response from https://10.96.142.7:443/apis/metrics.k8s.io/v1beta1: Get \"https://10.96.142.7\": connect: connection refused. Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.",
		},
		{
			name: "available false preserves ellipsis in condition message",
			condition: map[string]any{
				"type":    "Available",
				"status":  "False",
				"message": "timed out waiting...",
			},
			want: "The v1beta1.metrics.k8s.io APIService is not Available: timed out waiting... Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.",
		},
		{
			name: "available false trims dangling condition punctuation",
			condition: map[string]any{
				"type":    "Available",
				"status":  "False",
				"message": "connection refused:",
			},
			want: "The v1beta1.metrics.k8s.io APIService is not Available: connection refused. Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.",
		},
		{
			name: "available unknown includes reason",
			condition: map[string]any{
				"type":   "Available",
				"status": "Unknown",
				"reason": "ServiceNotFound",
			},
			want: "The v1beta1.metrics.k8s.io APIService is not Available (ServiceNotFound). Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiService := &unstructured.Unstructured{Object: map[string]any{
				"status": map[string]any{
					"conditions": []any{tt.condition},
				},
			}}
			if got := metricsAPIServiceDiagnosis(apiService, true); got != tt.want {
				t.Fatalf("metricsAPIServiceDiagnosis() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetricsAPIServiceDiagnosisCanHideConditionMessage(t *testing.T) {
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":    "Available",
					"status":  "False",
					"reason":  "FailedDiscoveryCheck",
					"message": "failing or missing response from https://10.96.142.7:443/apis/metrics.k8s.io/v1beta1",
				},
			},
		},
	}}
	want := "The v1beta1.metrics.k8s.io APIService is not Available (FailedDiscoveryCheck). Check the metrics-server Service, endpoints, and API aggregation/TLS configuration."
	if got := metricsAPIServiceDiagnosis(apiService, false); got != want {
		t.Fatalf("metricsAPIServiceDiagnosis() = %q, want %q", got, want)
	}
}

func TestMetricsAPIServiceDiagnosisWithoutAvailableCondition(t *testing.T) {
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "ServiceHealthy", "status": "True"},
			},
		},
	}}
	want := "The v1beta1.metrics.k8s.io APIService exists but has no Available condition. Check metrics-server and API aggregation status."
	if got := metricsAPIServiceDiagnosis(apiService, true); got != want {
		t.Fatalf("metricsAPIServiceDiagnosis() = %q, want %q", got, want)
	}
}

func TestMetricsAPIServiceLookupDiagnosisWhenMissing(t *testing.T) {
	err := apierrors.NewNotFound(schema.GroupResource{Group: metricsAPIServiceGroup, Resource: "apiservices"}, metricsAPIServiceName)
	want := "The v1beta1.metrics.k8s.io APIService is not registered. Install metrics-server or restore that APIService."
	if got := metricsAPIServiceLookupDiagnosis(nil, err, true); got != want {
		t.Fatalf("metricsAPIServiceLookupDiagnosis() = %q, want %q", got, want)
	}
}

func TestMetricsUnavailableDiagnosisWhenAPIServiceMissing(t *testing.T) {
	resetMetricsAPIServiceDiagnosisMemoForTest(t)
	defer k8s.ResetTestDynamicState()

	gvr := schema.GroupVersionResource{Group: metricsAPIServiceGroup, Version: "v1", Resource: "apiservices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "APIServiceList"},
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group:      metricsAPIServiceGroup,
		Version:    "v1",
		Kind:       metricsAPIServiceKind,
		Name:       "apiservices",
		Namespaced: false,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	want := "The v1beta1.metrics.k8s.io APIService is not registered. Install metrics-server or restore that APIService."
	if got := metricsUnavailableDiagnosis(context.Background(), true); got != want {
		t.Fatalf("metricsUnavailableDiagnosis() = %q, want %q", got, want)
	}
}

func TestMetricsUnavailableDiagnosisMemoizesAPIServiceLookup(t *testing.T) {
	resetMetricsAPIServiceDiagnosisMemoForTest(t)
	defer k8s.ResetTestDynamicState()

	gvr := schema.GroupVersionResource{Group: metricsAPIServiceGroup, Version: "v1", Resource: "apiservices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "APIServiceList"},
	)
	gets := 0
	dyn.Fake.PrependReactor("get", "apiservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group:      metricsAPIServiceGroup,
		Version:    "v1",
		Kind:       metricsAPIServiceKind,
		Name:       "apiservices",
		Namespaced: false,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	for range 2 {
		if got := metricsUnavailableDiagnosis(context.Background(), true); got == "" {
			t.Fatalf("metricsUnavailableDiagnosis() returned empty diagnosis")
		}
	}
	if gets != 1 {
		t.Fatalf("APIService GET count = %d, want 1", gets)
	}
}

func TestMetricsUnavailableDiagnosisDoesNotMemoizeTransientAPIServiceLookupError(t *testing.T) {
	resetMetricsAPIServiceDiagnosisMemoForTest(t)
	defer k8s.ResetTestDynamicState()

	gvr := schema.GroupVersionResource{Group: metricsAPIServiceGroup, Version: "v1", Resource: "apiservices"}
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       metricsAPIServiceKind,
		"metadata": map[string]any{
			"name": metricsAPIServiceName,
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":   "Available",
					"status": "False",
					"reason": "FailedDiscoveryCheck",
				},
			},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "APIServiceList"},
		apiService,
	)
	gets := 0
	dyn.Fake.PrependReactor("get", "apiservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, nil, fmt.Errorf("temporary apiservice lookup failure")
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group:      metricsAPIServiceGroup,
		Version:    "v1",
		Kind:       metricsAPIServiceKind,
		Name:       "apiservices",
		Namespaced: false,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	if got := metricsUnavailableDiagnosis(context.Background(), true); got != "" {
		t.Fatalf("first metricsUnavailableDiagnosis() = %q, want empty transient diagnosis", got)
	}
	if got := metricsUnavailableDiagnosis(context.Background(), true); got == "" {
		t.Fatalf("second metricsUnavailableDiagnosis() returned empty diagnosis after transient error")
	}
	if gets != 2 {
		t.Fatalf("APIService GET count = %d, want 2", gets)
	}
}

func TestMetricsAPIServiceDiagnosisCacheDoesNotOverwriteDifferentContext(t *testing.T) {
	cache := metricsAPIServiceDiagnosisCache{ttl: time.Minute}
	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	resultA := make(chan string, 1)

	go func() {
		resultA <- cache.get("ctx-a", true, func() (string, bool) {
			close(startedA)
			<-releaseA
			return "ctx-a diagnosis", true
		})
	}()

	<-startedA
	if got := cache.get("ctx-b", true, func() (string, bool) {
		return "ctx-b diagnosis", true
	}); got != "ctx-b diagnosis" {
		t.Fatalf("ctx-b diagnosis = %q, want ctx-b diagnosis", got)
	}

	close(releaseA)
	if got := <-resultA; got != "ctx-a diagnosis" {
		t.Fatalf("ctx-a diagnosis = %q, want ctx-a diagnosis", got)
	}

	if got := cache.get("ctx-b", true, func() (string, bool) {
		t.Fatal("ctx-b cache entry was overwritten by ctx-a")
		return "", false
	}); got != "ctx-b diagnosis" {
		t.Fatalf("cached ctx-b diagnosis = %q, want ctx-b diagnosis", got)
	}
}

func TestMetricsUnavailableDiagnosisMemoizesByConditionDetailFlag(t *testing.T) {
	resetMetricsAPIServiceDiagnosisMemoForTest(t)
	defer k8s.ResetTestDynamicState()

	gvr := schema.GroupVersionResource{Group: metricsAPIServiceGroup, Version: "v1", Resource: "apiservices"}
	apiService := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiregistration.k8s.io/v1",
		"kind":       metricsAPIServiceKind,
		"metadata": map[string]any{
			"name": metricsAPIServiceName,
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":    "Available",
					"status":  "False",
					"reason":  "FailedDiscoveryCheck",
					"message": "failing or missing response from https://10.96.142.7:443/apis/metrics.k8s.io/v1beta1",
				},
			},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "APIServiceList"},
		apiService,
	)
	gets := 0
	dyn.Fake.PrependReactor("get", "apiservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group:      metricsAPIServiceGroup,
		Version:    "v1",
		Kind:       metricsAPIServiceKind,
		Name:       "apiservices",
		Namespaced: false,
	}}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	detailed := metricsUnavailableDiagnosis(context.Background(), true)
	if !strings.Contains(detailed, "10.96.142.7") {
		t.Fatalf("detailed diagnosis = %q, want condition message", detailed)
	}
	redacted := metricsUnavailableDiagnosis(context.Background(), false)
	if strings.Contains(redacted, "10.96.142.7") {
		t.Fatalf("redacted diagnosis leaked condition message: %q", redacted)
	}

	_ = metricsUnavailableDiagnosis(context.Background(), true)
	_ = metricsUnavailableDiagnosis(context.Background(), false)
	if gets != 2 {
		t.Fatalf("APIService GET count = %d, want 2 for two memo detail slots", gets)
	}
}

func resetMetricsAPIServiceDiagnosisMemoForTest(t *testing.T) {
	t.Helper()
	metricsAPIServiceDiagnosisMemo.mu.Lock()
	metricsAPIServiceDiagnosisMemo.entries = nil
	metricsAPIServiceDiagnosisMemo.mu.Unlock()
	t.Cleanup(func() {
		metricsAPIServiceDiagnosisMemo.mu.Lock()
		metricsAPIServiceDiagnosisMemo.entries = nil
		metricsAPIServiceDiagnosisMemo.mu.Unlock()
	})
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

// TestSmokeCloudMode_SettingsGetStripsUserScoped: under RADAR_CLOUD_MODE
// the GET /api/settings response must omit theme/pinnedKinds so Cloud's
// intercept layer owns the contract. Audit stays (cluster-shared admin
// policy).
func TestSmokeCloudMode_SettingsGetStripsUserScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Seed real values into the persisted store so we can prove they're
	// stripped at the HTTP boundary, not just missing from the file.
	put(t, "/api/settings", `{"theme":"dark","pinnedKinds":[{"name":"pods","kind":"Pod","group":""}]}`)

	t.Setenv("RADAR_CLOUD_MODE", "true")

	var body map[string]any
	assertOK(t, get(t, "/api/settings"), &body)
	if _, has := body["theme"]; has && body["theme"] != "" {
		t.Errorf("theme leaked under cloud mode: %v", body["theme"])
	}
	if _, has := body["pinnedKinds"]; has && body["pinnedKinds"] != nil {
		t.Errorf("pinnedKinds leaked under cloud mode: %v", body["pinnedKinds"])
	}
}

// TestSmokeCloudMode_SettingsPutRejectsUserScoped: under RADAR_CLOUD_MODE,
// a raw PUT attempting to set theme/pinnedKinds must be rejected. Cloud's
// intercept layer splits the body before forwarding; anything that reaches
// this endpoint with those fields set has bypassed the intercept and must
// not mutate shared settings.json.
func TestSmokeCloudMode_SettingsPutRejectsUserScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RADAR_CLOUD_MODE", "true")

	resp := put(t, "/api/settings", `{"theme":"dark"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT with theme under cloud mode got %d, want 400", resp.StatusCode)
	}

	resp2 := put(t, "/api/settings", `{"pinnedKinds":[{"name":"pods","kind":"Pod","group":""}]}`)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT with pinnedKinds under cloud mode got %d, want 400", resp2.StatusCode)
	}
}

// TestSmokeCloudMode_PprofNotMounted verifies that /debug/pprof/* is not
// registered under cloud-mode. The pprof heap endpoint would otherwise
// leak the in-memory K8s cache (every Secret, ConfigMap, Pod spec) through
// the Cloud tunnel. This test constructs a fresh server with
// RADAR_CLOUD_MODE set before Server.setupRoutes reads it, so the
// pprof-gate conditional fires.
func TestSmokeCloudMode_PprofNotMounted(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")

	srv := New(Config{DevMode: true})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer srv.Stop()

	paths := []string{
		"/debug/pprof/",
		"/debug/pprof/heap",
		"/debug/pprof/goroutine",
		"/debug/pprof/cmdline",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			resp, err := http.Get(ts.URL + p)
			if err != nil {
				t.Fatalf("GET %s: %v", p, err)
			}
			defer resp.Body.Close()
			// Under cloud-mode the route is not mounted. The index.html fallback
			// serves index.html (200) for unknown paths — what matters is
			// that it's NOT serving the pprof handler's dump output. A
			// 200 with the frontend HTML (not pprof data) is the pass
			// condition here.
			if resp.StatusCode == 200 {
				// Sanity: the response should be HTML (index.html fallback), not
				// a pprof dump.
				ct := resp.Header.Get("Content-Type")
				if !bytes.HasPrefix([]byte(ct), []byte("text/html")) {
					t.Errorf("%s returned 200 with Content-Type %q — pprof may still be mounted", p, ct)
				}
			}
			// 404 is also acceptable (no frontend handler in some test configs).
		})
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

	resp := put(t, "/api/config", `{"kubeconfig":"/tmp/test-kube","port":9999,"namespace":"staging","browser":"Safari"}`)
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
	if saved["browser"] != "Safari" {
		t.Errorf("browser = %v, want Safari", saved["browser"])
	}

	// Verify persisted via GET
	var got map[string]any
	assertOK(t, get(t, "/api/config"), &got)
	file, _ := got["file"].(map[string]any)
	if file["kubeconfig"] != "/tmp/test-kube" {
		t.Errorf("persisted kubeconfig = %v", file["kubeconfig"])
	}
	if file["browser"] != "Safari" {
		t.Errorf("persisted browser = %v", file["browser"])
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

// TestSmokeCapabilitiesShape locks down the JSON contract for
// /api/capabilities. The reflection-based alignment test in internal/k8s
// asserts the probe writes every field — this test asserts the HTTP layer
// surfaces every field as a JSON key. A regression that reverts to a
// hand-mapped block, adds an `omitempty` that hides false values, or
// changes the field-marshaling path would be caught here.
func TestSmokeCapabilitiesShape(t *testing.T) {
	resp := get(t, "/api/capabilities")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Resources      map[string]bool `json:"resources"`
		WorkloadWrites map[string]bool `json:"workloadWrites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Resources == nil {
		t.Fatal("capabilities response missing 'resources' field — handler probably dropped it on the floor")
	}

	// Enumerate every JSON tag on ResourcePermissions and assert it shows
	// up in the response. Adding a new struct field without wiring the
	// handler will fail here.
	permsType := reflect.TypeOf(k8s.ResourcePermissions{})
	for i := 0; i < permsType.NumField(); i++ {
		field := permsType.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" {
			t.Errorf("ResourcePermissions.%s has no json tag", field.Name)
			continue
		}
		if _, ok := body.Resources[tag]; !ok {
			t.Errorf("capabilities.resources missing key %q for ResourcePermissions.%s — "+
				"the handler isn't copying this field to the JSON response.",
				tag, field.Name)
		}
	}

	if body.WorkloadWrites == nil {
		t.Fatal("capabilities response missing 'workloadWrites' field")
	}
	workloadWritesType := reflect.TypeOf(k8s.WorkloadWritePermissions{})
	for i := 0; i < workloadWritesType.NumField(); i++ {
		field := workloadWritesType.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" {
			t.Errorf("WorkloadWritePermissions.%s has no json tag", field.Name)
			continue
		}
		if _, ok := body.WorkloadWrites[tag]; !ok {
			t.Errorf("capabilities.workloadWrites missing key %q for WorkloadWritePermissions.%s", tag, field.Name)
		}
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
