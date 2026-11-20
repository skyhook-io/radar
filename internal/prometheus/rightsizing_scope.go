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

// rightsizingAnchors are this cluster's live objects the anchor checks look
// for, each set from one namespace: names alone, across namespaces, would let
// another cluster's same-named object elsewhere vouch for this cluster.
type rightsizingAnchors struct {
	podNamespace        string
	pods                []string
	replicaSetNamespace string
	replicaSets         []string
}

// rightsizingScopeRequest checks every metric over the subject's namespaces.
// Pod-keyed metrics are anchored on this cluster's Pods, ReplicaSet ownership
// on its ReplicaSets; ReplicaSet ownership is read only when a Deployment is
// sized.
func rightsizingScopeRequest(subject, proofNamespace string, deployments bool, anchors rightsizingAnchors, at time.Time) ViewScopeRequest {
	pods := BoundViewAnchors(anchors.pods)
	req := ViewScopeRequest{View: rightsizingScopeView, Joined: true, Span: rightsizingScopeSpan, At: at, Namespace: proofNamespace}
	for _, name := range rightsizingMetricNames {
		m := ViewMetric{Name: name, Subject: subject, AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: anchors.podNamespace}
		if name == metricReplicaSetOwner {
			if !deployments {
				continue
			}
			m.AnchorLabel, m.AnchorValues, m.AnchorNamespace = "replicaset", BoundViewAnchors(anchors.replicaSets), anchors.replicaSetNamespace
		}
		req.Metrics = append(req.Metrics, m)
	}
	return req
}

func singleRightsizingScopeRequest(kind, namespace string, workload rightsizingWorkload, cache *k8s.ResourceCache) ViewScopeRequest {
	anchors := rightsizingAnchors{
		podNamespace: namespace,
		pods:         append(append([]string(nil), workload.podNames...), NamespacePodAnchors(cache, namespace)...),
	}
	deployment := strings.EqualFold(kind, "Deployment")
	if deployment {
		anchors.replicaSetNamespace = namespace
		anchors.replicaSets = NamespaceReplicaSetAnchors(cache, namespace)
	}
	return rightsizingScopeRequest("namespace="+strconv.Quote(namespace), namespace, deployment, anchors, time.Time{})
}

// scanRightsizingScopeRequest covers the union of a scan's namespaces, so one
// decision holds for every batch.
func scanRightsizingScopeRequest(workloads []scanWorkload, cache *k8s.ResourceCache, at time.Time) ViewScopeRequest {
	return scanRightsizingScopeRequestFrom(workloads, func(namespace string) []string {
		return NamespacePodAnchors(cache, namespace)
	}, func(namespace string) []string {
		return NamespaceReplicaSetAnchors(cache, namespace)
	}, at)
}

func scanRightsizingScopeRequestFrom(workloads []scanWorkload, podsIn, replicaSetsIn func(string) []string, at time.Time) ViewScopeRequest {
	namespaces := batchNamespaces(workloads)
	var anchors rightsizingAnchors
	anchors.podNamespace, anchors.pods = firstAnchoredNamespace(namespaces, podsIn)
	deployments := hasScanKind(workloads, "Deployment")
	if deployments {
		anchors.replicaSetNamespace, anchors.replicaSets = firstAnchoredNamespace(namespaces, replicaSetsIn)
	}
	return rightsizingScopeRequest(labelRegexMatcher("namespace", namespaces), "", deployments, anchors, at)
}

// firstAnchoredNamespace is the first of the sorted namespaces holding any
// live object, with those objects' names.
func firstAnchoredNamespace(namespaces []string, objectsIn func(string) []string) (string, []string) {
	for _, namespace := range namespaces {
		if names := objectsIn(namespace); len(names) > 0 {
			return namespace, names
		}
	}
	return "", nil
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
