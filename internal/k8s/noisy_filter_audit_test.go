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
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

func TestComputeDiff_ConfigMapStructuredYAMLByContent(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"mongod.conf": "net:\n  tls:\n    mode: disabled\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"mongod.conf": "net:\n  tls:\n    mode: requireTLS\n    certificateKeyFile: /certs/server.pem\n",
	}}

	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for _, path := range []string{
		"data.mongod.conf.net.tls.mode",
		"data.mongod.conf.net.tls.certificateKeyFile",
	} {
		if !diffHasPath(diff, path) {
			t.Errorf("expected structured YAML path %q, got %+v", path, diff.Fields)
		}
	}
	if diffHasPath(diff, "data (modified keys)") {
		t.Fatalf("unexpected key-only fallback for structured YAML: %+v", diff.Fields)
	}
}

func TestComputeDiff_ConfigMapMultiDocumentYAMLFallsBack(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"app-config": "a: 1\n---\nb: 2\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"app-config": "a: 9\n---\nb: 8\n",
	}}
	assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "app-config")
}

func TestComputeDiff_ConfigMapBracketedPlainConfigFallsBack(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"fluent-bit.conf": "[INPUT]\n  Name tail\n  Path /a.log\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"fluent-bit.conf": "[FILTER]\n  Name grep\n  Regex log err\n",
	}}
	assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "fluent-bit.conf")
}

func TestComputeDiff_ConfigMapOneLineTextFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		oldVal string
		newVal string
	}{
		{name: "plain text", oldVal: "hello world", newVal: "goodbye world"},
		{name: "colon prose", oldVal: "Note: old", newVal: "Note: new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &corev1.ConfigMap{Data: map[string]string{"plain": tc.oldVal}}
			updated := &corev1.ConfigMap{Data: map[string]string{"plain": tc.newVal}}
			assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "plain")
		})
	}
}

func TestComputeDiff_ConfigMapStructuredValuesRedactCredentialURLs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    string
		oldVal string
		newVal string
		path   string
		want   string
	}{
		{
			name:   "yaml",
			key:    "database.conf",
			oldVal: "database:\n  connection: postgres://user:oldpass@db.example/app\n",
			newVal: "database:\n  connection: postgres://user:newpass@db.example/app\n",
			path:   "data.database.conf.database.connection",
			want:   "postgres://user:[REDACTED]@db.example/app",
		},
		{
			name:   "json",
			key:    "database.json",
			oldVal: `{"database":{"connection":"postgres://user:oldpass@db.example/app"}}`,
			newVal: `{"database":{"connection":"postgres://user:newpass@db.example/app"}}`,
			path:   "data.database.json.database.connection",
			want:   "postgres://user:[REDACTED]@db.example/app",
		},
		{
			name:   "properties",
			key:    "application.properties",
			oldVal: "database.url=postgres://user:oldpass@db.example/app",
			newVal: "database.url=postgres://user:newpass@db.example/app",
			path:   "data.application.properties.database.url",
			want:   "[REDACTED]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.oldVal}}
			updated := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.newVal}}
			diff := ComputeDiff("ConfigMap", old, updated)
			if diff == nil {
				t.Fatal("ComputeDiff returned nil")
			}
			change, ok := findChangePath(diff.Fields, tc.path)
			if !ok {
				t.Fatalf("expected credential URL change at %q, got %+v", tc.path, diff.Fields)
			}
			if change.OldValue != tc.want || change.NewValue != tc.want {
				t.Fatalf("credential URL redaction = %#v -> %#v, want %q", change.OldValue, change.NewValue, tc.want)
			}
		})
	}
}

