package opencost

import (
	"context"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/pkg/prom"
)

// windowHours parses an OpenCost window string (e.g. "1h", "24h", "7d",
// "30d") into a number of hours. OpenCost's /allocation returns totalCost
// summed over the whole window; to present an hourly rate (which then
// multiplies by 730 for monthly projection) we divide by this value. Falls
// back to 1.0 for unknown inputs so callers degrade gracefully rather than
// silently zero out costs.
func windowHours(w string) float64 {
	s := strings.TrimSpace(strings.ToLower(w))
	if s == "" {
		return 1
	}
	if len(s) < 2 {
		return 1
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil || n <= 0 {
		return 1
	}
	switch unit {
	case 'h':
		return n
	case 'd':
		return n * 24
	case 'w':
		return n * 24 * 7
	case 'm':
		// Ambiguous (minutes vs months). OpenCost uses "30d" for months, so
		// treat lone "m" as minutes for safety.
		return n / 60
	}
	return 1
}

// SummaryOptions tunes ComputeCostSummary behavior.
type SummaryOptions struct {
	// Currency label returned in the response (default "USD").
	Currency string

	// Window passed to OpenCost and echoed in the response (default "1h").
	// For PromQL paths this is a response label only; the query itself has
	// fixed time windows baked in. For REST paths it's forwarded to OpenCost.
	Window string

	// Aggregate controls how rows are grouped. "namespace" (default),
	// "controller", "pod". Passed straight to OpenCost's aggregate param.
	Aggregate string

	// Filter is an OpenCost /allocation filter expression (v1.106+).
	// Commonly used to scope pod/controller queries to a single namespace.
	// Example: `namespace:"kube-system"`
	Filter string

	// NamespaceFilter is a client-side namespace scope applied after the
	// OpenCost response is received. Set alongside Filter — older OpenCost
	// versions silently ignore the REST `filter` param, so we have to post-
	// filter rows by their Properties["namespace"] to actually honor the
	// drill-down scope.
	NamespaceFilter string
}

// ComputeCostSummary is the default compute path: asks OpenCost's REST API
// for namespace-level allocation over the window and maps the response into
// our normalized CostSummary.
//
// Why REST by default: OpenCost computes cost internally (cloud pricing +
// Kubernetes allocation data) and exposes the results two ways — REST at
// /allocation/assets/cloudCost and Prometheus metrics at /metrics. REST
// works wherever OpenCost works; the Prometheus path requires a scrape
// config that's often missing on clusters where OpenCost was installed
// manually. REST is also simpler (one pre-aggregated call instead of ~6
// PromQL queries + client-side math).
//
// When to reach for ComputeCostSummaryFromProm instead:
//   - You need custom label aggregations beyond what /allocation exposes.
//   - You want per-node hourly pricing as time series.
//   - You're correlating cost with live Prometheus metrics (deploy events,
//     HPA state, container_cpu_usage, etc.) in the same query.
//
// Contract:
//   - REST unreachable or returns error → Available=false, Reason=ReasonQueryError.
//   - REST returns empty data (OpenCost up but has no cost rows yet) →
//     Available=false, Reason=ReasonNoMetrics.
//   - Otherwise Available=true with namespace rows + totals filled in.
//   - Numbers rounded to 4dp for JSON cleanliness.
func ComputeCostSummary(ctx context.Context, client *RESTClient, opts SummaryOptions) *CostSummary {
	if opts.Currency == "" {
		opts.Currency = "USD"
	}
	if opts.Window == "" {
		opts.Window = "1h"
	}

	aggregate := opts.Aggregate
	if aggregate == "" {
		aggregate = "namespace"
	}
	resp, err := client.GetAllocation(ctx, AllocationOptions{
		Window:      opts.Window,
		Aggregate:   aggregate,
		Filter:      opts.Filter,
		IncludeIdle: true,
	})
	if err != nil {
		log.Printf("[opencost] /allocation summary failed: %v", err)
		return &CostSummary{Available: false, Reason: ReasonQueryError}
	}
	if resp == nil || len(resp.Data) == 0 {
		return &CostSummary{Available: false, Reason: ReasonNoMetrics}
	}

	// /allocation returns an array of time windows. For a single bucket we
	// merge across all windows; normally there's just one.
	//
	// Older OpenCost versions (< v1.106) silently ignore the REST filter param,
	// so when NamespaceFilter is set we post-filter rows by their
	// Properties["namespace"]. The __idle__ synthetic row has no namespace, so
	// it naturally drops out of a scoped drill-down — desired.
	combined := make(map[string]*Allocation)
	for _, bucket := range resp.Data {
		for name, a := range bucket {
			if a == nil {
				continue
			}
			if opts.NamespaceFilter != "" {
				ns, _ := a.Properties["namespace"].(string)
				if ns != opts.NamespaceFilter {
					continue
				}
			}
			if existing, ok := combined[name]; ok {
				// Weight TotalEfficiency by the per-bucket allocation cost
				// BEFORE adding this bucket's cost into the running total.
				// Each row's TotalEfficiency is independently per-bucket;
				// the merged row's effective efficiency is the cost-weighted
				// average. Without this, the merged efficiency would just
				// be the first bucket's value.
				existingAlloc := existing.CPUCost + existing.RAMCost
				bucketAlloc := a.CPUCost + a.RAMCost
				if totalAlloc := existingAlloc + bucketAlloc; totalAlloc > 0 {
					existing.TotalEfficiency =
						(existing.TotalEfficiency*existingAlloc + a.TotalEfficiency*bucketAlloc) / totalAlloc
				}
				existing.CPUCost += a.CPUCost
				existing.RAMCost += a.RAMCost
				existing.PVCost += a.PVCost
				existing.NetworkCost += a.NetworkCost
				existing.LoadBalancerCost += a.LoadBalancerCost
				existing.SharedCost += a.SharedCost
				existing.ExternalCost += a.ExternalCost
				existing.TotalCost += a.TotalCost
				existing.CPUCoreUsageAverage += a.CPUCoreUsageAverage
				existing.RAMByteUsageAverage += a.RAMByteUsageAverage
			} else {
				cp := *a
				combined[name] = &cp
			}
		}
	}

	if len(combined) == 0 {
		return &CostSummary{Available: false, Reason: ReasonNoMetrics}
	}

	namespaces := make([]NamespaceCost, 0, len(combined))
	var totalHourlyCost, totalStorageCost, totalNetworkCost, totalIdleCost float64
	var totalAllocCost, totalUsageCost float64

	for name, a := range combined {
		// OpenCost emits __idle__ as a synthetic row for unallocated node
		// capacity. Surface it as a dedicated idle total, not a namespace.
		//
		// Sign quirk: OpenCost can report __idle__ with negative costs when
		// the cluster's allocated sum over-counts relative to node pricing
		// (burstable workloads exceeding their request, or pricing-model
		// rounding). Clamp negative idle to 0 — idle is conceptually
		// "unused capacity cost", always non-negative.
		if name == "__idle__" {
			idle := a.CPUCost + a.RAMCost
			if idle < 0 {
				idle = 0
			}
			totalIdleCost += idle
			// Intentionally do NOT add __idle__ to totalHourlyCost —
			// totalHourlyCost is the sum of allocated spend. Idle is
			// surfaced separately as TotalIdleCost so callers can render
			// or sum it as needed.
			continue
		}
		// OpenCost aggregates orphan pods (those with no controller) into a
		// synthetic "__unallocated__" row when grouping by controller. On some
		// cluster configurations this row also absorbs cluster-level idle,
		// making it appear larger than the parent namespace. Drop it to keep
		// the drill-down consistent — named controllers tell the real story.
		if name == "__unallocated__" {
			continue
		}
		nc := NamespaceCost{
			Name:        name,
			Kind:        aggregate,
			CPUCost:     a.CPUCost,
			MemoryCost:  a.RAMCost,
			StorageCost: a.PVCost,
			NetworkCost: a.NetworkCost,
			HourlyCost:  a.TotalCost,
		}
		// For non-namespace aggregates, OpenCost stamps the parent namespace
		// in Properties so the UI can thread children under their parent
		// without a second query.
		if aggregate != "namespace" {
			if ns, ok := a.Properties["namespace"].(string); ok {
				nc.Namespace = ns
			}
		}
		allocCost := nc.CPUCost + nc.MemoryCost
		if a.TotalEfficiency > 0 && allocCost > 0 {
			// Cap per-row efficiency at 1.0 BEFORE accumulating into the
			// cluster total. OpenCost occasionally reports TotalEfficiency
			// > 1 (burstable pods exceeding their request, measurement
			// noise); without this cap a single outlier could push the
			// cluster total above 100%.
			rowEff := a.TotalEfficiency
			if rowEff > 1 {
				rowEff = 1
			}
			usageCost := rowEff * allocCost
			nc.CPUUsageCost = usageCost * safeRatio(nc.CPUCost, allocCost)
			nc.MemoryUsageCost = usageCost - nc.CPUUsageCost
			nc.Efficiency = efficiencyPct(usageCost, allocCost)
			nc.IdleCost = idleFromUsage(usageCost, allocCost)
			// Accumulate cost-weighted, matching ComputeCostSummaryFromProm.
			// An unweighted mean would let a $0.01 row at 10% efficiency
			// drag down the cluster number identically to a $100 row.
			totalAllocCost += allocCost
			totalUsageCost += usageCost
		}
		totalHourlyCost += nc.HourlyCost
		totalStorageCost += nc.StorageCost
		totalNetworkCost += nc.NetworkCost
		// Per-namespace idle (allocated-not-used) is separate from the
		// __idle__ row (unassigned node capacity). Both are real waste the
		// user can act on, so aggregate them together.
		totalIdleCost += nc.IdleCost
		namespaces = append(namespaces, nc)
	}

	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].HourlyCost > namespaces[j].HourlyCost
	})

	clusterEfficiency := efficiencyPct(totalUsageCost, totalAllocCost)

	// Normalize window-total to hourly. OpenCost's /allocation returns
	// totalCost summed over the entire window; we want rate so the UI can
	// multiply by 730 for monthly projections regardless of the window
	// picker state. Efficiency is unitless (usage/alloc ratio) so it does
	// not need normalization.
	hours := windowHours(opts.Window)
	if hours <= 0 {
		hours = 1
	}
	normalize := func(v float64) float64 { return v / hours }
	totalHourlyCost = normalize(totalHourlyCost)
	totalStorageCost = normalize(totalStorageCost)
	totalNetworkCost = normalize(totalNetworkCost)
	totalIdleCost = normalize(totalIdleCost)
	for i := range namespaces {
		namespaces[i].HourlyCost = normalize(namespaces[i].HourlyCost)
		namespaces[i].CPUCost = normalize(namespaces[i].CPUCost)
		namespaces[i].MemoryCost = normalize(namespaces[i].MemoryCost)
		namespaces[i].StorageCost = normalize(namespaces[i].StorageCost)
		namespaces[i].NetworkCost = normalize(namespaces[i].NetworkCost)
		namespaces[i].CPUUsageCost = normalize(namespaces[i].CPUUsageCost)
		namespaces[i].MemoryUsageCost = normalize(namespaces[i].MemoryUsageCost)
		namespaces[i].IdleCost = normalize(namespaces[i].IdleCost)
	}

	// Round everything for JSON stability.
	totalHourlyCost = roundTo(totalHourlyCost, 4)
	totalStorageCost = roundTo(totalStorageCost, 4)
	totalNetworkCost = roundTo(totalNetworkCost, 4)
	totalIdleCost = roundTo(totalIdleCost, 4)
	for i := range namespaces {
		namespaces[i].HourlyCost = roundTo(namespaces[i].HourlyCost, 4)
		namespaces[i].CPUCost = roundTo(namespaces[i].CPUCost, 4)
		namespaces[i].MemoryCost = roundTo(namespaces[i].MemoryCost, 4)
		namespaces[i].StorageCost = roundTo(namespaces[i].StorageCost, 4)
		namespaces[i].NetworkCost = roundTo(namespaces[i].NetworkCost, 4)
		namespaces[i].CPUUsageCost = roundTo(namespaces[i].CPUUsageCost, 4)
		namespaces[i].MemoryUsageCost = roundTo(namespaces[i].MemoryUsageCost, 4)
		namespaces[i].IdleCost = roundTo(namespaces[i].IdleCost, 4)
	}

	return &CostSummary{
		Available:         true,
		Currency:          opts.Currency,
		Window:            opts.Window,
		TotalHourlyCost:   totalHourlyCost,
		TotalStorageCost:  totalStorageCost,
		TotalNetworkCost:  totalNetworkCost,
		TotalIdleCost:     totalIdleCost,
		ClusterEfficiency: clusterEfficiency,
		Namespaces:        namespaces,
	}
}

