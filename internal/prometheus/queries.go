package prometheus

import (
	"fmt"
	"regexp"
	"strings"
)

// sanitizeLabelValue escapes characters that are special in PromQL label matchers.
// This prevents PromQL injection via resource names, namespaces, etc.
var unsafeLabelChars = regexp.MustCompile(`[\\'"` + "`" + `{}]`)

func sanitizeLabelValue(s string) string {
	return unsafeLabelChars.ReplaceAllStringFunc(s, func(c string) string {
		return `\` + c
	})
}

// escapeRegexMeta escapes regex metacharacters for PromQL =~ matching.
var regexMeta = regexp.MustCompile(`([.+*?^${}()|[\]\\])`)

func escapeRegexMeta(s string) string {
	return regexMeta.ReplaceAllString(s, `\\$1`)
}

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

// CategoryUnit returns the default unit for a metric category.
// For kind-aware units (e.g. Node filesystem = bytes vs Pod filesystem = bytes/s),
// use CategoryUnitForKind instead.
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

// CategoryUnitForKind returns the unit for a metric category, adjusted for the resource kind.
// Node filesystem queries return absolute bytes (used space), while pod/workload filesystem
// queries return I/O rate (bytes/s).
func CategoryUnitForKind(kind string, cat MetricCategory) string {
	if strings.EqualFold(kind, "node") && cat == CategoryFilesystem {
		return "bytes"
	}
	return CategoryUnit(cat)
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
// For workloads (Deployment, StatefulSet, Job, CronJob, etc.) it uses pod regex matching.
// For Pods it uses exact name matching.
// For Nodes it matches the node_exporter "instance" label.
func BuildQuery(kind, namespace, name string, category MetricCategory) string {
	switch strings.ToLower(kind) {
	case "pod":
		return buildPodQuery(namespace, name, category)
	case "deployment", "statefulset", "daemonset", "replicaset", "job", "cronjob":
		return buildWorkloadQuery(namespace, name, category)
	case "node":
		return buildNodeQuery(name, category)
	default:
		return ""
	}
}

// BuildNamespaceQuery builds a PromQL query for namespace-level aggregation.
func BuildNamespaceQuery(namespace string, category MetricCategory) string {
	ns := sanitizeLabelValue(namespace)
	switch category {
	case CategoryCPU:
		return fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='%s'}[5m]))`, ns)
	case CategoryMemory:
		return fmt.Sprintf(`sum(container_memory_working_set_bytes{container!='',namespace='%s'})`, ns)
	case CategoryNetworkRX:
		return fmt.Sprintf(`sum(rate(container_network_receive_bytes_total{namespace='%s'}[5m]))`, ns)
	case CategoryNetworkTX:
		return fmt.Sprintf(`sum(rate(container_network_transmit_bytes_total{namespace='%s'}[5m]))`, ns)
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
	ns := sanitizeLabelValue(namespace)
	pod := sanitizeLabelValue(podName)

	switch category {
	case CategoryCPU:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			ns, pod)
	case CategoryMemory:
		return fmt.Sprintf(
			`sum(container_memory_working_set_bytes{container!='',namespace='%s',pod='%s'}) by (pod,namespace)`,
			ns, pod)
	case CategoryNetworkRX:
		return fmt.Sprintf(
			`sum(rate(container_network_receive_bytes_total{namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			ns, pod)
	case CategoryNetworkTX:
		return fmt.Sprintf(
			`sum(rate(container_network_transmit_bytes_total{namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			ns, pod)
	case CategoryFilesystem:
		return fmt.Sprintf(
			`sum(rate(container_fs_writes_bytes_total{namespace='%s',pod='%s'}[5m]) + rate(container_fs_reads_bytes_total{namespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			ns, pod, ns, pod)
	default:
		return ""
	}
}

func buildWorkloadQuery(namespace, workloadName string, category MetricCategory) string {
	ns := sanitizeLabelValue(namespace)
	// Sanitize then escape regex metacharacters so e.g. "my.app" matches literally
	podPattern := fmt.Sprintf("%s-.*", escapeRegexMeta(sanitizeLabelValue(workloadName)))

	switch category {
	case CategoryCPU:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			ns, podPattern)
	case CategoryMemory:
		return fmt.Sprintf(
			`sum(container_memory_working_set_bytes{container!='',namespace='%s',pod=~'%s'}) by (pod,namespace)`,
			ns, podPattern)
	case CategoryNetworkRX:
		return fmt.Sprintf(
			`sum(rate(container_network_receive_bytes_total{namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			ns, podPattern)
	case CategoryNetworkTX:
		return fmt.Sprintf(
			`sum(rate(container_network_transmit_bytes_total{namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			ns, podPattern)
	case CategoryFilesystem:
		return fmt.Sprintf(
			`sum(rate(container_fs_writes_bytes_total{namespace='%s',pod=~'%s'}[5m]) + rate(container_fs_reads_bytes_total{namespace='%s',pod=~'%s'}[5m])) by (pod,namespace)`,
			ns, podPattern, ns, podPattern)
	default:
		return ""
	}
}

func buildNodeQuery(nodeName string, category MetricCategory) string {
	// Node exporter metrics use the "instance" label which is typically set to the node
	// name or IP. The value often includes a port suffix, so we match with an optional port.
	// This heuristic works for most standard deployments; clusters with custom relabeling
	// may need the --prometheus-url flag plus adjusted recording rules.
	sanitized := escapeRegexMeta(sanitizeLabelValue(nodeName))
	nodeFilter := fmt.Sprintf(`instance=~'%s(:\\d+)?'`, sanitized)

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
		// Filter to real filesystems by type (ext4, xfs, btrfs), excluding tmpfs/overlay/proc.
		return fmt.Sprintf(
			`sum(node_filesystem_size_bytes{%s,fstype=~'ext4|xfs|btrfs'} - node_filesystem_avail_bytes{%s,fstype=~'ext4|xfs|btrfs'})`,
			nodeFilter, nodeFilter)
	default:
		return ""
	}
}
