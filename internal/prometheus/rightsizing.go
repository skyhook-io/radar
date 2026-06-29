package prometheus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Sentinel errors distinguish the cache-loading failure modes so handlers can
// map them to the right HTTP status. A user without `list deployments` would
// otherwise see "404 not found" for a workload they simply can't read, and
// conclude Radar is broken.
var (
	errCacheNotReady   = errors.New("resource cache not initialized")
	errKindRBACDenied  = errors.New("kind not listable by service account")
	errWorkloadMissing = errors.New("workload not found")
)

// Tone is the recommendation severity used by the rightsizing UI.
// Deliberately mild vocabulary: most over/under-provisioning isn't a "problem,"
// it's a tuning opportunity. We reserve "alert" for actual throttling/OOM risk.
type Tone string

const (
	ToneOK       Tone = "ok"
	ToneInfo     Tone = "info"
	ToneWarning  Tone = "warning"
	ToneAlert    Tone = "alert"
	ToneCritical Tone = "critical"
)

// RightsizingRow is one row of the rightsizing recommendation: a container × resource.
type RightsizingRow struct {
	Container      string  `json:"container"`
	Resource       string  `json:"resource"` // "cpu" | "memory"
	CurrentRequest *string `json:"currentRequest,omitempty"`
	CurrentLimit   *string `json:"currentLimit,omitempty"`
	P95            *string `json:"p95,omitempty"`
	RecommendedReq *string `json:"recommendedRequest,omitempty"`
	Tone           Tone    `json:"tone"`
	Message        string  `json:"message"`
}

// RightsizingResponse is the rightsizing endpoint response.
type RightsizingResponse struct {
	Kind            string           `json:"kind"`
	Namespace       string           `json:"namespace"`
	Name            string           `json:"name"`
	Window          string           `json:"window"` // e.g. "24h"
	SampleAvailable bool             `json:"sampleAvailable"`
	Rows            []RightsizingRow `json:"rows"`
	Reason          string           `json:"reason,omitempty"` // populated when sampleAvailable=false
}

const (
	rightsizingWindow         = 24 * time.Hour
	rightsizingHeadroomCPU    = 1.15 // 15% headroom above P95
	rightsizingHeadroomMemory = 1.10 // memory P95 is already conservative
)

// handleRightsizing returns rightsizing recommendations for a workload's containers.
// Only Deployment / StatefulSet / DaemonSet supported — per-pod rightsizing
// is wrong granularity (recs are per-container-template).
func handleRightsizing(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !isRightsizingKind(kind) {
		writeError(w, http.StatusBadRequest, "rightsizing only supported for Deployment, StatefulSet, DaemonSet")
		return
	}

	// Per-user RBAC: the cache is populated under Radar's SA, so without this
	// gate any authenticated user could fetch any namespace's container spec
	// + P95 by guessing names. Use "get" — matches normal resource-detail reads.
	resourcePlural := strings.ToLower(kind) + "s"
	if !canRead(r, "apps", resourcePlural, namespace, "get") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	containers, err := loadWorkloadContainers(kind, namespace, name)
	if err != nil {
		switch {
		case errors.Is(err, errCacheNotReady):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, errKindRBACDenied):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, errWorkloadMissing):
			writeError(w, http.StatusNotFound, err.Error())
		default:
			errorlog.Record("prometheus", "error", "rightsizing: failed to load containers for %s %s/%s: %v", kind, namespace, name, err)
			writeError(w, http.StatusInternalServerError, "failed to load workload containers")
		}
		return
	}
	if len(containers) == 0 {
		writeJSON(w, http.StatusOK, RightsizingResponse{
			Kind: kind, Namespace: namespace, Name: name,
			Window: "24h", SampleAvailable: false,
			Rows:   []RightsizingRow{},
			Reason: "Workload has no runtime containers (init-only or empty spec).",
		})
		return
	}

	resp := RightsizingResponse{
		Kind: kind, Namespace: namespace, Name: name,
		Window:          "24h",
		SampleAvailable: true,
		Rows:            make([]RightsizingRow, 0, len(containers)*2),
	}

	anyData := false
	anyQueryErr := false
	for _, c := range containers {
		cpuRow, cpuErr := computeRightsizingRow(r.Context(), client, namespace, name, c, "cpu")
		memRow, memErr := computeRightsizingRow(r.Context(), client, namespace, name, c, "memory")
		if cpuRow.P95 != nil || memRow.P95 != nil {
			anyData = true
		}
		if cpuErr || memErr {
			anyQueryErr = true
		}
		resp.Rows = append(resp.Rows, cpuRow, memRow)
	}

	if !anyData {
		resp.SampleAvailable = false
		if anyQueryErr {
			resp.Reason = "Prometheus query failed — see server logs."
		} else {
			resp.Reason = "No usage samples in the last 24h — workload may be too new, or Prometheus retention is short."
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func isRightsizingKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "deployment", "statefulset", "daemonset":
		return true
	}
	return false
}

