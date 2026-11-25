package openobserve

import (
	"fmt"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
)

// collectorAgentMetrics are the metrics the OpenObserve collector's agent
// DaemonSet ships (kubeletstats and hostmetrics receivers). They are the only
// collector metrics that carry k8s_cluster_uid; everything the gateway scrapes
// (kube-state-metrics kube_*, cAdvisor container_fs_*, apiserver, coredns)
// carries the cluster name only.
var collectorAgentMetrics = func() map[string]bool {
	names := []string{
		"container_cpu_time", "container_cpu_usage",
		"container_memory_available", "container_memory_rss", "container_memory_usage", "container_memory_working_set",
		"k8s_node_cpu_time", "k8s_node_cpu_usage",
		"k8s_node_filesystem_available", "k8s_node_filesystem_capacity", "k8s_node_filesystem_usage",
		"k8s_node_memory_available", "k8s_node_memory_major_page_faults", "k8s_node_memory_page_faults",
		"k8s_node_memory_rss", "k8s_node_memory_usage", "k8s_node_memory_working_set",
		"k8s_node_network_errors", "k8s_node_network_io",
		"k8s_pod_cpu_time", "k8s_pod_cpu_usage", "k8s_pod_cpu_limit_utilization", "k8s_pod_cpu_request_utilization",
		"k8s_pod_filesystem_available", "k8s_pod_filesystem_capacity", "k8s_pod_filesystem_usage",
		"k8s_pod_memory_available", "k8s_pod_memory_major_page_faults", "k8s_pod_memory_page_faults",
		"k8s_pod_memory_rss", "k8s_pod_memory_usage", "k8s_pod_memory_working_set",
		"k8s_pod_memory_limit_utilization", "k8s_pod_memory_request_utilization",
		"k8s_pod_network_errors", "k8s_pod_network_io",
		"k8s_volume_available", "k8s_volume_capacity", "k8s_volume_inodes", "k8s_volume_inodes_free", "k8s_volume_inodes_used",
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}()

func isCollectorAgentMetric(name string) bool {
	return collectorAgentMetrics[name] || strings.HasPrefix(name, "system_")
}

type metricLevel int

const (
	levelContainer metricLevel = iota
	levelPod
)

type collectorTranslation struct {
	target string
	// podTarget replaces target when the query selects cAdvisor's pod-level
	// series (container="").
	podTarget string
	level     metricLevel
	extra     []*labels.Matcher
	network   bool
}

func direction(d string) []*labels.Matcher {
	return []*labels.Matcher{labels.MustNewMatcher(labels.MatchEqual, "direction", d)}
}

// collectorTranslations maps Prometheus (cAdvisor) metric names to the
// collector's kubeletstats equivalents with the same unit and type.
var collectorTranslations = map[string]collectorTranslation{
	"container_cpu_usage_seconds_total":       {target: "container_cpu_time", podTarget: "k8s_pod_cpu_time"},
	"container_memory_working_set_bytes":      {target: "container_memory_working_set", podTarget: "k8s_pod_memory_working_set"},
	"container_memory_rss":                    {target: "container_memory_rss", podTarget: "k8s_pod_memory_rss"},
	"container_memory_usage_bytes":            {target: "container_memory_usage", podTarget: "k8s_pod_memory_usage"},
	"container_network_receive_bytes_total":   {target: "k8s_pod_network_io", level: levelPod, extra: direction("receive"), network: true},
	"container_network_transmit_bytes_total":  {target: "k8s_pod_network_io", level: levelPod, extra: direction("transmit"), network: true},
	"container_network_receive_errors_total":  {target: "k8s_pod_network_errors", level: levelPod, extra: direction("receive"), network: true},
	"container_network_transmit_errors_total": {target: "k8s_pod_network_errors", level: levelPod, extra: direction("transmit"), network: true},
}

// collectorNotCollected are metric families the collector has no equivalent for.
var collectorNotCollected = []struct{ prefix, what string }{
	{"container_spec_", "cAdvisor container limits"},
	{"container_network_", "this network counter"},
	{"node_", "node-exporter metrics"},
	{"kubelet_volume_stats_", "volume usage"},
}

// cadvisorOnlyLabels never exist on collector series; a matcher on them is
// dropped rather than made to match nothing.
var cadvisorOnlyLabels = map[string]bool{
	"image": true, "id": true, "name": true, "cpu": true,
	"job": true, "endpoint": true, "metrics_path": true, "service": true,
	"prometheus": true, "prometheus_replica": true,
}

// translateCollector rewrites one lowercased selector to the collector's
// names. Unknown metrics pass through untouched. The returned renames copy
// the collector label names back to Prometheus's in the query output.
func translateCollector(name string, matchers []*labels.Matcher) (string, []*labels.Matcher, []rename, error) {
	t, ok := collectorTranslations[name]
	if !ok {
		for _, nc := range collectorNotCollected {
			if strings.HasPrefix(name, nc.prefix) {
				return "", nil, nil, fmt.Errorf("%w: %s (the OpenObserve collector does not ship %s)", ErrNotCollected, name, nc.what)
			}
		}
		return name, matchers, nil, nil
	}

	target, level := t.target, t.level
	if level == levelContainer && t.podTarget != "" {
		for _, m := range matchers {
			if m.Name == "container" && m.Type == labels.MatchEqual && m.Value == "" {
				target, level = t.podTarget, levelPod
			}
		}
	}

	out := make([]*labels.Matcher, 0, len(matchers)+len(t.extra))
	for _, m := range matchers {
		var to string
		switch m.Name {
		case labels.MetricName:
			continue
		case "namespace":
			to = "k8s_namespace_name"
		case "pod":
			to = "k8s_pod_name"
		case "container":
			if level != levelContainer {
				continue
			}
			to = "k8s_container_name"
		case "node", "instance":
			to = "k8s_node_name"
		case "interface":
			if !t.network {
				continue
			}
			to = m.Name
		default:
			if cadvisorOnlyLabels[m.Name] || overriddenBy(m.Name, t.extra) {
				continue
			}
			to = m.Name
		}
		lm, err := labels.NewMatcher(m.Type, to, m.Value)
		if err != nil {
			return "", nil, nil, fmt.Errorf("openobserve: translate matcher %s: %w", m, err)
		}
		out = append(out, lm)
	}
	for _, e := range t.extra {
		out = append(out, labels.MustNewMatcher(e.Type, e.Name, e.Value))
	}

	renames := []rename{{"k8s_namespace_name", "namespace"}, {"k8s_pod_name", "pod"}}
	if level == levelContainer {
		renames = append(renames, rename{"k8s_container_name", "container"})
	}
	renames = append(renames, rename{"k8s_node_name", "node"})
	return target, out, renames, nil
}

func overriddenBy(name string, extra []*labels.Matcher) bool {
	for _, e := range extra {
		if e.Name == name {
			return true
		}
	}
	return false
}
