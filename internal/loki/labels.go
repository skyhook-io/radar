package loki

import "slices"

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
}

var (
	namespaceLabels = []string{"namespace", "kubernetes_namespace_name", "namespace_name", "k8s_namespace_name"}
	podLabels       = []string{"pod", "kubernetes_pod_name", "pod_name", "k8s_pod_name"}
	containerLabels = []string{"container", "kubernetes_container_name", "container_name", "k8s_container_name"}
	jobLabels       = []string{"job_name", "batch_kubernetes_io_job_name"}
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
	return StreamLabels{Namespace: first(namespaceLabels), Pod: first(podLabels), Container: first(containerLabels), Job: first(jobLabels)}
}

// HistoryReason is why History can't read this Loki, or "" when it can.
func (l StreamLabels) HistoryReason() string {
	if l.Namespace == "" || l.Pod == "" {
		return "Loki has no namespace or pod stream label; History needs both"
	}
	return ""
}
