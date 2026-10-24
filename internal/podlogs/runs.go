package podlogs

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// EarlierRun reports whether the pod's status records a run of the container
// before the current one. known is false when the pod has no status for the
// container yet, so a caller cannot tell either way.
//
// The restart count and the last termination are both checked: either one is
// evidence of an earlier run, and the apiserver's "not found" answer for a
// previous-run read cannot tell a container that never restarted from one
// whose earlier run the node has since dropped.
func EarlierRun(pod *corev1.Pod, container string) (had, known bool) {
	status, ok := containerStatus(pod, container)
	if !ok {
		return false, false
	}
	return status.RestartCount > 0 || status.LastTerminationState.Terminated != nil, true
}

// IsNoPreviousRun matches the apiserver's answer to a previous-run read when it
// has no earlier run to give. It comes as a 400, so any other failure that
// mentions the phrase is left alone.
func IsNoPreviousRun(err error) bool {
	return apierrors.IsBadRequest(err) && strings.Contains(err.Error(), "previous terminated container")
}

const waitingToStart = "is waiting to start"

// IsContainerNotStarted matches the apiserver's answer to a log read for a
// container that has not run yet: ContainerCreating, an image that cannot be
// pulled, a missing ConfigMap or Secret. A container that ran before and is now
// waiting to restart still has logs, so it does not get this answer.
func IsContainerNotStarted(err error) bool {
	return apierrors.IsBadRequest(err) && strings.Contains(err.Error(), waitingToStart)
}

// NotStartedMessage says why a container that has not started has no logs.
// The reason comes from the pod's status when Radar has it, since that carries
// the kubelet's detail (the image it could not pull, the ConfigMap it could not
// find), and otherwise from the apiserver's answer.
func NotStartedMessage(pod *corev1.Pod, container string, err error) string {
	if PodStopped(pod) {
		return "This container never started, so it has no logs. The pod has finished (" + string(pod.Status.Phase) + "), so it won't start now."
	}
	const lead = "This container has not started yet, so it has no logs."
	reason := ""
	if status, ok := containerStatus(pod, container); ok && status.State.Waiting != nil && status.State.Waiting.Reason != "" {
		reason = status.State.Waiting.Reason
		if msg := strings.TrimSpace(status.State.Waiting.Message); msg != "" {
			reason += ": " + msg
		}
	} else if err != nil {
		if _, after, found := strings.Cut(err.Error(), waitingToStart+": "); found {
			reason = strings.TrimSpace(after)
		}
	}
	if reason == "" {
		return lead
	}
	return lead + " Kubernetes reports " + strings.TrimSuffix(reason, ".") + "."
}

// ContainerNotStartedYet reports whether the pod's status shows the container
// waiting and never having run, so it has no logs. A container waiting to
// restart after a crash has logs. False when Radar has no status for it.
func ContainerNotStartedYet(pod *corev1.Pod, container string) bool {
	status, ok := containerStatus(pod, container)
	return ok && status.State.Waiting != nil && status.RestartCount == 0 && status.LastTerminationState.Terminated == nil
}

// PodStopped reports whether the pod has finished (Failed or Succeeded). A
// container in it that never started, such as one held at PodInitializing by a
// failed init container in a pod that doesn't restart, never will.
func PodStopped(pod *corev1.Pod) bool {
	return pod != nil && (pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded)
}

func containerStatus(pod *corev1.Pod, name string) (corev1.ContainerStatus, bool) {
	if pod == nil {
		return corev1.ContainerStatus{}, false
	}
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, status := range statuses {
			if status.Name == name {
				return status, true
			}
		}
	}
	return corev1.ContainerStatus{}, false
}
