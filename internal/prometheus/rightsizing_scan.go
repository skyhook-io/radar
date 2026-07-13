package prometheus

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
)

type RightsizingScanState string

const (
	RightsizingScanComplete    RightsizingScanState = "complete"
	RightsizingScanPartial     RightsizingScanState = "partial"
	RightsizingScanUnavailable RightsizingScanState = "unavailable"
	rightsizingScanBatchSize                        = 50
)

type RightsizingScanScope struct {
	NamespacesByKind map[string][]string
	RestrictedKinds  []string
}

type RightsizingScanWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type RightsizingScanCoverage struct {
	WorkloadsDiscovered int      `json:"workloadsDiscovered"`
	WorkloadsEvaluated  int      `json:"workloadsEvaluated"`
	WorkloadsWithData   int      `json:"workloadsWithData"`
	Batches             int      `json:"batches"`
	CompletedBatches    int      `json:"completedBatches"`
	RestrictedKinds     []string `json:"restrictedKinds,omitempty"`
	UnavailableKinds    []string `json:"unavailableKinds,omitempty"`
}

type RightsizingScanWorkload struct {
	Kind         string           `json:"kind"`
	Namespace    string           `json:"namespace"`
	Name         string           `json:"name"`
	Replicas     int              `json:"replicas"`
	ScaledToZero bool             `json:"scaledToZero"`
	Rows         []RightsizingRow `json:"rows"`
}

type RightsizingScanResponse struct {
	State     RightsizingScanState      `json:"state"`
	ScannedAt time.Time                 `json:"scannedAt"`
	Window    string                    `json:"window"`
	Source    string                    `json:"source"`
	Coverage  RightsizingScanCoverage   `json:"coverage"`
	Workloads []RightsizingScanWorkload `json:"workloads"`
	Warnings  []RightsizingScanWarning  `json:"warnings,omitempty"`
	Reason    string                    `json:"reason,omitempty"`
}

type rightsizingScanQuerier interface {
	Query(context.Context, string) (*prom.QueryResult, error)
	QueryRange(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error)
}

type scanWorkload struct {
	kind      string
	namespace string
	name      string
	replicas  int
	workload  rightsizingWorkload
}

type scanKey struct {
	namespace string
	kind      string
	workload  string
	container string
}

type scanBatchEvidence struct {
	cpu          map[scanKey][]float64
	memory       map[scanKey][]float64
	throttle     map[scanKey][]float64
	restarts     map[scanKey]float64
	terminations map[scanKey]terminationEvidence
	errors       map[string]error
}

func ScanRightsizing(ctx context.Context, scope RightsizingScanScope) RightsizingScanResponse {
	now := time.Now().UTC()
	resp := newRightsizingScanResponse(now, scope)
	client := GetClient()
	if client == nil {
		resp.Reason = "prometheus_unavailable"
		return resp
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		resp.Reason = "resource_cache_unavailable"
		return resp
	}
	workloads, unavailable := snapshotScanWorkloads(cache, scope.NamespacesByKind)
	resp.Coverage.UnavailableKinds = unavailable
	return computeRightsizingScan(ctx, client, workloads, resp)
}

func newRightsizingScanResponse(now time.Time, scope RightsizingScanScope) RightsizingScanResponse {
	restricted := append([]string(nil), scope.RestrictedKinds...)
	sort.Strings(restricted)
	return RightsizingScanResponse{
		State: RightsizingScanUnavailable, ScannedAt: now, Window: "7d", Source: "radar",
		Coverage:  RightsizingScanCoverage{RestrictedKinds: restricted},
		Workloads: []RightsizingScanWorkload{},
	}
}

