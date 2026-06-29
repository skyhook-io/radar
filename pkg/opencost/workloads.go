package opencost

import (
	"context"
	"log"
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

// WorkloadOwner identifies a workload by name and kind.
type WorkloadOwner struct {
	Name string
	Kind string
}

// PodOwnerLookup returns the workload owner for a pod name in a given
// namespace, or (false) if the lookup cannot resolve it. Callers with an
// in-process K8s informer cache supply this directly; callers without can
// satisfy it from any other pod-metadata source. Keeping the dependency
// abstract here keeps pkg/opencost free of k8s.io/client-go.
type PodOwnerLookup func(podName string) (WorkloadOwner, bool)

// ComputeWorkloadsFromProm returns workload-level cost breakdown for a
// namespace, sourced from OpenCost-exported Prometheus metrics with a
// caller-supplied pod→owner mapping (typically from a K8s informer cache).
//
// When ownerLookup is nil or can't resolve a pod, the pod is assigned to a
// fallback "standalone" workload whose name is the pod name with its hash
// suffixes stripped — best-effort grouping for orphan pods.
func ComputeWorkloadsFromProm(ctx context.Context, client *prom.Client, namespace string, ownerLookup PodOwnerLookup) *WorkloadCostResponse {
	if client == nil {
		return &WorkloadCostResponse{Namespace: namespace, Available: false, Reason: ReasonNoPrometheus}
	}
	if namespace == "" {
		return &WorkloadCostResponse{Available: false, Reason: ReasonQueryError}
	}

	safeNS := prom.SanitizeLabelValue(namespace)

	cpuResult, err := client.Query(ctx,
		`sum by (pod) ((avg_over_time(container_cpu_allocation{exported_namespace="`+safeNS+`"}[1h]) or avg_over_time(container_cpu_allocation{namespace="`+safeNS+`", exported_namespace=""}[1h])) * on(node) group_left() node_cpu_hourly_cost)`)
	if err != nil {
		log.Printf("[opencost] workloads CPU query failed for ns=%q, trying opencost_container_cpu_cost_total: %v", namespace, err)
		cpuResult, err = client.Query(ctx,
			`sum by (pod) (rate(opencost_container_cpu_cost_total{exported_namespace="`+safeNS+`"}[1h]) or rate(opencost_container_cpu_cost_total{namespace="`+safeNS+`", exported_namespace=""}[1h]))`)
		if err != nil {
			log.Printf("[opencost] workloads CPU fallback query also failed for ns=%q: %v", namespace, err)
			return &WorkloadCostResponse{Namespace: namespace, Available: false, Reason: ReasonQueryError}
		}
	}

	memResult, err := client.Query(ctx,
		`sum by (pod) ((avg_over_time(container_memory_allocation_bytes{exported_namespace="`+safeNS+`"}[1h]) or avg_over_time(container_memory_allocation_bytes{namespace="`+safeNS+`", exported_namespace=""}[1h])) / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if err != nil {
		log.Printf("[opencost] workloads memory query failed for ns=%q, trying opencost_container_memory_cost_total: %v", namespace, err)
		memResult, err = client.Query(ctx,
			`sum by (pod) (rate(opencost_container_memory_cost_total{exported_namespace="`+safeNS+`"}[1h]) or rate(opencost_container_memory_cost_total{namespace="`+safeNS+`", exported_namespace=""}[1h]))`)
		if err != nil {
			log.Printf("[opencost] workloads memory fallback query also failed for ns=%q: %v", namespace, err)
			return &WorkloadCostResponse{Namespace: namespace, Available: false, Reason: ReasonQueryError}
		}
	}

	cpuUsageResult, cpuUsageErr := client.Query(ctx,
		`sum by (pod) (label_replace(rate(container_cpu_usage_seconds_total{container!="", namespace="`+safeNS+`"}[1h]), "node", "$1", "instance", "(.+?)(?::\\d+)?$") * on(node) group_left() node_cpu_hourly_cost)`)
	if cpuUsageErr != nil {
		log.Printf("[opencost] workloads CPU usage query failed for ns=%q (efficiency will be 0): %v", namespace, cpuUsageErr)
	}
	memUsageResult, memUsageErr := client.Query(ctx,
		`sum by (pod) (label_replace(container_memory_working_set_bytes{container!="", namespace="`+safeNS+`"}, "node", "$1", "instance", "(.+?)(?::\\d+)?$") / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if memUsageErr != nil {
		log.Printf("[opencost] workloads memory usage query failed for ns=%q (efficiency will be 0): %v", namespace, memUsageErr)
	}

	if len(cpuResult.Series) == 0 && len(memResult.Series) == 0 {
		// Queries succeeded but returned nothing — either the namespace has
		// no scraped pods or OpenCost metrics aren't present. Surface the
		// typed reason so the UI can render contextual guidance rather than
		// an empty list.
		return &WorkloadCostResponse{Namespace: namespace, Available: false, Reason: ReasonNoMetrics}
	}

	podCPUUsage := lastValuePerLabel(cpuUsageResult, cpuUsageErr, "pod")
	podMemUsage := lastValuePerLabel(memUsageResult, memUsageErr, "pod")

	type podCost struct {
		cpuCost, memoryCost, cpuUsage, memoryUsage float64
	}
	podCosts := make(map[string]*podCost)
	setPodLast := func(result *prom.QueryResult, set func(*podCost, float64)) {
		if result == nil {
			return
		}
		for _, s := range result.Series {
			pod := s.Labels["pod"]
			if pod == "" || len(s.DataPoints) == 0 {
				continue
			}
			pc, ok := podCosts[pod]
			if !ok {
				pc = &podCost{}
				podCosts[pod] = pc
			}
			set(pc, s.DataPoints[len(s.DataPoints)-1].Value)
		}
	}
	setPodLast(cpuResult, func(pc *podCost, v float64) { pc.cpuCost = v })
	setPodLast(memResult, func(pc *podCost, v float64) { pc.memoryCost = v })
	for pod, pc := range podCosts {
		pc.cpuUsage = podCPUUsage[pod]
		pc.memoryUsage = podMemUsage[pod]
	}

	workloadMap := make(map[WorkloadOwner]*WorkloadCost)
	for podName, pc := range podCosts {
		owner, ok := WorkloadOwner{}, false
		if ownerLookup != nil {
			owner, ok = ownerLookup(podName)
		}
		if !ok {
			owner = WorkloadOwner{Name: stripPodSuffix(podName), Kind: "standalone"}
		}

		wl, exists := workloadMap[owner]
		if !exists {
			wl = &WorkloadCost{Name: owner.Name, Kind: owner.Kind}
			workloadMap[owner] = wl
		}
		wl.CPUCost += pc.cpuCost
		wl.MemoryCost += pc.memoryCost
		wl.CPUUsageCost += pc.cpuUsage
		wl.MemoryUsageCost += pc.memoryUsage
		wl.Replicas++
	}

	workloads := make([]WorkloadCost, 0, len(workloadMap))
	for _, wl := range workloadMap {
		allocCost := wl.CPUCost + wl.MemoryCost
		usageCost := wl.CPUUsageCost + wl.MemoryUsageCost
		wl.HourlyCost = allocCost
		wl.Efficiency = efficiencyPct(usageCost, allocCost)
		wl.IdleCost = idleFromUsage(usageCost, allocCost)
		wl.HourlyCost = roundTo(wl.HourlyCost, 4)
		wl.CPUCost = roundTo(wl.CPUCost, 4)
		wl.MemoryCost = roundTo(wl.MemoryCost, 4)
		wl.CPUUsageCost = roundTo(wl.CPUUsageCost, 4)
		wl.MemoryUsageCost = roundTo(wl.MemoryUsageCost, 4)
		wl.IdleCost = roundTo(wl.IdleCost, 4)
		workloads = append(workloads, *wl)
	}
	sort.Slice(workloads, func(i, j int) bool { return workloads[i].HourlyCost > workloads[j].HourlyCost })

	return &WorkloadCostResponse{
		Available: true,
		Namespace: namespace,
		Workloads: workloads,
	}
}

// stripPodSuffix removes pod hash suffixes to approximate the workload name
// when owner-ref lookup fails. e.g. "myapp-7f8d9c-xyz12" → "myapp".
func stripPodSuffix(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	name = name[:idx]
	idx = strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	return name[:idx]
}
