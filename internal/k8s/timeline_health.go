package k8s

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/timeline"
)

// classifyTimelineHealth maps a changed resource to the timeline HealthState
// using the SAME canonical classifiers the Problems panel and dashboards use
// (ClassifyPodHealth for Pods), instead of a separate copy. The timeline package
// can't reach this logic across the module boundary, so the caller — here, in
// internal/k8s alongside ClassifyPodHealth — owns the classification and the
// timeline just stores the result. This is what keeps a normally-completing Job
// pod (phase Succeeded, or Ready False mid-termination) from being recorded as
// degraded/Unhealthy the way the old naive duplicate did.
func classifyTimelineHealth(kind string, obj any, now time.Time) timeline.HealthState {
	switch kind {
	case "Pod":
		if pod, ok := obj.(*corev1.Pod); ok {
			// The scheduler tried and failed to place this pod — the scheduling
			// detector flags it immediately (no age grace), so the timeline must
			// too, instead of treating a young Pending pod as healthy.
			if IsPodUnschedulable(pod) {
				return timeline.HealthDegraded
			}
			// A pod wedged in termination is a problem the badge + terminating
			// detector flag (10m threshold); the timeline must agree so it doesn't
			// stay in the Unhealthy filter's blind spot. ClassifyPodHealth doesn't
			// look at deletionTimestamp, so check it here.
			if dt := pod.DeletionTimestamp; dt != nil && now.Sub(dt.Time) > 10*time.Minute {
				return timeline.HealthDegraded
			}
			return podHealthToTimeline(ClassifyPodHealth(pod, now))
		}
	case "Deployment":
		if dep, ok := obj.(*appsv1.Deployment); ok {
			return workloadReplicaHealth(specReplicas(dep.Spec.Replicas), dep.Status.ReadyReplicas, dep.Status.AvailableReplicas, true)
		}
	case "ReplicaSet":
		if rs, ok := obj.(*appsv1.ReplicaSet); ok {
			return workloadReplicaHealth(specReplicas(rs.Spec.Replicas), rs.Status.ReadyReplicas, 0, false)
		}
	case "StatefulSet":
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			return workloadReplicaHealth(specReplicas(sts.Spec.Replicas), sts.Status.ReadyReplicas, 0, false)
		}
	case "DaemonSet":
		if ds, ok := obj.(*appsv1.DaemonSet); ok {
			// A DaemonSet whose selector matches no nodes has DesiredNumberScheduled
			// 0 — benign (nothing to run), not unhealthy.
			return workloadReplicaHealth(ds.Status.DesiredNumberScheduled, ds.Status.NumberReady, 0, false)
		}
	}
	return timeline.HealthUnknown
}

// podHealthToTimeline maps ClassifyPodHealth's vocabulary (healthy/warning/error)
// onto the timeline HealthState vocabulary (healthy/degraded/unhealthy).
func podHealthToTimeline(h string) timeline.HealthState {
	switch h {
	case "healthy":
		return timeline.HealthHealthy
	case "warning":
		return timeline.HealthDegraded
	case "error":
		return timeline.HealthUnhealthy
	default:
		return timeline.HealthUnknown
	}
}

// workloadReplicaHealth grades a replica-based workload. desired 0 is an
// intentional scale-to-zero (healthy, not unhealthy); full readiness is healthy;
// some-but-not-all ready is degraded; none ready with replicas wanted is
// unhealthy. requireAvailable additionally demands available==desired (Deployment
// tracks availability; ReplicaSet/StatefulSet/DaemonSet don't expose it the same).
func workloadReplicaHealth(desired, ready, available int32, requireAvailable bool) timeline.HealthState {
	if desired == 0 {
		return timeline.HealthHealthy
	}
	if ready == desired && (!requireAvailable || available == desired) {
		return timeline.HealthHealthy
	}
	if ready > 0 {
		return timeline.HealthDegraded
	}
	return timeline.HealthUnhealthy
}

func specReplicas(r *int32) int32 {
	if r != nil {
		return *r
	}
	return 1
}
