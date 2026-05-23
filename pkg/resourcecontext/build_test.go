package resourcecontext

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

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

type mockServiceBackends []*corev1.Pod

func (m mockServiceBackends) PodsForServiceSelector(namespace string, selector labels.Selector) ([]*corev1.Pod, error) {
	out := make([]*corev1.Pod, 0, len(m))
	for _, pod := range m {
		if pod.Namespace == namespace && selector.Matches(labels.Set(pod.Labels)) {
			out = append(out, pod)
		}
	}
	return out, nil
}

type mockResourceProvider struct {
	pods         []*corev1.Pod
	deploys      []*appsv1.Deployment
	daemonSets   []*appsv1.DaemonSet
	statefulSets []*appsv1.StatefulSet
	jobs         []*batchv1.Job
	cronJobs     []*batchv1.CronJob
}

func (m mockResourceProvider) Pods() ([]*corev1.Pod, error)               { return m.pods, nil }
func (m mockResourceProvider) Services() ([]*corev1.Service, error)       { return nil, nil }
func (m mockResourceProvider) Deployments() ([]*appsv1.Deployment, error) { return m.deploys, nil }
func (m mockResourceProvider) DaemonSets() ([]*appsv1.DaemonSet, error)   { return m.daemonSets, nil }
func (m mockResourceProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	return m.statefulSets, nil
}
func (m mockResourceProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error)  { return nil, nil }
func (m mockResourceProvider) Jobs() ([]*batchv1.Job, error)               { return m.jobs, nil }
func (m mockResourceProvider) CronJobs() ([]*batchv1.CronJob, error)       { return m.cronJobs, nil }
func (m mockResourceProvider) Ingresses() ([]*networkingv1.Ingress, error) { return nil, nil }
func (m mockResourceProvider) ConfigMaps() ([]*corev1.ConfigMap, error)    { return nil, nil }
func (m mockResourceProvider) Secrets() ([]*corev1.Secret, error)          { return nil, nil }
func (m mockResourceProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return nil, nil
}
func (m mockResourceProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	return nil, nil
}
func (m mockResourceProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return nil, nil
}
func (m mockResourceProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return nil, nil
}
func (m mockResourceProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	return nil, nil
}
func (m mockResourceProvider) Nodes() ([]*corev1.Node, error) { return nil, nil }
func (m mockResourceProvider) GetResourceStatus(kind, namespace, name string) *topology.ResourceStatus {
	return nil
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
	if rc.Owner == nil || rc.Owner.Kind != "ReplicaSet" || rc.Owner.Name != "web-7d" || rc.Owner.Group != "apps" {
		t.Errorf("Owner: got %+v want apps/ReplicaSet prod/web-7d", rc.Owner)
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

func TestBuild_Deployment_WorkloadSummaryAndTemplateUses(t *testing.T) {
	replicas := int32(3)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "api-sa",
					Volumes: []corev1.Volume{{
						Name: "settings",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "api-settings"},
							},
						},
					}},
					Containers: []corev1.Container{{
						Name: "api",
						Env: []corev1.EnvVar{{
							Name: "TOKEN",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "api-token"},
									Key:                  "token",
								},
							},
						}},
					}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:       2,
			AvailableReplicas:   2,
			UpdatedReplicas:     3,
			UnavailableReplicas: 1,
		},
	}

	rc := Build(context.Background(), dep, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.WorkloadSummary == nil || rc.WorkloadSummary.Replicas == nil {
		t.Fatalf("WorkloadSummary.Replicas: got nil; rc=%+v", rc)
	}
	rep := rc.WorkloadSummary.Replicas
	if rep.Desired != 3 || rep.Ready != 2 || rep.Available != 2 || rep.Updated != 3 || rep.Unavailable != 1 {
		t.Errorf("Replicas: got %+v", rep)
	}
	if rc.Uses == nil {
		t.Fatal("Uses: got nil")
	}
	if got, want := len(rc.Uses.ConfigMaps), 1; got != want {
		t.Errorf("Uses.ConfigMaps len: got %d want %d (%+v)", got, want, rc.Uses.ConfigMaps)
	}
	if got, want := len(rc.Uses.Secrets), 1; got != want {
		t.Errorf("Uses.Secrets len: got %d want %d (%+v)", got, want, rc.Uses.Secrets)
	}
	if rc.Uses.ServiceAccount == nil || rc.Uses.ServiceAccount.Name != "api-sa" {
		t.Errorf("Uses.ServiceAccount: got %+v", rc.Uses.ServiceAccount)
	}
}

