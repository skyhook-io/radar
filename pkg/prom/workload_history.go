package prom

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type WorkloadHistory struct {
	Kind, Namespace, Name string
	Scope                 WorkloadMetricsScope
	Recorded              bool
}

func (h WorkloadHistory) OwnerQuery() (string, error) {
	cluster, err := h.Scope.Matchers()
	if err != nil {
		return "", err
	}
	if h.Namespace == "" || h.Name == "" {
		return "", fmt.Errorf("workload history requires namespace and name")
	}
	kind := ""
	switch strings.ToLower(h.Kind) {
	case "deployment":
		kind = "Deployment"
	case "statefulset":
		kind = "StatefulSet"
	case "daemonset":
		kind = "DaemonSet"
	default:
		return "", fmt.Errorf("unsupported historical workload kind %q", h.Kind)
	}
	ns := "namespace=" + strconv.Quote(h.Namespace)
	metric := func(name, target string) string { return workloadMetricSelector(name, cluster, target) }
	if h.Recorded {
		return "max by (namespace,pod) (" + metric("namespace_workload_pod:kube_pod_owner:relabel", ns+",workload="+strconv.Quote(h.Name)+",workload_type=~"+strconv.Quote(strings.ToLower(kind)+"|"+kind)) + ")", nil
	}
	if kind == "Deployment" {
		return `label_replace(max by (namespace,pod,owner_name) (` + metric("kube_pod_owner", ns+`,owner_kind="ReplicaSet",owner_is_controller="true"`) + `), "replicaset", "$1", "owner_name", "(.*)") * on (namespace,replicaset) group_left() max by (namespace,replicaset) (` + metric("kube_replicaset_owner", ns+`,owner_kind="Deployment",owner_name=`+strconv.Quote(h.Name)+`,owner_is_controller="true"`) + `)`, nil
	}
	return "max by (namespace,pod) (" + metric("kube_pod_owner", ns+",owner_kind="+strconv.Quote(kind)+",owner_name="+strconv.Quote(h.Name)+`,owner_is_controller="true"`) + ")", nil
}

func (h WorkloadHistory) expression(metric, target, window string, beyla bool) (string, error) {
	owner, err := h.OwnerQuery()
	if err != nil {
		return "", err
	}
	cluster, _ := h.Scope.Matchers()
	expr := workloadMetricExpression(metric, cluster, target, nil, window)
	if beyla {
		expr = `label_replace(label_replace(` + expr + `,"namespace","$1","k8s_namespace_name","(.*)"),"pod","$1","k8s_pod_name","(.*)")`
	}
	return "(" + expr + ") and on (namespace,pod) (" + owner + ")", nil
}

func BuildHistoryResourceQuery(step time.Duration, h WorkloadHistory, category MetricCategory) (string, error) {
	target := "namespace=" + strconv.Quote(h.Namespace) + `,container!="",container!="POD"`
	metric, window := "container_memory_working_set_bytes", ""
	if category == CategoryCPU {
		metric, window = "container_cpu_usage_seconds_total", WorkloadRateWindow(step).String()
	} else if category != CategoryMemory {
		return "", fmt.Errorf("unsupported workload history category %q", category)
	}
	expr, err := h.expression(metric, target, window, false)
	if err != nil {
		return "", err
	}
	pods := "sum by (pod) (max by (pod,container) (" + expr + "))"
	return workloadAggregateLines("sum("+pods+")", "max("+pods+")"), nil
}

func BuildHistoryThrottleQuery(step time.Duration, h WorkloadHistory) (string, error) {
	target := "namespace=" + strconv.Quote(h.Namespace) + `,container!="",container!="POD"`
	numerator, err := h.expression("container_cpu_cfs_throttled_periods_total", target, WorkloadRateWindow(step).String(), false)
	if err != nil {
		return "", err
	}
	denominator, _ := h.expression("container_cpu_cfs_periods_total", target, WorkloadRateWindow(step).String(), false)
	numerator = "sum by (pod) (max by (pod,container) (" + numerator + "))"
	denominator = "sum by (pod) (max by (pod,container) (" + denominator + "))"
	return workloadAggregateLines("100 * sum("+numerator+") / sum("+denominator+")", "max(100 * ("+numerator+") / ("+denominator+"))"), nil
}

func workloadAggregateLines(total, maximum string) string {
	return `label_replace((` + total + `),"aggregation","Workload","",".*") or label_replace((` + maximum + `),"aggregation","Maximum Pod","",".*")`
}

func BuildHistoryResourcePopulationQuery(step time.Duration, h WorkloadHistory, family string) (string, error) {
	metrics := map[string][]string{
		"cpu":        {"container_cpu_usage_seconds_total"},
		"memory":     {"container_memory_working_set_bytes"},
		"throttling": {"container_cpu_cfs_periods_total", "container_cpu_cfs_throttled_periods_total"},
	}[family]
	if len(metrics) == 0 {
		return "", fmt.Errorf("unsupported resource family %q", family)
	}
	window := WorkloadRateWindow(step).String()
	if family == "memory" {
		window = ""
	}
	parts := make([]string, 0, len(metrics))
	for _, metric := range metrics {
		expr, err := h.expression(metric, "namespace="+strconv.Quote(h.Namespace)+`,container!="",container!="POD"`, window, false)
		if err != nil {
			return "", err
		}
		parts = append(parts, expr)
	}
	return "count(count by (job,replica,prometheus_replica,__replica__) (" + strings.Join(parts, " or ") + "))", nil
}