type containerSpec struct {
	name   string
	cpuReq *resource.Quantity
	cpuLim *resource.Quantity
	memReq *resource.Quantity
	memLim *resource.Quantity
}

// loadWorkloadContainers reads runtime container specs (excluding pure init,
// including native sidecars) from the K8s cache. Returns sentinel errors so
// the handler can map cache-not-ready to 503, RBAC-denied to 403, and only
// genuine misses to 404.
func loadWorkloadContainers(kind, namespace, name string) ([]containerSpec, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, errCacheNotReady
	}

	var podTemplate *corev1.PodSpec
	switch strings.ToLower(kind) {
	case "deployment":
		if cache.Deployments() == nil {
			return nil, fmt.Errorf("%w: deployments", errKindRBACDenied)
		}
		d, err := cache.Deployments().Deployments(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("%w: deployment %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &d.Spec.Template.Spec
	case "statefulset":
		if cache.StatefulSets() == nil {
			return nil, fmt.Errorf("%w: statefulsets", errKindRBACDenied)
		}
		ss, err := cache.StatefulSets().StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("%w: statefulset %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &ss.Spec.Template.Spec
	case "daemonset":
		if cache.DaemonSets() == nil {
			return nil, fmt.Errorf("%w: daemonsets", errKindRBACDenied)
		}
		ds, err := cache.DaemonSets().DaemonSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("%w: daemonset %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &ds.Spec.Template.Spec
	}

	if podTemplate == nil {
		return nil, errCacheNotReady
	}

	return extractRuntimeContainers(podTemplate), nil
}

// extractRuntimeContainers returns containers + native-sidecar init containers
// (initContainers with restartPolicy=Always, GA in 1.33). Native sidecars run
// for the pod's lifetime and must be included alongside regular containers;
// pure init containers run to completion and are excluded.
func extractRuntimeContainers(podSpec *corev1.PodSpec) []containerSpec {
	containers := make([]containerSpec, 0, len(podSpec.Containers))
	for _, c := range podSpec.Containers {
		containers = append(containers, extractContainerSpec(c))
	}
	for _, c := range podSpec.InitContainers {
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			containers = append(containers, extractContainerSpec(c))
		}
	}
	return containers
}

func extractContainerSpec(c corev1.Container) containerSpec {
	out := containerSpec{name: c.Name}
	if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
		qc := q.DeepCopy()
		out.cpuReq = &qc
	}
	if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
		qc := q.DeepCopy()
		out.cpuLim = &qc
	}
	if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
		qc := q.DeepCopy()
		out.memReq = &qc
	}
	if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
		qc := q.DeepCopy()
		out.memLim = &qc
	}
	return out
}

// computeRightsizingRow returns the row plus whether the Prometheus query
// errored (distinct from genuine empty data, so the handler can compose an
// accurate Reason instead of telling the user their workload is "too new").
func computeRightsizingRow(ctx context.Context, client *Client, namespace, workload string, c containerSpec, resKind string) (RightsizingRow, bool) {
	row := RightsizingRow{
		Container: c.name,
		Resource:  resKind,
		Tone:      ToneOK,
		Message:   "",
	}

	var req, lim *resource.Quantity
	switch resKind {
	case "cpu":
		req, lim = c.cpuReq, c.cpuLim
	case "memory":
		req, lim = c.memReq, c.memLim
	}
	if req != nil {
		s := req.String()
		row.CurrentRequest = &s
	}
	if lim != nil {
		s := lim.String()
		row.CurrentLimit = &s
	}

	p95, err := queryContainerP95(ctx, client, namespace, workload, c.name, resKind)
	if err != nil {
		// Mark the row so the UI can distinguish it from genuinely-healthy rows.
		// Otherwise a partial failure (some containers query OK, others error) would
		// render the errored containers as well-sized with no signal of the failure.
		row.Tone = ToneInfo
		row.Message = "Prometheus query failed"
		return row, true
	}
	if p95 == nil {
		return row, false
	}

	p95Str := formatRightsizingValue(*p95, resKind)
	row.P95 = &p95Str

	classifyRightsizing(&row, *p95, req, lim, resKind)
	return row, false
}

