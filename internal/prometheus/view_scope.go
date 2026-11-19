package prometheus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/apimachinery/pkg/labels"
)

// A view scope decides, for one view's metrics in a store that may hold
// several clusters, which matchers keep each metric to this cluster, or why
// the view can't be kept to it. Each metric is checked on its own: metrics
// from different scrape jobs often carry different cluster labels.

type ViewScopeState string

const (
	ViewScopeConfigured            ViewScopeState = "scoped_configured"
	ViewScopeSingleClusterAsserted ViewScopeState = "single_cluster_asserted"
	ViewScopeVerified              ViewScopeState = "scoped_verified"
	ViewScopeUnlabeled             ViewScopeState = "unlabeled"
	ViewScopeSeveralClusters       ViewScopeState = "several_clusters"
	ViewScopeLabelMissing          ViewScopeState = "label_missing"
	ViewScopeUnknown               ViewScopeState = "unknown"
)

const (
	viewScopeRemedy         = "Set --prometheus-cluster-label (Helm traffic.prometheusClusterLabels)."
	viewScopeSeveralNote    = "This metrics store holds several clusters and Radar can't tell which series are this one. " + viewScopeRemedy
	viewScopeUnknownNote    = "Radar couldn't check which cluster these metrics belong to; try again."
	viewScopeUnlabeledNote  = "Radar couldn't confirm these metrics belong to this exact cluster (no cluster label it could check)."
	viewScopeConfiguredNote = "Matched by the cluster labels an operator configured."
	viewScopeVerifiedNote   = "Matched by cluster labels confirmed against this cluster's Pods."
	viewScopeSingleNote     = "An operator declared this metrics store single-cluster."
	viewScopeMetricLabel    = "radar_metric"
	viewScopeAnchorLabel    = "radar_anchor"
	viewScopeAnchorLimit    = 50
	viewScopeCacheLimit     = 256
	viewScopeScopedTTL      = 5 * time.Minute
	viewScopeUnscopedTTL    = time.Minute
	viewScopeMinSpan        = 5 * time.Minute
	viewScopeLiveSlack      = 2 * time.Minute
	// The shared check outlives any one caller's deadline so a waiter with
	// time left is not failed by a leader that had little; each caller still
	// returns on its own cancellation.
	viewScopeFlightTimeout = 20 * time.Second
)

// ViewMetric is one metric a view reads. Subject narrows it to the view's
// subject (empty for cluster-wide); AnchorLabel is the label the metric is
// keyed by (pod, node, replicaset, persistentvolume, horizontalpodautoscaler)
// and AnchorValues are this cluster's live object names for it.
type ViewMetric struct {
	Name         string
	Subject      string
	AnchorLabel  string
	AnchorValues []string
}

// ViewScopeRequest describes one view's check. Span is what the view's
// queries actually read (range plus explicit lookbacks plus the store's
// implicit lookback); At is their evaluation time, zero for now. Joined views
// combine their metrics in one expression, so all must land on one cluster.
// Namespace anchors the Pod-UID proof; empty uses the traffic map's anchor.
type ViewScopeRequest struct {
	View      string
	Metrics   []ViewMetric
	Joined    bool
	Span      time.Duration
	At        time.Time
	Namespace string
}

// ViewScope is the decision. Filters maps each metric to the matchers to AND
// into its selectors ("" leaves it unfiltered); a withheld view has none.
// Labels are the cluster labels the decision checked, when there were any.
type ViewScope struct {
	State    ViewScopeState
	Withheld bool
	Labels   map[string]string
	Filters  map[string]string
	Note     string
}

// ClusterScope is a view scope as responses carry it.
type ClusterScope struct {
	State  ViewScopeState    `json:"state"`
	Labels map[string]string `json:"labels,omitempty"`
	Note   string            `json:"note"`
}

func (s ViewScope) Filter(metric string) string {
	return s.Filters[metric]
}

func (s ViewScope) Wire() *ClusterScope {
	return &ClusterScope{State: s.State, Labels: maps.Clone(s.Labels), Note: s.Note}
}

func (s ViewScope) clone() ViewScope {
	s.Labels = maps.Clone(s.Labels)
	s.Filters = maps.Clone(s.Filters)
	return s
}

func unknownViewScope() ViewScope {
	return ViewScope{State: ViewScopeUnknown, Withheld: true, Note: viewScopeUnknownNote}
}

type viewScopeEntry struct {
	scope   ViewScope
	expires time.Time
}

type instantQuerier interface {
	QueryAt(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error)
}

