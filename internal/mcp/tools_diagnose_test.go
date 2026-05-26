package mcp

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// setupFakeCacheForDiagnoseTests stages a single Deployment with a matching
// Pod so diagnose's workload-rooted path (selector resolution + pod fan-out)
// can execute end-to-end against the fake cache. Separate from the shared
// filter-tests setup so adding new fixtures here doesn't perturb the broader
// list / search / RBAC test surface.
func setupFakeCacheForDiagnoseTests(t *testing.T) {
	t.Helper()

	const (
		ns         = "alpha"
		deployName = "cart"
	)
	selector := map[string]string{"app": "cart"}

	fakeClient := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
				},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cart-abc123",
				Namespace: ns,
				Labels:    selector,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "cart"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)

	if err := k8s.InitTestResourceCache(fakeClient); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(func() {
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
}

func TestNormalizeDiagnoseKind(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pod", "pods"},
		{"Pods", "pods"},
		{"  POD  ", "pods"},
		{"deployment", "deployments"},
		{"deployments", "deployments"},
		{"statefulset", "statefulsets"},
		{"StatefulSets", "statefulsets"},
		{"daemonset", "daemonsets"},
		{"DaemonSet", "daemonsets"},
		{"replicaset", ""},      // not in scope for diagnose
		{"job", ""},             // not in scope
		{"service", ""},         // not in scope
		{"deployment.apps", ""}, // groups not accepted in kind
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeDiagnoseKind(c.in); got != c.want {
			t.Errorf("normalizeDiagnoseKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHandleDiagnose_InvalidKind(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")

	_, _, err := handleDiagnose(ctx, nil, diagnoseInput{Kind: "service", Namespace: "alpha", Name: "alpha-pod"})
	if err == nil {
		t.Fatalf("expected error for unsupported kind, got nil")
	}
	if !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("expected 'invalid kind' error, got %v", err)
	}
}

func TestHandleDiagnose_MissingFields(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")

	if _, _, err := handleDiagnose(ctx, nil, diagnoseInput{Kind: "pod", Namespace: "", Name: "alpha-pod"}); err == nil {
		t.Errorf("expected error for empty namespace, got nil")
	}
	if _, _, err := handleDiagnose(ctx, nil, diagnoseInput{Kind: "pod", Namespace: "alpha", Name: ""}); err == nil {
		t.Errorf("expected error for empty name, got nil")
	}
}

func TestHandleDiagnose_ForbiddenNamespace(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	// User restricted to alpha; diagnose request targets beta.
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	_, _, err := handleDiagnose(ctx, nil, diagnoseInput{Kind: "pod", Namespace: "beta", Name: "beta-pod"})
	if err == nil {
		t.Fatalf("expected forbidden error, got nil")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected forbidden error, got %v", err)
	}
}

func TestHandleDiagnose_PodHappyPath(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")

	result, _, err := handleDiagnose(ctx, nil, diagnoseInput{
		Kind:      "pod",
		Namespace: "alpha",
		Name:      "alpha-pod",
	})
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	body := extractText(t, result)
	// The minified resource is at .resource — name should appear there.
	if !strings.Contains(body, "alpha-pod") {
		t.Errorf("expected pod name in response: %s", body)
	}
	// Pods count: 1 (the pod itself).
	if !strings.Contains(body, `"pods":1`) {
		t.Errorf("expected pods:1 in response: %s", body)
	}
}

func TestHandleDiagnose_PodNotFound(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")

	_, _, err := handleDiagnose(ctx, nil, diagnoseInput{
		Kind:      "pod",
		Namespace: "alpha",
		Name:      "ghost-pod",
	})
	if err == nil {
		t.Fatalf("expected error for non-existent pod, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

// TestHandleDiagnose_DeploymentResolvesPods exercises the workload-rooted
// path (kind=deployment → workload selector → fan-out to matching pods),
// which is the diagnose tool's headline use case. The pod-only tests above
// never traverse this branch — without this test, a regression in
// GetWorkloadSelector / GetPodsForWorkload / selector matching would ship
// undetected on the most common debug journey ("CrashLoopBackOff on a
// Deployment"). The fake test environment has no kube client on ctx, so
// logs surface as LogsError rather than empty arrays — that's the
// intended contract.
func TestHandleDiagnose_DeploymentResolvesPods(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	ctx := withClusterAdmin(t, "admin")

	result, _, err := handleDiagnose(ctx, nil, diagnoseInput{
		Kind:      "deployment",
		Namespace: "alpha",
		Name:      "cart",
	})
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	body := extractText(t, result)
	if !strings.Contains(body, `"name":"cart"`) {
		t.Errorf("expected deployment name in response: %s", body)
	}
	// Selector resolution should find the matching pod.
	if !strings.Contains(body, `"pods":1`) {
		t.Errorf("expected pods:1 (selector matched 1 pod): %s", body)
	}
	// No kube client on ctx in tests — diagnose surfaces this distinctly.
	if !strings.Contains(body, "logsError") {
		t.Errorf("expected logsError when no kube client present: %s", body)
	}
}

func TestHandleDiagnose_DeploymentNotFound(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	ctx := withClusterAdmin(t, "admin")

	_, _, err := handleDiagnose(ctx, nil, diagnoseInput{
		Kind:      "deployment",
		Namespace: "alpha",
		Name:      "ghost",
	})
	if err == nil {
		t.Fatalf("expected error for non-existent deployment, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

// TestStartupBlockersForWorkload_ScopesToWorkload pins the relevance filter:
// a namespace-wide detector sweep must attach only rows belonging to the
// diagnosed workload. This commit changed the contract (dropped the blanket
// "any ResourceQuota" arm), so the scoping is the load-bearing logic that
// prevents over-attributing unrelated failures to a healthy workload.
func TestStartupBlockersForWorkload_ScopesToWorkload(t *testing.T) {
	defer k8s.ResetTestState()
	// Diagnosed Deployment "cart": its ReplicaSet is admission-blocked
	// (created 0 of 2 pods, FailedCreate quota event) → must attach.
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "cart-abc123", Namespace: "alpha"},
		Spec:       appsv1.ReplicaSetSpec{Replicas: ptrInt32(2)},
		Status:     appsv1.ReplicaSetStatus{Replicas: 0},
	}
	rsEvt := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "alpha"},
		InvolvedObject: corev1.ObjectReference{Kind: "ReplicaSet", Namespace: "alpha", Name: "cart-abc123"},
		Reason:         "FailedCreate",
		Type:           corev1.EventTypeWarning,
		Message:        `Error creating: pods "x" is forbidden: exceeded quota: mem-quota, used: requests.memory=2Gi, limited: requests.memory=2Gi`,
		LastTimestamp:  metav1.Now(),
	}
	// An UNRELATED unschedulable pod in the same namespace → must NOT attach.
	otherPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: "alpha"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: "Unschedulable", Message: "0/1 nodes are available",
			}},
		},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(rs, rsEvt, otherPod)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(func() { k8s.ResetTestState() })

	// pods arg = cart's own pods (none created). The RS attaches via the
	// ReplicaSet-of-Deployment match, not via pod-name.
	out := startupBlockersForWorkload(k8s.GetResourceCache(), "deployments", "alpha", "cart", nil)

	var sawRS bool
	for _, b := range out {
		if b.Name == "other-pod" {
			t.Errorf("unrelated unschedulable pod must not attach to cart's startupBlockers: %+v", b)
		}
		if b.Kind == "ReplicaSet" && b.Name == "cart-abc123" {
			sawRS = true
		}
	}
	if !sawRS {
		t.Errorf("the diagnosed Deployment's blocked ReplicaSet should attach, got %+v", out)
	}
}

func ptrInt32(i int32) *int32 { return &i }

func TestIsReplicaSetOf(t *testing.T) {
	cases := []struct {
		rs, deploy string
		want       bool
	}{
		{"api-5d4f8b6c7", "api", true},          // real RS of "api"
		{"my-app-5d4f8b6c7", "my-app", true},    // hyphenated Deployment name
		{"api-gateway-5d4f8b6c7", "api", false}, // belongs to "api-gateway", not "api"
		{"api", "api", false},                   // no hash suffix
		{"api-", "api", false},                  // empty hash
		{"other-abc", "api", false},             // unrelated
	}
	for _, c := range cases {
		if got := isReplicaSetOf(c.rs, c.deploy); got != c.want {
			t.Errorf("isReplicaSetOf(%q, %q) = %v, want %v", c.rs, c.deploy, got, c.want)
		}
	}
}
