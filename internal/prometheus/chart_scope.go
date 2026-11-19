package prometheus

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// The chart routes and the diagnose vitals read a store that may hold several
// clusters. Each resolves a view scope over the metrics its query reads and
// either narrows every selector to this cluster or answers with no series and
// the reason.

const (
	chartImplicitLookback = 5 * time.Minute
	chartRateWindow       = 5 * time.Minute
	// Restart charts read changes()/increase() over an hour.
	chartRestartsWindow = time.Hour
)

// ChartScopeResolver decides a view scope; production wraps
// Client.ResolveViewScope with the resource cache.
type ChartScopeResolver func(ctx context.Context, req ViewScopeRequest) (ViewScope, error)

// ResolverFor is the production resolver.
func ResolverFor(client *Client, cache *k8s.ResourceCache) ChartScopeResolver {
	return func(ctx context.Context, req ViewScopeRequest) (ViewScope, error) {
		return client.ResolveViewScope(ctx, req, cache)
	}
}

// ChartScopeSpan is what a chart query reads: the range, the query's own
// window, and the store's implicit lookback for plain selectors.
func ChartScopeSpan(chartRange time.Duration, category prom.MetricCategory) time.Duration {
	window := chartRateWindow
	if category == prom.CategoryRestarts {
		window = chartRestartsWindow
	}
	return chartRange + window + chartImplicitLookback
}

func chartMetrics(kind string, category prom.MetricCategory, subject, anchorLabel, anchorNamespace string, anchors []string) []ViewMetric {
	var out []ViewMetric
	for _, name := range prom.CategoryMetrics(kind, category) {
		out = append(out, ViewMetric{Name: name, Subject: subject, AnchorLabel: anchorLabel, AnchorValues: anchors, AnchorNamespace: anchorNamespace})
	}
	return out
}

// clusterChartScopeRequest checks the overview's metrics across the whole
// store. Its anchors are the live Pods of the namespace the Pod-UID proof
// anchors on, a stable sample of this cluster's Pods that stays cheap on a
// large cluster. The anchor check stays in that namespace, so another
// cluster's same-named Pod elsewhere can't vouch for this cluster.
func clusterChartScopeRequest(category prom.MetricCategory, chartRange time.Duration, cache *k8s.ResourceCache) ViewScopeRequest {
	namespace := trafficAnchorNamespace(cache)
	anchors := NamespacePodAnchors(cache, namespace)
	return ViewScopeRequest{
		View:    "cluster_chart",
		Metrics: chartMetrics("", category, "", "pod", namespace, anchors),
		Span:    ChartScopeSpan(chartRange, category),
	}
}

func namespaceChartScopeRequest(namespace string, category prom.MetricCategory, chartRange time.Duration, cache *k8s.ResourceCache) ViewScopeRequest {
	return ViewScopeRequest{
		View:      "namespace_chart",
		Metrics:   chartMetrics("", category, "namespace="+strconv.Quote(namespace), "pod", namespace, NamespacePodAnchors(cache, namespace)),
		Span:      ChartScopeSpan(chartRange, category),
		Namespace: namespace,
	}
}

// podChartScopeRequest anchors on the Pod itself when it is live here.
func podChartScopeRequest(namespace, pod string, category prom.MetricCategory, chartRange time.Duration, cache *k8s.ResourceCache) ViewScopeRequest {
	var anchors []string
	if livePod(cache, namespace, pod) {
		anchors = []string{pod}
	}
	return ViewScopeRequest{
		View:      "pod_chart",
		Metrics:   chartMetrics("Pod", category, "namespace="+strconv.Quote(namespace)+",pod="+strconv.Quote(pod), "pod", namespace, anchors),
		Span:      ChartScopeSpan(chartRange, category),
		Namespace: namespace,
	}
}

// PodSetScopeRequest checks a workload's resolved Pods, anchored on them.
// span is what the caller's queries read.
func PodSetScopeRequest(view string, sel prom.PodSelection, categories []prom.MetricCategory, span time.Duration) ViewScopeRequest {
	subject := "namespace=" + strconv.Quote(sel.Namespace) + "," + labelRegexMatcher("pod", sel.Pods)
	anchors := BoundViewAnchors(sel.Pods)
	req := ViewScopeRequest{View: view, Span: span, Namespace: sel.Namespace}
	seen := map[string]bool{}
	for _, category := range categories {
		for _, m := range chartMetrics("", category, subject, "pod", sel.Namespace, anchors) {
			if !seen[m.Name] {
				seen[m.Name] = true
				req.Metrics = append(req.Metrics, m)
			}
		}
	}
	return req
}

