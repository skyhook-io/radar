package prometheus

import (
	"fmt"
	"strings"
)

// MetricCategory represents a category of metrics.
type MetricCategory string

const (
	CategoryCPU        MetricCategory = "cpu"
	CategoryMemory     MetricCategory = "memory"
	CategoryNetworkRX  MetricCategory = "network_rx"
	CategoryNetworkTX  MetricCategory = "network_tx"
	CategoryFilesystem MetricCategory = "filesystem"
)

// AllCategories returns all metric categories in display order.
func AllCategories() []MetricCategory {
	return []MetricCategory{CategoryCPU, CategoryMemory, CategoryNetworkRX, CategoryNetworkTX, CategoryFilesystem}
}

// CategoryLabel returns a human-readable label for a metric category.
func CategoryLabel(cat MetricCategory) string {
	switch cat {
	case CategoryCPU:
		return "CPU"
	case CategoryMemory:
		return "Memory"
	case CategoryNetworkRX:
		return "Network Received"
	case CategoryNetworkTX:
		return "Network Transmitted"
	case CategoryFilesystem:
		return "Filesystem"
	default:
		return string(cat)
	}
}

// CategoryUnit returns the unit for a metric category.
func CategoryUnit(cat MetricCategory) string {
	switch cat {
	case CategoryCPU:
		return "cores"
	case CategoryMemory:
		return "bytes"
	case CategoryNetworkRX, CategoryNetworkTX:
		return "bytes/s"
	case CategoryFilesystem:
		return "bytes/s"
	default:
		return ""
	}
}

// SupportedKinds returns the resource kinds that support Prometheus metrics.
func SupportedKinds() []string {
	return []string{
		"Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet",
		"Job", "CronJob", "Node",
	}
}

// CategoriesForKind returns which metric categories are available for a resource kind.
func CategoriesForKind(kind string) []MetricCategory {
	switch strings.ToLower(kind) {
	case "node":
		return []MetricCategory{CategoryCPU, CategoryMemory, CategoryFilesystem}
	default:
		return AllCategories()
	}
}

// BuildQuery builds a PromQL query for the given resource and metric category.
// For workloads (Deployment, StatefulSet, etc.) it uses pod regex matching.
// For Pods it uses exact name matching.
// For Nodes it uses the node label.
func BuildQuery(kind, namespace, name string, category MetricCategory) string {
	switch strings.ToLower(kind) {
	case "pod":
		return buildPodQuery(namespace, name, category)
	case "deployment", "statefulset", "daemonset", "replicaset":
		return buildWorkloadQuery(namespace, name, category)
	case "job", "cronjob":
		return buildPodQuery(namespace, name, category)
	case "node":
		return buildNodeQuery(name, category)
	default:
		return ""
	}
}

// BuildNamespaceQuery builds a PromQL query for namespace-level aggregation.
func BuildNamespaceQuery(namespace string, category MetricCategory) string {
	switch category {
	case CategoryCPU:
		return fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='%s'}[5m]))`, namespace)
	case CategoryMemory:
		return fmt.Sprintf(`sum(container_memory_working_set_bytes{container!='',namespace='%s'})`, namespace)
	case CategoryNetworkRX:
		return fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace='%s'}[5m]))`, namespace)
	case CategoryNetworkTX:
		return fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace='%s'}[5m]))`, namespace)
	default:
		return ""
	}
}

// BuildClusterQuery builds a PromQL query for cluster-level aggregation.
func BuildClusterQuery(category MetricCategory) string {
	switch category {
	case CategoryCPU:
		return `sum(rate(container_cpu_usage_seconds_total{container!=''}[5m]))`
	case CategoryMemory:
		return `sum(container_memory_working_set_bytes{container!=''})`
	case CategoryNetworkRX:
		return `sum(rate(container_network_receive_bytes_total[5m]))`
	case CategoryNetworkTX:
		return `sum(rate(container_network_transmit_bytes_total[5m]))`
	default:
		return ""
	}
}

func buildPodQuery(namespace, podName string, category MetricCategory) string {
	switch category {
	case CategoryCPU:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			namespace, podName)
	case CategoryMemory:
		return fmt.Sprintf(
			`sum(container_memory_working_set_bytes{container!='',namespace='%s',pod='%s'}) by (pod,namespace)`,
			namespace, podName)
	case CategoryNetworkRX:
		return fmt.Sprintf(
			`sum(rate(container_network_receive_bytes_total{namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			namespace, podName)
	case CategoryNetworkTX:
		return fmt.Sprintf(
			`sum(rate(container_network_transmit_bytes_total{namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			namespace, podName)
	case CategoryFilesystem:
		return fmt.Sprintf(
			`sum(rate(container_fs_writes_bytes_total{namespace='%s',pod='%s'}[5m]) + rate(container_fs_reads_bytes_total{namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			namespace, podName, namespace, podName)
	default:
		return ""
	}
}

func buildWorkloadQuery(namespace, workloadName string, category MetricCategory) string {
	// Use regex matching to capture all pods belonging to the workload
	podPattern := fmt.Sprintf("%s-.*", workloadName)

	switch category {
	case CategoryCPU:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			namespace, podPattern)
	case CategoryMemory:
		return fmt.Sprintf(
			`sum(container_memory_working_set_bytes{container!='',namespace='%s',pod=~'%s'}) by (pod,namespace)`,
			namespace, podPattern)
	case CategoryNetworkRX:
		return fmt.Sprintf(
			`sum(rate(container_network_receive_bytes_total{namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			namespace, podPattern)
	case CategoryNetworkTX:
		return fmt.Sprintf(
			`sum(rate(container_network_transmit_bytes_total{namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			namespace, podPattern)
	case CategoryFilesystem:
		return fmt.Sprintf(
			`sum(rate(container_fs_writes_bytes_total{namespace='%s',pod=~'%s'}[5m]) + rate(container_fs_reads_bytes_total{namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			namespace, podPattern, namespace, podPattern)
	default:
		return ""
	}
}

func buildNodeQuery(nodeName string, category MetricCategory) string {
	// Node exporter metrics use "instance" label (standard from node_exporter / prometheus.exporter.unix).
	// Some setups add a "node" label via relabeling, so we match either.
	nodeFilter := fmt.Sprintf(`instance=~'%s(:\\d+)?'`, nodeName)

	switch category {
	case CategoryCPU:
		return fmt.Sprintf(
			`sum(rate(node_cpu_seconds_total{mode!='idle',%s}[5m]))`,
			nodeFilter)
	case CategoryMemory:
		return fmt.Sprintf(
			`node_memory_MemTotal_bytes{%s} - node_memory_MemAvailable_bytes{%s}`,
			nodeFilter, nodeFilter)
	case CategoryFilesystem:
		// Match common root mountpoints; falls back to '/' if none match.
		// In-container node exporters may only see container mounts.
		return fmt.Sprintf(
			`sum(node_filesystem_size_bytes{%s,fstype=~'ext4|xfs|btrfs'} - node_filesystem_avail_bytes{%s,fstype=~'ext4|xfs|btrfs'})`,
			nodeFilter, nodeFilter)
	default:
		return ""
	}
}
