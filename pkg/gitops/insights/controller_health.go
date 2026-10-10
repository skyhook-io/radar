package insights

import (
	"fmt"
	corev1 "k8s.io/api/core/v1"
)

type ControllerPodHealth struct {
	Ready       int
	Total       int
	Crashing    int
	Pending     int
	CrashReason string
}

func SummarizeControllerPods(pods []*corev1.Pod) ControllerPodHealth {
	out := ControllerPodHealth{Total: len(pods)}
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && (cs.State.Waiting.Reason == "CrashLoopBackOff" || cs.State.Waiting.Reason == "Error") {
				out.Crashing++
				if out.CrashReason == "" {
					out.CrashReason = cs.State.Waiting.Reason
				}
				break
			}
		}
		ready := p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil
		for _, cs := range p.Status.ContainerStatuses {
			if !cs.Ready {
				ready = false
			}
		}
		if ready {
			out.Ready++
		}
		if p.Status.Phase == corev1.PodPending {
			out.Pending++
		}
	}
	if out.Ready > out.Total {
		out.Ready = out.Total
	}
	return out
}

// SummarizeControllerHealth distills a slice of controller pods into a
// short, operator-readable status verb. Aggregates over multiple
// replicas (Argo's controller is typically deployed as a 2-replica
// StatefulSet for HA): if any pod is in CrashLoopBackOff or Error, that
// fact dominates the status. If all pods are Ready, "healthy". Anything
// in between is "degraded" with a count.
func SummarizeControllerHealth(controller string, pods []*corev1.Pod) string {
	health := SummarizeControllerPods(pods)
	switch {
	case health.Crashing > 0:
		return fmt.Sprintf("%s is %s (%d/%d pods)", controller, health.CrashReason, health.Crashing, health.Total)
	case health.Ready == health.Total && health.Total > 0:
		// All pods Ready — if the resource is *still* stuck deleting
		// despite a healthy controller, it's a different problem (RBAC,
		// network, broken finalizer logic). Surface the healthy state
		// so the operator knows to dig into the controller's logs
		// rather than its lifecycle.
		suffix := "s"
		if health.Ready == 1 {
			suffix = ""
		}
		return fmt.Sprintf("%s is healthy (%d pod%s ready)", controller, health.Ready, suffix)
	case health.Pending > 0:
		return fmt.Sprintf("%s is pending start (%d/%d pods Pending)", controller, health.Pending, health.Total)
	default:
		return fmt.Sprintf("%s is degraded (%d/%d pods ready)", controller, health.Ready, health.Total)
	}
}
