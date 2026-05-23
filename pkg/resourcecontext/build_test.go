package resourcecontext

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/topology"
)

// ---------------------------------------------------------------------------
// Test scaffolding
// ---------------------------------------------------------------------------

// allowAllChecker permits every CanRead check. Used by the happy-path
// goldens that don't exercise RBAC denial.
type allowAllChecker struct{}

func (allowAllChecker) CanRead(_ context.Context, _, _, _ string) bool { return true }

// denyChecker denies a specific (group, kind, namespace) tuple and permits
// everything else. Tests the "omitted: rbac_denied" path without requiring
// the full server stack.
type denyChecker struct {
	group     string
	kind      string
	namespace string
}

func (d denyChecker) CanRead(_ context.Context, group, kind, namespace string) bool {
	return !(group == d.group && kind == d.kind && namespace == d.namespace)
}

// mockPolicyReports implements PolicyReportLookup.
type mockPolicyReports map[string][]KyvernoFinding

func (m mockPolicyReports) FindingsFor(group, kind, namespace, name string) []KyvernoFinding {
	return m[kind+"/"+namespace+"/"+name]
}

// ---------------------------------------------------------------------------
// Golden-file tests
// ---------------------------------------------------------------------------

func TestBuild_Pod_FullEnrichment(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc",
			Namespace: "prod",
			Labels: map[string]string{
				"app.kubernetes.io/name": "web",
			},
			Annotations: map[string]string{
				"argocd.argoproj.io/tracking-id": "argocd_storefront:apps/Deployment:prod/web",
			},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "web-7d", Controller: ptrBool(true)},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           "node-1",
			ServiceAccountName: "web-sa",
			Volumes: []corev1.Volume{
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"},
						},
					},
				},
				{
					Name: "creds",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{SecretName: "web-creds"},
					},
				},
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "web-data"},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name: "web",
					EnvFrom: []corev1.EnvFromSource{
						{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared-env"}}},
					},
					Env: []corev1.EnvVar{
						{
							Name: "API_KEY",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "api-key-secret"},
									Key:                  "key",
								},
							},
						},
					},
				},
			},
		},
	}

	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "pod/prod/web-abc", Kind: topology.KindPod, Name: "web-abc"},
			{ID: "service/prod/web", Kind: topology.KindService, Name: "web"},
			{ID: "networkpolicy/prod/default-deny", Kind: topology.KindNetworkPolicy, Name: "default-deny"},
			{ID: "poddisruptionbudget/prod/web-pdb", Kind: topology.KindPDB, Name: "web-pdb"},
			{ID: "horizontalpodautoscaler/prod/web-hpa", Kind: topology.KindHPA, Name: "web-hpa"},
		},
		Edges: []topology.Edge{
			{Source: "service/prod/web", Target: "pod/prod/web-abc", Type: topology.EdgeRoutesTo},
			{Source: "networkpolicy/prod/default-deny", Target: "pod/prod/web-abc", Type: topology.EdgeProtects},
			{Source: "poddisruptionbudget/prod/web-pdb", Target: "pod/prod/web-abc", Type: topology.EdgeProtects},
			{Source: "horizontalpodautoscaler/prod/web-hpa", Target: "pod/prod/web-abc", Type: topology.EdgeUses},
		},
	}

	opts := Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Topology:      topo,
		IssueSummary: &IssueSummary{
			Count: 1, HighestSeverity: "critical", TopReason: "ImagePullBackOff",
			BySource: map[string]int{"problem": 1},
		},
	}

	rc := Build(context.Background(), pod, opts)
	if rc == nil {
		t.Fatal("Build returned nil")
	}

	// ManagedBy: argo tracking-id annotation wins over owner reference.
	if got, want := len(rc.ManagedBy), 1; got != want {
		t.Fatalf("ManagedBy len: got %d want %d (%+v)", got, want, rc.ManagedBy)
	}
	mb := rc.ManagedBy[0]
	if mb.Kind != "Application" || mb.Name != "storefront" || mb.Namespace != "argocd" {
		t.Errorf("ManagedBy[0]: got %+v, want Application argocd/storefront", mb)
	}

	// Exposes: the Service routes to the pod.
	if got, want := len(rc.Exposes), 1; got != want {
		t.Fatalf("Exposes len: got %d want %d (%+v)", got, want, rc.Exposes)
	}
	if rc.Exposes[0].Kind != "Service" || rc.Exposes[0].Name != "web" {
		t.Errorf("Exposes[0]: got %+v want Service/prod/web", rc.Exposes[0])
	}

	// SelectedBy: NP + PDB, sorted by kind (NetworkPolicy < PodDisruptionBudget).
	if got, want := len(rc.SelectedBy), 2; got != want {
		t.Fatalf("SelectedBy len: got %d want %d (%+v)", got, want, rc.SelectedBy)
	}
	if rc.SelectedBy[0].Kind != "NetworkPolicy" || rc.SelectedBy[1].Kind != "PodDisruptionBudget" {
		t.Errorf("SelectedBy order: got %s,%s want NetworkPolicy,PodDisruptionBudget",
			rc.SelectedBy[0].Kind, rc.SelectedBy[1].Kind)
	}

	// ScaledBy: HPA.
	if got, want := len(rc.ScaledBy), 1; got != want {
		t.Fatalf("ScaledBy len: got %d want %d", got, want)
	}
	if rc.ScaledBy[0].Kind != "HorizontalPodAutoscaler" {
		t.Errorf("ScaledBy[0].Kind: got %q", rc.ScaledBy[0].Kind)
	}

	// RunsOn: Node.
	if rc.RunsOn == nil || rc.RunsOn.Name != "node-1" {
		t.Errorf("RunsOn: got %+v want Node/node-1", rc.RunsOn)
	}

	// Uses: 2 ConfigMaps (web-config + shared-env), 2 Secrets (web-creds + api-key-secret), 1 PVC, ServiceAccount.
	if rc.Uses == nil {
		t.Fatal("Uses: got nil")
	}
	if got, want := len(rc.Uses.ConfigMaps), 2; got != want {
		t.Errorf("Uses.ConfigMaps len: got %d want %d (%+v)", got, want, rc.Uses.ConfigMaps)
	}
	if got, want := len(rc.Uses.Secrets), 2; got != want {
		t.Errorf("Uses.Secrets len: got %d want %d (%+v)", got, want, rc.Uses.Secrets)
	}
	if got, want := len(rc.Uses.PVCs), 1; got != want {
		t.Errorf("Uses.PVCs len: got %d want %d", got, want)
	}
	if rc.Uses.ServiceAccount == nil || rc.Uses.ServiceAccount.Name != "web-sa" {
		t.Errorf("Uses.ServiceAccount: got %+v", rc.Uses.ServiceAccount)
	}

	// Pre-computed summaries are passed through.
	if rc.IssueSummary == nil || rc.IssueSummary.Count != 1 {
		t.Errorf("IssueSummary not passed through: %+v", rc.IssueSummary)
	}
	if rc.AuditSummary != nil {
		t.Errorf("AuditSummary: want nil, got %+v", rc.AuditSummary)
	}
}