func computeRightsizingScan(ctx context.Context, client rightsizingScanQuerier, workloads []scanWorkload, resp RightsizingScanResponse) RightsizingScanResponse {
	sortScanWorkloads(workloads)
	resp.Coverage.WorkloadsDiscovered = len(workloads)
	if len(workloads) == 0 {
		limitations := len(resp.Coverage.RestrictedKinds) + len(resp.Coverage.UnavailableKinds)
		switch {
		case limitations >= 3:
			resp.State = RightsizingScanUnavailable
			resp.Reason = "workload_kinds_unavailable"
		case limitations > 0:
			resp.State = RightsizingScanPartial
			resp.Reason = "limited_scope_no_workloads"
		default:
			resp.State = RightsizingScanComplete
			resp.Reason = "no_workloads"
		}
		return resp
	}

	ksm, err := client.Query(ctx, `count(kube_pod_owner)`)
	if err != nil {
		resp.Reason = "owner_metrics_query_failed"
		resp.Warnings = append(resp.Warnings, RightsizingScanWarning{Code: "owner_metrics_query_failed", Message: err.Error()})
		return resp
	}
	if firstValue(ksm) == nil || *firstValue(ksm) <= 0 {
		resp.Reason = "owner_metrics_missing"
		return resp
	}
	if hasScanKind(workloads, "Deployment") {
		replicaSetOwners, queryErr := client.Query(ctx, `count(kube_replicaset_owner)`)
		if queryErr != nil || firstValue(replicaSetOwners) == nil || *firstValue(replicaSetOwners) <= 0 {
			resp.Coverage.UnavailableKinds = appendUniqueSorted(resp.Coverage.UnavailableKinds, "Deployment")
			workloads = withoutScanKind(workloads, "Deployment")
			if queryErr != nil {
				appendScanWarning(&resp, "deployment_owner_metrics_query_failed", queryErr.Error())
			}
		}
	}
	if len(workloads) == 0 {
		resp.Reason = "deployment_owner_metrics_missing"
		return resp
	}

	resp.Coverage.Batches = (len(workloads) + rightsizingScanBatchSize - 1) / rightsizingScanBatchSize
	for start := 0; start < len(workloads); start += rightsizingScanBatchSize {
		if err := ctx.Err(); err != nil {
			appendScanWarning(&resp, "scan_deadline_exceeded", err.Error())
			break
		}
		end := min(start+rightsizingScanBatchSize, len(workloads))
		batch := workloads[start:end]
		evidence := queryRightsizingScanBatch(ctx, client, batch, resp.ScannedAt)
		if len(evidence.errors) == 0 {
			resp.Coverage.CompletedBatches++
		} else {
			for key, queryErr := range evidence.errors {
				appendScanWarning(&resp, key+"_query_failed", queryErr.Error())
			}
		}
		for _, workload := range batch {
			out := buildScanWorkload(workload, evidence)
			resp.Workloads = append(resp.Workloads, out)
			resp.Coverage.WorkloadsEvaluated++
			if workloadHasData(out) {
				resp.Coverage.WorkloadsWithData++
			}
			if workloadHasUnavailableOOMEvidence(out) {
				appendScanWarning(&resp, "oom_evidence_unavailable", "Restart history was incomplete for some memory recommendations.")
			}
		}
	}

	switch {
	case resp.Coverage.WorkloadsEvaluated == 0:
		resp.State = RightsizingScanUnavailable
		if resp.Reason == "" {
			resp.Reason = "scan_incomplete"
		}
	case len(resp.Warnings) > 0 || resp.Coverage.WorkloadsEvaluated < len(workloads) || len(resp.Coverage.RestrictedKinds) > 0 || len(resp.Coverage.UnavailableKinds) > 0:
		resp.State = RightsizingScanPartial
		resp.Reason = "some_evidence_unavailable"
	default:
		resp.State = RightsizingScanComplete
		if resp.Coverage.WorkloadsWithData == 0 {
			resp.Reason = "no_usage_samples"
		}
	}
	return resp
}

