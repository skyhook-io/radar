package k8s

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/timeline"
)

func TestClassifyTimelineHealthPod(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		pod  *corev1.Pod
		want timeline.HealthState
	}{
		{
			name: "succeeded pod is neutral (completed — intentional, sky span)",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			want: timeline.HealthNeutral,
		},
		{
			// The reported bug: a Job pod completing — phase still Running, container
			// Terminated exit 0, Ready flipped False — must NOT read as degraded.
			name: "completing pod (terminated exit 0, ready false) is healthy",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{
						Name:  "main",
						Ready: false,
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0}},
					}},
				},
			},
			want: timeline.HealthHealthy,
		},
		{
			name: "failed pod is unhealthy",
			pod:  &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			want: timeline.HealthUnhealthy,
		},
		{
			// Unschedulable is flagged immediately (no young-Pending grace) so the
			// timeline doesn't miss what the scheduling detector already surfaces.
			name: "unschedulable pending pod is degraded",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:      corev1.PodPending,
				Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable}},
			}},
			want: timeline.HealthDegraded,
		},
		{
			name: "stuck-terminating pod is degraded",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: now.Add(-11 * time.Minute)}},
				Status: corev1.PodStatus{
					Phase:             corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
				},
			},
			want: timeline.HealthDegraded,
		},
		{
			name: "running and ready is healthy",
			pod: &corev1.Pod{Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
			}},
			want: timeline.HealthHealthy,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTimelineHealth("Pod", tc.pod, now); got != tc.want {
				t.Errorf("classifyTimelineHealth = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyTimelineHealthWorkloads(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		kind string
		obj  any
		want timeline.HealthState
	}{
		{
			name: "deployment scaled to zero is neutral (intentional — sky span)",
			kind: "Deployment",
			obj:  &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr32(0)}},
			want: timeline.HealthNeutral,
		},
		{
			name: "daemonset matching no nodes (0/0) is neutral (intentional — sky span)",
			kind: "DaemonSet",
			obj:  &appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 0, NumberReady: 0}},
			want: timeline.HealthNeutral,
		},
		{
			name: "deployment fully ready is healthy",
			kind: "Deployment",
			obj:  &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr32(2)}, Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 2}},
			want: timeline.HealthHealthy,
		},
		{
			name: "deployment partially ready is degraded",
			kind: "Deployment",
			obj:  &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr32(2)}, Status: appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1}},
			want: timeline.HealthDegraded,
		},
		{
			name: "deployment none ready is unhealthy",
			kind: "Deployment",
			obj:  &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr32(2)}, Status: appsv1.DeploymentStatus{ReadyReplicas: 0}},
			want: timeline.HealthUnhealthy,
		},
		{
			name: "failed job is unhealthy (batch kinds now classified)",
			kind: "Job",
			obj:  &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}}},
			want: timeline.HealthUnhealthy,
		},
		{
			name: "lost pvc is unhealthy",
			kind: "PersistentVolumeClaim",
			obj:  &corev1.PersistentVolumeClaim{Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimLost}},
			want: timeline.HealthUnhealthy,
		},
		{
			name: "unknown kind is unknown",
			kind: "ConfigMap",
			obj:  &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}},
			want: timeline.HealthUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTimelineHealth(tc.kind, tc.obj, now); got != tc.want {
				t.Errorf("classifyTimelineHealth(%s) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}
