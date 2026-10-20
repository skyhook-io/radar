package k8score

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestDefaultExecContainer pins the container-selection precedence: a valid
// kubectl.kubernetes.io/default-container annotation wins, otherwise the first
// non-mesh app container. This is what keeps mesh-injected pods (sidecar at containers[0])
// from execing into a shell-less proxy.
func TestDefaultExecContainer(t *testing.T) {
	podWith := func(annotation string, containers ...string) *corev1.Pod {
		pod := &corev1.Pod{}
		if annotation != "" {
			pod.Annotations = map[string]string{DefaultContainerAnnotation: annotation}
		}
		for _, name := range containers {
			pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: name})
		}
		return pod
	}

	tests := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"annotation honored over first container", podWith("app", "istio-proxy", "app"), "app"},
		{"no annotation falls back to first", podWith("", "istio-proxy", "app"), "app"},
		{"annotation naming missing container ignored", podWith("ghost", "istio-proxy", "app"), "app"},
		{"mesh-only pod has no default", podWith("", "istio-proxy", "linkerd-proxy", "consul-dataplane"), ""},
		{"annotation cannot select mesh by default", podWith("istio-proxy", "istio-proxy", "app"), "app"},
		{"linkerd skipped", podWith("", "linkerd-proxy", "app"), "app"},
		{"consul skipped", podWith("", "consul-dataplane", "app"), "app"},
		{"single container", podWith("", "app"), "app"},
		{"no containers", podWith(""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultExecContainer(tt.pod); got != tt.want {
				t.Errorf("DefaultExecContainer() = %q, want %q", got, tt.want)
			}
		})
	}
}
