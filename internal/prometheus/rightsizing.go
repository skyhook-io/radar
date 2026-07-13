package prometheus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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

type RightsizingFit string

const (
	FitBalanced            RightsizingFit = "balanced"
	FitOversized           RightsizingFit = "oversized"
	FitUnderRequested      RightsizingFit = "under_requested"
	FitMissingRequest      RightsizingFit = "missing_request"
	FitInsufficientHistory RightsizingFit = "insufficient_history"
)

type RightsizingConfidence string

const (
	ConfidenceLow    RightsizingConfidence = "low"
	ConfidenceMedium RightsizingConfidence = "medium"
	ConfidenceHigh   RightsizingConfidence = "high"
)

type OwnerCoverage string

const (
	OwnerCoverageKSMHistory  OwnerCoverage = "ksm_history"
	OwnerCoverageCurrentPods OwnerCoverage = "current_pods"
)

type ObservedStatistic struct {
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
	Formatted string  `json:"formatted"`
}

type RightsizingRow struct {
	Container               string               `json:"container"`
	Resource                string               `json:"resource"`
	Fit                     RightsizingFit        `json:"fit"`
	Confidence              RightsizingConfidence `json:"confidence"`
	CurrentRequest          *string              `json:"currentRequest,omitempty"`
	CurrentRequestValue     *float64             `json:"currentRequestValue,omitempty"`
	CurrentLimit            *string              `json:"currentLimit,omitempty"`
	CurrentLimitValue       *float64             `json:"currentLimitValue,omitempty"`
	Observed                *ObservedStatistic   `json:"observed,omitempty"`
	Peak                    *ObservedStatistic   `json:"peak,omitempty"`
	CalculatedReq           *string              `json:"calculatedRequest,omitempty"`
	CalculatedRequestValue  *float64             `json:"calculatedRequestValue,omitempty"`
	RecommendedReq          *string              `json:"recommendedRequest,omitempty"`
	RecommendedRequestValue *float64             `json:"recommendedRequestValue,omitempty"`
	ReductionLimited        bool                 `json:"reductionLimited,omitempty"`
	Bursty                  bool                 `json:"bursty,omitempty"`
	RecommendationReason    string               `json:"recommendationReason,omitempty"`
	SampleCount             int                  `json:"sampleCount"`
	ExpectedSamples         int                  `json:"expectedSamples"`
	Coverage                float64              `json:"coverage"`
	HPAManaged              bool                 `json:"hpaManaged"`
	HPAEvidenceAvailable    bool                 `json:"hpaEvidenceAvailable"`
	ThrottleAvailable       bool                 `json:"throttleAvailable,omitempty"`
	ThrottleRatio           *float64             `json:"throttleRatio,omitempty"`
	CurrentPodOOM           bool                 `json:"currentPodOOM,omitempty"`
	WindowOOMEvidence       bool                 `json:"windowOomEvidence,omitempty"`
	OOMEvidenceAvailable    bool                 `json:"oomEvidenceAvailable"`
	LimitConflict           bool                 `json:"limitConflict,omitempty"`
	QueryError              string               `json:"queryError,omitempty"`
}

type RightsizingResponse struct {
	Kind            string           `json:"kind"`
	Namespace       string           `json:"namespace"`
	Name            string           `json:"name"`
	Window          string           `json:"window"`
	Source          string           `json:"source"`
	OwnerCoverage   OwnerCoverage    `json:"ownerCoverage"`
	ScaledToZero    bool             `json:"scaledToZero"`
	SampleAvailable bool             `json:"sampleAvailable"`
	Rows            []RightsizingRow `json:"rows"`
	Reason          string           `json:"reason,omitempty"`
}

