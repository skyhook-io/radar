package health

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptr32(v int32) *int32 { return &v }
func ptrBool(b bool) *bool { return &b }

func TestWorkloadGoldenVectors(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		obj        any
		wantLevel  Level
		wantReason string
	}{
		// Deployments (require available).
		{"deployment fully ready", &appsv1.Deployment{
			Spec:   appsv1.DeploymentSpec{Replicas: ptr32(3)},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 3, AvailableReplicas: 3},
		}, LevelHealthy, ""},
		{"deployment scaled to zero is neutral", &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{Replicas: ptr32(0)},
		}, LevelNeutral, "ScaledToZero"},
		{"deployment partially ready is degraded", &appsv1.Deployment{
			Spec:   appsv1.DeploymentSpec{Replicas: ptr32(3)},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
		}, LevelDegraded, "PartiallyReady"},
		{"deployment none ready is unhealthy", &appsv1.Deployment{
			Spec:   appsv1.DeploymentSpec{Replicas: ptr32(2)},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 0},
		}, LevelUnhealthy, "NoneReady"},
		{"deployment ready-but-not-available is degraded", &appsv1.Deployment{
			Spec:   appsv1.DeploymentSpec{Replicas: ptr32(2)},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 1},
		}, LevelDegraded, "PartiallyReady"},

		// StatefulSet / ReplicaSet (no availability requirement).
		{"statefulset fully ready", &appsv1.StatefulSet{
			Spec:   appsv1.StatefulSetSpec{Replicas: ptr32(2)},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 2},
		}, LevelHealthy, ""},
		{"replicaset scaled to zero is neutral", &appsv1.ReplicaSet{
			Spec: appsv1.ReplicaSetSpec{Replicas: ptr32(0)},
		}, LevelNeutral, "ScaledToZero"},

		// DaemonSet (desired from DesiredNumberScheduled).
		{"daemonset all ready", &appsv1.DaemonSet{
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 4, NumberReady: 4},
		}, LevelHealthy, ""},
		{"daemonset no matching nodes is neutral", &appsv1.DaemonSet{
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 0},
		}, LevelNeutral, "ScaledToZero"},
		{"daemonset partially ready is degraded", &appsv1.DaemonSet{
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 4, NumberReady: 2},
		}, LevelDegraded, "PartiallyReady"},

		// Jobs.
		{"job complete is neutral", &batchv1.Job{
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
		}, LevelNeutral, "Completed"},
		{"job failed is unhealthy", &batchv1.Job{
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
		}, LevelUnhealthy, "Failed"},
		{"job suspended is neutral", &batchv1.Job{
			Spec: batchv1.JobSpec{Suspend: ptrBool(true)},
		}, LevelNeutral, "Suspended"},
		{"suspended job with terminal failure is still unhealthy", &batchv1.Job{
			Spec:   batchv1.JobSpec{Suspend: ptrBool(true)},
			Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}},
		}, LevelUnhealthy, "Failed"},
		{"job running is neutral", &batchv1.Job{
			Status: batchv1.JobStatus{Active: 1},
		}, LevelNeutral, "Running"},
		{"job succeeded count meets completions is neutral", &batchv1.Job{
			Spec:   batchv1.JobSpec{Completions: ptr32(1)},
			Status: batchv1.JobStatus{Succeeded: 1},
		}, LevelNeutral, "Completed"},
		{"explicit zero-completions job is complete (K8s semantics)", &batchv1.Job{
			Spec:   batchv1.JobSpec{Completions: ptr32(0)},
			Status: batchv1.JobStatus{Succeeded: 0},
		}, LevelNeutral, "Completed"},
		{"job failed with nothing active/succeeded is unhealthy", &batchv1.Job{
			Status: batchv1.JobStatus{Failed: 3},
		}, LevelUnhealthy, "Failed"},

		// CronJobs.
		{"cronjob suspended is neutral", &batchv1.CronJob{
			Spec: batchv1.CronJobSpec{Suspend: ptrBool(true), Schedule: "0 * * * *"},
		}, LevelNeutral, "Suspended"},
		{"cronjob recently run is healthy", &batchv1.CronJob{
			Spec:   batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
			Status: batchv1.CronJobStatus{LastScheduleTime: &metav1.Time{Time: now.Add(-2 * time.Minute)}},
		}, LevelHealthy, ""},
		{"cronjob stale past cadence is degraded", &batchv1.CronJob{
			Spec:   batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
			Status: batchv1.CronJobStatus{LastScheduleTime: &metav1.Time{Time: now.Add(-48 * time.Hour)}},
		}, LevelDegraded, "Stale"},
		{"cronjob freshly created, never run, is neutral", &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.Time{Time: now.Add(-1 * time.Minute)}},
			Spec:       batchv1.CronJobSpec{Schedule: "0 0 * * *"},
		}, LevelNeutral, "NeverRun"},
		{"cronjob old and never scheduled is degraded", &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.Time{Time: now.Add(-72 * time.Hour)}},
			Spec:       batchv1.CronJobSpec{Schedule: "0 0 * * *"},
		}, LevelDegraded, "NeverScheduled"},

		// PVCs.
		{"pvc bound is healthy", &corev1.PersistentVolumeClaim{
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		}, LevelHealthy, ""},
		{"pvc pending is neutral", &corev1.PersistentVolumeClaim{
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
		}, LevelNeutral, "Pending"},
		{"pvc lost is unhealthy", &corev1.PersistentVolumeClaim{
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimLost},
		}, LevelUnhealthy, "Lost"},

		// Unhandled kind falls back to unknown.
		{"unknown kind", &corev1.Pod{}, LevelUnknown, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Workload(c.obj, now)
			if got.Level != c.wantLevel {
				t.Errorf("Workload().Level = %q, want %q", got.Level, c.wantLevel)
			}
			if c.wantReason != "" && got.Reason != c.wantReason {
				t.Errorf("Workload().Reason = %q, want %q", got.Reason, c.wantReason)
			}
		})
	}
}

