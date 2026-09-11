package prometheus

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// AuthGate is the per-request resource read check used by handlers that read
// K8s spec data via the shared informer cache. The cache is populated using
// Radar's service-account permissions, so without this gate any authenticated
// user could fetch any namespace's spec by guessing names. Server.canRead is
// the concrete implementation; passing it via SetAuthGate avoids an import
// cycle (server imports prometheus, not the other way around).
type AuthGate func(r *http.Request, group, resource, namespace, verb string) bool

var authGate atomic.Pointer[AuthGate]

// SetAuthGate installs the request-scoped authorization check. Pass nil to
// disable gating (only appropriate for tests).
func SetAuthGate(fn AuthGate) {
	if fn == nil {
		authGate.Store(nil)
		return
	}
	authGate.Store(&fn)
}

// canRead consults the installed AuthGate. Returns true when no gate is
// installed so the gate stays strictly additive — never accidentally locks
// out the OSS no-auth path.
func canRead(r *http.Request, group, resource, namespace, verb string) bool {
	g := authGate.Load()
	if g == nil {
		return true
	}
	return (*g)(r, group, resource, namespace, verb)
}

// ClusterWideMetricsDeniedMessage is the denial every unbounded metrics
// surface returns: raw PromQL, metric discovery, rules and the cluster
// aggregate. Nothing in such a request names a resource the caller could be
// told to ask for, so the message names the grant instead.
const ClusterWideMetricsDeniedMessage = "cluster-wide metrics access requires permission to list pods across all namespaces"

// canReadClusterWideMetrics gates the surfaces where one query can reach any
// namespace's series. Listing pods across all namespaces is the RBAC grant
// closest to "may see everything running in the cluster"; an empty namespace
// makes the SubjectAccessReview an all-namespaces check.
func canReadClusterWideMetrics(r *http.Request) bool {
	return canRead(r, "", "pods", "", "list")
}

// canReadMetricsResource gates a curated chart on reading the resource it
// charts. Fails closed for a kind with no mapping so a new entry in
// prom.SupportedKinds cannot ship ungated. A cluster-scoped kind is always
// reviewed without a namespace: the namespaced route accepts Node too, and a
// SubjectAccessReview that carries a namespace consults that namespace's
// RoleBindings, which would let a namespace-only admin read node metrics.
func canReadMetricsResource(r *http.Request, kind, namespace string) bool {
	group, resource, clusterScoped, ok := metricsKindResource(kind)
	if !ok {
		return false
	}
	if clusterScoped {
		namespace = ""
	}
	return canRead(r, group, resource, namespace, "get")
}

func metricsKindResource(kind string) (group, resource string, clusterScoped, ok bool) {
	switch strings.ToLower(kind) {
	case "pod":
		return "", "pods", false, true
	case "node":
		return "", "nodes", true, true
	case "deployment":
		return "apps", "deployments", false, true
	case "statefulset":
		return "apps", "statefulsets", false, true
	case "daemonset":
		return "apps", "daemonsets", false, true
	case "replicaset":
		return "apps", "replicasets", false, true
	case "job":
		return "batch", "jobs", false, true
	case "cronjob":
		return "batch", "cronjobs", false, true
	}
	return "", "", false, false
}
