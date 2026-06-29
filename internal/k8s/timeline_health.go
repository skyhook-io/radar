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
		// PodDisplayLevel folds the scheduling + stuck-terminating signals the
		// canonical classifier leaves to its caller, so the timeline surfaces them
		// (and stays consistent with topology + the AI summary).
		return levelToTimeline(health.PodDisplayLevel(pod, now))
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob", "PersistentVolumeClaim":
		return levelToTimeline(health.Workload(obj, now).Level)
	}
	return timeline.HealthUnknown
}

// levelToTimeline projects a canonical health.Level onto the timeline's wire
// HealthState vocabulary. neutral (intentional/lifecycle states — scaled-to-zero,
// completed, suspended) maps to the dedicated HealthNeutral so the timeline draws
// a sky span instead of a false-green healthy one.
func levelToTimeline(l health.Level) timeline.HealthState {
	switch l {
	case health.LevelHealthy:
		return timeline.HealthHealthy
	case health.LevelNeutral:
		return timeline.HealthNeutral
	case health.LevelDegraded:
		return timeline.HealthDegraded
	case health.LevelUnhealthy:
		return timeline.HealthUnhealthy
	default:
		return timeline.HealthUnknown
	}
}
