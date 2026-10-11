package k8s

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/health"
)

// NodeProblem describes a detected problem on a node.
type NodeProblem struct {
	NodeName string `json:"nodeName"`
	Problem  string `json:"problem"`
	Reason   string `json:"reason,omitempty"`
	Severity string `json:"severity"` // "critical", "high", or "medium"
	Action   string `json:"action,omitempty"`
}

// DetectNodeProblems separates intentional removal from readiness failures.
func DetectNodeProblems(nodes []*corev1.Node) []NodeProblem {
	var problems []NodeProblem
	now := time.Now()

	for _, node := range nodes {
		h := health.Node(node)

		lifecycle := health.NodeLifecycle(node, now)

		if lifecycle.ReadinessFailed {
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
		}
		if lifecycle.Delayed {
			detection, _ := terminatingProblemSince("Node", "", node, now, lifecycle.StartedAt)
			reason := "Node is still present after it was marked for removal"
			if node.DeletionTimestamp != nil && len(node.Finalizers) > 0 {
				reason = detection.Message
			}
			problems = append(problems, NodeProblem{
				NodeName: node.Name, Problem: "Removal delayed", Reason: reason,
				Severity: detection.Severity, Action: "Check remaining pods, PodDisruptionBudgets and controller events; if deletion has started, check finalizers too. Keep scheduling disabled during removal.",
			})
		}
		for _, problem := range lifecycle.Problems {
			severity := "critical"
			if problem == "NetworkUnavailable" {
				severity = "high"
			}
			problems = append(problems, NodeProblem{NodeName: node.Name, Problem: problem, Reason: problem, Severity: severity})
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
