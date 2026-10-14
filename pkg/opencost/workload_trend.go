package opencost

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/skyhook-io/radar/pkg/prom"
)

type WorkloadTrendOptions struct {
	Range     string
	Namespace string
	Kind      string
	Name      string
}

func ComputeWorkloadCostTrendFromProm(ctx context.Context, client *prom.Client, opts WorkloadTrendOptions) *WorkloadCostTrendResponse {
	kind, ok := CanonicalWorkloadKind(opts.Kind)
	resp := &WorkloadCostTrendResponse{
		Namespace: opts.Namespace,
		Kind:      kind,
		Name:      opts.Name,
	}
	if client == nil {
		resp.Available = false
		resp.Reason = ReasonNoPrometheus
		return resp
	}
	if opts.Namespace == "" || opts.Name == "" || !ok {
		resp.Available = false
		resp.Reason = ReasonQueryError
		return resp
	}

	start, end, step, label := resolveTrendRange(opts.Range)
	resp.Range = label

	query := buildWorkloadTrendQuery(opts.Namespace, kind, opts.Name, false)
	result, err := client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Print("[opencost] workload trend range query failed; trying opencost_container_* fallback")
		result, err = client.QueryRange(ctx, buildWorkloadTrendQuery(opts.Namespace, kind, opts.Name, true), start, end, step)
		if err != nil {
			log.Print("[opencost] workload trend fallback query failed")
			resp.Available = false
			resp.Reason = ReasonQueryError
			return resp
		}
	}
	if result == nil || len(result.Series) == 0 {
		resp.Available = false
		resp.Reason = ReasonNoMetrics
		return resp
	}

	points := mergeCostSeries(result.Series)
	if len(points) == 0 {
		resp.Available = false
		resp.Reason = ReasonNoMetrics
		return resp
	}

	resp.Available = true
	resp.DataPoints = points
	resp.WindowTotalCost = roundTo(integrateHourlyCost(points), 4)
	return resp
}

func buildWorkloadTrendQuery(namespace, kind, name string, fallback bool) string {
	safeNS := prom.SanitizeLabelValue(namespace)
	safeName := prom.SanitizeLabelValue(name)
	podCost := workloadPodCostExpr(safeNS, fallback)

	if kind == "Deployment" {
		return fmt.Sprintf(`sum(
  (%s)
  * on(namespace, pod) group_left(replicaset)
    max by (namespace, pod, replicaset) (
      label_replace(kube_pod_owner{namespace="%s", owner_kind="ReplicaSet", owner_is_controller="true"}, "replicaset", "$1", "owner_name", "(.+)")
    )
  * on(namespace, replicaset) group_left()
    max by (namespace, replicaset) (
      kube_replicaset_owner{namespace="%s", owner_kind="Deployment", owner_name="%s", owner_is_controller="true"}
    )
)`, podCost, safeNS, safeNS, safeName)
	}

	return fmt.Sprintf(`sum(
  (%s)
  * on(namespace, pod) group_left(owner_kind, owner_name)
    max by (namespace, pod, owner_kind, owner_name) (
      kube_pod_owner{namespace="%s", owner_kind="%s", owner_name="%s", owner_is_controller="true"}
    )
)`, podCost, safeNS, kind, safeName)
}

func workloadPodCostExpr(namespace string, fallback bool) string {
	if fallback {
		return fmt.Sprintf(`sum by (namespace, pod) (
  (label_replace(rate(opencost_container_cpu_cost_total{exported_namespace="%s"}[1h]), "namespace", "$1", "exported_namespace", "(.+)")
    or rate(opencost_container_cpu_cost_total{namespace="%s", exported_namespace=""}[1h]))
) + sum by (namespace, pod) (
  (label_replace(rate(opencost_container_memory_cost_total{exported_namespace="%s"}[1h]), "namespace", "$1", "exported_namespace", "(.+)")
    or rate(opencost_container_memory_cost_total{namespace="%s", exported_namespace=""}[1h]))
)`, namespace, namespace, namespace, namespace)
	}

	return fmt.Sprintf(`sum by (namespace, pod) (
  (label_replace((sum_over_time(container_cpu_allocation{exported_namespace="%s"}[1h:1m]) / 60), "namespace", "$1", "exported_namespace", "(.+)")
    or (sum_over_time(container_cpu_allocation{namespace="%s", exported_namespace=""}[1h:1m]) / 60))
  * on(node) group_left() `+nodeCPUHourlyCostExpr+`
) + sum by (namespace, pod) (
  (label_replace((sum_over_time(container_memory_allocation_bytes{exported_namespace="%s"}[1h:1m]) / 60), "namespace", "$1", "exported_namespace", "(.+)")
    or (sum_over_time(container_memory_allocation_bytes{namespace="%s", exported_namespace=""}[1h:1m]) / 60))
  / 1073741824 * on(node) group_left() `+nodeRAMHourlyCostExpr+`
)`, namespace, namespace, namespace, namespace)
}

func mergeCostSeries(series []prom.Series) []CostDataPoint {
	byTS := make(map[int64]float64)
	for _, s := range series {
		for _, dp := range s.DataPoints {
			byTS[dp.Timestamp] += dp.Value
		}
	}
	points := make([]CostDataPoint, 0, len(byTS))
	for ts, val := range byTS {
		points = append(points, CostDataPoint{Timestamp: ts, Value: roundTo(val, 4)})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Timestamp < points[j].Timestamp })
	return points
}

func integrateHourlyCost(points []CostDataPoint) float64 {
	if len(points) < 2 {
		return 0
	}
	var total float64
	for i := 1; i < len(points); i++ {
		deltaSeconds := points[i].Timestamp - points[i-1].Timestamp
		if deltaSeconds <= 0 {
			continue
		}
		total += points[i].Value * (float64(deltaSeconds) / 3600)
	}
	return total
}

func CanonicalWorkloadKind(kind string) (string, bool) {
	switch kind {
	case "Deployment", "deployment", "deployments":
		return "Deployment", true
	case "StatefulSet", "statefulset", "statefulsets":
		return "StatefulSet", true
	case "DaemonSet", "daemonset", "daemonsets":
		return "DaemonSet", true
	default:
		return "", false
	}
}
