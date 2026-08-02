package k8s

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/skyhook-io/radar/pkg/health"
)

type oomLimitDiscrepancy struct {
	containerName string
	replicaSet    string
	templateLimit resource.Quantity
	podSpecLimit  resource.Quantity
	observedLimit resource.Quantity
	limitSource   string
}

func oomLimitDiagnosis(cache *ResourceCache, pod *corev1.Pod, reason, lastTerminatedReason string, now time.Time) (string, string) {
	if !podReasonClassifiesAsOOM(reason, lastTerminatedReason) {
		return "", ""
	}

	discrepancy, ok := activeOOMLimitDiscrepancy(cache, pod, now)
	if !ok {
		return "", ""
	}

	var cause string
	if discrepancy.limitSource == "status" {
		cause = fmt.Sprintf(
			"Container %q was OOMKilled. Its currently reported enacted memory limit is %s, below its owning ReplicaSet %q template limit of %s.",
			discrepancy.containerName,
			discrepancy.observedLimit.String(),
			discrepancy.replicaSet,
			discrepancy.templateLimit.String(),
		)
		if !discrepancy.podSpecLimit.IsZero() {
			cause += fmt.Sprintf(" The current Pod spec limit is %s.", discrepancy.podSpecLimit.String())
		} else {
			cause += " The current Pod spec has no explicit memory limit."
		}
	} else {
		cause = fmt.Sprintf(
			"Container %q was OOMKilled. Its current admitted Pod spec memory limit is %s, below its owning ReplicaSet %q template limit of %s. Kubernetes does not currently report an enacted runtime limit for this container.",
			discrepancy.containerName,
			discrepancy.observedLimit.String(),
			discrepancy.replicaSet,
			discrepancy.templateLimit.String(),
		)
	}

	action := "Determine why the Pod and ReplicaSet differ: inspect Pod resize status, VPA or admission mutation, and whether the ReplicaSet template changed after Pod creation before changing the workload memory limit."
	return cause, action
}

func activeOOMLimitDiscrepancy(cache *ResourceCache, pod *corev1.Pod, now time.Time) (oomLimitDiscrepancy, bool) {
	statuses := health.ActiveOOMKilledContainers(pod, now)
	if len(statuses) == 0 || cache == nil {
		return oomLimitDiscrepancy{}, false
	}

	owner := controllerOwnerRef(pod.OwnerReferences)
	if owner == nil || owner.APIVersion != "apps/v1" || owner.Kind != "ReplicaSet" || owner.UID == "" {
		return oomLimitDiscrepancy{}, false
	}
	rsLister := cache.ReplicaSets()
	if rsLister == nil {
		return oomLimitDiscrepancy{}, false
	}
	rs, err := rsLister.ReplicaSets(pod.Namespace).Get(owner.Name)
	if err != nil || rs.UID == "" || rs.UID != owner.UID {
		return oomLimitDiscrepancy{}, false
	}

	for i := range statuses {
		status := statuses[i]
		templateLimit, ok := containerMemoryLimit(rs.Spec.Template.Spec.Containers, status.Name)
		if !ok {
			continue
		}
		podSpecLimit, _ := containerMemoryLimit(pod.Spec.Containers, status.Name)

		d := oomLimitDiscrepancy{
			containerName: status.Name,
			replicaSet:    rs.Name,
			templateLimit: templateLimit,
			podSpecLimit:  podSpecLimit,
		}
		if status.Resources != nil {
			enactedLimit, exists := explicitMemoryLimit(status.Resources.Limits)
			if !exists || enactedLimit.Cmp(templateLimit) >= 0 {
				continue
			}
			d.observedLimit = enactedLimit
			d.limitSource = "status"
			return d, true
		}

		if podHasResizeSignal(pod) || podSpecLimit.IsZero() || podSpecLimit.Cmp(templateLimit) >= 0 {
			continue
		}
		d.observedLimit = podSpecLimit
		d.limitSource = "spec"
		return d, true
	}
	return oomLimitDiscrepancy{}, false
}

func containerMemoryLimit(containers []corev1.Container, name string) (resource.Quantity, bool) {
	for i := range containers {
		if containers[i].Name == name {
			return explicitMemoryLimit(containers[i].Resources.Limits)
		}
	}
	return resource.Quantity{}, false
}

func explicitMemoryLimit(limits corev1.ResourceList) (resource.Quantity, bool) {
	limit, ok := limits[corev1.ResourceMemory]
	return limit, ok && limit.Sign() > 0
}

func podHasResizeSignal(pod *corev1.Pod) bool {
	if pod.Status.Resize != "" {
		return true
	}
	for i := range pod.Status.Conditions {
		switch pod.Status.Conditions[i].Type {
		case corev1.PodResizePending, corev1.PodResizeInProgress:
			return true
		}
	}
	return false
}

// This mirrors the Pod reason precedence in internal/issues without importing
// that higher layer back into the detector package. Probe, image, init, and
// waiting reasons must not receive an OOM-specific cause even with OOM history.
// Message-driven reason promotion is normalized by the issues composer, which
// clears any diagnosis belonging to the superseded detector reason.
func podReasonClassifiesAsOOM(reason, lastTerminatedReason string) bool {
	if reason == "OOMKilled" {
		return true
	}
	if lastTerminatedReason != "OOMKilled" {
		return false
	}
	if isImagePullReason(reason) {
		return false
	}
	switch reason {
	case highRestartReason,
		livenessProbeFailedReason, livenessProbeInvalidReason,
		readinessProbeFailedReason, readinessProbeInvalidReason,
		initContainerStalledReason,
		"CreateContainerConfigError", "CreateContainerError", "RunContainerError", "Pending", "ContainerCreating":
		return false
	default:
		return true
	}
}
