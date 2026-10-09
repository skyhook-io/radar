package health

import (
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const NodeNetworkUnavailableGrace = 2 * time.Minute

const NodeRemovalWarningAfter = 10 * time.Minute
const NodeRemovalCriticalAfter = 30 * time.Minute

// NodeLifecycleState keeps observed readiness separate from an intentional removal.
type NodeLifecycleState struct {
	Label           string    `json:"label"`
	Level           Level     `json:"level"`
	Removing        bool      `json:"removing"`
	Actor           string    `json:"actor,omitempty"`
	StartedAt       time.Time `json:"startedAt,omitzero"`
	ReadinessFailed bool      `json:"readinessFailed"`
	Problems        []string  `json:"problems,omitempty"`
	Delayed         bool      `json:"delayed"`
}

func NodeLifecycle(node *corev1.Node, now time.Time) NodeLifecycleState {
	s := NodeLifecycleState{Label: "Unknown", Level: LevelUnknown}
	var ready *corev1.NodeCondition
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == corev1.NodeReady {
			ready = &node.Status.Conditions[i]
			break
		}
	}
	validTime := func(t time.Time) time.Time {
		if t.IsZero() || t.After(now) || (!node.CreationTimestamp.IsZero() && t.Before(node.CreationTimestamp.Time)) {
			return time.Time{}
		}
		return t
	}
	if node.DeletionTimestamp != nil {
		s.Removing = true
		s.StartedAt = validTime(node.DeletionTimestamp.Time)
	}
	candidate := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "DeletionCandidateOfClusterAutoscaler" && taint.Effect == corev1.TaintEffectPreferNoSchedule {
			candidate = true
		}
		if taint.Effect != corev1.TaintEffectNoSchedule {
			continue
		}
		var actor string
		switch taint.Key {
		case "ToBeDeletedByClusterAutoscaler":
			actor = "cluster autoscaler"
		case "karpenter.sh/disrupted":
			actor = "Karpenter"
		default:
			continue
		}
		s.Removing, s.Actor = true, actor
		// NoSchedule taints have no automatic timeAdded. Only CA records a
		// start time in its value; deletionTimestamp dates actual deletion.
		if taint.Key == "ToBeDeletedByClusterAutoscaler" {
			if seconds, err := strconv.ParseInt(taint.Value, 10, 64); err == nil && seconds > 0 {
				start := validTime(time.Unix(seconds, 0))
				if !start.IsZero() && (s.StartedAt.IsZero() || start.Before(s.StartedAt)) {
					s.StartedAt = start
				}
			}
		}
	}
	s.ReadinessFailed = ready != nil && ready.Status != corev1.ConditionTrue
	if s.Removing {
		s.Label, s.Level = "Removing", LevelNeutral
		if s.Actor != "" {
			s.Label += " (" + s.Actor + ")"
		}
		if !s.StartedAt.IsZero() {
			s.Delayed = now.Sub(s.StartedAt) >= NodeRemovalWarningAfter
		}
		// A removal requested to repair an existing outage does not explain it.
		if s.ReadinessFailed && ready != nil && !s.StartedAt.IsZero() && !ready.LastTransitionTime.IsZero() && !ready.LastTransitionTime.Time.Before(s.StartedAt) && !ready.LastTransitionTime.Time.After(now) {
			s.ReadinessFailed = false
		}
		if s.Delayed {
			s.Level = LevelDegraded
			if now.Sub(s.StartedAt) >= NodeRemovalCriticalAfter {
				s.Level = LevelUnhealthy
			}
		}
		if s.ReadinessFailed {
			s.Level = LevelUnhealthy
			s.Label += " · NotReady"
		}
	} else if s.ReadinessFailed {
		s.Label, s.Level = "NotReady", LevelUnhealthy
	} else if ready != nil && ready.Status == corev1.ConditionTrue {
		s.Label, s.Level = "Ready", LevelHealthy
		if node.Spec.Unschedulable {
			s.Label, s.Level = "Cordoned", LevelDegraded
		} else if candidate {
			s.Label, s.Level = "Scale-down candidate", LevelNeutral
		}
	}
	for _, cond := range node.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		name := ""
		switch cond.Type {
		case corev1.NodeMemoryPressure:
			name = "Memory pressure"
		case corev1.NodeDiskPressure:
			name = "Disk pressure"
		case corev1.NodePIDPressure:
			name = "PID pressure"
		case corev1.NodeNetworkUnavailable:
			if !cond.LastTransitionTime.IsZero() && now.Sub(cond.LastTransitionTime.Time) < NodeNetworkUnavailableGrace {
				continue
			}
			name = "Network unavailable"
		}
		if name != "" {
			s.Problems = append(s.Problems, string(cond.Type))
			s.Label += " · " + name
			if cond.Type == corev1.NodeNetworkUnavailable {
				s.Level = WorseOf(s.Level, LevelDegraded)
			} else {
				s.Level = LevelUnhealthy
			}
		}
	}
	return s
}