func TestBuild_Pod_PodSummary(t *testing.T) {
	pod := readyPod("api-1", "prod", map[string]string{"app": "api"}, true)
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         "api",
			Ready:        false,
			RestartCount: 2,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
			},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: "Error"},
			},
		},
		{
			Name:  "sidecar",
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		},
	}

	rc := Build(context.Background(), pod, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.PodSummary == nil {
		t.Fatal("PodSummary: got nil")
	}
	if rc.PodSummary.Phase != "Running" || !rc.PodSummary.Ready || rc.PodSummary.RestartCount != 2 {
		t.Errorf("PodSummary: got %+v", rc.PodSummary)
	}
	if got, want := len(rc.PodSummary.Containers), 2; got != want {
		t.Fatalf("Containers len: got %d want %d", got, want)
	}
	c := rc.PodSummary.Containers[0]
	if c.State != "waiting" || c.Reason != "CrashLoopBackOff" || c.LastTerminationReason != "Error" {
		t.Errorf("Container[0]: got %+v", c)
	}
}

func TestBuild_Service_ExposedByIngress(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
		},
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
		ServiceBackends: mockServiceBackends{
			readyPod("api-1", "prod", map[string]string{"app": "api"}, true),
			readyPod("api-2", "prod", map[string]string{"app": "api"}, false),
			readyPod("other", "prod", map[string]string{"app": "other"}, true),
		},
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
	if rc.ServiceSummary == nil || rc.ServiceSummary.SelectedPods == nil {
		t.Fatalf("ServiceSummary.SelectedPods: got nil; rc=%+v", rc)
	}
	if got, want := rc.ServiceSummary.SelectedPods.Total, 2; got != want {
		t.Errorf("SelectedPods.Total: got %d want %d", got, want)
	}
	if got, want := rc.ServiceSummary.SelectedPods.Ready, 1; got != want {
		t.Errorf("SelectedPods.Ready: got %d want %d", got, want)
	}
	if got, want := rc.ServiceSummary.SelectedPods.NotReady, 1; got != want {
		t.Errorf("SelectedPods.NotReady: got %d want %d", got, want)
	}
	if len(rc.ServiceSummary.Warnings) != 0 {
		t.Errorf("ServiceSummary.Warnings: got %+v want none", rc.ServiceSummary.Warnings)
	}
}

func TestBuild_Service_NoReadyPodsWarning(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
		},
	}
	rc := Build(context.Background(), svc, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		ServiceBackends: mockServiceBackends{
			readyPod("api-1", "prod", map[string]string{"app": "api"}, false),
		},
	})
	if rc.ServiceSummary == nil {
		t.Fatal("ServiceSummary: got nil")
	}
	if got := rc.ServiceSummary.Warnings; len(got) != 1 || got[0] != ServiceWarningNoReadyPods {
		t.Fatalf("Warnings: got %+v want [%s]", got, ServiceWarningNoReadyPods)
	}
}

func TestBuild_IngressSummary_BackendsTLSAndWarnings(t *testing.T) {
	className := "nginx"
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &className,
			DefaultBackend: &networkingv1.IngressBackend{
				Service: &networkingv1.IngressServiceBackend{Name: "fallback"},
			},
			Rules: []networkingv1.IngressRule{{
				Host: "example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
						Backend: networkingv1.IngressBackend{
							Service: &networkingv1.IngressServiceBackend{Name: "web"},
						},
					}}},
				},
			}},
			TLS: []networkingv1.IngressTLS{{SecretName: "web-tls"}},
		},
		Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{
			Ingress: []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.com"}},
		}},
	}

	rc := Build(context.Background(), ing, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.IngressSummary == nil {
		t.Fatal("IngressSummary: got nil")
	}
	if rc.IngressSummary.Class != "nginx" {
		t.Errorf("Class: got %q", rc.IngressSummary.Class)
	}
	if got, want := len(rc.IngressSummary.BackendServices), 2; got != want {
		t.Fatalf("BackendServices len: got %d want %d (%+v)", got, want, rc.IngressSummary.BackendServices)
	}
	if got, want := len(rc.IngressSummary.TLSSecrets), 1; got != want {
		t.Fatalf("TLSSecrets len: got %d want %d", got, want)
	}
	if len(rc.IngressSummary.Warnings) != 0 {
		t.Errorf("Warnings: got %+v want none", rc.IngressSummary.Warnings)
	}
}

