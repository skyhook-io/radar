package opencost

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type RouteScope struct {
	AllowedNamespaces func(*http.Request, []string) []string
	CanReadNodes      func(*http.Request) bool
}

// RegisterRoutes registers OpenCost routes on the given router.
func RegisterRoutes(r chi.Router, resolveCurrency func() string, scope RouteScope) {
	r.Get("/opencost/summary", func(w http.ResponseWriter, r *http.Request) { handleSummaryScoped(w, r, resolveCurrency, scope) })
	r.Get("/opencost/workloads", func(w http.ResponseWriter, r *http.Request) { handleWorkloadsScoped(w, r, resolveCurrency, scope) })
	r.Get("/opencost/trend", func(w http.ResponseWriter, r *http.Request) { handleTrendScoped(w, r, resolveCurrency, scope) })
	r.Get("/opencost/nodes", func(w http.ResponseWriter, r *http.Request) { handleNodesScoped(w, r, resolveCurrency, scope) })
}

// handleSummary returns namespace-level cost summary from OpenCost Prometheus metrics.
func handleSummary(w http.ResponseWriter, r *http.Request, resolveCurrency func() string) {
	handleSummaryScoped(w, r, resolveCurrency, RouteScope{})
}

func handleSummaryScoped(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, scope RouteScope) {
	currency := resolvedCurrency(resolveCurrency)
	connection, err := Selected(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: ConnectionFailureReason(err), Currency: currency, Source: "kubecost"})
		return
	}
	if connection.Source == SourceKubecost {
		resp, err := pkgopencost.ComputeKubecostSummary(r.Context(), connection.Client, pkgopencost.KubecostCurrentOptions{Currency: currency, ClusterID: connection.ClusterID})
		if err != nil {
			log.Printf("[opencost] Kubecost summary failed: %v", err)
			writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: ConnectionFailureReason(err), Currency: currency, Source: "kubecost"})
			return
		}
		if scope.AllowedNamespaces != nil {
			filterCostSummary(resp, scope.AllowedNamespaces(r, nil))
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: pkgopencost.ReasonNoPrometheus, Currency: currency, Source: "prometheus"})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (summary): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: ConnectionFailureReason(err), Currency: currency, Source: "prometheus"})
		return
	}
	resp := pkgopencost.ComputeCostSummaryFromProm(
		r.Context(), client.Prom(), pkgopencost.SummaryOptions{Currency: currency})
	resp.Source = "prometheus"
	if scope.AllowedNamespaces != nil {
		filterCostSummary(resp, scope.AllowedNamespaces(r, nil))
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[opencost] Failed to encode JSON response: %v", err)
	}
}

// handleWorkloads returns workload-level cost breakdown for a namespace.
func handleWorkloads(w http.ResponseWriter, r *http.Request, resolveCurrency func() string) {
	handleWorkloadsScoped(w, r, resolveCurrency, RouteScope{})
}

func handleWorkloadsScoped(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, scope RouteScope) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "namespace parameter is required"})
		return
	}
	if scope.AllowedNamespaces != nil && len(scope.AllowedNamespaces(r, []string{ns})) == 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no access to namespace " + ns})
		return
	}
	currency := resolvedCurrency(resolveCurrency)
	connection, err := Selected(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{Namespace: ns, Reason: ConnectionFailureReason(err), Currency: currency, Source: "kubecost"})
		return
	}
	if connection.Source == SourceKubecost {
		resp, err := pkgopencost.ComputeKubecostWorkloads(r.Context(), connection.Client, ns, pkgopencost.KubecostCurrentOptions{Currency: currency, ClusterID: connection.ClusterID, Owners: BuildPodOwnerLookup(ns)})
		if err != nil {
			log.Printf("[opencost] Kubecost workloads failed for namespace %q: %v", ns, err)
			writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{Namespace: ns, Reason: ConnectionFailureReason(err), Currency: currency, Source: "kubecost"})
			return
		}
		attachDesiredReplicas(resp, ns)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{Namespace: ns, Reason: pkgopencost.ReasonNoPrometheus, Currency: currency, Source: "prometheus"})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (workloads): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{Namespace: ns, Reason: ConnectionFailureReason(err), Currency: currency, Source: "prometheus"})
		return
	}

	resp := pkgopencost.ComputeWorkloadsFromProm(r.Context(), client.Prom(), ns, BuildPodOwnerLookup(ns))
	resp.Currency = currency
	resp.Source = "prometheus"
	writeJSON(w, http.StatusOK, resp)
}

