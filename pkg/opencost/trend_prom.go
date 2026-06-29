package opencost

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// TrendPromOptions controls ComputeCostTrendFromProm.
type TrendPromOptions struct {
	// Range is "6h", "24h", "7d" (default "24h"). Drives the start/end and
	// step of the underlying range query and is echoed on the response.
	Range string

	// MaxSeries is the top-N namespaces kept; the rest are aggregated into
	// a single "other" series. Defaults to 8 when zero.
	MaxSeries int
}

// ComputeCostTrendFromProm returns a stacked per-namespace cost trend from
// OpenCost-exported Prometheus metrics. The top MaxSeries namespaces by
// latest cost are returned as individual series; the remainder is collapsed
// into a single "other" series.
//
// Contract mirrors ComputeCostSummaryFromProm:
//   - Underlying range query fails → Available=false, Reason=ReasonQueryError.
//   - No series returned → Available=false, Reason=ReasonNoMetrics.
func ComputeCostTrendFromProm(ctx context.Context, client *prom.Client, opts TrendPromOptions) *CostTrendResponse {
	if client == nil {
		return &CostTrendResponse{Available: false, Reason: ReasonNoPrometheus}
	}

	start, end, step, label := resolveTrendRange(opts.Range)
	maxSeries := opts.MaxSeries
	if maxSeries <= 0 {
		maxSeries = 8
	}

	const query = `sum by (namespace) (
  label_replace(avg_over_time(container_cpu_allocation{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") * on(node) group_left() node_cpu_hourly_cost
) + sum by (namespace) (
  label_replace(avg_over_time(container_memory_allocation_bytes{namespace!=""}[1h]), "namespace", "$1", "exported_namespace", "(.+)") / 1073741824 * on(node) group_left() node_ram_hourly_cost
)`

	result, err := client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Printf("[opencost] PromQL trend range query failed (range=%s): %v", label, err)
		return &CostTrendResponse{Available: false, Reason: ReasonQueryError}
	}
	if len(result.Series) == 0 {
		return &CostTrendResponse{Available: false, Reason: ReasonNoMetrics}
	}

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

	topSet := make(map[int]bool, maxSeries)
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

	if len(ranks) > maxSeries {
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

	return &CostTrendResponse{Available: true, Range: label, Series: series}
}

// resolveTrendRange returns the start/end/step/label for the named Range.
func resolveTrendRange(rangeStr string) (start, end time.Time, step time.Duration, label string) {
	end = time.Now()
	switch rangeStr {
	case "6h":
		return end.Add(-6 * time.Hour), end, 15 * time.Minute, "6h"
	case "7d":
		return end.Add(-7 * 24 * time.Hour), end, 6 * time.Hour, "7d"
	default:
		return end.Add(-24 * time.Hour), end, time.Hour, "24h"
	}
}
