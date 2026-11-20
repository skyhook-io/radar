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
	// Scope keeps the query to this cluster; the zero value leaves it as it
	// always was.
	Scope StoreScope
}

func ComputeWorkloadCostTrendFromProm(ctx context.Context, client *prom.Client, opts WorkloadTrendOptions) *WorkloadCostTrendResponse {
	if client == nil {
		return computeWorkloadCostTrendFromProm(ctx, nil, opts)
	}
	q := &costQuerier{client: client}
	resp := computeWorkloadCostTrendFromProm(ctx, q, opts)
	if opts.Scope.partial(q) {
		resp = &WorkloadCostTrendResponse{Namespace: resp.Namespace, Kind: resp.Kind, Name: resp.Name, Range: resp.Range, Reason: ReasonPartialData}
	}
	resp.ClusterScope = opts.Scope.ClusterScope
	return resp
}

func computeWorkloadCostTrendFromProm(ctx context.Context, client *costQuerier, opts WorkloadTrendOptions) *WorkloadCostTrendResponse {
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

	f := opts.Scope.Filter
	query := buildWorkloadTrendQuery(f, opts.Namespace, kind, opts.Name, false)
	result, err := client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Print("[opencost] workload trend range query failed; trying opencost_container_* fallback")
		result, err = client.QueryRange(ctx, buildWorkloadTrendQuery(f, opts.Namespace, kind, opts.Name, true), start, end, step)
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

func buildWorkloadTrendQuery(f prom.ClusterFilter, namespace, kind, name string, fallback bool) string {
	safeNS := prom.SanitizeLabelValue(namespace)
	safeName := prom.SanitizeLabelValue(name)
	podCost := workloadPodCostExpr(f, safeNS, fallback)

	if kind == "Deployment" {
		return fmt.Sprintf(`sum(
  (%s)
  * on(namespace, pod) group_left(replicaset)
    max by (namespace, pod, replicaset) (
      label_replace(%s, "replicaset", "$1", "owner_name", "(.+)")
    )
  * on(namespace, replicaset) group_left()
    max by (namespace, replicaset) (
      %s
    )
)`, podCost,
			sel(f, MetricPodOwner, `namespace="`+safeNS+`", owner_kind="ReplicaSet", owner_is_controller="true"`),
			sel(f, MetricReplicaSetOwner, `namespace="`+safeNS+`", owner_kind="Deployment", owner_name="`+safeName+`", owner_is_controller="true"`))
	}

	return fmt.Sprintf(`sum(
  (%s)
  * on(namespace, pod) group_left(owner_kind, owner_name)
    max by (namespace, pod, owner_kind, owner_name) (
      %s
    )
)`, podCost, sel(f, MetricPodOwner, `namespace="`+safeNS+`", owner_kind="`+kind+`", owner_name="`+safeName+`", owner_is_controller="true"`))
}

func workloadPodCostExpr(f prom.ClusterFilter, namespace string, fallback bool) string {
	exported := `exported_namespace="` + namespace + `"`
	own := `namespace="` + namespace + `", exported_namespace=""`
	if fallback {
		return fmt.Sprintf(`sum by (namespace, pod) (
  (label_replace(rate(%s[1h]), "namespace", "$1", "exported_namespace", "(.+)")
    or rate(%s[1h]))
) + sum by (namespace, pod) (
  (label_replace(rate(%s[1h]), "namespace", "$1", "exported_namespace", "(.+)")
    or rate(%s[1h]))
)`, sel(f, MetricCPUCostTotal, exported), sel(f, MetricCPUCostTotal, own), sel(f, MetricMemoryCostTotal, exported), sel(f, MetricMemoryCostTotal, own))
	}

	return fmt.Sprintf(`sum by (namespace, pod) (
  (label_replace((sum_over_time(%s[1h:1m]) / 60), "namespace", "$1", "exported_namespace", "(.+)")
    or (sum_over_time(%s[1h:1m]) / 60))
  * on(node) group_left() %s
) + sum by (namespace, pod) (
  (label_replace((sum_over_time(%s[1h:1m]) / 60), "namespace", "$1", "exported_namespace", "(.+)")
    or (sum_over_time(%s[1h:1m]) / 60))
  / 1073741824 * on(node) group_left() %s
)`, sel(f, MetricCPUAllocation, exported), sel(f, MetricCPUAllocation, own), nodeCPUHourlyCostExpr(f),
		sel(f, MetricMemoryAllocation, exported), sel(f, MetricMemoryAllocation, own), nodeRAMHourlyCostExpr(f))
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
