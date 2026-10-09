package prometheus

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
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
	return selector + "," + matchers
}

// scopeProbe names the series whose identity labels decide a scope: one
// selector per batch and, for a chart over a range, the window every identity
// is collected over (an instant check would miss one that stopped reporting
// minutes ago but still fills the chart).
type scopeProbe struct {
	metric string
	// key is the label one Kubernetes object's series share (pod, claim).
	key       string
	selectors []string
	window    time.Duration
}

func (p scopeProbe) over(sel string) string {
	if p.window <= 0 {
		return p.metric + "{" + sel + "}"
	}
	return "count_over_time(" + p.metric + "{" + sel + "}[" + p.window.String() + "])"
}

// ResolvePVCScope decides the cluster-identity matchers for the named claims'
// kubelet volume stats over window (0 for an instant read).
func (client *Client) ResolvePVCScope(ctx context.Context, namespace string, claims []string, anchors []prom.WorkloadPodIdentity, window time.Duration) (string, SeriesIsolation, error) {
	m, iso, err := client.resolveScope(ctx, namespace, pvcScopeProbe(namespace, claims, window), anchors, nil)
	return m, iso.forClaims(), err
}

// forClaims words an unverified match for volume stats, which are matched by
// claim name rather than Pod name.
func (iso SeriesIsolation) forClaims() SeriesIsolation {
	if iso.Mode == SeriesIsolationUnverified {
		iso.Note = "Matched by namespace and claim names. Radar couldn't confirm these volume stats belong to this exact cluster (no cluster label it could check)"
	}
	return iso
}

func pvcScopeProbe(namespace string, claims []string, window time.Duration) scopeProbe {
	return scopeProbe{metric: "kubelet_volume_stats_capacity_bytes", key: "persistentvolumeclaim", selectors: ClaimSelectors(namespace, claims), window: window}
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
func resolveScope(ctx context.Context, namespace string, probe scopeProbe, anchors []prom.WorkloadPodIdentity, cache *k8s.ResourceCache) (string, SeriesIsolation, error) {
	return GetClient().resolveScope(ctx, namespace, probe, anchors, cache)
}

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
	if len(anchors) > 0 || cache != nil {
		if cfg, err := client.historicalClusterScope(ctx, PodScope{Namespace: namespace, Identities: anchors}, cache); err == nil {
			verified = cfg.ClusterLabels
		}
	}
	return decideScope(ctx, client, probe, verified)
}

func decideScope(ctx context.Context, q seriesQuerier, probe scopeProbe, verified map[string]string) (string, SeriesIsolation, error) {
	if len(verified) > 0 {
		m, err := prom.WorkloadMetricsScope{ClusterLabels: verified}.Matchers()
		if err != nil {
			return "", SeriesIsolation{}, err
		}
		for _, sel := range probe.selectors {
			scoped, err := q.Query(ctx, "count("+probe.over(withScope(sel, m))+")")
			if err != nil {
				return "", SeriesIsolation{}, err
			}
			if len(scoped.Series) > 0 {
				return m, verifiedIsolation(verified), nil
			}
		}
		for _, sel := range probe.selectors {
			all, err := q.Query(ctx, "count("+probe.over(sel)+")")
			if err != nil {
				return "", SeriesIsolation{}, err
			}
			if len(all.Series) > 0 {
				return "", SeriesIsolation{}, ErrScopeMismatch
			}
		}
		return m, verifiedIsolation(verified), nil
	}
	// Unproven: nothing is pinned. One object (Pod, claim) whose series carry
	// more than one set of partition labels over the range may be two
	// clusters' objects of the same name, so that is refused.
	for _, sel := range probe.selectors {
		res, err := q.Query(ctx, "max(count by ("+probe.key+") (count by ("+probe.key+","+strings.Join(partitionLabels, ",")+") ("+probe.over(sel)+")))")
		if err != nil {
			return "", SeriesIsolation{}, err
		}
		if len(res.Series) > 0 && len(res.Series[0].DataPoints) > 0 && res.Series[0].DataPoints[0].Value > 1 {
			return "", SeriesIsolation{}, ErrScopeAmbiguous
		}
	}
	return "", SeriesIsolation{Mode: SeriesIsolationUnverified, Note: "Matched by namespace and Pod names. Radar couldn't confirm these series belong to this exact cluster (no cluster label it could check)"}, nil
}

func verifiedIsolation(labels map[string]string) SeriesIsolation {
	return SeriesIsolation{Mode: SeriesIsolationVerified, Labels: labels, Note: "Matched by cluster labels confirmed against this cluster's Pods"}
}
