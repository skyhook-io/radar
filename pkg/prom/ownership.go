package prom

import (
	"fmt"
	"sort"
	"strings"
)

// Pod membership for a workload's metrics is never inferred from a pod's
// name: a Deployment called api must not pick up api-worker pods. A caller
// establishes membership from one of two sources and passes it here as a
// PodSelection.
//
// Pods are the pods the workload controls right now, established from the
// cluster by controller ownership and rendered as pod=~'^(a|b)$'. Membership
// is the population at resolution time: a pod replaced earlier in a chart's
// window is not in it.
type PodSelection struct {
	Namespace string
	Pods      []string
}

// Aggregate says whether a scoped query keeps one series per pod (the
// workload page's charts, whose legends and per-pod overlays need them) or
// sums the pods into one series (the diagnose vitals).
type Aggregate int

const (
	AggregatePerPod Aggregate = iota
	AggregateTotal
)

// IsEmpty reports a selection that can name no pod. Callers should not query
// with it; a query built from it matches nothing.
func (s PodSelection) IsEmpty() bool {
	return len(s.Pods) == 0
}

// SelectPods is the current-pods form of a selection.
func SelectPods(namespace string, pods []string) PodSelection {
	names := append([]string(nil), pods...)
	sort.Strings(names)
	return PodSelection{Namespace: namespace, Pods: names}
}

func exactSetPattern(names []string) string {
	escaped := make([]string, 0, len(names))
	for _, name := range names {
		escaped = append(escaped, EscapeRegexMeta(SanitizeLabelValue(name)))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

// scopeClause renders the selection into a label matcher on the left side of
// a query. An empty selection matches nothing rather than everything: a
// workload whose pods could not be established must not chart its namespace.
func scopeClause(sel PodSelection) string {
	if len(sel.Pods) > 0 {
		return fmt.Sprintf(",pod=~'%s'", exactSetPattern(sel.Pods))
	}
	return ",pod=~'a^'"
}

// BuildScopedQuery builds a workload category over one selection. The
// per-pod form keeps one series per pod for the workload page; the total
// form sums them for the diagnose vitals. filterContainer drops the
// container!=” matcher for clusters whose cAdvisor lacks the label.
func BuildScopedQuery(sel PodSelection, category MetricCategory, agg Aggregate, filterContainer bool) string {
	ns := SanitizeLabelValue(sel.Namespace)
	pods := scopeClause(sel)
	cf := ""
	if filterContainer {
		cf = "container!='',"
	}
	rate := func(metric string) string {
		return fmt.Sprintf(`rate(%s{%snamespace='%s'%s}[5m])`, metric, cf, ns, pods)
	}
	rateNoCF := func(metric string) string {
		return fmt.Sprintf(`rate(%s{namespace='%s'%s}[5m])`, metric, ns, pods)
	}
	switch category {
	case CategoryRestarts:
		if agg == AggregateTotal {
			// The total answers "how many restarts did this workload take":
			// increase() reads the counter delta, so several restarts between
			// two scrapes count as several. Its extrapolation yields
			// fractions, which round() settles per pod before summing.
			return fmt.Sprintf(`sum(round(increase(kube_pod_container_status_restarts_total{namespace='%s'%s}[1h])))`, ns, pods)
		}
		// The per-pod chart answers "when did this pod restart": changes()
		// counts the transitions the samples show, which is what puts a step
		// on the line at the moment it happened. On a fast crashloop it reads
		// lower than the total above, which is the honest difference between
		// counting events and counting restarts.
		return fmt.Sprintf(`sum by (pod,namespace) (changes(kube_pod_container_status_restarts_total{namespace='%s'%s}[1h]))`, ns, pods)
	case CategoryCPU:
		return wrap(agg, rate("container_cpu_usage_seconds_total"))
	case CategoryMemory:
		inner := fmt.Sprintf(`max by (pod,namespace,container) (container_memory_working_set_bytes{%snamespace='%s'%s})`, cf, ns, pods)
		if agg == AggregateTotal {
			return fmt.Sprintf(`sum(%s)`, inner)
		}
		return fmt.Sprintf(`sum by (pod,namespace) (%s)`, inner)
	case CategoryNetworkRX:
		return wrap(agg, rateNoCF("container_network_receive_bytes_total"))
	case CategoryNetworkTX:
		return wrap(agg, rateNoCF("container_network_transmit_bytes_total"))
	case CategoryFilesystem:
		return wrap(agg, rateNoCF("container_fs_writes_bytes_total")+" + "+rateNoCF("container_fs_reads_bytes_total"))
	default:
		return ""
	}
}

func wrap(agg Aggregate, expr string) string {
	if agg == AggregateTotal {
		return fmt.Sprintf(`sum(%s)`, expr)
	}
	return fmt.Sprintf(`sum(%s) by (pod,namespace)`, expr)
}
