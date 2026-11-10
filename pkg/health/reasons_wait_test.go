package health

import (
	corev1 "k8s.io/api/core/v1"
	"testing"
)

func TestPodBlockingReason(t *testing.T) {
	for _, tc := range []struct {
		reason string
		fatal  bool
	}{
		{"CrashLoopBackOff", true}, {"ImagePullBackOff", true}, {"ErrImagePull", true}, {"InvalidImageName", true},
		{"ImageInspectError", true}, {"CreateContainerConfigError", true}, {"CreateContainerError", true}, {"RunContainerError", true},
		{"PodInitializing", false}, {"ContainerCreating", false}, {"", false},
	} {
		for _, init := range []bool{false, true} {
			t.Run(tc.reason, func(t *testing.T) {
				pod := &corev1.Pod{}
				status := corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: tc.reason, Message: "kubelet detail"}}}
				if init {
					pod.Status.InitContainerStatuses = []corev1.ContainerStatus{status}
				} else {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{status}
				}
				container, reason, message := PodBlockingReason(pod)
				if tc.fatal {
					if container != "app" || reason != tc.reason || message != "kubelet detail" {
						t.Fatalf("%q %q %q", container, reason, message)
					}
				} else if container != "" || reason != "" || message != "" {
					t.Fatalf("transient state counted: %q %q %q", container, reason, message)
				}
			})
		}
	}
}
