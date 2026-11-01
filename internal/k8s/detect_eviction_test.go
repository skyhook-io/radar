package k8s

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The shape of a real limit eviction (EKS 1.34): no DisruptionTarget, the
// container killed with exit 137 and listed as Error.
func limitEvictedPod(killedAt time.Time) *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{NodeName: "ip-10-0-17-33.ec2.internal"},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed, Reason: "Evicted",
			Message: "Pod ephemeral local storage usage exceeds the total limit of containers 2Mi. ",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodFailed",
				LastTransitionTime: metav1.NewTime(killedAt.Add(-time.Hour))}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "Error", ExitCode: 137, FinishedAt: metav1.NewTime(killedAt)}}}},
		},
	}
}

func TestEvictedPodProblem(t *testing.T) {
	killedAt := time.Date(2026, 11, 1, 0, 57, 23, 0, time.UTC)
	pod := limitEvictedPod(killedAt)

	got := evictedPodProblem(pod, "Deployment")
	if got.severity != "high" {
		t.Errorf("severity = %q, want high for a Deployment's pod", got.severity)
	}
	if want := "Evicted by the kubelet on node ip-10-0-17-33.ec2.internal: Pod ephemeral local storage usage exceeds the total limit of containers 2Mi."; got.message != want {
		t.Errorf("message = %q, want %q", got.message, want)
	}
	if got.cause != "The kubelet evicted this pod because it exceeded its own ephemeral-storage limit; the node itself was not under pressure." {
		t.Errorf("cause = %q", got.cause)
	}
	// Ready=False kept an older timestamp; the container kill is the eviction.
	if !got.onset.Equal(killedAt) {
		t.Errorf("onset = %v, want the container kill time %v", got.onset, killedAt)
	}

	for _, owner := range []string{"", "Workflow"} {
		if sev := evictedPodProblem(pod, owner).severity; sev != "critical" {
			t.Errorf("owner %q: severity = %q, want critical (nothing known to replace it)", owner, sev)
		}
	}
}