func TestBuild_NodeSummary(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints: []corev1.Taint{{
				Key:    "dedicated",
				Value:  "batch",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3900m"),
				corev1.ResourceMemory: resource.MustParse("14Gi"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
			},
		},
	}

	rc := Build(context.Background(), node, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.NodeSummary == nil {
		t.Fatal("NodeSummary: got nil")
	}
	if rc.NodeSummary.ReadyStatus != "False" || !rc.NodeSummary.Unschedulable {
		t.Errorf("NodeSummary: got %+v", rc.NodeSummary)
	}
	if rc.NodeSummary.Capacity["cpu"] != "4" || rc.NodeSummary.Allocatable["memory"] != "14Gi" {
		t.Errorf("Capacity/Allocatable: got %+v / %+v", rc.NodeSummary.Capacity, rc.NodeSummary.Allocatable)
	}
	if got, want := len(rc.NodeSummary.Taints), 1; got != want {
		t.Fatalf("Taints len: got %d want %d", got, want)
	}
	if got := rc.NodeSummary.Warnings; len(got) != 3 {
		t.Errorf("Warnings: got %+v want unschedulable/not_ready/memory_pressure", got)
	}
}

func TestBuild_PVCSummary(t *testing.T) {
	storageClass := "standard"
	volumeMode := corev1.PersistentVolumeFilesystem
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "data",
			Namespace: "prod",
			Annotations: map[string]string{
				"volume.kubernetes.io/storage-provisioner": "pd.csi.storage.gke.io",
				"volume.kubernetes.io/selected-node":       "node-1",
				"pv.kubernetes.io/bind-completed":          "yes",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClass,
			VolumeName:       "pv-data",
			VolumeMode:       &volumeMode,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimPending,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("8Gi")},
		},
	}

	rc := Build(context.Background(), pvc, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.PVCSummary == nil {
		t.Fatal("PVCSummary: got nil")
	}
	if rc.PVCSummary.Phase != "Pending" || rc.PVCSummary.RequestedStorage != "10Gi" || rc.PVCSummary.CapacityStorage != "8Gi" {
		t.Errorf("PVCSummary: got %+v", rc.PVCSummary)
	}
	if got := rc.PVCSummary.Warnings; len(got) != 1 || got[0] != PVCWarningPending {
		t.Errorf("Warnings: got %+v", got)
	}
}

func TestBuild_JobAndCronJobSummary(t *testing.T) {
	completions := int32(5)
	parallelism := int32(2)
	backoff := int32(3)
	suspend := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "prod"},
		Spec: batchv1.JobSpec{
			Completions:  &completions,
			Parallelism:  &parallelism,
			BackoffLimit: &backoff,
			Suspend:      &suspend,
		},
		Status: batchv1.JobStatus{Active: 1, Succeeded: 2, Failed: 1},
	}
	rc := Build(context.Background(), job, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.JobSummary == nil {
		t.Fatal("JobSummary: got nil")
	}
	if rc.JobSummary.Completions != 5 || rc.JobSummary.Parallelism != 2 || rc.JobSummary.BackoffLimit != 3 || !rc.JobSummary.Suspended {
		t.Errorf("JobSummary: got %+v", rc.JobSummary)
	}

	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "prod"},
		Spec:       batchv1.CronJobSpec{Schedule: "0 0 * * *", Suspend: &suspend},
		Status: batchv1.CronJobStatus{
			Active: []corev1.ObjectReference{{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       "nightly-1",
			}},
		},
	}
	rc = Build(context.Background(), cj, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.CronJobSummary == nil {
		t.Fatal("CronJobSummary: got nil")
	}
	if rc.CronJobSummary.Schedule != "0 0 * * *" || !rc.CronJobSummary.Suspended {
		t.Errorf("CronJobSummary: got %+v", rc.CronJobSummary)
	}
	if got, want := len(rc.CronJobSummary.ActiveJobs), 1; got != want {
		t.Fatalf("ActiveJobs len: got %d want %d", got, want)
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

func TestBuild_Unstructured_StatusSummary(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"metadata": map[string]interface{}{
			"name":      "w1",
			"namespace": "prod",
		},
		"status": map[string]interface{}{
			"phase": "Reconciling",
			"conditions": []interface{}{
				map[string]interface{}{
					"type":               "Ready",
					"status":             "False",
					"reason":             "DependencyMissing",
					"message":            "waiting for dependency",
					"lastTransitionTime": "2026-05-21T10:00:00Z",
				},
			},
		},
	}}

	rc := Build(context.Background(), obj, Options{Tier: TierBasic, AccessChecker: allowAllChecker{}})
	if rc.StatusSummary == nil {
		t.Fatal("StatusSummary: got nil")
	}
	if rc.StatusSummary.Phase != "Reconciling" {
		t.Errorf("Phase: got %q", rc.StatusSummary.Phase)
	}
	if got, want := len(rc.StatusSummary.Conditions), 1; got != want {
		t.Fatalf("Conditions len: got %d want %d", got, want)
	}
	cond := rc.StatusSummary.Conditions[0]
	if cond.Type != "Ready" || cond.Status != "False" || cond.Reason != "DependencyMissing" {
		t.Errorf("Condition: got %+v", cond)
	}
}