type strictInstantQuerier struct{ inner instantQuerier }

func (s strictInstantQuerier) QueryAt(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error) {
	return requireComplete(s.inner.QueryAt(ctx, query, at))
}

var viewScopeLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func normalizeViewScopeRequest(req ViewScopeRequest) (ViewScopeRequest, error) {
	if len(req.Metrics) == 0 {
		return req, errors.New("view scope: no metrics")
	}
	seen := map[string]bool{}
	metrics := make([]ViewMetric, len(req.Metrics))
	for i, m := range req.Metrics {
		if m.Name == "" || seen[m.Name] {
			return req, fmt.Errorf("view scope: metric %q missing or repeated", m.Name)
		}
		if m.AnchorLabel != "" && (!viewScopeLabelName.MatchString(m.AnchorLabel) || strings.HasPrefix(m.AnchorLabel, "__")) {
			return req, fmt.Errorf("view scope: invalid anchor label %q", m.AnchorLabel)
		}
		seen[m.Name] = true
		m.AnchorValues = BoundViewAnchors(m.AnchorValues)
		metrics[i] = m
	}
	req.Metrics = metrics
	req.Span = viewScopeSpanBucket(req.Span)
	return req, nil
}

// viewScopeSpanBucket rounds the span up, so nearby spans share a decision
// and the check never reads less than the view does.
func viewScopeSpanBucket(span time.Duration) time.Duration {
	span = max(span, viewScopeMinSpan)
	bucket := time.Hour
	switch {
	case span <= time.Hour:
		bucket = time.Minute
	case span <= 6*time.Hour:
		bucket = 5 * time.Minute
	}
	return (span + bucket - 1) / bucket * bucket
}

func promSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// ResolveViewScope decides each of a view's metrics' cluster filter. A
// returned error means the check could not run (the caller's context ended
// or the request is invalid); the scope is then the unknown one. Every other
// failure is the unknown state.
func (c *Client) ResolveViewScope(ctx context.Context, req ViewScopeRequest, cache *k8s.ResourceCache) (ViewScope, error) {
	if c == nil {
		return unknownViewScope(), errors.New("Prometheus client not initialized")
	}
	req, err := normalizeViewScopeRequest(req)
	if err != nil {
		return unknownViewScope(), err
	}
	config, beyla, configured := c.workloadMetricsConfig()
	if configured && config.SingleCluster {
		filters := make(map[string]string, len(req.Metrics))
		for _, m := range req.Metrics {
			filters[m.Name] = ""
		}
		return ViewScope{State: ViewScopeSingleClusterAsserted, Filters: filters, Note: viewScopeSingleNote}, nil
	}
	now := time.Now()
	at, live := req.At, false
	if at.IsZero() || (at.Sub(now) < viewScopeLiveSlack && now.Sub(at) < viewScopeLiveSlack) {
		live = true
		if at.IsZero() {
			at = now
		}
	}
	c.mu.RLock()
	generation, epoch := c.discoveryGen, c.connectionEpoch
	c.mu.RUnlock()
	key := viewScopeKey(generation, epoch, config, beyla, configured, req, live, at)
	if scope, ok := c.cachedViewScope(key, now); ok {
		return scope, nil
	}
	ch := c.viewScopeSF.DoChan(key, func() (any, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), viewScopeFlightTimeout)
		defer cancel()
		scope, err := c.decideViewScopeFor(flightCtx, req, at, config, configured, cache)
		if err != nil {
			if flightCtx.Err() == nil {
				errorlog.Record("prometheus", "warning", "cluster scope check for %s failed: %v", req.View, err)
			}
			return scope, nil
		}
		if !c.viewScopeConnectionCurrent(generation, epoch) {
			return unknownViewScope(), nil
		}
		if ttl := viewScopeTTL(scope); ttl > 0 {
			c.storeViewScope(key, scope, ttl, generation, epoch)
		}
		return scope, nil
	})
	select {
	case <-ctx.Done():
		return unknownViewScope(), ctx.Err()
	case result := <-ch:
		if err := ctx.Err(); err != nil {
			return unknownViewScope(), err
		}
		return result.Val.(ViewScope).clone(), nil
	}
}

