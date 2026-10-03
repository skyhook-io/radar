package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	cnpgHistorySourcePrometheus = "prometheus"
	cnpgHistorySourceNone       = "none"

	cnpgHistoryStateOK            = "ok"
	cnpgHistoryStateAmbiguous     = "ambiguous"
	cnpgHistoryStateScopeMismatch = "scopeMismatch"
	cnpgHistoryStateError         = "error"
	cnpgHistoryStateDenied        = "denied"

	cnpgFleetLagSource    = "Prometheus cnpg_pg_replication_lag, standbys only (cnpg_pg_replication_in_recovery = 1)"
	cnpgFleetGrowthSource = "Prometheus deriv(kubelet_volume_stats_used_bytes) over 6h"
	cnpgFleetGrowthWindow = 6 * time.Hour
	cnpgHistoryAnchorCap  = 100
)

var cnpgHistoryMemoTTL = 15 * time.Second

// CNPGClusterHistoryResponse is GET /api/cnpg/clusters/{ns}/{name}/history.
// Source "none" means Radar has no Prometheus; the UI then keeps its own
// in-browser samples. State describes the query as a whole; each chart
// carries its own state when the whole succeeded.
type CNPGClusterHistoryResponse struct {
	Cluster     CNPGRuntimeObjectRef           `json:"cluster"`
	Source      string                         `json:"source"`
	State       string                         `json:"state,omitempty"`
	Reason      string                         `json:"reason,omitempty"`
	Range       string                         `json:"range"`
	Start       string                         `json:"start,omitempty"`
	End         string                         `json:"end,omitempty"`
	StepSeconds int                            `json:"stepSeconds,omitempty"`
	Selector    string                         `json:"selector,omitempty"`
	Isolation   *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
	// PVCIsolation is the volume chart's own: claims are matched apart from Pods.
	PVCIsolation *prometheuspkg.SeriesIsolation   `json:"pvcIsolation,omitempty"`
	SampledAt    string                           `json:"sampledAt"`
	Charts       []prometheuspkg.CNPGHistoryChart `json:"charts"`
}

type cnpgHistoryMemoEntry struct {
	expires time.Time
	charts  []prometheuspkg.CNPGHistoryChart
}

var (
	cnpgHistoryMemoMu sync.Mutex
	cnpgHistoryMemo   = map[string]cnpgHistoryMemoEntry{}
)

func cnpgHistoryMemoGet(key string, now time.Time) ([]prometheuspkg.CNPGHistoryChart, bool) {
	cnpgHistoryMemoMu.Lock()
	defer cnpgHistoryMemoMu.Unlock()
	e, ok := cnpgHistoryMemo[key]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.charts, true
}

func cnpgHistoryMemoPut(key string, now time.Time, charts []prometheuspkg.CNPGHistoryChart) {
	cnpgHistoryMemoMu.Lock()
	defer cnpgHistoryMemoMu.Unlock()
	for k, e := range cnpgHistoryMemo {
		if now.After(e.expires) {
			delete(cnpgHistoryMemo, k)
		}
	}
	cnpgHistoryMemo[key] = cnpgHistoryMemoEntry{expires: now.Add(cnpgHistoryMemoTTL), charts: charts}
}

