package cnpg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	auth "github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	historySourcePrometheus = "prometheus"
	historySourceNone       = "none"

	historyStateOK                = "ok"
	cnpgHistoryStateAmbiguous     = "ambiguous"
	cnpgHistoryStateScopeMismatch = "scopeMismatch"
	historyStateError             = "error"
	cnpgHistoryStateDenied        = "denied"

	fleetLagSource        = "Prometheus cnpg_pg_replication_lag, standbys only (cnpg_pg_replication_in_recovery = 1)"
	fleetReceiverSource   = "Prometheus cnpg_pg_replication_is_wal_receiver_up, standbys only (cnpg_pg_replication_in_recovery = 1)"
	fleetSlotsSource      = "Prometheus cnpg_pg_replication_slots_active = 0 for physical slots, retained WAL from cnpg_pg_replication_slots_pg_wal_lsn_diff on each reporting instance"
	fleetGrowthSource     = "Prometheus deriv(kubelet_volume_stats_used_bytes) over 6h"
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

func historyMemoGet(key string, now time.Time) ([]prometheuspkg.CNPGHistoryChart, bool) {
	cnpgHistoryMemoMu.Lock()
	defer cnpgHistoryMemoMu.Unlock()
	e, ok := cnpgHistoryMemo[key]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.charts, true
}

func historyMemoPut(key string, now time.Time, charts []prometheuspkg.CNPGHistoryChart) {
	cnpgHistoryMemoMu.Lock()
	defer cnpgHistoryMemoMu.Unlock()
	for k, e := range cnpgHistoryMemo {
		if now.After(e.expires) {
			delete(cnpgHistoryMemo, k)
		}
	}
	cnpgHistoryMemo[key] = cnpgHistoryMemoEntry{expires: now.Add(cnpgHistoryMemoTTL), charts: charts}
}

// cnpgPrometheusUnavailable returns why Radar has no Prometheus to read, or
// "" when it does.
// Discovery's own failures are complete diagnoses, so they are not prefixed again.
func cnpgNoPrometheusReason(msg string) string {
	switch {
	case msg == "":
		return "Radar is not connected to Prometheus"
	case strings.HasPrefix(msg, "Radar "), strings.HasPrefix(msg, "No working Prometheus endpoint found"):
		return truncateCNPGRuntimeError(msg)
	}
	return "Radar is not connected to Prometheus: " + truncateCNPGRuntimeError(msg)
}

func historyScopeFailure(err error) (string, string) {
	switch {
	case errors.Is(err, prometheuspkg.ErrScopeAmbiguous):
		return cnpgHistoryStateAmbiguous, "This Prometheus holds series for these Pod names under more than one cluster identity, so history could mix clusters. An operator can configure the cluster identity labels Radar should require."
	case errors.Is(err, prometheuspkg.ErrScopeMismatch):
		return cnpgHistoryStateScopeMismatch, "The cluster identity labels proven for this cluster do not appear on the CNPG exporter series, so Radar cannot tell this cluster's history from another's."
	}
	return historyStateError, "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())
}

// cnpgUsageScopeFailure is cnpgHistoryScopeFailure for kubelet volume stats.
func usageScopeFailure(err error) (string, string) {
	switch {
	case errors.Is(err, prometheuspkg.ErrScopeAmbiguous):
		return cnpgHistoryStateAmbiguous, "This Prometheus holds volume stats for these claim names under more than one cluster identity, so a value could be another cluster's. An operator can configure the cluster identity labels Radar should require."
	case errors.Is(err, prometheuspkg.ErrScopeMismatch):
		return cnpgHistoryStateScopeMismatch, "The cluster identity labels proven for this cluster do not appear on these claims' volume stats, so Radar cannot tell this cluster's volumes from another's."
	}
	return historyStateError, "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())
}

