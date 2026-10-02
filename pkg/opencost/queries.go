package opencost

// Allocation queries average container_*_allocation over the last hour with
// sum_over_time(x[1h:1m]) / 60 rather than avg_over_time(x[1h]).
// avg_over_time only averages the samples a series has, so a pod that lived
// five minutes counted as if it had run the whole hour; on clusters with
// short-lived pods (CI runners, preview environments) that inflated costs
// several times over. The 1m subquery weights each series by the time it
// was actually present.

const (
	nodeTotalHourlyCostExpr         = `max by (node) (node_total_hourly_cost)`
	nodeTotalHourlyCostMetadataExpr = `max by (node, instance_type, region) (node_total_hourly_cost)`
	nodeCPUHourlyCostExpr           = `max by (node) (node_cpu_hourly_cost)`
	nodeRAMHourlyCostExpr           = `max by (node) (node_ram_hourly_cost)`
	// node_gpu_hourly_cost is exported for every node, GPU or not, so GPU
	// spend is the count times the rate.
	nodeGPUHourlyCostExpr          = `sum(max by (node) (node_gpu_count) * on (node) max by (node) (node_gpu_hourly_cost))`
	// pv_hourly_cost is a per-GiB rate, so it has to be scaled by the volume
	// size to get the volume's hourly cost.
	persistentVolumeHourlyCostExpr = `(max by (persistentvolume) (pv_hourly_cost) * on (persistentvolume) max by (persistentvolume) (kube_persistentvolume_capacity_bytes) / 1073741824)`
	persistentVolumeClaimRef       = `max by (persistentvolume, namespace) (label_replace(kube_persistentvolume_claim_ref, "namespace", "$1", "claim_namespace", "(.+)"))`
)