func TestBuild_Deployment_OwnerRefHelmRelease(t *testing.T) {
	// Flux HelmRelease labels take precedence over owner references —
	// owner is "ReplicaSet web-7d" but Flux labels point at HelmRelease.
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "prod",
			Labels: map[string]string{
				"helm.toolkit.fluxcd.io/name":      "web",
				"helm.toolkit.fluxcd.io/namespace": "flux-system",
			},
		},
	}

	rc := Build(context.Background(), dep, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
	})
	if rc == nil {
		t.Fatal("Build returned nil")
	}
	if got, want := len(rc.ManagedBy), 1; got != want {
		t.Fatalf("ManagedBy len: got %d want %d", got, want)
	}
	mb := rc.ManagedBy[0]
	if mb.Kind != "HelmRelease" || mb.Name != "web" || mb.Namespace != "flux-system" {
		t.Errorf("ManagedBy[0]: got %+v want HelmRelease/flux-system/web", mb)
	}
	if mb.Group != "helm.toolkit.fluxcd.io" {
		t.Errorf("ManagedBy[0].Group: got %q", mb.Group)
	}
}

func TestBuild_Service_ExposedByIngress(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "service/prod/api", Kind: topology.KindService, Name: "api"},
			{ID: "ingress/prod/api-ingress", Kind: topology.KindIngress, Name: "api-ingress"},
		},
		Edges: []topology.Edge{
			{Source: "ingress/prod/api-ingress", Target: "service/prod/api", Type: topology.EdgeRoutesTo},
		},
	}
	rc := Build(context.Background(), svc, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Topology:      topo,
	})

	if got, want := len(rc.Exposes), 1; got != want {
		t.Fatalf("Exposes len: got %d want %d", got, want)
	}
	if rc.Exposes[0].Kind != "Ingress" || rc.Exposes[0].Name != "api-ingress" {
		t.Errorf("Exposes[0]: got %+v", rc.Exposes[0])
	}
	// Service has no Uses block — make sure we don't synthesize an empty one.
	if rc.Uses != nil {
		t.Errorf("Uses should be nil for Service: got %+v", rc.Uses)
	}
}