// cnpgHistoryAnchors are current instance Pods, name and UID, that prove
// which cluster-identity labels are this cluster's.
func historyAnchors(cache *k8s.ResourceCache, cluster *unstructured.Unstructured) []prom.WorkloadPodIdentity {
	pods, err := clusterInstancePods(cache, cluster)
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

// CNPGFleetMetricsResponse is GET /api/cnpg/fleet-metrics: per visible
// Cluster, its largest current standby replay lag with its standbys' WAL
// receivers, its inactive physical replication slots and the growth of its
// fastest-growing volume, all from Prometheus.
type CNPGFleetMetricsResponse struct {
	SampledAt      string                    `json:"sampledAt"`
	Source         string                    `json:"source"`
	Reason         string                    `json:"reason,omitempty"`
	LagSource      string                    `json:"lagSource"`
	ReceiverSource string                    `json:"receiverSource"`
	SlotsSource    string                    `json:"slotsSource"`
	GrowthSource   string                    `json:"growthSource"`
	Clusters       []CNPGClusterFleetMetrics `json:"clusters"`
}

type CNPGClusterFleetMetrics struct {
	Namespace string          `json:"namespace"`
	Name      string          `json:"name"`
	Lag       CNPGFleetLag    `json:"lag"`
	Slots     CNPGFleetSlots  `json:"slots"`
	Growth    CNPGFleetGrowth `json:"growth"`
}

// CNPGFleetLag State: ok (Seconds is the largest standby lag), noStandby
// (scraped, but no instance is in recovery), noSeries, denied, ambiguous,
// scopeMismatch, error or notRead.
type CNPGFleetLag struct {
	State   string      `json:"state"`
	Grant   *auth.Grant `json:"grant,omitempty"`
	Reason  string      `json:"reason,omitempty"`
	Seconds *float64    `json:"seconds,omitempty"`
	Pod     string      `json:"pod,omitempty"`
	// LagStandbys counts the standbys whose replay lag was read; Seconds
	// covers only these.
	LagStandbys *int `json:"lagStandbys,omitempty"`
	// SustainedSeconds is the worst standby's lowest recorded lag over
	// SustainedWindow, for a standby already reporting when it began.
	SustainedSeconds *float64 `json:"sustainedSeconds,omitempty"`
	SustainedPod     string   `json:"sustainedPod,omitempty"`
	SustainedWindow  string   `json:"sustainedWindow,omitempty"`
	// Set only with State ok. Standbys counts instances reporting
	// cnpg_pg_replication_in_recovery = 1; Receiving those whose WAL receiver
	// is up, ReceiverDown those whose is not. A standby whose receiver is down
	// can still read lag 0, so receiving is never inferred from lag: when no
	// standby reports the receiver, ReceiverUnknown is set instead, with
	// ReceiverReason.
	Standbys        *int     `json:"standbys,omitempty"`
	Receiving       *int     `json:"receiving,omitempty"`
	ReceiverDown    []string `json:"receiverDown,omitempty"`
	ReceiverUnknown bool     `json:"receiverUnknown,omitempty"`
	ReceiverReason  string   `json:"receiverReason,omitempty"`
	// ReceiverDownSustained lists the standbys whose receiver was down in every
	// sample over ReceiverDownWindow; only these raise a problem.
	ReceiverDownSustained []string `json:"receiverDownSustained,omitempty"`
	ReceiverDownWindow    string   `json:"receiverDownWindow,omitempty"`
	// Isolation says how the series were tied to this cluster.
	Isolation *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

// CNPGFleetSlots State: ok (Inactive lists the inactive physical slots,
// empty when none is), noSeries, denied, ambiguous, scopeMismatch, error or
// notRead. Inactive is null unless State is ok.
type CNPGFleetSlots struct {
	State     string                         `json:"state"`
	Grant     *auth.Grant                    `json:"grant,omitempty"`
	Reason    string                         `json:"reason,omitempty"`
	Inactive  []CNPGFleetSlot                `json:"inactive"`
	Omitted   int                            `json:"omitted,omitempty"`
	Isolation *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

// CNPGFleetSlot is one inactive physical slot as one instance reports it.
// Role is that instance's (primary or standby), absent when it reports no
// recovery state; CloudNativePG copies HA slots to standbys, where they are
// never active. Bytes is the WAL the slot retains there, null when not
// reported.
type CNPGFleetSlot struct {
	Slot  string   `json:"slot"`
	Pod   string   `json:"pod"`
	Role  string   `json:"role,omitempty"`
	Bytes *float64 `json:"bytes"`
}

// CNPGFleetGrowth State: ok (BytesPerHour of the fastest-growing claim),
// noSeries, denied, unavailable, error or notRead.
type CNPGFleetGrowth struct {
	State        string                         `json:"state"`
	Grant        *auth.Grant                    `json:"grant,omitempty"`
	Reason       string                         `json:"reason,omitempty"`
	BytesPerHour *float64                       `json:"bytesPerHour,omitempty"`
	Claim        string                         `json:"claim,omitempty"`
	Instance     string                         `json:"instance,omitempty"`
	Isolation    *prometheuspkg.SeriesIsolation `json:"isolation,omitempty"`
}

func (s *Reader) namespaceFleetMetrics(callerCtx context.Context, cache *k8s.ResourceCache, namespace string, clusters []*unstructured.Unstructured) []CNPGClusterFleetMetrics {
	ctx := callerCtx
	names := make([]string, len(clusters))
	var anchors []prom.WorkloadPodIdentity
	for i, c := range clusters {
		names[i] = c.GetName()
		if len(anchors) < cnpgHistoryAnchorCap {
			anchors = append(anchors, historyAnchors(cache, c)...)
		}
	}
	lags := make([]CNPGFleetLag, len(clusters))
	slots := make([]CNPGFleetSlots, len(clusters))
	growths := make([]CNPGFleetGrowth, len(clusters))
	fillLag := func(l CNPGFleetLag) {
		for i := range lags {
			lags[i] = l
		}
	}
	fillSlots := func(sl CNPGFleetSlots) {
		for i := range slots {
			slots[i] = sl
		}
	}
	fillGrowth := func(g CNPGFleetGrowth) {
		for i := range growths {
			growths[i] = g
		}
	}

	podsAllowed := s.Access.MetricsRead(callerCtx, "", "pods", namespace, "get")
	claimsByCluster, growthCov := s.fleetClaims(callerCtx, cache, namespace, clusters)

	matchers, scopeErr := "", error(nil)
	var lagIso prometheuspkg.SeriesIsolation
	if podsAllowed {
		matchers, lagIso, scopeErr = s.Metrics.CNPGScope(ctx, namespace, prometheuspkg.CNPGInstancesSelector(namespace, names), anchors, prometheuspkg.CNPGSustainedLagWindow)
	}

	if !podsAllowed {
		fillLag(CNPGFleetLag{State: cnpgHistoryStateDenied, Grant: grantGetPods.In(namespace).Ref()})
	} else if scopeErr != nil {
		state, reason := historyScopeFailure(scopeErr)
		fillLag(CNPGFleetLag{State: state, Reason: reason})
	} else if res, err := s.Metrics.FleetLag(ctx, namespace, names, matchers); err != nil {
		fillLag(CNPGFleetLag{State: historyStateError, Reason: "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())})
	} else {
		for i, c := range clusters {
			switch reading, ok := res.Lag[c.GetName()]; {
			case ok:
				v, n := reading.Seconds, reading.Reporting
				lags[i] = CNPGFleetLag{State: historyStateOK, Seconds: &v, Pod: reading.Pod, LagStandbys: &n, Isolation: &lagIso}
				if sus, ok := res.Sustained[c.GetName()]; ok {
					sv := sus.Seconds
					lags[i].SustainedSeconds, lags[i].SustainedPod, lags[i].SustainedWindow = &sv, sus.Pod, prometheuspkg.CNPGSustainedLagWindow.String()
				}
				cnpgFleetReceivers(&lags[i], res, c.GetName())
			case res.Scraped[c.GetName()]:
				lags[i] = CNPGFleetLag{State: "noStandby", Reason: "no instance reports being a standby"}
			default:
				lags[i] = CNPGFleetLag{State: cnpgUsageStateNoSeries, Reason: "Prometheus has no CNPG exporter series for this cluster's instances"}
			}
		}
	}

	if !podsAllowed {
		fillSlots(CNPGFleetSlots{State: cnpgHistoryStateDenied, Grant: grantGetPods.In(namespace).Ref()})
	} else if scopeErr != nil {
		state, reason := historyScopeFailure(scopeErr)
		fillSlots(CNPGFleetSlots{State: state, Reason: reason})
	} else if res, err := s.Metrics.FleetSlots(ctx, namespace, names, matchers); err != nil {
		fillSlots(CNPGFleetSlots{State: historyStateError, Reason: "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())})
	} else {
		for i, c := range clusters {
			slots[i] = cnpgFleetSlotsOf(res, c.GetName(), &lagIso)
		}
	}

	if growthCov.State != "" {
		fillGrowth(growthCov)
	} else {
		var all []string
		for _, cs := range claimsByCluster {
			all = append(all, claimNames(cs)...)
		}
		pvcMatchers, pvcIso, err := s.Metrics.PVCScope(ctx, namespace, all, anchors, cnpgFleetGrowthWindow)
		var byClaim map[string]float64
		if err != nil {
			state, reason := usageScopeFailure(err)
			fillGrowth(CNPGFleetGrowth{State: state, Reason: reason})
		} else if byClaim, err = s.Metrics.DiskGrowth(ctx, namespace, all, cnpgFleetGrowthWindow, pvcMatchers); err != nil {
			fillGrowth(CNPGFleetGrowth{State: historyStateError, Reason: "Prometheus query failed: " + truncateCNPGRuntimeError(err.Error())})
		}
		for i, c := range clusters {
			if err != nil {
				break
			}
			owned := claimsByCluster[c.GetName()]
			if len(owned) == 0 {
				growths[i] = CNPGFleetGrowth{State: usageStateNotRead, Reason: "no claims owned by this cluster"}
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
					g = CNPGFleetGrowth{State: historyStateOK, BytesPerHour: &val, Claim: pvc.Name, Instance: pvc.Labels[instanceNameLabel], Isolation: &pvcIso}
				}
			}
			growths[i] = g
		}
	}
	return cnpgFleetMetricsRows(namespace, clusters, lags, slots, growths, claimsByCluster)
}

// cnpgFleetReceivers adds a standby-reporting Cluster's WAL receiver evidence
// to its lag reading.
func cnpgFleetReceivers(l *CNPGFleetLag, res prometheuspkg.CNPGFleetLag, cluster string) {
	rec, ok := res.Receivers[cluster]
	switch {
	case res.ReceiversError != "":
		l.ReceiverUnknown, l.ReceiverReason = true, res.ReceiversError
	case !ok:
		l.ReceiverUnknown, l.ReceiverReason = true, "no instance reported being a standby when the WAL receivers were read"
	case rec.Unknown:
		standbys := rec.Standbys
		l.Standbys = &standbys
		l.ReceiverUnknown, l.ReceiverReason = true, "the exporter does not report cnpg_pg_replication_is_wal_receiver_up for this cluster's standbys (custom monitoring queries)"
	default:
		standbys, receiving := rec.Standbys, rec.Receiving
		l.Standbys, l.Receiving, l.ReceiverDown = &standbys, &receiving, rec.Down
		if res.ReceiversDownSustained != nil {
			l.ReceiverDownSustained, l.ReceiverDownWindow = res.ReceiversDownSustained[cluster], prometheuspkg.CNPGReceiverDownWindow.String()
		}
	}
}

func cnpgFleetSlotsOf(res prometheuspkg.CNPGFleetSlots, cluster string, iso *prometheuspkg.SeriesIsolation) CNPGFleetSlots {
	cs, ok := res.Clusters[cluster]
	switch {
	case ok:
		out := CNPGFleetSlots{State: historyStateOK, Inactive: make([]CNPGFleetSlot, 0, len(cs.Inactive)), Omitted: cs.Omitted, Isolation: iso}
		for _, s := range cs.Inactive {
			out.Inactive = append(out.Inactive, CNPGFleetSlot{Slot: s.Slot, Pod: s.Pod, Role: s.Role, Bytes: s.Bytes})
		}
		return out
	case res.Scraped[cluster]:
		return CNPGFleetSlots{State: cnpgUsageStateNoSeries, Reason: "no instance of this cluster reports a replication slot: it has none, or its monitoring queries leave pg_replication_slots out"}
	default:
		return CNPGFleetSlots{State: cnpgUsageStateNoSeries, Reason: "Prometheus has no CNPG exporter series for this cluster's instances"}
	}
}

// cnpgFleetClaims lists each Cluster's owned claims under the caller's grants.
// A non-empty returned State says why growth is not read for the namespace.
func (s *Reader) fleetClaims(ctx context.Context, cache *k8s.ResourceCache, namespace string, clusters []*unstructured.Unstructured) (map[string][]*corev1.PersistentVolumeClaim, CNPGFleetGrowth) {
	if !s.Access.CanRead(ctx, "", "persistentvolumeclaims", namespace, "list") {
		return nil, CNPGFleetGrowth{State: storageStateDenied, Grant: cnpgGrantListPVCs.In(namespace).Ref()}
	}
	if !s.Access.MetricsRead(ctx, "", "persistentvolumeclaims", namespace, "get") {
		return nil, CNPGFleetGrowth{State: storageStateDenied, Grant: grantGetPVCs.In(namespace).Ref()}
	}
	req, err := labels.NewRequirement(clusterLabel, selection.Exists, nil)
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
			if pvc.Labels[clusterLabel] == c.GetName() {
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

func cnpgFleetMetricsRows(namespace string, clusters []*unstructured.Unstructured, lags []CNPGFleetLag, slots []CNPGFleetSlots, growths []CNPGFleetGrowth, claimsByCluster map[string][]*corev1.PersistentVolumeClaim) []CNPGClusterFleetMetrics {
	out := make([]CNPGClusterFleetMetrics, len(clusters))
	for i, c := range clusters {
		createdAt := c.GetCreationTimestamp().Time
		if !createdAt.IsZero() {
			age := time.Since(createdAt)
			if age < prometheuspkg.CNPGMetricLookback {
				if lags[i].State == historyStateOK || lags[i].State == "noStandby" {
					lags[i] = CNPGFleetLag{State: usageStateNotRead, Reason: "Waiting for exporter samples after this Cluster was created"}
				}
				if slots[i].State == historyStateOK {
					slots[i] = CNPGFleetSlots{State: usageStateNotRead, Reason: "Waiting for exporter samples after this Cluster was created"}
				}
			}
			if age < prometheuspkg.CNPGSustainedLagWindow+prometheuspkg.CNPGMetricLookback {
				lags[i].SustainedSeconds, lags[i].SustainedPod, lags[i].SustainedWindow = nil, "", ""
			}
			if age < prometheuspkg.CNPGReceiverDownWindow+prometheuspkg.CNPGMetricLookback {
				lags[i].ReceiverDownSustained, lags[i].ReceiverDownWindow = nil, ""
			}
			if growths[i].State == historyStateOK && age < cnpgFleetGrowthWindow {
				growths[i] = CNPGFleetGrowth{State: usageStateNotRead, Reason: "Waiting for a full volume-growth window after this Cluster was created"}
			}
		}
		if growths[i].State == historyStateOK {
			for _, pvc := range claimsByCluster[c.GetName()] {
				if !pvc.CreationTimestamp.IsZero() && time.Since(pvc.CreationTimestamp.Time) < cnpgFleetGrowthWindow {
					growths[i] = CNPGFleetGrowth{State: usageStateNotRead, Reason: "Waiting for a full volume-growth window after this Cluster's PVCs were created"}
					break
				}
			}
		}
		out[i] = CNPGClusterFleetMetrics{Namespace: namespace, Name: c.GetName(), Lag: lags[i], Slots: slots[i], Growth: growths[i]}
	}
	return out
}

func (s *Reader) ClusterHistory(ctx context.Context, cache *k8s.ResourceCache, cluster *unstructured.Unstructured, rng prometheuspkg.CNPGHistoryRange) CNPGClusterHistoryResponse {
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	now := time.Now()
	resp := CNPGClusterHistoryResponse{
		Cluster:   CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		Range:     rng.Name,
		SampledAt: now.UTC().Format(time.RFC3339),
		Charts:    []prometheuspkg.CNPGHistoryChart{},
	}
	if reason := s.prometheusUnavailable(ctx); reason != "" {
		resp.Source, resp.Reason = historySourceNone, reason
		return resp
	}
	resp.Source = historySourcePrometheus
	start, end := rng.Bounds(now, cluster.GetCreationTimestamp().Time)
	resp.Start, resp.End = start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
	if start.After(end) {
		resp.State, resp.Reason = "pending", "This Cluster is too new for isolated history. Waiting for samples after its creation and the query lookback window."
		return resp
	}
	if start.After(end.Add(-rng.Duration)) {
		resp.Reason = "History is limited to this Cluster incarnation; the first query lookback window after creation is omitted to exclude samples from reused Pod names."
	}
	resp.StepSeconds = int(rng.Step / time.Second)
	selector := prometheuspkg.CNPGInstanceSelector(namespace, name)
	resp.Selector = selector

	req := prometheuspkg.CNPGHistoryRequest{Namespace: namespace, Cluster: name, Range: rng, End: end, CreatedAt: cluster.GetCreationTimestamp().Time}
	if !s.Access.MetricsRead(ctx, "", "pods", namespace, "get") {
		req.PodsDenied = grantGetPods.In(namespace).Ref()
	}
	claims, _, claimCov := s.clusterClaims(ctx, cache, cluster)
	switch {
	case claimCov.State == storageStateDenied:
		req.PVCDenied = claimCov.Grant
	case claimCov.State != storageStateOK:
		req.PVCReason = claimCov.Reason
	case !s.Access.MetricsRead(ctx, "", "persistentvolumeclaims", namespace, "get"):
		req.PVCDenied = grantGetPVCs.In(namespace).Ref()
	default:
		req.Claims = claimNames(claims)
	}

	anchors := historyAnchors(cache, cluster)
	if req.PodsDenied == nil {
		matchers, iso, err := s.Metrics.CNPGScope(ctx, namespace, selector, anchors, rng.Duration)
		if err != nil {
			resp.State, resp.Reason = historyScopeFailure(err)
			return resp
		}
		req.Matchers, resp.Isolation = matchers, &iso
	}
	if len(req.Claims) > 0 {
		matchers, iso, err := s.Metrics.PVCScope(ctx, namespace, req.Claims, anchors, rng.Duration)
		if err != nil {
			req.PVCScopeState, req.PVCReason = usageScopeFailure(err)
		} else {
			req.PVCMatchers, resp.PVCIsolation = matchers, &iso
		}
	}

	key := strings.Join([]string{namespace, name, string(cluster.GetUID()), rng.Name, end.Format(time.RFC3339), req.Matchers, req.PVCMatchers, req.PVCScopeState, integration.GrantText(req.PodsDenied), integration.GrantText(req.PVCDenied), req.PVCReason, strings.Join(req.Claims, ",")}, "\x00")
	charts, hit := historyMemoGet(key, now)
	if !hit {
		var err error
		charts, err = s.Metrics.History(ctx, req)
		if err != nil {
			resp.State, resp.Reason = historyStateError, err.Error()
			return resp
		}
		historyMemoPut(key, now, charts)
	}
	resp.State, resp.Charts = historyStateOK, charts
	return resp

}

func (s *Reader) FleetMetrics(ctx context.Context, cache *k8s.ResourceCache, namespaces []string) CNPGFleetMetricsResponse {
	resp := CNPGFleetMetricsResponse{
		SampledAt: time.Now().UTC().Format(time.RFC3339), Source: historySourcePrometheus,
		LagSource: fleetLagSource, ReceiverSource: fleetReceiverSource, SlotsSource: fleetSlotsSource,
		GrowthSource: fleetGrowthSource, Clusters: []CNPGClusterFleetMetrics{},
	}
	if reason := s.prometheusUnavailable(ctx); reason != "" {
		resp.Source, resp.Reason = historySourceNone, reason
		return resp
	}

	var clusterKind integration.WorkspaceKind
	for _, k := range workspaceKinds {
		if k.Key == workspaceClusterKey {
			clusterKind = k
		}
	}
	acc, clusters := s.workspaceReadKind(ctx, cache, clusterKind, namespaces)
	if acc.State != integration.KindCoverageFull && acc.State != integration.KindCoveragePartial {
		return resp
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

	read := min(len(nsList), fleetDiskMaxNamespaces)
	results := integration.FanOut(ctx, read, fleetDiskConcurrency, func(i int) []CNPGClusterFleetMetrics {
		return s.namespaceFleetMetrics(ctx, cache, nsList[i], byNamespace[nsList[i]])
	})
	for _, rs := range results {
		resp.Clusters = append(resp.Clusters, rs...)
	}
	reason := fmt.Sprintf("read for at most %d namespaces at a time; narrow the namespace filter", fleetDiskMaxNamespaces)
	for _, ns := range nsList[read:] {
		for _, c := range byNamespace[ns] {
			resp.Clusters = append(resp.Clusters, CNPGClusterFleetMetrics{Namespace: ns, Name: c.GetName(),
				Lag: CNPGFleetLag{State: usageStateNotRead, Reason: reason}, Slots: CNPGFleetSlots{State: usageStateNotRead, Reason: reason},
				Growth: CNPGFleetGrowth{State: usageStateNotRead, Reason: reason}})
		}
	}
	sort.Slice(resp.Clusters, func(i, j int) bool {
		if resp.Clusters[i].Namespace != resp.Clusters[j].Namespace {
			return resp.Clusters[i].Namespace < resp.Clusters[j].Namespace
		}
		return resp.Clusters[i].Name < resp.Clusters[j].Name
	})
	return resp

}
