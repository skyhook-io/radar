package prometheus

import (
	"context"
	"errors"
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

// A series scope keeps a query to this Kubernetes cluster's series in a
// Prometheus that may hold several clusters' series under the same namespace
// and object names.

const (
	SeriesIsolationConfigured = "configured"
	SeriesIsolationVerified   = "verified"
	SeriesIsolationUnverified = "unverified"
)

// ErrScopeAmbiguous means the selected series exist under more than one
// cluster identity in this Prometheus, so any answer could mix clusters.
var ErrScopeAmbiguous = errors.New("prometheus series scope: the selected series appear under more than one cluster identity")

// ErrScopeMismatch means the verified cluster identity labels select none
// of the probed series although unscoped ones exist.
var ErrScopeMismatch = errors.New("prometheus series scope: cluster identity labels do not appear on the selected series")

type seriesQuerier interface {
	Query(ctx context.Context, query string) (*prom.QueryResult, error)
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error)
}

// SeriesIsolation says how the queries keep to this Kubernetes cluster's series.
type SeriesIsolation struct {
	Mode   string            `json:"mode"`
	Labels map[string]string `json:"labels,omitempty"`
	Note   string            `json:"note"`
}

func withScope(selector, matchers string) string {
	if matchers == "" {
		return selector
	}
	if selector == "" {
		return matchers
	}
	return selector + "," + matchers
}

// scopeProbe names the series whose identity labels decide a scope: one
// selector per batch and, for a chart over a range, the window every identity
// is collected over (an instant check would miss one that stopped reporting
// minutes ago but still fills the chart).
type scopeProbe struct {
	metric            string
	additionalMetrics []string
	// key is the label one Kubernetes object's series share (pod, claim).
	key                     string
	selectors               []string
	window                  time.Duration
	allowUnlabeledProof     bool
	allowUnlabeledDuplicate bool
	singlePartition         bool
	unverifiedNote          string
}

func (p scopeProbe) over(sel string) string {
	metrics := append([]string{p.metric}, p.additionalMetrics...)
	probes := make([]string, len(metrics))
	for i, metric := range metrics {
		probes[i] = metric + "{" + sel + "}"
		if p.window > 0 {
			probes[i] = "count_over_time(" + probes[i] + "[" + p.window.String() + "])"
		}
	}
	// Range functions drop metric names, so evaluate each metric before unioning
	// to avoid identical labelsets in a single range-function result.
	return strings.Join(probes, " or ")
}

// ResolvePVCScope decides the cluster-identity matchers for the named claims'
// kubelet volume stats over window (0 for an instant read).
func (client *Client) ResolvePVCScope(ctx context.Context, namespace string, claims []string, anchors []prom.WorkloadPodIdentity, window time.Duration) (string, SeriesIsolation, error) {
	return client.resolveScope(ctx, namespace, pvcScopeProbe(namespace, claims, window), anchors, nil)
}

// ResolveTrafficScope decides the cluster-identity matchers for one traffic
// metric's series (selector narrows them, e.g. to Beyla's jobs) across the
// whole store. The map has no namespace of its own, so the
// automatic proof anchors on kube-system, which always runs Pods, or on any
// namespace the cache holds when it never sees kube-system. On
// ErrScopeMismatch the isolation still names the proven labels, so a caller
// can say which cluster the traffic failed to match, and on ErrScopeAmbiguous
// after a proof it names the labels every ambiguous series already carries.
func (client *Client) ResolveTrafficScope(ctx context.Context, metric, selector string, cache *k8s.ResourceCache) (string, SeriesIsolation, error) {
	return client.resolveScope(ctx, trafficAnchorNamespace(cache), trafficScopeProbe(metric, selector), nil, cache)
}

// TrafficSpansClusters reports whether one traffic metric's series (selector
// narrows them) carry more than one cluster identity across the whole store,
// read from the labels ClusterIdentityLabels names. Series without any of
// those labels count as one identity of their own.
func (client *Client) TrafficSpansClusters(ctx context.Context, metric, selector string) (bool, error) {
	if client == nil {
		return false, errors.New("Prometheus client not initialized")
	}
	return trafficSpansClusters(ctx, client, metric, selector)
}

