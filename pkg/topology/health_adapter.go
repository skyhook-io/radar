package topology

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/health"
)

// healthLevelToStatus projects a canonical health.Level onto the topology
// HealthStatus used for NODE COLORING. neutral (intentional / lifecycle states —
// completed, scaled-to-zero, suspended, pending PVC) renders as StatusNeutral
// (sky): calm, and distinct from unknown (gray, "can't determine").
func healthLevelToStatus(l health.Level) HealthStatus {
	switch l {
	case health.LevelHealthy:
		return StatusHealthy
	case health.LevelDegraded:
		return StatusDegraded
	case health.LevelUnhealthy:
		return StatusUnhealthy
	case health.LevelNeutral:
		return StatusNeutral
	default: // unknown
		return StatusUnknown
	}
}

// podSummaryStatus buckets a pod into the three PodSummary counters
// (Healthy/Degraded/Unhealthy). Unlike node coloring, a neutral/completed pod
// counts as healthy here — it is emphatically not unhealthy — while an unknown
// pod joins the unhealthy bucket (a pod we cannot classify is worth surfacing in
// the rollup, matching the pre-migration behavior where unknown fell through to
// unhealthy).
func podSummaryStatus(pod *corev1.Pod) HealthStatus {
	switch topoPodLevel(pod) {
	case health.LevelDegraded:
		return StatusDegraded
	case health.LevelUnhealthy, health.LevelUnknown:
		return StatusUnhealthy
	default: // healthy + neutral
		return StatusHealthy
	}
}

// topoPodLevel is the pod classification used by the topology display surfaces.
// It folds in the unschedulable signal the scheduling detector owns — the same
// way the timeline does — so a pod the scheduler failed to place reads degraded
// here instead of healthy (the canonical health.Pod leaves scheduling to its
// caller, since folding it there would shift the dashboard/MCP health counters).
func topoPodLevel(pod *corev1.Pod) health.Level {
	return health.PodDisplayLevel(pod, time.Now())
}
