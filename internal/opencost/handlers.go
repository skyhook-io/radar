package opencost

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// RegisterRoutes registers OpenCost routes on the given router.
func RegisterRoutes(r chi.Router) {
	r.Get("/opencost/summary", handleSummary)
	r.Get("/opencost/workloads", handleWorkloads)
	r.Get("/opencost/trend", handleTrend)
	r.Get("/opencost/nodes", handleNodes)
}

// handleSummary returns namespace-level cost summary from OpenCost Prometheus metrics.
func handleSummary(w http.ResponseWriter, r *http.Request) {
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, CostSummary{Available: false, Reason: ReasonNoPrometheus})
		return
	}

	// Check if Prometheus is reachable (triggers discovery if needed)
	_, _, err := client.EnsureConnected(r.Context())
	if err != nil {
		log.Printf("[opencost] EnsureConnected failed (summary): %v", err)
		writeJSON(w, http.StatusOK, CostSummary{Available: false, Reason: ReasonNoPrometheus})
		return
	}

	// Query per-namespace CPU cost
	// container_cpu_allocation is a gauge (current allocated cores), not a counter — use avg_over_time.
	// label_replace handles honor_labels=false setups where Prometheus renames the original
	// namespace label to exported_namespace and sets namespace to the scrape target's namespace.
	cpuResult, err := client.Query(r.Context(),
		`sum by (namespace) (label_replace(avg_over_time(container_cpu_allocation{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") * on(node) group_left() node_cpu_hourly_cost)`)
	if err != nil {
		// Try the opencost_container metric name variant (this IS a counter, so rate is correct)
		cpuResult, err = client.Query(r.Context(),
			`sum by (namespace) (label_replace(rate(opencost_container_cpu_cost_total[1h]), "namespace", "$1", "exported_namespace", "(.+)"))`)
		if err != nil {
			log.Printf("[opencost] CPU cost query failed: %v", err)
			writeJSON(w, http.StatusOK, CostSummary{Available: false, Reason: ReasonQueryError})
			return
		}
	}

	// Query per-namespace memory cost
	// container_memory_allocation_bytes is a gauge — use avg_over_time
	memResult, err := client.Query(r.Context(),
		`sum by (namespace) (label_replace(avg_over_time(container_memory_allocation_bytes{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if err != nil {
		// Try the opencost_container metric name variant (this IS a counter, so rate is correct)
		memResult, err = client.Query(r.Context(),
			`sum by (namespace) (label_replace(rate(opencost_container_memory_cost_total[1h]), "namespace", "$1", "exported_namespace", "(.+)"))`)
		if err != nil {
			log.Printf("[opencost] Memory cost query failed: %v", err)
			writeJSON(w, http.StatusOK, CostSummary{Available: false, Reason: ReasonQueryError})
			return
		}
	}

	// If both queries returned empty results, OpenCost metrics aren't available
	if len(cpuResult.Series) == 0 && len(memResult.Series) == 0 {
		writeJSON(w, http.StatusOK, CostSummary{Available: false, Reason: ReasonNoMetrics})
		return
	}

	// Query actual CPU usage cost (for efficiency calculation)
	// cAdvisor metrics use "instance" for the node hostname, while OpenCost uses "node",
	// so we label_replace to bridge the join.
	cpuUsageMap := make(map[string]float64)
	cpuUsageResult, err := client.Query(r.Context(),
		`sum by (namespace) (label_replace(rate(container_cpu_usage_seconds_total{container!="", namespace!=""}[1h]), "node", "$1", "instance", "(.+?)(?::\\d+)?$") * on(node) group_left() node_cpu_hourly_cost)`)
	if err == nil {
		for _, s := range cpuUsageResult.Series {
			ns := s.Labels["namespace"]
			if ns != "" && len(s.DataPoints) > 0 {
				cpuUsageMap[ns] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	// Query actual memory usage cost (for efficiency calculation)
	memUsageMap := make(map[string]float64)
	memUsageResult, err := client.Query(r.Context(),
		`sum by (namespace) (label_replace(container_memory_working_set_bytes{container!="", namespace!=""}, "node", "$1", "instance", "(.+?)(?::\\d+)?$") / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if err == nil {
		for _, s := range memUsageResult.Series {
			ns := s.Labels["namespace"]
			if ns != "" && len(s.DataPoints) > 0 {
				memUsageMap[ns] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	// Query storage (PV) cost per namespace
	storageMap := make(map[string]float64)
	storageResult, err := client.Query(r.Context(),
		`sum by (namespace) (pv_hourly_cost * on(persistentvolume) group_left(namespace) kube_persistentvolume_claim_ref)`)
	if err == nil {
		for _, s := range storageResult.Series {
			ns := s.Labels["namespace"]
			if ns != "" && len(s.DataPoints) > 0 {
				storageMap[ns] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	// Build per-namespace cost map
	nsMap := make(map[string]*NamespaceCost)

	for _, s := range cpuResult.Series {
		ns := s.Labels["namespace"]
		if ns == "" {
			continue
		}
		if _, ok := nsMap[ns]; !ok {
			nsMap[ns] = &NamespaceCost{Name: ns}
		}
		if len(s.DataPoints) > 0 {
			nsMap[ns].CPUCost = s.DataPoints[len(s.DataPoints)-1].Value
		}
	}

	for _, s := range memResult.Series {
		ns := s.Labels["namespace"]
		if ns == "" {
			continue
		}
		if _, ok := nsMap[ns]; !ok {
			nsMap[ns] = &NamespaceCost{Name: ns}
		}
		if len(s.DataPoints) > 0 {
			nsMap[ns].MemoryCost = s.DataPoints[len(s.DataPoints)-1].Value
		}
	}

	// Calculate totals
	var totalHourlyCost, totalStorageCost, totalUsageCost, totalAllocCost float64
	namespaces := make([]NamespaceCost, 0, len(nsMap))
	for _, nc := range nsMap {
		nc.HourlyCost = nc.CPUCost + nc.MemoryCost
		nc.StorageCost = storageMap[nc.Name]
		nc.HourlyCost += nc.StorageCost
		totalStorageCost += nc.StorageCost

		// Efficiency
		nc.CPUUsageCost = cpuUsageMap[nc.Name]
		nc.MemoryUsageCost = memUsageMap[nc.Name]
		allocCost := nc.CPUCost + nc.MemoryCost // allocation cost (excl storage)
		usageCost := nc.CPUUsageCost + nc.MemoryUsageCost
		if allocCost > 0 && usageCost > 0 {
			nc.Efficiency = roundTo((usageCost/allocCost)*100, 1)
			if nc.Efficiency > 100 {
				nc.Efficiency = 100
			}
			nc.IdleCost = allocCost - usageCost
			if nc.IdleCost < 0 {
				nc.IdleCost = 0
			}
		}
		totalAllocCost += allocCost
		totalUsageCost += usageCost

		totalHourlyCost += nc.HourlyCost
		namespaces = append(namespaces, *nc)
	}

	// Also try to get node-level total cost for a more accurate total
	nodeResult, err := client.Query(r.Context(), `sum(node_total_hourly_cost)`)
	if err == nil && len(nodeResult.Series) > 0 && len(nodeResult.Series[0].DataPoints) > 0 {
		nodeCost := nodeResult.Series[0].DataPoints[0].Value
		if nodeCost > totalHourlyCost {
			totalHourlyCost = nodeCost
		}
	}

	// Sort by cost descending
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].HourlyCost > namespaces[j].HourlyCost
	})

	// Cluster-level efficiency
	var clusterEfficiency float64
	var totalIdleCost float64
	if totalAllocCost > 0 && totalUsageCost > 0 {
		clusterEfficiency = roundTo((totalUsageCost/totalAllocCost)*100, 1)
		if clusterEfficiency > 100 {
			clusterEfficiency = 100
		}
		totalIdleCost = totalAllocCost - totalUsageCost
		if totalIdleCost < 0 {
			totalIdleCost = 0
		}
	}

	// Round to 4 decimal places for cleaner JSON
	totalHourlyCost = roundTo(totalHourlyCost, 4)
	totalStorageCost = roundTo(totalStorageCost, 4)
	totalIdleCost = roundTo(totalIdleCost, 4)
	for i := range namespaces {
		namespaces[i].HourlyCost = roundTo(namespaces[i].HourlyCost, 4)
		namespaces[i].CPUCost = roundTo(namespaces[i].CPUCost, 4)
		namespaces[i].MemoryCost = roundTo(namespaces[i].MemoryCost, 4)
		namespaces[i].StorageCost = roundTo(namespaces[i].StorageCost, 4)
		namespaces[i].CPUUsageCost = roundTo(namespaces[i].CPUUsageCost, 4)
		namespaces[i].MemoryUsageCost = roundTo(namespaces[i].MemoryUsageCost, 4)
		namespaces[i].IdleCost = roundTo(namespaces[i].IdleCost, 4)
	}

	writeJSON(w, http.StatusOK, CostSummary{
		Available:         true,
		Currency:          "USD",
		Window:            "1h",
		TotalHourlyCost:   totalHourlyCost,
		TotalStorageCost:  totalStorageCost,
		TotalIdleCost:     totalIdleCost,
		ClusterEfficiency: clusterEfficiency,
		Namespaces:        namespaces,
	})
}

func roundTo(val float64, places int) float64 {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0
	}
	pow := math.Pow(10, float64(places))
	return math.Round(val*pow) / pow
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[opencost] Failed to encode JSON response: %v", err)
	}
}

// workloadKey identifies a workload by name and kind for aggregation.
type workloadKey struct {
	name string
	kind string
}

// handleWorkloads returns workload-level cost breakdown for a namespace.
func handleWorkloads(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "namespace parameter is required"})
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, WorkloadCostResponse{Namespace: ns, Reason: ReasonNoPrometheus})
		return
	}

	_, _, err := client.EnsureConnected(r.Context())
	if err != nil {
		log.Printf("[opencost] EnsureConnected failed (workloads): %v", err)
		writeJSON(w, http.StatusOK, WorkloadCostResponse{Namespace: ns, Reason: ReasonNoPrometheus})
		return
	}

	// Sanitize namespace for safe PromQL label interpolation
	safeNS := prometheuspkg.SanitizeLabelValue(ns)

	// Query per-pod CPU cost in this namespace.
	// Use "or" to handle both honor_labels configurations:
	//   exported_namespace="X"  → honor_labels=false (namespace was renamed)
	//   namespace="X", exported_namespace=""  → honor_labels=true (no renaming, label absent)
	cpuQuery := `sum by (pod) ((avg_over_time(container_cpu_allocation{exported_namespace="` + safeNS + `"}[1h]) or avg_over_time(container_cpu_allocation{namespace="` + safeNS + `", exported_namespace=""}[1h])) * on(node) group_left() node_cpu_hourly_cost)`
	cpuResult, err := client.Query(r.Context(), cpuQuery)
	if err != nil {
		cpuQuery = `sum by (pod) (rate(opencost_container_cpu_cost_total{exported_namespace="` + safeNS + `"}[1h]) or rate(opencost_container_cpu_cost_total{namespace="` + safeNS + `", exported_namespace=""}[1h]))`
		cpuResult, err = client.Query(r.Context(), cpuQuery)
		if err != nil {
			log.Printf("[opencost] Workload CPU cost query failed for %s: %v", ns, err)
			writeJSON(w, http.StatusOK, WorkloadCostResponse{Namespace: ns, Reason: ReasonQueryError})
			return
		}
	}

	// Query per-pod memory cost in this namespace
	memQuery := `sum by (pod) ((avg_over_time(container_memory_allocation_bytes{exported_namespace="` + safeNS + `"}[1h]) or avg_over_time(container_memory_allocation_bytes{namespace="` + safeNS + `", exported_namespace=""}[1h])) / 1073741824 * on(node) group_left() node_ram_hourly_cost)`
	memResult, err := client.Query(r.Context(), memQuery)
	if err != nil {
		memQuery = `sum by (pod) (rate(opencost_container_memory_cost_total{exported_namespace="` + safeNS + `"}[1h]) or rate(opencost_container_memory_cost_total{namespace="` + safeNS + `", exported_namespace=""}[1h]))`
		memResult, err = client.Query(r.Context(), memQuery)
		if err != nil {
			log.Printf("[opencost] Workload memory cost query failed for %s: %v", ns, err)
			writeJSON(w, http.StatusOK, WorkloadCostResponse{Namespace: ns, Reason: ReasonQueryError})
			return
		}
	}

	// Query per-pod CPU usage cost (for efficiency)
	podCPUUsage := make(map[string]float64)
	cpuUsageQuery := `sum by (pod) (label_replace(rate(container_cpu_usage_seconds_total{container!="", namespace="` + safeNS + `"}[1h]), "node", "$1", "instance", "(.+?)(?::\\d+)?$") * on(node) group_left() node_cpu_hourly_cost)`
	cpuUsageResult, usageErr := client.Query(r.Context(), cpuUsageQuery)
	if usageErr == nil {
		for _, s := range cpuUsageResult.Series {
			pod := s.Labels["pod"]
			if pod != "" && len(s.DataPoints) > 0 {
				podCPUUsage[pod] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	// Query per-pod memory usage cost (for efficiency)
	podMemUsage := make(map[string]float64)
	memUsageQuery := `sum by (pod) (label_replace(container_memory_working_set_bytes{container!="", namespace="` + safeNS + `"}, "node", "$1", "instance", "(.+?)(?::\\d+)?$") / 1073741824 * on(node) group_left() node_ram_hourly_cost)`
	memUsageResult, usageErr := client.Query(r.Context(), memUsageQuery)
	if usageErr == nil {
		for _, s := range memUsageResult.Series {
			pod := s.Labels["pod"]
			if pod != "" && len(s.DataPoints) > 0 {
				podMemUsage[pod] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	// Build per-pod cost map
	type podCost struct {
		cpuCost     float64
		memoryCost  float64
		cpuUsage    float64
		memoryUsage float64
	}
	podCosts := make(map[string]*podCost)

	for _, s := range cpuResult.Series {
		pod := s.Labels["pod"]
		if pod == "" {
			continue
		}
		if _, ok := podCosts[pod]; !ok {
			podCosts[pod] = &podCost{}
		}
		if len(s.DataPoints) > 0 {
			podCosts[pod].cpuCost = s.DataPoints[len(s.DataPoints)-1].Value
		}
	}

	for _, s := range memResult.Series {
		pod := s.Labels["pod"]
		if pod == "" {
			continue
		}
		if _, ok := podCosts[pod]; !ok {
			podCosts[pod] = &podCost{}
		}
		if len(s.DataPoints) > 0 {
			podCosts[pod].memoryCost = s.DataPoints[len(s.DataPoints)-1].Value
		}
	}

	// Merge usage data into pod costs
	for pod, pc := range podCosts {
		pc.cpuUsage = podCPUUsage[pod]
		pc.memoryUsage = podMemUsage[pod]
	}

	// Resolve pod -> workload using K8s cache owner references
	podOwnerMap := make(map[string]workloadKey)
	rc := k8s.GetResourceCache()
	if rc != nil && rc.Pods() != nil {
		pods, _ := rc.Pods().Pods(ns).List(labels.Everything())
		for _, p := range pods {
			podOwnerMap[p.Name] = resolveOwner(p.OwnerReferences)
		}
	}

	workloadMap := make(map[workloadKey]*WorkloadCost)
	for podName, pc := range podCosts {
		owner, ok := podOwnerMap[podName]
		if !ok {
			// Fallback: strip pod hash suffixes to guess workload name
			owner = workloadKey{name: stripPodSuffix(podName), kind: "standalone"}
		}

		wl, exists := workloadMap[owner]
		if !exists {
			wl = &WorkloadCost{Name: owner.name, Kind: owner.kind}
			workloadMap[owner] = wl
		}
		wl.CPUCost += pc.cpuCost
		wl.MemoryCost += pc.memoryCost
		wl.CPUUsageCost += pc.cpuUsage
		wl.MemoryUsageCost += pc.memoryUsage
		wl.Replicas++
	}

	// Build sorted result
	workloads := make([]WorkloadCost, 0, len(workloadMap))
	for _, wl := range workloadMap {
		wl.HourlyCost = wl.CPUCost + wl.MemoryCost
		// Compute efficiency
		allocCost := wl.CPUCost + wl.MemoryCost
		usageCost := wl.CPUUsageCost + wl.MemoryUsageCost
		if allocCost > 0 && usageCost > 0 {
			wl.Efficiency = roundTo((usageCost/allocCost)*100, 1)
			if wl.Efficiency > 100 {
				wl.Efficiency = 100
			}
			wl.IdleCost = allocCost - usageCost
			if wl.IdleCost < 0 {
				wl.IdleCost = 0
			}
		}
		wl.HourlyCost = roundTo(wl.HourlyCost, 4)
		wl.CPUCost = roundTo(wl.CPUCost, 4)
		wl.MemoryCost = roundTo(wl.MemoryCost, 4)
		wl.CPUUsageCost = roundTo(wl.CPUUsageCost, 4)
		wl.MemoryUsageCost = roundTo(wl.MemoryUsageCost, 4)
		wl.IdleCost = roundTo(wl.IdleCost, 4)
		workloads = append(workloads, *wl)
	}
	sort.Slice(workloads, func(i, j int) bool {
		return workloads[i].HourlyCost > workloads[j].HourlyCost
	})

	writeJSON(w, http.StatusOK, WorkloadCostResponse{
		Available: true,
		Namespace: ns,
		Workloads: workloads,
	})
}

// resolveOwner walks owner references to find the top-level workload.
// For pods owned by ReplicaSets, it strips the RS hash suffix to get the Deployment name.
func resolveOwner(owners []metav1.OwnerReference) workloadKey {
	if len(owners) == 0 {
		return workloadKey{kind: "standalone"}
	}

	owner := owners[0]

	// If owned by a ReplicaSet, strip hash suffix to get the Deployment name
	if owner.Kind == "ReplicaSet" {
		deployName := stripReplicaSetSuffix(owner.Name)
		if deployName != owner.Name {
			return workloadKey{name: deployName, kind: "Deployment"}
		}
	}

	return workloadKey{name: owner.Name, kind: owner.Kind}
}

// stripReplicaSetSuffix removes the hash suffix from a ReplicaSet name
// (e.g., "myapp-7f8d9c" -> "myapp").
func stripReplicaSetSuffix(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx > 0 {
		return name[:idx]
	}
	return name
}

// stripPodSuffix removes pod hash suffixes to approximate the workload name.
// e.g., "myapp-7f8d9c-xyz12" -> "myapp"
func stripPodSuffix(name string) string {
	// Strip last segment (pod hash)
	idx := strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	name = name[:idx]
	// Strip RS hash segment
	idx = strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	return name[:idx]
}

// parseCostTimeRange parses the "range" query parameter into start/end/step for cost trends.
func parseCostTimeRange(rangeStr string) (start, end time.Time, step time.Duration, label string) {
	end = time.Now()
	switch rangeStr {
	case "6h":
		start = end.Add(-6 * time.Hour)
		step = 15 * time.Minute
		label = "6h"
	case "7d":
		start = end.Add(-7 * 24 * time.Hour)
		step = 6 * time.Hour
		label = "7d"
	default: // "24h"
		start = end.Add(-24 * time.Hour)
		step = time.Hour
		label = "24h"
	}
	return
}

// handleTrend returns cost trend data over time as a stacked series per namespace.
func handleTrend(w http.ResponseWriter, r *http.Request) {
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, CostTrendResponse{Available: false, Reason: ReasonNoPrometheus})
		return
	}

	_, _, err := client.EnsureConnected(r.Context())
	if err != nil {
		log.Printf("[opencost] EnsureConnected failed (trend): %v", err)
		writeJSON(w, http.StatusOK, CostTrendResponse{Available: false, Reason: ReasonNoPrometheus})
		return
	}

	rangeStr := r.URL.Query().Get("range")
	start, end, step, label := parseCostTimeRange(rangeStr)

	// Combined CPU + memory allocation cost per namespace over time.
	// label_replace normalises exported_namespace → namespace when honor_labels=false.
	query := `sum by (namespace) (
  label_replace(avg_over_time(container_cpu_allocation{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") * on(node) group_left() node_cpu_hourly_cost
) + sum by (namespace) (
  label_replace(avg_over_time(container_memory_allocation_bytes{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") / 1073741824 * on(node) group_left() node_ram_hourly_cost
)`

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		log.Printf("[opencost] Trend query failed: %v", err)
		writeJSON(w, http.StatusOK, CostTrendResponse{Available: false, Reason: ReasonQueryError})
		return
	}

	if len(result.Series) == 0 {
		writeJSON(w, http.StatusOK, CostTrendResponse{Available: false, Reason: ReasonNoMetrics})
		return
	}

	// Rank namespaces by latest cost to pick top 8
	type nsRank struct {
		ns       string
		lastCost float64
		idx      int
	}
	ranks := make([]nsRank, 0, len(result.Series))
	for i, s := range result.Series {
		ns := s.Labels["namespace"]
		if ns == "" {
			continue
		}
		var last float64
		if len(s.DataPoints) > 0 {
			last = s.DataPoints[len(s.DataPoints)-1].Value
		}
		ranks = append(ranks, nsRank{ns: ns, lastCost: last, idx: i})
	}
	sort.Slice(ranks, func(i, j int) bool { return ranks[i].lastCost > ranks[j].lastCost })

	const maxSeries = 8
	topSet := make(map[int]bool)
	series := make([]CostTrendSeries, 0, maxSeries+1)
	for i, r := range ranks {
		if i >= maxSeries {
			break
		}
		topSet[r.idx] = true
		s := result.Series[r.idx]
		dps := make([]CostDataPoint, 0, len(s.DataPoints))
		for _, dp := range s.DataPoints {
			dps = append(dps, CostDataPoint{Timestamp: dp.Timestamp, Value: roundTo(dp.Value, 4)})
		}
		series = append(series, CostTrendSeries{Namespace: r.ns, DataPoints: dps})
	}

	// Aggregate remaining into "other"
	if len(ranks) > maxSeries {
		// Collect all timestamps from any overflow series
		otherMap := make(map[int64]float64)
		for i, s := range result.Series {
			if topSet[i] {
				continue
			}
			for _, dp := range s.DataPoints {
				otherMap[dp.Timestamp] += dp.Value
			}
		}
		if len(otherMap) > 0 {
			dps := make([]CostDataPoint, 0, len(otherMap))
			for ts, val := range otherMap {
				dps = append(dps, CostDataPoint{Timestamp: ts, Value: roundTo(val, 4)})
			}
			sort.Slice(dps, func(i, j int) bool { return dps[i].Timestamp < dps[j].Timestamp })
			series = append(series, CostTrendSeries{Namespace: "other", DataPoints: dps})
		}
	}

	writeJSON(w, http.StatusOK, CostTrendResponse{
		Available: true,
		Range:     label,
		Series:    series,
	})
}

// handleNodes returns per-node cost breakdown.
func handleNodes(w http.ResponseWriter, r *http.Request) {
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, NodeCostResponse{Available: false, Reason: ReasonNoPrometheus})
		return
	}

	_, _, err := client.EnsureConnected(r.Context())
	if err != nil {
		log.Printf("[opencost] EnsureConnected failed (nodes): %v", err)
		writeJSON(w, http.StatusOK, NodeCostResponse{Available: false, Reason: ReasonNoPrometheus})
		return
	}

	// Query per-node total hourly cost (includes labels: node, instance_type, region)
	totalResult, err := client.Query(r.Context(), `node_total_hourly_cost`)
	if err != nil {
		log.Printf("[opencost] Node cost query failed: %v", err)
		writeJSON(w, http.StatusOK, NodeCostResponse{Available: false, Reason: ReasonQueryError})
		return
	}
	if len(totalResult.Series) == 0 {
		writeJSON(w, http.StatusOK, NodeCostResponse{Available: false, Reason: ReasonNoMetrics})
		return
	}

	// Query per-node CPU and memory costs
	cpuMap := make(map[string]float64)
	cpuResult, err := client.Query(r.Context(), `node_cpu_hourly_cost`)
	if err == nil {
		for _, s := range cpuResult.Series {
			node := s.Labels["node"]
			if node != "" && len(s.DataPoints) > 0 {
				cpuMap[node] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	memMap := make(map[string]float64)
	memResult, err := client.Query(r.Context(), `node_ram_hourly_cost`)
	if err == nil {
		for _, s := range memResult.Series {
			node := s.Labels["node"]
			if node != "" && len(s.DataPoints) > 0 {
				memMap[node] = s.DataPoints[len(s.DataPoints)-1].Value
			}
		}
	}

	nodes := make([]NodeCost, 0, len(totalResult.Series))
	for _, s := range totalResult.Series {
		node := s.Labels["node"]
		if node == "" || len(s.DataPoints) == 0 {
			continue
		}
		nc := NodeCost{
			Name:         node,
			InstanceType: s.Labels["instance_type"],
			Region:       s.Labels["region"],
			HourlyCost:   roundTo(s.DataPoints[len(s.DataPoints)-1].Value, 4),
			CPUCost:      roundTo(cpuMap[node], 4),
			MemoryCost:   roundTo(memMap[node], 4),
		}
		nodes = append(nodes, nc)
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].HourlyCost > nodes[j].HourlyCost })

	writeJSON(w, http.StatusOK, NodeCostResponse{
		Available: true,
		Nodes:     nodes,
	})
}
