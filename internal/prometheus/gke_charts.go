package prometheus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func (g *gkeMetrics) chart(ctx context.Context, kind, namespace, name string, category prom.MetricCategory, pods *PodScope, start, end time.Time, step time.Duration) (ChartAnswer, error) {
	q := g.chartQuery(kind, namespace, name, category, pods)
	if q == "" {
		return ChartAnswer{Result: emptyChartResult()}, nil
	}
	if len(q) > prom.MaxWorkloadQueryBytes {
		return ChartAnswer{}, errChartQuery{prom.ErrWorkloadQueryTooLarge}
	}
	r, err := g.store.QueryRange(ctx, q, start, end, step)
	if err != nil {
		return ChartAnswer{}, errChartQuery{err}
	}
	if pods != nil {
		trimGKEPodHistory(r, pods.Created, category)
		if category == prom.CategoryRestarts {
			if err := g.addYoungPodRestarts(ctx, r, *pods, start, end, step); err != nil {
				return ChartAnswer{}, errChartQuery{err}
			}
		}
	}
	if g.owner != nil && g.owner.gkeChartStore() != g {
		return ChartAnswer{}, errDiscoverySuperseded
	}
	return ChartAnswer{Result: r, Query: q, Partial: incompleteResult(r)}, nil
}

// Before a Pod has lived an hour, its current cumulative counter already
// covers the entire part of the rolling hour during which it existed. Reading
// the counter avoids mixing in a previous incarnation and losing early crashes.
func (g *gkeMetrics) addYoungPodRestarts(ctx context.Context, r *prom.QueryResult, scope PodScope, start, end time.Time, step time.Duration) error {
	young := scope
	young.CurrentPods = nil
	first, last := end, start
	for _, pod := range scope.CurrentPods {
		created := scope.Created[pod]
		if !created.IsZero() && created.Before(end) && !created.Add(time.Hour).Before(start) {
			young.CurrentPods = append(young.CurrentPods, pod)
			if created.Before(first) {
				first = created
			}
			if until := created.Add(time.Hour); until.After(last) {
				last = until
			}
		}
	}
	if len(young.CurrentPods) == 0 {
		return nil
	}
	q := g.chartQuery(scope.Kind, scope.Namespace, scope.Name, gkeRestartsSinceCreation, &young)
	if len(q) > prom.MaxWorkloadQueryBytes {
		return prom.ErrWorkloadQueryTooLarge
	}
	if first.After(start) {
		start = start.Add(first.Sub(start).Truncate(step))
	}
	if last.Before(end) {
		end = last
	}
	early, err := g.store.QueryRange(ctx, q, start, end, step)
	if err != nil {
		return err
	}
	r.Annotations.Partial = incompleteResult(r) || incompleteResult(early)
	byPod := map[string]int{}
	for i, s := range r.Series {
		byPod[s.Labels["pod"]] = i
	}
	for _, s := range early.Series {
		created := young.Created[s.Labels["pod"]]
		if created.IsZero() {
			continue
		}
		points := slices.DeleteFunc(s.DataPoints, func(p prom.DataPoint) bool {
			return p.Timestamp <= created.Add(time.Minute).Unix() || p.Timestamp > created.Add(time.Hour).Unix()
		})
		if len(points) == 0 {
			continue
		}
		if i, ok := byPod[s.Labels["pod"]]; ok {
			r.Series[i].DataPoints = append(points, r.Series[i].DataPoints...)
		} else {
			s.DataPoints = points
			r.Series = append(r.Series, s)
		}
	}
	return nil
}

const gkeCurrentPodHistory = "GKE system metrics cover the workload’s current Pods. Samples from before each Pod’s creation and its first five minutes are excluded."

func (g *gkeMetrics) workloadPanel(ctx context.Context, scope PodScope, category prom.MetricCategory, start, end time.Time, step time.Duration) workloadMetricPanel {
	query := g.chartQuery(scope.Kind, scope.Namespace, scope.Name, category, &scope)
	panel := queryWorkloadPanel(ctx, gkePodReader{g, scope.Created, category}, query, nil, prom.CategoryUnit(category), start, end, step, true)
	if panel.State == "available" && len(panel.Series) < scope.CurrentTotal {
		panel.State = "partial"
		panel.Reason = fmt.Sprintf("GKE reported %d of %d current Pod names in this time range.", len(panel.Series), scope.CurrentTotal)
	}
	panel.Source = "gke"
	return panel
}

func (g *gkeMetrics) workload(ctx context.Context, scope PodScope, start, end time.Time, step time.Duration) workloadMetricsResponse {
	out := workloadMetricsResponse{
		ResourceSource: "gke", State: "available", Pods: len(scope.CurrentPods), PodsTotal: scope.CurrentTotal,
		Start: start.Unix(), End: end.Unix(), StepSeconds: step.Seconds(), RateWindowSeconds: 300,
		Sources: []workloadRequestSource{}, Panels: map[string]workloadMetricPanel{}, Comparison: map[string]workloadMetricPanel{},
		Attribution: map[string]string{"cpu": gkeCurrentPodHistory, "memory": gkeCurrentPodHistory}, History: map[string]workloadMetricScope{},
	}
	if len(scope.CurrentPods) == 0 {
		out.State, out.Reason = "unavailable", "This workload has no current Pods."
		return out
	}
	if scope.Partial() {
		out.State, out.Reason = "partial", "The Pod selection was capped. Values cover the included Pods only."
	}
	for _, category := range []prom.MetricCategory{prom.CategoryCPU, prom.CategoryMemory} {
		key := string(category)
		panel := g.workloadPanel(ctx, scope, category, start, end, step)
		out.Panels[key], out.Comparison[key] = panel, panel
		out.History[key] = workloadMetricScope{Mode: "current-pods", Reason: gkeCurrentPodHistory}
	}
	cpu, memory := out.Panels["cpu"], out.Panels["memory"]
	switch {
	case !hasSample(cpu.Series) && !hasSample(memory.Series):
		out.State, out.Reason = "unavailable", "GKE returned no usable CPU or memory samples for the current Pods in this time range."
		if cpu.State == "error" || memory.State == "error" {
			out.State, out.Reason = "error", "GKE resource metrics could not be read for the current Pods."
		}
	case out.State != "partial" && (cpu.State != "available" || memory.State != "available"):
		out.State, out.Reason = "partial", "GKE returned partial resource metrics for the current Pods in this time range."
	}
	return out
}