func TestComputeDiff_ConfigMapPropertiesFormats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    string
		oldVal string
		newVal string
		path   string
	}{
		{
			name:   "single property",
			key:    "application.properties",
			oldVal: "mode=old",
			newVal: "mode=new",
			path:   "data.application.properties.mode",
		},
		{
			name:   "dotenv",
			key:    ".env.production",
			oldVal: "MODE=old\nREGION=us-east-1",
			newVal: "MODE=new\nREGION=us-east-1",
			path:   "data..env.production.MODE",
		},
		{
			name:   "colon in property value",
			key:    "application.properties",
			oldVal: "DESCRIPTION=Service: legacy\nOWNER=team: payments",
			newVal: "DESCRIPTION=Service: billing\nOWNER=team: payments",
			path:   "data.application.properties.DESCRIPTION",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.oldVal}}
			updated := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.newVal}}
			diff := ComputeDiff("ConfigMap", old, updated)
			if diff == nil || !diffHasPath(diff, tc.path) {
				t.Fatalf("expected properties diff at %q, got %+v", tc.path, diff)
			}
		})
	}
}

func TestComputeDiff_ConfigMapDotEnvRedactsAllValues(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		".env": "MODE=old\nDB_PASS=hunter2hunter2\nSENDGRID_KEY=old-vendor-secret\nREMOVED=old-value",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		".env": "MODE=new\nDB_PASS=s3cr3ts3cr3t\nSENDGRID_KEY=new-vendor-secret\nADDED=new-value",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	mode, ok := findChangePath(diff.Fields, "data..env.MODE")
	if !ok || mode.OldValue != "[REDACTED]" || mode.NewValue != "[REDACTED]" {
		t.Fatalf("dotenv value was not redacted: %+v", diff.Fields)
	}
	for _, path := range []string{"data..env.DB_PASS", "data..env.SENDGRID_KEY"} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok {
			t.Fatalf("expected dotenv change at %s, got %+v", path, diff.Fields)
		}
		if change.OldValue != "[REDACTED]" || change.NewValue != "[REDACTED]" {
			t.Fatalf("dotenv value at %s was not redacted: %#v -> %#v", path, change.OldValue, change.NewValue)
		}
	}
	removed, ok := findChangePath(diff.Fields, "data..env.REMOVED")
	if !ok || removed.OldValue != "[REDACTED]" || removed.NewValue != nil {
		t.Fatalf("removed dotenv value was not safely represented: %+v", diff.Fields)
	}
	added, ok := findChangePath(diff.Fields, "data..env.ADDED")
	if !ok || added.OldValue != nil || added.NewValue != "[REDACTED]" {
		t.Fatalf("added dotenv value was not safely represented: %+v", diff.Fields)
	}
}

func TestComputeDiff_ConfigMapEnvNamedStructuredFilesKeepValues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    string
		oldVal string
		newVal string
		path   string
	}{
		{name: "yaml", key: "app.env.yaml", oldVal: "log:\n  level: info\nnet:\n  port: 8080", newVal: "log:\n  level: debug\nnet:\n  port: 8080", path: "data.app.env.yaml.log.level"},
		{name: "json", key: "app.env.json", oldVal: `{"log":{"level":"info"}}`, newVal: `{"log":{"level":"debug"}}`, path: "data.app.env.json.log.level"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.oldVal}}
			updated := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.newVal}}
			diff := ComputeDiff("ConfigMap", old, updated)
			if diff == nil {
				t.Fatal("ComputeDiff returned nil")
			}
			change, ok := findChangePath(diff.Fields, tc.path)
			if !ok || change.OldValue != "info" || change.NewValue != "debug" {
				t.Fatalf("env-named structured file lost its readable field delta: %+v", diff.Fields)
			}
		})
	}
}

func TestComputeDiff_ConfigMapPropertiesRedactsSensitiveAliases(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"application.properties": "db.pass=old-password\nencryption.key=old-key",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"application.properties": "db.pass=new-password\nencryption.key=new-key",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for _, path := range []string{
		"data.application.properties.db.pass",
		"data.application.properties.encryption.key",
	} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok {
			t.Fatalf("expected properties change at %q, got %+v", path, diff.Fields)
		}
		if change.OldValue != "[REDACTED]" || change.NewValue != "[REDACTED]" {
			t.Fatalf("sensitive property at %q was not redacted: %#v -> %#v", path, change.OldValue, change.NewValue)
		}
	}
}