// BuildPodOwnerLookup snapshots radar's pod informer for `ns` so
// pkg/opencost.ComputeWorkloadsFromProm can resolve pod→workload without
// depending on client-go.
func BuildPodOwnerLookup(ns string) pkgopencost.PodOwnerLookup {
	rc := k8s.GetResourceCache()
	if rc == nil || rc.Pods() == nil {
		return nil
	}
	pods, err := rc.Pods().Pods(ns).List(labels.Everything())
	if err != nil || len(pods) == 0 {
		return nil
	}
	owners := make(map[string]pkgopencost.WorkloadOwner, len(pods))
	for _, p := range pods {
		owners[p.Name] = resolvePodOwner(p.OwnerReferences)
	}
	return func(podName string) (pkgopencost.WorkloadOwner, bool) {
		o, ok := owners[podName]
		return o, ok
	}
}

// resolvePodOwner walks owner references to find the top-level workload.
// Pods owned by a ReplicaSet are mapped back to the parent Deployment by
// stripping the RS hash suffix.
func resolvePodOwner(refs []metav1.OwnerReference) pkgopencost.WorkloadOwner {
	if len(refs) == 0 {
		return pkgopencost.WorkloadOwner{Kind: "standalone"}
	}
	owner := refs[0]
	if owner.Kind == "ReplicaSet" {
		if deployName := stripReplicaSetSuffix(owner.Name); deployName != owner.Name {
			return pkgopencost.WorkloadOwner{Name: deployName, Kind: "Deployment"}
		}
	}
	return pkgopencost.WorkloadOwner{Name: owner.Name, Kind: owner.Kind}
}

func stripReplicaSetSuffix(name string) string {
	if idx := strings.LastIndex(name, "-"); idx > 0 {
		return name[:idx]
	}
	return name
}

// handleTrend returns cost trend data over time as a stacked series per namespace.
func handleTrend(w http.ResponseWriter, r *http.Request, resolveCurrency func() string) {
	handleTrendScoped(w, r, resolveCurrency, RouteScope{})
}

func handleTrendScoped(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, scope RouteScope) {
	connection, err := Selected(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency), Source: "kubecost", Range: r.URL.Query().Get("range")})
		return
	}
	if connection.Source == SourceKubecost {
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: pkgopencost.ReasonHistoryUnsupported, Currency: resolvedCurrency(resolveCurrency), Source: "kubecost", Range: r.URL.Query().Get("range")})
		return
	}
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: pkgopencost.ReasonNoPrometheus, Currency: resolvedCurrency(resolveCurrency), Source: "prometheus", Range: r.URL.Query().Get("range")})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (trend): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency), Source: "prometheus", Range: r.URL.Query().Get("range")})
		return
	}
	var namespaces []string
	if scope.AllowedNamespaces != nil {
		namespaces = scope.AllowedNamespaces(r, nil)
	}
	resp := pkgopencost.ComputeCostTrendFromProm(r.Context(), client.Prom(), pkgopencost.TrendPromOptions{
		Range:      r.URL.Query().Get("range"),
		Namespaces: namespaces,
	})
	resp.Currency = resolvedCurrency(resolveCurrency)
	resp.Source = "prometheus"
	writeJSON(w, http.StatusOK, resp)
}

// handleNodes returns per-node cost breakdown.
func handleNodes(w http.ResponseWriter, r *http.Request, resolveCurrency func() string) {
	handleNodesScoped(w, r, resolveCurrency, RouteScope{})
}

func handleNodesScoped(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, scope RouteScope) {
	if scope.CanReadNodes != nil && !scope.CanReadNodes(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "no access to nodes"})
		return
	}
	currency := resolvedCurrency(resolveCurrency)
	connection, err := Selected(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: currency, Source: "kubecost"})
		return
	}
	if connection.Source == SourceKubecost {
		resp, err := pkgopencost.ComputeKubecostNodes(r.Context(), connection.Client, pkgopencost.KubecostCurrentOptions{Currency: currency, ClusterID: connection.ClusterID})
		if err != nil {
			log.Printf("[opencost] Kubecost nodes failed: %v", err)
			writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: currency, Source: "kubecost"})
			return
		}
		attachNodeProviderIDs(resp)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: pkgopencost.ReasonNoPrometheus, Currency: resolvedCurrency(resolveCurrency), Source: "prometheus"})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (nodes): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency), Source: "prometheus"})
		return
	}
	resp := pkgopencost.ComputeNodeCosts(r.Context(), client.Prom())
	resp.Currency = currency
	resp.Source = "prometheus"
	attachNodeProviderIDs(resp)
	writeJSON(w, http.StatusOK, resp)
}