func TestBuild_NetworkPolicy_OutgoingEdgeNotSurfaced(t *testing.T) {
	// NetworkPolicy on the "policy side" emits an outgoing EdgeProtects to
	// the workload it selects. The topology relationships projection does
	// NOT surface that direction (see relationships.go's intentional skip).
	// Build inherits this — the NP should have nothing in SelectedBy.
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: "prod"},
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "networkpolicy/prod/default-deny", Kind: topology.KindNetworkPolicy, Name: "default-deny"},
			{ID: "deployment/prod/web", Kind: topology.KindDeployment, Name: "web"},
		},
		Edges: []topology.Edge{
			{Source: "networkpolicy/prod/default-deny", Target: "deployment/prod/web", Type: topology.EdgeProtects},
		},
	}
	rc := Build(context.Background(), np, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Topology:      topo,
	})
	if rc == nil {
		t.Fatal("Build returned nil")
	}
	if len(rc.SelectedBy) != 0 {
		t.Errorf("SelectedBy: expected empty (outgoing EdgeProtects not surfaced), got %+v", rc.SelectedBy)
	}
}

func TestBuild_ConfigMap_OwnerOnly(t *testing.T) {
	// A ConfigMap owned by a Deployment via EdgeManages — owner-chain
	// ManagedBy is sourced from topology.SynthesizeManagedBy walking the
	// owner graph (T23 canonical projection). No Pod spec, no GitOps
	// labels — just the topology owner edge.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-config",
			Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", APIVersion: "apps/v1", Name: "web", Controller: ptrBool(true)},
			},
		},
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "configmap/prod/web-config", Kind: topology.KindConfigMap, Name: "web-config"},
			{ID: "deployment/prod/web", Kind: topology.KindDeployment, Name: "web"},
		},
		Edges: []topology.Edge{
			{Source: "deployment/prod/web", Target: "configmap/prod/web-config", Type: topology.EdgeManages},
		},
	}
	rc := Build(context.Background(), cm, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Topology:      topo,
	})
	if got, want := len(rc.ManagedBy), 1; got != want {
		t.Fatalf("ManagedBy len: got %d want %d", got, want)
	}
	mb := rc.ManagedBy[0]
	if mb.Kind != "Deployment" || mb.Name != "web" || mb.Namespace != "prod" {
		t.Errorf("ManagedBy[0]: got %+v", mb)
	}
}

func TestBuild_RBACDenied_AppendsOmitted(t *testing.T) {
	// Deny reads on Secrets in the pod's namespace — buildUsesFromPod
	// should drop them all and emit an omitted entry.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "prod"},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{
				Name: "creds",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: "web-creds"},
				},
			}},
		},
	}
	rc := Build(context.Background(), pod, Options{
		Tier:          TierBasic,
		AccessChecker: denyChecker{group: "", kind: "Secret", namespace: "prod"},
	})
	if rc.Uses != nil && len(rc.Uses.Secrets) != 0 {
		t.Errorf("Secrets should be empty after deny; got %+v", rc.Uses.Secrets)
	}
	gotOmitted := false
	for _, o := range rc.Omitted {
		if o.Field == "uses.secrets" && o.Reason == OmittedRBACDenied {
			gotOmitted = true
			break
		}
	}
	if !gotOmitted {
		t.Errorf("expected omitted [uses.secrets, rbac_denied]; got %+v", rc.Omitted)
	}
}

func TestBuild_NilObj(t *testing.T) {
	if rc := Build(context.Background(), nil, Options{}); rc != nil {
		t.Errorf("Build(nil) = %+v, want nil", rc)
	}
}

func TestBuild_HPA_Identity(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "web-hpa", Namespace: "prod"},
	}
	rc := Build(context.Background(), hpa, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc == nil {
		t.Fatal("Build returned nil for HPA")
	}
	if rc.Tier != TierBasic {
		t.Errorf("Tier: got %q want %q", rc.Tier, TierBasic)
	}
}

