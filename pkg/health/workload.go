package health

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/cronsched"
)

// Workload classifies a controller / batch / storage object into a canonical
// Verdict. Kinds it doesn't handle return LevelUnknown so the caller can fall
// back. This subsumes the four divergent replica/job/pvc classifiers that lived
// in pkg/topology, pkg/packages, pkg/resourcecontext, and internal/k8s.
//
// The semantics align with the frontend's status badges (the most
// operator-considered surface): intentional/lifecycle states are neutral, not
// degraded or unknown. Notable reconciliations vs the old backend copies:
//   - desired==0 (scaled to zero) → neutral (was unknown/healthy, depending on copy)
//   - a running Job → neutral (in-progress, not "healthy"; was healthy)
//   - a Pending PVC → neutral (WaitForFirstConsumer is benign; topology said degraded)
func Workload(obj any, now time.Time) Verdict {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return replicaVerdict(specReplicas(o.Spec.Replicas), o.Status.ReadyReplicas, o.Status.AvailableReplicas, true)
	case *appsv1.ReplicaSet:
		return replicaVerdict(specReplicas(o.Spec.Replicas), o.Status.ReadyReplicas, 0, false)
	case *appsv1.StatefulSet:
		return replicaVerdict(specReplicas(o.Spec.Replicas), o.Status.ReadyReplicas, 0, false)
	case *appsv1.DaemonSet:
		// DesiredNumberScheduled 0 means the selector matches no nodes — benign,
		// nothing to run, not unhealthy.
		return replicaVerdict(o.Status.DesiredNumberScheduled, o.Status.NumberReady, 0, false)
	case *batchv1.Job:
		return jobVerdict(o)
	case *batchv1.CronJob:
		return cronJobVerdict(o, now)
	case *corev1.PersistentVolumeClaim:
		return pvcVerdict(o)
	}
	return Verdict{Level: LevelUnknown}
}

// replicaVerdict grades a replica-based workload. requireAvailable additionally
// demands available==desired (Deployment tracks availability; ReplicaSet /
// StatefulSet / DaemonSet don't expose it the same way).
func replicaVerdict(desired, ready, available int32, requireAvailable bool) Verdict {
	if desired == 0 {
		return Verdict{Level: LevelNeutral, Reason: "ScaledToZero"}
	}
	if ready == desired && (!requireAvailable || available == desired) {
		return Verdict{Level: LevelHealthy}
	}
	if ready > 0 {
		return Verdict{Level: LevelDegraded, Reason: "PartiallyReady"}
	}
	return Verdict{Level: LevelUnhealthy, Reason: "NoneReady"}
}

func jobVerdict(j *batchv1.Job) Verdict {
	// Terminal conditions dominate spec intent: a Job that has failed or completed
	// must not be masked by spec.suspend (a failed-then-suspended Job is still a
	// failure). Check the terminal conditions before the suspend branch.
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return Verdict{Level: LevelUnhealthy, Reason: "Failed"}
		}
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return Verdict{Level: LevelNeutral, Reason: "Completed"}
		}
	}
	if j.Spec.Suspend != nil && *j.Spec.Suspend {
		return Verdict{Level: LevelNeutral, Reason: "Suspended"}
	}
	if int(j.Status.Succeeded) >= jobDesiredCompletions(j) {
		return Verdict{Level: LevelNeutral, Reason: "Completed"}
	}
	// A failed batch with nothing active and nothing succeeded is a real failure.
	if j.Status.Failed > 0 && j.Status.Active == 0 && j.Status.Succeeded == 0 {
		return Verdict{Level: LevelUnhealthy, Reason: "Failed"}
	}
	// In-progress: running, not yet complete — intentional/transient, not "healthy".
	if j.Status.Active > 0 {
		return Verdict{Level: LevelNeutral, Reason: "Running"}
	}
	return Verdict{Level: LevelUnknown}
}

// jobDesiredCompletions is the number of successful completions a Job needs.
// Only a nil completions defaults to 1 (the K8s default for non-indexed Jobs); an
// explicit 0 is preserved — Kubernetes treats a zero-completion Job as already
// complete (the controller never schedules pods for it), so Succeeded>=0 reading
// complete is correct, not a false positive.
func jobDesiredCompletions(j *batchv1.Job) int {
	if j.Spec.Completions != nil {
		return int(*j.Spec.Completions)
	}
	return 1
}

func cronJobVerdict(cj *batchv1.CronJob, now time.Time) Verdict {
	if cj == nil {
		return Verdict{Level: LevelUnknown}
	}
	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		return Verdict{Level: LevelNeutral, Reason: "Suspended"}
	}
	// Never run yet: benign for a freshly-created CronJob, but one that has existed
	// longer than its cadence and still never fired is stuck (bad schedule / broken
	// controller) — degraded, matching the never-scheduled problem detector.
	if cj.Status.LastScheduleTime == nil {
		if now.Sub(cj.CreationTimestamp.Time) > cronsched.StaleThreshold(cj.Spec.Schedule) {
			return Verdict{Level: LevelDegraded, Reason: "NeverScheduled"}
		}
		return Verdict{Level: LevelNeutral, Reason: "NeverRun"}
	}
	// Cadence-aware staleness: a job that has missed its schedule by more than the
	// cadence-relative grace window is degraded.
	if now.Sub(cj.Status.LastScheduleTime.Time) > cronsched.StaleThreshold(cj.Spec.Schedule) {
		return Verdict{Level: LevelDegraded, Reason: "Stale"}
	}
	return Verdict{Level: LevelHealthy}
}

func pvcVerdict(pvc *corev1.PersistentVolumeClaim) Verdict {
	switch pvc.Status.Phase {
	case corev1.ClaimBound:
		return Verdict{Level: LevelHealthy}
	case corev1.ClaimPending:
		// WaitForFirstConsumer / provisioning is benign — not a problem to alarm on.
		return Verdict{Level: LevelNeutral, Reason: "Pending"}
	case corev1.ClaimLost:
		return Verdict{Level: LevelUnhealthy, Reason: "Lost"}
	}
	return Verdict{Level: LevelUnknown}
}

func specReplicas(r *int32) int32 {
	if r != nil {
		return *r
	}
	return 1
}