func queryRightsizingScanBatch(ctx context.Context, client rightsizingScanQuerier, batch []scanWorkload, now time.Time) scanBatchEvidence {
	queries := buildRightsizingScanQueries(batch)
	out := scanBatchEvidence{
		cpu: map[scanKey][]float64{}, memory: map[scanKey][]float64{}, throttle: map[scanKey][]float64{},
		restarts: map[scanKey]float64{}, terminations: map[scanKey]terminationEvidence{}, errors: map[string]error{},
	}
	start := now.Add(-rightsizingWindow)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2)
	for _, key := range []string{"cpu", "memory", "throttle"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				out.errors[key] = ctx.Err()
				mu.Unlock()
				return
			}
			result, err := client.QueryRange(ctx, queries[key], start, now, rightsizingStep)
			<-sem
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.errors[key] = err
				return
			}
			values := scanMatrixValues(result)
			switch key {
			case "cpu":
				out.cpu = values
			case "memory":
				out.memory = values
			case "throttle":
				out.throttle = values
			}
		}()
	}
	wg.Wait()
	for _, key := range []string{"restart_activity", "termination_history"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				out.errors[key] = ctx.Err()
				mu.Unlock()
				return
			}
			result, err := client.Query(ctx, queries[key])
			<-sem
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out.errors[key] = err
				return
			}
			if key == "restart_activity" {
				out.restarts = scanVectorValues(result)
			} else {
				out.terminations = scanTerminationEvidence(result)
			}
		}()
	}
	wg.Wait()
	return out
}

