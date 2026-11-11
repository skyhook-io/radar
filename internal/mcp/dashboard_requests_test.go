package mcp

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDashboardRequestTotals(t *testing.T) {
	requests := func(cpu, mem string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
		}}
	}
	podLevel := requests("200m", "128Mi")
	always := corev1.ContainerRestartPolicyAlways
	pods := []*corev1.Pod{
		// Pod-level budget only: the containers set nothing of their own.
		{Spec: corev1.PodSpec{Resources: &podLevel, Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}}},
		// Pod-level budget wins over the containers' own values.
		{Spec: corev1.PodSpec{Resources: &podLevel, Containers: []corev1.Container{{Name: "app", Resources: requests("100m", "64Mi")}}}},
		// Container-only, with a native sidecar the scheduler also reserves.
		{Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "app", Resources: requests("100m", "64Mi")}},
			InitContainers: []corev1.Container{{Name: "proxy", RestartPolicy: &always, Resources: requests("50m", "32Mi")}},
		}},
		// Finished pods reserve nothing.
		{Spec: corev1.PodSpec{Resources: &podLevel}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
	}
	cpu, mem := dashboardRequestTotals(pods)
	if want := int64(200 + 200 + 150); cpu != want {
		t.Errorf("cpu = %dm, want %dm", cpu, want)
	}
	if want := int64(128+128+96) * 1024 * 1024; mem != want {
		t.Errorf("memory = %d, want %d", mem, want)
	}
}