func trafficSpansClusters(ctx context.Context, q seriesQuerier, metric, selector string) (bool, error) {
	_, err := readScopePartition(ctx, q, trafficScopeProbe(metric, selector), "")
	if errors.Is(err, ErrScopeAmbiguous) {
		return true, nil
	}
	return false, err
}

func trafficScopeProbe(metric, selector string) scopeProbe {
	return scopeProbe{
		metric: metric, selectors: []string{selector}, singlePartition: true, allowUnlabeledProof: true,
		unverifiedNote: "Radar couldn't confirm this traffic belongs to this exact cluster (no cluster label it could check)",
	}
}

func trafficAnchorNamespace(cache *k8s.ResourceCache) string {
	const preferred = "kube-system"
	if cache == nil || cache.Pods() == nil {
		return preferred
	}
	if pods, err := cache.Pods().Pods(preferred).List(labels.Everything()); err == nil && len(pods) > 0 {
		return preferred
	}
	pods, err := cache.Pods().List(labels.Everything())
	if err != nil || len(pods) == 0 {
		return preferred
	}
	namespace := pods[0].Namespace
	for _, pod := range pods[1:] {
		namespace = min(namespace, pod.Namespace)
	}
	return namespace
}

// ClusterIdentityLabels are the labels Radar reads as a cluster's identity.
func ClusterIdentityLabels() []string {
	return slices.Clone(partitionLabels)
}

func pvcScopeProbe(namespace string, claims []string, window time.Duration) scopeProbe {
	return scopeProbe{
		metric: "kubelet_volume_stats_capacity_bytes", additionalMetrics: []string{"kubelet_volume_stats_used_bytes"},
		selectors: ClaimSelectors(namespace, claims), window: window,
		allowUnlabeledProof: true, allowUnlabeledDuplicate: true, singlePartition: true,
		unverifiedNote: "Matched by namespace and claim names. Radar couldn't confirm these volume stats belong to this exact cluster (no cluster label it could check)",
	}
}

// ClaimSelectors selects the named claims of one namespace, in batches that
// keep each regex matcher small.
func ClaimSelectors(namespace string, claims []string) []string {
	names := make([]string, len(claims))
	for i, c := range claims {
		names[i] = regexp.QuoteMeta(c)
	}
	sort.Strings(names)
	var out []string
	for start := 0; start < len(names); start += pvcUsageBatchSize {
		end := min(start+pvcUsageBatchSize, len(names))
		out = append(out, "namespace="+strconv.Quote(namespace)+",persistentvolumeclaim=~"+strconv.Quote(strings.Join(names[start:end], "|")))
	}
	return out
}

// resolveScope applies an operator-configured scope first, then identity
// labels proven by kube-state-metrics Pod UIDs. Only those two may add
// matchers: labels merely seen on the series are not identity (the CNPG
// exporter's own `cluster` label is the database cluster's name).
// With no anchors, a cache lets the proof use the namespace's current Pods.
func (client *Client) resolveScope(ctx context.Context, namespace string, probe scopeProbe, anchors []prom.WorkloadPodIdentity, cache *k8s.ResourceCache) (string, SeriesIsolation, error) {
	if client == nil {
		return "", SeriesIsolation{}, errors.New("Prometheus client not initialized")
	}
	if config, _, configured := client.workloadMetricsConfig(); configured {
		m, err := config.Matchers()
		if err != nil {
			return "", SeriesIsolation{}, err
		}
		iso := SeriesIsolation{Mode: SeriesIsolationConfigured, Labels: config.ClusterLabels, Note: "Matched by the cluster labels an operator configured"}
		if config.SingleCluster {
			iso.Note = "An operator declared this Prometheus single-cluster"
		}
		return m, iso, nil
	}
	var verified map[string]string
	unlabeled := false
	if len(anchors) > 0 || cache != nil {
		if proof, err := client.historicalClusterProof(ctx, PodScope{Namespace: namespace, Identities: anchors}, cache); err == nil {
			verified, unlabeled = proof.scope.ClusterLabels, proof.unlabeled
		} else if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			errorlog.Record("prometheus", "warning", "series scope proof failed for namespace %s: %v", namespace, err)
		}
	}
	if unlabeled && probe.allowUnlabeledProof {
		matchers := make([]string, len(partitionLabels))
		for i, label := range partitionLabels {
			matchers[i] = label + `=""`
		}
		iso := SeriesIsolation{Mode: SeriesIsolationUnverified, Note: "Matched to stats without cluster identity labels, like this namespace's kube-state-metrics Pod evidence. Radar cannot distinguish unlabeled stats from another cluster"}
		return decideVerifiedScope(ctx, client, probe, strings.Join(matchers, ","), iso)
	}
	return decideScope(ctx, client, probe, verified)
}