func buildRightsizingScanQueries(batch []scanWorkload) map[string]string {
	owner := rightsizingScanOwnerVector(batch)
	namespaces := batchNamespaces(batch)
	nsMatcher := labelRegexMatcher("namespace", namespaces)
	group := "namespace,workload_kind,workload,container"
	cpu := fmt.Sprintf(`max by (%s) (rate(container_cpu_usage_seconds_total{%s,container!="",container!="POD"}[5m]) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	memory := fmt.Sprintf(`max by (%s) (container_memory_working_set_bytes{%s,container!="",container!="POD"} * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	throttled := fmt.Sprintf(`sum by (%s) (rate(container_cpu_cfs_throttled_periods_total{%s,container!="",container!="POD"}[5m]) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	periods := fmt.Sprintf(`sum by (%s) (rate(container_cpu_cfs_periods_total{%s,container!="",container!="POD"}[5m]) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	restarts := fmt.Sprintf(`max by (%s,pod) (kube_pod_container_status_restarts_total{%s,container!=""} * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, owner)
	terminations := fmt.Sprintf(`max by (%s,pod,reason) (kube_pod_container_status_last_terminated_timestamp{%s,container!=""} * on (namespace,pod,container) group_left(reason) max by (namespace,pod,container,reason) (kube_pod_container_status_last_terminated_reason{%s,container!=""}) * on (namespace,pod) group_left(workload_kind,workload) (%s))`, group, nsMatcher, nsMatcher, owner)
	return map[string]string{
		"cpu":                 cpu,
		"memory":              memory,
		"throttle":            fmt.Sprintf(`(%s) / (%s)`, throttled, periods),
		"restart_activity":    fmt.Sprintf(`sum by (%s) (increase((%s)[7d:5m]))`, group, restarts),
		"termination_history": fmt.Sprintf(`max by (%s,reason) (max_over_time((%s)[7d:5m])) > (time() - 604800)`, group, terminations),
	}
}

func rightsizingScanOwnerVector(batch []scanWorkload) string {
	byKindNamespace := map[string]map[string][]string{}
	for _, workload := range batch {
		if byKindNamespace[workload.kind] == nil {
			byKindNamespace[workload.kind] = map[string][]string{}
		}
		byKindNamespace[workload.kind][workload.namespace] = append(byKindNamespace[workload.kind][workload.namespace], workload.name)
	}
	var terms []string
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		namespaces := byKindNamespace[kind]
		var namespaceNames []string
		for namespace := range namespaces {
			namespaceNames = append(namespaceNames, namespace)
		}
		sort.Strings(namespaceNames)
		for _, namespace := range namespaceNames {
			names := namespaces[namespace]
			sort.Strings(names)
			ns := prom.SanitizeLabelValue(namespace)
			namePattern := exactRegex(names)
			if kind == "Deployment" {
				right := fmt.Sprintf(`label_replace(max by (namespace,replicaset,owner_name) (kube_replicaset_owner{namespace="%s",owner_kind="Deployment",owner_name=~"%s",owner_is_controller="true"}), "workload", "$1", "owner_name", "(.*)")`, ns, namePattern)
				joined := fmt.Sprintf(`label_replace(max by (namespace,pod,owner_name) (kube_pod_owner{namespace="%s",owner_kind="ReplicaSet",owner_is_controller="true"}), "replicaset", "$1", "owner_name", "(.*)") * on (namespace,replicaset) group_left(workload) (%s)`, ns, right)
				terms = append(terms, fmt.Sprintf(`label_replace((%s), "workload_kind", "Deployment", "workload", ".*")`, joined))
				continue
			}
			owner := fmt.Sprintf(`label_replace(max by (namespace,pod,owner_name) (kube_pod_owner{namespace="%s",owner_kind="%s",owner_name=~"%s",owner_is_controller="true"}), "workload", "$1", "owner_name", "(.*)")`, ns, kind, namePattern)
			terms = append(terms, fmt.Sprintf(`label_replace((%s), "workload_kind", "%s", "workload", ".*")`, owner, kind))
		}
	}
	return strings.Join(terms, " or ")
}

func labelRegexMatcher(label string, values []string) string {
	return fmt.Sprintf(`%s=~"%s"`, label, exactRegex(values))
}

func exactRegex(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, prom.EscapeRegexMeta(prom.SanitizeLabelValue(value)))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func batchNamespaces(batch []scanWorkload) []string {
	seen := map[string]bool{}
	for _, workload := range batch {
		seen[workload.namespace] = true
	}
	values := make([]string, 0, len(seen))
	for namespace := range seen {
		values = append(values, namespace)
	}
	sort.Strings(values)
	return values
}

func scanMatrixValues(result *prom.QueryResult) map[scanKey][]float64 {
	values := map[scanKey][]float64{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		key, ok := scanSeriesKey(series.Labels)
		if !ok {
			continue
		}
		for _, point := range series.DataPoints {
			if !math.IsNaN(point.Value) && !math.IsInf(point.Value, 0) {
				values[key] = append(values[key], point.Value)
			}
		}
	}
	return values
}

func scanVectorValues(result *prom.QueryResult) map[scanKey]float64 {
	values := map[scanKey]float64{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		key, ok := scanSeriesKey(series.Labels)
		if ok && len(series.DataPoints) > 0 && !math.IsNaN(series.DataPoints[0].Value) && !math.IsInf(series.DataPoints[0].Value, 0) {
			values[key] = series.DataPoints[0].Value
		}
	}
	return values
}

func scanTerminationEvidence(result *prom.QueryResult) map[scanKey]terminationEvidence {
	values := map[scanKey]terminationEvidence{}
	if result == nil {
		return values
	}
	for _, series := range result.Series {
		key, ok := scanSeriesKey(series.Labels)
		if !ok || len(series.DataPoints) == 0 || series.DataPoints[0].Value <= 0 {
			continue
		}
		evidence := values[key]
		evidence.Any = true
		evidence.OOM = evidence.OOM || series.Labels["reason"] == "OOMKilled"
		values[key] = evidence
	}
	return values
}

func scanSeriesKey(labels map[string]string) (scanKey, bool) {
	key := scanKey{namespace: labels["namespace"], kind: labels["workload_kind"], workload: labels["workload"], container: labels["container"]}
	return key, key.namespace != "" && key.kind != "" && key.workload != "" && key.container != ""
}

func buildScanWorkload(input scanWorkload, evidence scanBatchEvidence) RightsizingScanWorkload {
	out := RightsizingScanWorkload{Kind: input.kind, Namespace: input.namespace, Name: input.name, Replicas: input.replicas, ScaledToZero: input.workload.scaledToZero, Rows: make([]RightsizingRow, 0, len(input.workload.containers)*2)}
	expected := int(rightsizingWindow/rightsizingStep) + 1
	for _, container := range input.workload.containers {
		key := scanKey{namespace: input.namespace, kind: input.kind, workload: input.name, container: container.name}
		for _, resourceName := range []string{"cpu", "memory"} {
			row := buildScanRow(container, resourceName, key, expected, input.workload, evidence)
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

func buildScanRow(container containerSpec, resourceName string, key scanKey, expected int, workload rightsizingWorkload, evidence scanBatchEvidence) RightsizingRow {
	row := RightsizingRow{
		Container: container.name, Resource: resourceName, Fit: FitInsufficientHistory, Confidence: ConfidenceLow,
		ExpectedSamples: expected, HPAManaged: workload.hpaManaged[resourceName], HPAEvidenceAvailable: workload.hpaAvailable,
	}
	var request, limit = container.cpuReq, container.cpuLim
	statistic := "P95"
	series := evidence.cpu[key]
	if resourceName == "memory" {
		request, limit = container.memReq, container.memLim
		statistic = "Max"
		series = evidence.memory[key]
		row.CurrentPodOOM = workload.currentPodOOM[container.name]
		if evidence.errors["restart_activity"] == nil && evidence.errors["termination_history"] == nil {
			restartActivity, restartAvailable := evidence.restarts[key]
			termination := evidence.terminations[key]
			row.WindowOOMEvidence = termination.OOM
			row.OOMEvidenceAvailable = termination.OOM || (restartAvailable && (restartActivity <= 0 || termination.Any))
		}
	}
	setCurrentQuantities(&row, request, limit, resourceName)
	if queryErr := evidence.errors[resourceName]; queryErr != nil {
		row.QueryError = "usage query failed"
		return row
	}
	if len(series) == 0 {
		return row
	}
	observed := percentile(series, 0.95)
	if resourceName == "memory" {
		observed = maxFinite(series)
	} else {
		peak := percentile(series, 0.99)
		row.Peak = &ObservedStatistic{Name: "P99", Value: peak, Formatted: formatObservedValue(peak, resourceName)}
		row.Bursty = isBurstyCPU(observed, peak)
	}
	row.Observed = &ObservedStatistic{Name: statistic, Value: observed, Formatted: formatObservedValue(observed, resourceName)}
	row.SampleCount = len(series)
	row.Coverage = math.Min(float64(row.SampleCount)/float64(expected), 1)
	row.Confidence = confidenceFor(row.SampleCount, row.Coverage, OwnerCoverageKSMHistory)
	if resourceName == "cpu" && evidence.errors["throttle"] == nil {
		if values := evidence.throttle[key]; len(values) > 0 {
			value := maxFinite(values)
			row.ThrottleAvailable = true
			row.ThrottleRatio = &value
		}
	}
	if row.SampleCount < rightsizingMinSamples {
		row.RecommendationReason = "insufficient_history"
		return row
	}
	classifyRightsizingFit(&row, observed, request, limit, resourceName)
	return row
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	if len(ordered) == 1 {
		return ordered[0]
	}
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	weight := position - float64(lower)
	return ordered[lower]*(1-weight) + ordered[upper]*weight
}

func maxFinite(values []float64) float64 {
	maximum := -math.MaxFloat64
	for _, value := range values {
		if !math.IsNaN(value) && !math.IsInf(value, 0) && value > maximum {
			maximum = value
		}
	}
	if maximum == -math.MaxFloat64 {
		return 0
	}
	return maximum
}

func workloadHasData(workload RightsizingScanWorkload) bool {
	for _, row := range workload.Rows {
		if row.Observed != nil {
			return true
		}
	}
	return false
}

func workloadHasUnavailableOOMEvidence(workload RightsizingScanWorkload) bool {
	for _, row := range workload.Rows {
		if row.Resource == "memory" && row.RecommendationReason == "oom_evidence_unavailable" {
			return true
		}
	}
	return false
}

func appendScanWarning(resp *RightsizingScanResponse, code, message string) {
	for _, warning := range resp.Warnings {
		if warning.Code == code {
			return
		}
	}
	resp.Warnings = append(resp.Warnings, RightsizingScanWarning{Code: code, Message: message})
	sort.Slice(resp.Warnings, func(i, j int) bool { return resp.Warnings[i].Code < resp.Warnings[j].Code })
}

func hasScanKind(workloads []scanWorkload, kind string) bool {
	for _, workload := range workloads {
		if workload.kind == kind {
			return true
		}
	}
	return false
}

func withoutScanKind(workloads []scanWorkload, kind string) []scanWorkload {
	out := workloads[:0]
	for _, workload := range workloads {
		if workload.kind != kind {
			out = append(out, workload)
		}
	}
	return out
}

func appendUniqueSorted(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

func sortScanWorkloads(workloads []scanWorkload) {
	sort.Slice(workloads, func(i, j int) bool {
		if workloads[i].namespace != workloads[j].namespace {
			return workloads[i].namespace < workloads[j].namespace
		}
		if workloads[i].kind != workloads[j].kind {
			return workloads[i].kind < workloads[j].kind
		}
		return workloads[i].name < workloads[j].name
	})
}

func snapshotScanWorkloads(cache *k8s.ResourceCache, scopes map[string][]string) ([]scanWorkload, []string) {
	workloads := map[string]*scanWorkload{}
	var unavailable []string
	add := func(kind, namespace, name string, replicas int, podSpec *corev1.PodSpec, scaledToZero bool) {
		key := workloadIdentity(kind, namespace, name)
		workloads[key] = &scanWorkload{kind: kind, namespace: namespace, name: name, replicas: replicas, workload: rightsizingWorkload{
			containers: extractRuntimeContainers(podSpec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, scaledToZero: scaledToZero,
		}}
	}

	if namespaces, ok := scopes["Deployment"]; ok {
		lister := cache.Deployments()
		if lister == nil {
			unavailable = append(unavailable, "Deployment")
		} else if items, err := listDeployments(lister, namespaces); err != nil {
			unavailable = append(unavailable, "Deployment")
		} else {
			for _, item := range items {
				replicas := int32(1)
				if item.Spec.Replicas != nil {
					replicas = *item.Spec.Replicas
				}
				add("Deployment", item.Namespace, item.Name, int(replicas), &item.Spec.Template.Spec, replicas == 0)
			}
		}
	}
	if namespaces, ok := scopes["StatefulSet"]; ok {
		lister := cache.StatefulSets()
		if lister == nil {
			unavailable = append(unavailable, "StatefulSet")
		} else if items, err := listStatefulSets(lister, namespaces); err != nil {
			unavailable = append(unavailable, "StatefulSet")
		} else {
			for _, item := range items {
				replicas := int32(1)
				if item.Spec.Replicas != nil {
					replicas = *item.Spec.Replicas
				}
				add("StatefulSet", item.Namespace, item.Name, int(replicas), &item.Spec.Template.Spec, replicas == 0)
			}
		}
	}
	if namespaces, ok := scopes["DaemonSet"]; ok {
		lister := cache.DaemonSets()
		if lister == nil {
			unavailable = append(unavailable, "DaemonSet")
		} else if items, err := listDaemonSets(lister, namespaces); err != nil {
			unavailable = append(unavailable, "DaemonSet")
		} else {
			for _, item := range items {
				add("DaemonSet", item.Namespace, item.Name, int(item.Status.DesiredNumberScheduled), &item.Spec.Template.Spec, item.Status.DesiredNumberScheduled == 0)
			}
		}
	}

	enrichScanHPA(cache, scopes, workloads)
	enrichScanCurrentOOM(cache, scopes, workloads)
	out := make([]scanWorkload, 0, len(workloads))
	for _, workload := range workloads {
		out = append(out, *workload)
	}
	sort.Strings(unavailable)
	return out, unavailable
}

func listDeployments(lister listersappsv1.DeploymentLister, namespaces []string) ([]*appsv1.Deployment, error) {
	if namespaces == nil {
		return lister.List(labels.Everything())
	}
	var out []*appsv1.Deployment
	for _, namespace := range namespaces {
		items, err := lister.Deployments(namespace).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func listStatefulSets(lister listersappsv1.StatefulSetLister, namespaces []string) ([]*appsv1.StatefulSet, error) {
	if namespaces == nil {
		return lister.List(labels.Everything())
	}
	var out []*appsv1.StatefulSet
	for _, namespace := range namespaces {
		items, err := lister.StatefulSets(namespace).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func listDaemonSets(lister listersappsv1.DaemonSetLister, namespaces []string) ([]*appsv1.DaemonSet, error) {
	if namespaces == nil {
		return lister.List(labels.Everything())
	}
	var out []*appsv1.DaemonSet
	for _, namespace := range namespaces {
		items, err := lister.DaemonSets(namespace).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func enrichScanHPA(cache *k8s.ResourceCache, scopes map[string][]string, workloads map[string]*scanWorkload) {
	if !cache.IsDeferredSynced() {
		return
	}
	lister := cache.HorizontalPodAutoscalers()
	if lister == nil {
		return
	}
	namespaces := scanScopeNamespaces(scopes)
	var hpas []*autoscalingv2.HorizontalPodAutoscaler
	if namespaces == nil {
		var err error
		hpas, err = lister.List(labels.Everything())
		if err != nil {
			return
		}
		markScanHPAAvailable(workloads, "")
	} else {
		for _, namespace := range namespaces {
			items, err := lister.HorizontalPodAutoscalers(namespace).List(labels.Everything())
			if err != nil {
				continue
			}
			hpas = append(hpas, items...)
			markScanHPAAvailable(workloads, namespace)
		}
	}
	for _, hpa := range hpas {
		ref := hpa.Spec.ScaleTargetRef
		workload := workloads[workloadIdentity(ref.Kind, hpa.Namespace, ref.Name)]
		if workload == nil {
			continue
		}
		for _, metric := range hpa.Spec.Metrics {
			if metric.Type == autoscalingv2.ResourceMetricSourceType && metric.Resource != nil && metric.Resource.Target.AverageUtilization != nil {
				resourceName := string(metric.Resource.Name)
				if resourceName == "cpu" || resourceName == "memory" {
					workload.workload.hpaManaged[resourceName] = true
				}
			}
		}
	}
}

func markScanHPAAvailable(workloads map[string]*scanWorkload, namespace string) {
	for _, workload := range workloads {
		if namespace == "" || workload.namespace == namespace {
			workload.workload.hpaAvailable = true
		}
	}
}

func enrichScanCurrentOOM(cache *k8s.ResourceCache, scopes map[string][]string, workloads map[string]*scanWorkload) {
	if cache.Pods() == nil {
		return
	}
	namespaces := scanScopeNamespaces(scopes)
	replicaSetOwners := map[string]string{}
	if cache.ReplicaSets() != nil {
		var replicaSets []*appsv1.ReplicaSet
		if namespaces == nil {
			replicaSets, _ = cache.ReplicaSets().List(labels.Everything())
		} else {
			for _, namespace := range namespaces {
				items, _ := cache.ReplicaSets().ReplicaSets(namespace).List(labels.Everything())
				replicaSets = append(replicaSets, items...)
			}
		}
		for _, replicaSet := range replicaSets {
			if owner := metav1.GetControllerOf(replicaSet); owner != nil && owner.Kind == "Deployment" {
				replicaSetOwners[replicaSet.Namespace+"\x00"+replicaSet.Name] = owner.Name
			}
		}
	}
	var pods []*corev1.Pod
	if namespaces == nil {
		pods, _ = cache.Pods().List(labels.Everything())
	} else {
		for _, namespace := range namespaces {
			items, _ := cache.Pods().Pods(namespace).List(labels.Everything())
			pods = append(pods, items...)
		}
	}
	for _, pod := range pods {
		owner := metav1.GetControllerOf(pod)
		if owner == nil {
			continue
		}
		kind, name := owner.Kind, owner.Name
		if owner.Kind == "ReplicaSet" {
			kind, name = "Deployment", replicaSetOwners[pod.Namespace+"\x00"+owner.Name]
		}
		workload := workloads[workloadIdentity(kind, pod.Namespace, name)]
		if workload == nil {
			continue
		}
		collectCurrentPodOOM(workload.workload.currentPodOOM, pod.Status.ContainerStatuses)
		collectCurrentPodOOM(workload.workload.currentPodOOM, pod.Status.InitContainerStatuses)
	}
}

func scanScopeNamespaces(scopes map[string][]string) []string {
	seen := map[string]bool{}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		namespaces, ok := scopes[kind]
		if !ok {
			continue
		}
		if namespaces == nil {
			return nil
		}
		for _, namespace := range namespaces {
			seen[namespace] = true
		}
	}
	out := make([]string, 0, len(seen))
	for namespace := range seen {
		out = append(out, namespace)
	}
	sort.Strings(out)
	return out
}

func workloadIdentity(kind, namespace, name string) string {
	return strings.ToLower(kind) + "\x00" + namespace + "\x00" + name
}
