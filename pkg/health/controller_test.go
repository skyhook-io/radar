package health

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

// makePodWithStatus constructs a minimal Pod with the container statuses
// summarizeControllerHealth inspects. Phase + ContainerStatuses are the
// only fields the function reads.
func makePodWithStatus(phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod"},
		Status:     corev1.PodStatus{Phase: phase, ContainerStatuses: statuses},
	}
}

// TestSummarizeControllerHealth pins the exact wording of the
// finalizer-owner status string. The output is concatenated into the
// lifecycle Issue's Cause field surfaced to operators, so a copy-paste
// regression that changed "1 pod ready" to "1 pods ready" or "Healthy"
// to "healthy" is part of the user-visible contract — assertions are
// exact-match on purpose.
func TestSummarizeControllerHealth(t *testing.T) {
	ready := corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	notReady := corev1.ContainerStatus{Ready: false}
	crashing := corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	erroring := corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "Error"}}}

	tests := []struct {
		name string
		pods []*corev1.Pod
		want string
	}{
		{
			name: "single ready pod uses singular form",
			pods: []*corev1.Pod{makePodWithStatus(corev1.PodRunning, ready)},
			want: "argocd-application-controller is healthy (1 pod ready)",
		},
		{
			name: "two ready pods uses plural form",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodRunning, ready),
				makePodWithStatus(corev1.PodRunning, ready),
			},
			want: "argocd-application-controller is healthy (2 pods ready)",
		},
		{
			name: "any crashloop pod dominates aggregate",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodRunning, ready),
				makePodWithStatus(corev1.PodRunning, crashing),
			},
			want: "argocd-application-controller is CrashLoopBackOff (1/2 pods)",
		},
		{
			name: "Error reason surfaces alongside crashing count",
			pods: []*corev1.Pod{makePodWithStatus(corev1.PodRunning, erroring)},
			want: "argocd-application-controller is Error (1/1 pods)",
		},
		{
			name: "all pending and zero ready reports pending",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodPending),
				makePodWithStatus(corev1.PodPending),
			},
			want: "argocd-application-controller is pending start (2/2 pods Pending)",
		},
		{
			name: "partial ready falls back to degraded",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodRunning, ready),
				makePodWithStatus(corev1.PodRunning, notReady),
			},
			want: "argocd-application-controller is degraded (1/2 pods ready)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SummarizeControllerHealth("argocd-application-controller", tt.pods)
			if got != tt.want {
				t.Fatalf("summarizeControllerHealth =\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}
