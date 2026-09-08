package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	diagnoseMetricsDefaultSince = time.Hour
	diagnoseMetricsMaxPods      = 50
	diagnoseMetricsMaxPoints    = 60
	// diagnoseMetricsMaxBytes budgets the samples plus their envelope and
	// sets the resolution; the query strings, which name every pod in the
	// set, sit outside it and under the hard backstop instead.
	diagnoseMetricsMaxBytes = 8 * 1024
	// diagnoseMetricsHardMaxBytes bounds the entire serialized field,
	// queries included.
	diagnoseMetricsHardMaxBytes = 24 * 1024
	// diagnoseMetricsValueDigits bounds each sample's serialized width so
	// three full-resolution series fit the byte budget; four significant
	// digits keep every chart-visible distinction.
	diagnoseMetricsValueDigits = 4
	// diagnoseMetricsBytesPerSample is the widest sample after rounding,
	// {"timestamp":1700000000,"value":-0.00001235} plus its comma.
	diagnoseMetricsBytesPerSample = 45
	// diagnoseMetricsMaxErrorBytes keeps an error-only field inside the byte
	// budget; backend errors echo operator-supplied URLs of any length.
	diagnoseMetricsMaxErrorBytes = 1024
)

// DiagnoseMetricsEnabled decides whether diagnose collects workload vitals at
// all, and is the single place to change that decision in a release: set it
// false and the field is omitted, with the rest of the bundle and
// query_prometheus untouched.
//
// Collecting them is the default because an agent that has to ask for
// resource data can silently never ask, and a healthy verdict reached without
// looking is indistinguishable from one where the numbers were fine. The cost
// is one Prometheus round trip on every diagnose of a pod-backed workload:
// against a healthy backend that is roughly 130ms becoming roughly 650ms,
// because nothing else diagnose does takes long enough to hide it. Turning it
// off leaves query_prometheus in place, so the data stays reachable when the
// agent goes looking for it.
var DiagnoseMetricsEnabled = true

// diagnoseMetricsBudget bounds the whole metrics branch, availability probe
// included. A var so tests can exercise the breach path.
var diagnoseMetricsBudget = 3 * time.Second

// diagnoseMetricsCategories is one half of a Go↔TS contract:
// DIAGNOSE_METRICS_LABELS in
// web/src/components/diagnose/investigationEvidence.ts keys off these exact
// category names and discards a series whose category it does not know, so a
// category added here without a label there is captured and never charted.
// Change both together.
var diagnoseMetricsCategories = []prom.MetricCategory{prom.CategoryCPU, prom.CategoryMemory, prom.CategoryRestarts}

// diagnoseMetrics is the vitals snapshot diagnose captures alongside the
// rest of the bundle when Prometheus is reachable: cpu, memory and restarts
// summed over the exact pod set the bundle covers, so the chart, the agent
// and the evidence ledger all hold the same numbers. Pods counts the pods
// the queries name; when the resolved set exceeded the cap, Partial is set
// and OmittedPods says how many were left out. Error explains an empty or
// missing Series (unreachable backend, a failed query, or a breached budget)
// and is never set for "no samples in the window".
type diagnoseMetrics struct {
	Window      diagnoseMetricsWindow  `json:"window"`
	Pods        int                    `json:"pods"`
	Partial     bool                   `json:"partial,omitempty"`
	OmittedPods int                    `json:"omittedPods,omitempty"`
	Series      []diagnoseMetricSeries `json:"series"`
	Error       string                 `json:"error,omitempty"`
}

type diagnoseMetricsWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Step  string `json:"step"`
}

type diagnoseMetricSeries struct {
	Category string `json:"category"`
	Unit     string `json:"unit"`
	Query    string `json:"query"`
	// ObservedSamples is how many points Prometheus actually returned across
	// this category's series, before the flat stretches were compressed away.
	// Without it a two-point series is ambiguous: it could be an hour of
	// agreeing samples or two lonely ones, and the window and step cannot
	// settle it because a range query returns nothing for an evaluation with
	// no data.
	ObservedSamples int           `json:"observedSamples,omitempty"`
	Series          []prom.Series `json:"series"`
}