// TestWorkloadConvergenceGrace covers the window between a spec change and the
// controller acting on it. Without it, replicaVerdict graded purely on
// ready-vs-desired, so a Deployment scaled 3->10 read NoneReady/PartiallyReady
// the instant the spec changed — before the controller had created a single
// pod. That is controller lag, not an outage.
//
// The contract is Neutral, never Healthy: radar declines to call it broken
// without claiming it is fine.
func TestWorkloadConvergenceGrace(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	old := metav1.NewTime(now.Add(-30 * 24 * time.Hour))
	fresh := metav1.NewTime(now.Add(-30 * time.Second))

	tests := []struct {
		name      string
		dep       *appsv1.Deployment
		wantLevel Level
	}{
		{
			name: "long-lived deployment scaled up, controller has not observed the new spec",
			dep: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old, Generation: 7},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr32(10)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 6, // one behind: spec moved, controller hasn't
					ReadyReplicas:      3, AvailableReplicas: 3,
				},
			},
			wantLevel: LevelNeutral,
		},
		{
			// Age alone must NOT buy grace. A deploy that is broken on arrival is
			// still broken, and softening it is worst exactly when it matters
			// most: an earlier revision granted every workload a 5-minute window
			// and reported an unschedulable Deployment as neutral while that same
			// object carried a critical workload_degraded issue.
			name: "freshly created but controller caught up is graded normally",
			dep: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: fresh, Generation: 1},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr32(3)},
				Status:     appsv1.DeploymentStatus{ObservedGeneration: 1},
			},
			wantLevel: LevelUnhealthy,
		},
		{
			// Past the window with the controller caught up and still nothing
			// ready — this is a real outage and must not be softened.
			name: "settled deployment with zero ready is unhealthy",
			dep: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old, Generation: 7},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr32(3)},
				Status:     appsv1.DeploymentStatus{ObservedGeneration: 7},
			},
			wantLevel: LevelUnhealthy,
		},
		{
			// Converged: grace must never downgrade a genuinely healthy result.
			name: "settled and fully ready is healthy",
			dep: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old, Generation: 7},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr32(3)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 7, ReadyReplicas: 3, AvailableReplicas: 3,
				},
			},
			wantLevel: LevelHealthy,
		},
		{
			name: "surge replicas do not degrade serving health",
			dep: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: old, Generation: 8},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr32(1)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 8, ReadyReplicas: 2, AvailableReplicas: 1,
				},
			},
			wantLevel: LevelHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Workload(tt.dep, now).Level; got != tt.wantLevel {
				t.Errorf("Level = %v, want %v", got, tt.wantLevel)
			}
		})
	}
}