const (
	rightsizingWindow       = 7 * 24 * time.Hour
	rightsizingStep         = 5 * time.Minute
	rightsizingHeadroom     = 1.15
	rightsizingCPUMin       = 0.01
	rightsizingMemoryMin    = 64 * 1024 * 1024
	rightsizingMinSamples   = 72
	rightsizingHighCoverage = 0.8
	rightsizingMedCoverage  = 0.14
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

	workload, err := loadRightsizingWorkload(kind, namespace, name)
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
	if len(workload.containers) == 0 {
		writeJSON(w, http.StatusOK, RightsizingResponse{
			Kind: kind, Namespace: namespace, Name: name,
			Window: "7d", Source: "radar", SampleAvailable: false,
			Rows:   []RightsizingRow{},
			Reason: "Workload has no runtime containers (init-only or empty spec).",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp := computeRightsizing(ctx, client, kind, namespace, name, workload)

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

type rightsizingWorkload struct {
	containers    []containerSpec
	podNames      []string
	currentPodOOM map[string]bool
	hpaManaged    map[string]bool
	hpaAvailable  bool
	scaledToZero  bool
}

func loadRightsizingWorkload(kind, namespace, name string) (rightsizingWorkload, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return rightsizingWorkload{}, errCacheNotReady
	}

	var podTemplate *corev1.PodSpec
	scaledToZero := false
	switch strings.ToLower(kind) {
	case "deployment":
		if cache.Deployments() == nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: deployments", errKindRBACDenied)
		}
		d, err := cache.Deployments().Deployments(namespace).Get(name)
		if err != nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: deployment %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &d.Spec.Template.Spec
		scaledToZero = d.Spec.Replicas != nil && *d.Spec.Replicas == 0
	case "statefulset":
		if cache.StatefulSets() == nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: statefulsets", errKindRBACDenied)
		}
		ss, err := cache.StatefulSets().StatefulSets(namespace).Get(name)
		if err != nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: statefulset %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &ss.Spec.Template.Spec
		scaledToZero = ss.Spec.Replicas != nil && *ss.Spec.Replicas == 0
	case "daemonset":
		if cache.DaemonSets() == nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: daemonsets", errKindRBACDenied)
		}
		ds, err := cache.DaemonSets().DaemonSets(namespace).Get(name)
		if err != nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: daemonset %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &ds.Spec.Template.Spec
		scaledToZero = ds.Status.DesiredNumberScheduled == 0
	}

	if podTemplate == nil {
		return rightsizingWorkload{}, errCacheNotReady
	}

	hpaManaged, hpaAvailable := loadHPAManagedResources(cache, kind, namespace, name)
	workload := rightsizingWorkload{
		containers:    extractRuntimeContainers(podTemplate),
		currentPodOOM: map[string]bool{},
		hpaManaged:    hpaManaged,
		hpaAvailable:  hpaAvailable,
		scaledToZero:  scaledToZero,
	}
	if cache.Pods() == nil {
		return workload, nil
	}
	pods, err := cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return workload, nil
	}
	for _, pod := range pods {
		if !podOwnedByWorkload(cache, pod, kind, name) {
			continue
		}
		workload.podNames = append(workload.podNames, pod.Name)
		collectCurrentPodOOM(workload.currentPodOOM, pod.Status.ContainerStatuses)
		collectCurrentPodOOM(workload.currentPodOOM, pod.Status.InitContainerStatuses)
	}
	sort.Strings(workload.podNames)
	return workload, nil
}

func podOwnedByWorkload(cache *k8s.ResourceCache, pod *corev1.Pod, kind, name string) bool {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return false
	}
	if strings.EqualFold(owner.Kind, kind) {
		return owner.Name == name
	}
	if !strings.EqualFold(kind, "Deployment") || owner.Kind != "ReplicaSet" || cache.ReplicaSets() == nil {
		return false
	}
	rs, err := cache.ReplicaSets().ReplicaSets(pod.Namespace).Get(owner.Name)
	if err != nil {
		return false
	}
	rsOwner := metav1.GetControllerOf(rs)
	return rsOwner != nil && rsOwner.Kind == "Deployment" && rsOwner.Name == name
}

