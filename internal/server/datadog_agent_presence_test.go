package server

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRunningDatadogAgent(t *testing.T) {
	for _, tc := range []struct {
		name, label, container, image string
		running, want                 bool
	}{
		{"node agent", "agent", "agent", "registry.datadoghq.com/agent:7.84.6", true, true},
		{"mirror", "agent", "agent", "registry.example/team/agent@sha256:123", true, true},
		{"application tag alone", "", "agent", "registry.datadoghq.com/agent:7", true, false},
		{"tracer injection", "agent", "agent", "registry.datadoghq.com/dd-lib-java-init:1", true, false},
		{"operator", "operator", "operator", "registry.datadoghq.com/operator:1", true, false},
		{"starting", "agent", "agent", "registry.datadoghq.com/agent:7", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"agent.datadoghq.com/component": tc.label}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: tc.container, Image: tc.image}}}}
			if tc.running {
				p.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}
			}
			if got := runningDatadogAgent(&p); got != tc.want {
				t.Errorf("detected=%v, want %v", got, tc.want)
			}
			now := metav1.Now()
			p.DeletionTimestamp = &now
			if runningDatadogAgent(&p) {
				t.Fatal("detected terminating agent")
			}
		})
	}
}
