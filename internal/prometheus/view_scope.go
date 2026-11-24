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
// AnchorNamespace, when set, restricts the anchor check to that namespace, so
// another cluster's same-named object in a different namespace cannot vouch
// for this cluster's; set it for namespaced objects. AnchorNamespaceExported
// also accepts the namespace in exported_namespace (with namespace then
// naming the exporter's own), as a scrape that renamed a clashing namespace
// label leaves it. AnchorOptionalPort lets an anchor value carry a ":<port>"
// suffix, as node-exporter's instance label does when it holds the node's
// name. Foreign, when set, guards a view that sums the metric over a
// namespace or the cluster; see ForeignCheck.
type ViewMetric struct {
	Name                    string
	Subject                 string
	AnchorLabel             string
	AnchorValues            []string
	AnchorNamespace         string
	AnchorNamespaceExported bool
	AnchorOptionalPort      bool
	Foreign                 ForeignCheck
}

// ForeignCheck names the kube-state-metrics series that list the objects a
// metric is keyed by under each object's cluster labels. Anchors prove a
// metric without this cluster's label holds this cluster's objects, not that
// it holds only them: two clusters leaving the label off one scrape job read
// as one identity, and a view summing the metric would add both. With this
// cluster's labels known, such a metric is also checked for objects the
// reference lists under other clusters' labels and never under this
// cluster's. Per-object views need no such check, since their subject names
// this cluster's objects.
//
// Keys map each of the metric's key labels to the reference's label for the
// same thing. NamespaceExported reads the metric's namespace from
// exported_namespace when that is set, as a scrape that renamed a clashing
// namespace label leaves it. Namespace, when set, checks only the metric's
// objects in that namespace (under either spelling with NamespaceExported):
// a view of one namespace reads only its series, so another cluster's objects
// in other namespaces never reach its sums. Set it for a metric whose Subject
// can't select the namespace because the label naming it varies.
type ForeignCheck struct {
	Reference         string
	Keys              []ForeignKey
	NamespaceExported bool
	Namespace         string
}

// ForeignKey maps one of the metric's labels to the reference's.
type ForeignKey struct {
	Label     string
	Reference string
}

// PodForeignCheck checks a metric keyed by namespace and pod against
// kube_pod_info.
func PodForeignCheck(namespaceExported bool) ForeignCheck {
	return ForeignCheck{
		Reference:         "kube_pod_info",
		Keys:              []ForeignKey{{Label: "namespace", Reference: "namespace"}, {Label: "pod", Reference: "pod"}},
		NamespaceExported: namespaceExported,
	}
}

// NodeForeignCheck checks a metric naming the node in label against
// kube_node_info.
func NodeForeignCheck(label string) ForeignCheck {
	return ForeignCheck{Reference: "kube_node_info", Keys: []ForeignKey{{Label: label, Reference: "node"}}}
}

// PersistentVolumeForeignCheck checks a metric naming the PersistentVolume in
// label against kube_persistentvolume_info.
func PersistentVolumeForeignCheck(label string) ForeignCheck {
	return ForeignCheck{Reference: "kube_persistentvolume_info", Keys: []ForeignKey{{Label: label, Reference: "persistentvolume"}}}
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
// Labels identify the cluster checked. A configured scope includes the qualified
// labels pinned on measurement queries. A withheld scope reports the selector
// that failed verification.
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

// instantQuerier runs the scope probes: instant queries at a fixed time, sent
// in a POST body because a probe can outgrow a proxy's URL limit.
type instantQuerier interface {
	QueryAtPost(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error)
}

type strictInstantQuerier struct{ inner instantQuerier }

func (s strictInstantQuerier) QueryAtPost(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error) {
	return requireComplete(s.inner.QueryAtPost(ctx, query, at))
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
		if m.AnchorLabel == "" || len(m.AnchorValues) == 0 {
			m.AnchorNamespace = ""
		}
		if m.AnchorNamespace == "" {
			m.AnchorNamespaceExported = false
		}
		foreign, err := normalizeForeignCheck(m.Foreign)
		if err != nil {
			return req, fmt.Errorf("view scope: metric %q: %w", m.Name, err)
		}
		m.Foreign = foreign
		metrics[i] = m
	}
	req.Metrics = metrics
	req.Span = viewScopeSpanBucket(req.Span)
	return req, nil
}

var viewScopeMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

func validViewLabel(name string) bool {
	return viewScopeLabelName.MatchString(name) && !strings.HasPrefix(name, "__")
}