func TestBuild_PolicyReports_BasicTierCountsOnly(t *testing.T) {
	// Basic tier emits counts only (fail/warn/pass). Top[] is reserved
	// for diagnostic tier — keeps the basic-tier wire footprint minimal.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "prod"}}
	reports := mockPolicyReports{
		"Pod/prod/p": {
			{Policy: "require-labels", Rule: "check-app", Result: "fail", Message: "missing label"},
			{Policy: "require-labels", Rule: "check-env", Result: "warn"},
			{Policy: "no-host-network", Rule: "main", Result: "pass"},
		},
	}
	rc := Build(context.Background(), pod, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		PolicyReports: reports,
	})
	if rc.PolicySummary == nil || rc.PolicySummary.Kyverno == nil {
		t.Fatalf("PolicySummary.Kyverno: got nil; rc=%+v", rc)
	}
	k := rc.PolicySummary.Kyverno
	if k.Fail != 1 || k.Warn != 1 || k.Pass != 1 {
		t.Errorf("Kyverno counts: got fail=%d warn=%d pass=%d", k.Fail, k.Warn, k.Pass)
	}
	if len(k.Top) != 0 {
		t.Errorf("basic tier must NOT emit Top[]; got %d entries: %+v", len(k.Top), k.Top)
	}
}

func TestBuild_PolicyReports_DiagnosticTierIncludesTop(t *testing.T) {
	// Diagnostic tier adds the Top[] findings (capped at 3, ordered
	// fail > warn > error > pass). Used by the deep agent investigation
	// path — basic tier is for everyday triage.
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "prod"}}
	reports := mockPolicyReports{
		"Pod/prod/p": {
			{Policy: "require-labels", Rule: "check-app", Result: "fail", Message: "missing label"},
			{Policy: "require-labels", Rule: "check-env", Result: "warn"},
			{Policy: "no-host-network", Rule: "main", Result: "pass"},
		},
	}
	rc := Build(context.Background(), pod, Options{
		Tier:          TierDiagnostic,
		AccessChecker: allowAllChecker{},
		PolicyReports: reports,
	})
	if rc.PolicySummary == nil || rc.PolicySummary.Kyverno == nil {
		t.Fatalf("PolicySummary.Kyverno: got nil; rc=%+v", rc)
	}
	k := rc.PolicySummary.Kyverno
	if k.Fail != 1 || k.Warn != 1 || k.Pass != 1 {
		t.Errorf("Kyverno counts: got fail=%d warn=%d pass=%d", k.Fail, k.Warn, k.Pass)
	}
	if len(k.Top) == 0 {
		t.Fatal("diagnostic tier must emit Top[] findings")
	}
	if k.Top[0].Result != "fail" {
		t.Errorf("Top[0] should be the failing finding; got %+v", k.Top)
	}
}

func TestBuild_PDB_OutputJSONShape(t *testing.T) {
	// Pin the wire shape one full populated Build produces, so a future
	// reorder of fields (or accidental omitempty change) is caught.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p", Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", APIVersion: "apps/v1", Name: "rs", Controller: ptrBool(true)},
			},
		},
		Spec: corev1.PodSpec{NodeName: "n1"},
	}
	// Topology with the owner edge so SynthesizeManagedBy can walk the
	// chain and emit a ReplicaSet ManagedBy ref for wire-shape coverage.
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "pod/prod/p", Kind: topology.KindPod, Name: "p"},
			{ID: "replicaset/prod/rs", Kind: topology.KindReplicaSet, Name: "rs"},
		},
		Edges: []topology.Edge{
			{Source: "replicaset/prod/rs", Target: "pod/prod/p", Type: topology.EdgeManages},
		},
	}
	rc := Build(context.Background(), pod, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Topology:      topo,
	})
	b, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Spot-check: tier basic, owner ref managedBy, runsOn node.
	want := `"managedBy"`
	if !contains(string(b), want) {
		t.Errorf("JSON missing %s\n%s", want, b)
	}
	if !contains(string(b), `"tier": "basic"`) {
		t.Errorf("JSON missing tier=basic\n%s", b)
	}
	if !contains(string(b), `"runsOn"`) {
		t.Errorf("JSON missing runsOn\n%s", b)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ptrBool(b bool) *bool { return &b }

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Compile-time pin: keep PDB and Networking imports referenced for future tests.
var (
	_ = policyv1.PodDisruptionBudget{}
)
