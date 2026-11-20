package podlogs

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A pod held by a scheduling gate reads as a calm, explained state in the pod
// detail: neutral health, a "SchedulingGated" reason, and a message naming the
// gate — not a bare unexplained Pending.
func TestBuildPodInfo_SchedulingGatedIsExplained(t *testing.T) {
	now := time.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "held", Namespace: "prod",
			CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
		},
		Spec:   corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: "kueue.x-k8s.io/admission"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	info := BuildPodInfo(pod, now)
	if info.HealthLevel != "neutral" {
		t.Errorf("HealthLevel = %q, want neutral", info.HealthLevel)
	}
	if info.Reason != corev1.PodReasonSchedulingGated {
		t.Errorf("Reason = %q, want %q", info.Reason, corev1.PodReasonSchedulingGated)
	}
	if !strings.Contains(info.Message, "kueue.x-k8s.io/admission") {
		t.Errorf("Message = %q, want it to name the gate", info.Message)
	}
}

// When a gated pod is ALSO stuck terminating, the display floor escalates it and
// the explanation must describe the escalation, not keep the benign "SchedulingGated"
// reason — otherwise a wedged pod looks merely held.
func TestBuildPodInfo_StuckTerminatingOverridesGatedReason(t *testing.T) {
	now := time.Now()
	del := metav1.NewTime(now.Add(-30 * time.Minute))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "held-term", Namespace: "prod",
			CreationTimestamp: metav1.NewTime(now.Add(-40 * time.Minute)),
			DeletionTimestamp: &del,
			Finalizers:        []string{"example.com/cleanup"},
		},
		Spec:   corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: "kueue.x-k8s.io/admission"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
	info := BuildPodInfo(pod, now)
	if info.HealthLevel != "degraded" {
		t.Errorf("HealthLevel = %q, want degraded (stuck-terminating floor)", info.HealthLevel)
	}
	// The benign gated reason must be replaced by the escalation reason. For a pod
	// with no container fault that is the phase ("Pending") — the same reason a
	// non-gated stuck-terminating pod produces here (BuildPodInfo/PodProblemReason
	// has never emitted a dedicated "Terminating" reason; that general limitation is
	// out of scope). The point is only that "SchedulingGated" does not leak onto a
	// pod whose real state is stuck deletion.
	if info.Reason == corev1.PodReasonSchedulingGated {
		t.Errorf("Reason = %q, want the escalation reason, not the benign gated one", info.Reason)
	}
	if info.Reason != "Pending" {
		t.Errorf("Reason = %q, want %q (same as a non-gated stuck-terminating pod)", info.Reason, "Pending")
	}
}