func TestComputeDiff_ConfigMapPropertiesRedactsAllValues(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"application.properties": "cache_key=old-cache\nauth_mode=optional",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"application.properties": "cache_key=new-cache\nauth_mode=required",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for _, path := range []string{
		"data.application.properties.cache_key",
		"data.application.properties.auth_mode",
	} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok {
			t.Fatalf("expected properties change at %q, got %+v", path, diff.Fields)
		}
		if change.OldValue != "[REDACTED]" || change.NewValue != "[REDACTED]" {
			t.Fatalf("properties value at %q was not redacted: %#v -> %#v", path, change.OldValue, change.NewValue)
		}
	}
}

func TestComputeDiff_ConfigMapYAMLRedactsCompactSecretNames(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"app-config": "dbpass: old-db\nadminpwd: old-admin\nlicensekey: old-license\nsmtppw: old-smtp\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"app-config": "dbpass: new-db\nadminpwd: new-admin\nlicensekey: new-license\nsmtppw: new-smtp\n",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for _, field := range []string{"dbpass", "adminpwd", "licensekey", "smtppw"} {
		path := "data.app-config." + field
		change, ok := findChangePath(diff.Fields, path)
		if !ok {
			t.Fatalf("expected YAML change at %q, got %+v", path, diff.Fields)
		}
		if change.OldValue != "[REDACTED]" || change.NewValue != "[REDACTED]" {
			t.Fatalf("compact secret at %q was not redacted: %#v -> %#v", path, change.OldValue, change.NewValue)
		}
	}
}

func TestComputeDiff_ConfigMapYAMLRedactsStructuredSecretAliases(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"app.yaml": "keystore:\n  passphrase: old-passphrase\njwt:\n  key: old-jwt\nsentry_dsn: old-dsn\nslack_webhook: old-hook\ndatadog:\n  app_key: old-app-key\ntwilio_auth: old-auth\nauth_mode: optional\ncache_key: old-cache\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"app.yaml": "keystore:\n  passphrase: new-passphrase\njwt:\n  key: new-jwt\nsentry_dsn: new-dsn\nslack_webhook: new-hook\ndatadog:\n  app_key: new-app-key\ntwilio_auth: new-auth\nauth_mode: required\ncache_key: new-cache\n",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for _, path := range []string{
		"data.app.yaml.keystore.passphrase",
		"data.app.yaml.jwt.key",
		"data.app.yaml.sentry_dsn",
		"data.app.yaml.slack_webhook",
		"data.app.yaml.datadog.app_key",
		"data.app.yaml.twilio_auth",
	} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok {
			t.Fatalf("expected YAML change at %q, got %+v", path, diff.Fields)
		}
		if change.OldValue != "[REDACTED]" || change.NewValue != "[REDACTED]" {
			t.Fatalf("structured secret at %q was not redacted: %#v -> %#v", path, change.OldValue, change.NewValue)
		}
	}
	for path, want := range map[string][2]string{
		"data.app.yaml.auth_mode": {"optional", "required"},
		"data.app.yaml.cache_key": {"old-cache", "new-cache"},
	} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok || change.OldValue != want[0] || change.NewValue != want[1] {
			t.Fatalf("non-secret YAML value at %q was over-redacted: %+v", path, diff.Fields)
		}
	}
}

func TestComputeDiff_ConfigMapYAMLRedactsSecretArrays(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"app.yaml": "receivers:\n  webhook:\n    - old-hook\n  dsn:\n    - old-dsn\n  auth:\n    - old-auth\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"app.yaml": "receivers:\n  webhook:\n    - new-hook\n  dsn:\n    - new-dsn\n  auth:\n    - new-auth\n",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for _, path := range []string{
		"data.app.yaml.receivers.webhook[0]",
		"data.app.yaml.receivers.dsn[0]",
		"data.app.yaml.receivers.auth[0]",
	} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok || change.OldValue != "[REDACTED]" || change.NewValue != "[REDACTED]" {
			t.Fatalf("secret array value at %q was not redacted: %+v", path, diff.Fields)
		}
	}
}

