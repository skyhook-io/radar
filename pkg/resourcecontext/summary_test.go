package resourcecontext

import (
	"encoding/json"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestBuildSummary_NilWhenEmpty pins that BuildSummary returns nil when
// every field would be empty — keeps the per-row JSON minimal.
func TestBuildSummary_NilWhenEmpty(t *testing.T) {
	// ConfigMap has no health heuristic and no caller-supplied options.
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"}}
	if got := BuildSummary(cm, SummaryOptions{}); got != nil {
		t.Fatalf("BuildSummary(ConfigMap, {}) = %#v, want nil", got)
	}
}

// TestBuildSummary_PodGoldens golden-files BuildSummary across the
// Pod phases that drive the health heuristic. Locks the wire shape
// for the common "list pods" call.
func TestBuildSummary_PodGoldens(t *testing.T) {
	cases := []struct {
		name string
		pod  *corev1.Pod
		opts SummaryOptions
		want string
	}{
		{
			name: "running_all_ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", Ready: true},
						{Name: "sidecar", Ready: true},
					},
				},
			},
			want: `{"health":"healthy"}`,
		},
		{
			name: "running_one_not_ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", Ready: false},
					},
				},
			},
			want: `{"health":"degraded"}`,
		},
		{
			name: "failed",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			want: `{"health":"unhealthy"}`,
		},
		{
			name: "pending",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{Phase: corev1.PodPending},
			},
			want: `{"health":"degraded"}`,
		},
		{
			name: "running_with_issues_and_managedby",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "main", Ready: true},
					},
				},
			},
			opts: SummaryOptions{
				ManagedBy:  ManagedByFromOwner("ReplicaSet", "apps", "prod", "api-7d5"),
				IssueCount: 2,
			},
			want: `{"managedBy":{"kind":"ReplicaSet","source":"native","name":"api-7d5","namespace":"prod"},"health":"healthy","issueCount":2}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildSummary(c.pod, c.opts)
			if got == nil {
				t.Fatalf("got nil, want %s", c.want)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(b) != c.want {
				t.Errorf("got %s\nwant %s", b, c.want)
			}
		})
	}
}

// TestBuildSummary_DeploymentReplicasHealth covers the replica-driven
// health heuristic across the Deployment cases.
func TestBuildSummary_DeploymentReplicasHealth(t *testing.T) {
	cases := []struct {
		name      string
		ready     int32
		desired   int32
		wantSlice []byte // JSON of BuildSummary output
	}{
		{"all_ready", 3, 3, []byte(`{"health":"healthy"}`)},
		{"none_ready", 0, 3, []byte(`{"health":"unhealthy"}`)},
		{"partial", 1, 3, []byte(`{"health":"degraded"}`)},
		{"scaled_to_zero", 0, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			desired := c.desired
			dep := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Replicas: &desired, // desired is Spec.Replicas (not Status) — see deriveHealth
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas: c.ready,
					// Status.Replicas mirrors the actual non-terminated pod count
					// in real clusters; we set it equal to ready here so the
					// fixture matches a steady-state Deployment for that test.
					Replicas: c.ready,
				},
			}
			got := BuildSummary(dep, SummaryOptions{})
			if c.wantSlice == nil {
				if got != nil {
					t.Fatalf("got %#v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %s", c.wantSlice)
			}
			b, _ := json.Marshal(got)
			if string(b) != string(c.wantSlice) {
				t.Errorf("got %s\nwant %s", b, c.wantSlice)
			}
		})
	}
}

// TestBuildSummary_DeploymentHealthDuringScaleDown pins the Spec-vs-Status
// regression flagged on PR #722: during rolling updates or scale-down,
// Status.Replicas (current pod count) can exceed Spec.Replicas (desired).
// Before the fix, deriveHealth compared ReadyReplicas against Status.Replicas
// and reported "degraded" because not all current pods were ready — even
// though all DESIRED replicas were ready and the cluster was healthily
// draining excess pods. Use Spec.Replicas as the denominator instead.
func TestBuildSummary_DeploymentHealthDuringScaleDown(t *testing.T) {
	desired := int32(2)
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{Replicas: &desired},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 2, // all DESIRED replicas are ready
			Replicas:      4, // but 2 extras still terminating from a scale-down
		},
	}
	got := BuildSummary(dep, SummaryOptions{})
	if got == nil {
		t.Fatal("got nil, want ResourceSummaryContext with health=healthy")
	}
	if got.Health != "healthy" {
		t.Errorf("Health = %q, want %q (Spec.Replicas=2 ready, Status.Replicas=4 due to draining)", got.Health, "healthy")
	}
}

// TestBuildSummary_ReplicaSetHealthDuringScaleDown pins the same fix for
// ReplicaSet — the Deployment regression also applied here.
func TestBuildSummary_ReplicaSetHealthDuringScaleDown(t *testing.T) {
	desired := int32(3)
	rs := &appsv1.ReplicaSet{
		Spec: appsv1.ReplicaSetSpec{Replicas: &desired},
		Status: appsv1.ReplicaSetStatus{
			ReadyReplicas: 3,
			Replicas:      5,
		},
	}
	got := BuildSummary(rs, SummaryOptions{})
	if got == nil || got.Health != "healthy" {
		t.Errorf("ReplicaSet during scale-down: got %+v, want Health=healthy", got)
	}
}

// TestBuildSummary_NetworkPolicy verifies BuildSummary handles a kind
// without a health heuristic — it should only emit fields the caller
// supplied (e.g. issueCount, managedBy) and skip health entirely.
func TestBuildSummary_NetworkPolicy(t *testing.T) {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: "prod"},
	}
	// Empty opts → nil; the kind has no health heuristic so no field is set.
	if got := BuildSummary(np, SummaryOptions{}); got != nil {
		t.Fatalf("got %#v, want nil", got)
	}
	// IssueCount only → summary with just issueCount.
	got := BuildSummary(np, SummaryOptions{IssueCount: 3})
	if got == nil {
		t.Fatalf("got nil, want summary with issueCount")
	}
	b, _ := json.Marshal(got)
	want := `{"issueCount":3}`
	if string(b) != want {
		t.Errorf("got %s\nwant %s", b, want)
	}
}

// TestBuildSummary_UnstructuredReadyCondition covers the CRD fallback
// — Ready/Available conditions are translated to the health vocabulary.
func TestBuildSummary_UnstructuredReadyCondition(t *testing.T) {
	cases := []struct {
		name       string
		conditions []any
		want       string
	}{
		{
			name: "ready_true",
			conditions: []any{
				map[string]any{"type": "Ready", "status": "True"},
			},
			want: `{"health":"healthy"}`,
		},
		{
			name: "ready_false",
			conditions: []any{
				map[string]any{"type": "Ready", "status": "False"},
			},
			want: `{"health":"unhealthy"}`,
		},
		{
			name: "ready_unknown",
			conditions: []any{
				map[string]any{"type": "Ready", "status": "Unknown"},
			},
			want: `{"health":"degraded"}`,
		},
		{
			name: "available_true",
			conditions: []any{
				map[string]any{"type": "Available", "status": "True"},
			},
			want: `{"health":"healthy"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "example.io/v1",
				"kind":       "Widget",
				"status":     map[string]any{"conditions": c.conditions},
			}}
			got := BuildSummary(u, SummaryOptions{})
			if got == nil {
				t.Fatalf("got nil, want %s", c.want)
			}
			b, _ := json.Marshal(got)
			if string(b) != c.want {
				t.Errorf("got %s\nwant %s", b, c.want)
			}
		})
	}
}

