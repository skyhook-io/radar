package mcp

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	startedAt := time.Now().UTC().Add(-2 * time.Minute)
	secretChangedAt := startedAt.Add(time.Minute)

	fakeClient := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: selector},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: selector},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{
						Name: "cart",
						Env: []corev1.EnvVar{{
							Name: "CART_MODE",
							ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "cart-config"},
								Key:                  "mode",
							}},
						}},
					}}},
				},
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cart-config", Namespace: ns},
			Data:       map[string]string{"mode": "production"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cart-abc123",
				Namespace: ns,
				Labels:    selector,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "cart",
					Env: []corev1.EnvVar{{
						Name: "DB_PASSWORD",
						ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "cart-db"},
							Key:                  "password",
						}},
					}},
				}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "cart",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{
						StartedAt: metav1.NewTime(startedAt),
					}},
				}},
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cart-db",
				Namespace: ns,
				ManagedFields: []metav1.ManagedFieldsEntry{{
					Manager:    "secret-controller",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					Time:       &metav1.Time{Time: secretChangedAt},
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}}}`)},
				}},
			},
			Data: map[string][]byte{"password": []byte("diagnose-secret-sentinel")},
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
		{"rollout", "rollouts"},
		{"Rollouts", "rollouts"},
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

func TestGitopsDiagnoseTarget(t *testing.T) {
	cases := []struct {
		in                                          string
		wantKind, wantGroup, wantResource, wantTool string
		wantOK                                      bool
	}{
		{"application", "Application", "argoproj.io", "applications", "argocd", true},
		{"applications", "Application", "argoproj.io", "applications", "argocd", true},
		{"app", "Application", "argoproj.io", "applications", "argocd", true},
		{"kustomization", "Kustomization", "kustomize.toolkit.fluxcd.io", "kustomizations", "flux", true},
		{"helmrelease", "HelmRelease", "helm.toolkit.fluxcd.io", "helmreleases", "flux", true},
		{"HR", "HelmRelease", "helm.toolkit.fluxcd.io", "helmreleases", "flux", true},
		{"pod", "", "", "", "", false},
		{"deployment", "", "", "", "", false},
		{"", "", "", "", "", false},
	}
	for _, c := range cases {
		k, g, resource, tool, ok := gitopsDiagnoseTarget(c.in)
		if k != c.wantKind || g != c.wantGroup || resource != c.wantResource || tool != c.wantTool || ok != c.wantOK {
			t.Errorf("gitopsDiagnoseTarget(%q) = (%q,%q,%q,%q,%v), want (%q,%q,%q,%q,%v)",
				c.in, k, g, resource, tool, ok, c.wantKind, c.wantGroup, c.wantResource, c.wantTool, c.wantOK)
		}
	}
}

func testDiagnoseInput(kind, namespace, name string) diagnoseInput {
	return diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{
		Kind: kind, Namespace: namespace, Name: name,
	}}
}

// TestHandleDiagnose_GitOpsKindDispatch confirms a GitOps kind routes to the
// no-pods GitOps path (not the workload "invalid kind" error). With no Argo CRD
// in the fake cache the fetch fails, but the error must come from the GitOps
// branch — proving the dispatch fork before pod resolution.
func TestHandleDiagnose_GitOpsKindDispatch(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")
	// The GitOps read is gated on a per-kind get SAR; grant it so the test
	// exercises the dispatch fork (not the RBAC gate, covered separately).
	getPermCache().Get("admin", nil).SetCanI("get", "argoproj.io", "applications", "alpha", true)

	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("application", "alpha", "whatever"))
	if err == nil {
		t.Fatalf("expected an error (no Application in fake cache), got nil")
	}
	if strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("GitOps kind must route to the GitOps path, not the workload invalid-kind error; got %v", err)
	}
}

// TestHandleGitOpsDiagnose_PerKindRBAC pins that the GitOps read is gated on a
// per-kind get SAR, not just namespace access — the object is served from the
// shared (connector-identity) cache, so a user who can reach the namespace but
// lacks get on applications.argoproj.io must not receive it.
func TestHandleGitOpsDiagnose_PerKindRBAC(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	// Namespace access to argocd, but no get on applications.argoproj.io.
	ctx := withRestrictedUser(t, "limited", []string{"argocd"})

	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("application", "argocd", "guestbook"))
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("expected forbidden without get on applications.argoproj.io, got %v", err)
	}

	// Granting the per-kind get lets the read through (then fails for not-found,
	// not forbidden) — proving the gate is the only thing blocking it.
	getPermCache().Get("limited", nil).SetCanI("get", "argoproj.io", "applications", "argocd", true)
	if _, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("application", "argocd", "guestbook")); err == nil || strings.Contains(err.Error(), "forbidden") {
		t.Errorf("with get granted, expected a non-forbidden (not-found) error, got %v", err)
	}
}

// TestHandleGitOpsDiagnose_NamespaceGate pins that the GitOps path honors
// Radar's namespace allow-list like the workload path: a namespace outside the
// user's scope is forbidden even when cluster RBAC would permit the get.
func TestHandleGitOpsDiagnose_NamespaceGate(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	// User scoped to team-a; cluster RBAC grants get on applications in argocd.
	ctx := withRestrictedUser(t, "scoped", []string{"team-a"})
	getPermCache().Get("scoped", nil).SetCanI("get", "argoproj.io", "applications", "argocd", true)

	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("application", "argocd", "guestbook"))
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("namespace outside the allow-list must be forbidden, got %v", err)
	}
}

func TestHandleDiagnose_InvalidKind(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")

	// configmap is not a workload, not a GitOps reconciler, and not a
	// network entry kind - diagnose should reject it.
	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("configmap", "alpha", "alpha-cm"))
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

	if _, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("pod", "", "alpha-pod")); err == nil {
		t.Errorf("expected error for empty namespace, got nil")
	}
	if _, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("pod", "alpha", "")); err == nil {
		t.Errorf("expected error for empty name, got nil")
	}
}

func TestHandleDiagnose_ForbiddenNamespace(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	// User restricted to alpha; diagnose request targets beta.
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})

	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("pod", "beta", "beta-pod"))
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

	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("pod", "alpha", "alpha-pod"))
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

func TestHandleDiagnose_AttachesPodDNSSignalButRBACGatesCoreDNSFinding(t *testing.T) {
	defer k8s.ResetTestState()
	fakeClient := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "frontend", Namespace: "alpha"},
			Spec: corev1.PodSpec{
				DNSPolicy: corev1.DNSNone,
				DNSConfig: &corev1.PodDNSConfig{
					Nameservers: []string{"8.8.8.8"},
				},
				Containers: []corev1.Container{{Name: "frontend"}},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
			Data: map[string]string{
				"Corefile": ".:53 {\n  template ANY svc.cluster.local {\n    rcode NXDOMAIN\n  }\n}\n",
			},
		},
	)
	if err := k8s.InitTestResourceCache(fakeClient); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	ctx := withClusterAdmin(t, "admin")

	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("pod", "alpha", "frontend"))
	if err != nil {
		t.Fatalf("handleDiagnose: %v", err)
	}
	body := extractText(t, result)
	if !strings.Contains(body, `"dnsContext"`) || !strings.Contains(body, "dnsPolicy=None") {
		t.Fatalf("expected dnsContext with pod DNS signal: %s", body)
	}
	if strings.Contains(body, "CoreDNS NXDOMAIN override") {
		t.Fatalf("expected CoreDNS finding to be RBAC-gated from dnsContext: %s", body)
	}
}

func TestHandleDiagnose_PodNotFound(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "admin")

	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("pod", "alpha", "ghost-pod"))
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

	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "alpha", "cart"))
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
	if !strings.Contains(body, `"evidenceSource":"managed_fields_mtime"`) || !strings.Contains(body, `"name":"cart-db"`) {
		t.Errorf("expected workload diagnosis to aggregate the Pod's stale Secret env FYI: %s", body)
	}
	if strings.Contains(body, "diagnose-secret-sentinel") || strings.Contains(body, `"key":"password"`) {
		t.Errorf("managedFields fallback leaked Secret data or claimed a specific stale key: %s", body)
	}
}

func TestHandleDiagnose_DeploymentGroupIsCanonicalAndExact(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	ctx := withClusterAdmin(t, "admin")

	mixedCase := testDiagnoseInput("deployment", "alpha", "cart")
	mixedCase.Group = "ApPs"
	result, _, err := handleDiagnose(ctx, nil, mixedCase)
	if err != nil {
		t.Fatalf("mixed-case canonical apps group: %v", err)
	}
	if body := extractText(t, result); !strings.Contains(body, `"name":"cart"`) {
		t.Fatalf("mixed-case apps group did not resolve the Deployment: %s", body)
	}

	// A same-named built-in exists in the typed cache. Supplying a different
	// group must reject the request before any group-blind typed lookup can
	// accidentally return that Deployment.
	wrongGroup := testDiagnoseInput("deployment", "alpha", "cart")
	wrongGroup.Group = "workloads.example.io"
	if _, _, err := handleDiagnose(ctx, nil, wrongGroup); err == nil || !strings.Contains(err.Error(), `expected "apps"`) {
		t.Fatalf("wrong Deployment group = %v, want an exact-group rejection", err)
	}
}

func TestHandleDiagnose_RolloutGroupDefaultsAndCanonicalizes(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata": map[string]any{
			"name": "cart", "namespace": "alpha",
		},
		"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]any{"app": "cart"}},
		},
	}}
	setupMCPDynamicResource(t, rolloutGVR, "RolloutList", k8s.APIResource{
		Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout",
		Name: "rollouts", Namespaced: true, Verbs: []string{"get", "list", "watch"},
	}, rollout)
	ctx := withClusterAdmin(t, "admin")

	for _, tc := range []struct {
		name  string
		group string
	}{
		{name: "omitted group defaults to argoproj.io"},
		{name: "explicit mixed-case group canonicalizes", group: "ArGoPrOj.Io"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := testDiagnoseInput("rollout", "alpha", "cart")
			input.Group = tc.group
			result, _, err := handleDiagnose(ctx, nil, input)
			if err != nil {
				t.Fatalf("handleDiagnose: %v", err)
			}
			body := extractText(t, result)
			if !strings.Contains(body, `"apiVersion":"argoproj.io/v1alpha1"`) || !strings.Contains(body, `"kind":"Rollout"`) {
				t.Fatalf("diagnose resolved something other than the exact Rollout target: %s", body)
			}
		})
	}

	wrongGroup := testDiagnoseInput("rollout", "alpha", "cart")
	wrongGroup.Group = "apps"
	if _, _, err := handleDiagnose(ctx, nil, wrongGroup); err == nil || !strings.Contains(err.Error(), `expected "argoproj.io"`) {
		t.Fatalf("wrong Rollout group = %v, want an exact-group rejection", err)
	}
}

func TestHandleDiagnose_DeploymentNotFound(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	ctx := withClusterAdmin(t, "admin")

	_, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "alpha", "ghost"))
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
	out := startupBlockersForWorkload(k8s.GetResourceCache(), "deployments", "apps", "alpha", "cart", nil)

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

func TestStartupBlockersForWorkload_AttributesReplicaSetToArgoRollout(t *testing.T) {
	defer k8s.ResetTestState()
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-abc123", Namespace: "alpha"},
		Spec:       appsv1.ReplicaSetSpec{Replicas: ptrInt32(2)},
		Status:     appsv1.ReplicaSetStatus{Replicas: 0},
	}
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "rollout-replicaset-denial", Namespace: "alpha"},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: "alpha", Name: "checkout-abc123",
		},
		Reason: "FailedCreate", Type: corev1.EventTypeWarning,
		Message:       `pods "checkout" is forbidden: exceeded quota: rollout-quota`,
		LastTimestamp: metav1.Now(),
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(replicaSet, event)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	argo := startupBlockersForWorkload(k8s.GetResourceCache(), "rollouts", "argoproj.io", "alpha", "checkout", nil)
	if len(argo) != 1 || argo[0].Kind != "ReplicaSet" || argo[0].Name != "checkout-abc123" {
		t.Fatalf("Argo Rollout blockers = %+v, want its blocked apps/v1 ReplicaSet", argo)
	}

	wrongGroup := startupBlockersForWorkload(k8s.GetResourceCache(), "rollouts", "delivery.example.io", "alpha", "checkout", nil)
	if len(wrongGroup) != 0 {
		t.Fatalf("non-Argo Rollout blockers = %+v, want no cross-group ReplicaSet attribution", wrongGroup)
	}
}

func TestStartupBlockersForWorkload_RequiresExactAPIGroup(t *testing.T) {
	defer k8s.ResetTestState()
	replicas := int32(1)
	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "alpha"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	events := []runtime.Object{
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "apps-denial", Namespace: "alpha"},
			InvolvedObject: corev1.ObjectReference{
				APIVersion: "apps/v1", Kind: "Deployment", Namespace: "alpha", Name: "cart",
			},
			Reason: "FailedCreate", Type: corev1.EventTypeWarning,
			Message:       `pods "apps" is forbidden: exceeded quota: apps-quota`,
			LastTimestamp: metav1.Now(),
		},
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "custom-denial", Namespace: "alpha"},
			InvolvedObject: corev1.ObjectReference{
				APIVersion: "workloads.example.io/v1", Kind: "Deployment", Namespace: "alpha", Name: "cart",
			},
			Reason: "FailedCreate", Type: corev1.EventTypeWarning,
			Message:       `pods "custom" is forbidden: exceeded quota: custom-quota`,
			LastTimestamp: metav1.Now(),
		},
	}
	objects := []runtime.Object{deployment}
	objects = append(objects, events...)
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	apps := startupBlockersForWorkload(k8s.GetResourceCache(), "deployments", "apps", "alpha", "cart", nil)
	if len(apps) != 1 || !strings.Contains(apps[0].Message, "apps-quota") {
		t.Fatalf("apps Deployment blockers = %+v, want only apps-group evidence", apps)
	}
	custom := startupBlockersForWorkload(k8s.GetResourceCache(), "deployments", "workloads.example.io", "alpha", "cart", nil)
	if len(custom) != 1 || !strings.Contains(custom[0].Message, "custom-quota") {
		t.Fatalf("custom Deployment blockers = %+v, want only custom-group evidence", custom)
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

func TestIssueNamespacesForResourcePreservesClusterScope(t *testing.T) {
	if got := issueNamespacesForResource(""); got != nil {
		t.Fatalf("cluster-scoped namespaces = %#v, want nil", got)
	}
	got := issueNamespacesForResource("team-a")
	if len(got) != 1 || got[0] != "team-a" {
		t.Fatalf("namespaced namespaces = %#v, want [team-a]", got)
	}
}
