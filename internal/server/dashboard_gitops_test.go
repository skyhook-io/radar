package server

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readyContainer / waitingContainer / pendingPod build minimal Pod
// fixtures focused on the fields summarizeControllerForDashboard
// reads — phase, ready flag, and waiting-state reason. Anything not
// touched by the function is omitted to keep the test surface small.
func readyContainer() corev1.ContainerStatus {
	return corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

func waitingContainer(reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Ready: false,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
	}
}

func pod(name string, phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "argocd"},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: statuses,
		},
	}
}

// TestSummarizeControllerForDashboard pins the per-pod aggregation
// rules. These mappings drive the home dashboard tone (green/amber/red),
// so silently changing them changes user-perceived severity.
func TestSummarizeControllerForDashboard(t *testing.T) {
	probe := gitopsControllerProbe{Name: "argocd-application-controller", Tool: ctrlToolArgoCD, Namespace: "argocd"}

	tests := []struct {
		name        string
		pods        []*corev1.Pod
		wantStatus  string
		wantReady   int
		wantTotal   int
		wantCrash   string
	}{
		{
			name:       "single ready pod is healthy",
			pods:       []*corev1.Pod{pod("p1", corev1.PodRunning, readyContainer())},
			wantStatus: "healthy",
			wantReady:  1,
			wantTotal:  1,
		},
		{
			name:       "two ready pods is healthy (HA Argo)",
			pods:       []*corev1.Pod{pod("p1", corev1.PodRunning, readyContainer()), pod("p2", corev1.PodRunning, readyContainer())},
			wantStatus: "healthy",
			wantReady:  2,
			wantTotal:  2,
		},
		{
			name:       "one ready, one not is degraded",
			pods:       []*corev1.Pod{pod("p1", corev1.PodRunning, readyContainer()), pod("p2", corev1.PodRunning, corev1.ContainerStatus{Ready: false})},
			wantStatus: "degraded",
			wantReady:  1,
			wantTotal:  2,
		},
		{
			name:       "any crashloop pod dominates as crashing",
			pods:       []*corev1.Pod{pod("p1", corev1.PodRunning, readyContainer()), pod("p2", corev1.PodRunning, waitingContainer("CrashLoopBackOff"))},
			wantStatus: "crashing",
			wantReady:  1,
			wantTotal:  2,
			wantCrash:  "CrashLoopBackOff",
		},
		{
			name:       "Error reason also surfaces as crashing",
			pods:       []*corev1.Pod{pod("p1", corev1.PodRunning, waitingContainer("Error"))},
			wantStatus: "crashing",
			wantReady:  0,
			wantTotal:  1,
			wantCrash:  "Error",
		},
		{
			name:       "all pods Pending and zero Ready is pending",
			pods:       []*corev1.Pod{pod("p1", corev1.PodPending), pod("p2", corev1.PodPending)},
			wantStatus: "pending",
			wantReady:  0,
			wantTotal:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeControllerForDashboard(probe, tt.pods)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Ready != tt.wantReady {
				t.Errorf("Ready = %d, want %d", got.Ready, tt.wantReady)
			}
			if got.Total != tt.wantTotal {
				t.Errorf("Total = %d, want %d", got.Total, tt.wantTotal)
			}
			if got.CrashReason != tt.wantCrash {
				t.Errorf("CrashReason = %q, want %q", got.CrashReason, tt.wantCrash)
			}
		})
	}
}

// TestAggregateControllerStatus pins the worst-case-wins logic. The
// aggregate severity drives the card's overall tone — getting this
// wrong silently downgrades perceived severity on the home dashboard,
// so the boundaries deserve a pinned test.
func TestAggregateControllerStatus(t *testing.T) {
	tests := []struct {
		name string
		in   []DashboardGitOpsController
		want string
	}{
		{"all healthy", []DashboardGitOpsController{{Status: ctrlStatusHealthy}, {Status: ctrlStatusHealthy}}, ctrlStatusHealthy},
		{"one degraded → degraded", []DashboardGitOpsController{{Status: ctrlStatusHealthy}, {Status: ctrlStatusDegraded}}, ctrlStatusDegraded},
		{"pending normalizes to degraded at aggregate", []DashboardGitOpsController{{Status: ctrlStatusHealthy}, {Status: ctrlStatusPending}}, ctrlStatusDegraded},
		{"one crashing → crashing", []DashboardGitOpsController{{Status: ctrlStatusHealthy}, {Status: ctrlStatusCrashing}}, ctrlStatusCrashing},
		{"crashing wins over degraded", []DashboardGitOpsController{{Status: ctrlStatusDegraded}, {Status: ctrlStatusCrashing}}, ctrlStatusCrashing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aggregateControllerStatus(tt.in); got != tt.want {
				t.Errorf("aggregateControllerStatus = %q, want %q", got, tt.want)
			}
		})
	}
}