func collectCurrentPodOOM(dst map[string]bool, statuses []corev1.ContainerStatus) {
	for _, status := range statuses {
		if status.State.Terminated != nil && status.State.Terminated.Reason == "OOMKilled" {
			dst[status.Name] = true
		}
		if status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.Reason == "OOMKilled" {
			dst[status.Name] = true
		}
	}
}

func loadHPAManagedResources(cache *k8s.ResourceCache, kind, namespace, name string) (map[string]bool, bool) {
	managed := map[string]bool{}
	if !cache.IsDeferredSynced() || cache.HorizontalPodAutoscalers() == nil {
		return managed, false
	}
	hpas, err := cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).List(labels.Everything())
	if err != nil {
		return managed, false
	}
	for _, hpa := range hpas {
		ref := hpa.Spec.ScaleTargetRef
		if !strings.EqualFold(ref.Kind, kind) || ref.Name != name {
			continue
		}
		for _, metric := range hpa.Spec.Metrics {
			if metric.Type != autoscalingv2.ResourceMetricSourceType || metric.Resource == nil || metric.Resource.Target.AverageUtilization == nil {
				continue
			}
			resourceName := string(metric.Resource.Name)
			if resourceName == "cpu" || resourceName == "memory" {
				managed[resourceName] = true
			}
		}
	}
	return managed, true
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

type rightsizingQuerier interface {
	Query(context.Context, string) (*prom.QueryResult, error)
}

type queryOutcome struct {
	values       map[string]float64
	terminations map[string]terminationEvidence
	err          error
}

type terminationEvidence struct {
	Any bool
	OOM bool
}

func computeRightsizing(ctx context.Context, client rightsizingQuerier, kind, namespace, name string, workload rightsizingWorkload) RightsizingResponse {
	selection, coverage := workloadSelection(ctx, client, kind, namespace, name, workload.podNames)
	queries := buildRightsizingQueries(namespace, selection)
	results := runRightsizingQueries(ctx, client, queries)
	expected := int(rightsizingWindow / rightsizingStep)
	resp := RightsizingResponse{
		Kind: kind, Namespace: namespace, Name: name, Window: "7d", Source: "radar",
		OwnerCoverage: coverage, ScaledToZero: workload.scaledToZero,
		Rows: make([]RightsizingRow, 0, len(workload.containers)*2),
	}
	for _, container := range workload.containers {
		for _, resourceName := range []string{"cpu", "memory"} {
			row := buildRightsizingRow(container, resourceName, expected, coverage, workload, results)
			resp.Rows = append(resp.Rows, row)
		}
	}
	for _, row := range resp.Rows {
		if row.Observed != nil {
			resp.SampleAvailable = true
			break
		}
	}
	if !resp.SampleAvailable {
		if results["cpu_stat"].err != nil || results["cpu_coverage"].err != nil || results["memory_stat"].err != nil || results["memory_coverage"].err != nil {
			resp.Reason = "Prometheus rightsizing queries failed."
		} else if len(workload.podNames) == 0 && coverage == OwnerCoverageCurrentPods {
			resp.Reason = "No current or retained workload ownership samples are available."
		} else {
			resp.Reason = "No workload usage samples are available in the last 7d."
		}
	}
	return resp
}

type metricSelection struct {
	join       string
	podPattern string
}

func workloadSelection(ctx context.Context, client rightsizingQuerier, kind, namespace, name string, podNames []string) (metricSelection, OwnerCoverage) {
	owner := ownerSelection(kind, namespace, name)
	if owner != "" {
		probe := fmt.Sprintf(`sum(count_over_time((%s)[7d:5m]))`, owner)
		if result, err := client.Query(ctx, probe); err == nil && firstValue(result) != nil && *firstValue(result) > 0 {
			return metricSelection{join: fmt.Sprintf(`* on (namespace,pod) group_left() (%s)`, owner)}, OwnerCoverageKSMHistory
		}
	}
	if len(podNames) == 0 {
		return metricSelection{podPattern: "a^"}, OwnerCoverageCurrentPods
	}
	escaped := make([]string, 0, len(podNames))
	for _, podName := range podNames {
		escaped = append(escaped, prom.EscapeRegexMeta(prom.SanitizeLabelValue(podName)))
	}
	return metricSelection{podPattern: fmt.Sprintf("^(%s)$", strings.Join(escaped, "|"))}, OwnerCoverageCurrentPods
}

