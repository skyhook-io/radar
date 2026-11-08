package podlogs

import (
	"time"

	"github.com/skyhook-io/radar/pkg/health"
	corev1 "k8s.io/api/core/v1"
)

// ContainerInfo contains compact per-container runtime status for the UI.
type ContainerInfo struct {
	Name string `json:"name"`
	Init bool   `json:"init,omitempty"`
	// A native sidecar: an init container with restartPolicy Always, which
	// keeps running beside the application instead of finishing first.
	Sidecar      bool  `json:"sidecar,omitempty"`
	Ready        bool  `json:"ready"`
	RestartCount int32 `json:"restartCount"`
	// State is the current run's state: waiting, running or terminated.
	State string `json:"state,omitempty"`
	// Reason is the waiting or terminated reason, such as PodInitializing,
	// CrashLoopBackOff, Completed or Error.
	Reason string `json:"reason,omitempty"`
	// Started reports a container past its startup probe; for a native
	// sidecar it means it no longer holds up Pod initialization.
	Started bool `json:"started,omitempty"`
}

func containerInfo(cs corev1.ContainerStatus) ContainerInfo {
	info := ContainerInfo{Name: cs.Name, Ready: cs.Ready, RestartCount: cs.RestartCount, Started: cs.Started != nil && *cs.Started}
	switch {
	case cs.State.Waiting != nil:
		info.State, info.Reason = "waiting", cs.State.Waiting.Reason
	case cs.State.Running != nil:
		info.State = "running"
	case cs.State.Terminated != nil:
		info.State, info.Reason = "terminated", cs.State.Terminated.Reason
	}
	return info
}

// PodInfo contains compact runtime status about a pod for workload views.
type PodInfo struct {
	Name                  string          `json:"name"`
	Containers            []string        `json:"containers"`
	Ready                 bool            `json:"ready"`
	Phase                 string          `json:"phase,omitempty"`
	NodeName              string          `json:"nodeName,omitempty"`
	HealthLevel           string          `json:"healthLevel,omitempty"`
	Reason                string          `json:"reason,omitempty"`
	Message               string          `json:"message,omitempty"`
	RestartCount          int32           `json:"restartCount,omitempty"`
	LastTerminationReason string          `json:"lastTerminationReason,omitempty"`
	CreatedAt             string          `json:"createdAt,omitempty"`
	ContainerStatuses     []ContainerInfo `json:"containerStatuses,omitempty"`
	StepID                string          `json:"stepID,omitempty"`
	StepName              string          `json:"stepName,omitempty"`
	StepPhase             string          `json:"stepPhase,omitempty"`
	RevisionIdentity      string          `json:"revisionIdentity,omitempty"`
	UpdatedRevision       *bool           `json:"updatedRevision,omitempty"`
}

func IsPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// ContainerTerminated reports whether the named container's current run ended.
func ContainerTerminated(pod *corev1.Pod, name string) bool {
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for _, status := range statuses {
			if status.Name == name {
				return status.State.Terminated != nil
			}
		}
	}
	return false
}

func BuildPodInfo(pod *corev1.Pod, now time.Time) PodInfo {
	containers := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	containerStatuses := make([]ContainerInfo, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	sidecars := map[string]bool{}
	for _, c := range pod.Spec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sidecars[c.Name] = true
		}
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		info := containerInfo(cs)
		info.Init, info.Sidecar = true, sidecars[cs.Name]
		containerStatuses = append(containerStatuses, info)
	}
	for _, cs := range pod.Status.ContainerStatuses {
		containerStatuses = append(containerStatuses, containerInfo(cs))
	}
	verdict := health.Pod(pod, now)
	displayLevel := health.PodDisplayLevel(pod, now)
	if displayLevel != verdict.Level {
		// The display floor escalated the pod (unschedulable / stuck-terminating),
		// so its explanation must describe that cause, not a lower-severity base
		// reason. A deliberately held pod carries a benign "SchedulingGated" reason;
		// overwrite it here so an escalated pod isn't mislabelled as merely held.
		verdict.Level = displayLevel
		verdict.Reason = health.PodProblemReason(pod, now)
		verdict.Message = health.PodProblemMessage(pod)
	}
	restartCount, lastTerminationReason := health.PodRestartContext(pod)
	createdAt := ""
	if !pod.CreationTimestamp.IsZero() {
		createdAt = pod.CreationTimestamp.Time.Format(time.RFC3339)
	}
	annotations := pod.GetAnnotations()
	labels := pod.GetLabels()
	return PodInfo{
		Name:                  pod.Name,
		Containers:            containers,
		Ready:                 IsPodReady(pod),
		Phase:                 string(pod.Status.Phase),
		NodeName:              pod.Spec.NodeName,
		HealthLevel:           string(verdict.Level),
		Reason:                verdict.Reason,
		Message:               verdict.Message,
		RestartCount:          restartCount,
		LastTerminationReason: lastTerminationReason,
		CreatedAt:             createdAt,
		ContainerStatuses:     containerStatuses,
		StepID:                annotations["workflows.argoproj.io/node-id"],
		StepName:              annotations["workflows.argoproj.io/node-name"],
		StepPhase:             labels["workflows.argoproj.io/phase"],
	}
}

func BuildPodInfos(pods []*corev1.Pod) []PodInfo {
	infos := make([]PodInfo, 0, len(pods))
	now := time.Now()
	for _, pod := range pods {
		infos = append(infos, BuildPodInfo(pod, now))
	}
	return infos
}
