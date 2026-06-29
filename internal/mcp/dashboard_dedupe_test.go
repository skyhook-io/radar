package mcp

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// TestBuildDashboard_PodMissingRefs_BothReasonsAppear pins the dedupe-key
// contract for the MCP dashboard: a Pod with multiple distinct missing-ref
// reasons (PVC + ConfigMap) must emit a row per reason, and a Pod already
// in a kubelet error state (CreateContainerConfigError) must NOT suppress
// its underlying root-cause missing-ref row.
//
// Without the (kind, ns, name, reason)-shaped dedupe key, the old logic
// keyed on identity alone and dropped the dangling-ref root cause exactly
// when the agent needed it most.
func TestBuildDashboard_PodMissingRefs_BothReasonsAppear(t *testing.T) {
	defer k8s.ResetTestState()

	ns := "prod"
	now := metav1.NewTime(time.Now().Add(-5 * time.Minute))

	// Pod A: references TWO missing things (PVC + ConfigMap). The kubelet
	// has reported Waiting=ContainerCreating but no specific config error
	// yet — agent needs BOTH missing refs surfaced as distinct rows so it
	// can fix both.
	podMultipleMissing := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "multi-missing", Namespace: ns, CreationTimestamp: now,
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{
				{Name: "data", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "missing-pvc"},
				}},
				{Name: "cfg", VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-cm"}},
				}},
			},
			Containers: []corev1.Container{{Name: "app"}},
		},
		// Pending phase — pods that won't schedule on a missing PVC stay
		// here without container statuses. ClassifyPodHealth returns
		// "warning" for Pending so this won't enter the pod-error loop —
		// missing-ref detection is the ONLY source of these rows.
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}

	// Pod B: kubelet reports CreateContainerConfigError because envFrom
	// references a missing Secret. The pod-error loop will add a Pod row
	// with reason="CreateContainerConfigError". DetectMissingRefs will
	// independently add a Pod row with reason="Missing Secret". The
	// dedupe key must let BOTH rows survive — the missing-Secret row is
	// the actual root cause the agent should act on.
	podKubeletErrorPlusMissingRef := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubelet-err", Namespace: ns, CreationTimestamp: now,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "app",
				EnvFrom: []corev1.EnvFromSource{{
					SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-secret"}},
				}},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CreateContainerConfigError"},
				},
			}},
		},
	}

	client := fake.NewClientset(podMultipleMissing, podKubeletErrorPlusMissingRef)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		t.Fatal("cache nil after init")
	}

	// Let informers settle so DetectMissingRefs sees both pods.
	deadline := time.Now().Add(2 * time.Second)
	var dashboard mcpDashboard
	for time.Now().Before(deadline) {
		dashboard = buildDashboard(context.Background(), cache, ns, false, false)
		if hasReason(dashboard.Problems, "multi-missing", "Missing PVC") &&
			hasReason(dashboard.Problems, "multi-missing", "Missing ConfigMap") &&
			hasReason(dashboard.Problems, "kubelet-err", "Missing Secret") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// (1) Pod with PVC + ConfigMap both missing must emit a row per missing
	// ref. The OLD identity-only dedupe key would have kept only the first.
	if !hasReason(dashboard.Problems, "multi-missing", "Missing PVC") {
		t.Errorf("Missing PVC row absent for multi-missing — dedupe dropped it; got %s", problemSummary(dashboard.Problems))
	}
	if !hasReason(dashboard.Problems, "multi-missing", "Missing ConfigMap") {
		t.Errorf("Missing ConfigMap row absent for multi-missing — dedupe dropped it; got %s", problemSummary(dashboard.Problems))
	}

	// (2) Pod with kubelet error + missing-ref MUST keep the missing-ref
	// row even when the kubelet row was added first. This is the bug the
	// reviewer specifically flagged on PR #755.
	if !hasReason(dashboard.Problems, "kubelet-err", "Missing Secret") {
		t.Errorf("Missing Secret row absent for kubelet-err — dedupe dropped the root-cause row; got %s", problemSummary(dashboard.Problems))
	}
}

func hasReason(problems []mcpProblem, name, reason string) bool {
	for _, p := range problems {
		if p.Name == name && p.Reason == reason {
			return true
		}
	}
	return false
}

func problemSummary(problems []mcpProblem) string {
	out := "["
	for i, p := range problems {
		if i > 0 {
			out += ", "
		}
		out += p.Kind + "/" + p.Namespace + "/" + p.Name + ":" + p.Reason
	}
	return out + "]"
}
