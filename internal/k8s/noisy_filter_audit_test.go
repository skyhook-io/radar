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

	"github.com/skyhook-io/radar/internal/timeline"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

func TestComputeDiff_UnknownCRDMetadataOnly_ReturnsNil(t *testing.T) {
	base := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":            "w",
			"namespace":       "default",
			"resourceVersion": "1",
			"generation":      int64(3),
		},
		"status": map[string]any{
			"observedGeneration": int64(3),
			"conditions": []any{map[string]any{
				"type":               "Ready",
				"status":             "True",
				"reason":             "Available",
				"lastTransitionTime": "2026-05-31T10:00:00Z",
			}},
		},
	}}

	updated := base.DeepCopy()
	updated.SetResourceVersion("2")
	_ = unstructured.SetNestedField(updated.Object, int64(3), "status", "observedGeneration")
	_ = unstructured.SetNestedSlice(updated.Object, []any{map[string]any{
		"type":               "Ready",
		"status":             "True",
		"reason":             "Available",
		"lastTransitionTime": "2026-05-31T10:05:00Z",
	}}, "status", "conditions")

	if diff := ComputeDiff("Widget", base, updated); diff != nil {
		t.Fatalf("expected nil diff for metadata/timestamp-only unknown CRD update, got %+v", diff)
	}
}

func TestComputeDiff_UnknownCRDConditionStatus_Detected(t *testing.T) {
	base := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"name":       "w",
			"namespace":  "default",
			"generation": int64(3),
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "Reconciling"}},
		},
	}}
	updated := base.DeepCopy()
	_ = unstructured.SetNestedSlice(updated.Object, []any{map[string]any{"type": "Ready", "status": "True", "reason": "Available"}}, "status", "conditions")

	diff := ComputeDiff("Widget", base, updated)
	if diff == nil || !containsPath(diff, "status.conditions[Ready]") {
		t.Fatalf("expected Ready condition diff for unknown CRD, got %+v", diff)
	}
}

func TestComputeDiff_UnknownCRDArbitraryStatus_Detected(t *testing.T) {
	base := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"name": "w", "namespace": "default", "generation": int64(1)},
		"status":     map[string]any{"endpoint": "10.0.0.1"},
	}}
	updated := base.DeepCopy()
	_ = unstructured.SetNestedField(updated.Object, "10.0.0.2", "status", "endpoint")

	diff := ComputeDiff("Widget", base, updated)
	if diff == nil || !containsPath(diff, "resource") {
		t.Fatalf("expected generic resource diff for unknown status field, got %+v", diff)
	}
}

func TestRecordToTimelineStore_SyncAddMarksResourceSeen(t *testing.T) {
	prev := initialSyncComplete
	initialSyncComplete = false
	defer func() { initialSyncComplete = prev }()

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	defer timeline.ResetStore()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "p",
			Namespace:         "default",
			UID:               "pod-uid",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute)),
		},
	}

	recordToTimelineStore(ActiveClusterContext(), "Pod", "default", "p", "pod-uid", "add", nil, pod, nil, false)

	store := timeline.GetStore()
	if store == nil {
		t.Fatal("timeline store is nil")
	}
	if !store.IsResourceSeen(ActiveClusterContext(), "Pod", "default", "p") {
		t.Fatal("sync add should mark resource seen after historical event recording")
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

// Note: contract drift between KindHasDiffer and ComputeDiff dispatch is
// structurally impossible — both read the same diffFunctions map. No test
// needed for that anymore.

func TestComputeDiff_ReplicaSetReplicaFailure_Detected(t *testing.T) {
	base := &appsv1.ReplicaSet{
		Status: appsv1.ReplicaSetStatus{
			Conditions: []appsv1.ReplicaSetCondition{
				{Type: appsv1.ReplicaSetReplicaFailure, Status: corev1.ConditionFalse},
			},
		},
	}
	updated := base.DeepCopy()
	updated.Status.Conditions[0].Status = corev1.ConditionTrue

	diff := ComputeDiff("ReplicaSet", base, updated)
	if diff == nil || !containsPath(diff, "status.conditions[ReplicaFailure]") {
		t.Fatalf("expected ReplicaFailure flip detected, got %+v", diff)
	}
}

func TestComputeDiff_DaemonSetMisscheduled_Detected(t *testing.T) {
	base := &appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{NumberMisscheduled: 0}}
	updated := base.DeepCopy()
	updated.Status.NumberMisscheduled = 3
	diff := ComputeDiff("DaemonSet", base, updated)
	if diff == nil || !containsPath(diff, "status.numberMisscheduled") {
		t.Fatalf("expected NumberMisscheduled change detected, got %+v", diff)
	}
}

