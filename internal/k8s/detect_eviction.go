package k8s

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/skyhook-io/radar/pkg/health"
)

// evictionDetection is what an Evicted pod contributes to its problem row.
type evictionDetection struct {
	severity string
	message  string
	raw      string
	cause    string
	action   string
	onset    time.Time
}

// evictedPodProblem describes an Evicted pod from the kubelet's own message.
// The message names the node and what it ran out of; cause and action stay
// node-agnostic so a workload's evictions across several nodes still agree on
// one diagnosis when grouped.
//
// An evicted pod owned by a controller that replaces pods is a warning: the
// replacement already exists, and an outage shows up on the workload's own
// availability row. Any other evicted pod is gone for good, so it stays critical.
func evictedPodProblem(pod *corev1.Pod, ownerKind string) evictionDetection {
	raw := pod.Status.Message
	ev := health.ParsePodEviction(raw)
	out := evictionDetection{severity: "critical", raw: raw, onset: podEvictionTime(pod)}
	switch ownerKind {
	case "Deployment", "ReplicaSet", "StatefulSet", "DaemonSet", "Job", "CronJob", "Rollout":
		out.severity = "high"
	}

	where := "Evicted by the kubelet"
	if node := pod.Spec.NodeName; node != "" {
		where += " on node " + node
	}
	switch ev.Kind {
	case health.EvictionNodePressure:
		out.message = where + ": the node was low on " + ev.Resource
		if ev.Threshold != "" {
			out.message += fmt.Sprintf(" (eviction threshold %s, available %s)", formatEvictionQuantity(ev.Resource, ev.Threshold), formatEvictionQuantity(ev.Resource, ev.Available))
		}
		out.message += "."
		for _, c := range ev.Containers {
			out.message += fmt.Sprintf(" Container %s was using %s, request %s.", c.Name, formatEvictionQuantity(ev.Resource, c.Usage), formatEvictionQuantity(ev.Resource, c.Request))
		}
		out.cause = fmt.Sprintf("The kubelet evicted this pod because its node ran low on %s.", ev.Resource)
		out.action = evictionPressureAction(ev.Resource)
	case health.EvictionNodeCondition:
		out.message = strings.Replace(where, "Evicted by the kubelet", "Rejected at admission by the kubelet", 1) +
			": the node reported " + strings.Join(ev.Conditions, ", ") + "."
		out.cause = "The kubelet rejected this pod because its node reported " + strings.Join(ev.Conditions, ", ") + "."
		out.action = "Check the node's conditions and what is consuming its resources; the controller retries on another node once one has room."
	case health.EvictionLocalStorageLimit:
		out.message = where + ": " + ev.LimitDetail
		out.cause = "The kubelet evicted this pod because it exceeded its own ephemeral-storage limit; the node itself was not under pressure."
		out.action = "Find what the container writes locally (logs, temp files, emptyDir) and reduce it, or raise the ephemeral-storage limit or the emptyDir sizeLimit."
	default:
		if raw == "" {
			out.message = where + "."
		} else {
			out.message = where + ": " + raw
		}
		out.cause = "The kubelet evicted this pod."
		out.action = "Check the node's conditions and the pod's events around the eviction."
	}
	return out
}

func evictionPressureAction(resource string) string {
	switch resource {
	case string(corev1.ResourceMemory):
		return "Check which pods use the node's memory and set memory requests close to real usage; the kubelet evicts pods using the most memory above their request first."
	case string(corev1.ResourceEphemeralStorage), "inodes":
		return "Check what fills the node's disk (container logs, writable layers, emptyDir volumes, unused images) and set ephemeral-storage requests and limits on containers that write locally."
	case "pids":
		return "Find the process that leaks PIDs and set a per-pod PID limit on the node."
	}
	return "Check the node's conditions and what is consuming the starved resource."
}

// podEvictionTime is when the kubelet evicted the pod: the DisruptionTarget
// condition it sets for pressure evictions (Kubernetes 1.26+), else the newest
// container termination, which is the kubelet killing the containers on its
// way out (limit evictions get no DisruptionTarget). Ready=False is not used:
// it keeps its earlier timestamp when the pod was already unready. Zero when
// neither is recorded.
func podEvictionTime(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	var killed time.Time
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, cs := range statuses {
			if t := cs.State.Terminated; t != nil && t.FinishedAt.After(killed) {
				killed = t.FinishedAt.Time
			}
		}
	}
	return killed
}

// formatEvictionQuantity renders a byte count the kubelet printed raw
// ("4013935590") in binary units. Other values pass through unchanged.
func formatEvictionQuantity(res, value string) string {
	if res != string(corev1.ResourceMemory) && res != string(corev1.ResourceEphemeralStorage) {
		return value
	}
	q, err := resource.ParseQuantity(value)
	if err != nil {
		return value
	}
	b := float64(q.Value())
	for _, u := range []struct {
		suffix string
		size   float64
	}{{"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}} {
		if b >= u.size {
			return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", b/u.size), "0"), ".") + u.suffix
		}
	}
	return value
}
