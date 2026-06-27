package k8s

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/health"
)

// classifyTimelineHealth maps a changed resource to the timeline HealthState
// using the shared canonical classifiers (health.Pod / health.Workload), instead
// of a separate copy that historically drifted. The timeline package can't reach
// this logic across the module boundary, so the caller — here, in internal/k8s —
// owns the classification and the timeline just stores the result.
func classifyTimelineHealth(kind string, obj any, now time.Time) timeline.HealthState {
	switch kind {
	case "Pod":
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return timeline.HealthUnknown
		}
		// The scheduler tried and failed to place this pod — the scheduling
		// detector flags it immediately (no age grace), so the timeline must too,
		// instead of treating a young Pending pod as healthy.
		if health.IsPodUnschedulable(pod) {
			return timeline.HealthDegraded
		}
		// A pod wedged in termination is a problem the badge + terminating detector
		// flag (10m threshold); the timeline must agree so it doesn't stay in the
		// Unhealthy filter's blind spot. health.Pod doesn't look at
		// deletionTimestamp, so check it here.
		if dt := pod.DeletionTimestamp; dt != nil && now.Sub(dt.Time) > 10*time.Minute {
			return timeline.HealthDegraded
		}
		return levelToTimeline(health.Pod(pod, now).Level)
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet":
		return levelToTimeline(health.Workload(obj, now).Level)
	}
	return timeline.HealthUnknown
}

// levelToTimeline projects a canonical health.Level onto the timeline's wire
// HealthState vocabulary. The timeline wire stays four-valued in this change, so
// neutral (intentional/lifecycle states — scaled-to-zero, completed) collapses to
// healthy, preserving the pre-consolidation behavior where those were recorded
// healthy. The dedicated neutral tier — and the frontend rendering for it — lands
// in the follow-up that owns the wire + UI together.
func levelToTimeline(l health.Level) timeline.HealthState {
	switch l {
	case health.LevelHealthy, health.LevelNeutral:
		return timeline.HealthHealthy
	case health.LevelDegraded:
		return timeline.HealthDegraded
	case health.LevelUnhealthy:
		return timeline.HealthUnhealthy
	default:
		return timeline.HealthUnknown
	}
}
