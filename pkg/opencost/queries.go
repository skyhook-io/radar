package opencost

import "github.com/skyhook-io/radar/pkg/prom"

// Allocation queries average container_*_allocation over the last hour with
// sum_over_time(x[1h:1m]) / 60 rather than avg_over_time(x[1h]).
// avg_over_time only averages the samples a series has, so a pod that lived
// five minutes counted as if it had run the whole hour; on clusters with
// short-lived pods (CI runners, preview environments) that inflated costs
// several times over. The 1m subquery weights each series by the time it
// was actually present.

func nodeTotalHourlyCostExpr(f prom.ClusterFilter) string {
	return `max by (node) (` + sel(f, MetricNodeTotalHourlyCost, "") + `)`
}

func nodeTotalHourlyCostMetadataExpr(f prom.ClusterFilter) string {
	return `max by (node, instance_type, region) (` + sel(f, MetricNodeTotalHourlyCost, "") + `)`
}

func nodeCPUHourlyCostExpr(f prom.ClusterFilter) string {
	return `max by (node) (` + sel(f, MetricNodeCPUHourlyCost, "") + `)`
}

func nodeRAMHourlyCostExpr(f prom.ClusterFilter) string {
	return `max by (node) (` + sel(f, MetricNodeRAMHourlyCost, "") + `)`
}

// node_gpu_hourly_cost is exported for every node, GPU or not, so GPU spend
// is the count times the rate.
func nodeGPUHourlyCostExpr(f prom.ClusterFilter) string {
	return `sum(max by (node) (` + sel(f, MetricNodeGPUCount, "") + `) * on (node) max by (node) (` + sel(f, MetricNodeGPUHourlyCost, "") + `))`
}

// pv_hourly_cost is a per-GiB rate, so it has to be scaled by the volume size
// to get the volume's hourly cost.
func persistentVolumeHourlyCostExpr(f prom.ClusterFilter) string {
	return `(max by (persistentvolume) (` + sel(f, MetricPVHourlyCost, "") + `) * on (persistentvolume) max by (persistentvolume) (` + sel(f, MetricPVCapacityBytes, "") + `) / 1073741824)`
}

// unsizedPersistentVolumeExpr lists priced volumes whose size is unknown.
func unsizedPersistentVolumeExpr(f prom.ClusterFilter) string {
	return `(max by (persistentvolume) (` + sel(f, MetricPVHourlyCost, "") + `) unless on (persistentvolume) ` + sel(f, MetricPVCapacityBytes, "") + `)`
}

func persistentVolumeClaimRef(f prom.ClusterFilter) string {
	return `max by (persistentvolume, namespace) (label_replace(` + sel(f, MetricPVClaimRef, "") + `, "namespace", "$1", "claim_namespace", "(.+)"))`
}