func diagnoseMetricsSince(sinceSeconds *int64) time.Duration {
	if sinceSeconds == nil || *sinceSeconds <= 0 {
		return diagnoseMetricsDefaultSince
	}
	return time.Duration(*sinceSeconds) * time.Second
}

// diagnoseWorkloadMetrics returns nil when there is nothing to report (vitals
// are switched off, no pod could be established, the caller cannot get the
// diagnosed resource, or Prometheus is absent) so the caller omits the field.
// The per-kind gate runs inside the budget: a slow SubjectAccessReview counts
// against the branch, not against the rest of diagnose, and a gate that does
// not answer in time denies.
//
// A workload with no pods right now is not a workload with no metrics: one
// scaled to zero, or whose pods were all replaced, still has an hour of
// history if kube-state-metrics recorded who owned them. The resolver
// decides that, so the pod list is not a precondition here.
func diagnoseWorkloadMetrics(ctx context.Context, group, resource, namespace, name string, pods []*corev1.Pod, since time.Duration, now time.Time) *diagnoseMetrics {
	if !DiagnoseMetricsEnabled {
		return nil
	}
	budgetCtx, cancel := context.WithTimeout(ctx, diagnoseMetricsBudget)
	defer cancel()

	if !canReadInNamespace(budgetCtx, group, resource, namespace, "get") {
		return nil
	}
	avail := prometheus.Availability(budgetCtx)
	if avail.State == prometheus.AvailabilityAbsent {
		if avail.Err != nil && (errors.Is(avail.Err, context.DeadlineExceeded) || errors.Is(avail.Err, context.Canceled)) {
			// Discovery did not answer in time. That is not "no Prometheus",
			// and the field must say so rather than vanish.
			return &diagnoseMetrics{
				Window: diagnoseMetricsWindow{Start: now.Add(-since).UTC().Format(time.RFC3339), End: now.UTC().Format(time.RFC3339), Step: "0s"},
				Pods:   len(pods),
				Series: []diagnoseMetricSeries{},
				Error:  fmt.Sprintf("prometheus probe did not answer within %s", diagnoseMetricsBudget),
			}
		}
		return nil
	}
	scope, err := prometheus.ResolvePodScope(k8s.GetResourceCache(), resource, namespace, name, diagnoseMetricsMaxPods)
	if err != nil {
		log.Printf("[mcp] diagnose: pod scope for %s %s/%s failed: %v", resource, namespace, name, err)
		// Prometheus is there and the caller may read the resource; the pods
		// could not be established. Say so rather than omit the field, which
		// reads as "this workload has no metrics".
		return &diagnoseMetrics{
			Window: diagnoseMetricsWindow{Start: now.Add(-since).UTC().Format(time.RFC3339), End: now.UTC().Format(time.RFC3339), Step: "0s"},
			Pods:   len(pods),
			Series: []diagnoseMetricSeries{},
			Error:  boundDiagnoseMetricsError(fmt.Sprintf("metrics omitted: the workload's pods could not be established: %v", err)),
		}
	}
	if scope.Selection.IsEmpty() {
		// The workload controls no pods right now. Membership is the current
		// population, so there is nothing to chart and nothing to say about it.
		return nil
	}
	return diagnoseMetricsForScope(budgetCtx, avail, scope, since, now)
}

