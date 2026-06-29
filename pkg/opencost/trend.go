package opencost

import (
	"context"
	"log"
	"sort"
	"time"
)

// TrendOptions controls ComputeCostTrend.
type TrendOptions struct {
	// Window is the overall time range (e.g. "7d", "30d"). Defaults to "24h".
	Window string

	// Step is the bucket size inside the window. Defaults based on Window:
	// 1h → 5m, 24h → 1h, 7d → 6h, 30d → 1d. If set, overrides the default.
	Step string

	// Aggregate controls how rows inside each bucket are grouped. Defaults
	// to "namespace" so callers can produce both total and per-namespace
	// series from the same response. Use "cluster" when only the fleet-total
	// line is needed (cheaper for the backend + OpenCost).
	Aggregate string
}

// ComputeCostTrend queries OpenCost's /allocation with a step parameter
// and returns a bucketed cost trend. Each CostTrendSeries becomes one line
// on the UI chart; a synthetic "__total__" series carries the cluster-level
// sum so the default view doesn't have to re-sum on the client.
//
// Contract mirrors ComputeCostSummary:
//   - REST unreachable / parse error → Available=false, Reason=ReasonQueryError.
//   - OpenCost responds but has no buckets → Available=false,
//     Reason=ReasonNoMetrics.
//   - Otherwise Available=true with one CostTrendSeries per (aggregate row)
//     — always including a "__total__" aggregate — ordered by bucket
//     timestamp ascending.
//
// Each data point's Value is normalized to $/hr for the bucket (OpenCost's
// per-bucket totalCost ÷ bucket duration), matching the hourly-rate
// convention used throughout the Costs UI. The UI multiplies by 730 for
// monthly projections or hours-in-period for retrospective totals.
func ComputeCostTrend(ctx context.Context, client *RESTClient, opts TrendOptions) *CostTrendResponse {
	window := opts.Window
	if window == "" {
		window = "24h"
	}
	aggregate := opts.Aggregate
	if aggregate == "" {
		aggregate = "namespace"
	}
	step := opts.Step
	if step == "" {
		step = defaultStep(window)
	}

	resp, err := client.GetAllocation(ctx, AllocationOptions{
		Window:      window,
		Aggregate:   aggregate,
		Step:        step,
		IncludeIdle: false, // idle is a summary concept; drop it here to keep the chart focused on spend
	})
	if err != nil {
		log.Printf("[opencost] /allocation trend failed (window=%s step=%s): %v", window, step, err)
		return &CostTrendResponse{Available: false, Reason: ReasonQueryError, Range: window}
	}
	if resp == nil || len(resp.Data) == 0 {
		return &CostTrendResponse{Available: false, Reason: ReasonNoMetrics, Range: window}
	}

	bucketHours := windowHours(step)
	skippedBuckets := 0

	// Walk buckets in order. For each bucket, accumulate per-aggregate
	// totals and the bucket timestamp (parsed from one row's Start, since
	// every row in a bucket shares the same window).
	seriesByName := make(map[string][]CostDataPoint)
	totals := make([]CostDataPoint, 0, len(resp.Data))

	for _, bucket := range resp.Data {
		if len(bucket) == 0 {
			continue
		}
		ts := bucketTimestamp(bucket)
		if ts == 0 {
			// No parseable Start on any row — skip rather than stamping all
			// points at the Unix epoch, which would collapse the chart.
			skippedBuckets++
			continue
		}
		var bucketTotal float64
		for name, a := range bucket {
			if a == nil || name == "__idle__" {
				continue
			}
			// Normalize to hourly rate for this bucket. OpenCost returns
			// totalCost summed across the bucket; dividing by bucket
			// duration (hours) gives the $/hr rate the UI consumes.
			value := a.TotalCost / bucketHours
			seriesByName[name] = append(seriesByName[name], CostDataPoint{
				Timestamp: ts,
				Value:     roundTo(value, 4),
			})
			bucketTotal += a.TotalCost
		}
		totals = append(totals, CostDataPoint{
			Timestamp: ts,
			Value:     roundTo(bucketTotal/bucketHours, 4),
		})
	}

	if skippedBuckets > 0 {
		log.Printf("[opencost] trend dropped %d bucket(s) with no parseable timestamp (window=%s step=%s)", skippedBuckets, window, step)
	}

	if len(totals) == 0 {
		return &CostTrendResponse{Available: false, Reason: ReasonNoMetrics, Range: window}
	}

	// Assemble the response. Put __total__ first so the UI can find it
	// without scanning, then per-namespace series sorted by peak spend
	// (descending). Non-total series are sorted so the chart's default
	// stacking shows the biggest spenders consistently across refreshes.
	series := make([]CostTrendSeries, 0, len(seriesByName)+1)
	series = append(series, CostTrendSeries{
		Namespace:  "__total__",
		DataPoints: sortByTimestamp(totals),
	})

	type namedSeries struct {
		name   string
		peak   float64
		points []CostDataPoint
	}
	byPeak := make([]namedSeries, 0, len(seriesByName))
	for name, pts := range seriesByName {
		pts = sortByTimestamp(pts)
		peak := 0.0
		for _, p := range pts {
			if p.Value > peak {
				peak = p.Value
			}
		}
		byPeak = append(byPeak, namedSeries{name: name, peak: peak, points: pts})
	}
	sort.Slice(byPeak, func(i, j int) bool { return byPeak[i].peak > byPeak[j].peak })
	for _, s := range byPeak {
		series = append(series, CostTrendSeries{
			Namespace:  s.name,
			DataPoints: s.points,
		})
	}

	return &CostTrendResponse{
		Available: true,
		Range:     window,
		Series:    series,
	}
}

// defaultStep picks a sensible bucket size for a window. We bias toward
// fewer, coarser buckets than a typical charting library would because
// OpenCost's /allocation with step= scales roughly with bucket count —
// a 24h query at 1h step takes ~30s on a test cluster vs ~3s at 6h step.
// Callers behind short request deadlines need the response well under
// that budget.
//
// Bucket counts we target: 1h → 12, 24h → 4, 7d → 7, 30d → 15.
func defaultStep(window string) string {
	hours := windowHours(window)
	switch {
	case hours <= 1:
		return "5m"
	case hours <= 24:
		return "6h"
	case hours <= 24*7:
		return "1d"
	default:
		return "2d"
	}
}

// bucketTimestamp returns a Unix-seconds timestamp derived from the first
// allocation row in the bucket (each row in a bucket shares the same
// window, so any row is representative). Seconds because the PromQL trend
// path emits seconds, and both paths feed the same CostDataPoint.Timestamp
// field — the UI assumes seconds at the render layer.
func bucketTimestamp(bucket map[string]*Allocation) int64 {
	for _, a := range bucket {
		if a == nil {
			continue
		}
		if a.Start != "" {
			if t, err := time.Parse(time.RFC3339, a.Start); err == nil {
				return t.Unix()
			}
		}
	}
	return 0
}

func sortByTimestamp(pts []CostDataPoint) []CostDataPoint {
	sort.Slice(pts, func(i, j int) bool { return pts[i].Timestamp < pts[j].Timestamp })
	return pts
}
