package prometheus

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// ReasonClusterScopeWithheld is the single-workload reason when the metrics
// store can't be kept to this cluster; the response's clusterScope says why.
const ReasonClusterScopeWithheld = "Radar can't tell which series in this metrics store belong to this cluster, so it withheld recommendations."

// ReasonRightsizingPartialData is the single-workload reason when the store
// answered the usage queries only partially.
const ReasonRightsizingPartialData = "Prometheus returned partial data for the rightsizing queries, so no recommendation was made."

// ScanReasonClusterScopeWithheld is the scan reason, warning code and row
// recommendationReason for the same situation.
const ScanReasonClusterScopeWithheld = "cluster_scope_withheld"

const (
	metricPodOwner          = "kube_pod_owner"
	metricReplicaSetOwner   = "kube_replicaset_owner"
	metricRestarts          = "kube_pod_container_status_restarts_total"
	metricTerminatedAt      = "kube_pod_container_status_last_terminated_timestamp"
	metricTerminatedReason  = "kube_pod_container_status_last_terminated_reason"
	metricCPUUsage          = "container_cpu_usage_seconds_total"
	metricMemoryWorkingSet  = "container_memory_working_set_bytes"
	metricThrottledPeriods  = "container_cpu_cfs_throttled_periods_total"
	metricCFSPeriods        = "container_cpu_cfs_periods_total"
	rightsizingScopeView    = "rightsizing"
	rightsizingImplicitLook = 5 * time.Minute
)

// rightsizingScopeSpan is what the queries read: the 7-day subquery, the 5m
// rate window inside it, and the store's implicit lookback for plain selectors.
const rightsizingScopeSpan = rightsizingWindow + 5*time.Minute + rightsizingImplicitLook

// rightsizingMetricNames are every metric the rightsizing queries read.
var rightsizingMetricNames = []string{
	metricPodOwner, metricReplicaSetOwner, metricRestarts, metricTerminatedAt, metricTerminatedReason,
	metricCPUUsage, metricMemoryWorkingSet, metricThrottledPeriods, metricCFSPeriods,
}

// rightsizingScopeRequest checks every metric over the subject's namespaces.
// Pod-keyed metrics are anchored on this cluster's Pods there, ReplicaSet
// ownership on its ReplicaSets; ReplicaSet ownership is read only when a
// Deployment is sized.
func rightsizingScopeRequest(subject, proofNamespace string, deployments bool, pods, replicaSets []string, at time.Time) ViewScopeRequest {
	pods = BoundViewAnchors(pods)
	req := ViewScopeRequest{View: rightsizingScopeView, Joined: true, Span: rightsizingScopeSpan, At: at, Namespace: proofNamespace}
	for _, name := range rightsizingMetricNames {
		m := ViewMetric{Name: name, Subject: subject, AnchorLabel: "pod", AnchorValues: pods}
		if name == metricReplicaSetOwner {
			if !deployments {
				continue
			}
			m.AnchorLabel, m.AnchorValues = "replicaset", BoundViewAnchors(replicaSets)
		}
		req.Metrics = append(req.Metrics, m)
	}
	return req
}

func singleRightsizingScopeRequest(kind, namespace string, workload rightsizingWorkload, cache *k8s.ResourceCache) ViewScopeRequest {
	pods := append(append([]string(nil), workload.podNames...), NamespacePodAnchors(cache, namespace)...)
	deployment := strings.EqualFold(kind, "Deployment")
	var replicaSets []string
	if deployment {
		replicaSets = NamespaceReplicaSetAnchors(cache, namespace)
	}
	return rightsizingScopeRequest("namespace="+strconv.Quote(namespace), namespace, deployment, pods, replicaSets, time.Time{})
}

// scanRightsizingScopeRequest covers the union of a scan's namespaces, so one
// decision holds for every batch.
func scanRightsizingScopeRequest(workloads []scanWorkload, cache *k8s.ResourceCache, at time.Time) ViewScopeRequest {
	namespaces := batchNamespaces(workloads)
	var pods, replicaSets []string
	for _, namespace := range namespaces {
		pods = append(pods, NamespacePodAnchors(cache, namespace)...)
	}
	deployments := hasScanKind(workloads, "Deployment")
	if deployments {
		for _, namespace := range namespaces {
			replicaSets = append(replicaSets, NamespaceReplicaSetAnchors(cache, namespace)...)
		}
	}
	return rightsizingScopeRequest(labelRegexMatcher("namespace", namespaces), "", deployments, pods, replicaSets, at)
}

// scopeMatchers is ",<matchers>" for a metric the scope filters, else "", so
// an unfiltered selector stays exactly what it was.
func scopeMatchers(scope ViewScope, metric string) string {
	if filter := scope.Filter(metric); filter != "" {
		return "," + filter
	}
	return ""
}

// bareScopedSelector is the metric name alone when unfiltered.
func bareScopedSelector(scope ViewScope, metric string) string {
	if filter := scope.Filter(metric); filter != "" {
		return metric + "{" + filter + "}"
	}
	return metric
}

// strictRightsizingQuerier reads a partial or warning answer as a failed
// query: a recommendation built from some of the samples looks as confident
// as one built from all of them.
type strictRightsizingQuerier struct{ inner rightsizingQuerier }

func (s strictRightsizingQuerier) Query(ctx context.Context, query string) (*prom.QueryResult, error) {
	return requireComplete(s.inner.Query(ctx, query))
}

func isIncompleteAnswer(err error) bool {
	return errors.Is(err, prom.ErrPartialResponse) || errors.Is(err, prom.ErrQueryWarning)
}
