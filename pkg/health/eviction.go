package health

import (
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// PodEvictedReason is the pod-level status.reason the kubelet sets when it
// evicts a pod under node pressure or rejects one at admission because the node
// reports a pressure condition.
const PodEvictedReason = "Evicted"

// EvictionKind says which kubelet path produced an eviction message.
type EvictionKind string

const (
	// EvictionNodePressure: "The node was low on resource: X." The node crossed
	// an eviction threshold and the kubelet picked this pod to reclaim X.
	EvictionNodePressure EvictionKind = "node_pressure"
	// EvictionNodeCondition: "The node had condition: [DiskPressure]." The pod
	// was rejected at admission because the node already reported pressure.
	EvictionNodeCondition EvictionKind = "node_condition"
	// EvictionLocalStorageLimit: the pod or one of its containers or emptyDir
	// volumes exceeded its own ephemeral-storage limit. Not node pressure.
	EvictionLocalStorageLimit EvictionKind = "local_storage_limit"
	// EvictionUnparsed: an Evicted pod whose message matches none of the above.
	EvictionUnparsed EvictionKind = "unparsed"
)

// EvictedContainer is one container the kubelet named in its eviction message.
type EvictedContainer struct {
	Name    string
	Usage   string // empty for a limit-exceeded container
	Request string
	Limit   string // set only for a limit-exceeded container
}

// PodEviction is the parsed form of a kubelet eviction message. Every value is
// the kubelet's own text; nothing is compared or recomputed here.
type PodEviction struct {
	Kind EvictionKind
	// Resource is the starved resource: memory, ephemeral-storage, inodes or
	// pids (kubelet resource names, not signal names).
	Resource   string
	Threshold  string
	Available  string
	Conditions []string // node conditions from an admission rejection
	Containers []EvictedContainer
	// LimitDetail is the kubelet's sentence for a local-storage limit eviction.
	LimitDetail string
}

var (
	evictionLowResourceRe = regexp.MustCompile(`The node was low on resource: ([A-Za-z0-9._/-]+)\.`)
	evictionThresholdRe   = regexp.MustCompile(`Threshold quantity: ([^,]+), available: ([^.\s]+(?:\.\d+[^.\s]*)?)\.`)
	evictionConditionRe   = regexp.MustCompile(`The node had condition: \[([^\]]*)\]\.`)
	// Current kubelets: "Container X was using Y, request is Z, has larger consumption of R."
	evictionContainerRe = regexp.MustCompile(`Container ([^ ]+) was using ([^,]+), request is ([^,]+)`)
	// Kubelets before 1.22: "Container X was using Y, which exceeds its request of Z."
	evictionContainerLegacyRe = regexp.MustCompile(`Container ([^ ]+) was using ([^,]+), which exceeds its request of ([^.]+(?:\.\d+[^.\s]*)?)\.`)
	evictionContainerLimitRe  = regexp.MustCompile(`Container ([^ ]+) exceeded its local ephemeral storage limit "([^"]*)"\.`)
	evictionPodLimitRe        = regexp.MustCompile(`Pod ephemeral local storage usage exceeds the total limit of containers ([^.\s]+(?:\.\d+[^.\s]*)?)\.`)
	evictionEmptyDirRe        = regexp.MustCompile(`Usage of EmptyDir volume "([^"]*)" exceeds the limit "([^"]*)"\.`)
)

// IsEvictedPod reports whether the kubelet evicted (or admission-rejected) the pod.
func IsEvictedPod(pod *corev1.Pod) bool {
	return pod != nil && pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == PodEvictedReason
}

// ParsePodEviction parses the kubelet's eviction message (pod.status.message of
// an Evicted pod). The formats come from pkg/kubelet/eviction/helpers.go and
// the admission path in the eviction manager.
func ParsePodEviction(message string) PodEviction {
	ev := PodEviction{Kind: EvictionUnparsed}
	if m := evictionContainerLimitRe.FindAllStringSubmatch(message, -1); len(m) > 0 {
		ev.Kind = EvictionLocalStorageLimit
		ev.Resource = string(corev1.ResourceEphemeralStorage)
		for _, c := range m {
			ev.Containers = append(ev.Containers, EvictedContainer{Name: c[1], Limit: c[2]})
		}
		ev.LimitDetail = strings.TrimSpace(m[0][0])
		return ev
	}
	if m := evictionPodLimitRe.FindStringSubmatch(message); m != nil {
		ev.Kind = EvictionLocalStorageLimit
		ev.Resource = string(corev1.ResourceEphemeralStorage)
		ev.LimitDetail = strings.TrimSpace(m[0])
		return ev
	}
	if m := evictionEmptyDirRe.FindStringSubmatch(message); m != nil {
		ev.Kind = EvictionLocalStorageLimit
		ev.Resource = string(corev1.ResourceEphemeralStorage)
		ev.LimitDetail = strings.TrimSpace(m[0])
		return ev
	}
	if m := evictionLowResourceRe.FindStringSubmatch(message); m != nil {
		ev.Kind = EvictionNodePressure
		ev.Resource = m[1]
		if t := evictionThresholdRe.FindStringSubmatch(message); t != nil {
			ev.Threshold = strings.TrimSpace(t[1])
			ev.Available = strings.TrimSpace(t[2])
		}
		for _, re := range []*regexp.Regexp{evictionContainerRe, evictionContainerLegacyRe} {
			for _, c := range re.FindAllStringSubmatch(message, -1) {
				ev.Containers = append(ev.Containers, EvictedContainer{
					Name: c[1], Usage: strings.TrimSpace(c[2]), Request: strings.TrimSpace(c[3]),
				})
			}
		}
		return ev
	}
	if m := evictionConditionRe.FindStringSubmatch(message); m != nil {
		ev.Kind = EvictionNodeCondition
		for _, c := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ' ' || r == ',' }) {
			ev.Conditions = append(ev.Conditions, c)
		}
		if len(ev.Conditions) == 1 {
			ev.Resource = resourceForPressureCondition(ev.Conditions[0])
		}
		return ev
	}
	return ev
}

// PressureCondition is the node condition type that matches the starved
// resource ("MemoryPressure", "DiskPressure", "PIDPressure"), or "" when the
// eviction is not node pressure or the resource is unrecognized.
func (e PodEviction) PressureCondition() string {
	switch e.Kind {
	case EvictionNodePressure:
		return pressureConditionForResource(e.Resource)
	case EvictionNodeCondition:
		if len(e.Conditions) == 1 {
			return e.Conditions[0]
		}
	}
	return ""
}

func pressureConditionForResource(resource string) string {
	switch resource {
	case string(corev1.ResourceMemory):
		return string(corev1.NodeMemoryPressure)
	case string(corev1.ResourceEphemeralStorage), "inodes":
		return string(corev1.NodeDiskPressure)
	case "pids":
		return string(corev1.NodePIDPressure)
	}
	return ""
}

func resourceForPressureCondition(condition string) string {
	switch condition {
	case string(corev1.NodeMemoryPressure):
		return string(corev1.ResourceMemory)
	case string(corev1.NodeDiskPressure):
		return string(corev1.ResourceEphemeralStorage)
	case string(corev1.NodePIDPressure):
		return "pids"
	}
	return ""
}