func ownerSelection(kind, namespace, name string) string {
	ns := prom.SanitizeLabelValue(namespace)
	workload := prom.SanitizeLabelValue(name)
	switch strings.ToLower(kind) {
	case "deployment":
		return fmt.Sprintf(`label_replace(max by (namespace,pod,owner_name) (kube_pod_owner{namespace="%s",owner_kind="ReplicaSet",owner_is_controller="true"}), "replicaset", "$1", "owner_name", "(.*)") * on (namespace,replicaset) group_left() max by (namespace,replicaset) (kube_replicaset_owner{namespace="%s",owner_kind="Deployment",owner_name="%s",owner_is_controller="true"})`, ns, ns, workload)
	case "statefulset", "daemonset":
		ownerKind := "StatefulSet"
		if strings.EqualFold(kind, "DaemonSet") {
			ownerKind = "DaemonSet"
		}
		return fmt.Sprintf(`max by (namespace,pod) (kube_pod_owner{namespace="%s",owner_kind="%s",owner_name="%s",owner_is_controller="true"})`, ns, ownerKind, workload)
	}
	return ""
}

func buildRightsizingQueries(namespace string, selection metricSelection) map[string]string {
	ns := prom.SanitizeLabelValue(namespace)
	podMatcher := ""
	if selection.podPattern != "" {
		podMatcher = fmt.Sprintf(`,pod=~"%s"`, selection.podPattern)
	}
	cpu := fmt.Sprintf(`max by (container) (rate(container_cpu_usage_seconds_total{namespace="%s"%s,container!="",container!="POD"}[5m]) %s)`, ns, podMatcher, selection.join)
	memory := fmt.Sprintf(`max by (container) (container_memory_working_set_bytes{namespace="%s"%s,container!="",container!="POD"} %s)`, ns, podMatcher, selection.join)
	throttled := fmt.Sprintf(`sum by (container) (rate(container_cpu_cfs_throttled_periods_total{namespace="%s"%s,container!="",container!="POD"}[5m]) %s)`, ns, podMatcher, selection.join)
	periods := fmt.Sprintf(`sum by (container) (rate(container_cpu_cfs_periods_total{namespace="%s"%s,container!="",container!="POD"}[5m]) %s)`, ns, podMatcher, selection.join)
	restarts := fmt.Sprintf(`max by (namespace,pod,container) (kube_pod_container_status_restarts_total{namespace="%s"%s,container!=""} %s)`, ns, podMatcher, selection.join)
	terminations := fmt.Sprintf(`max by (namespace,pod,container,reason) (kube_pod_container_status_last_terminated_timestamp{namespace="%s"%s,container!=""} * on (namespace,pod,container) group_left(reason) max by (namespace,pod,container,reason) (kube_pod_container_status_last_terminated_reason{namespace="%s"%s,container!=""}) %s)`, ns, podMatcher, ns, podMatcher, selection.join)
	return map[string]string{
		"cpu_stat":            fmt.Sprintf(`quantile_over_time(0.95, (%s)[7d:5m])`, cpu),
		"cpu_peak":            fmt.Sprintf(`quantile_over_time(0.99, (%s)[7d:5m])`, cpu),
		"cpu_coverage":        fmt.Sprintf(`count_over_time((%s)[7d:5m])`, cpu),
		"memory_stat":         fmt.Sprintf(`max_over_time((%s)[7d:5m])`, memory),
		"memory_coverage":     fmt.Sprintf(`count_over_time((%s)[7d:5m])`, memory),
		"throttle":            fmt.Sprintf(`max_over_time(((%s) / (%s))[7d:5m])`, throttled, periods),
		"restart_activity":    fmt.Sprintf(`sum by (container) (increase((%s)[7d:5m]))`, restarts),
		"termination_history": fmt.Sprintf(`max by (container,reason) (max_over_time((%s)[7d:5m])) > (time() - 604800)`, terminations),
	}
}

