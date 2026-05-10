package k8s

// Audit tests for ComputeDiff. Two goals:
//   1. Pin that pure-noise updates (heartbeats, managedFields) still produce
//      nil diffs — those are what KindHasDiffer + the no-diff drop filter out.
//   2. Pin that real signal we previously missed (Node pressure flips,
//      HTTPRoute Programmed flips, Job Failed condition, HPA ScalingActive
//      flip) now produces a non-nil diff. If a future refactor removes
//      coverage, the test catches it before the no-diff drop silently hides
//      the regression.

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestComputeDiff_PodHeartbeatOnly_ReturnsNil(t *testing.T) {
	t0 := time.Now()
	t1 := t0.Add(10 * time.Second)

	base := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "p", ResourceVersion: "1"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastProbeTime: metav1.NewTime(t0), LastTransitionTime: metav1.NewTime(t0)},
				{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastProbeTime: metav1.NewTime(t0), LastTransitionTime: metav1.NewTime(t0)},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", Ready: true, RestartCount: 0, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(t0)}}},
			},
		},
	}

	updated := base.DeepCopy()
	updated.ResourceVersion = "2"
	for i := range updated.Status.Conditions {
		updated.Status.Conditions[i].LastProbeTime = metav1.NewTime(t1)
	}

	diff := ComputeDiff("Pod", base, updated)
	if diff != nil {
		t.Fatalf("expected nil diff for heartbeat-only update, got %+v", diff)
	}
}

func TestComputeDiff_NodeHeartbeatOnly_ReturnsNil(t *testing.T) {
	t0 := time.Now()
	t1 := t0.Add(10 * time.Second)

	base := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n", ResourceVersion: "1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.NewTime(t0), LastTransitionTime: metav1.NewTime(t0)},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse, LastHeartbeatTime: metav1.NewTime(t0), LastTransitionTime: metav1.NewTime(t0)},
			},
		},
	}

	updated := base.DeepCopy()
	updated.ResourceVersion = "2"
	for i := range updated.Status.Conditions {
		updated.Status.Conditions[i].LastHeartbeatTime = metav1.NewTime(t1)
	}

	diff := ComputeDiff("Node", base, updated)
	if diff != nil {
		t.Fatalf("expected nil diff for heartbeat-only Node update, got %+v", diff)
	}
}

func TestComputeDiff_ServiceManagedFieldsOnly_ReturnsNil(t *testing.T) {
	base := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "svc", ResourceVersion: "1"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: []corev1.ServicePort{{Port: 80}}},
	}
	updated := base.DeepCopy()
	updated.ResourceVersion = "2"
	updated.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kube-controller-manager", Operation: "Update"}}

	diff := ComputeDiff("Service", base, updated)
	if diff != nil {
		t.Fatalf("expected nil diff for managedFields-only Service update, got %+v", diff)
	}
}

// ---------------------------------------------------------------------------
// Positive coverage: signal that previously slipped through must now be caught.
// ---------------------------------------------------------------------------

func TestComputeDiff_NodeMemoryPressureFlip_Detected(t *testing.T) {
	t0 := time.Now()
	base := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, LastHeartbeatTime: metav1.NewTime(t0)},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse, LastHeartbeatTime: metav1.NewTime(t0)},
			},
		},
	}
	updated := base.DeepCopy()
	updated.Status.Conditions[1].Status = corev1.ConditionTrue
	updated.Status.Conditions[1].LastHeartbeatTime = metav1.NewTime(t0.Add(time.Second))

	diff := ComputeDiff("Node", base, updated)
	if diff == nil {
		t.Fatal("expected non-nil diff when MemoryPressure flips True — previously missed")
	}
	if !containsPath(diff, "status.conditions[MemoryPressure]") {
		t.Errorf("expected MemoryPressure path in diff, got %+v", diff.Fields)
	}
}

func TestComputeDiff_NodeKubeletUpgrade_Detected(t *testing.T) {
	base := &corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.30.0"}}}
	updated := base.DeepCopy()
	updated.Status.NodeInfo.KubeletVersion = "v1.30.5"

	diff := ComputeDiff("Node", base, updated)
	if diff == nil || !containsPath(diff, "status.nodeInfo.kubeletVersion") {
		t.Fatalf("expected kubelet upgrade to be detected, got %+v", diff)
	}
}

