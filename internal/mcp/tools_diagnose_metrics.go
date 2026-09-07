package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	diagnoseMetricsDefaultSince = time.Hour
	diagnoseMetricsMaxPods      = 50
	diagnoseMetricsMaxPoints    = 60
	diagnoseMetricsMaxBytes     = 8 * 1024
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

// diagnoseMetricsBudget bounds the whole metrics branch, availability probe
// included. A var so tests can exercise the breach path.
var diagnoseMetricsBudget = 3 * time.Second

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
	Category string        `json:"category"`
	Unit     string        `json:"unit"`
	Query    string        `json:"query"`
	Series   []prom.Series `json:"series"`
}

func diagnoseMetricsSince(sinceSeconds *int64) time.Duration {
	if sinceSeconds == nil || *sinceSeconds <= 0 {
		return diagnoseMetricsDefaultSince
	}
	return time.Duration(*sinceSeconds) * time.Second
}

// diagnoseWorkloadMetrics returns nil when there is nothing to report (no
// pods to name, the caller cannot get the diagnosed resource, or Prometheus
// is absent) so the caller omits the field. The per-kind gate runs inside
// the budget: a slow SubjectAccessReview counts against the branch, not
// against the rest of diagnose, and a gate that does not answer in time
// denies.
func diagnoseWorkloadMetrics(ctx context.Context, group, resource, namespace string, pods []*corev1.Pod, since time.Duration, now time.Time) *diagnoseMetrics {
	names := make([]string, 0, len(pods))
	for _, p := range pods {
		if p != nil && p.Name != "" {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	budgetCtx, cancel := context.WithTimeout(ctx, diagnoseMetricsBudget)
	defer cancel()

	if !canReadInNamespace(budgetCtx, group, resource, namespace, "get") {
		return nil
	}
	avail := prometheus.Availability(budgetCtx)
	if avail.State == prometheus.AvailabilityAbsent {
		return nil
	}
	return diagnoseMetricsForPodSet(budgetCtx, avail, namespace, names, since, now)
}

func diagnoseMetricsForPodSet(budgetCtx context.Context, avail prometheus.AvailabilityState, namespace string, names []string, since time.Duration, now time.Time) *diagnoseMetrics {

	start := now.Add(-since)
	out := &diagnoseMetrics{
		Window: diagnoseMetricsWindow{
			Start: start.UTC().Format(time.RFC3339),
			End:   now.UTC().Format(time.RFC3339),
		},
		Series: []diagnoseMetricSeries{},
	}
	if len(names) > diagnoseMetricsMaxPods {
		out.Partial = true
		out.OmittedPods = len(names) - diagnoseMetricsMaxPods
		names = names[:diagnoseMetricsMaxPods]
	}
	out.Pods = len(names)
	step, _ := adjustStep(since, "", diagnoseMetricsPointBudget(out, namespace, names))
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
			s, err := queryPodSetCategory(budgetCtx, p, namespace, names, cat, start, now, step)
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
	if len(encoded) > diagnoseMetricsMaxBytes {
		out.Series = []diagnoseMetricSeries{}
		out.Error = fmt.Sprintf("metrics omitted: %d bytes serialized exceeds the %d byte limit", len(encoded), diagnoseMetricsMaxBytes)
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
// byte budget leaves after the envelope. The three query strings name every
// pod in the set, so a large set can take half the budget; lowering the
// resolution keeps the chart where dropping it would lose the vitals.
func diagnoseMetricsPointBudget(envelope *diagnoseMetrics, namespace string, pods []string) int {
	probe := *envelope
	probe.Window.Step = "10m0s"
	probe.Series = make([]diagnoseMetricSeries, 0, len(diagnoseMetricsCategories))
	for _, cat := range diagnoseMetricsCategories {
		probe.Series = append(probe.Series, diagnoseMetricSeries{
			Category: string(cat),
			Unit:     prom.CategoryUnit(cat),
			Query:    prom.BuildPodSetQuery(namespace, pods, cat),
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

// queryPodSetCategory runs one category over the pod set, retrying without
// the container filter when the filtered query finds nothing, as the curated
// HTTP metrics handler does for clusters whose cAdvisor lacks the label.
func queryPodSetCategory(ctx context.Context, p *prom.Client, namespace string, pods []string, cat prom.MetricCategory, start, end time.Time, step time.Duration) (diagnoseMetricSeries, error) {
	out := diagnoseMetricSeries{Category: string(cat), Unit: prom.CategoryUnit(cat)}
	query := prom.BuildPodSetQuery(namespace, pods, cat)
	result, err := p.QueryRange(ctx, query, start, end, step)
	if err != nil {
		return out, err
	}
	if len(result.Series) == 0 && prom.CategoryUsesContainerFilter(cat) {
		fallback := prom.BuildPodSetQueryNoContainerFilter(namespace, pods, cat)
		// On a cluster whose cAdvisor lacks the container label the retry is
		// the query that carries the data, so its failure is the category's
		// failure rather than an empty window.
		if fallbackResult, fallbackErr := p.QueryRange(ctx, fallback, start, end, step); fallbackErr != nil {
			return out, fallbackErr
		} else if len(fallbackResult.Series) > 0 {
			result, query = fallbackResult, fallback
		}
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
	}
	return out, nil
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
