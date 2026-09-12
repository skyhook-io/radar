package prometheus

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	"golang.org/x/sync/errgroup"
)

type workloadMetricPanel struct {
	State  string        `json:"state"`
	Reason string        `json:"reason,omitempty"`
	Unit   string        `json:"unit"`
	Series []prom.Series `json:"series"`
}

type workloadRequestSource struct {
	ID    prom.RequestSource `json:"id"`
	Label string             `json:"label"`
	State string             `json:"state"`
}

type workloadMetricsResponse struct {
	SetupRequired     bool                           `json:"setupRequired"`
	State             string                         `json:"state"`
	Reason            string                         `json:"reason,omitempty"`
	Source            prom.RequestSource             `json:"source,omitempty"`
	Sources           []workloadRequestSource        `json:"sources"`
	Pods              int                            `json:"pods"`
	PodsTotal         int                            `json:"podsTotal"`
	End               int64                          `json:"end"`
	Start             int64                          `json:"start"`
	StepSeconds       float64                        `json:"stepSeconds"`
	RateWindowSeconds float64                        `json:"rateWindowSeconds"`
	Panels            map[string]workloadMetricPanel `json:"panels"`
}

func handleWorkloadMetrics(w http.ResponseWriter, r *http.Request) {
	if !k8s.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "Not connected to cluster")
		return
	}
	kind := k8s.CanonicalWorkloadKind(chi.URLParam(r, "kind"))
	if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
		writeError(w, http.StatusBadRequest, "workload metrics support Deployment, StatefulSet and DaemonSet")
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !canReadMetricsResource(r, kind, namespace) || !canRead(r, "", "pods", namespace, "list") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	source := prom.RequestSource(r.URL.Query().Get("source"))
	if source != "" && source != prom.RequestSourceBeyla && source != prom.RequestSourceIstio {
		writeError(w, http.StatusBadRequest, "unsupported request source")
		return
	}
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}
	generation := client.DiscoveryGeneration()
	config, job, configured := client.workloadMetricsConfig()
	if !configured {
		if client != GetClient() || generation != client.DiscoveryGeneration() {
			writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
			return
		}
		writeJSON(w, http.StatusOK, workloadMetricsResponse{
			State: "unavailable", SetupRequired: true, Reason: "Request and pressure metrics need a confirmed cluster scope. Existing resource charts remain available.",
			Sources: []workloadRequestSource{}, Panels: map[string]workloadMetricPanel{},
		})
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		writeError(w, http.StatusServiceUnavailable, "cluster cache not ready")
		return
	}
	scope, err := ResolvePodScope(cache, kind, namespace, name, 100)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, k8s.ErrWorkloadAccessDenied) {
			status = http.StatusForbidden
		} else if errors.Is(err, k8s.ErrWorkloadCacheWarming) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "could not resolve current workload pods")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	start, end, step := parseTimeRange(r.URL.Query().Get("range"))
	step = max(step, end.Sub(start)/360)
	resp := collectWorkloadMetrics(ctx, client, scope, config, job, source, start, end, step)
	if client != GetClient() || generation != client.DiscoveryGeneration() {
		writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func collectWorkloadMetrics(ctx context.Context, client RangeQuerier, scope PodScope, config prom.WorkloadMetricsScope, job string, selected prom.RequestSource, start, end time.Time, step time.Duration) workloadMetricsResponse {
	resp := workloadMetricsResponse{
		State: "available", Pods: len(scope.CurrentPods), PodsTotal: scope.CurrentTotal, End: end.Unix(),
		Start: start.Unix(), StepSeconds: step.Seconds(),
		RateWindowSeconds: prom.WorkloadRateWindow(step).Seconds(),
		Sources:           []workloadRequestSource{}, Panels: map[string]workloadMetricPanel{},
	}
	if scope.Partial() {
		resp.State = "partial"
		resp.Reason = "Current pod population was capped; values cover only the included pods."
	}
	if scope.Selection.IsEmpty() {
		resp.State, resp.Reason = "unavailable", "This workload has no current pods."
		return resp
	}
	type candidate struct {
		source  workloadRequestSource
		queries prom.RequestQueries
		rate    workloadMetricPanel
	}
	candidates := []candidate{
		{source: workloadRequestSource{ID: prom.RequestSourceIstio, Label: "Istio · destination sidecars"}},
		{source: workloadRequestSource{ID: prom.RequestSourceBeyla, Label: "Beyla · HTTP server"}},
	}
	pressure := make([]workloadMetricPanel, 3)
	var group errgroup.Group
	group.SetLimit(3)
	for i := range pressure {
		group.Go(func() error {
			query, err := prom.BuildThrottleQuery(step, scope.Selection, config)
			unit := "percent"
			if i > 0 {
				category := prom.CategoryCPU
				if i == 2 {
					category = prom.CategoryMemory
				}
				query, err = prom.BuildWorkloadResourceQuery(step, scope.Selection, config, category)
				unit = prom.CategoryUnit(category)
			}
			pressure[i] = queryWorkloadPanel(ctx, client, query, err, unit, start, end, step, true)
			return nil
		})
	}
	for i := range candidates {
		group.Go(func() error {
			c := &candidates[i]
			queries, err := prom.BuildRequestQueries(step, scope.Selection, config, c.source.ID, job)
			c.queries = queries
			c.rate = queryWorkloadPanel(ctx, client, queries.Rate, err, "requests/s", start, end, step, false)
			c.source.State = c.rate.State
			return nil
		})
	}
	_ = group.Wait()
	resp.Panels["throttling"] = pressure[0]
	resp.Panels["cpu"] = pressure[1]
	resp.Panels["memory"] = pressure[2]
	chosen := -1
	for i, c := range candidates {
		resp.Sources = append(resp.Sources, c.source)
		if selected == c.source.ID || (selected == "" && chosen == -1 && (c.rate.State == "available" || c.rate.State == "stale")) {
			chosen = i
		}
	}
	if chosen == -1 {
		resp.Panels["requests"] = workloadMetricPanel{State: "unavailable", Unit: "requests/s", Reason: "No attributed HTTP request metrics found. Istio needs destination-reporter pod labels; Beyla needs HTTP server metrics with Kubernetes pod labels. Waypoint and ingress observations are not included.", Series: []prom.Series{}}
		for _, c := range candidates {
			if c.rate.State == "error" {
				resp.Panels["requests"] = c.rate
				break
			}
		}
		return resp
	}
	c := candidates[chosen]
	resp.Source = c.source.ID
	resp.Panels["requests"] = c.rate
	if c.rate.State == "error" || c.rate.State == "unavailable" {
		return resp
	}
	keys := []string{"errors", "p50", "p95", "histogramCoverage", "statusCoverage", "observedPods"}
	queries := []string{c.queries.Errors, c.queries.P50, c.queries.P95, c.queries.HistogramCoverage, c.queries.StatusCoverage, c.queries.ObservedPods}
	panels := make([]workloadMetricPanel, len(keys))
	for i := range keys {
		group.Go(func() error {
			unit := "seconds"
			if i == 0 {
				unit = "percent"
			} else if i == 5 {
				unit = "pods"
			}
			panels[i] = queryWorkloadPanel(ctx, client, queries[i], nil, unit, start, end, step, false)
			return nil
		})
	}
	_ = group.Wait()
	resp.Panels["observedPods"] = panels[5]
	for i, key := range keys[:3] {
		panel := panels[i]
		if i > 0 {
			panel = withMetricCoverage(panel, panels[3], "Histogram buckets do not cover the same requests as the counter; latency is withheld for those samples.")
		} else {
			panel = withMetricCoverage(panel, panels[4], "HTTP status labels do not cover every request; error percentage is withheld for those samples.")
		}
		resp.Panels[key] = panel
	}
	for _, key := range []string{"requests", "errors", "p50", "p95"} {
		resp.Panels[key] = withObservedPodCoverage(resp.Panels[key], panels[5], len(scope.CurrentPods))
	}
	return resp
}

func withObservedPodCoverage(panel, observed workloadMetricPanel, total int) workloadMetricPanel {
	if panel.State == "error" || panel.State == "unavailable" {
		return panel
	}
	counts := map[int64]float64{}
	for _, series := range observed.Series {
		for _, point := range series.DataPoints {
			counts[point.Timestamp] = point.Value
		}
	}
	for _, series := range panel.Series {
		for _, point := range series.DataPoints {
			if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
				continue
			}
			if count, ok := counts[point.Timestamp]; !ok || count != float64(total) {
				if panel.State != "stale" {
					panel.State = "partial"
				}
				if panel.Reason != "" {
					panel.Reason += " "
				}
				panel.Reason += "Some samples do not cover every current Pod. Values describe reporting Pods only; other Pods may be idle, new, or not instrumented."
				return panel
			}
		}
	}
	return panel
}