func TestComputeDiff_DaemonSetProbeConfig_Detected(t *testing.T) {
	base := &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "agent", Image: "agent:v1",
		ReadinessProbe: &corev1.Probe{PeriodSeconds: 10, TimeoutSeconds: 1},
	}}}}}}
	updated := base.DeepCopy()
	updated.Spec.Template.Spec.Containers[0].ReadinessProbe.TimeoutSeconds = 5

	diff := ComputeDiff("DaemonSet", base, updated)
	if diff == nil || !containsPath(diff, "spec.template.spec.containers[agent].readinessProbe") {
		t.Fatalf("expected DaemonSet readinessProbe change detected, got %+v", diff)
	}
}

func TestComputeDiff_StatefulSetEnvRemoval_Detected(t *testing.T) {
	base := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "db", Image: "db:v1", Env: []corev1.EnvVar{{Name: "DATABASE_URL", Value: "postgres://db"}},
	}}}}}}
	updated := base.DeepCopy()
	updated.Spec.Template.Spec.Containers[0].Env = nil

	diff := ComputeDiff("StatefulSet", base, updated)
	if diff == nil || !containsPath(diff, "spec.template.spec.containers[db].env[DATABASE_URL]") {
		t.Fatalf("expected StatefulSet env removal detected, got %+v", diff)
	}
}

func TestComputeDiff_FluxKustomizationStalled_Detected(t *testing.T) {
	mk := func(stalledStatus string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Stalled", "status": stalledStatus},
				},
			},
		}}
	}
	diff := ComputeDiff("Kustomization", mk("False"), mk("True"))
	if diff == nil || !containsPath(diff, "status.conditions[Stalled]") {
		t.Fatalf("expected Kustomization Stalled flip detected, got %+v", diff)
	}
}

func TestComputeDiff_HTTPRouteMultiParent_PerListener(t *testing.T) {
	// Two parents on the same Gateway via different sectionNames. Parent A
	// stays Accepted=True; parent B flips Programmed False→True. Without
	// per-listener keying, the second parent overwrites the first in the
	// per-parent map and the Programmed flip is invisible.
	parent := func(section, programmed string) map[string]any {
		return map[string]any{
			"parentRef": map[string]any{
				"group": "gateway.networking.k8s.io", "kind": "Gateway",
				"namespace": "infra", "name": "g", "sectionName": section,
			},
			"conditions": []any{
				map[string]any{"type": "Accepted", "status": "True"},
				map[string]any{"type": "Programmed", "status": programmed},
			},
		}
	}
	old := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"parents": []any{parent("http", "True"), parent("https", "False")}},
	}}
	upd := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"parents": []any{parent("http", "True"), parent("https", "True")}},
	}}
	diff := ComputeDiff("HTTPRoute", old, upd)
	if diff == nil {
		t.Fatal("expected non-nil diff when one of two listener parents flips Programmed")
	}
	// We should see the https listener flip but not the http listener.
	httpsHit, httpHit := false, false
	for _, f := range diff.Fields {
		if stringContains(f.Path, "https") && stringContains(f.Path, "Programmed") {
			httpsHit = true
		}
		if stringContains(f.Path, "/http/") && stringContains(f.Path, "Programmed") {
			httpHit = true
		}
	}
	if !httpsHit {
		t.Errorf("expected https listener Programmed flip in diff, got %+v", diff.Fields)
	}
	if httpHit {
		t.Errorf("did not expect http listener flip in diff (was unchanged), got %+v", diff.Fields)
	}
}

