package prometheus

import (
	"context"
	"errors"
	"fmt"
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
	ScopeNotice       string                         `json:"scopeNotice,omitempty"`
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
	Attribution       map[string]string              `json:"attribution,omitempty"`
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
	var automatic []workloadAttributions
	if !configured && len(scope.CurrentPods) > 0 {
		attributions, state := client.automaticWorkloadAttributions(scope)
		if state != "available" {
			reason := "Matching metrics to current Pods…"
			if state == "error" {
				reason = "Metrics identity could not be checked. Retrying automatically."
			}
			if state == "unavailable" {
				reason = "Current Pod identities are not available yet."
			}
			if client != GetClient() || generation != client.DiscoveryGeneration() {
				writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
				return
			}
			writeJSON(w, http.StatusOK, workloadMetricsResponse{State: state, Reason: reason, ScopeNotice: client.workloadScopeNotice(), Sources: []workloadRequestSource{}, Panels: map[string]workloadMetricPanel{}})
			return
		}
		automatic = append(automatic, attributions)
	}
	resp := collectWorkloadMetrics(ctx, workloadRangeClient{client}, scope, config, job, source, start, end, step, automatic...)
	resp.ScopeNotice = client.workloadScopeNotice()
	if client != GetClient() || generation != client.DiscoveryGeneration() {
		writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type workloadRangeClient struct{ *Client }

func (c workloadRangeClient) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error) {
	if _, _, err := c.EnsureConnected(ctx); err != nil {
		return nil, err
	}
	p := c.getPromClient()
	if p == nil {
		return nil, errors.New("prometheus connection was reset")
	}
	return p.QueryWorkloadRange(ctx, query, start, end, step)
}

func collectWorkloadMetrics(ctx context.Context, client RangeQuerier, scope PodScope, config prom.WorkloadMetricsScope, job string, selected prom.RequestSource, start, end time.Time, step time.Duration, automatic ...workloadAttributions) workloadMetricsResponse {
	resp := workloadMetricsResponse{
		State: "available", Pods: len(scope.CurrentPods), PodsTotal: scope.CurrentTotal, End: end.Unix(),
		Start: start.Unix(), StepSeconds: step.Seconds(),
		RateWindowSeconds: prom.WorkloadRateWindow(step).Seconds(),
		Sources:           []workloadRequestSource{}, Panels: map[string]workloadMetricPanel{},
		Attribution: map[string]string{"scope": "Operator-asserted cluster scope; Pod identity was not established automatically."},
	}
	if len(automatic) > 0 {
		resp.Attribution = map[string]string{}
		for key, plan := range automatic[0] {
			if len(plan.Pods) == 0 {
				resp.Attribution[key] = plan.Reason
				continue
			}
			label := "Matched by cluster label"
			if plan.UID {
				label = "Matched to current Pod UIDs"
			}
			resp.Attribution[key] = fmt.Sprintf("%s · %d of %d current Pods", label, len(plan.Pods), scope.CurrentTotal)
			if len(plan.Pods) < scope.CurrentTotal {
				resp.State, resp.Reason = "partial", "Some sources cover only a subset of current Pods; see attribution details."
			}
		}
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
			if len(automatic) > 0 {
				key := []string{"throttling", "cpu", "memory"}[i]
				plan := automatic[0][key]
				if len(plan.Pods) == 0 {
					pressure[i] = workloadMetricPanel{State: "unavailable", Reason: plan.Reason, Series: []prom.Series{}}
					if plan.State == "error" {
						pressure[i].State = "error"
					}
					return nil
				}
				selection := identitySelection(scope.Namespace, plan.Pods)
				var query string
				var err error
				unit := "percent"
				if i == 0 {
					if plan.UID {
						query, err = prom.BuildIdentityThrottleQuery(step, selection, plan.Pods)
					} else {
						query, err = prom.BuildThrottleQuery(step, selection, plan.Scope)
					}
				} else {
					category := prom.CategoryCPU
					if i == 2 {
						category = prom.CategoryMemory
					}
					unit = prom.CategoryUnit(category)
					if plan.UID {
						query, err = prom.BuildIdentityWorkloadResourceQuery(step, selection, plan.Pods, category)
					} else {
						query, err = prom.BuildWorkloadResourceQuery(step, selection, plan.Scope, category)
					}
				}
				pressure[i] = queryWorkloadPanel(ctx, client, query, err, unit, start, end, step, true)
				if len(plan.Pods) < scope.CurrentTotal && pressure[i].State == "available" {
					pressure[i].State = "partial"
					pressure[i].Reason = fmt.Sprintf("Attributed to %d of %d current Pods; other Pods are excluded.", len(plan.Pods), scope.CurrentTotal)
				}
				return nil
			}
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
			if len(automatic) > 0 {
				plan := automatic[0][string(c.source.ID)]
				if len(plan.Pods) == 0 {
					c.rate = workloadMetricPanel{State: "unavailable", Reason: plan.Reason, Series: []prom.Series{}}
					c.source.State = "unavailable"
					if plan.State == "error" {
						c.rate.State, c.source.State = "error", "error"
					}
					return nil
				}
				selection := identitySelection(scope.Namespace, plan.Pods)
				if plan.UID {
					queries, err = prom.BuildIdentityRequestQueries(step, selection, plan.Pods, c.source.ID, plan.Job)
				} else {
					queries, err = prom.BuildRequestQueries(step, selection, plan.Scope, c.source.ID, plan.Job)
				}
			}
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
	keys := []string{"errors", "p50", "p95", "histogramCoverage", "statusCoverage", "observedPods", "population", "histogramUniformity"}
	queries := []string{c.queries.Errors, c.queries.P50, c.queries.P95, c.queries.HistogramCoverage, c.queries.StatusCoverage, c.queries.ObservedPods, c.queries.Population, c.queries.HistogramUniformity}
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
	panels[0] = workloadRatioPanel(panels[0], c.rate, 100, true)
	panels[3] = workloadRatioPanel(panels[3], c.rate, 1, false)
	panels[3] = withMetricCoverage(panels[3], panels[7], "Histogram bucket populations differ.")
	panels[4] = workloadRatioPanel(panels[4], c.rate, 1, false)
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
		resp.Panels[key] = withMetricCoverage(resp.Panels[key], panels[6], "Multiple or unverified observation populations in this window; affected samples are withheld to avoid duplicate counting.")
	}
	return resp
}

func workloadRatioPanel(numerator, denominator workloadMetricPanel, scale float64, zeroMissing bool) workloadMetricPanel {
	if numerator.State == "error" {
		return numerator
	}
	if denominator.State == "error" {
		return denominator
	}
	values := map[int64]float64{}
	for _, series := range numerator.Series {
		for _, point := range series.DataPoints {
			values[point.Timestamp] = point.Value
		}
	}
	result := workloadMetricPanel{State: "unavailable", Unit: numerator.Unit, Reason: "No defined ratio for these samples.", Series: []prom.Series{}}
	for _, series := range denominator.Series {
		out := prom.Series{Labels: map[string]string{}}
		for _, point := range series.DataPoints {
			n, ok := values[point.Timestamp]
			value := math.NaN()
			if (ok || zeroMissing) && point.Value > 0 && !math.IsInf(point.Value, 0) {
				value = scale * n / point.Value
			}
			out.DataPoints = append(out.DataPoints, prom.DataPoint{Timestamp: point.Timestamp, Value: value})
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				result.State, result.Reason = "available", ""
			}
		}
		result.Series = append(result.Series, out)
	}
	if result.State == "available" && (denominator.State == "stale" || (!zeroMissing && numerator.State == "stale")) {
		result.State, result.Reason = "stale", "Only historical ratio samples are available."
	}
	return result
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
	if len(query) > prom.MaxWorkloadQueryBytes {
		panel.State = "error"
		panel.Reason = "This Pod population requires a query larger than Radar's safe metrics query limit; no Pods were silently dropped."
		return panel
	}
	result, err := client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Printf("[prometheus] Workload metric query failed: %v", err)
		panel.State, panel.Reason = "error", "The metrics query failed. Check the Prometheus connection and query support."
		if errors.Is(err, prom.ErrPartialResponse) {
			panel.Reason = "The metrics backend returned a partial response; samples are withheld to avoid misleading totals."
		}
		if errors.Is(err, prom.ErrQueryWarning) {
			panel.Reason = "The metrics backend returned a query warning; samples are withheld because the result may be incomplete or unreliable."
		}
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
		if coverage.Reason != "" {
			panel.Reason += " " + coverage.Reason
		}
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