func queryWorkloadPanel(ctx context.Context, client RangeQuerier, query string, buildErr error, unit string, start, end time.Time, step time.Duration, keepPod bool) workloadMetricPanel {
	panel := workloadMetricPanel{State: "unavailable", Unit: unit, Series: []prom.Series{}, Reason: "No usable samples in this window."}
	if buildErr != nil {
		panel.State, panel.Reason = "error", buildErr.Error()
		return panel
	}
	result, err := client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Workload metric query failed: %v", err)
		panel.State, panel.Reason = "error", "The metrics query failed. Check the Prometheus connection and query support."
		return panel
	}
	latest := int64(0)
	hasFinite := false
	for _, series := range result.Series {
		labels := map[string]string{}
		if keepPod {
			labels["pod"] = series.Labels["pod"]
		}
		panel.Series = append(panel.Series, prom.Series{Labels: labels, DataPoints: series.DataPoints})
		for _, point := range series.DataPoints {
			latest = max(latest, point.Timestamp)
			hasFinite = hasFinite || (!math.IsNaN(point.Value) && !math.IsInf(point.Value, 0))
		}
	}
	if hasFinite {
		panel.State, panel.Reason = "available", ""
		if end.Unix()-latest > int64(max(2*step, 90*time.Second).Seconds()) {
			panel.State, panel.Reason = "stale", "Only historical samples are available; no recent rate evaluation."
		}
	}
	return panel
}

func withMetricCoverage(panel, coverage workloadMetricPanel, reason string) workloadMetricPanel {
	if panel.State == "error" || panel.State == "unavailable" {
		return panel
	}
	if coverage.State == "error" {
		panel.State, panel.Reason = "error", "Metric coverage could not be verified."
		panel.Series = []prom.Series{}
		return panel
	}
	complete := map[int64]bool{}
	for _, series := range coverage.Series {
		for _, point := range series.DataPoints {
			complete[point.Timestamp] = math.Abs(point.Value-1) < 0.000001
		}
	}
	partial := false
	for i := range panel.Series {
		for j := range panel.Series[i].DataPoints {
			point := &panel.Series[i].DataPoints[j]
			if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
				continue
			}
			if !complete[point.Timestamp] {
				point.Value = math.NaN()
				partial = true
			}
		}
	}
	if partial {
		if panel.State != "stale" {
			panel.State = "partial"
		}
		if panel.Reason != "" {
			panel.Reason += " "
		}
		panel.Reason += reason
	}
	return panel
}
