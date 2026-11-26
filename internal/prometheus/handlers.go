package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// RegisterRoutes registers Prometheus metric routes on the given router.
type Routes struct {
	Capture func(context.Context) (*Client, error)
}

func (h Routes) RegisterRoutes(r chi.Router) {
	r.Get("/prometheus/status", h.handleStatus)
	r.Post("/prometheus/connect", h.handleConnect)
	r.Get("/prometheus/resources/{kind}/{namespace}/{name}", h.handleResourceMetrics)
	r.Get("/prometheus/resources/{kind}/{name}", h.handleClusterScopedResourceMetrics)
	r.Get("/prometheus/namespace/{namespace}", h.handleNamespaceMetrics)
	r.Get("/prometheus/cluster", h.handleClusterMetrics)
	r.Get("/prometheus/query", h.handleRawQuery)
	r.Get("/prometheus/hpa/{namespace}/{name}", h.handleHPAMetrics)
	r.Get("/prometheus/pvc/{namespace}/{name}", h.handlePVCUsage)
	r.Get("/prometheus/rightsizing/{kind}/{namespace}/{name}", h.handleRightsizing)
	r.Get("/prometheus/workload/{kind}/{namespace}/{name}", h.handleWorkloadMetrics)
	r.Get("/prometheus/substrate/workerpools/{namespace}/{name}", h.handleSubstrateWorkerPool)
	r.Get("/prometheus/substrate/nodes/{name}", h.handleSubstrateNode)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[prometheus] Failed to encode JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

var openObservePath atomic.Pointer[func() string]

// SetOpenObservePath installs the check that says how this install can get
// OpenObserve metrics: "hub" (Radar Hub selects the source), "local" (Settings
// → Metrics) or "" (it cannot). The server owns the answer; passing it in
// avoids an import cycle.
func SetOpenObservePath(fn func() string) {
	openObservePath.Store(&fn)
}

type statusResponse struct {
	prom.Status
	OpenObserve string `json:"openobserve,omitempty"`
}

// handleStatus returns the current Prometheus connection status.
func (h Routes) handleStatus(w http.ResponseWriter, r *http.Request) {
	var out statusResponse
	if client, connectionErr := h.Capture(r.Context()); connectionErr != nil {
		out.Status = prom.Status{Available: false, Error: connectionErr.Error()}
	} else {
		out.Status = client.GetStatus()
	}
	if fn := openObservePath.Load(); fn != nil {
		out.OpenObserve = (*fn)()
	}
	writeJSON(w, http.StatusOK, out)
}

// checkHubMetricsSource is cloud.CheckMetricsSourceNow; a variable for tests.
var checkHubMetricsSource = cloud.CheckMetricsSourceNow

// hubMetricsSourceCheckWait covers Hub's own request timeout plus the
// poller's minimum gap between requests.
const hubMetricsSourceCheckWait = 12 * time.Second

// handleConnect triggers Prometheus discovery and connection. The endpoint
// has no body or query parameters — the Prometheus URL is configured at
// process startup via --prometheus-url, never per-request. Accepting a URL
// here would let any caller redirect Prometheus queries to an arbitrary
// host (SSRF) since radar binds to 0.0.0.0 by default.
func (h Routes) handleConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	optional := r.URL.Query().Get("optional") == "true"
	var checked bool
	if !optional {
		// A user asking for discovery first gets Radar Hub's current choice.
		checkCtx, cancel := context.WithTimeout(ctx, hubMetricsSourceCheckWait)
		checked = checkHubMetricsSource(checkCtx)
		cancel()
	}
	client, connectionErr := h.Capture(ctx)
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}
	if checked && client.relayActive() {
		writeJSON(w, http.StatusOK, client.GetStatus())
		return
	}
	if optional {
		ctx = withSuppressedDiscoveryDiagnostics(ctx)
	} else {
		client.mu.Lock()
		if client.gke == nil {
			client.gkeChecked = time.Time{}
			client.gkeServices = nil
		}
		client.mu.Unlock()
	}
	_, _, err := client.connectWithGKE(ctx)
	if client.Closed() || portforward.IsMemberClosed(ctx, err) {
		client.writeChartFailure(w, ctx, err)
		return
	}
	if err != nil && client.gkeChartStore() == nil {
		log.Printf("[prometheus] Connection failed: %v", err)
		if optional {
			status := client.GetStatus()
			status.Error = "Prometheus connection failed: " + err.Error()
			writeJSON(w, http.StatusOK, status)
			return
		}
		// Turned-off port-forwarding is the operator's setting, not a failure.
		if !errors.Is(err, portforward.ErrAutoDisabled) {
			errorlog.Record("prometheus", "error", "connection failed: %v", err)
		}
		writeError(w, http.StatusBadGateway, "Prometheus connection failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, client.GetStatus())
}

var timeRanges = map[string]struct{ duration, step time.Duration }{
	"10m": {10 * time.Minute, 15 * time.Second},
	"30m": {30 * time.Minute, 30 * time.Second},
	"1h":  {time.Hour, time.Minute},
	"3h":  {3 * time.Hour, 2 * time.Minute},
	"6h":  {6 * time.Hour, 5 * time.Minute},
	"12h": {12 * time.Hour, 10 * time.Minute},
	"24h": {24 * time.Hour, 15 * time.Minute},
	"48h": {48 * time.Hour, 30 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"14d": {14 * 24 * time.Hour, 2 * time.Hour},
}

// normalizeTimeRange returns the range parseTimeRange serves for rangeStr.
func normalizeTimeRange(rangeStr string) string {
	if _, ok := timeRanges[rangeStr]; ok {
		return rangeStr
	}
	return "1h"
}

// parseTimeRange parses the "range" query parameter into start/end/step.
// Supported values: 10m, 30m, 1h, 3h, 6h, 12h, 24h, 48h, 7d, 14d (default: 1h).
// The frontend UI exposes a subset of these; the full set is available via the API.
func parseTimeRange(rangeStr string) (start, end time.Time, step time.Duration) {
	end = time.Now()
	r, ok := timeRanges[rangeStr]
	if !ok {
		if rangeStr != "" {
			log.Printf("[prometheus] Unrecognized range %q, falling back to 1h", rangeStr)
		}
		r = timeRanges["1h"]
	}
	return end.Add(-r.duration), end, r.step
}

type chartMeasurement struct {
	Source       string            `json:"source,omitempty"`
	Result       *prom.QueryResult `json:"result"`
	ClusterScope *ClusterScope     `json:"clusterScope,omitempty"`
	Partial      bool              `json:"partial,omitempty"`
	Query        string            `json:"query,omitempty"`
	Hint         string            `json:"hint,omitempty"`
}

// ResourceMetricsResponse is the response shape for resource metrics.
type ResourceMetricsResponse struct {
	chartMeasurement
	Kind      string              `json:"kind"`
	Namespace string              `json:"namespace,omitempty"`
	Name      string              `json:"name"`
	Category  prom.MetricCategory `json:"category"`
	Unit      string              `json:"unit"`
	Range     string              `json:"range"`
	// Workload kinds carry the pods their query named, established by
	// controller ownership: Pods counts them, PodsTotal the workload's full
	// set when the cap cut the list. Pods is a pointer because zero is an
	// answer — a workload that controls none — and has to survive the wire
	// distinct from a kind that has no pod scope at all, such as a Node.
	Pods      *int `json:"pods,omitempty"`
	PodsTotal int  `json:"podsTotal,omitempty"`
}

// restMaxScopePods caps the pod list a chart query names, bounding the regex
// the chart sends.
const restMaxScopePods = 500

// chartDeps are what a chart needs beyond its request: the store, the scope
// check and the resource cache that anchors it.
type chartDeps struct {
	gke        *gkeMetrics
	supplement func(context.Context) (*gkeMetrics, error)
	q          RangeQuerier
	resolve    ChartScopeResolver
	cache      *k8s.ResourceCache
	// connectErr is why the store couldn't be connected; the chart answers
	// it as a failed query before checking or querying anything.
	connectErr error
}

// unconnected is the error a chart answers when its store couldn't be
// connected: the caller's own end, else the connect failure as a failed query.
func (d chartDeps) unconnected(ctx context.Context) error {
	if d.connectErr == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(d.connectErr, errDiscoverySuperseded) {
		return errDiscoverySuperseded
	}
	return errChartQuery{d.connectErr}
}

// productionChartDeps runs a chart's queries on one store handle, and its
// check only on the connection that handle was taken from.
func productionChartDeps(ctx context.Context, client *Client) chartDeps {
	cache := client.cache
	stores := client.chartStores(ctx)
	return chartDeps{gke: stores.native, supplement: stores.supplement, q: stores.standard, resolve: BoundResolver(client, stores.standard.conn, chartResolverFor(client, cache)), cache: cache, connectErr: stores.standard.err}
}

// chartResolverFor builds the charts' check on the client's current
// connection; tests replace it to move the connection around the check.
var chartResolverFor = ResolverFor

// errChartQuery marks a store failure that requires an HTTP response.
type errChartQuery struct{ err error }

func (e errChartQuery) Error() string { return e.err.Error() }
func (e errChartQuery) Unwrap() error { return e.err }

func chartRangeDuration(rangeStr string) time.Duration {
	return timeRanges[normalizeTimeRange(rangeStr)].duration
}

const ConnectionClosedMessage = "Metrics connection closed. Retry the request"

func (c *Client) writeChartFailure(w http.ResponseWriter, ctx context.Context, err error) bool {
	if c.Closed() {
		err = portforward.ErrClosed
	}
	if err == nil {
		return false
	}
	writeChartError(w, ctx, err)
	return true
}

// writeChartError reports a closed or changed connection and store query failures.
func writeChartError(w http.ResponseWriter, ctx context.Context, err error) {
	if portforward.IsMemberClosed(ctx, err) {
		writeError(w, http.StatusServiceUnavailable, ConnectionClosedMessage)
		return
	}
	if errors.Is(err, errDiscoverySuperseded) {
		writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
		return
	}
	var queryErr errChartQuery
	if errors.As(err, &queryErr) {
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+queryErr.Error())
	}
}

// handleResourceMetrics returns Prometheus metrics for a specific resource.
// Query params: category (cpu|memory|network_rx|network_tx|filesystem, default: cpu), range (10m|30m|1h|...|14d, default: 1h)
func (h Routes) handleResourceMetrics(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := h.Capture(r.Context())
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	category := prom.MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = prom.CategoryCPU
	}

	// Validate kind is supported
	supported := false
	for _, k := range prom.SupportedKinds() {
		if strings.EqualFold(k, kind) {
			kind = k // normalize casing
			supported = true
			break
		}
	}
	if !supported {
		writeError(w, http.StatusBadRequest, "unsupported resource kind: "+kind)
		return
	}
	if !canReadMetricsResource(r, kind, namespace) {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Validate category
	validCategories := prom.CategoriesForKind(kind)
	categoryValid := false
	for _, c := range validCategories {
		if c == category {
			categoryValid = true
			break
		}
	}
	if !categoryValid {
		writeError(w, http.StatusBadRequest, "unsupported metric category for "+kind+": "+string(category))
		return
	}

	rangeStr := r.URL.Query().Get("range")

	// A Pod is named exactly and a Node has no pods; a workload's pods are
	// resolved by ownership, never guessed from the workload's name.
	var scope *PodScope
	if !strings.EqualFold(kind, "Pod") && !strings.EqualFold(kind, "Node") {
		cache := client.cache
		if cache == nil {
			writeError(w, http.StatusServiceUnavailable, "cluster cache not ready")
			return
		}
		resolved, err := ResolvePodScope(r.Context(), cache, kind, namespace, name, restMaxScopePods)
		if err != nil {
			switch {
			case errors.Is(err, k8s.ErrWorkloadCacheWarming):
				writeError(w, http.StatusServiceUnavailable, "cluster cache is still loading the workload's pods: "+err.Error())
			case errors.Is(err, k8s.ErrWorkloadAccessDenied):
				// Every path to this error is a permission one: a lister the
				// identity may not have, or a namespace its informer was
				// scoped away from. Retrying will not change it, and 503
				// tells the caller it might.
				writeError(w, http.StatusForbidden, "cluster cache cannot establish the workload's Pod membership: "+err.Error())
			case errors.Is(err, ErrPodScopeUnsupportedKind):
				writeError(w, http.StatusBadRequest, "cannot resolve pods for "+kind)
			case errors.Is(err, context.Canceled):
				// The caller left while a Rollout or Workflow inventory was
				// still starting; there is nobody to answer.
				return
			case errors.Is(err, k8s.ErrUnknownDynamicKind):
				writeError(w, http.StatusBadRequest, k8s.CanonicalWorkloadKind(kind)+" (argoproj.io) is not installed or not yet discovered in this cluster")
			default:
				log.Printf("[prometheus] Pod scope failed for %q/%q/%q: %v", kind, namespace, name, err)
				writeError(w, http.StatusBadGateway, "could not resolve the workload's pods: "+err.Error())
			}
			return
		}
		scope = &resolved
	}
	if resourceChartQuery(kind, namespace, name, category, scope)(true, nil) == "" {
		writeError(w, http.StatusBadRequest, "cannot build query for "+kind+"/"+string(category))
		return
	}

	resp, err := resourceChart(r.Context(), productionChartDeps(r.Context(), client), kind, namespace, name, category, rangeStr, scope)
	if client.writeChartFailure(w, r.Context(), err) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// resourceChartQuery builds a Pod's, a Node's or a resolved workload's query.
func resourceChartQuery(kind, namespace, name string, category prom.MetricCategory, scope *PodScope) func(bool, prom.ClusterFilter) string {
	if scope != nil {
		return func(filterContainer bool, filter prom.ClusterFilter) string {
			return prom.BuildScopedQueryFiltered(scope.Selection, category, prom.AggregatePerPod, filterContainer, filter)
		}
	}
	return func(filterContainer bool, filter prom.ClusterFilter) string {
		return prom.BuildQueryFiltered(kind, namespace, name, category, filterContainer, filter)
	}
}

// resourceChartScopeRequest is the scope check for one resource chart, or
// false for a workload that controls no Pods: its query matches nothing, so
// nothing can mix.
func resourceChartScopeRequest(kind, namespace, name string, category prom.MetricCategory, chartRange time.Duration, scope *PodScope, cache *k8s.ResourceCache) (ViewScopeRequest, bool) {
	switch {
	case scope != nil:
		if scope.Selection.IsEmpty() {
			return ViewScopeRequest{}, false
		}
		return PodSetScopeRequest("workload_chart", scope.Selection, []prom.MetricCategory{category}, ChartScopeSpan(chartRange, category)), true
	case strings.EqualFold(kind, "Node"):
		return nodeChartScopeRequest(name, category, chartRange, cache), true
	default:
		return podChartScopeRequest(namespace, name, category, chartRange, cache), true
	}
}

// resourceChart charts one Pod, Node or resolved workload kept to this
// cluster.
func resourceChart(ctx context.Context, d chartDeps, kind, namespace, name string, category prom.MetricCategory, rangeStr string, scope *PodScope) (ResourceMetricsResponse, error) {
	resp := ResourceMetricsResponse{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Category:  category,
		Unit:      prom.CategoryUnitForKind(kind, category),
		Range:     rangeStr,
	}
	if scope != nil {
		pods := len(scope.CurrentPods)
		resp.Pods = &pods
		resp.PodsTotal = scope.CurrentTotal
	}
	var err error
	resp.chartMeasurement, err = d.readChart(ctx, kind, namespace, name, category, rangeStr, scope)
	return resp, err
}

// Each channel supplies one whole chart. Preserve conventional data, partial
// results and failures; only replace a complete empty answer with native samples.
func (d chartDeps) readChart(ctx context.Context, kind, namespace, name string, category prom.MetricCategory, rangeStr string, pods *PodScope) (chartMeasurement, error) {
	start, end, step := parseTimeRange(rangeStr)
	var conventional chartMeasurement
	if d.gke == nil || d.gke.fallback {
		var err error
		conventional, err = d.readConventionalChart(ctx, kind, namespace, name, category, pods, start, end, step)
		if (d.gke == nil && d.supplement == nil) || err != nil || conventional.Partial || hasSample(conventional.Result.Series) {
			return conventional, err
		}
	}
	if d.supplement != nil {
		var err error
		d.gke, err = d.supplement(ctx)
		if err != nil {
			return chartMeasurement{}, err
		}
		if d.gke == nil {
			return conventional, nil
		}
	}
	if strings.EqualFold(kind, "Pod") && pods == nil {
		if d.cache != nil && d.cache.Pods() != nil {
			if p, err := d.cache.Pods().Pods(namespace).Get(name); err == nil && !p.CreationTimestamp.IsZero() {
				current := PodScopeFromPods(kind, namespace, name, []*corev1.Pod{p}, 1)
				pods = &current
			}
		}
		if pods == nil {
			if d.gke.fallback {
				return conventional, nil
			}
			return chartMeasurement{Source: "gke", ClusterScope: &d.gke.scope, Result: emptyChartResult(), Hint: "The current Pod’s creation time is unavailable. Its history could include an earlier Pod with the same name."}, nil
		}
	}
	a, err := d.gke.chart(ctx, kind, namespace, name, category, pods, start, end, step)
	if ctx.Err() != nil {
		return chartMeasurement{}, ctx.Err()
	}
	if errors.Is(err, errDiscoverySuperseded) {
		return chartMeasurement{}, err
	}
	if d.gke.fallback && (err != nil || !hasSample(a.Result.Series)) {
		return conventional, nil
	}
	return chartMeasurement{Source: "gke", ClusterScope: &d.gke.scope, Result: a.Result, Partial: a.Partial}, err
}

func (d chartDeps) readConventionalChart(ctx context.Context, kind, namespace, name string, category prom.MetricCategory, pods *PodScope, start, end time.Time, step time.Duration) (chartMeasurement, error) {
	var resp chartMeasurement
	if err := d.unconnected(ctx); err != nil {
		return resp, err
	}
	var req ViewScopeRequest
	check := true
	var build func(bool, prom.ClusterFilter) string
	switch kind {
	case "Cluster":
		req = clusterChartScopeRequest(category, end.Sub(start), d.cache)
		build = func(containers bool, filter prom.ClusterFilter) string {
			return prom.BuildClusterQueryFiltered(category, containers, filter)
		}
	case "Namespace":
		req = namespaceChartScopeRequest(namespace, category, end.Sub(start), d.cache)
		build = func(containers bool, filter prom.ClusterFilter) string {
			return prom.BuildNamespaceQueryFiltered(namespace, category, containers, filter)
		}
	default:
		req, check = resourceChartScopeRequest(kind, namespace, name, category, end.Sub(start), pods, d.cache)
		build = resourceChartQuery(kind, namespace, name, category, pods)
	}
	var view ViewScope
	if check {
		var err error
		view, err = ResolveChartScope(ctx, d.resolve, req)
		if err != nil {
			return resp, err
		}
	}
	resp.ClusterScope = ClusterScopeOf(view)
	if view.Withheld {
		resp.Result = emptyChartResult()
		return resp, nil
	}
	subject := fmt.Sprintf("%q/%q/%q (%q)", kind, namespace, name, category)
	a, err := QueryScopedChart(ctx, d.q, view, category, build, start, end, step, "Primary query empty for "+subject)
	if err != nil {
		if !portforward.IsMemberClosed(ctx, err) {
			log.Printf("[prometheus] Query failed for %s: %v", subject, err)
			errorlog.Record("prometheus", "error", "query failed for %s: %v", subject, err)
		}
		return resp, errChartQuery{err}
	}
	resp.Result, resp.Partial = a.Result, a.Partial
	if kind != "Cluster" && kind != "Namespace" && len(a.Result.Series) == 0 {
		resp.Query = a.Query
		resp.Hint = detectCRIDockerHint(d.cache, kind, namespace, name)
		log.Printf("[prometheus] Empty result for %s, query: %q", subject, a.Query)
		errorlog.Record("prometheus", "warning", "empty result for %s: %q", subject, a.Query)
	}
	return resp, nil
}

// handleClusterScopedResourceMetrics handles metrics for cluster-scoped resources (e.g. Node).
func (h Routes) handleClusterScopedResourceMetrics(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := h.Capture(r.Context())
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")

	// Only Node is a cluster-scoped kind with metrics
	if !strings.EqualFold(kind, "Node") {
		writeError(w, http.StatusBadRequest, "unsupported cluster-scoped resource kind: "+kind)
		return
	}
	kind = "Node"
	if !canReadMetricsResource(r, kind, "") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	category := prom.MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = prom.CategoryCPU
	}

	validCategories := prom.CategoriesForKind(kind)
	categoryValid := false
	for _, c := range validCategories {
		if c == category {
			categoryValid = true
			break
		}
	}
	if !categoryValid {
		writeError(w, http.StatusBadRequest, "unsupported metric category for "+kind+": "+string(category))
		return
	}

	if prom.BuildQuery(kind, "", name, category) == "" {
		writeError(w, http.StatusBadRequest, "cannot build query for "+kind+"/"+string(category))
		return
	}

	resp, err := resourceChart(r.Context(), productionChartDeps(r.Context(), client), kind, "", name, category, r.URL.Query().Get("range"), nil)
	if client.writeChartFailure(w, r.Context(), err) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// NamespaceMetricsResponse is the response shape for namespace-level metrics.
type NamespaceMetricsResponse struct {
	chartMeasurement
	Namespace string              `json:"namespace"`
	Category  prom.MetricCategory `json:"category"`
	Unit      string              `json:"unit"`
	Range     string              `json:"range"`
}

// handleNamespaceMetrics returns aggregate metrics for a namespace.
func (h Routes) handleNamespaceMetrics(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := h.Capture(r.Context())
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	namespace := chi.URLParam(r, "namespace")
	if !canRead(r, "", "pods", namespace, "list") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	category := prom.MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = prom.CategoryCPU
	}

	if prom.BuildNamespaceQuery(namespace, category) == "" {
		writeError(w, http.StatusBadRequest, "unsupported category for namespace: "+string(category))
		return
	}

	resp, err := namespaceChart(r.Context(), productionChartDeps(r.Context(), client), namespace, category, r.URL.Query().Get("range"))
	if client.writeChartFailure(w, r.Context(), err) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func namespaceChart(ctx context.Context, d chartDeps, namespace string, category prom.MetricCategory, rangeStr string) (NamespaceMetricsResponse, error) {
	chart, err := d.readChart(ctx, "Namespace", namespace, "", category, rangeStr, nil)
	return NamespaceMetricsResponse{chartMeasurement: chart, Namespace: namespace, Category: category, Unit: prom.CategoryUnit(category), Range: rangeStr}, err
}

// ClusterMetricsResponse is the response shape for cluster-level metrics.
type ClusterMetricsResponse struct {
	chartMeasurement
	Category prom.MetricCategory `json:"category"`
	Unit     string              `json:"unit"`
	Range    string              `json:"range"`
}

// handleClusterMetrics returns aggregate metrics for the entire cluster.
func (h Routes) handleClusterMetrics(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := h.Capture(r.Context())
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	if !canReadClusterWideMetrics(r) {
		writeError(w, http.StatusForbidden, ClusterWideMetricsDeniedMessage)
		return
	}

	category := prom.MetricCategory(r.URL.Query().Get("category"))
	if category == "" {
		category = prom.CategoryCPU
	}

	if prom.BuildClusterQuery(category) == "" {
		writeError(w, http.StatusBadRequest, "unsupported category for cluster: "+string(category))
		return
	}

	resp, err := clusterChart(r.Context(), productionChartDeps(r.Context(), client), category, r.URL.Query().Get("range"))
	if client.writeChartFailure(w, r.Context(), err) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func clusterChart(ctx context.Context, d chartDeps, category prom.MetricCategory, rangeStr string) (ClusterMetricsResponse, error) {
	chart, err := d.readChart(ctx, "Cluster", "", "", category, rangeStr, nil)
	return ClusterMetricsResponse{chartMeasurement: chart, Category: category, Unit: prom.CategoryUnit(category), Range: rangeStr}, err
}

// RawQueryResponse is the response shape for raw PromQL. It carries the
// same resultType/series pair as prom.QueryResult plus the bounding fields
// the MCP tool emits: an oversized payload comes back as truncated with a
// cardinality summary instead of a silently cut series list.
type RawQueryResponse struct {
	ResultType string          `json:"resultType"`
	Series     []prom.Series   `json:"series"`
	Truncated  bool            `json:"truncated,omitempty"`
	Summary    json.RawMessage `json:"summary,omitempty"`
}

// handleRawQuery proxies a raw PromQL query to Prometheus.
// Query params: query (PromQL), range (time range), type (instant|range)
func (h Routes) handleRawQuery(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := h.Capture(r.Context())
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}
	if !canReadClusterWideMetrics(r) {
		writeError(w, http.StatusForbidden, ClusterWideMetricsDeniedMessage)
		return
	}

	query := r.URL.Query().Get("query")
	if query == "" {
		writeError(w, http.StatusBadRequest, "query parameter is required")
		return
	}

	queryType := r.URL.Query().Get("type")
	if queryType == "instant" {
		result, err := client.Query(r.Context(), query)
		if err != nil {
			if portforward.IsMemberClosed(r.Context(), err) {
				writeError(w, http.StatusServiceUnavailable, ConnectionClosedMessage)
				return
			}
			if msg, ok := QueryRefusal(err); ok {
				writeError(w, http.StatusBadRequest, msg)
				return
			}
			log.Printf("[prometheus] Raw instant query failed: %v", err)
			errorlog.Record("prometheus", "error", "raw instant query failed: %v", err)
			writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, boundRawResult(result, query, false))
		return
	}

	// Default to range query
	rangeStr := r.URL.Query().Get("range")
	start, end, step := parseTimeRange(rangeStr)

	result, err := client.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		if portforward.IsMemberClosed(r.Context(), err) {
			writeError(w, http.StatusServiceUnavailable, ConnectionClosedMessage)
			return
		}
		if msg, ok := QueryRefusal(err); ok {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		log.Printf("[prometheus] Raw range query failed: %v", err)
		errorlog.Record("prometheus", "error", "raw range query failed: %v", err)
		writeError(w, http.StatusBadGateway, "Prometheus query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, boundRawResult(result, query, true))
}

// QueryRefusal reports whether err is a query refused as bad input (by
// OpenObserve, Radar Hub's relay or the scoping in front of OpenObserve), and
// the message to show the caller. A refusal is the caller's error, not a
// failed backend.
func QueryRefusal(err error) (string, bool) {
	if !errors.Is(err, prom.ErrQueryRefused) {
		return "", false
	}
	msg := strings.TrimPrefix(err.Error(), "openobserve: ")
	if msg == "" {
		return "The query was refused", true
	}
	return strings.ToUpper(msg[:1]) + msg[1:], true
}

func boundRawResult(result *prom.QueryResult, query string, isRange bool) RawQueryResponse {
	resp := RawQueryResponse{ResultType: result.ResultType, Series: []prom.Series{}}
	if len(result.Series) == 0 {
		return resp
	}
	seriesBytes, err := json.Marshal(result.Series)
	if err != nil || len(seriesBytes) > MaxResponseBytes() {
		resp.Truncated = true
		resp.Summary = SummarizeLargeResult(result, query, isRange)
		return resp
	}
	resp.Series = result.Series
	return resp
}

type HPAMetricsResponse struct {
	Namespace    string            `json:"namespace"`
	Name         string            `json:"name"`
	Range        string            `json:"range"`
	Current      *prom.QueryResult `json:"current"`
	Desired      *prom.QueryResult `json:"desired"`
	ClusterScope *ClusterScope     `json:"clusterScope,omitempty"`
	Partial      bool              `json:"partial,omitempty"`
}

// handleHPAMetrics returns the current and desired replica series for one
// HPA. Curated so a caller who can read the HPA gets its chart without the
// cluster-wide grant raw PromQL requires.
// Query params: range (10m|30m|1h|...|14d, default: 1h)
func (h Routes) handleHPAMetrics(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := h.Capture(r.Context())
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if !canRead(r, "autoscaling", "horizontalpodautoscalers", namespace, "get") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	resp, err := hpaChart(r.Context(), productionChartDeps(r.Context(), client), namespace, name, r.URL.Query().Get("range"))
	if client.writeChartFailure(w, r.Context(), err) {
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func hpaChart(ctx context.Context, d chartDeps, namespace, name, rangeStr string) (HPAMetricsResponse, error) {
	start, end, step := parseTimeRange(rangeStr)
	resp := HPAMetricsResponse{Namespace: namespace, Name: name, Range: rangeStr}
	if err := d.unconnected(ctx); err != nil {
		return resp, err
	}
	view, err := ResolveChartScope(ctx, d.resolve, hpaChartScopeRequest(namespace, name, chartRangeDuration(rangeStr), d.cache))
	if err != nil {
		return resp, err
	}
	resp.ClusterScope = ClusterScopeOf(view)
	if view.Withheld {
		resp.Current, resp.Desired = emptyChartResult(), emptyChartResult()
		return resp, nil
	}
	currentQuery, desiredQuery := prom.BuildHPAReplicasQueriesFiltered(namespace, name, view.Filter)

	current, err := d.q.QueryRange(ctx, currentQuery, start, end, step)
	if err != nil {
		log.Printf("[prometheus] HPA current replicas query failed for %q/%q: %v", namespace, name, err)
		errorlog.Record("prometheus", "error", "hpa current replicas query failed for %q/%q: %v", namespace, name, err)
		return resp, errChartQuery{err}
	}
	desired, err := d.q.QueryRange(ctx, desiredQuery, start, end, step)
	if err != nil {
		log.Printf("[prometheus] HPA desired replicas query failed for %q/%q: %v", namespace, name, err)
		errorlog.Record("prometheus", "error", "hpa desired replicas query failed for %q/%q: %v", namespace, name, err)
		return resp, errChartQuery{err}
	}
	resp.Current, resp.Desired = current, desired
	resp.Partial = incompleteResult(current) || incompleteResult(desired)
	return resp, nil
}

const criDockerHint = "This pod's node uses the Docker container runtime (cri-docker), which is known to cause missing pod and namespace labels in cAdvisor metrics. " +
	"Verify that your Prometheus scrape config produces the standard 'pod' and 'namespace' labels on container_cpu_usage_seconds_total."

// detectCRIDockerHint returns a diagnostic hint when the resource runs on a
// node using cri-docker, which is known to cause missing cAdvisor labels.
// For Pods it checks the specific node; for workloads it checks all nodes
// running pods that match the workload name prefix.
func detectCRIDockerHint(cache *k8s.ResourceCache, kind, namespace, name string) string {
	// Node metrics use node-exporter, not cAdvisor — cri-docker is irrelevant.
	if strings.EqualFold(kind, "Node") {
		return ""
	}

	if cache == nil || cache.Nodes() == nil {
		return ""
	}

	// Collect the node names to check.
	var nodeNames []string
	if strings.EqualFold(kind, "Pod") {
		// For a specific pod, check only its assigned node.
		if cache.Pods() != nil {
			pod, err := cache.Pods().Pods(namespace).Get(name)
			if err == nil && pod.Spec.NodeName != "" {
				nodeNames = append(nodeNames, pod.Spec.NodeName)
			}
		}
	} else {
		// For workloads, find pods matching the name prefix (e.g. "myapp-" for Deployment "myapp").
		if cache.Pods() != nil {
			pods, err := cache.Pods().Pods(namespace).List(labels.Everything())
			if err == nil {
				prefix := name + "-"
				for _, pod := range pods {
					if strings.HasPrefix(pod.Name, prefix) && pod.Spec.NodeName != "" {
						nodeNames = append(nodeNames, pod.Spec.NodeName)
					}
				}
			}
		}
	}

	// If we couldn't resolve any nodes (pod not scheduled yet, etc.), fall back
	// to checking all cluster nodes.
	if len(nodeNames) == 0 {
		allNodes, err := cache.Nodes().List(labels.Everything())
		if err != nil {
			return ""
		}
		return anyNodeUsesDocker(allNodes)
	}

	// Check specific nodes.
	for _, nodeName := range nodeNames {
		node, err := cache.Nodes().Get(nodeName)
		if err != nil {
			continue
		}
		if strings.HasPrefix(node.Status.NodeInfo.ContainerRuntimeVersion, "docker://") {
			return criDockerHint
		}
	}
	return ""
}

// anyNodeUsesDocker returns the cri-docker hint if any node in the list uses
// the Docker container runtime, empty string otherwise.
func anyNodeUsesDocker(nodes []*corev1.Node) string {
	for _, node := range nodes {
		if strings.HasPrefix(node.Status.NodeInfo.ContainerRuntimeVersion, "docker://") {
			return criDockerHint
		}
	}
	return ""
}
