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

// classifyTimelineChangeHealth labels one recorded change. The canonical
// classifier judges standing state, so it holds back on a fresh failure (a
// readiness grace, a restart threshold); a timeline row instead labels the
// moment it records, and a Pod whose container just exited with an error, or
// that just lost readiness, is not healthy at that moment.
func classifyTimelineChangeHealth(kind string, oldObj, newObj any, now time.Time) timeline.HealthState {
	pod, ok := newObj.(*corev1.Pod)
	if kind != "Pod" || !ok {
		return classifyTimelineHealth(kind, newObj, now)
	}
	level := health.PodDisplayLevel(pod, now)
	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
		return levelToTimeline(level)
	}
	completing := false
	for _, cs := range pod.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil {
			if t.ExitCode != 0 {
				level = health.WorseOf(level, health.LevelUnhealthy)
			} else if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways {
				// Under restartPolicy Always a clean exit is a restart, not completion.
				completing = true
			}
		}
	}
	// A container finishing cleanly also drops readiness; that is completion.
	// Lost readiness stays degraded on every later row until it returns, rather
	// than reading healthy again inside the standing-state grace.
	if !completing && !timelinePodReady(pod) {
		if old, ok := oldObj.(*corev1.Pod); (ok && old != pod && timelinePodReady(old)) || timelinePodLostReadiness(pod) {
			level = health.WorseOf(level, health.LevelDegraded)
		}
	}
	return levelToTimeline(level)
}

// timelinePodLostReadiness reports readiness that went false after a container
// had started, in its current run or the one before a restart: lost, not
// still to come at startup.
func timelinePodLostReadiness(p *corev1.Pod) bool {
	var since time.Time
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionFalse {
			since = c.LastTransitionTime.Time
		}
	}
	if since.IsZero() {
		return false
	}
	for _, cs := range p.Status.ContainerStatuses {
		if r := cs.State.Running; r != nil && !r.StartedAt.IsZero() && r.StartedAt.Time.Before(since) {
			return true
		}
		if t := cs.LastTerminationState.Terminated; t != nil && !t.StartedAt.IsZero() && t.StartedAt.Time.Before(since) {
			return true
		}
	}
	return false
}

func timelinePodReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
