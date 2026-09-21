package opencost

const (
	nodeTotalHourlyCostExpr         = `max by (node) (node_total_hourly_cost)`
	nodeTotalHourlyCostMetadataExpr = `max by (node, instance_type, region) (node_total_hourly_cost)`
	nodeCPUHourlyCostExpr           = `max by (node) (node_cpu_hourly_cost)`
	nodeRAMHourlyCostExpr           = `max by (node) (node_ram_hourly_cost)`
	// node_gpu_hourly_cost is exported for every node, GPU or not, so GPU
	// spend is the count times the rate.
	nodeGPUHourlyCostExpr          = `sum(max by (node) (node_gpu_count) * on (node) max by (node) (node_gpu_hourly_cost))`
	persistentVolumeHourlyCostExpr = `max by (persistentvolume) (pv_hourly_cost)`
	persistentVolumeClaimRef       = `max by (persistentvolume, namespace) (label_replace(kube_persistentvolume_claim_ref, "namespace", "$1", "claim_namespace", "(.+)"))`
)
