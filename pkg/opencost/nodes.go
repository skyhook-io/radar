package opencost

import (
	"context"
	"log"
	"sort"

	"github.com/skyhook-io/radar/pkg/prom"
)

// ComputeNodeCosts returns per-node hourly cost breakdown sourced from the
// OpenCost-exported Prometheus metrics (node_total_hourly_cost,
// node_cpu_hourly_cost, node_ram_hourly_cost). Sorted descending by hourly
// cost. Errors map to typed Reason values; never returned to callers because
// the HTTP layer serves them in-band.
func ComputeNodeCosts(ctx context.Context, client *prom.Client) *NodeCostResponse {
	if client == nil {
		return &NodeCostResponse{Available: false, Reason: ReasonNoPrometheus}
	}

	totalResult, err := client.Query(ctx, `node_total_hourly_cost`)
	if err != nil {
		log.Printf("[opencost] node_total_hourly_cost query failed: %v", err)
		return &NodeCostResponse{Available: false, Reason: ReasonQueryError}
	}
	if len(totalResult.Series) == 0 {
		return &NodeCostResponse{Available: false, Reason: ReasonNoMetrics}
	}

	cpuResult, cpuErr := client.Query(ctx, `node_cpu_hourly_cost`)
	cpuMap := lastValuePerLabel(cpuResult, cpuErr, "node")
	memResult, memErr := client.Query(ctx, `node_ram_hourly_cost`)
	memMap := lastValuePerLabel(memResult, memErr, "node")

	nodes := make([]NodeCost, 0, len(totalResult.Series))
	for _, s := range totalResult.Series {
		node := s.Labels["node"]
		if node == "" || len(s.DataPoints) == 0 {
			continue
		}
		nodes = append(nodes, NodeCost{
			Name:         node,
			InstanceType: s.Labels["instance_type"],
			Region:       s.Labels["region"],
			HourlyCost:   roundTo(s.DataPoints[len(s.DataPoints)-1].Value, 4),
			CPUCost:      roundTo(cpuMap[node], 4),
			MemoryCost:   roundTo(memMap[node], 4),
		})
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].HourlyCost > nodes[j].HourlyCost })

	return &NodeCostResponse{Available: true, Nodes: nodes}
}

func lastValuePerLabel(result *prom.QueryResult, err error, label string) map[string]float64 {
	out := make(map[string]float64)
	if err != nil || result == nil {
		return out
	}
	for _, s := range result.Series {
		v := s.Labels[label]
		if v == "" || len(s.DataPoints) == 0 {
			continue
		}
		out[v] = s.DataPoints[len(s.DataPoints)-1].Value
	}
	return out
}