func (c *Client) decideViewScopeFor(ctx context.Context, req ViewScopeRequest, at time.Time, config prom.WorkloadMetricsScope, configured bool, cache *k8s.ResourceCache) (ViewScope, error) {
	clusterLabels := config.ClusterLabels
	if !configured {
		namespace := req.Namespace
		if namespace == "" {
			namespace = trafficAnchorNamespace(cache)
		}
		proof, err := c.historicalClusterProof(ctx, PodScope{Namespace: namespace}, cache)
		if err != nil {
			return unknownViewScope(), err
		}
		clusterLabels = proof.scope.ClusterLabels
	}
	return decideViewScope(ctx, c, req, at, clusterLabels, configured)
}

func viewScopeTTL(scope ViewScope) time.Duration {
	switch {
	case scope.State == ViewScopeUnknown:
		return 0
	case scope.Withheld:
		return viewScopeUnscopedTTL
	}
	for _, filter := range scope.Filters {
		if filter == "" {
			return viewScopeUnscopedTTL
		}
	}
	return viewScopeScopedTTL
}

func viewScopeKey(generation, epoch uint64, config prom.WorkloadMetricsScope, beyla string, configured bool, req ViewScopeRequest, live bool, at time.Time) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			_, _ = h.Write([]byte(strconv.Quote(p)))
		}
		_, _ = h.Write([]byte{'\n'})
	}
	labelKeys := slices.Sorted(maps.Keys(config.ClusterLabels))
	write("config", strconv.FormatBool(configured), strconv.FormatBool(config.SingleCluster), beyla)
	for _, k := range labelKeys {
		write("label", k, config.ClusterLabels[k])
	}
	write("view", req.View, strconv.FormatBool(req.Joined), req.Namespace, promSeconds(req.Span))
	for _, m := range req.Metrics {
		write(append([]string{"metric", m.Name, m.Subject, m.AnchorLabel}, m.AnchorValues...)...)
	}
	when := "live"
	if !live {
		when = strconv.FormatInt(at.UnixNano(), 10)
	}
	return fmt.Sprintf("%d#%d#%s#%s", generation, epoch, when, hex.EncodeToString(h.Sum(nil)))
}

func (c *Client) cachedViewScope(key string, now time.Time) (ViewScope, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.viewScopes[key]
	if !ok || c.retired || !now.Before(entry.expires) {
		return ViewScope{}, false
	}
	return entry.scope.clone(), true
}

func (c *Client) viewScopeConnectionCurrent(generation, epoch uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.retired && c.discoveryGen == generation && c.connectionEpoch == epoch
}

func (c *Client) storeViewScope(key string, scope ViewScope, ttl time.Duration, generation, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired || c.discoveryGen != generation || c.connectionEpoch != epoch {
		return
	}
	now := time.Now()
	if c.viewScopes == nil {
		c.viewScopes = map[string]viewScopeEntry{}
	}
	if _, exists := c.viewScopes[key]; !exists && len(c.viewScopes) >= viewScopeCacheLimit {
		oldest := ""
		for k, entry := range c.viewScopes {
			if !now.Before(entry.expires) {
				delete(c.viewScopes, k)
				continue
			}
			if oldest == "" || entry.expires.Before(c.viewScopes[oldest].expires) {
				oldest = k
			}
		}
		if len(c.viewScopes) >= viewScopeCacheLimit {
			delete(c.viewScopes, oldest)
		}
	}
	c.viewScopes[key] = viewScopeEntry{scope: scope.clone(), expires: now.Add(ttl)}
}

// decideViewScope runs the checks for one normalized request. clusterLabels
// are the operator's labels (configured) or the labels proven on this
// cluster's Pods; empty means neither exists.
func decideViewScope(ctx context.Context, q instantQuerier, req ViewScopeRequest, at time.Time, clusterLabels map[string]string, configured bool) (ViewScope, error) {
	q = strictInstantQuerier{q}
	if len(clusterLabels) > 0 {
		return decideLabeledViewScope(ctx, q, req, at, clusterLabels, configured)
	}
	return decideUnlabeledViewScope(ctx, q, req, at)
}