// nodeChartScopeRequest narrows node-exporter's series the way the chart
// query does: instance holding the node's name, with an optional port. The
// anchor is the same match on the node's name when the node is live here, so
// the check finds exactly the series the chart would draw. A store whose
// instance holds the node's IP gives the chart no series and the check none
// either: absent, not withheld.
func nodeChartScopeRequest(node string, category prom.MetricCategory, chartRange time.Duration, cache *k8s.ResourceCache) ViewScopeRequest {
	subject := "instance=~" + strconv.Quote(regexp.QuoteMeta(node)+"(:[0-9]+)?")
	var anchors []string
	if liveNode(cache, node) {
		anchors = []string{node}
	}
	metrics := chartMetrics("Node", category, subject, "instance", "", anchors)
	for i := range metrics {
		metrics[i].AnchorOptionalPort = true
	}
	return ViewScopeRequest{View: "node_chart", Metrics: metrics, Span: ChartScopeSpan(chartRange, category)}
}

// hpaChartScopeRequest anchors on the HPA's own name: the weakest anchor,
// since another cluster's same-named HPA in a same-named namespace matches it.
// Joined: the chart plots current against desired replicas, so both must come
// from one cluster.
func hpaChartScopeRequest(namespace, name string, chartRange time.Duration, cache *k8s.ResourceCache) ViewScopeRequest {
	subject := "namespace=" + strconv.Quote(namespace) + ",horizontalpodautoscaler=" + strconv.Quote(name)
	var anchors []string
	if liveHPA(cache, namespace, name) {
		anchors = []string{name}
	}
	req := ViewScopeRequest{View: "hpa_chart", Joined: true, Span: chartRange + chartImplicitLookback, Namespace: namespace}
	for _, metric := range []string{prom.MetricHPACurrentReplicas, prom.MetricHPADesiredReplicas} {
		req.Metrics = append(req.Metrics, ViewMetric{Name: metric, Subject: subject, AnchorLabel: "horizontalpodautoscaler", AnchorValues: anchors, AnchorNamespace: namespace})
	}
	return req
}

func livePod(cache *k8s.ResourceCache, namespace, name string) bool {
	if cache == nil || cache.Pods() == nil {
		return false
	}
	_, err := cache.Pods().Pods(namespace).Get(name)
	return err == nil
}

func liveNode(cache *k8s.ResourceCache, name string) bool {
	if cache == nil || cache.Nodes() == nil {
		return false
	}
	_, err := cache.Nodes().Get(name)
	return err == nil
}

func liveHPA(cache *k8s.ResourceCache, namespace, name string) bool {
	if cache == nil || cache.HorizontalPodAutoscalers() == nil {
		return false
	}
	_, err := cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
	return err == nil
}

// ResolveChartScope runs resolve. An error is returned only when the caller's
// context ended; a failed check is the unknown scope, which withholds.
func ResolveChartScope(ctx context.Context, resolve ChartScopeResolver, req ViewScopeRequest) (ViewScope, error) {
	scope, err := resolve(ctx, req)
	if err != nil && ctx.Err() != nil {
		return scope, ctx.Err()
	}
	if err != nil && scope.State == "" {
		return unknownViewScope(), nil
	}
	return scope, nil
}

// ChartAnswer is one scoped chart query's outcome.
type ChartAnswer struct {
	Result  *prom.QueryResult
	Query   string
	Partial bool
}

// emptyChartResult is the answer for a withheld chart.
func emptyChartResult() *prom.QueryResult {
	return &prom.QueryResult{ResultType: "matrix", Series: []prom.Series{}}
}

// QueryScopedChart runs one chart query with every selector narrowed by
// scope, re-running it without the container filter when it comes back
// empty. build(filterContainer, filter) builds the query; scope must not be
// withheld.
func QueryScopedChart(ctx context.Context, q RangeQuerier, scope ViewScope, category prom.MetricCategory, build func(bool, prom.ClusterFilter) string, start, end time.Time, step time.Duration, logPrefix string) (ChartAnswer, error) {
	filter := prom.ClusterFilter(scope.Filter)
	query := build(true, filter)
	result, err := q.QueryRange(ctx, query, start, end, step)
	if err != nil {
		return ChartAnswer{Query: query}, err
	}
	result, query, err = QueryWithContainerFilterFallback(ctx, q, result, query, category, start, end, step,
		func() string { return build(false, filter) }, logPrefix)
	if err != nil {
		return ChartAnswer{Query: query}, err
	}
	return ChartAnswer{Result: result, Query: query, Partial: incompleteResult(result)}, nil
}

// incompleteResult reports a partial or warning answer: still drawn, but
// marked so the chart can say it may be missing samples.
func incompleteResult(res *prom.QueryResult) bool {
	return res != nil && (res.Annotations.Partial || len(res.Annotations.Warnings) > 0)
}

// ClusterScopeOf is the response's clusterScope: absent when no check ran.
func ClusterScopeOf(scope ViewScope) *ClusterScope {
	if scope.State == "" {
		return nil
	}
	return scope.Wire()
}