func (s *Server) handleCNPGClusterHistory(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.requireConnected(w) {
		return
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.canRead(r, cnpgGroup, "clusters", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to clusters.postgresql.cnpg.io in namespace "+namespace)
		return
	}
	rng, ok := prometheuspkg.ParseCNPGHistoryRange(r.URL.Query().Get("range"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid range "+r.URL.Query().Get("range")+" (expected 15m, 1h, 6h or 24h)")
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	cluster, ok := s.loadCNPGCluster(w, r, cache, namespace, name)
	if !ok {
		return
	}

	now := time.Now()
	resp := CNPGClusterHistoryResponse{
		Cluster:   CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		Range:     rng.Name,
		SampledAt: now.UTC().Format(time.RFC3339),
		Charts:    []prometheuspkg.CNPGHistoryChart{},
	}
	if reason := cnpgPrometheusUnavailable(r.Context()); reason != "" {
		resp.Source, resp.Reason = cnpgHistorySourceNone, reason
		s.writeJSON(w, resp)
		return
	}
	resp.Source = cnpgHistorySourcePrometheus
	end := now.Truncate(rng.Step)
	resp.Start, resp.End = end.Add(-rng.Duration).UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
	resp.StepSeconds = int(rng.Step / time.Second)
	selector := prometheuspkg.CNPGInstanceSelector(namespace, name)
	resp.Selector = selector

	req := prometheuspkg.CNPGHistoryRequest{Namespace: namespace, Cluster: name, Range: rng, End: end}
	if !s.prometheusAuthGate(r, "", "pods", namespace, "get") {
		req.PodsDenied = "get pods in " + namespace
	}
	claims, _, claimCov := s.cnpgClusterClaims(r, cache, cluster)
	switch {
	case claimCov.State == cnpgStorageStateDenied:
		req.PVCDenied = claimCov.Grant
	case claimCov.State != cnpgStorageStateOK:
		req.PVCReason = claimCov.Reason
	case !s.prometheusAuthGate(r, "", "persistentvolumeclaims", namespace, "get"):
		req.PVCDenied = "get persistentvolumeclaims in " + namespace
	default:
		req.Claims = claimNames(claims)
	}

	anchors := cnpgHistoryAnchors(cache, cluster)
	if req.PodsDenied == "" {
		matchers, iso, err := prometheuspkg.ResolveCNPGScope(r.Context(), namespace, selector, anchors, rng.Duration)
		if err != nil {
			resp.State, resp.Reason = cnpgHistoryScopeFailure(err)
			s.writeJSON(w, resp)
			return
		}
		req.Matchers, resp.Isolation = matchers, &iso
	}
	if len(req.Claims) > 0 {
		matchers, iso, err := prometheuspkg.ResolvePVCScope(r.Context(), namespace, req.Claims, anchors, rng.Duration)
		if err != nil {
			_, req.PVCAmbiguous = cnpgUsageScopeFailure(err)
		} else {
			req.PVCMatchers, resp.PVCIsolation = matchers, &iso
		}
	}

	key := strings.Join([]string{namespace, name, string(cluster.GetUID()), rng.Name, end.Format(time.RFC3339), req.Matchers, req.PVCMatchers, req.PVCAmbiguous, req.PodsDenied, req.PVCDenied, req.PVCReason, strings.Join(req.Claims, ",")}, "\x00")
	charts, hit := cnpgHistoryMemoGet(key, now)
	if !hit {
		var err error
		charts, err = prometheuspkg.QueryCNPGHistory(r.Context(), req)
		if err != nil {
			resp.State, resp.Reason = cnpgHistoryStateError, err.Error()
			s.writeJSON(w, resp)
			return
		}
		cnpgHistoryMemoPut(key, now, charts)
	}
	resp.State, resp.Charts = cnpgHistoryStateOK, charts
	s.writeJSON(w, resp)
}

// cnpgPrometheusUnavailable returns why Radar has no Prometheus to read, or
// "" when it does.
func cnpgPrometheusUnavailable(ctx context.Context) string {
	client := prometheuspkg.GetClient()
	if client == nil {
		return cnpgNoPrometheusReason("")
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		return cnpgNoPrometheusReason(err.Error())
	}
	return ""
}

// cnpgNoPrometheusReason is one sentence for why Radar has no Prometheus.
// Discovery's own failures are already complete sentences ("Radar found 2
// services that may be Prometheus but …"), so they are not prefixed again.
func cnpgNoPrometheusReason(msg string) string {
	switch {
	case msg == "":
		return "Radar is not connected to Prometheus"
	case strings.HasPrefix(msg, "Radar "):
		return truncateCNPGRuntimeError(msg)
	}
	return "Radar is not connected to Prometheus: " + truncateCNPGRuntimeError(msg)
}

func cnpgHistoryScopeFailure(err error) (string, string) {
	switch {
	case errors.Is(err, prometheuspkg.ErrScopeAmbiguous):
		return cnpgHistoryStateAmbiguous, "This Prometheus holds series for these Pod names under more than one cluster identity, so history could mix clusters. An operator can configure the cluster identity labels Radar should require."
	case errors.Is(err, prometheuspkg.ErrScopeMismatch):
		return cnpgHistoryStateScopeMismatch, "The cluster identity labels proven for this cluster do not appear on the CNPG exporter series, so Radar cannot tell this cluster's history from another's."
	}
	return cnpgHistoryStateError, "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())
}

// cnpgUsageScopeFailure is cnpgHistoryScopeFailure for kubelet volume stats.
func cnpgUsageScopeFailure(err error) (string, string) {
	switch {
	case errors.Is(err, prometheuspkg.ErrScopeAmbiguous):
		return cnpgHistoryStateAmbiguous, "This Prometheus holds volume stats for these claim names under more than one cluster identity, so a value could be another cluster's. An operator can configure the cluster identity labels Radar should require."
	case errors.Is(err, prometheuspkg.ErrScopeMismatch):
		return cnpgHistoryStateScopeMismatch, "The cluster identity labels proven for this cluster do not appear on these claims' volume stats, so Radar cannot tell this cluster's volumes from another's."
	}
	return cnpgHistoryStateError, "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())
}

// cnpgHistoryAnchors are current instance Pods, name and UID, that prove
// which cluster-identity labels are this cluster's.
func cnpgHistoryAnchors(cache *k8s.ResourceCache, cluster *unstructured.Unstructured) []prom.WorkloadPodIdentity {
	pods, err := cnpgClusterInstancePods(cache, cluster)
	if err != nil {
		return nil
	}
	return cnpgPodIdentities(pods)
}

func cnpgPodIdentities(pods []*corev1.Pod) []prom.WorkloadPodIdentity {
	out := make([]prom.WorkloadPodIdentity, 0, min(len(pods), cnpgHistoryAnchorCap))
	for _, p := range pods {
		if len(out) == cnpgHistoryAnchorCap {
			break
		}
		out = append(out, prom.WorkloadPodIdentity{Name: p.Name, UID: string(p.UID)})
	}
	return out
}

// ---------- fleet ----------

// CNPGFleetMetricsResponse is GET /api/cnpg/fleet-metrics: per visible
// Cluster, its largest current standby replay lag and the growth of its
// fastest-growing volume, both from Prometheus.
type CNPGFleetMetricsResponse struct {
	SampledAt    string                    `json:"sampledAt"`
	Source       string                    `json:"source"`
	Reason       string                    `json:"reason,omitempty"`
	LagSource    string                    `json:"lagSource"`
	GrowthSource string                    `json:"growthSource"`
	Clusters     []CNPGClusterFleetMetrics `json:"clusters"`
}

type CNPGClusterFleetMetrics struct {
	Namespace string          `json:"namespace"`
	Name      string          `json:"name"`
	Lag       CNPGFleetLag    `json:"lag"`
	Growth    CNPGFleetGrowth `json:"growth"`
}

// CNPGFleetLag State: ok (Seconds is the largest standby lag), noStandby
// (scraped, but no instance is in recovery), noSeries, denied, ambiguous,
// scopeMismatch, error or notRead.
type CNPGFleetLag struct {
	State   string   `json:"state"`
	Grant   string   `json:"grant,omitempty"`
	Reason  string   `json:"reason,omitempty"`
	Seconds *float64 `json:"seconds,omitempty"`
	Pod     string   `json:"pod,omitempty"`
	// SustainedSeconds is the worst standby's lowest recorded lag over
	// SustainedWindow, for a standby already reporting when it began.
	SustainedSeconds *float64 `json:"sustainedSeconds,omitempty"`
	SustainedPod     string   `json:"sustainedPod,omitempty"`
	SustainedWindow  string   `json:"sustainedWindow,omitempty"`
	// Isolation says how the series were tied to this cluster.
	Isolation *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

// CNPGFleetGrowth State: ok (BytesPerHour of the fastest-growing claim),
// noSeries, denied, unavailable, error or notRead.
type CNPGFleetGrowth struct {
	State        string                         `json:"state"`
	Grant        string                         `json:"grant,omitempty"`
	Reason       string                         `json:"reason,omitempty"`
	BytesPerHour *float64                       `json:"bytesPerHour,omitempty"`
	Claim        string                         `json:"claim,omitempty"`
	Instance     string                         `json:"instance,omitempty"`
	Isolation    *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

func (s *Server) handleCNPGFleetMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	resp := CNPGFleetMetricsResponse{
		SampledAt: time.Now().UTC().Format(time.RFC3339), Source: cnpgHistorySourcePrometheus,
		LagSource: cnpgFleetLagSource, GrowthSource: cnpgFleetGrowthSource, Clusters: []CNPGClusterFleetMetrics{},
	}
	if reason := cnpgPrometheusUnavailable(r.Context()); reason != "" {
		resp.Source, resp.Reason = cnpgHistorySourceNone, reason
		s.writeJSON(w, resp)
		return
	}

	var clusterKind workspaceKind
	for _, k := range cnpgWorkspaceKinds {
		if k.key == cnpgWorkspaceClusterKey {
			clusterKind = k
		}
	}
	acc, clusters := s.cnpgWorkspaceReadKind(r, cache, clusterKind, namespaces)
	if acc.state != kindCoverageFull && acc.state != kindCoveragePartial {
		s.writeJSON(w, resp)
		return
	}
	byNamespace := map[string][]*unstructured.Unstructured{}
	for _, c := range clusters {
		byNamespace[c.GetNamespace()] = append(byNamespace[c.GetNamespace()], c)
	}
	nsList := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		nsList = append(nsList, ns)
	}
	sort.Strings(nsList)

	results := make([][]CNPGClusterFleetMetrics, len(nsList))
	sem := make(chan struct{}, cnpgFleetDiskConcurrency)
	var wg sync.WaitGroup
	for i, ns := range nsList {
		if i >= cnpgFleetDiskMaxNamespaces {
			reason := fmt.Sprintf("read for at most %d namespaces at a time; narrow the namespace filter", cnpgFleetDiskMaxNamespaces)
			for _, c := range byNamespace[ns] {
				results[i] = append(results[i], CNPGClusterFleetMetrics{Namespace: ns, Name: c.GetName(),
					Lag: CNPGFleetLag{State: cnpgUsageStateNotRead, Reason: reason}, Growth: CNPGFleetGrowth{State: cnpgUsageStateNotRead, Reason: reason}})
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			defer func() { <-sem }()
			results[i] = s.cnpgNamespaceFleetMetrics(r, cache, ns, byNamespace[ns])
		}()
	}
	wg.Wait()
	for _, rs := range results {
		resp.Clusters = append(resp.Clusters, rs...)
	}
	sort.Slice(resp.Clusters, func(i, j int) bool {
		if resp.Clusters[i].Namespace != resp.Clusters[j].Namespace {
			return resp.Clusters[i].Namespace < resp.Clusters[j].Namespace
		}
		return resp.Clusters[i].Name < resp.Clusters[j].Name
	})
	s.writeJSON(w, resp)
}

func (s *Server) cnpgNamespaceFleetMetrics(r *http.Request, cache *k8s.ResourceCache, namespace string, clusters []*unstructured.Unstructured) []CNPGClusterFleetMetrics {
	ctx := r.Context()
	names := make([]string, len(clusters))
	var anchors []prom.WorkloadPodIdentity
	for i, c := range clusters {
		names[i] = c.GetName()
		if len(anchors) < cnpgHistoryAnchorCap {
			anchors = append(anchors, cnpgHistoryAnchors(cache, c)...)
		}
	}
	lags := make([]CNPGFleetLag, len(clusters))
	growths := make([]CNPGFleetGrowth, len(clusters))
	fillLag := func(l CNPGFleetLag) {
		for i := range lags {
			lags[i] = l
		}
	}
	fillGrowth := func(g CNPGFleetGrowth) {
		for i := range growths {
			growths[i] = g
		}
	}

	podsAllowed := s.prometheusAuthGate(r, "", "pods", namespace, "get")
	claimsByCluster, growthCov := s.cnpgFleetClaims(r, cache, namespace, clusters)

	matchers, scopeErr := "", error(nil)
	var lagIso prometheuspkg.SeriesIsolation
	if podsAllowed {
		matchers, lagIso, scopeErr = prometheuspkg.ResolveCNPGScope(ctx, namespace, prometheuspkg.CNPGInstancesSelector(namespace, names), anchors, prometheuspkg.CNPGSustainedLagWindow)
	}

	if !podsAllowed {
		fillLag(CNPGFleetLag{State: cnpgHistoryStateDenied, Grant: "get pods in " + namespace})
	} else if scopeErr != nil {
		state, reason := cnpgHistoryScopeFailure(scopeErr)
		fillLag(CNPGFleetLag{State: state, Reason: reason})
	} else if res, err := prometheuspkg.QueryCNPGFleetLag(ctx, namespace, names, matchers); err != nil {
		fillLag(CNPGFleetLag{State: cnpgHistoryStateError, Reason: "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())})
	} else {
		for i, c := range clusters {
			switch reading, ok := res.Lag[c.GetName()]; {
			case ok:
				v := reading.Seconds
				lags[i] = CNPGFleetLag{State: cnpgHistoryStateOK, Seconds: &v, Pod: reading.Pod, Isolation: &lagIso}
				if sus, ok := res.Sustained[c.GetName()]; ok {
					sv := sus.Seconds
					lags[i].SustainedSeconds, lags[i].SustainedPod, lags[i].SustainedWindow = &sv, sus.Pod, prometheuspkg.CNPGSustainedLagWindow.String()
				}
			case res.Scraped[c.GetName()]:
				lags[i] = CNPGFleetLag{State: "noStandby", Reason: "no instance reports being a standby"}
			default:
				lags[i] = CNPGFleetLag{State: cnpgUsageStateNoSeries, Reason: "Prometheus has no CNPG exporter series for this cluster's instances"}
			}
		}
	}

	if growthCov.State != "" {
		fillGrowth(growthCov)
	} else {
		var all []string
		for _, cs := range claimsByCluster {
			all = append(all, claimNames(cs)...)
		}
		pvcMatchers, pvcIso, err := prometheuspkg.ResolvePVCScope(ctx, namespace, all, anchors, cnpgFleetGrowthWindow)
		var byClaim map[string]float64
		if err != nil {
			state, reason := cnpgUsageScopeFailure(err)
			fillGrowth(CNPGFleetGrowth{State: state, Reason: reason})
		} else if byClaim, err = prometheuspkg.QueryCNPGDiskGrowth(ctx, namespace, all, cnpgFleetGrowthWindow, pvcMatchers); err != nil {
			fillGrowth(CNPGFleetGrowth{State: cnpgHistoryStateError, Reason: "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())})
		}
		for i, c := range clusters {
			if err != nil {
				break
			}
			owned := claimsByCluster[c.GetName()]
			if len(owned) == 0 {
				growths[i] = CNPGFleetGrowth{State: cnpgUsageStateNotRead, Reason: "no claims owned by this cluster"}
				continue
			}
			g := CNPGFleetGrowth{State: cnpgUsageStateNoSeries, Reason: "Prometheus has no kubelet volume stats for this cluster's claims over the last 6h"}
			for _, pvc := range owned {
				v, ok := byClaim[pvc.Name]
				if !ok {
					continue
				}
				if g.BytesPerHour == nil || v > *g.BytesPerHour {
					val := v
					g = CNPGFleetGrowth{State: cnpgHistoryStateOK, BytesPerHour: &val, Claim: pvc.Name, Instance: pvc.Labels[cnpgInstanceNameLabel], Isolation: &pvcIso}
				}
			}
			growths[i] = g
		}
	}
	return cnpgFleetMetricsRows(namespace, clusters, lags, growths)
}

// cnpgFleetClaims lists each Cluster's owned claims under the caller's grants.
// A non-empty returned State says why growth is not read for the namespace.
func (s *Server) cnpgFleetClaims(r *http.Request, cache *k8s.ResourceCache, namespace string, clusters []*unstructured.Unstructured) (map[string][]*corev1.PersistentVolumeClaim, CNPGFleetGrowth) {
	if !s.canRead(r, "", "persistentvolumeclaims", namespace, "list") {
		return nil, CNPGFleetGrowth{State: cnpgStorageStateDenied, Grant: "list persistentvolumeclaims in " + namespace}
	}
	if !s.prometheusAuthGate(r, "", "persistentvolumeclaims", namespace, "get") {
		return nil, CNPGFleetGrowth{State: cnpgStorageStateDenied, Grant: "get persistentvolumeclaims in " + namespace}
	}
	req, err := labels.NewRequirement(cnpgClusterLabel, selection.Exists, nil)
	if err != nil {
		return nil, CNPGFleetGrowth{State: cnpgStorageStateError, Reason: err.Error()}
	}
	candidates, reason := cnpgCachedClaims(cache, namespace, labels.NewSelector().Add(*req))
	if reason != "" {
		return nil, CNPGFleetGrowth{State: cnpgStorageStateUnavailable, Reason: reason}
	}
	out := map[string][]*corev1.PersistentVolumeClaim{}
	for _, c := range clusters {
		var mine []*corev1.PersistentVolumeClaim
		for _, pvc := range candidates {
			if pvc.Labels[cnpgClusterLabel] == c.GetName() {
				mine = append(mine, pvc)
			}
		}
		owned, _ := cnpgOwnedClaims(mine, c)
		if len(owned) > 0 {
			out[c.GetName()] = owned
		}
	}
	return out, CNPGFleetGrowth{}
}

func cnpgFleetMetricsRows(namespace string, clusters []*unstructured.Unstructured, lags []CNPGFleetLag, growths []CNPGFleetGrowth) []CNPGClusterFleetMetrics {
	out := make([]CNPGClusterFleetMetrics, len(clusters))
	for i, c := range clusters {
		out[i] = CNPGClusterFleetMetrics{Namespace: namespace, Name: c.GetName(), Lag: lags[i], Growth: growths[i]}
	}
	return out
}