func decideLabeledViewScope(ctx context.Context, q instantQuerier, req ViewScopeRequest, at time.Time, clusterLabels map[string]string, configured bool) (ViewScope, error) {
	matchers, err := prom.WorkloadMetricsScope{ClusterLabels: clusterLabels}.Matchers()
	if err != nil {
		return unknownViewScope(), err
	}
	keys := slices.Sorted(maps.Keys(clusterLabels))
	res, err := q.QueryAt(ctx, viewReadbackQuery(req.Metrics, keys, matchers, req.Span), at)
	if err != nil {
		return unknownViewScope(), err
	}
	readback, _, err := groupViewScopeRows(req.Metrics, res)
	if err != nil {
		return unknownViewScope(), err
	}
	filters := map[string]string{}
	var missing []string
	var pending []ViewMetric
	for _, m := range req.Metrics {
		rows := readback[m.Name]
		if len(rows) == 0 {
			pending = append(pending, m)
			continue
		}
		ignored := false
		for _, row := range rows {
			for _, k := range keys {
				ignored = ignored || row[k] != clusterLabels[k]
			}
		}
		if ignored {
			missing = append(missing, m.Name)
			continue
		}
		filters[m.Name] = matchers
	}
	if len(pending) > 0 {
		identityKeys := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(partitionLabels), keys...))))
		res, err := q.QueryAt(ctx, viewIdentityQuery(pending, identityKeys, req.Span, true), at)
		if err != nil {
			return unknownViewScope(), err
		}
		identities, anchored, err := groupViewScopeRows(pending, res)
		if err != nil {
			return unknownViewScope(), err
		}
		for _, m := range pending {
			ids := identities[m.Name]
			switch {
			case len(ids) == 0:
				filters[m.Name] = matchers
			case len(ids) == 1 && anchored[m.Name] && !conflictsWithLabels(ids[0], clusterLabels):
				filters[m.Name] = ""
			default:
				missing = append(missing, m.Name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return ViewScope{
			State: ViewScopeLabelMissing, Withheld: true, Labels: maps.Clone(clusterLabels),
			Note: fmt.Sprintf("None of the %s series carry this cluster's label %s (they have no cluster label, or another cluster's value), so Radar can't tell which of them are this cluster's. Add the label to that metric's scrape, or set --prometheus-cluster-label (Helm traffic.prometheusClusterLabels) to a label every metric carries.", strings.Join(missing, ", "), matchers),
		}, nil
	}
	scope := ViewScope{State: ViewScopeVerified, Labels: maps.Clone(clusterLabels), Filters: filters, Note: viewScopeVerifiedNote}
	if configured {
		scope.State, scope.Note = ViewScopeConfigured, viewScopeConfiguredNote
	}
	for _, filter := range filters {
		if filter == "" {
			scope.State, scope.Note = ViewScopeUnlabeled, viewScopeUnlabeledNote
		}
	}
	return scope, nil
}

// conflictsWithLabels reports whether an identity names another value for
// one of this cluster's labels: such series belong to another cluster even
// when their object names coincide with ours.
func conflictsWithLabels(identity, clusterLabels map[string]string) bool {
	for k, v := range clusterLabels {
		if got := identity[k]; got != "" && got != v {
			return true
		}
	}
	return false
}

func decideUnlabeledViewScope(ctx context.Context, q instantQuerier, req ViewScopeRequest, at time.Time) (ViewScope, error) {
	res, err := q.QueryAt(ctx, viewIdentityQuery(req.Metrics, partitionLabels, req.Span, false), at)
	if err != nil {
		return unknownViewScope(), err
	}
	identities, _, err := groupViewScopeRows(req.Metrics, res)
	if err != nil {
		return unknownViewScope(), err
	}
	several := ViewScope{State: ViewScopeSeveralClusters, Withheld: true, Note: viewScopeSeveralNote}
	filters := map[string]string{}
	joined := ""
	for _, m := range req.Metrics {
		ids := identities[m.Name]
		if len(ids) > 1 {
			return several, nil
		}
		filters[m.Name] = ""
		if len(ids) == 0 || !req.Joined {
			continue
		}
		key := identityKey(ids[0], partitionLabels)
		if key == identityKey(nil, partitionLabels) {
			continue
		}
		if joined != "" && joined != key {
			return several, nil
		}
		joined = key
	}
	return ViewScope{State: ViewScopeUnlabeled, Filters: filters, Note: viewScopeUnlabeledNote}, nil
}

func identityKey(identity map[string]string, keys []string) string {
	values := make([]string, len(keys))
	for i, k := range keys {
		values[i] = strconv.Quote(identity[k])
	}
	return strings.Join(values, ",")
}

// viewReadbackQuery counts each metric's filtered series by the filter's own
// labels, so a store that ignores a matcher shows itself in the values it
// returns. Range functions drop metric names, hence the label_replace.
func viewReadbackQuery(metrics []ViewMetric, keys []string, matchers string, span time.Duration) string {
	parts := make([]string, len(metrics))
	for i, m := range metrics {
		parts[i] = viewTagged(viewScopeMetricLabel, m.Name, "count by ("+strings.Join(keys, ",")+") (count_over_time("+vectorSelector(m.Name, withScope(m.Subject, matchers))+"["+promSeconds(span)+"]))")
	}
	return strings.Join(parts, " or ")
}

// viewIdentityQuery reads up to two cluster identities per metric and, with
// anchors, whether the metric holds any of this cluster's live objects.
func viewIdentityQuery(metrics []ViewMetric, keys []string, span time.Duration, anchors bool) string {
	var parts []string
	window := "[" + promSeconds(span) + "]"
	for _, m := range metrics {
		parts = append(parts, viewTagged(viewScopeMetricLabel, m.Name, "topk(2, count by ("+strings.Join(keys, ",")+") (count_over_time("+vectorSelector(m.Name, m.Subject)+window+")))"))
		if anchors && m.AnchorLabel != "" && len(m.AnchorValues) > 0 {
			names := make([]string, len(m.AnchorValues))
			for i, v := range m.AnchorValues {
				names[i] = regexp.QuoteMeta(v)
			}
			anchor := m.AnchorLabel + "=~" + strconv.Quote(strings.Join(names, "|"))
			parts = append(parts, viewTagged(viewScopeAnchorLabel, m.Name, "count(count_over_time("+vectorSelector(m.Name, withScope(m.Subject, anchor))+window+"))"))
		}
	}
	return strings.Join(parts, " or ")
}

func viewTagged(label, metric, expr string) string {
	return "label_replace(" + expr + ", " + strconv.Quote(label) + ", " + strconv.Quote(metric) + ", \"\", \"\")"
}

// groupViewScopeRows splits a batched answer by metric: rows tagged with the
// metric label, and the metrics an anchor row came back for. A row for no
// requested metric means the store didn't evaluate the query as written.
func groupViewScopeRows(metrics []ViewMetric, res *prom.QueryResult) (map[string][]map[string]string, map[string]bool, error) {
	requested := map[string]bool{}
	for _, m := range metrics {
		requested[m.Name] = true
	}
	rows := map[string][]map[string]string{}
	anchored := map[string]bool{}
	if res == nil {
		return rows, anchored, nil
	}
	for _, series := range res.Series {
		if name := series.Labels[viewScopeAnchorLabel]; name != "" && requested[name] {
			anchored[name] = true
			continue
		}
		name := series.Labels[viewScopeMetricLabel]
		if !requested[name] {
			return nil, nil, fmt.Errorf("unexpected series in cluster scope answer: %v", series.Labels)
		}
		rows[name] = append(rows[name], series.Labels)
	}
	return rows, anchored, nil
}

// BoundViewAnchors sorts and deduplicates object names and keeps the first
// few, which is enough to find this cluster's objects in a metric.
func BoundViewAnchors(names []string) []string {
	out := slices.Compact(slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(names), func(s string) bool { return s == "" }))))
	if len(out) > viewScopeAnchorLimit {
		out = out[:viewScopeAnchorLimit]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NamespacePodAnchors names live Pods of one namespace.
func NamespacePodAnchors(cache *k8s.ResourceCache, namespace string) []string {
	if cache == nil || cache.Pods() == nil {
		return nil
	}
	pods, err := cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, len(pods))
	for i, pod := range pods {
		names[i] = pod.Name
	}
	return BoundViewAnchors(names)
}

// NodeAnchors names live Nodes.
func NodeAnchors(cache *k8s.ResourceCache) []string {
	if cache == nil || cache.Nodes() == nil {
		return nil
	}
	nodes, err := cache.Nodes().List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, len(nodes))
	for i, node := range nodes {
		names[i] = node.Name
	}
	return BoundViewAnchors(names)
}

// NamespaceReplicaSetAnchors names live ReplicaSets of one namespace.
func NamespaceReplicaSetAnchors(cache *k8s.ResourceCache, namespace string) []string {
	if cache == nil || cache.ReplicaSets() == nil {
		return nil
	}
	sets, err := cache.ReplicaSets().ReplicaSets(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, len(sets))
	for i, rs := range sets {
		names[i] = rs.Name
	}
	return BoundViewAnchors(names)
}

// PersistentVolumeAnchors names live PersistentVolumes.
func PersistentVolumeAnchors(cache *k8s.ResourceCache) []string {
	if cache == nil || cache.PersistentVolumes() == nil {
		return nil
	}
	volumes, err := cache.PersistentVolumes().List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, len(volumes))
	for i, pv := range volumes {
		names[i] = pv.Name
	}
	return BoundViewAnchors(names)
}