func runRightsizingQueries(ctx context.Context, client rightsizingQuerier, queries map[string]string) map[string]queryOutcome {
	results := make(map[string]queryOutcome, len(queries))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for key, query := range queries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			result, err := client.Query(ctx, query)
			<-sem
			outcome := queryOutcome{values: resultByContainer(result), err: err}
			if key == "termination_history" {
				outcome.terminations = terminationEvidenceByContainer(result)
			}
			if err != nil {
				errorlog.Record("prometheus", "warning", "rightsizing query %s failed: %v", key, err)
			}
			mu.Lock()
			results[key] = outcome
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

func resultByContainer(result *prom.QueryResult) map[string]float64 {
	values := map[string]float64{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		if len(series.DataPoints) == 0 || math.IsNaN(series.DataPoints[0].Value) || math.IsInf(series.DataPoints[0].Value, 0) {
			continue
		}
		if container := series.Labels["container"]; container != "" {
			values[container] = series.DataPoints[0].Value
		}
	}
	return values
}

func terminationEvidenceByContainer(result *prom.QueryResult) map[string]terminationEvidence {
	values := map[string]terminationEvidence{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		if len(series.DataPoints) == 0 || series.DataPoints[0].Value <= 0 {
			continue
		}
		container := series.Labels["container"]
		if container == "" {
			continue
		}
		evidence := values[container]
		evidence.Any = true
		evidence.OOM = evidence.OOM || series.Labels["reason"] == "OOMKilled"
		values[container] = evidence
	}
	return values
}

func buildRightsizingRow(container containerSpec, resourceName string, expected int, ownerCoverage OwnerCoverage, workload rightsizingWorkload, results map[string]queryOutcome) RightsizingRow {
	row := RightsizingRow{Container: container.name, Resource: resourceName, Fit: FitInsufficientHistory, Confidence: ConfidenceLow, ExpectedSamples: expected, HPAManaged: workload.hpaManaged[resourceName], HPAEvidenceAvailable: workload.hpaAvailable}
	var req, lim *resource.Quantity
	statistic := "P95"
	if resourceName == "cpu" {
		req, lim = container.cpuReq, container.cpuLim
	} else {
		req, lim = container.memReq, container.memLim
		statistic = "Max"
		row.CurrentPodOOM = workload.currentPodOOM[container.name]
		restarts := results["restart_activity"]
		terminations := results["termination_history"]
		if restarts.err == nil && terminations.err == nil {
			restartActivity, restartAvailable := restarts.values[container.name]
			termination := terminations.terminations[container.name]
			row.WindowOOMEvidence = termination.OOM
			row.OOMEvidenceAvailable = termination.OOM || (restartAvailable && (restartActivity <= 0 || termination.Any))
		}
	}
	setCurrentQuantities(&row, req, lim, resourceName)
	stat := results[resourceName+"_stat"]
	coverage := results[resourceName+"_coverage"]
	if stat.err != nil {
		row.QueryError = "usage query failed"
		return row
	}
	if coverage.err != nil {
		row.QueryError = "sample coverage query failed"
		return row
	}
	observed, ok := stat.values[container.name]
	if !ok {
		return row
	}
	row.Observed = &ObservedStatistic{Name: statistic, Value: observed, Formatted: formatObservedValue(observed, resourceName)}
	if resourceName == "cpu" {
		peak := results["cpu_peak"]
		if peak.err == nil {
			if value, present := peak.values[container.name]; present {
				row.Peak = &ObservedStatistic{Name: "P99", Value: value, Formatted: formatObservedValue(value, resourceName)}
				row.Bursty = isBurstyCPU(observed, value)
			}
		}
	}
	row.SampleCount = int(coverage.values[container.name])
	row.Coverage = math.Min(float64(row.SampleCount)/float64(expected), 1)
	row.Confidence = confidenceFor(row.SampleCount, row.Coverage, ownerCoverage)
	if resourceName == "cpu" {
		throttle := results["throttle"]
		if throttle.err == nil {
			if value, present := throttle.values[container.name]; present {
				row.ThrottleAvailable = true
				row.ThrottleRatio = &value
			}
		}
	}
	if row.SampleCount < rightsizingMinSamples {
		row.RecommendationReason = "insufficient_history"
		return row
	}
	classifyRightsizingFit(&row, observed, req, lim, resourceName)
	return row
}

func setCurrentQuantities(row *RightsizingRow, req, lim *resource.Quantity, resourceName string) {
	if req != nil {
		value := quantityToFloat(*req, resourceName)
		formatted := req.String()
		row.CurrentRequest = &formatted
		row.CurrentRequestValue = &value
	}
	if lim != nil {
		value := quantityToFloat(*lim, resourceName)
		formatted := lim.String()
		row.CurrentLimit = &formatted
		row.CurrentLimitValue = &value
	}
}

func confidenceFor(samples int, coverage float64, ownerCoverage OwnerCoverage) RightsizingConfidence {
	if samples < rightsizingMinSamples {
		return ConfidenceLow
	}
	if coverage >= rightsizingHighCoverage && ownerCoverage == OwnerCoverageKSMHistory {
		return ConfidenceHigh
	}
	if coverage >= rightsizingMedCoverage {
		return ConfidenceMedium
	}
	return ConfidenceLow
}

func classifyRightsizingFit(row *RightsizingRow, observed float64, req, lim *resource.Quantity, resourceName string) {
	calculated := calculatedRequest(observed, resourceName)
	calculatedValue := quantityToFloat(resource.MustParse(calculated), resourceName)
	minimum := float64(rightsizingMemoryMin)
	if resourceName == "cpu" {
		minimum = rightsizingCPUMin
	}
	candidate := max(observed*rightsizingHeadroom, minimum)
	if req == nil || quantityToFloat(*req, resourceName) <= 0 {
		row.Fit = FitMissingRequest
	} else {
		requestValue := quantityToFloat(*req, resourceName)
		switch {
		case candidate <= requestValue*0.7:
			row.Fit = FitOversized
		case candidate > requestValue:
			row.Fit = FitUnderRequested
		default:
			row.Fit = FitBalanced
		}
	}
	if row.Fit == FitBalanced {
		row.RecommendationReason = "request_within_fit_range"
		return
	}
	recommended, reductionLimited := recommendRequest(observed, req, resourceName, row.Bursty || (row.ThrottleRatio != nil && *row.ThrottleRatio >= 0.1))
	recommendedValue := quantityToFloat(resource.MustParse(recommended), resourceName)
	row.CalculatedReq = &calculated
	row.CalculatedRequestValue = &calculatedValue
	if req != nil {
		requestValue := quantityToFloat(*req, resourceName)
		if (row.Fit == FitOversized && recommendedValue >= requestValue) ||
			(row.Fit == FitUnderRequested && recommendedValue <= requestValue) {
			row.Fit = FitBalanced
			row.RecommendationReason = "request_within_fit_range"
			return
		}
	}
	if row.HPAManaged {
		row.RecommendationReason = "hpa_managed"
		return
	}
	if row.Fit == FitOversized && !row.HPAEvidenceAvailable {
		row.RecommendationReason = "hpa_evidence_unavailable"
		return
	}
	if resourceName == "memory" && row.Fit == FitOversized && (row.CurrentPodOOM || row.WindowOOMEvidence) {
		row.RecommendationReason = "oom_evidence"
		return
	}
	if resourceName == "memory" && row.Fit == FitOversized && !row.OOMEvidenceAvailable {
		row.RecommendationReason = "oom_evidence_unavailable"
		return
	}
	if lim != nil && recommendedValue > quantityToFloat(*lim, resourceName) {
		row.LimitConflict = true
		row.RecommendationReason = "recommended_request_exceeds_limit"
		return
	}
	row.RecommendedReq = &recommended
	row.RecommendedRequestValue = &recommendedValue
	row.ReductionLimited = reductionLimited
}

func calculatedRequest(observed float64, resourceName string) string {
	minimum := float64(rightsizingMemoryMin)
	if resourceName == "cpu" {
		minimum = rightsizingCPUMin
	}
	return formatRightsizingValue(max(observed*rightsizingHeadroom, minimum), resourceName)
}

func recommendRequest(observed float64, current *resource.Quantity, resourceName string, conservative bool) (string, bool) {
	calculated := calculatedRequest(observed, resourceName)
	calculatedValue := quantityToFloat(resource.MustParse(calculated), resourceName)
	if current == nil {
		return calculated, false
	}
	currentValue := quantityToFloat(*current, resourceName)
	if calculatedValue >= currentValue {
		return calculated, false
	}
	floor := reductionFloor(currentValue, resourceName, conservative)
	if calculatedValue >= floor {
		return calculated, false
	}
	return formatRightsizingValue(floor, resourceName), true
}

func reductionFloor(current float64, resourceName string, conservative bool) float64 {
	if resourceName == "memory" || conservative || current >= 1 {
		return current * 0.5
	}
	if current >= 0.1 {
		return current * 0.25
	}
	return rightsizingCPUMin
}

func isBurstyCPU(p95, p99 float64) bool {
	return p99-p95 >= 0.05 && p99 >= p95*3
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
		millis := int64(math.Ceil(v * 1000))
		millis = max(millis, 10)
		switch {
		case millis < 100:
			millis = roundUp(millis, 10)
		case millis < 1000:
			millis = roundUp(millis, 50)
		case millis < 4000:
			millis = roundUp(millis, 500)
		default:
			millis = roundUp(millis, 1000)
		}
		if millis < 1000 {
			return fmt.Sprintf("%dm", millis)
		}
		cores := float64(millis) / 1000.0
		if cores == float64(int64(cores)) {
			return fmt.Sprintf("%d", int64(cores))
		}
		return fmt.Sprintf("%.1f", cores)
	case "memory":
		const Mi = 1024 * 1024
		const Gi = 1024 * Mi
		mib := int64(math.Ceil(v / float64(Mi)))
		preferred := []int64{64, 96, 128, 192, 256, 384, 512, 768, 1024, 1536, 2048, 3072, 4096, 6144, 8192}
		for _, candidate := range preferred {
			if mib <= candidate {
				if candidate >= 1024 {
					gib := float64(candidate) / 1024
					if gib == float64(int64(gib)) {
						return fmt.Sprintf("%dGi", int64(gib))
					}
					return fmt.Sprintf("%.1fGi", gib)
				}
				return fmt.Sprintf("%dMi", candidate)
			}
		}
		gib := roundUp(mib, 2*1024) / 1024
		return fmt.Sprintf("%dGi", gib)
	}
	return ""
}

func roundUp(value, step int64) int64 {
	return (value + step - 1) / step * step
}

func formatObservedValue(v float64, resourceName string) string {
	switch resourceName {
	case "cpu":
		millis := v * 1000
		if millis < 1 {
			return "<1m"
		}
		if millis < 1000 {
			return fmt.Sprintf("%.0fm", millis)
		}
		return fmt.Sprintf("%.2f", v)
	case "memory":
		const Mi = 1024 * 1024
		const Gi = 1024 * Mi
		if v >= float64(Gi) {
			return fmt.Sprintf("%.2fGi", v/float64(Gi))
		}
		return fmt.Sprintf("%.0fMi", v/float64(Mi))
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