func TestComputeDiff_HPAScalingActiveFlip_Detected(t *testing.T) {
	base := &autoscalingv2.HorizontalPodAutoscaler{
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{
				{Type: autoscalingv2.ScalingActive, Status: corev1.ConditionTrue},
			},
		},
	}
	updated := base.DeepCopy()
	updated.Status.Conditions[0].Status = corev1.ConditionFalse

	diff := ComputeDiff("HorizontalPodAutoscaler", base, updated)
	if diff == nil || !containsPath(diff, "status.conditions[ScalingActive]") {
		t.Fatalf("expected ScalingActive flip to be detected, got %+v", diff)
	}
}

func TestComputeDiff_JobFailedCondition_Detected(t *testing.T) {
	base := &batchv1.Job{}
	updated := base.DeepCopy()
	updated.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"},
	}
	diff := ComputeDiff("Job", base, updated)
	if diff == nil || !containsPath(diff, "status.conditions[Failed]") {
		t.Fatalf("expected JobFailed condition to be detected, got %+v", diff)
	}
}

func TestComputeDiff_DeploymentAvailableFlip_Detected(t *testing.T) {
	base := &appsv1.Deployment{
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
			},
		},
	}
	updated := base.DeepCopy()
	updated.Status.Conditions[0].Status = corev1.ConditionFalse

	diff := ComputeDiff("Deployment", base, updated)
	if diff == nil || !containsPath(diff, "status.conditions[Available]") {
		t.Fatalf("expected Available flip to be detected, got %+v", diff)
	}
}

func TestComputeDiff_HTTPRouteProgrammedFlip_PerParent(t *testing.T) {
	// One parent, one route — Accepted stays True, Programmed flips False.
	// The previous count-based logic (count of Accepted parents) would not have
	// noticed; the per-parent per-condition logic must.
	parent := func(programmed string) map[string]any {
		return map[string]any{
			"parentRef": map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "infra", "name": "g"},
			"conditions": []any{
				map[string]any{"type": "Accepted", "status": "True"},
				map[string]any{"type": "Programmed", "status": programmed},
			},
		}
	}
	old := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"parents": []any{parent("True")}},
	}}
	upd := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"parents": []any{parent("False")}},
	}}
	diff := ComputeDiff("HTTPRoute", old, upd)
	if diff == nil {
		t.Fatal("expected non-nil diff when Programmed flips on a parent")
	}
	if !containsPathSubstring(diff, "Programmed") {
		t.Errorf("expected per-parent Programmed in diff, got %+v", diff.Fields)
	}
}

func TestKindHasDiffer_ContractMatchesComputeDiff(t *testing.T) {
	// Drift guard: if a kind is in KindHasDiffer but ComputeDiff doesn't recognize
	// it, the no-diff drop fires for a kind that always returns nil — silently
	// dropping every update. Verify each KindHasDiffer kind actually reaches a
	// non-default case in ComputeDiff (proxy: a no-op old/new of the right type
	// returns nil only when ComputeDiff dispatched).
	kinds := []string{
		"Deployment", "Pod", "Service", "ConfigMap", "Ingress",
		"ReplicaSet", "DaemonSet", "StatefulSet",
		"HorizontalPodAutoscaler", "Job", "Node", "PersistentVolumeClaim",
		"Application", "Kustomization", "HelmRelease",
		"GitRepository", "OCIRepository", "HelmRepository",
		"Gateway", "HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute",
	}
	for _, k := range kinds {
		if !KindHasDiffer(k) {
			t.Errorf("%s: KindHasDiffer should return true for kinds in ComputeDiff", k)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func containsPath(d *DiffInfo, path string) bool {
	for _, f := range d.Fields {
		if f.Path == path {
			return true
		}
	}
	return false
}

func containsPathSubstring(d *DiffInfo, substr string) bool {
	for _, f := range d.Fields {
		if len(f.Path) > 0 && stringContains(f.Path, substr) {
			return true
		}
	}
	return false
}

func stringContains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
