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

func TestBuildDashboard_CrashLoopCurrentStateControlsHealthBucket(t *testing.T) {
	defer k8s.ResetTestState()

	now := time.Now()
	crash := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason:     "Error",
		ExitCode:   1,
		FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
	}}
	recovered := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "recovered", Namespace: "prod"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:                 "app",
				Ready:                true,
				RestartCount:         2,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-time.Minute))}},
				LastTerminationState: crash,
			}},
		},
	}
	down := recovered.DeepCopy()
	down.Name = "down"
	down.Status.ContainerStatuses[0].Ready = false
	down.Status.ContainerStatuses[0].State = corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
	}

	if err := k8s.InitTestResourceCache(fake.NewClientset(recovered, down)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	var dashboard mcpDashboard
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dashboard = buildDashboard(context.Background(), k8s.GetResourceCache(), "prod", false, false)
		if dashboard.ResourceCounts["pods"] == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dashboard.Health.WarningPods != 1 || dashboard.Health.ErrorPods != 1 {
		t.Fatalf("health = %+v, want one warning recovered pod and one error down pod", dashboard.Health)
	}
	for _, problem := range dashboard.Problems {
		if problem.Name == "recovered" && problem.Severity == "critical" {
			t.Fatalf("recovered pod remained a critical MCP problem: %+v", problem)
		}
	}
}