func TestComputeDiff_HTTPRouteRemovedParent_Detected(t *testing.T) {
	// One parent in old, zero parents in new — both per-parent walk and the
	// length check should fire. Worst case: one parent removed + one added so
	// the count stays the same; the union-walk catches the disappearance.
	mk := func(parents []any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{"parents": parents},
		}}
	}
	parentA := map[string]any{
		"parentRef":  map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "infra", "name": "a"},
		"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}},
	}
	parentB := map[string]any{
		"parentRef":  map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": "infra", "name": "b"},
		"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}},
	}
	diff := ComputeDiff("HTTPRoute", mk([]any{parentA}), mk([]any{parentB}))
	if diff == nil {
		t.Fatal("expected non-nil diff when one parent disappears and another appears")
	}
}

func TestComputeDiff_PodReadinessGateFlip_Detected(t *testing.T) {
	base := &corev1.Pod{Status: corev1.PodStatus{
		Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		},
	}}
	updated := base.DeepCopy()
	updated.Status.Conditions[0].Status = corev1.ConditionFalse
	diff := ComputeDiff("Pod", base, updated)
	if diff == nil || !containsPath(diff, "status.conditions[Ready]") {
		t.Fatalf("expected PodReady flip detected, got %+v", diff)
	}
}

func TestComputeDiff_PodEphemeralContainerAttached_Detected(t *testing.T) {
	base := &corev1.Pod{}
	updated := base.DeepCopy()
	updated.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: "debugger"}}
	diff := ComputeDiff("Pod", base, updated)
	if diff == nil || !containsPath(diff, "status.ephemeralContainerStatuses") {
		t.Fatalf("expected ephemeral container attach detected, got %+v", diff)
	}
}

func TestComputeDiff_NodeAllocatableChanged_Detected(t *testing.T) {
	base := &corev1.Node{Status: corev1.NodeStatus{
		Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resourceQty("4"),
			corev1.ResourceMemory: resourceQty("8Gi"),
		},
	}}
	updated := base.DeepCopy()
	updated.Status.Allocatable[corev1.ResourceMemory] = resourceQty("4Gi")
	diff := ComputeDiff("Node", base, updated)
	if diff == nil || !containsPath(diff, "status.allocatable.memory") {
		t.Fatalf("expected allocatable memory change detected, got %+v", diff)
	}
}

func TestComputeDiff_ApplicationImageRoll_Detected(t *testing.T) {
	mk := func(images []string) *unstructured.Unstructured {
		imgs := make([]any, len(images))
		for i, s := range images {
			imgs[i] = s
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"sync":    map[string]any{"status": "Synced"},
				"health":  map[string]any{"status": "Healthy"},
				"summary": map[string]any{"images": imgs},
			},
		}}
	}
	diff := ComputeDiff("Application", mk([]string{"app:v1"}), mk([]string{"app:v2"}))
	if diff == nil || !containsPath(diff, "status.summary.images") {
		t.Fatalf("expected Application image roll detected (Synced+Healthy app rolling images), got %+v", diff)
	}
}

func resourceQty(s string) resource.Quantity {
	return resource.MustParse(s)
}

func TestComputeDiff_GatewayClassAcceptedFlip_Detected(t *testing.T) {
	mk := func(accepted string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"spec":   map[string]any{"controllerName": "example.com/gateway-controller"},
			"status": map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": accepted}}},
		}}
	}
	diff := ComputeDiff("GatewayClass", mk("True"), mk("False"))
	if diff == nil || !containsPath(diff, "status.conditions.Accepted") {
		t.Fatalf("expected GatewayClass Accepted flip detected, got %+v", diff)
	}
}

func TestComputeDiff_ReferenceGrantSpecChange_Detected(t *testing.T) {
	mk := func(toCount int) *unstructured.Unstructured {
		toItems := make([]any, toCount)
		for i := range toItems {
			toItems[i] = map[string]any{"group": "", "kind": "Service"}
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"from": []any{}, "to": toItems},
		}}
	}
	diff := ComputeDiff("ReferenceGrant", mk(1), mk(2))
	if diff == nil || !containsPath(diff, "spec.to") {
		t.Fatalf("expected ReferenceGrant spec.to change detected, got %+v", diff)
	}
}

