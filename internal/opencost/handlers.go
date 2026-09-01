package opencost

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// RegisterRoutes registers OpenCost routes on the given router. resolveScope
// is required: these handlers serve cost data pulled under Radar's own
// identity, so it is the only thing bounding a response to what the caller may
// see.
func RegisterRoutes(r chi.Router, resolveCurrency func() string, resolveScope ScopeResolver) {
	r.Get("/opencost/summary", func(w http.ResponseWriter, r *http.Request) {
		handleSummary(w, r, resolveCurrency, resolveScope)
	})
	r.Get("/opencost/workloads", func(w http.ResponseWriter, r *http.Request) {
		handleWorkloads(w, r, resolveCurrency, resolveScope)
	})
	r.Get("/opencost/trend", func(w http.ResponseWriter, r *http.Request) {
		handleTrend(w, r, resolveCurrency, resolveScope)
	})
	r.Get("/opencost/nodes", func(w http.ResponseWriter, r *http.Request) {
		handleNodes(w, r, resolveCurrency, resolveScope)
	})
}

// handleSummary returns namespace-level cost summary from OpenCost Prometheus metrics.
func handleSummary(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, resolveScope ScopeResolver) {
	scope := resolveScope(r)
	if scope.Restricted() && len(scope.Namespaces) == 0 {
		writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: pkgopencost.ReasonAccessDenied, Restricted: true, Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: pkgopencost.ReasonNoPrometheus, Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (summary): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.CostSummary{Available: false, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	currency := resolvedCurrency(resolveCurrency)
	resp := pkgopencost.ComputeCostSummaryFromProm(
		r.Context(), client.Prom(), pkgopencost.SummaryOptions{
			Currency:          currency,
			AllowedNamespaces: scope.Namespaces,
			CanReadNodes:      scope.CanReadNodes,
		})
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
func handleWorkloads(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, resolveScope ScopeResolver) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "namespace parameter is required"})
		return
	}

	scope := resolveScope(r)
	if !scope.Allows(ns) {
		writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{
			Namespace:  ns,
			Reason:     pkgopencost.ReasonAccessDenied,
			Restricted: true,
			Workloads:  []pkgopencost.WorkloadCost{},
			Currency:   resolvedCurrency(resolveCurrency),
		})
		return
	}

	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{Namespace: ns, Reason: pkgopencost.ReasonNoPrometheus, Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (workloads): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.WorkloadCostResponse{Namespace: ns, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency)})
		return
	}

	resp := pkgopencost.ComputeWorkloadsFromProm(r.Context(), client.Prom(), ns, BuildPodOwnerLookup(ns))
	resp.Currency = resolvedCurrency(resolveCurrency)
	resp.Restricted = scope.Restricted()
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
func handleTrend(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, resolveScope ScopeResolver) {
	scope := resolveScope(r)
	if scope.Restricted() && len(scope.Namespaces) == 0 {
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: pkgopencost.ReasonAccessDenied, Restricted: true, Range: pkgopencost.TrendRangeLabel(r.URL.Query().Get("range")), Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: pkgopencost.ReasonNoPrometheus, Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (trend): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.CostTrendResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	resp := pkgopencost.ComputeCostTrendFromProm(r.Context(), client.Prom(), pkgopencost.TrendPromOptions{
		Range:             r.URL.Query().Get("range"),
		AllowedNamespaces: scope.Namespaces,
	})
	resp.Currency = resolvedCurrency(resolveCurrency)
	writeJSON(w, http.StatusOK, resp)
}

// handleNodes returns per-node cost breakdown.
func handleNodes(w http.ResponseWriter, r *http.Request, resolveCurrency func() string, resolveScope ScopeResolver) {
	// Node spend is cluster-scoped — there is no namespace slice of it to
	// hand a restricted caller, so it is all or nothing.
	if !resolveScope(r).CanReadNodes {
		writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: pkgopencost.ReasonAccessDenied, Restricted: true, Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	client := prometheuspkg.GetClient()
	if client == nil {
		writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: pkgopencost.ReasonNoPrometheus, Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	if _, _, err := client.EnsureConnected(r.Context()); err != nil {
		log.Printf("[opencost] EnsureConnected failed (nodes): %v", err)
		writeJSON(w, http.StatusOK, pkgopencost.NodeCostResponse{Available: false, Reason: ConnectionFailureReason(err), Currency: resolvedCurrency(resolveCurrency)})
		return
	}
	resp := pkgopencost.ComputeNodeCosts(r.Context(), client.Prom())
	resp.Currency = resolvedCurrency(resolveCurrency)
	attachNodeProviderIDs(resp)
	writeJSON(w, http.StatusOK, resp)
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
		resp.Nodes[i].ProviderID = providerIDs[resp.Nodes[i].Name]
	}
}