func TestBuild_ConfigMapReferencedBy(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "prod"},
		Data:       map[string]string{"config.yaml": "enabled: true"},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
							},
						},
					}},
					Containers: []corev1.Container{{
						Name: "api",
						EnvFrom: []corev1.EnvFromSource{{
							ConfigMapRef: &corev1.ConfigMapEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"},
							},
						}},
					}},
				},
			},
		},
	}
	other := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "prod"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "other",
						EnvFrom: []corev1.EnvFromSource{{
							ConfigMapRef: &corev1.ConfigMapEnvSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "other-config"},
							},
						}},
					}},
				},
			},
		},
	}

	rc := Build(context.Background(), cm, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Provider:      mockResourceProvider{deploys: []*appsv1.Deployment{other, dep}},
	})
	if rc.ReferencedBy == nil {
		t.Fatal("ReferencedBy: got nil")
	}
	if got, want := rc.ReferencedBy.Total, 1; got != want {
		t.Fatalf("ReferencedBy.Total: got %d want %d", got, want)
	}
	if got, want := len(rc.ReferencedBy.Items), 1; got != want {
		t.Fatalf("ReferencedBy.Items len: got %d want %d", got, want)
	}
	ref := rc.ReferencedBy.Items[0]
	if ref.Kind != "Deployment" || ref.Group != "apps" || ref.Namespace != "prod" || ref.Name != "api" {
		t.Fatalf("ReferencedBy item: got %+v", ref)
	}
	wantPaths := []string{
		"spec.template.spec.containers[].envFrom[].configMapRef.name",
		"spec.template.spec.volumes[].configMap.name",
	}
	if got := ref.Paths; !stringSlicesEqual(got, wantPaths) {
		t.Fatalf("ReferencedBy paths: got %+v want %+v", got, wantPaths)
	}
}

func TestBuild_SecretReferencedByCapsAndSkipsOwnedPods(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "prod"}}
	deploys := make([]*appsv1.Deployment, 0, maxReferencedByItems+1)
	for i := 0; i < maxReferencedByItems+1; i++ {
		deploys = append(deploys, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api-" + string(rune('a'+i)), Namespace: "prod"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name: "api",
							Env: []corev1.EnvVar{{
								Name: "PASSWORD",
								ValueFrom: &corev1.EnvVarSource{
									SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: "db-creds"},
										Key:                  "password",
									},
								},
							}},
						}},
					},
				},
			},
		})
	}
	ownedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "api-owned",
			Namespace:       "prod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "api-rs", Controller: ptrBool(true)}},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "api",
				EnvFrom: []corev1.EnvFromSource{{
					SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "db-creds"}},
				}},
			}},
		},
	}

	rc := Build(context.Background(), secret, Options{
		Tier:          TierBasic,
		AccessChecker: allowAllChecker{},
		Provider:      mockResourceProvider{deploys: deploys, pods: []*corev1.Pod{ownedPod}},
	})
	if rc.ReferencedBy == nil {
		t.Fatal("ReferencedBy: got nil")
	}
	if got, want := rc.ReferencedBy.Total, maxReferencedByItems+1; got != want {
		t.Fatalf("ReferencedBy.Total: got %d want %d", got, want)
	}
	if got, want := len(rc.ReferencedBy.Items), maxReferencedByItems; got != want {
		t.Fatalf("ReferencedBy.Items len: got %d want %d", got, want)
	}
	if !rc.ReferencedBy.Truncated {
		t.Fatal("ReferencedBy.Truncated: got false want true")
	}
	for _, ref := range rc.ReferencedBy.Items {
		if ref.Kind == "Pod" {
			t.Fatalf("owned pod should be skipped when controller workload is available: %+v", ref)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ptrBool(b bool) *bool { return &b }

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readyPod(name, namespace string, podLabels map[string]string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    podLabels,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: status,
			}},
		},
	}
}

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