func TestComputeDiff_ConfigMapYAMLKeepsNonSecretSuffixes(t *testing.T) {
	old := &corev1.ConfigMap{Data: map[string]string{
		"app-config": "compass: north\nbypass: disabled\nturkey: wild\n",
	}}
	updated := &corev1.ConfigMap{Data: map[string]string{
		"app-config": "compass: south\nbypass: enabled\nturkey: domestic\n",
	}}
	diff := ComputeDiff("ConfigMap", old, updated)
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	for path, want := range map[string][2]string{
		"data.app-config.compass": {"north", "south"},
		"data.app-config.bypass":  {"disabled", "enabled"},
		"data.app-config.turkey":  {"wild", "domestic"},
	} {
		change, ok := findChangePath(diff.Fields, path)
		if !ok {
			t.Fatalf("expected YAML change at %q, got %+v", path, diff.Fields)
		}
		if change.OldValue != want[0] || change.NewValue != want[1] {
			t.Fatalf("YAML value at %q = %#v -> %#v, want %q -> %q", path, change.OldValue, change.NewValue, want[0], want[1])
		}
	}
}

func TestComputeDiff_ConfigMapPropertiesFalsePositivesFallBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		key    string
		oldVal string
		newVal string
	}{
		{name: "shell script", key: ".env", oldVal: "#!/bin/sh\nFOO=old", newVal: "#!/bin/sh\nFOO=new"},
		{name: "export syntax", key: ".env", oldVal: "export FOO=old", newVal: "export FOO=new"},
		{name: "sql", key: "query.properties", oldVal: "select * from users = old", newVal: "select * from users = new"},
		{name: "prose", key: "notes.properties", oldVal: "this is prose = old", newVal: "this is prose = new"},
		{name: "ini deferred", key: "server.ini", oldVal: "[server]\nport=80", newVal: "[server]\nport=81"},
		{name: "toml deferred", key: "server.toml", oldVal: "title = old\n[server]", newVal: "title = new\n[server]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.oldVal}}
			updated := &corev1.ConfigMap{Data: map[string]string{tc.key: tc.newVal}}
			assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", tc.key)
		})
	}
}

