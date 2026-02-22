package k8s

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// ClassifyPodHealth determines if a pod is "healthy", "warning", or "error".
// This is the canonical implementation used by both MCP and REST dashboards.
func ClassifyPodHealth(pod *corev1.Pod, now time.Time) string {
	if pod.Status.Phase == corev1.PodSucceeded {
		return "healthy"
	}
	if pod.Status.Phase == corev1.PodFailed {
		return "error"
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "CreateContainerConfigError" {
				return "error"
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return "error"
		}
		if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			return "error"
		}
	}

	// Init container errors
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" {
				return "error"
			}
		}
	}

	// Warning: pods pending for more than 5 minutes
	if pod.Status.Phase == corev1.PodPending {
		if now.Sub(pod.CreationTimestamp.Time) > 5*time.Minute {
			return "warning"
		}
		return "healthy"
	}

	// Warning: pods with high restart counts
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > 3 {
			return "warning"
		}
	}

	return "healthy"
}

// PodProblemReason returns a short reason string for a problematic pod.
func PodProblemReason(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	return string(pod.Status.Phase)
}

// NodeHealth describes the health of a single node.
type NodeHealth struct {
	Ready         bool
	Unschedulable bool
	Pressures     []string // "MemoryPressure", "DiskPressure", "PIDPressure"
	Version       string   // kubelet version
	Reason        string   // condition message if NotReady
}

// ClassifyNodeHealth evaluates a node's conditions and spec.
func ClassifyNodeHealth(node *corev1.Node) NodeHealth {
	h := NodeHealth{
		Unschedulable: node.Spec.Unschedulable,
		Version:       node.Status.NodeInfo.KubeletVersion,
	}

	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeReady:
			h.Ready = cond.Status == corev1.ConditionTrue
			if !h.Ready && cond.Message != "" {
				h.Reason = cond.Message
			}
		case corev1.NodeMemoryPressure:
			if cond.Status == corev1.ConditionTrue {
				h.Pressures = append(h.Pressures, "MemoryPressure")
			}
		case corev1.NodeDiskPressure:
			if cond.Status == corev1.ConditionTrue {
				h.Pressures = append(h.Pressures, "DiskPressure")
			}
		case corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				h.Pressures = append(h.Pressures, "PIDPressure")
			}
		}
	}

	return h
}

// NodeProblem describes a detected problem on a node.
type NodeProblem struct {
	NodeName string `json:"nodeName"`
	Problem  string `json:"problem"`
	Reason   string `json:"reason,omitempty"`
	Severity string `json:"severity"` // "error" or "warning"
}

// DetectNodeProblems scans nodes for NotReady, Cordoned, and pressure conditions.
func DetectNodeProblems(nodes []*corev1.Node) []NodeProblem {
	var problems []NodeProblem

	for _, node := range nodes {
		h := ClassifyNodeHealth(node)

		if !h.Ready {
			reason := "NotReady"
			if h.Reason != "" {
				reason = h.Reason
			}
			problems = append(problems, NodeProblem{
				NodeName: node.Name,
				Problem:  "NotReady",
				Reason:   reason,
				Severity: "error",
			})
		} else if h.Unschedulable {
			problems = append(problems, NodeProblem{
				NodeName: node.Name,
				Problem:  "Cordoned",
				Reason:   "SchedulingDisabled",
				Severity: "warning",
			})
		}

		for _, pressure := range h.Pressures {
			problems = append(problems, NodeProblem{
				NodeName: node.Name,
				Problem:  pressure,
				Reason:   pressure,
				Severity: "warning",
			})
		}
	}

	return problems
}

// VersionSkew describes a detected minor version skew across cluster nodes.
type VersionSkew struct {
	Versions   map[string][]string `json:"versions"`   // minor version -> node names
	MinVersion string              `json:"minVersion"`
	MaxVersion string              `json:"maxVersion"`
}

// DetectVersionSkew checks for minor version differences across nodes.
// Returns nil if all nodes are on the same minor version (patch-only differences are normal).
func DetectVersionSkew(nodes []*corev1.Node) *VersionSkew {
	if len(nodes) == 0 {
		return nil
	}

	versions := make(map[string][]string) // minor version -> node names
	for _, node := range nodes {
		ver := node.Status.NodeInfo.KubeletVersion
		minor := extractMinorVersion(ver)
		if minor == "" {
			continue
		}
		versions[minor] = append(versions[minor], node.Name)
	}

	if len(versions) <= 1 {
		return nil
	}

	// Find min and max versions
	var minV, maxV string
	for v := range versions {
		if minV == "" || v < minV {
			minV = v
		}
		if maxV == "" || v > maxV {
			maxV = v
		}
	}

	return &VersionSkew{
		Versions:   versions,
		MinVersion: minV,
		MaxVersion: maxV,
	}
}

// extractMinorVersion extracts "v1.28" from "v1.28.3" or "1.28" from "1.28.3".
func extractMinorVersion(version string) string {
	version = strings.TrimPrefix(version, "v")
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// FormatAge formats a duration into a human-readable age string (e.g., "5d", "3h").
func FormatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Truncate trims a string to maxLen characters, appending "..." if truncated.
func Truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