// TestBuildSummary_HealthOverride pins that caller-supplied Health
// short-circuits the per-kind heuristic.
func TestBuildSummary_HealthOverride(t *testing.T) {
	dep := &appsv1.Deployment{
		Status: appsv1.DeploymentStatus{ReadyReplicas: 3, Replicas: 3},
	}
	got := BuildSummary(dep, SummaryOptions{Health: "degraded"})
	if got == nil || got.Health != "degraded" {
		t.Fatalf("Health override ignored: %#v", got)
	}
}

// TestManagedByFromOwner pins source classification for each cluster
// of owner kinds we care about.
func TestManagedByFromOwner(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		group     string
		namespace string
		ownerName string
		want      *ManagedByRef
	}{
		{
			name:      "empty_kind",
			kind:      "",
			ownerName: "x",
			want:      nil,
		},
		{
			name:      "empty_name",
			kind:      "Deployment",
			ownerName: "",
			want:      nil,
		},
		{
			name:      "deployment",
			kind:      "Deployment",
			group:     "apps",
			namespace: "prod",
			ownerName: "api",
			want:      &ManagedByRef{Kind: "Deployment", Source: "native", Name: "api", Namespace: "prod"},
		},
		{
			name:      "argocd_application",
			kind:      "Application",
			group:     "argoproj.io",
			namespace: "argocd",
			ownerName: "storefront",
			want:      &ManagedByRef{Kind: "Application", Source: "argocd", Name: "storefront", Namespace: "argocd"},
		},
		{
			name:      "flux_kustomization",
			kind:      "Kustomization",
			group:     "kustomize.toolkit.fluxcd.io",
			namespace: "flux-system",
			ownerName: "prod-apps",
			want:      &ManagedByRef{Kind: "Kustomization", Source: "flux", Name: "prod-apps", Namespace: "flux-system"},
		},
		{
			name:      "flux_helmrelease",
			kind:      "HelmRelease",
			group:     "helm.toolkit.fluxcd.io",
			namespace: "flux-system",
			ownerName: "prod-apps",
			want:      &ManagedByRef{Kind: "HelmRelease", Source: "flux", Name: "prod-apps", Namespace: "flux-system"},
		},
		{
			name:      "flux_gitrepository",
			kind:      "GitRepository",
			group:     "source.toolkit.fluxcd.io",
			namespace: "flux-system",
			ownerName: "repo",
			want:      &ManagedByRef{Kind: "GitRepository", Source: "flux", Name: "repo", Namespace: "flux-system"},
		},
		{
			// Native Helm release: topology's detectManagedByFromMeta emits
			// {Kind:"HelmRelease", Group:""} when it sees Helm's release-name
			// annotation (no Flux/GitOps signal). Must classify as "helm",
			// not "native" — distinguishes Helm-managed resources in the
			// list/search UI from raw kubectl-applied ones. The Flux
			// HelmRelease CR lives at helm.toolkit.fluxcd.io and is covered
			// by the case above; the empty-group form is unambiguous.
			name:      "native_helm_release",
			kind:      "HelmRelease",
			group:     "",
			namespace: "cert-manager",
			ownerName: "cert-manager",
			want:      &ManagedByRef{Kind: "HelmRelease", Source: "helm", Name: "cert-manager", Namespace: "cert-manager"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ManagedByFromOwner(c.kind, c.group, c.namespace, c.ownerName)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ManagedByFromOwner(%q, %q, %q, %q) = %#v, want %#v",
					c.kind, c.group, c.namespace, c.ownerName, got, c.want)
			}
		})
	}
}

// TestBuildSummary_NilObject defends against the typed-nil-in-interface
// trap: handlers occasionally pass interface-wrapped nils.
func TestBuildSummary_NilObject(t *testing.T) {
	var obj runtime.Object
	if got := BuildSummary(obj, SummaryOptions{}); got != nil {
		t.Fatalf("BuildSummary(nil) = %#v, want nil", got)
	}
	// IssueCount alone still produces output (no panic via nil obj).
	got := BuildSummary(obj, SummaryOptions{IssueCount: 1})
	if got == nil || got.IssueCount != 1 {
		t.Fatalf("BuildSummary(nil, IssueCount=1) = %#v, want issueCount=1", got)
	}
}