func diagnoseMetricsForScope(budgetCtx context.Context, avail prometheus.AvailabilityState, scope prometheus.PodScope, since time.Duration, now time.Time) *diagnoseMetrics {
	namespace := scope.Namespace
	start := now.Add(-since)
	out := &diagnoseMetrics{
		Window: diagnoseMetricsWindow{
			Start: start.UTC().Format(time.RFC3339),
			End:   now.UTC().Format(time.RFC3339),
		},
		Pods:   len(scope.CurrentPods),
		Series: []diagnoseMetricSeries{},
	}
	if scope.Partial() {
		out.Partial = true
		out.OmittedPods = scope.CurrentTotal - len(scope.CurrentPods)
	}
	step, _ := adjustStep(since, "", diagnoseMetricsPointBudget(out))
	out.Window.Step = step.String()

	if avail.State != prometheus.AvailabilityConnected {
		out.Error = boundDiagnoseMetricsError(fmt.Sprintf("prometheus unreachable: %v", avail.Err))
		return out
	}
	client := prometheus.GetClient()
	var p *prom.Client
	if client != nil {
		p = client.PromForMCP()
	}
	if p == nil {
		out.Error = "prometheus unreachable: connection was reset"
		return out
	}
	if scope.Selection.IsEmpty() {
		out.Error = "metrics omitted: no pods could be attributed to the workload"
		return out
	}

	type categoryResult struct {
		series diagnoseMetricSeries
		err    error
	}
	results := make([]categoryResult, len(diagnoseMetricsCategories))
	var wg sync.WaitGroup
	for i, cat := range diagnoseMetricsCategories {
		wg.Add(1)
		go func(i int, cat prom.MetricCategory) {
			defer wg.Done()
			s, err := queryPodSetCategory(budgetCtx, p, scope.Selection, cat, start, now, step)
			results[i] = categoryResult{series: s, err: err}
		}(i, cat)
	}
	wg.Wait()

	var failures []string
	for _, r := range results {
		if r.err != nil {
			if budgetCtx.Err() != nil {
				out.Series = []diagnoseMetricSeries{}
				out.Error = fmt.Sprintf("metrics omitted: %s budget exceeded before all queries answered", diagnoseMetricsBudget)
				log.Printf("[mcp] diagnose: metrics for %d pods in %s dropped: %s", out.Pods, namespace, out.Error)
				return out
			}
			failures = append(failures, fmt.Sprintf("%s: %v", r.series.Category, r.err))
			continue
		}
		out.Series = append(out.Series, r.series)
	}
	if len(failures) > 0 {
		out.Error = boundDiagnoseMetricsError(strings.Join(failures, "; "))
		log.Printf("[mcp] diagnose: metrics query failed for %d pods in %s: %s", out.Pods, namespace, out.Error)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		out.Series = []diagnoseMetricSeries{}
		out.Error = fmt.Sprintf("metrics omitted: %v", err)
		return out
	}
	if len(encoded) > diagnoseMetricsHardMaxBytes {
		out.Series = []diagnoseMetricSeries{}
		out.Error = fmt.Sprintf("metrics omitted: %d bytes serialized exceeds the %d byte backstop", len(encoded), diagnoseMetricsHardMaxBytes)
		log.Printf("[mcp] diagnose: metrics for %d pods in %s dropped: %s", out.Pods, namespace, out.Error)
	}
	return out
}

func boundDiagnoseMetricsError(msg string) string {
	if len(msg) <= diagnoseMetricsMaxErrorBytes {
		return msg
	}
	return msg[:diagnoseMetricsMaxErrorBytes-len("…")] + "…"
}

// diagnoseMetricsPointBudget derives the per-series point cap from what the
// sample budget leaves after the envelope, with the query strings left out
// so the resolution does not depend on how many pods the set names.
func diagnoseMetricsPointBudget(envelope *diagnoseMetrics) int {
	probe := *envelope
	probe.Window.Step = "10m0s"
	probe.Series = make([]diagnoseMetricSeries, 0, len(diagnoseMetricsCategories))
	for _, cat := range diagnoseMetricsCategories {
		probe.Series = append(probe.Series, diagnoseMetricSeries{
			Category: string(cat),
			Unit:     prom.CategoryUnit(cat),
			Series:   []prom.Series{{Labels: map[string]string{}, DataPoints: []prom.DataPoint{}}},
		})
	}
	encoded, err := json.Marshal(probe)
	if err != nil {
		return diagnoseMetricsMaxPoints
	}
	points := (diagnoseMetricsMaxBytes - len(encoded)) / (len(diagnoseMetricsCategories) * diagnoseMetricsBytesPerSample)
	if points > diagnoseMetricsMaxPoints {
		return diagnoseMetricsMaxPoints
	}
	if points < 2 {
		return 2
	}
	return points
}

