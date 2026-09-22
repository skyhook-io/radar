package prom

import (
	"fmt"
	"regexp"
	"strings"
)

// SanitizeLabelValue escapes characters that are special in PromQL label matchers.
// This prevents PromQL injection via resource names, namespaces, etc.
var unsafeLabelChars = regexp.MustCompile(`[\\'"` + "`" + `{}]`)

func SanitizeLabelValue(s string) string {
	return unsafeLabelChars.ReplaceAllStringFunc(s, func(c string) string {
		return `\` + c
	})
}

// EscapeRegexMeta escapes regex metacharacters for PromQL =~ matching.
var regexMeta = regexp.MustCompile(`([.+*?^${}()|[\]\\])`)

func EscapeRegexMeta(s string) string {
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
	// CategoryRestarts is sourced from KSM and represents the rate-of-change
	// of container restart counters; gracefully degrades when KSM isn't scraped.
	CategoryRestarts MetricCategory = "restarts"
)

// AllCategories returns all metric categories in display order.
func AllCategories() []MetricCategory {
	return []MetricCategory{CategoryCPU, CategoryMemory, CategoryNetworkRX, CategoryNetworkTX, CategoryFilesystem, CategoryRestarts}
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
	case CategoryRestarts:
		return "Restarts"
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
	case CategoryRestarts:
		return "count"
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
		// Nodes have neither workload restart semantics nor the network/filesystem
		// container metrics — node-exporter covers them separately on the Node page.
		return []MetricCategory{CategoryCPU, CategoryMemory, CategoryFilesystem}
	default:
		return AllCategories()
	}
}

// BuildQuery builds a PromQL query for the given resource and metric category.
// Workload kinds return "" here: their pods are resolved by ownership and
// built with BuildScopedQuery.
// For Pods it uses exact name matching.
// For Nodes it matches the node_exporter "instance" label.
func BuildQuery(kind, namespace, name string, category MetricCategory) string {
	return buildQueryInner(kind, namespace, name, category, true)
}

// BuildQueryNoContainerFilter builds the same query as BuildQuery but without
// the container!=” filter. This is used as a fallback for clusters where cAdvisor
// metrics lack the container label (e.g. cri-docker setups).
func BuildQueryNoContainerFilter(kind, namespace, name string, category MetricCategory) string {
	return buildQueryInner(kind, namespace, name, category, false)
}

func buildQueryInner(kind, namespace, name string, category MetricCategory, filterContainer bool) string {
	switch strings.ToLower(kind) {
	case "pod":
		return buildPodQuery(namespace, name, category, filterContainer)
	case "deployment", "statefulset", "daemonset", "replicaset", "job", "cronjob", "rollout", "workflow":
		// A workload's pods are not a function of its name. Resolve membership
		// first and build with BuildScopedQuery.
		return ""
	case "node":
		return buildNodeQuery(name, category)
	default:
		return ""
	}
}

// BuildNamespaceQuery builds a PromQL query for namespace-level aggregation.
func BuildNamespaceQuery(namespace string, category MetricCategory) string {
	return buildNamespaceQueryInner(namespace, category, true)
}

// BuildNamespaceQueryNoContainerFilter is the fallback variant without container!=” filter.
func BuildNamespaceQueryNoContainerFilter(namespace string, category MetricCategory) string {
	return buildNamespaceQueryInner(namespace, category, false)
}

func buildNamespaceQueryInner(namespace string, category MetricCategory, filterContainer bool) string {
	ns := SanitizeLabelValue(namespace)
	cf := ""
	if filterContainer {
		cf = "container!='',"
	}
	switch category {
	case CategoryCPU:
		return fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%snamespace='%s'}[5m]))`, cf, ns)
	case CategoryMemory:
		return fmt.Sprintf(`sum(max by (namespace,pod,container) (container_memory_working_set_bytes{%snamespace='%s'}))`, cf, ns)
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
	return buildClusterQueryInner(category, true)
}

// BuildClusterQueryNoContainerFilter is the fallback variant without container!=” filter.
func BuildClusterQueryNoContainerFilter(category MetricCategory) string {
	return buildClusterQueryInner(category, false)
}

func buildClusterQueryInner(category MetricCategory, filterContainer bool) string {
	cf := ""
	if filterContainer {
		cf = "container!=''"
	}
	switch category {
	case CategoryCPU:
		return fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s}[5m]))`, cf)
	case CategoryMemory:
		return fmt.Sprintf(`sum(max by (namespace,pod,container) (container_memory_working_set_bytes{%s}))`, cf)
	case CategoryNetworkRX:
		return `sum(rate(container_network_receive_bytes_total[5m]))`
	case CategoryNetworkTX:
		return `sum(rate(container_network_transmit_bytes_total[5m]))`
	default:
		return ""
	}
}

// CategoryUsesContainerFilter returns true if the category's queries include
// the container!=” filter that may need a fallback on cri-docker clusters.
func CategoryUsesContainerFilter(category MetricCategory) bool {
	return category == CategoryCPU || category == CategoryMemory
}

func buildPodQuery(namespace, podName string, category MetricCategory, filterContainer bool) string {
	ns := SanitizeLabelValue(namespace)
	pod := SanitizeLabelValue(podName)
	cf := ""
	if filterContainer {
		cf = "container!='',"
	}

	switch category {
	case CategoryRestarts:
		// changes() counts samples that differ from the one before, so several
		// restarts between two scrapes read as one. buildPodSetQueryInner
		// counts the same category with increase() instead; the two disagree
		// and only that one is exact. The long window keeps the chart legible,
		// since most pods never restart, and the sum across containers gives a
		// multi-container pod one line.
		return fmt.Sprintf(
			`sum by (pod,namespace) (changes(kube_pod_container_status_restarts_total{namespace='%s',pod='%s'}[1h]))`,
			ns, pod)
	case CategoryCPU:
		return fmt.Sprintf(
			`sum(rate(container_cpu_usage_seconds_total{%snamespace='%s',pod='%s'}[5m])) by (pod,namespace)`,
			cf, ns, pod)
	case CategoryMemory:
		return fmt.Sprintf(
			`sum by (pod,namespace) (max by (pod,namespace,container) (container_memory_working_set_bytes{%snamespace='%s',pod='%s'}))`,
			cf, ns, pod)
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

func buildNodeQuery(nodeName string, category MetricCategory) string {
	// Node exporter metrics use the "instance" label which is typically set to the node
	// name or IP. The value often includes a port suffix, so we match with an optional port.
	// This heuristic works for most standard deployments; clusters with custom relabeling
	// may need the --prometheus-url flag plus adjusted recording rules.
	sanitized := EscapeRegexMeta(SanitizeLabelValue(nodeName))
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

// BuildHPAReplicasQueries returns the kube-state-metrics series behind an
// HPA's replica history: the replica count it currently observes and the
// count it has decided on.
func BuildHPAReplicasQueries(namespace, name string) (current, desired string) {
	ns := SanitizeLabelValue(namespace)
	n := SanitizeLabelValue(name)
	current = fmt.Sprintf(`kube_horizontalpodautoscaler_status_current_replicas{namespace="%s",horizontalpodautoscaler="%s"}`, ns, n)
	desired = fmt.Sprintf(`kube_horizontalpodautoscaler_status_desired_replicas{namespace="%s",horizontalpodautoscaler="%s"}`, ns, n)
	return current, desired
}

// BuildPodSetQuery aggregates one category over an explicit pod set: the
// pods a caller has already resolved through the workload's selector, so a
// Deployment named api never picks up api-worker pods the way the
// "<name>-.*" workload pattern does. Pods match as pod=~'^(a|b|c)$' inside
// namespace; the result is a single sum series. Empty when pods is empty or
// the category has no expression.
func BuildPodSetQuery(namespace string, pods []string, category MetricCategory) string {
	return buildPodSetQueryInner(namespace, pods, category, true)
}

// BuildPodSetQueryNoContainerFilter is BuildPodSetQuery without the
// container!=” filter, for clusters whose cAdvisor metrics lack the label.
func BuildPodSetQueryNoContainerFilter(namespace string, pods []string, category MetricCategory) string {
	return buildPodSetQueryInner(namespace, pods, category, false)
}

func buildPodSetQueryInner(namespace string, pods []string, category MetricCategory, filterContainer bool) string {
	if len(pods) == 0 {
		return ""
	}
	// The vitals keep to the three categories the diagnose bundle promises.
	switch category {
	case CategoryCPU, CategoryMemory, CategoryRestarts:
		return BuildScopedQuery(SelectPods(namespace, pods), category, AggregateTotal, filterContainer)
	default:
		return ""
	}
}
