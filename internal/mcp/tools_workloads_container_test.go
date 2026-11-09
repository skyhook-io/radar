package mcp

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestWorkloadPodsHaveContainerAcceptsInitContainers(t *testing.T) {
	pods := []*corev1.Pod{{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "storage-initializer"}},
		Containers:     []corev1.Container{{Name: "kserve-container"}},
	}}}
	for name, want := range map[string]bool{"kserve-container": true, "storage-initializer": true, "missing": false} {
		if got := workloadPodsHaveContainer(pods, name); got != want {
			t.Errorf("%s: got %t, want %t", name, got, want)
		}
	}
}
