package traffic

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	promclient "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// clusterScope keeps one metric family's traffic queries to this cluster's
// series when a configured metrics store holds several clusters.
type clusterScope struct {
	// matchers is the PromQL fragment added to every query; empty reads all.
	matchers string
	// labels are the cluster labels behind matchers, for messages.
	labels map[string]string
	// mixed means Radar could not tell which series are this cluster's, so the
	// family is read unfiltered and may include other clusters' traffic.
	mixed bool
}

// resolveTrafficScope is the shared cluster proof the workload charts use:
// the operator's --prometheus-cluster-label when set, otherwise the labels
// kube-state-metrics proves for this cluster's Pods. A variable so tests can
// stand in for the Prometheus client.
var resolveTrafficScope = func(ctx context.Context, metric, selector string) (string, promclient.SeriesIsolation, error) {
	client, err := promclient.ClientForOperation()
	if err != nil {
		return "", promclient.SeriesIsolation{}, err
	}
	return client.ResolveTrafficScope(ctx, metric, selector, k8s.GetResourceCache())
}

// scopeForMetric decides how to keep metric's traffic queries to this cluster.
// selector is the source's own matcher fragment for the metric (may be empty).
// filter is false when the source reads a store Radar found on its own, such
// as Caretta's caretta-vm, which holds only this cluster and usually carries
// no cluster label: filtering there would empty the map.
//
// An error means Radar could not check which cluster the series belong to;
// the caller shows nothing rather than risk drawing another cluster's traffic.
func scopeForMetric(ctx context.Context, metric, selector string, filter bool) (clusterScope, error) {
	if !filter {
		return clusterScope{}, nil
	}
	matchers, iso, err := resolveTrafficScope(ctx, metric, selector)
	switch {
	case err == nil && matchers != "":
		// The operator's labels, the labels proven for this cluster, or, when
		// this cluster's kube-state-metrics carry no cluster label, the
		// series without one.
		return clusterScope{matchers: matchers, labels: iso.Labels}, nil
	case err == nil && iso.Mode == promclient.SeriesIsolationUnverified:
		// Nothing proves which cluster this is. A store holding one identity
		// may still hold another cluster's traffic, so say so.
		return clusterScope{mixed: true}, nil
	case err == nil:
		// An operator declared the store single-cluster.
		return clusterScope{}, nil
	case errors.Is(err, promclient.ErrScopeMismatch), errors.Is(err, promclient.ErrScopeAmbiguous):
		// With proven labels, keep them: they still exclude every series
		// another cluster labels differently. On a mismatch the query comes
		// back empty and the empty-map warning names them. On ambiguity the
		// series they select carry more than one identity, which may be one
		// cluster scraped twice or two clusters sharing these labels, so the
		// map also says it may include other clusters' traffic.
		if len(iso.Labels) > 0 {
			if m, mErr := (prom.WorkloadMetricsScope{ClusterLabels: iso.Labels}).Matchers(); mErr == nil {
				return clusterScope{matchers: m, labels: iso.Labels, mixed: errors.Is(err, promclient.ErrScopeAmbiguous)}, nil
			}
		}
		// No labels proven: this cluster's kube-state-metrics carry none while
		// the traffic does, or no proof at all. Radar can't tell clusters apart.
		return clusterScope{mixed: true}, nil
	}
	log.Printf("[traffic] Could not check which cluster %s series belong to: %v", metric, err)
	return clusterScope{}, err
}

// with joins a selector fragment with the scope's matchers.
func (sc clusterScope) with(selector string) string {
	switch {
	case sc.matchers == "":
		return selector
	case selector == "":
		return sc.matchers
	}
	return selector + "," + sc.matchers
}

// labelKeys are the labels whose values tell clusters apart in a store: the
// scope's own and every label Radar reads as cluster identity.
func (sc clusterScope) labelKeys() []string {
	keys := promclient.ClusterIdentityLabels()
	for k := range sc.labels {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func (sc clusterScope) labelText() string {
	keys := make([]string, 0, len(sc.labels))
	for k := range sc.labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + sc.labels[k]
	}
	return strings.Join(parts, ", ")
}

// identityCountQuery counts selector's series by cluster identity labels.
// selector is a full PromQL expression without the cluster matchers.
func (sc clusterScope) identityCountQuery(selector string) string {
	return fmt.Sprintf("count by (%s) (%s)", strings.Join(sc.labelKeys(), ", "), selector)
}

// carriesIdentity reports whether any row has a cluster identity label.
func (sc clusterScope) carriesIdentity(rows []map[string]string) bool {
	keys := sc.labelKeys()
	for _, row := range rows {
		for _, k := range keys {
			if row[k] != "" {
				return true
			}
		}
	}
	return false
}

// selectsAny reports whether any row carries every one of the scope's labels.
func (sc clusterScope) selectsAny(rows []map[string]string) bool {
	for _, row := range rows {
		all := true
		for k, v := range sc.labels {
			if row[k] != v {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// clusterMismatchWarning explains an empty map. rows are the identity-count
// rows of the same query run without the cluster matchers. None means the
// store really has no traffic here; a row carrying this cluster's labels
// means this cluster is quiet or its traffic could not be placed. Either
// way the plain empty state is the truth. collector names who exports the
// metric, for the fix.
func (sc clusterScope) clusterMismatchWarning(rows []map[string]string, collector string) string {
	if sc.matchers == "" || len(rows) == 0 || sc.selectsAny(rows) {
		return ""
	}
	if sc.carriesIdentity(rows) {
		return fmt.Sprintf("The metrics store has traffic, but none of it carries %s, the label Radar uses for this cluster. "+
			"If this cluster's traffic is labeled differently there, start Radar with that label in "+
			"--prometheus-cluster-label (Helm: traffic.prometheusClusterLabels).", sc.labelText())
	}
	return fmt.Sprintf("The metrics store has traffic, but it isn't labeled by cluster, so Radar can't tell this cluster's traffic from others'. "+
		"Add a cluster label where %s is scraped.", collector)
}

const mixedClustersNote = "Radar couldn't work out which cluster this traffic comes from, so the map may include other clusters' traffic. " +
	"To limit it to this cluster, start Radar with --prometheus-cluster-label (Helm: traffic.prometheusClusterLabels)."

// applyClusterScopeWarning sets the cluster warning on a response: the reason
// for an empty map, or the note that the flows shown may mix clusters. The
// note joins a warning the source already set, since which cluster the flows
// belong to matters more than their details, but a transient kind stays so
// the client still retries the read that failed. An empty-map reason never
// replaces one: the source's warning is about what it could read at all.
func applyClusterScopeWarning(response *FlowsResponse, mismatch string, mixed bool) {
	switch {
	case len(response.Flows) == 0 && mismatch != "" && response.Warning == "":
		response.Warning = mismatch
		response.WarningKind = WarningCluster
	case len(response.Flows) > 0 && mixed:
		response.Warning = strings.TrimSpace(mixedClustersNote + " " + response.Warning)
		if response.WarningKind != WarningTransient {
			response.WarningKind = WarningCluster
		}
	}
}

// scopeCheckFailed is the response when Radar could not check which cluster
// the traffic belongs to. Drawing it anyway could show another cluster's.
func scopeCheckFailed(source string, err error) *FlowsResponse {
	return &FlowsResponse{Source: source, Timestamp: time.Now(), Flows: []Flow{},
		Warning:     fmt.Sprintf("Radar couldn't check which cluster this traffic belongs to, so it isn't shown: %v", err),
		WarningKind: WarningTransient}
}