func filterCostSummary(resp *pkgopencost.CostSummary, allowed []string) {
	if resp == nil || allowed == nil {
		return
	}
	if len(allowed) == 0 {
		resp.Available = false
		resp.Reason = pkgopencost.ReasonAccessDenied
		resp.Namespaces = nil
		resp.TotalHourlyCost = 0
		resp.TotalStorageCost = 0
		resp.TotalNetworkCost = 0
		resp.TotalIdleCost = 0
		resp.ClusterEfficiency = 0
		return
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, namespace := range allowed {
		allow[namespace] = struct{}{}
	}
	filtered := make([]pkgopencost.NamespaceCost, 0, len(resp.Namespaces))
	resp.TotalHourlyCost = 0
	resp.TotalStorageCost = 0
	resp.TotalNetworkCost = 0
	resp.TotalIdleCost = 0
	var allocated, usage float64
	for _, row := range resp.Namespaces {
		if _, ok := allow[row.Name]; !ok {
			continue
		}
		filtered = append(filtered, row)
		resp.TotalHourlyCost += row.HourlyCost
		resp.TotalStorageCost += row.StorageCost
		resp.TotalNetworkCost += row.NetworkCost
		resp.TotalIdleCost += row.IdleCost
		allocated += row.CPUCost + row.MemoryCost
		usage += row.CPUUsageCost + row.MemoryUsageCost
	}
	resp.Namespaces = filtered
	if allocated > 0 {
		resp.ClusterEfficiency = usage / allocated * 100
	} else {
		resp.ClusterEfficiency = 0
	}
	sort.Slice(resp.Namespaces, func(i, j int) bool { return resp.Namespaces[i].HourlyCost > resp.Namespaces[j].HourlyCost })
}

func attachDesiredReplicas(resp *pkgopencost.WorkloadCostResponse, namespace string) {
	if resp == nil || len(resp.Workloads) == 0 {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return
	}
	for i := range resp.Workloads {
		workload := &resp.Workloads[i]
		switch workload.Kind {
		case "Deployment":
			if cache.Deployments() != nil {
				if deployment, err := cache.Deployments().Deployments(namespace).Get(workload.Name); err == nil {
					workload.Replicas = 1
					if deployment.Spec.Replicas != nil {
						workload.Replicas = int(*deployment.Spec.Replicas)
					}
				}
			}
		case "StatefulSet":
			if cache.StatefulSets() != nil {
				if statefulSet, err := cache.StatefulSets().StatefulSets(namespace).Get(workload.Name); err == nil {
					workload.Replicas = 1
					if statefulSet.Spec.Replicas != nil {
						workload.Replicas = int(*statefulSet.Spec.Replicas)
					}
				}
			}
		case "DaemonSet":
			if cache.DaemonSets() != nil {
				if daemonSet, err := cache.DaemonSets().DaemonSets(namespace).Get(workload.Name); err == nil {
					workload.Replicas = int(daemonSet.Status.DesiredNumberScheduled)
				}
			}
		}
	}
}

func resolvedCurrency(resolve func() string) string {
	if resolve == nil {
		return pkgopencost.DefaultCurrency
	}
	if currency := resolve(); currency != "" {
		return currency
	}
	return pkgopencost.DefaultCurrency
}

func ConnectionFailureReason(err error) string {
	if errors.Is(err, prometheuspkg.ErrPrometheusNotFound) {
		return pkgopencost.ReasonNoPrometheus
	}
	if errors.Is(err, ErrKubecostNoData) {
		return pkgopencost.ReasonNoMetrics
	}
	if errors.Is(err, ErrKubecostAuthentication) {
		return pkgopencost.ReasonAuthentication
	}
	var httpErr *prom.HTTPError
	if errors.As(err, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden) {
		return pkgopencost.ReasonAuthentication
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, ErrKubecostClusterID) || errors.Is(err, ErrKubecostContextMismatch) || errors.Is(err, ErrKubecostUnavailable) || strings.Contains(message, "kubecost") || strings.Contains(message, "cost source") {
		return pkgopencost.ReasonSourceUnavailable
	}
	return pkgopencost.ReasonQueryError
}

func attachNodeProviderIDs(resp *pkgopencost.NodeCostResponse) {
	if resp == nil || !resp.Available || len(resp.Nodes) == 0 {
		return
	}
	rc := k8s.GetResourceCache()
	if rc == nil || rc.Nodes() == nil {
		return
	}
	nodes, err := rc.Nodes().List(labels.Everything())
	if err != nil || len(nodes) == 0 {
		return
	}
	providerIDs := make(map[string]string, len(nodes))
	for _, node := range nodes {
		if node.Spec.ProviderID != "" {
			providerIDs[node.Name] = node.Spec.ProviderID
		}
	}
	for i := range resp.Nodes {
		if providerID, ok := providerIDs[resp.Nodes[i].Name]; ok {
			resp.Nodes[i].ProviderID = providerID
		}
	}
}
