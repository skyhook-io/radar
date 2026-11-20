package loki

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// StreamLabels names the Loki stream labels that carry a pod's namespace, pod,
// container and (when the shipper adds one) Job name. Setups differ:
// Promtail and Alloy use "namespace"/"pod", Fluent Bit "kubernetes_*_name",
// the Logging operator's fluentd output "namespace_name"/"pod_name",
// OpenTelemetry "k8s_*_name".
type StreamLabels struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Job       string `json:"job,omitempty"`
	// Cluster is the label that names a cluster when one Loki holds several
	// clusters' logs; ClusterValue is this cluster's value for it
	// (--loki-cluster). ClusterRequired: an unset value refuses History
	// (with authentication, where a read must stay within this cluster).
	Cluster         string `json:"cluster,omitempty"`
	ClusterValue    string `json:"clusterValue,omitempty"`
	ClusterRequired bool   `json:"-"`
}

var (
	namespaceLabels = []string{"namespace", "kubernetes_namespace_name", "namespace_name", "k8s_namespace_name"}
	podLabels       = []string{"pod", "kubernetes_pod_name", "pod_name", "k8s_pod_name"}
	containerLabels = []string{"container", "kubernetes_container_name", "container_name", "k8s_container_name"}
	jobLabels       = []string{"job_name", "batch_kubernetes_io_job_name"}
	clusterLabels   = []string{"cluster", "k8s_cluster_name", "cluster_name"}
)

// DetectLabels picks, from the label names Loki reports, the first known name
// for each role.
func DetectLabels(names []string) StreamLabels {
	first := func(candidates []string) string {
		for _, c := range candidates {
			if slices.Contains(names, c) {
				return c
			}
		}
		return ""
	}
	return StreamLabels{Namespace: first(namespaceLabels), Pod: first(podLabels), Container: first(containerLabels), Job: first(jobLabels), Cluster: first(clusterLabels)}
}

// HistoryReason is why History can't read this Loki, or "" when it can.
func (l StreamLabels) HistoryReason() string {
	if l.Namespace == "" || l.Pod == "" {
		return "Loki has no namespace or pod stream label; History needs both"
	}
	if l.ClusterValue != "" && l.Cluster == "" {
		return "--loki-cluster is set, but Radar found no cluster, k8s_cluster_name or cluster_name label in Loki; name the label as --loki-cluster=<label>=<value>"
	}
	if l.Cluster != "" && l.ClusterValue == "" && l.ClusterRequired {
		return fmt.Sprintf("Loki's streams carry a %q label, so it may hold other clusters' logs; start Radar with --loki-cluster set to this cluster's value", l.Cluster)
	}
	return ""
}

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ParseCluster reads --loki-cluster: "value", or "label=value" for a label
// Radar doesn't know. The label name goes into LogQL unquoted, so it must be
// a valid Loki label name.
func ParseCluster(raw string) (label, value string, err error) {
	raw = strings.TrimSpace(raw)
	if l, v, ok := strings.Cut(raw, "="); ok {
		label, value = strings.TrimSpace(l), strings.TrimSpace(v)
		if !labelName.MatchString(label) || value == "" {
			return "", "", fmt.Errorf("--loki-cluster %q: want a value, or label=value with a Loki label name", raw)
		}
		return label, value, nil
	}
	return "", raw, nil
}
