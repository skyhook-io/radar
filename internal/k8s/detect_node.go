package k8s

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/health"
)

// NodeProblem describes a detected problem on a node.
type NodeProblem struct {
	NodeName string `json:"nodeName"`
	Problem  string `json:"problem"`
	Reason   string `json:"reason,omitempty"`
	Severity string `json:"severity"` // "critical", "high", or "medium"
}

// DetectNodeProblems scans nodes for NotReady, Cordoned, and pressure conditions.
func DetectNodeProblems(nodes []*corev1.Node) []NodeProblem {
	var problems []NodeProblem

	for _, node := range nodes {
		h := health.Node(node)

		if !h.Ready {
			reason := "NotReady"
			if h.Reason != "" {
				reason = h.Reason
			}
			problems = append(problems, NodeProblem{
				NodeName: node.Name,
				Problem:  "NotReady",
				Reason:   reason,
				Severity: "critical",
			})
		} else if h.Unschedulable {
			problems = append(problems, NodeProblem{
				NodeName: node.Name,
				Problem:  "Cordoned",
				Reason:   "SchedulingDisabled",
				Severity: "medium",
			})
		}

		for _, pressure := range h.Pressures {
			problems = append(problems, NodeProblem{
				NodeName: node.Name,
				Problem:  pressure,
				Reason:   pressure,
				Severity: "critical",
			})
		}
	}

	return problems
}

// VersionSkew describes a detected minor version skew across cluster nodes.
type VersionSkew struct {
	Versions   map[string][]string `json:"versions"` // minor version -> node names
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
