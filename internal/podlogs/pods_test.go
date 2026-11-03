package podlogs

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildPodInfoMarksNativeSidecars(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "proxy", RestartPolicy: &always}, {Name: "wait-gcs-ready"}},
			Containers:     []corev1.Container{{Name: "ray-worker"}},
		},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{Name: "proxy"}, {Name: "wait-gcs-ready"}},
			ContainerStatuses:     []corev1.ContainerStatus{{Name: "ray-worker"}},
		},
	}
	got := map[string]ContainerInfo{}
	for _, status := range BuildPodInfo(pod, time.Now()).ContainerStatuses {
		got[status.Name] = status
	}
	if !got["proxy"].Init || !got["proxy"].Sidecar {
		t.Fatalf("proxy = %+v, want an init sidecar", got["proxy"])
	}
	if !got["wait-gcs-ready"].Init || got["wait-gcs-ready"].Sidecar {
		t.Fatalf("wait-gcs-ready = %+v, want a regular init container", got["wait-gcs-ready"])
	}
	if got["ray-worker"].Init || got["ray-worker"].Sidecar {
		t.Fatalf("ray-worker = %+v, want an application container", got["ray-worker"])
	}
}