// safeRatio returns num/den or 0 when den is non-positive.
func safeRatio(num, den float64) float64 {
	if den <= 0 {
		return 0
	}
	return num / den
}

// ComputeCostSummaryFromProm is the PromQL-based compute path, for callers
// that have a scraped-OpenCost Prometheus available rather than the REST
// API (or that need to correlate cost with live Prometheus metrics in the
// same query).
//
// Contract:
//   - If the primary OpenCost allocation metrics are absent entirely, the
//     returned summary has Available=false and Reason=ReasonNoMetrics.
//   - If the underlying query fails outright, Available=false and
//     Reason=ReasonQueryError. Errors are never returned — callers serve
//     the typed reason to the UI.
//   - Numbers are rounded to 4 decimal places for cleaner JSON.
func ComputeCostSummaryFromProm(ctx context.Context, client *prom.Client, opts SummaryOptions) *CostSummary {
	if client == nil {
		return &CostSummary{Available: false, Reason: ReasonNoPrometheus}
	}
	if opts.Currency == "" {
		opts.Currency = "USD"
	}
	if opts.Window == "" {
		opts.Window = "1h"
	}

	cpuResult, err := client.Query(ctx,
		`sum by (namespace) (label_replace(avg_over_time(container_cpu_allocation{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") * on(node) group_left() node_cpu_hourly_cost)`)
	if err != nil {
		log.Printf("[opencost] CPU allocation query failed, trying opencost_container_cpu_cost_total: %v", err)
		cpuResult, err = client.Query(ctx,
			`sum by (namespace) (label_replace(rate(opencost_container_cpu_cost_total[1h]), "namespace", "$1", "exported_namespace", "(.+)"))`)
		if err != nil {
			log.Printf("[opencost] CPU allocation fallback query also failed: %v", err)
			return &CostSummary{Available: false, Reason: ReasonQueryError}
		}
	}

	memResult, err := client.Query(ctx,
		`sum by (namespace) (label_replace(avg_over_time(container_memory_allocation_bytes{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if err != nil {
		log.Printf("[opencost] memory allocation query failed, trying opencost_container_memory_cost_total: %v", err)
		memResult, err = client.Query(ctx,
			`sum by (namespace) (label_replace(rate(opencost_container_memory_cost_total[1h]), "namespace", "$1", "exported_namespace", "(.+)"))`)
		if err != nil {
			log.Printf("[opencost] memory allocation fallback query also failed: %v", err)
			return &CostSummary{Available: false, Reason: ReasonQueryError}
		}
	}

	if len(cpuResult.Series) == 0 && len(memResult.Series) == 0 {
		return &CostSummary{Available: false, Reason: ReasonNoMetrics}
	}

	// Usage queries are best-effort: efficiency / idle are derived from them
	// and zero out cleanly if the queries fail, but a silent failure here can
	// look identical to a low-utilization workload — so log when it happens.
	cpuUsageRes, cpuUsageErr := client.Query(ctx,
		`sum by (namespace) (label_replace(rate(container_cpu_usage_seconds_total{container!="", namespace!=""}[1h]), "node", "$1", "instance", "(.+?)(?::\\d+)?$") * on(node) group_left() node_cpu_hourly_cost)`)
	if cpuUsageErr != nil {
		log.Printf("[opencost] CPU usage query failed (efficiency will be 0 for affected rows): %v", cpuUsageErr)
	}
	cpuUsageMap := lastValuePerLabel(cpuUsageRes, cpuUsageErr, "namespace")

	memUsageRes, memUsageErr := client.Query(ctx,
		`sum by (namespace) (label_replace(container_memory_working_set_bytes{container!="", namespace!=""}, "node", "$1", "instance", "(.+?)(?::\\d+)?$") / 1073741824 * on(node) group_left() node_ram_hourly_cost)`)
	if memUsageErr != nil {
		log.Printf("[opencost] memory usage query failed (efficiency will be 0 for affected rows): %v", memUsageErr)
	}
	memUsageMap := lastValuePerLabel(memUsageRes, memUsageErr, "namespace")

	storageRes, storageErr := client.Query(ctx,
		`sum by (namespace) (pv_hourly_cost * on(persistentvolume) group_left(namespace) kube_persistentvolume_claim_ref)`)
	if storageErr != nil {
		log.Printf("[opencost] storage cost query failed (storage costs will be 0): %v", storageErr)
	}
	storageMap := lastValuePerLabel(storageRes, storageErr, "namespace")

	nsMap := make(map[string]*NamespaceCost)
	mergeSeriesIntoNamespaceField(cpuResult, nsMap, func(nc *NamespaceCost, v float64) { nc.CPUCost = v })
	mergeSeriesIntoNamespaceField(memResult, nsMap, func(nc *NamespaceCost, v float64) { nc.MemoryCost = v })

	var totalHourlyCost, totalStorageCost, totalUsageCost, totalAllocCost float64
	namespaces := make([]NamespaceCost, 0, len(nsMap))
	for _, nc := range nsMap {
		nc.HourlyCost = nc.CPUCost + nc.MemoryCost
		nc.StorageCost = storageMap[nc.Name]
		nc.HourlyCost += nc.StorageCost
		totalStorageCost += nc.StorageCost

		nc.CPUUsageCost = cpuUsageMap[nc.Name]
		nc.MemoryUsageCost = memUsageMap[nc.Name]
		allocCost := nc.CPUCost + nc.MemoryCost
		usageCost := nc.CPUUsageCost + nc.MemoryUsageCost
		nc.Efficiency = efficiencyPct(usageCost, allocCost)
		nc.IdleCost = idleFromUsage(usageCost, allocCost)
		totalAllocCost += allocCost
		totalUsageCost += usageCost
		totalHourlyCost += nc.HourlyCost
		namespaces = append(namespaces, *nc)
	}

	if nodeResult, err := client.Query(ctx, `sum(node_total_hourly_cost)`); err == nil && len(nodeResult.Series) > 0 && len(nodeResult.Series[0].DataPoints) > 0 {
		if nodeCost := nodeResult.Series[0].DataPoints[0].Value; nodeCost > totalHourlyCost {
			totalHourlyCost = nodeCost
		}
	}

	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].HourlyCost > namespaces[j].HourlyCost
	})

	clusterEfficiency := efficiencyPct(totalUsageCost, totalAllocCost)
	totalIdleCost := idleFromUsage(totalUsageCost, totalAllocCost)

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

	return &CostSummary{
		Available:         true,
		Currency:          opts.Currency,
		Window:            opts.Window,
		TotalHourlyCost:   totalHourlyCost,
		TotalStorageCost:  totalStorageCost,
		TotalIdleCost:     totalIdleCost,
		ClusterEfficiency: clusterEfficiency,
		Namespaces:        namespaces,
	}
}

func mergeSeriesIntoNamespaceField(result *prom.QueryResult, nsMap map[string]*NamespaceCost, set func(*NamespaceCost, float64)) {
	if result == nil {
		return
	}
	for _, s := range result.Series {
		ns := s.Labels["namespace"]
		if ns == "" {
			continue
		}
		nc, ok := nsMap[ns]
		if !ok {
			nc = &NamespaceCost{Name: ns}
			nsMap[ns] = nc
		}
		if len(s.DataPoints) > 0 {
			set(nc, s.DataPoints[len(s.DataPoints)-1].Value)
		}
	}
}

// roundTo rounds to `places` decimal places, returning 0 for NaN/Inf
// to keep JSON responses stable.
func roundTo(val float64, places int) float64 {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return 0
	}
	pow := math.Pow(10, float64(places))
	return math.Round(val*pow) / pow
}

// efficiencyPct returns 100 * usage / alloc rounded to 1 decimal,
// clamped to [0, 100]. Returns 0 when usage or alloc is non-positive
// (treated as "no data" — distinct from "100% idle").
func efficiencyPct(usage, alloc float64) float64 {
	if usage <= 0 || alloc <= 0 {
		return 0
	}
	eff := roundTo((usage/alloc)*100, 1)
	if eff > 100 {
		eff = 100
	}
	return eff
}

// idleFromUsage returns max(alloc - usage, 0) but only when both are
// positive. Mirrors efficiencyPct's "no data ≠ 100% idle" semantics.
func idleFromUsage(usage, alloc float64) float64 {
	if usage <= 0 || alloc <= 0 {
		return 0
	}
	idle := alloc - usage
	if idle < 0 {
		return 0
	}
	return idle
}
