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

func (g *gkeMetrics) workload(ctx context.Context, scope PodScope, start, end time.Time, step time.Duration) workloadMetricsResponse {
	const reason = "GKE system metrics cover the workload’s current Pods. Samples from before each Pod’s creation and its first five minutes are excluded."
	out := workloadMetricsResponse{
		ResourceSource: "gke", State: "available", Pods: len(scope.CurrentPods), PodsTotal: scope.CurrentTotal,
		Start: start.Unix(), End: end.Unix(), StepSeconds: step.Seconds(), RateWindowSeconds: 300,
		Sources: []workloadRequestSource{}, Panels: map[string]workloadMetricPanel{}, Comparison: map[string]workloadMetricPanel{},
		Attribution: map[string]string{"cpu": reason, "memory": reason}, History: map[string]workloadMetricScope{},
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
		query := g.chartQuery(scope.Kind, scope.Namespace, scope.Name, category, &scope)
		panel := queryWorkloadPanel(ctx, gkePodReader{g, scope.Created, category}, query, nil, prom.CategoryUnit(category), start, end, step, true)
		if panel.State == "available" && len(panel.Series) < scope.CurrentTotal {
			panel.State = "partial"
			panel.Reason = fmt.Sprintf("GKE reported %d of %d current Pod names in this time range.", len(panel.Series), scope.CurrentTotal)
		}
		panel.Source = "gke"
		out.Panels[key], out.Comparison[key] = panel, panel
		out.History[key] = workloadMetricScope{Mode: "current-pods", Reason: reason}
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

// A configured store can serve both metric vocabularies. Keep each existing
// panel with data, including historical ownership and HTTP instrumentation.
func mergeGKEResources(ctx context.Context, g *gkeMetrics, resp *workloadMetricsResponse, scope PodScope, start, end time.Time, step time.Duration) {
	needs := func(key string) bool {
		p := resp.Panels[key]
		state := p.State
		if state == "" {
			state = resp.State
		}
		return len(p.Series) == 0 && (state == "unavailable" || state == "detecting" || state == "error")
	}
	if !needs("cpu") && !needs("memory") {
		return
	}
	native := g.workload(ctx, scope, start, end, step)
	if len(resp.Panels) == 0 {
		if len(native.Panels["cpu"].Series) == 0 && len(native.Panels["memory"].Series) == 0 {
			return
		}
		native.ScopeNotice = resp.ScopeNotice
		if resp.State == "detecting" || resp.State == "error" {
			native.State = "partial"
			native.Reason = strings.TrimSpace(native.Reason + " Conventional Prometheus workload identity is unverified. GKE samples cover current Pods.")
		}
		*resp = native
		return
	}
	filled := false
	for _, key := range []string{"cpu", "memory"} {
		if !needs(key) || len(native.Panels[key].Series) == 0 {
			continue
		}
		if resp.Attribution == nil {
			resp.Attribution = make(map[string]string)
		}
		resp.Panels[key], resp.History[key], resp.Attribution[key] = native.Panels[key], native.History[key], native.Attribution[key]
		if len(resp.Comparison[key].Series) == 0 {
			resp.Comparison[key] = native.Comparison[key]
		}
		filled = true
	}
	if filled && resp.State == "unavailable" {
		resp.State, resp.Reason = native.State, native.Reason
		resp.Start, resp.End, resp.StepSeconds, resp.RateWindowSeconds = native.Start, native.End, native.StepSeconds, native.RateWindowSeconds
		resp.Pods, resp.PodsTotal = native.Pods, native.PodsTotal
	}
}