// queryContainerP95 returns the P95 of a container's CPU/memory usage over the
// rightsizing window. Returns nil (no error) when there's no data.
func queryContainerP95(ctx context.Context, client *Client, namespace, workload, container, resKind string) (*float64, error) {
	ns := prom.SanitizeLabelValue(namespace)
	podPattern := fmt.Sprintf("%s-.*", prom.EscapeRegexMeta(prom.SanitizeLabelValue(workload)))
	cn := prom.SanitizeLabelValue(container)
	windowSec := int64(rightsizingWindow.Seconds())

	var query string
	switch resKind {
	case "cpu":
		// P95 over 24h of 5min rates, max across pods (worst-case for sizing).
		query = fmt.Sprintf(
			`quantile_over_time(0.95, max by (container) (rate(container_cpu_usage_seconds_total{namespace='%s',pod=~'%s',container='%s'}[5m]))[%ds:5m])`,
			ns, podPattern, cn, windowSec)
	case "memory":
		// Memory is a gauge — straight P95 of working set, max across pods.
		query = fmt.Sprintf(
			`quantile_over_time(0.95, max by (container) (container_memory_working_set_bytes{namespace='%s',pod=~'%s',container='%s'})[%ds:])`,
			ns, podPattern, cn, windowSec)
	default:
		return nil, fmt.Errorf("unsupported resource: %s", resKind)
	}

	res, err := client.Query(ctx, query)
	if err != nil {
		errorlog.Record("prometheus", "warning", "rightsizing P95 query failed for %s/%s/%s/%s: %v", namespace, workload, container, resKind, err)
		return nil, err
	}
	if len(res.Series) == 0 || len(res.Series[0].DataPoints) == 0 {
		return nil, nil
	}
	v := res.Series[0].DataPoints[0].Value
	// Prom returns NaN as a float; treat as no data.
	if v != v {
		return nil, nil
	}
	return &v, nil
}

// classifyRightsizing applies the tone + message + recommended request based
// on P95 vs current request/limit. Deliberately mild — most workloads are
// over-provisioned by 2-3x and we don't want to nag them about it.
func classifyRightsizing(row *RightsizingRow, p95 float64, req, lim *resource.Quantity, resKind string) {
	// Hard rule: memory P95 ≥ limit is an active OOM risk regardless of headroom math.
	if resKind == "memory" && lim != nil {
		limVal := quantityToFloat(*lim, resKind)
		if limVal > 0 && p95 >= limVal*0.95 {
			row.Tone = ToneCritical
			row.Message = "P95 near memory limit — active OOM risk"
			if rec := recommendRequest(p95, resKind); rec != "" {
				row.RecommendedReq = &rec
			}
			return
		}
	}

	// No request set — informational nudge, not an alarm.
	if req == nil {
		row.Tone = ToneWarning
		row.Message = fmt.Sprintf("No %s request set — consider setting one based on observed usage", resKind)
		if rec := recommendRequest(p95, resKind); rec != "" {
			row.RecommendedReq = &rec
		}
		return
	}

	reqVal := quantityToFloat(*req, resKind)
	if reqVal <= 0 {
		return
	}

	// p95 == 0 (idle container, or all-zero rate over the window) — ratio would
	// be +Inf and render as "Over-provisioned by +Infx". An idle container with
	// a request set is "well-sized" by definition (it's reserving its quota),
	// and there's no useful recommendation we can derive from a zero sample.
	if p95 <= 0 {
		row.Tone = ToneOK
		row.Message = "Idle — no usage in window"
		return
	}

	// CPU-specific: P95 exceeds limit = active throttling.
	if resKind == "cpu" && lim != nil {
		limVal := quantityToFloat(*lim, resKind)
		if limVal > 0 && p95 > limVal {
			row.Tone = ToneAlert
			row.Message = "P95 exceeds CPU limit — throttling likely"
			if rec := recommendRequest(p95, resKind); rec != "" {
				row.RecommendedReq = &rec
			}
			return
		}
	}

	ratio := reqVal / p95

	// P95 exceeds request (but within limit) → throttled occasionally / no burst headroom.
	if ratio < 1.0 {
		row.Tone = ToneWarning
		row.Message = fmt.Sprintf("P95 usage exceeds request (%.0f%% over)", (1.0/ratio-1.0)*100.0)
		if rec := recommendRequest(p95, resKind); rec != "" {
			row.RecommendedReq = &rec
		}
		return
	}

	// Sensible headroom (1x-3x) — well-sized. No nag.
	if ratio <= 3.0 {
		row.Tone = ToneOK
		row.Message = "Well-sized"
		return
	}

	// Significant over-provisioning thresholds chosen to avoid nagging the common
	// "I requested 256Mi and use 100Mi" pattern (~2.5x — that's fine).
	// CPU is bursty so we tolerate more headroom there than memory.
	overThreshold := 5.0
	if resKind == "cpu" {
		overThreshold = 8.0
	}

	if ratio > overThreshold {
		row.Tone = ToneInfo
		row.Message = fmt.Sprintf("Over-provisioned by %.1fx — could reduce", ratio)
		if rec := recommendRequest(p95, resKind); rec != "" {
			row.RecommendedReq = &rec
		}
		return
	}

	// Between 3x and threshold — informational only, no recommendation.
	row.Tone = ToneOK
	row.Message = fmt.Sprintf("%.1fx headroom", ratio)
}