// A range evaluation can carry forward an old gauge or include earlier counter
// samples. Exclude its entire lookback after a Pod name is reused.
func trimGKEPodHistory(r *prom.QueryResult, created map[string]time.Time, category prom.MetricCategory) {
	lookback := 5 * time.Minute
	if category == prom.CategoryRestarts {
		lookback = time.Hour
	}
	for i, series := range r.Series {
		at := created[series.Labels["pod"]]
		if at.IsZero() {
			r.Series[i].DataPoints = nil
			continue
		}
		boundary := at.Add(lookback).Unix()
		r.Series[i].DataPoints = slices.DeleteFunc(series.DataPoints, func(p prom.DataPoint) bool { return p.Timestamp <= boundary })
	}
	r.Series = slices.DeleteFunc(r.Series, func(s prom.Series) bool { return len(s.DataPoints) == 0 })
}

type gkePodReader struct {
	*gkeMetrics
	created  map[string]time.Time
	category prom.MetricCategory
}

func (g gkePodReader) QueryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) (*prom.QueryResult, error) {
	r, err := g.store.QueryRange(ctx, q, start, end, step)
	if err == nil && (incompleteResult(r) || len(r.Annotations.Infos) > 0) {
		return nil, prom.ErrPartialResponse
	}
	if err == nil {
		trimGKEPodHistory(r, g.created, g.category)
	}
	return r, err
}

func (g *gkeMetrics) pvc(ctx context.Context, namespace, name string) PVCUsageResponse {
	resp := PVCUsageResponse{Namespace: namespace, Name: name, Status: "no_series"}
	measure := func(metric string) (*float64, error) {
		r, err := g.store.Query(ctx, g.volumeQuery(namespace, name, metric))
		if err != nil {
			return nil, err
		}
		if incompleteResult(r) {
			return nil, prom.ErrPartialResponse
		}
		if len(r.Series) > 1 {
			return nil, errors.New("expected one usage measurement")
		}
		return firstValue(r), nil
	}
	u, err := measure("pod_volume_used_bytes")
	if err != nil {
		resp.Status = PVCUsageQueryFailed
		return resp
	}
	if u == nil {
		return resp
	}
	v, err := measure("pod_volume_total_bytes")
	if err != nil {
		resp.Status = PVCUsageQueryFailed
		return resp
	}
	if v == nil {
		return resp
	}
	usage, ok := pvcUsageOf(*u, *v)
	if !ok {
		resp.Status = "invalid_data"
		return resp
	}
	resp.Used, resp.Capacity, resp.Ratio, resp.HasData, resp.Status = usage.UsedBytes, usage.CapacityBytes, usage.Ratio, true, PVCUsageAvailable
	return resp
}

func missingResourcePanel(p workloadMetricPanel) bool {
	return p.State == "unavailable" && !hasSample(p.Series)
}

func needsGKEResources(resp *workloadMetricsResponse) bool {
	if len(resp.Panels) == 0 {
		return resp.State == "unavailable" || resp.State == "detecting" || resp.State == "error"
	}
	return missingResourcePanel(resp.Panels["cpu"]) || missingResourcePanel(resp.Panels["memory"]) || missingResourcePanel(resp.Comparison["cpu"]) || missingResourcePanel(resp.Comparison["memory"])
}

// A configured store can serve both metric vocabularies. Keep each existing
// panel with data, including historical ownership and HTTP instrumentation.
func mergeGKEResources(ctx context.Context, g *gkeMetrics, resp *workloadMetricsResponse, scope PodScope, start, end time.Time, step time.Duration) {
	if len(scope.CurrentPods) == 0 || !needsGKEResources(resp) {
		return
	}
	if len(resp.Panels) == 0 {
		native := g.workload(ctx, scope, start, end, step)
		if !hasSample(native.Panels["cpu"].Series) && !hasSample(native.Panels["memory"].Series) {
			return
		}
		native.ScopeNotice = resp.ScopeNotice
		if resp.State == "detecting" || resp.State == "error" {
			native.State = "partial"
			native.Reason = strings.TrimSpace(native.Reason + " Conventional Prometheus metrics are incomplete. " + resp.Reason)
		}
		*resp = native
		return
	}
	for _, category := range []prom.MetricCategory{prom.CategoryCPU, prom.CategoryMemory} {
		key := string(category)
		total, comparison := missingResourcePanel(resp.Panels[key]), missingResourcePanel(resp.Comparison[key])
		if !total && !comparison {
			continue
		}
		panel := g.workloadPanel(ctx, scope, category, start, end, step)
		if !hasSample(panel.Series) {
			continue
		}
		if comparison {
			resp.Comparison[key] = panel
		}
		if total {
			if resp.Attribution == nil {
				resp.Attribution = make(map[string]string)
			}
			resp.Panels[key] = panel
			resp.History[key] = workloadMetricScope{Mode: "current-pods", Reason: gkeCurrentPodHistory}
			resp.Attribution[key] = gkeCurrentPodHistory
		}
	}
}
