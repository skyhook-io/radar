package prometheus

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	if g.owner != nil && g.owner.gkeChartStore() != g {
		return ChartAnswer{}, errDiscoverySuperseded
	}
	if pods != nil {
		trimGKEPodHistory(r, pods.Created, category)
	}
	return ChartAnswer{Result: r, Query: q, Partial: incompleteResult(r)}, nil
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
		return len(p.Series) == 0 && (p.State == "unavailable" || (p.State == "" && resp.State == "unavailable"))
	}
	if !needs("cpu") && !needs("memory") {
		return
	}
	native := g.workload(ctx, scope, start, end, step)
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
		if resp.State == "unavailable" {
			resp.State, resp.Reason = native.State, native.Reason
		}
	}
}