// normalizeForeignCheck validates the check and orders its keys. A target
// label that is also one of the metric's key labels is refused: renaming
// onto it would merge series the probe must keep apart.
func normalizeForeignCheck(f ForeignCheck) (ForeignCheck, error) {
	if f.Reference == "" {
		return ForeignCheck{}, nil
	}
	if !viewScopeMetricName.MatchString(f.Reference) || len(f.Keys) == 0 {
		return f, fmt.Errorf("invalid foreign check on %q", f.Reference)
	}
	labels, refs := map[string]bool{}, map[string]bool{}
	for _, k := range f.Keys {
		if !validViewLabel(k.Label) || !validViewLabel(k.Reference) || labels[k.Label] || refs[k.Reference] || k.Label == "exported_namespace" {
			return f, fmt.Errorf("invalid or repeated foreign check key %q→%q", k.Label, k.Reference)
		}
		labels[k.Label], refs[k.Reference] = true, true
	}
	for _, k := range f.Keys {
		if k.Label != k.Reference && labels[k.Reference] {
			return f, fmt.Errorf("foreign check key %q→%q renames onto another key label", k.Label, k.Reference)
		}
	}
	if f.NamespaceExported && !labels["namespace"] {
		return f, errors.New("foreign check reads exported_namespace but has no namespace key")
	}
	if f.Namespace != "" && !labels["namespace"] {
		return f, errors.New("foreign check keeps to a namespace but has no namespace key")
	}
	f.Keys = slices.Clone(f.Keys)
	slices.SortFunc(f.Keys, func(a, b ForeignKey) int { return strings.Compare(a.Label, b.Label) })
	return f, nil
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
		write(append([]string{"metric", m.Name, m.Subject, m.AnchorLabel, m.AnchorNamespace, strconv.FormatBool(m.AnchorNamespaceExported), strconv.FormatBool(m.AnchorOptionalPort)}, m.AnchorValues...)...)
		if f := m.Foreign; f.Reference != "" {
			foreign := []string{"foreign", f.Reference, strconv.FormatBool(f.NamespaceExported), f.Namespace}
			for _, k := range f.Keys {
				foreign = append(foreign, k.Label, k.Reference)
			}
			write(foreign...)
		}
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
	qualifiers := clusterQualifierLabels
	if configured && !slices.ContainsFunc(keys, func(k string) bool { return !slices.Contains(clusterQualifierLabels, k) }) {
		// An operator may use a project as the cluster boundary. Check visible cluster names within it.
		qualifiers = partitionLabels
	}
	readbackKeys := slices.Compact(slices.Sorted(slices.Values(slices.Concat(keys, qualifiers))))
	readback, _, err := runViewProbes(ctx, q, at, viewReadbackParts(req.Metrics, readbackKeys, matchers, req.Span))
	if err != nil {
		return unknownViewScope(), err
	}
	queryLabels := clusterLabels
	if configured {
		var coherent bool
		queryLabels, coherent = configuredReadbackLabels(readback, readbackKeys, clusterLabels)
		if !coherent {
			return ViewScope{State: ViewScopeLabelMissing, Withheld: true, Labels: maps.Clone(clusterLabels),
				Note: fmt.Sprintf("The configured selector %s matches inconsistent cluster, project or location labels across the measured series. Add exact labels for this cluster to --prometheus-cluster-label.", matchers)}, nil
		}
	}
	queryMatchers, err := prom.WorkloadMetricsScope{ClusterLabels: queryLabels}.Matchers()
	if err != nil {
		return unknownViewScope(), err
	}
	filters := map[string]string{}
	var missing, unfiltered []string
	var pending []ViewMetric
	for _, m := range req.Metrics {
		rows := readback[m.Name]
		if len(rows) == 0 {
			pending = append(pending, m)
			continue
		}
		ignored := false
		for _, row := range rows {
			for _, qualifier := range clusterQualifierLabels {
				if value := row[qualifier]; value != "" && value != queryLabels[qualifier] {
					return unverifiedClusterQualifier(m.Name, clusterLabels), nil
				}
			}
			for _, k := range keys {
				ignored = ignored || row[k] != clusterLabels[k]
			}
		}
		if ignored {
			missing = append(missing, m.Name)
			continue
		}
		filters[m.Name] = queryMatchers
	}
	if len(pending) > 0 {
		identityKeys := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(partitionLabels), keys...))))
		identities, marks, err := runViewProbes(ctx, q, at, viewIdentityParts(pending, identityKeys, req.Span, true))
		if err != nil {
			return unknownViewScope(), err
		}
		anchored := marks[viewScopeAnchorLabel]
		var unfilteredIDs []map[string]string
		for _, m := range pending {
			ids := identities[m.Name]
			switch {
			case len(ids) == 0:
				filters[m.Name] = queryMatchers
			case len(ids) == 1 && anchored[m.Name] && !hasClusterQualifier(queryLabels) && !hasClusterQualifier(ids[0]) && !conflictsWithLabels(ids[0], clusterLabels):
				filters[m.Name] = ""
				unfiltered = append(unfiltered, m.Name)
				unfilteredIDs = append(unfilteredIDs, ids[0])
			default:
				missing = append(missing, m.Name)
			}
		}
		// A joined view reading several metrics unfiltered combines their
		// series, so they must name one cluster between them, as without
		// labels; when they don't, none of them can be told to be this one.
		if req.Joined && !joinedIdentitiesAgree(unfilteredIDs, identityKeys) {
			missing = append(missing, unfiltered...)
		}
	}
	if len(missing) == 0 && len(unfiltered) > 0 {
		foreign, uncheckable, err := findForeignObjects(ctx, q, at, foreignChecks(req.Metrics, unfiltered), matchers, req.Span)
		if err != nil {
			return unknownViewScope(), err
		}
		if len(uncheckable) > 0 {
			return ViewScope{
				State: ViewScopeUnknown, Withheld: true, Labels: maps.Clone(clusterLabels),
				Note: fmt.Sprintf("Radar couldn't check that the %s series hold only this cluster's objects: kube-state-metrics' %s has no series with this cluster's label %s over this time range.", strings.Join(foreignCheckedNames(req.Metrics, unfiltered, uncheckable), ", "), strings.Join(uncheckable, ", "), matchers),
			}, nil
		}
		if len(foreign) > 0 {
			return ViewScope{
				State: ViewScopeLabelMissing, Withheld: true, Labels: maps.Clone(clusterLabels),
				Note: fmt.Sprintf("The %s series have no cluster label and include objects kube-state-metrics lists under another cluster, not under this cluster's label %s, so Radar can't tell which of them are this cluster's. Add the label to that metric's scrape, or set --prometheus-cluster-label (Helm traffic.prometheusClusterLabels) to a label every metric carries.", strings.Join(foreign, ", "), matchers),
			}, nil
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return ViewScope{
			State: ViewScopeLabelMissing, Withheld: true, Labels: maps.Clone(clusterLabels),
			Note: fmt.Sprintf("None of the %s series carry this cluster's label %s (they have no cluster label, or another cluster's value), so Radar can't tell which of them are this cluster's. Add the label to that metric's scrape, or set --prometheus-cluster-label (Helm traffic.prometheusClusterLabels) to a label every metric carries.", strings.Join(missing, ", "), matchers),
		}, nil
	}
	scope := ViewScope{State: ViewScopeVerified, Labels: maps.Clone(queryLabels), Filters: filters, Note: viewScopeVerifiedNote}
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

// A configured selector can stand for one managed-cluster context. Pin that
// context on later reads, even when the original decision comes from cache.
func configuredReadbackLabels(readback map[string][]map[string]string, keys []string, configured map[string]string) (map[string]string, bool) {
	var first map[string]string
	for _, rows := range readback {
		for _, row := range rows {
			if first == nil {
				first = row
				continue
			}
			for _, k := range keys {
				if row[k] != first[k] {
					return nil, false
				}
			}
		}
	}
	labels := maps.Clone(configured)
	for _, k := range keys {
		if v := first[k]; v != "" && labels[k] == "" {
			labels[k] = v
		}
	}
	return labels, true
}

func unverifiedClusterQualifier(metric string, labels map[string]string) ViewScope {
	return ViewScope{State: ViewScopeLabelMissing, Withheld: true, Labels: maps.Clone(labels),
		Note: fmt.Sprintf("Radar couldn't verify the project or location of the %s series. Set --prometheus-cluster-label to the cluster and its exact project_id or location labels.", metric)}
}

func hasClusterQualifier(labels map[string]string) bool {
	return slices.ContainsFunc(clusterQualifierLabels, func(k string) bool { return labels[k] != "" })
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
	identities, _, err := runViewProbes(ctx, q, at, viewIdentityParts(req.Metrics, partitionLabels, req.Span, false))
	if err != nil {
		return unknownViewScope(), err
	}
	several := ViewScope{State: ViewScopeSeveralClusters, Withheld: true, Note: viewScopeSeveralNote}
	filters := map[string]string{}
	var single []map[string]string
	for _, m := range req.Metrics {
		ids := identities[m.Name]
		if len(ids) > 1 {
			return several, nil
		}
		filters[m.Name] = ""
		if len(ids) == 1 {
			if hasClusterQualifier(ids[0]) {
				return unverifiedClusterQualifier(m.Name, nil), nil
			}
			single = append(single, ids[0])
		}
	}
	if req.Joined && !joinedIdentitiesAgree(single, partitionLabels) {
		return several, nil
	}
	return ViewScope{State: ViewScopeUnlabeled, Filters: filters, Note: viewScopeUnlabeledNote}, nil
}

// joinedIdentitiesAgree reports whether metrics a joined view combines name
// one cluster: equal identities, where one with none of the keys fits any.
func joinedIdentitiesAgree(ids []map[string]string, keys []string) bool {
	empty, joined := identityKey(nil, keys), ""
	for _, id := range ids {
		key := identityKey(id, keys)
		if key == empty {
			continue
		}
		if joined != "" && joined != key {
			return false
		}
		joined = key
	}
	return true
}

func identityKey(identity map[string]string, keys []string) string {
	values := make([]string, len(keys))
	for i, k := range keys {
		values[i] = strconv.Quote(identity[k])
	}
	return strings.Join(values, ",")
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