func TestComputeDiff_DeploymentEnvRemoval_ReturnsFieldDiff(t *testing.T) {
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 5},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "app", Image: "app:v1",
						Env: []corev1.EnvVar{{Name: "FOO", Value: "1"}},
					}},
				},
			},
		},
	}
	updated := old.DeepCopy()
	updated.Generation = 6
	updated.Spec.Template.Spec.Containers[0].Env = nil

	diff := ComputeDiff("Deployment", old, updated)
	if diff == nil {
		t.Fatalf("ComputeDiff returned nil")
	}
	if !diffHasPath(diff, "spec.template.spec.containers[app].env[FOO]") {
		t.Fatalf("expected env field diff, got %+v", diff.Fields)
	}
	if diff.Fields[0].NewValue != nil {
		t.Fatalf("removed env var should have nil NewValue, got %#v", diff.Fields[0].NewValue)
	}

	// The generation helper remains the fallback for unstructured objects and
	// kind-specific coverage gaps.
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"generation": int64(42)},
	}}
	if got := getGeneration(u); got != 42 {
		t.Errorf("getGeneration(unstructured) = %d, want 42", got)
	}
}

func TestComputeDiff_ConfigMapStructuredJSON_RedactsAndPinpointsField(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"demo.flagd.json": `{"flags":{"paymentFailure":{"defaultVariant":"off"}},"apiToken":"old-secret"}`,
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"demo.flagd.json": `{"flags":{"paymentFailure":{"defaultVariant":"on"}},"apiToken":"new-secret"}`,
	}}

	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatalf("ComputeDiff returned nil")
	}
	if !diffHasPath(diff, "data.demo.flagd.json.flags.paymentFailure.defaultVariant") {
		t.Fatalf("expected structured flag field diff, got %+v", diff.Fields)
	}
	if !diffHasRedactedPath(diff, "data.demo.flagd.json.apiToken") {
		t.Fatalf("expected apiToken redaction, got %+v", diff.Fields)
	}
}

func TestComputeDiff_ConfigMapStructuredJSON_RedactsAddedSubtree(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"settings.json": `{"auth":{}}`,
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"settings.json": `{"auth":{"apiToken":"new-secret"}}`,
	}}

	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatalf("ComputeDiff returned nil")
	}
	if !diffHasNewRedactedPath(diff, "data.settings.json.auth.apiToken") {
		t.Fatalf("expected added apiToken redaction, got %+v", diff.Fields)
	}
}

func TestComputeDiff_ConfigMapStructuredJSONByContent(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"flags": `{"cartFailure":{"defaultVariant":"off"}}`,
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"flags": `{"cartFailure":{"defaultVariant":"on"}}`,
	}}

	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatalf("ComputeDiff returned nil")
	}
	if !diffHasPath(diff, "data.flags.cartFailure.defaultVariant") {
		t.Fatalf("expected structured flag field diff, got %+v", diff.Fields)
	}
}

func TestComputeDiff_ConfigMapYAMLRequiresStructuredKey(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"plain": "enabled: false\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"plain": "enabled: true\n",
	}}

	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatalf("ComputeDiff returned nil")
	}
	if diffHasPath(diff, "data.plain.enabled") {
		t.Fatalf("unexpected structured YAML diff for untyped key, got %+v", diff.Fields)
	}
	if !diffHasPath(diff, "data (modified keys)") {
		t.Fatalf("expected key-level fallback diff, got %+v", diff.Fields)
	}
}

func diffHasPath(diff *DiffInfo, path string) bool {
	for _, f := range diff.Fields {
		if f.Path == path {
			return true
		}
	}
	return false
}

func diffHasRedactedPath(diff *DiffInfo, path string) bool {
	for _, f := range diff.Fields {
		if f.Path == path && f.OldValue == "[REDACTED]" && f.NewValue == "[REDACTED]" {
			return true
		}
	}
	return false
}

func diffHasNewRedactedPath(diff *DiffInfo, path string) bool {
	for _, f := range diff.Fields {
		if f.Path == path && f.NewValue == "[REDACTED]" {
			return true
		}
	}
	return false
}

func TestComputeDiff_ApplicationConditionAdded_Detected(t *testing.T) {
	mk := func(conds []any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"sync":       map[string]any{"status": "Synced"},
				"health":     map[string]any{"status": "Healthy"},
				"conditions": conds,
			},
		}}
	}
	diff := ComputeDiff("Application",
		mk([]any{}),
		mk([]any{map[string]any{"type": "OrphanedResourceWarning", "status": "True"}}),
	)
	if diff == nil || !containsPath(diff, "status.conditions[OrphanedResourceWarning]") {
		t.Fatalf("expected Application condition addition detected, got %+v", diff)
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