func TestComputeDiff_ConfigMapStructuredCapsFallBack(t *testing.T) {
	t.Run("value bytes", func(t *testing.T) {
		oldVal := `{"payload":"` + strings.Repeat("a", configMapStructuredValueBytes) + `"}`
		newVal := `{"payload":"` + strings.Repeat("b", configMapStructuredValueBytes) + `"}`
		old := &corev1.ConfigMap{Data: map[string]string{"large.json": oldVal}}
		updated := &corev1.ConfigMap{Data: map[string]string{"large.json": newVal}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "large.json")
	})

	t.Run("field count", func(t *testing.T) {
		oldFields := make(map[string]string, configMapStructuredFieldCap+1)
		newFields := make(map[string]string, configMapStructuredFieldCap+1)
		for i := 0; i <= configMapStructuredFieldCap; i++ {
			key := fmt.Sprintf("field-%02d", i)
			oldFields[key] = "old"
			newFields[key] = "new"
		}
		oldVal, err := json.Marshal(oldFields)
		if err != nil {
			t.Fatal(err)
		}
		newVal, err := json.Marshal(newFields)
		if err != nil {
			t.Fatal(err)
		}
		old := &corev1.ConfigMap{Data: map[string]string{"wide.json": string(oldVal)}}
		updated := &corev1.ConfigMap{Data: map[string]string{"wide.json": string(newVal)}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "wide.json")
	})

	t.Run("node count", func(t *testing.T) {
		oldItems := make([]string, configMapStructuredNodeCap+1)
		newItems := make([]string, configMapStructuredNodeCap+1)
		for i := range oldItems {
			oldItems[i] = "same"
			newItems[i] = "same"
		}
		newItems[len(newItems)-1] = "changed"
		oldVal, err := json.Marshal(map[string]any{"items": oldItems})
		if err != nil {
			t.Fatal(err)
		}
		newVal, err := json.Marshal(map[string]any{"items": newItems})
		if err != nil {
			t.Fatal(err)
		}
		old := &corev1.ConfigMap{Data: map[string]string{"large-tree.json": string(oldVal)}}
		updated := &corev1.ConfigMap{Data: map[string]string{"large-tree.json": string(newVal)}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "large-tree.json")
	})

	t.Run("depth", func(t *testing.T) {
		oldVal := `"old"`
		newVal := `"new"`
		for range configMapStructuredDepthCap + 1 {
			oldVal = `{"nested":` + oldVal + `}`
			newVal = `{"nested":` + newVal + `}`
		}
		old := &corev1.ConfigMap{Data: map[string]string{"deep.json": oldVal}}
		updated := &corev1.ConfigMap{Data: map[string]string{"deep.json": newVal}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "deep.json")
	})

	t.Run("added subtree field count", func(t *testing.T) {
		added := make(map[string]any, configMapStructuredFieldCap+1)
		for i := 0; i <= configMapStructuredFieldCap; i++ {
			added[fmt.Sprintf("field-%02d", i)] = "value"
		}
		oldVal := `{"stable":true}`
		newVal, err := json.Marshal(map[string]any{"stable": true, "added": added})
		if err != nil {
			t.Fatal(err)
		}
		old := &corev1.ConfigMap{Data: map[string]string{"added-wide.json": oldVal}}
		updated := &corev1.ConfigMap{Data: map[string]string{"added-wide.json": string(newVal)}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "added-wide.json")
	})

	t.Run("added subtree node count", func(t *testing.T) {
		oldVal := `{"stable":true}`
		newVal, err := json.Marshal(map[string]any{"stable": true, "added": make([]string, configMapStructuredNodeCap+1)})
		if err != nil {
			t.Fatal(err)
		}
		old := &corev1.ConfigMap{Data: map[string]string{"added-large-tree.json": oldVal}}
		updated := &corev1.ConfigMap{Data: map[string]string{"added-large-tree.json": string(newVal)}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "added-large-tree.json")
	})

	t.Run("added subtree depth", func(t *testing.T) {
		added := any("value")
		for range configMapStructuredDepthCap + 1 {
			added = map[string]any{"nested": added}
		}
		oldVal := `{"stable":true}`
		newVal, err := json.Marshal(map[string]any{"stable": true, "added": added})
		if err != nil {
			t.Fatal(err)
		}
		old := &corev1.ConfigMap{Data: map[string]string{"added-deep.json": oldVal}}
		updated := &corev1.ConfigMap{Data: map[string]string{"added-deep.json": string(newVal)}}
		assertKeyOnlyDiff(t, ComputeDiff("ConfigMap", old, updated), "data (modified keys)", "added-deep.json")
	})
}

