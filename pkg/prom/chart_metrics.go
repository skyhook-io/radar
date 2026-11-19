package prom

import "strings"

// ClusterFilter returns the matchers that keep one metric's series to one
// cluster in a store several clusters share, or "" to leave it unfiltered.
// A nil filter leaves every metric unfiltered, so the builders produce
// exactly the queries they did before filters existed.
type ClusterFilter func(metric string) string

func (f ClusterFilter) of(metric string) string {
	if f == nil {
		return ""
	}
	return f(metric)
}

// and is ",<matchers>" for a filtered metric, else "".
func (f ClusterFilter) and(metric string) string {
	if m := f.of(metric); m != "" {
		return "," + m
	}
	return ""
}

// The metrics the chart and vitals builders read.
const (
	MetricCPUUsage           = "container_cpu_usage_seconds_total"
	MetricMemoryWorkingSet   = "container_memory_working_set_bytes"
	MetricNetworkReceive     = "container_network_receive_bytes_total"
	MetricNetworkTransmit    = "container_network_transmit_bytes_total"
	MetricFSWrites           = "container_fs_writes_bytes_total"
	MetricFSReads            = "container_fs_reads_bytes_total"
	MetricContainerRestarts  = "kube_pod_container_status_restarts_total"
	MetricNodeCPU            = "node_cpu_seconds_total"
	MetricNodeMemTotal       = "node_memory_MemTotal_bytes"
	MetricNodeMemAvailable   = "node_memory_MemAvailable_bytes"
	MetricNodeFSSize         = "node_filesystem_size_bytes"
	MetricNodeFSAvail        = "node_filesystem_avail_bytes"
	MetricHPACurrentReplicas = "kube_horizontalpodautoscaler_status_current_replicas"
	MetricHPADesiredReplicas = "kube_horizontalpodautoscaler_status_desired_replicas"
)

// CategoryMetrics names the metrics one category's chart query reads for a
// kind: node-exporter for a Node, cAdvisor (and kube-state-metrics restarts)
// for anything else, including the namespace and cluster aggregates.
func CategoryMetrics(kind string, category MetricCategory) []string {
	if strings.EqualFold(kind, "node") {
		switch category {
		case CategoryCPU:
			return []string{MetricNodeCPU}
		case CategoryMemory:
			return []string{MetricNodeMemTotal, MetricNodeMemAvailable}
		case CategoryFilesystem:
			return []string{MetricNodeFSSize, MetricNodeFSAvail}
		}
		return nil
	}
	switch category {
	case CategoryCPU:
		return []string{MetricCPUUsage}
	case CategoryMemory:
		return []string{MetricMemoryWorkingSet}
	case CategoryNetworkRX:
		return []string{MetricNetworkReceive}
	case CategoryNetworkTX:
		return []string{MetricNetworkTransmit}
	case CategoryFilesystem:
		return []string{MetricFSWrites, MetricFSReads}
	case CategoryRestarts:
		return []string{MetricContainerRestarts}
	}
	return nil
}
