package prometheus

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

// Native system metrics are shared across a Cloud Monitoring metrics scope.
// Each selector includes the project, control-plane location and cluster.
type gkeMetrics struct {
	owner      *Client
	service    prom.ServiceInfo
	portName   string
	configured bool
	fallback   bool
	store      *prom.Client
	scope      ClusterScope
	matchers   string
}

func (g *gkeMetrics) series(resource, metric string, selectors ...string) string {
	all := []string{"monitored_resource=" + strconv.Quote(resource), g.matchers}
	all = append(all, selectors...)
	return "kubernetes_io:" + metric + "{" + strings.Join(all, ",") + "}"
}

const gkeRestartsSinceCreation prom.MetricCategory = "restarts_since_creation"

func (g *gkeMetrics) chartQuery(kind, namespace, name string, category prom.MetricCategory, pods *PodScope) string {
	kind = strings.ToLower(kind)
	if kind == "node" {
		if name == "" {
			return ""
		}
		node := "node_name=" + strconv.Quote(name)
		switch category {
		case prom.CategoryCPU:
			return "sum(rate(" + g.series("k8s_node", "node_cpu_core_usage_time", node) + "[5m]))"
		case prom.CategoryMemory:
			return "sum(" + g.series("k8s_node", "node_memory_used_bytes", node, `memory_type="non-evictable"`) + ")"
		case prom.CategoryFilesystem:
			return "sum(" + g.series("k8s_node", "node_ephemeral_storage_used_bytes", node) + ")"
		}
		return ""
	}

	if (kind != "cluster" && namespace == "") || (kind == "pod" && name == "") ||
		(pods == nil && kind != "cluster" && kind != "namespace" && kind != "pod") {
		return ""
	}
	var selectors []string
	if namespace != "" {
		selectors = append(selectors, "namespace_name="+strconv.Quote(namespace))
	}
	switch {
	case pods != nil:
		if len(pods.CurrentPods) == 0 {
			return ""
		}
		names := make([]string, len(pods.CurrentPods))
		for i, p := range pods.CurrentPods {
			names[i] = regexp.QuoteMeta(p)
		}
		selectors = append(selectors, "pod_name=~"+strconv.Quote(strings.Join(names, "|")))
	case kind == "pod":
		selectors = append(selectors, "pod_name="+strconv.Quote(name))
	}
	var expression string
	switch category {
	case prom.CategoryCPU:
		expression = "rate(" + g.series("k8s_container", "container_cpu_core_usage_time", selectors...) + "[5m])"
	case prom.CategoryMemory:
		expression = g.series("k8s_container", "container_memory_used_bytes", append(selectors, `memory_type="non-evictable"`)...)
		if pods != nil {
			// Keep the gauge lookback within the lifetime guard even at coarse evaluation steps.
			expression = "last_over_time(" + expression + "[5m])"
		}
	case gkeRestartsSinceCreation:
		expression = "last_over_time(" + g.series("k8s_container", "container_restart_count", selectors...) + "[1m])"
	case prom.CategoryRestarts:
		expression = "round(increase(" + g.series("k8s_container", "container_restart_count", selectors...) + "[1h]))"
	case prom.CategoryNetworkRX, prom.CategoryNetworkTX:
		metric := "pod_network_received_bytes_count"
		if category == prom.CategoryNetworkTX {
			metric = "pod_network_sent_bytes_count"
		}
		expression = "rate(" + g.series("k8s_pod", metric, selectors...) + "[5m])"
	default:
		return ""
	}
	if pods != nil || kind == "pod" {
		return `sum by (pod,namespace) (label_replace(label_replace(sum by (pod_name,namespace_name) (` + expression + `),"pod","$1","pod_name","(.+)"),"namespace","$1","namespace_name","(.+)"))`
	}
	return "sum(" + expression + ")"
}

func (g *gkeMetrics) volumeQuery(namespace, name, metric string) string {
	return fmt.Sprintf("max(%s)", g.series("k8s_pod", metric, "persistentvolumeclaim_namespace="+strconv.Quote(namespace), "persistentvolumeclaim_name="+strconv.Quote(name)))
}