func TestComputeDiff_ConfigMapStructuredFieldCapSpansModifiedKeys(t *testing.T) {
	oldData := map[string]string{}
	newData := map[string]string{}
	for _, configKey := range []string{"a.json", "b.json"} {
		oldFields := make(map[string]string, 30)
		newFields := make(map[string]string, 30)
		for i := range 30 {
			field := fmt.Sprintf("field-%02d", i)
			oldFields[field] = "old"
			newFields[field] = "new"
		}
		oldVal, err := json.Marshal(oldFields)
		if err != nil {
			t.Fatal(err)
		}
		newVal, err := json.Marshal(newFields)
		if err != nil {
			t.Fatal(err)
		}
		oldData[configKey] = string(oldVal)
		newData[configKey] = string(newVal)
	}

	diff := ComputeDiff("ConfigMap", &corev1.ConfigMap{Data: oldData}, &corev1.ConfigMap{Data: newData})
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	if len(diff.Fields) > configMapStructuredFieldCap+1 {
		t.Fatalf("aggregate structured field cap exceeded: %d fields", len(diff.Fields))
	}
	fallback, ok := findChangePath(diff.Fields, "data (modified keys)")
	if !ok || !reflect.DeepEqual(fallback.OldValue, []string{"b.json"}) || !reflect.DeepEqual(fallback.NewValue, []string{"b.json"}) {
		t.Fatalf("expected the over-budget key to fall back, got %+v", diff.Fields)
	}
	for _, field := range diff.Fields {
		if strings.HasPrefix(field.Path, "data.b.json.") {
			t.Fatalf("over-budget key also emitted a structured field: %+v", field)
		}
	}
}

func TestComputeDiff_ConfigMapBinaryData_ReportsModifiedKeyOnly(t *testing.T) {
	old := &corev1.ConfigMap{BinaryData: map[string][]byte{"bundle": []byte("old-binary-value")}}
	updated := &corev1.ConfigMap{BinaryData: map[string][]byte{"bundle": []byte("new-binary-value")}}

	diff := ComputeDiff("ConfigMap", old, updated)
	assertKeyOnlyDiff(t, diff, "binaryData (modified keys)", "bundle")
}

func TestComputeDiff_SecretData_ReportsModifiedKeyOnly(t *testing.T) {
	old := &corev1.Secret{Data: map[string][]byte{"token": []byte("old-secret-value")}}
	updated := &corev1.Secret{Data: map[string][]byte{"token": []byte("new-secret-value")}}

	diff := ComputeDiff("Secret", old, updated)
	assertKeyOnlyDiff(t, diff, "data (modified keys)", "token")
}

func TestComputeDiff_SealedSecretEncryptedData_ReportsModifiedKeyOnly(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"encryptedData": map[string]any{"token": "old-ciphertext"}},
	}}
	updated := old.DeepCopy()
	_ = unstructured.SetNestedField(updated.Object, "new-ciphertext", "spec", "encryptedData", "token")

	diff := ComputeDiff("SealedSecret", old, updated)
	assertKeyOnlyDiff(t, diff, "spec.encryptedData (modified keys)", "token")
}

func TestComputeDiff_SealedSecretOtherSpecChange_RemainsVisible(t *testing.T) {
	old := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"generation": int64(1)},
		"spec":     map[string]any{"template": map[string]any{"type": "Opaque"}},
	}}
	updated := old.DeepCopy()
	updated.SetGeneration(2)
	_ = unstructured.SetNestedField(updated.Object, "kubernetes.io/tls", "spec", "template", "type")

	diff := ComputeDiff("SealedSecret", old, updated)
	if diff == nil || !diffHasPath(diff, "spec") {
		t.Fatalf("expected generic spec diff, got %+v", diff)
	}
}

func assertKeyOnlyDiff(t *testing.T, diff *DiffInfo, path, key string) {
	t.Helper()
	if diff == nil {
		t.Fatal("ComputeDiff returned nil")
	}
	if len(diff.Fields) != 1 {
		t.Fatalf("expected exactly one key-only field for %q, got %+v", key, diff.Fields)
	}
	for _, field := range diff.Fields {
		if field.Path != path {
			continue
		}
		oldKeys, oldOK := field.OldValue.([]string)
		newKeys, newOK := field.NewValue.([]string)
		if !oldOK || !newOK || len(oldKeys) != 1 || len(newKeys) != 1 || oldKeys[0] != key || newKeys[0] != key {
			t.Fatalf("expected key-only diff for %q, got %#v", key, field)
		}
		return
	}
	t.Fatalf("expected %q diff, got %+v", path, diff.Fields)
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