// queryPodSetCategory runs one category over the pod set, sharing the curated
// HTTP metrics handler's container-filter fallback for clusters whose cAdvisor
// lacks the label.
func queryPodSetCategory(ctx context.Context, p *prom.Client, sel prom.PodSelection, cat prom.MetricCategory, start, end time.Time, step time.Duration) (diagnoseMetricSeries, error) {
	out := diagnoseMetricSeries{Category: string(cat), Unit: prom.CategoryUnit(cat)}
	query := prom.BuildScopedQuery(sel, cat, prom.AggregateTotal, true)
	result, err := p.QueryRange(ctx, query, start, end, step)
	if err != nil {
		return out, err
	}
	result, query, err = prometheus.QueryWithContainerFilterFallback(ctx, p, result, query, cat, start, end, step,
		func() string { return prom.BuildScopedQuery(sel, cat, prom.AggregateTotal, false) },
		fmt.Sprintf("diagnose metrics empty for %q", cat))
	if err != nil {
		return out, err
	}
	out.Query = query
	out.Series = result.Series
	if out.Series == nil {
		out.Series = []prom.Series{}
	}
	for i := range out.Series {
		for j := range out.Series[i].DataPoints {
			out.Series[i].DataPoints[j].Value = roundSignificant(out.Series[i].DataPoints[j].Value, diagnoseMetricsValueDigits)
		}
		out.ObservedSamples += len(out.Series[i].DataPoints)
		out.Series[i].DataPoints = collapseConstantRuns(out.Series[i].DataPoints)
	}
	return out, nil
}

// collapseConstantRuns drops the interior of an evenly spaced run of equal
// values, keeping the point that opens it and the point that closes it. A flat
// hour costs two points instead of sixty, so what the field costs follows what
// it says rather than how long the window is.
//
// This is a tolerance, not a lossless transform: values are compared after
// rounding, so movement too small to reach four significant digits — movement
// no chart would draw and no reader could see — is treated as flat. The count
// of what was observed survives on ObservedSamples.
//
// Two properties the rule depends on:
//
//   - Points are compared against their neighbours in the ORIGINAL slice.
//     Deciding from the output as it is built lets an already-kept boundary be
//     dropped by the point after it.
//   - A run must be evenly spaced. Prometheus returns nothing for an
//     evaluation with no data, so equal values either side of a gap are not
//     adjacent, and merging them would erase the only evidence the gap
//     happened. (No chart draws that gap today — pkg/prom never inserts a null
//     and AreaChart only breaks a path on one — but the timestamps that would
//     let it are kept.)
//
// Non-finite values are never collapsed: NaN is unequal to itself, and two
// equal infinities are a boundary worth keeping rather than a flat stretch.
func collapseConstantRuns(points []prom.DataPoint) []prom.DataPoint {
	if len(points) < 3 {
		return points
	}
	kept := make([]prom.DataPoint, 0, len(points))
	for i, p := range points {
		if i == 0 || i == len(points)-1 {
			kept = append(kept, p)
			continue
		}
		prev, next := points[i-1], points[i+1]
		flat := finite(prev.Value) && finite(p.Value) && finite(next.Value) &&
			prev.Value == p.Value && p.Value == next.Value
		even := p.Timestamp-prev.Timestamp == next.Timestamp-p.Timestamp
		if flat && even {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// roundSignificant rounds v to the given number of significant digits,
// keeping integer results exact so they serialize without float noise.
func roundSignificant(v float64, digits int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	exp := int(math.Ceil(math.Log10(math.Abs(v)))) - digits
	if exp >= 0 {
		scale := math.Pow(10, float64(exp))
		return math.Round(v/scale) * scale
	}
	scale := math.Pow(10, float64(-exp))
	return math.Round(v*scale) / scale
}
