package podlogs

import (
	"time"

	"github.com/skyhook-io/radar/pkg/health"
	corev1 "k8s.io/api/core/v1"
)

// ContainerInfo contains compact per-container runtime status for the UI.
type ContainerInfo struct {
	Name         string `json:"name"`
	Init         bool   `json:"init,omitempty"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
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

// IsPodReady checks if all containers in a pod are ready
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

// BuildPodInfo converts a single pod to PodInfo
func BuildPodInfo(pod *corev1.Pod, now time.Time) PodInfo {
	containers := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	containerStatuses := make([]ContainerInfo, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		containerStatuses = append(containerStatuses, ContainerInfo{
			Name:         cs.Name,
			Init:         true,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		})
	}
	for _, cs := range pod.Status.ContainerStatuses {
		containerStatuses = append(containerStatuses, ContainerInfo{
			Name:         cs.Name,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		})
	}
	verdict := health.Pod(pod, now)
	displayLevel := health.PodDisplayLevel(pod, now)
	if displayLevel != verdict.Level {
		verdict.Level = displayLevel
		if verdict.Reason == "" {
			verdict.Reason = health.PodProblemReason(pod, now)
		}
		if verdict.Message == "" {
			verdict.Message = health.PodProblemMessage(pod)
		}
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