func decideScope(ctx context.Context, q seriesQuerier, probe scopeProbe, verified map[string]string) (string, SeriesIsolation, error) {
	if len(verified) > 0 {
		m, err := prom.WorkloadMetricsScope{ClusterLabels: verified}.Matchers()
		if err != nil {
			return "", SeriesIsolation{}, err
		}
		return decideVerifiedScope(ctx, q, probe, m, verifiedIsolation(verified))
	}
	if probe.singlePartition {
		if _, err := readScopePartition(ctx, q, probe, ""); err != nil {
			return "", SeriesIsolation{}, err
		}
	} else {
		for _, sel := range probe.selectors {
			identities := "count by (" + probe.key + "," + strings.Join(partitionLabels, ",") + ") (" + probe.over(sel) + ")"
			if probe.allowUnlabeledDuplicate {
				// A second scrape without cluster labels is not a second identity.
				identities += " unless on (" + strings.Join(partitionLabels, ",") + ") vector(1)"
			}
			res, err := q.Query(ctx, "max(count by ("+probe.key+") ("+identities+"))")
			if err != nil {
				return "", SeriesIsolation{}, err
			}
			if len(res.Series) > 0 && len(res.Series[0].DataPoints) > 0 && res.Series[0].DataPoints[0].Value > 1 {
				return "", SeriesIsolation{}, ErrScopeAmbiguous
			}
		}
	}
	note := "Matched by namespace and Pod names. Radar couldn't confirm these series belong to this exact cluster (no cluster label it could check)"
	if probe.unverifiedNote != "" {
		note = probe.unverifiedNote
	}
	return "", SeriesIsolation{Mode: SeriesIsolationUnverified, Note: note}, nil
}

func verifiedIsolation(labels map[string]string) SeriesIsolation {
	return SeriesIsolation{Mode: SeriesIsolationVerified, Labels: labels, Note: "Matched by cluster labels confirmed against this cluster's Pods"}
}

func decideVerifiedScope(ctx context.Context, q seriesQuerier, probe scopeProbe, m string, iso SeriesIsolation) (string, SeriesIsolation, error) {
	if probe.singlePartition {
		present, err := readScopePartition(ctx, q, probe, m)
		if err != nil {
			return "", iso, err
		}
		if present {
			return m, iso, nil
		}
	} else {
		for _, sel := range probe.selectors {
			scoped, err := q.Query(ctx, "count("+probe.over(withScope(sel, m))+")")
			if err != nil {
				return "", SeriesIsolation{}, err
			}
			if len(scoped.Series) > 0 {
				return m, iso, nil
			}
		}
	}
	for _, sel := range probe.selectors {
		all, err := q.Query(ctx, "count("+probe.over(sel)+")")
		if err != nil {
			return "", SeriesIsolation{}, err
		}
		if len(all.Series) > 0 {
			return "", iso, ErrScopeMismatch
		}
	}
	return m, iso, nil
}

func readScopePartition(ctx context.Context, q seriesQuerier, probe scopeProbe, matchers string) (bool, error) {
	var partition string
	for _, sel := range probe.selectors {
		identities := "count by (" + strings.Join(partitionLabels, ",") + ") (" + probe.over(withScope(sel, matchers)) + ")"
		if probe.allowUnlabeledDuplicate && matchers == "" {
			identities += " unless on (" + strings.Join(partitionLabels, ",") + ") vector(1)"
		}
		res, err := q.Query(ctx, "topk(2,"+identities+")")
		if err != nil {
			return false, err
		}
		if len(res.Series) > 1 {
			return false, ErrScopeAmbiguous
		}
		for _, row := range res.Series {
			values := make([]string, len(partitionLabels))
			for i, label := range partitionLabels {
				values[i] = strconv.Quote(row.Labels[label])
			}
			key := strings.Join(values, ",")
			if partition != "" && partition != key {
				return false, ErrScopeAmbiguous
			}
			partition = key
		}
	}
	return partition != "", nil
}