func recommendRequest(p95 float64, resKind string) string {
	headroom := rightsizingHeadroomCPU
	if resKind == "memory" {
		headroom = rightsizingHeadroomMemory
	}
	return formatRightsizingValue(p95*headroom, resKind)
}

// quantityToFloat converts a K8s Quantity to a float in the same units as
// Prom values (CPU = cores, memory = bytes).
func quantityToFloat(q resource.Quantity, resKind string) float64 {
	switch resKind {
	case "cpu":
		// MilliValue / 1000 gives cores as float — handles "100m" / "1" / "1.5" uniformly.
		return float64(q.MilliValue()) / 1000.0
	case "memory":
		return float64(q.Value())
	}
	return 0
}

// formatRightsizingValue formats a Prom-shaped value (cores or bytes) into the
// human-friendly form that maps back to spec.resources strings.
func formatRightsizingValue(v float64, resKind string) string {
	switch resKind {
	case "cpu":
		if v < 0.001 {
			return "1m"
		}
		// Round to the nearest 10m to avoid noisy recommendations like 137m.
		millis := max(int64(v*1000.0+5)/10*10, 10)
		if millis < 1000 {
			return fmt.Sprintf("%dm", millis)
		}
		cores := float64(millis) / 1000.0
		// Trim trailing .0
		if cores == float64(int64(cores)) {
			return fmt.Sprintf("%d", int64(cores))
		}
		return fmt.Sprintf("%.1f", cores)
	case "memory":
		const Mi = 1024 * 1024
		const Gi = 1024 * Mi
		if v >= float64(Gi) {
			return fmt.Sprintf("%.1fGi", v/float64(Gi))
		}
		// Round up to next 16Mi to give a clean recommendation.
		mib := max(int64(v/float64(Mi)+15)/16*16, 16)
		return fmt.Sprintf("%dMi", mib)
	}
	return ""
}

// PVCUsageResponse is returned by the PVC usage endpoint.
type PVCUsageResponse struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	Used      int64   `json:"used"`     // bytes
	Capacity  int64   `json:"capacity"` // bytes
	Ratio     float64 `json:"ratio"`    // 0.0 - 1.0
	HasData   bool    `json:"hasData"`  // false when no series (CSI not reporting, kubelet not scraped, etc.)
}

// handlePVCUsage returns current usage for a PVC, computed from
// kubelet_volume_stats_{used,capacity}_bytes. Returns HasData=false silently
// when no series — many CSI drivers don't implement NodeGetVolumeStats and
// some Prom configs (notably GMP default) don't scrape kubelet endpoints.
func handlePVCUsage(w http.ResponseWriter, r *http.Request) {
	client := GetClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "Prometheus client not initialized")
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !canRead(r, "", "persistentvolumeclaims", namespace, "get") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	ns := prom.SanitizeLabelValue(namespace)
	pvc := prom.SanitizeLabelValue(name)

	// kubelet's native label is `persistentvolumeclaim`; clusters with custom
	// relabeling that renamed it will return no series and the gauge hides.
	usedQuery := fmt.Sprintf(`max(kubelet_volume_stats_used_bytes{namespace='%s',persistentvolumeclaim='%s'})`, ns, pvc)
	capQuery := fmt.Sprintf(`max(kubelet_volume_stats_capacity_bytes{namespace='%s',persistentvolumeclaim='%s'})`, ns, pvc)

	resp := PVCUsageResponse{Namespace: namespace, Name: name}

	usedRes, err := client.Query(r.Context(), usedQuery)
	if err != nil {
		// Distinguish "Prometheus is unreachable" from "CSI doesn't report" so
		// operators can find this in the errorlog stream when the gauge mysteriously
		// disappears. The frontend still hides on hasData=false.
		errorlog.Record("prometheus", "warning", "pvc used-bytes query failed for %s/%s: %v", namespace, name, err)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	capRes, err := client.Query(r.Context(), capQuery)
	if err != nil {
		errorlog.Record("prometheus", "warning", "pvc capacity-bytes query failed for %s/%s: %v", namespace, name, err)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	used := firstValue(usedRes)
	capacity := firstValue(capRes)
	if used == nil || capacity == nil || *capacity <= 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Used = int64(*used)
	resp.Capacity = int64(*capacity)
	resp.Ratio = *used / *capacity
	resp.HasData = true
	writeJSON(w, http.StatusOK, resp)
}

func firstValue(res *prom.QueryResult) *float64 {
	if res == nil || len(res.Series) == 0 || len(res.Series[0].DataPoints) == 0 {
		return nil
	}
	v := res.Series[0].DataPoints[0].Value
	if v != v {
		return nil
	}
	return &v
}
