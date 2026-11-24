package prometheus

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// Sentinel errors distinguish the cache-loading failure modes so handlers can
// map them to the right HTTP status. A user without `list deployments` would
// otherwise see "404 not found" for a workload they simply can't read, and
// conclude Radar is broken.
var (
	errCacheNotReady   = errors.New("resource cache not initialized")
	errKindRBACDenied  = errors.New("kind not listable by service account")
	errWorkloadMissing = errors.New("workload not found")

	ErrPrometheusUnavailable      = errors.New("prometheus is not connected")
	ErrRightsizingKindUnsupported = errors.New("rightsizing supports only Deployment, StatefulSet, and DaemonSet")
)

// Recommendation reasons set only when absent evidence actually suppressed a
// recommendation. A row carrying one is a withheld verdict, not a clean bill of
// health, so every consumer that reports coverage must treat it as a gap.
const (
	ReasonHPAEvidenceUnavailable = rightsizing.ReasonHPAEvidenceUnavailable
	ReasonOOMEvidenceUnavailable = rightsizing.ReasonOOMEvidenceUnavailable
)

// Reasons a single-workload rightsizing answer carries. They are prose because
// the REST response renders them to the user directly, and named here so the
// MCP tool can attach remediation to them instead of matching the literals.
const (
	ReasonWorkloadNoContainers     = "Workload has no runtime containers (init-only or empty spec)."
	ReasonRightsizingQueriesFailed = "Prometheus rightsizing queries failed."
	ReasonNoOwnerSamples           = "No current or retained workload ownership samples are available."
	ReasonNoUsageSamples           = "No workload usage samples are available in the last 7d."
	ReasonPodInventoryUnreadable   = "Radar could not read this workload's pods, and no retained ownership history was available, so there was nothing to measure."
)

type RightsizingFit = rightsizing.RightsizingFit
type RightsizingConfidence = rightsizing.RightsizingConfidence
type OwnerCoverage = rightsizing.OwnerCoverage
type ObservedStatistic = rightsizing.ObservedStatistic
type RightsizingRow = rightsizing.Row

const (
	FitBalanced                  = rightsizing.FitBalanced
	FitOversized                 = rightsizing.FitOversized
	FitUnderRequested            = rightsizing.FitUnderRequested
	FitMissingRequest            = rightsizing.FitMissingRequest
	FitInsufficientHistory       = rightsizing.FitInsufficientHistory
	FitPodLevelRequest           = rightsizing.FitPodLevelRequest
	OwnerCoverageKSMHistory      = rightsizing.OwnerCoverageKSMHistory
	OwnerCoverageCurrentPods     = rightsizing.OwnerCoverageCurrentPods
	ConfidenceLow                = rightsizing.ConfidenceLow
	ConfidenceMedium             = rightsizing.ConfidenceMedium
	ConfidenceHigh               = rightsizing.ConfidenceHigh
	ReasonPodLevelRequest        = rightsizing.ReasonPodLevelRequest
	ReasonRequestsExceedPodLimit = rightsizing.ReasonRequestsExceedPodLimit
)

type RightsizingResponse struct {
	Kind          string        `json:"kind"`
	Namespace     string        `json:"namespace"`
	Name          string        `json:"name"`
	Window        string        `json:"window"`
	Source        string        `json:"source"`
	OwnerCoverage OwnerCoverage `json:"ownerCoverage"`
	Replicas      int           `json:"replicas"`
	ScaledToZero  bool          `json:"scaledToZero"`
	// SampleAvailable means at least one CPU or memory row has an observation.
	// A recommendation additionally requires enough history and safety evidence.
	SampleAvailable bool             `json:"sampleAvailable"`
	ManagedBy       *WorkloadManager `json:"managedBy,omitempty"`
	Rows            []RightsizingRow `json:"rows"`
	Reason          string           `json:"reason,omitempty"`
	// Warnings name the supporting queries that failed. A withheld memory
	// recommendation reads identically whether the OOM-evidence queries
	// answered "no history" or never answered at all, so without these a
	// caller cannot tell an absence of evidence from an absent query — and
	// a scan of the same workload, whose queries did answer, contradicts it.
	Warnings []RightsizingScanWarning `json:"warnings,omitempty"`
	// ClusterScope is how the queries were kept to this cluster in a store
	// that may hold several; absent when no check ran.
	ClusterScope *ClusterScope `json:"clusterScope,omitempty"`
}

const (
	rightsizingWindow = 7 * 24 * time.Hour
	rightsizingStep   = 5 * time.Minute
)

// handleRightsizing returns rightsizing recommendations for a workload's containers.
// Only Deployment / StatefulSet / DaemonSet supported — per-pod rightsizing
// is wrong granularity (recs are per-container-template).
func handleRightsizing(w http.ResponseWriter, r *http.Request) {
	if _, connectionErr := ClientForOperation(); connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !IsRightsizingKind(kind) {
		writeError(w, http.StatusBadRequest, "rightsizing only supported for Deployment, StatefulSet, DaemonSet")
		return
	}

	// Per-user RBAC: the cache is populated under Radar's SA, so without this
	// gate any authenticated user could fetch any namespace's container spec
	// + P95 by guessing names. Use "get" — matches normal resource-detail reads.
	if !canRead(r, "apps", ScanKindResource(kind), namespace, "get") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	resp, err := RightsizingForWorkload(r.Context(), kind, namespace, name)
	if err != nil {
		switch {
		case errors.Is(err, errCacheNotReady):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		case errors.Is(err, errKindRBACDenied):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, errWorkloadMissing):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// The client is gone; nothing to report and nothing worth logging.
			return
		default:
			errorlog.Record("prometheus", "error", "rightsizing: failed to load containers for %s %s/%s: %v", kind, namespace, name, err)
			writeError(w, http.StatusInternalServerError, "failed to load workload containers")
		}
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// RightsizingForWorkload is the shared entry point behind the REST handler and
// the get_rightsizing MCP tool. It performs no per-user RBAC check: callers own
// that gate, because the cache is populated under Radar's ServiceAccount.
func RightsizingForWorkload(ctx context.Context, kind, namespace, name string) (RightsizingResponse, error) {
	client, connectionErr := ClientForOperation()
	if connectionErr != nil {
		return RightsizingResponse{}, connectionErr
	}
	if !IsRightsizingKind(kind) {
		return RightsizingResponse{}, ErrRightsizingKindUnsupported
	}

	workload, err := loadRightsizingWorkload(ctx, kind, namespace, name)
	if err != nil {
		return RightsizingResponse{}, err
	}
	if len(workload.containers) == 0 {
		return RightsizingResponse{
			Kind: kind, Namespace: namespace, Name: name,
			Window: "7d", Source: "radar", SampleAvailable: false,
			Rows:   []RightsizingRow{},
			Reason: ReasonWorkloadNoContainers,
		}, nil
	}

	// The cluster scope check shares the queries' budget.
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// The queries run on one store handle and the check must describe that
	// handle's backend.
	cache := k8s.GetResourceCache()
	store, err := client.bindStore(queryCtx)
	if err != nil {
		if ctx.Err() != nil {
			return RightsizingResponse{}, ctx.Err()
		}
		// With no store to check, the queries fail with the connect error,
		// which is the answer, rather than a cluster scope that never ran.
		return computeRightsizing(queryCtx, store, kind, namespace, name, workload, ViewScope{}), nil
	}
	scope, err := resolveOnConnection(func() bool { return client.ConnectionCurrent(store.conn) }, func() (ViewScope, error) {
		return resolveRightsizingScope(queryCtx, client, singleRightsizingScopeRequest(kind, namespace, workload, cache), cache)
	})
	if err != nil && ctx.Err() != nil {
		return RightsizingResponse{}, ctx.Err()
	}
	if cloud.IsAuthenticatedTunnelRequest(ctx) && scope.State == ViewScopeUnlabeled && !scope.Withheld {
		input := scanWorkload{kind: k8s.CanonicalWorkloadKind(kind), namespace: namespace, name: name, replicas: workload.replicas, workload: workload}
		// Range reads for a large cohort run in bounded chunks. Give them
		// a separate budget after the store and cluster have been verified.
		rangeCtx, stop := context.WithTimeout(ctx, rightsizingScanQueryTimeout)
		defer stop()
		return currentPodRightsizingDetail(rangeCtx, store, input), nil
	}
	return computeRightsizing(queryCtx, store, kind, namespace, name, workload, scope), nil
}

// Use the scan's evidence and policy so a drill-down describes the same current replicas.
func currentPodRightsizingDetail(ctx context.Context, store rightsizingScanQuerier, input scanWorkload) RightsizingResponse {
	evidence := queryCurrentPodRightsizing(ctx, store, input, time.Now())
	measured := buildScanWorkload(input, evidence)
	resp := RightsizingResponse{Kind: input.kind, Namespace: input.namespace, Name: input.name, Window: "7d", Source: "radar",
		OwnerCoverage: OwnerCoverageCurrentPods, Replicas: input.replicas, ScaledToZero: measured.ScaledToZero, ManagedBy: measured.ManagedBy,
		Rows: measured.Rows, ClusterScope: currentPodUIDScope().Wire(), SampleAvailable: workloadHasData(measured)}
	for _, key := range []string{"cpu", "memory", "throttle"} {
		if err := evidence.errors[key]; err != nil {
			warning := RightsizingScanWarning{Code: key + "_query_failed", Message: boundWarningMessage(err.Error())}
			if errors.Is(err, context.DeadlineExceeded) {
				warning = RightsizingScanWarning{Code: key + "_query_timed_out", Message: "The " + key + " history read timed out."}
			}
			resp.Warnings = append(resp.Warnings, warning)
		}
	}
	if !resp.SampleAvailable {
		switch {
		case measured.ScaledToZero:
			resp.Reason = "This workload has no current replicas to measure. Configure the Prometheus cluster scope to use retained workload history."
		case !hasCurrentPodSizingCohort(input):
			resp.Reason = "Radar could not verify a complete set of ready Pods matching the current workload template."
		case errors.Is(evidence.errors["cpu"], context.DeadlineExceeded) || errors.Is(evidence.errors["memory"], context.DeadlineExceeded):
			resp.Reason = "The rightsizing history read timed out before complete replica measurements were available."
		case isIncompleteAnswer(evidence.errors["cpu"]) || isIncompleteAnswer(evidence.errors["memory"]):
			resp.Reason = ReasonRightsizingPartialData
		case evidence.errors["cpu"] != nil || evidence.errors["memory"] != nil:
			resp.Reason = ReasonRightsizingQueriesFailed
		default:
			resp.Reason = "No complete CPU or memory samples matched the current Pods by UID."
		}
	}
	return resp
}

// resolveRightsizingScope decides rightsizing's cluster scope on the client's
// current connection; tests replace it to move the connection mid-check.
var resolveRightsizingScope = func(ctx context.Context, client *Client, req ViewScopeRequest, cache *k8s.ResourceCache) (ViewScope, error) {
	return client.ResolveViewScope(ctx, req, cache)
}

// specReplicas defaults a nil replica count to 1, matching the apiserver: a
// Deployment written without spec.replicas runs one pod, and treating it as
// zero would zero out every impact figure computed from it.
func specReplicas(replicas *int32) int {
	if replicas == nil {
		return 1
	}
	return int(*replicas)
}

// IsRightsizingKind reports whether recommendations exist for a kind. Callers
// check it before authorizing, so an unsupported kind gets that answer instead
// of a denial from a SubjectAccessReview against a resource that cannot exist.
func IsRightsizingKind(kind string) bool {
	return ScanKindResource(kind) != ""
}

type containerSpec struct {
	name   string
	cpuReq *resource.Quantity
	cpuLim *resource.Quantity
	memReq *resource.Quantity
	memLim *resource.Quantity
	// The pod's fixed pod-level requests, shared by every container.
	podCPUReq *resource.Quantity
	podMemReq *resource.Quantity
}

type rightsizingWorkload struct {
	// podSpec is the workload's pod template, for checks that need the whole pod.
	podSpec               *corev1.PodSpec
	containers            []containerSpec
	podNames              []string
	podIdentities         []prom.WorkloadPodIdentity
	podIdentityIncomplete bool
	daemonRevisionMatches map[string]bool
	admittedImages        []string
	podRestartEvidence    map[string]sizingPodRestarts
	qos                   rightsizing.PodQoS
	currentPodOOM         map[string]bool
	unexplainedPodExits   map[string]bool
	hpaManaged            map[string]bool
	hpaAvailable          bool
	replicas              int
	scaledToZero          bool
	// The live pod list could not be read, so podNames and currentPodOOM are
	// empty because nothing answered — not because the workload has no pods.
	liveInventoryUnavailable bool
	managedBy                *WorkloadManager
}

// warmingRetryBudget bounds how long a rightsizing read waits for an informer
// that is still filling. Initial sync takes well under this; the budget exists
// so a cache that never syncs cannot hold the request open.
const warmingRetryBudget = 3 * time.Second

// workloadPodsOnceWarm reads the pods of the workload incarnation the caller
// already observed (uid), waiting out a cache that is still loading. Warming and denial arrive at this call as the same empty pod
// list, but they are not the same thing: warming clears on its own within
// seconds, so surfacing it would put a caveat on the recommendation that the
// next refresh silently removes. A denial never clears, and is returned.
func workloadPodsOnceWarm(ctx context.Context, cache *k8s.ResourceCache, kind, namespace, name string, uid types.UID) ([]*corev1.Pod, error) {
	deadline := time.Now().Add(warmingRetryBudget)
	for {
		pods, err := k8s.WorkloadPodsForUID(cache, kind, namespace, name, "apps", uid)
		if !errors.Is(err, k8s.ErrWorkloadCacheWarming) || time.Now().After(deadline) {
			return pods, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func loadRightsizingWorkload(ctx context.Context, kind, namespace, name string) (rightsizingWorkload, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return rightsizingWorkload{}, errCacheNotReady
	}

	var podTemplate *corev1.PodSpec
	var managedBy *WorkloadManager
	var uid types.UID
	var cohortReady bool
	scaledToZero := false
	replicas := 0
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
		uid = d.UID
		managedBy = DetectWorkloadManager(d)
		replicas = specReplicas(d.Spec.Replicas)
		cohortReady = sizingControllerCohortReady(d, d.Status.ObservedGeneration, int32(replicas), d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.ReadyReplicas)
		scaledToZero = replicas == 0
	case "statefulset":
		if cache.StatefulSets() == nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: statefulsets", errKindRBACDenied)
		}
		ss, err := cache.StatefulSets().StatefulSets(namespace).Get(name)
		if err != nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: statefulset %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &ss.Spec.Template.Spec
		uid = ss.UID
		managedBy = DetectWorkloadManager(ss)
		replicas = specReplicas(ss.Spec.Replicas)
		cohortReady = sizingControllerCohortReady(ss, ss.Status.ObservedGeneration, int32(replicas), ss.Status.Replicas, ss.Status.UpdatedReplicas, ss.Status.ReadyReplicas)
		scaledToZero = replicas == 0
	case "daemonset":
		if cache.DaemonSets() == nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: daemonsets", errKindRBACDenied)
		}
		ds, err := cache.DaemonSets().DaemonSets(namespace).Get(name)
		if err != nil {
			return rightsizingWorkload{}, fmt.Errorf("%w: daemonset %s/%s", errWorkloadMissing, namespace, name)
		}
		podTemplate = &ds.Spec.Template.Spec
		uid = ds.UID
		managedBy = DetectWorkloadManager(ds)
		replicas = int(ds.Status.DesiredNumberScheduled)
		cohortReady = sizingControllerCohortReady(ds, ds.Status.ObservedGeneration, int32(replicas), ds.Status.CurrentNumberScheduled, ds.Status.UpdatedNumberScheduled, ds.Status.NumberReady) && ds.Status.NumberMisscheduled == 0
		// Scans skip a zero the controller has observed, but a caller naming
		// one gets its retained history, which is exactly the scaled-to-zero
		// case: a node pool scaled away.
		scaledToZero = ds.Status.DesiredNumberScheduled == 0
	}

	if podTemplate == nil {
		return rightsizingWorkload{}, errCacheNotReady
	}

	hpaManaged, hpaAvailable := loadHPAManagedResources(ctx, cache, kind, namespace, name)
	workload := rightsizingWorkload{
		podSpec:               podTemplate,
		podIdentityIncomplete: !cohortReady,
		containers:            extractRuntimeContainers(podTemplate),
		currentPodOOM:         map[string]bool{},
		hpaManaged:            hpaManaged,
		hpaAvailable:          hpaAvailable,
		replicas:              replicas,
		scaledToZero:          scaledToZero,
		managedBy:             managedBy,
	}
	pods, err := workloadPodsOnceWarm(ctx, cache, kind, namespace, name, uid)
	if ctx.Err() != nil {
		// The caller went away mid-wait. Reporting that as an unreadable
		// inventory would answer a question nobody is listening for, and would
		// send the rest of this request into Prometheus on a dead context.
		return rightsizingWorkload{}, ctx.Err()
	}
	if err != nil {
		// The recommendation itself survives: it comes from seven days of
		// kube-state-metrics ownership, which never needed the pod list. What
		// does not survive is everything read from the live pods — the pod
		// names, and whether those pods have been OOM-killed — so the row is
		// marked rather than served as if the workload simply had no pods.
		workload.liveInventoryUnavailable = true
		return workload, nil
	}
	collectPodIdentities := cloud.IsAuthenticatedTunnelRequest(ctx)
	windowEnd := time.Now()
	for _, pod := range pods {
		workload.qos.Observe(pod.Status.QOSClass)
		workload.podNames = append(workload.podNames, pod.Name)
		if collectPodIdentities {
			workload.observeSizingPodIdentity(ctx, cache, pod)
		}
		workload.observePodTerminations(pod, windowEnd)
	}
	sort.Strings(workload.podNames)
	return workload, nil
}

func (w *rightsizingWorkload) observePodTerminations(pod *corev1.Pod, windowEnd time.Time) {
	inWindow := func(t time.Time) bool { return !t.Before(windowEnd.Add(-rightsizingWindow)) && !t.After(windowEnd) }
	bornInWindow := inWindow(pod.CreationTimestamp.Time)
	// SIGKILL alone cannot distinguish a manual kill from an OOM.
	unexplained := func(exit *corev1.ContainerStateTerminated) bool {
		return exit == nil || exit.Reason == "" || exit.Reason == "ContainerStatusUnknown" || exit.Reason == "Unknown" || exit.ExitCode == 137 || exit.Signal == 9
	}
	recent := func(exit *corev1.ContainerStateTerminated) bool {
		return bornInWindow || (exit != nil && inWindow(exit.FinishedAt.Time))
	}
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		for _, status := range statuses {
			current, last := status.State.Terminated, status.LastTerminationState.Terminated
			if (current != nil && current.Reason == "OOMKilled") || (last != nil && last.Reason == "OOMKilled") {
				w.currentPodOOM[status.Name] = true
			}
			// A counter already positive at its first scrape has no visible increase.
			// A Pod born inside the window dates its restarts even when the scrape cannot.
			if pod.UID == "" {
				continue
			}
			// A container can die and the Pod disappear before the kubelet restarts it.
			currentGap := current != nil && unexplained(current) && recent(current)
			lastGap := (status.RestartCount > 0 || last != nil) && unexplained(last) && recent(last)
			earlierGap := bornInWindow && status.RestartCount > 1
			if !currentGap && !lastGap && !earlierGap {
				continue
			}
			if w.unexplainedPodExits == nil {
				w.unexplainedPodExits = map[string]bool{}
			}
			w.unexplainedPodExits[status.Name] = true
		}
	}
}

func (w rightsizingWorkload) hasOOMEvidence(container string, restartActivity float64, restartAvailable bool, termination terminationEvidence) bool {
	if termination.OOM {
		return true
	}
	// The aggregated historical reason could describe another replica or just
	// the last of several restarts. It cannot clear a known gap in a live Pod.
	return restartAvailable && !w.unexplainedPodExits[container] && (restartActivity <= 0 || termination.Any)
}

func loadHPAManagedResources(ctx context.Context, cache *k8s.ResourceCache, kind, namespace, name string) (map[string]bool, bool) {
	managed := map[string]bool{}
	if cache.HorizontalPodAutoscalers() == nil {
		return managed, false
	}
	// Only this informer's sync state says whether the HPA inventory is
	// readable. The deferred phase as a whole reports false while any unrelated
	// kind is warming, and permanently once one fails, which would withhold a
	// correct recommendation for a reason that has nothing to do with
	// autoscaling.
	if !waitForInformerSynced(ctx, cache, string(k8score.HorizontalPodAutoscalers)) {
		return managed, false
	}
	// A namespace-scoped informer answers an empty list for every namespace it
	// does not watch, with no error. Treating that as "no autoscaler here" is
	// what the availability flag exists to prevent: it gates whether a
	// reduction may be suggested at all.
	if !cache.KindCoversNamespace(string(k8score.HorizontalPodAutoscalers), namespace) {
		return managed, false
	}
	hpas, err := cache.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).List(labels.Everything())
	if err != nil {
		return managed, false
	}
	for _, hpa := range hpas {
		AddHPATargetResources(managed, hpa, kind, name)
	}
	return managed, true
}

func AddHPATargetResources(managed rightsizing.HPATargets, hpa *autoscalingv2.HorizontalPodAutoscaler, kind, name string) {
	ref := hpa.Spec.ScaleTargetRef
	if !strings.EqualFold(ref.Kind, kind) || ref.Name != name {
		return
	}
	for _, metric := range hpa.Spec.Metrics {
		var resourceName corev1.ResourceName
		var container string
		switch {
		case metric.Type == autoscalingv2.ResourceMetricSourceType && metric.Resource != nil && metric.Resource.Target.AverageUtilization != nil:
			resourceName = metric.Resource.Name
		case metric.Type == autoscalingv2.ContainerResourceMetricSourceType && metric.ContainerResource != nil && metric.ContainerResource.Target.AverageUtilization != nil:
			resourceName = metric.ContainerResource.Name
			container = metric.ContainerResource.Container
		}
		if resourceName == corev1.ResourceCPU || resourceName == corev1.ResourceMemory {
			managed.Add(container, string(resourceName))
		}
	}
}

// extractRuntimeContainers returns containers + native-sidecar init containers
// (initContainers with restartPolicy=Always, GA in 1.33). Native sidecars run
// for the pod's lifetime and must be included alongside regular containers;
// pure init containers run to completion and are excluded.
func extractRuntimeContainers(podSpec *corev1.PodSpec) []containerSpec {
	containers := make([]containerSpec, 0, len(podSpec.Containers))
	for c := range rightsizing.RuntimeContainers(podSpec) {
		containers = append(containers, extractContainerSpec(*c))
	}
	podCPU := rightsizing.FixedPodLevelRequest(podSpec, corev1.ResourceCPU)
	podMem := rightsizing.FixedPodLevelRequest(podSpec, corev1.ResourceMemory)
	for i := range containers {
		containers[i].podCPUReq, containers[i].podMemReq = podCPU, podMem
	}
	return containers
}

func podLevelRequestFor(container containerSpec, resourceName string) *resource.Quantity {
	if resourceName == "cpu" {
		return container.podCPUReq
	}
	return container.podMemReq
}

func extractContainerSpec(c corev1.Container) containerSpec {
	out := containerSpec{name: c.Name}
	requests := rightsizing.EffectiveContainerRequests(c.Resources)
	if q, ok := requests[corev1.ResourceCPU]; ok {
		qc := q.DeepCopy()
		out.cpuReq = &qc
	}
	if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
		qc := q.DeepCopy()
		out.cpuLim = &qc
	}
	if q, ok := requests[corev1.ResourceMemory]; ok {
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

// computeRightsizing sizes one workload with the queries kept to this cluster
// by scope. A zero scope leaves every selector unfiltered.
func computeRightsizing(ctx context.Context, client rightsizingQuerier, kind, namespace, name string, workload rightsizingWorkload, scope ViewScope) RightsizingResponse {
	var clusterScope *ClusterScope
	if scope.State != "" {
		clusterScope = scope.Wire()
	}
	if scope.Withheld {
		return RightsizingResponse{
			Kind: kind, Namespace: namespace, Name: name, Window: "7d", Source: "radar",
			Replicas: workload.replicas, ScaledToZero: workload.scaledToZero, ManagedBy: workload.managedBy,
			Rows: []RightsizingRow{}, Reason: ReasonClusterScopeWithheld, ClusterScope: clusterScope,
		}
	}
	client = strictRightsizingQuerier{client}
	selection, coverage, ownerErr := workloadSelection(ctx, client, kind, namespace, name, workload.podNames, scope)
	if isIncompleteAnswer(ownerErr) {
		// Ownership answered in part may have left out Pods the workload
		// ran this week; the current Pods alone would size it from some of
		// its history as confidently as from all of it.
		return RightsizingResponse{
			Kind: kind, Namespace: namespace, Name: name, Window: "7d", Source: "radar",
			Replicas: workload.replicas, ScaledToZero: workload.scaledToZero, ManagedBy: workload.managedBy,
			Rows: []RightsizingRow{}, Reason: ReasonRightsizingPartialData, ClusterScope: clusterScope,
			Warnings: []RightsizingScanWarning{{Code: "owner_metrics_query_failed", Message: boundWarningMessage(ownerErr.Error())}},
		}
	}
	queries := buildRightsizingQueries(namespace, selection)
	results := runRightsizingQueries(ctx, client, queries)
	expected := int(rightsizingWindow / rightsizingStep)
	resp := RightsizingResponse{
		Kind: kind, Namespace: namespace, Name: name, Window: "7d", Source: "radar",
		OwnerCoverage: coverage, Replicas: workload.replicas, ScaledToZero: workload.scaledToZero, ManagedBy: workload.managedBy,
		Rows: make([]RightsizingRow, 0, len(workload.containers)*2), ClusterScope: clusterScope,
	}
	for _, container := range workload.containers {
		for _, resourceName := range []string{"cpu", "memory"} {
			row := buildRightsizingRow(container, resourceName, expected, coverage, workload, results)
			resp.Rows = append(resp.Rows, row)
		}
	}
	rightsizing.Finalize(resp.Rows, workload.podSpec, workload.qos, workload.scaledToZero)
	resp.Warnings = evidenceQueryWarnings(results)
	for _, row := range resp.Rows {
		if row.Observed != nil {
			resp.SampleAvailable = true
			break
		}
	}
	if !resp.SampleAvailable {
		usage := []error{results["cpu_stat"].err, results["cpu_coverage"].err, results["memory_stat"].err, results["memory_coverage"].err}
		if slices.ContainsFunc(usage, isIncompleteAnswer) {
			resp.Reason = ReasonRightsizingPartialData
		} else if slices.ContainsFunc(usage, func(err error) bool { return err != nil }) {
			resp.Reason = ReasonRightsizingQueriesFailed
		} else if workload.liveInventoryUnavailable && coverage == OwnerCoverageCurrentPods {
			// Without retained ownership the selection falls back to the current
			// pods, and those could not be listed — so the query matched nothing
			// by construction. "No samples" would send the reader after missing
			// metrics instead of the unreadable inventory.
			resp.Reason = ReasonPodInventoryUnreadable
		} else if len(workload.podNames) == 0 && coverage == OwnerCoverageCurrentPods {
			resp.Reason = ReasonNoOwnerSamples
		} else {
			resp.Reason = ReasonNoUsageSamples
		}
	}
	return resp
}

// evidenceQueryWarnings names the failed queries behind a workload-scope
// answer, using the same codes a scan reports so a caller comparing the two
// scopes reads one vocabulary. The usage queries are included because a row's
// queryError points at these codes.
func evidenceQueryWarnings(results map[string]queryOutcome) []RightsizingScanWarning {
	codes := []struct{ query, code string }{
		{"cpu_stat", "cpu_query_failed"},
		{"memory_stat", "memory_query_failed"},
		// cpu_peak decides Bursty, which gates manual review and tightens the
		// clamp. Its failure is swallowed at the row — an absent peak reads the
		// same as a container that never had one — so only a warning shows it.
		{"cpu_peak", "cpu_peak_query_failed"},
		{"cpu_coverage", "cpu_coverage_query_failed"},
		{"memory_coverage", "memory_coverage_query_failed"},
		// A failed read leaves the row unable to report observed throttling.
		{"throttle", "throttle_query_failed"},
		{"restart_activity", "restart_activity_query_failed"},
		{"termination_history", "termination_history_query_failed"},
	}
	var warnings []RightsizingScanWarning
	for _, entry := range codes {
		outcome, ok := results[entry.query]
		if !ok || outcome.err == nil {
			continue
		}
		warnings = append(warnings, RightsizingScanWarning{
			Code:    entry.code,
			Message: boundWarningMessage(outcome.err.Error()),
		})
	}
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].Code < warnings[j].Code })
	return warnings
}

type metricSelection struct {
	join       string
	podPattern string
	scope      ViewScope
}

// workloadSelection selects the workload's Pods by retained ownership, else
// by its current Pods. The error is the ownership probe's, when it failed.
func workloadSelection(ctx context.Context, client rightsizingQuerier, kind, namespace, name string, podNames []string, scope ViewScope) (metricSelection, OwnerCoverage, error) {
	owner := ownerSelection(kind, namespace, name, scope)
	var probeErr error
	if owner != "" {
		probe := fmt.Sprintf(`sum(count_over_time((%s)[7d:5m]))`, owner)
		result, err := client.Query(ctx, probe)
		if err == nil && firstValue(result) != nil && *firstValue(result) > 0 {
			return metricSelection{join: fmt.Sprintf(`* on (namespace,pod) group_left() (%s)`, owner), scope: scope}, OwnerCoverageKSMHistory, nil
		}
		probeErr = err
	}
	if len(podNames) == 0 {
		return metricSelection{podPattern: "a^", scope: scope}, OwnerCoverageCurrentPods, probeErr
	}
	escaped := make([]string, 0, len(podNames))
	for _, podName := range podNames {
		escaped = append(escaped, prom.EscapeRegexMeta(prom.SanitizeLabelValue(podName)))
	}
	return metricSelection{podPattern: fmt.Sprintf("^(%s)$", strings.Join(escaped, "|")), scope: scope}, OwnerCoverageCurrentPods, probeErr
}

func ownerSelection(kind, namespace, name string, scope ViewScope) string {
	query, _ := (prom.WorkloadHistory{Kind: kind, Namespace: namespace, Name: name}).OwnerQueryFor(scope.Filter)
	return query
}

func buildRightsizingQueries(namespace string, selection metricSelection) map[string]string {
	ns := prom.SanitizeLabelValue(namespace)
	podMatcher := ""
	if selection.podPattern != "" {
		podMatcher = fmt.Sprintf(`,pod=~"%s"`, selection.podPattern)
	}
	f := func(metric string) string { return scopeMatchers(selection.scope, metric) }
	cpu := fmt.Sprintf(`max by (container) (rate(container_cpu_usage_seconds_total{namespace="%s"%s,container!="",container!="POD",cpu=~"total|"%s}[5m]) %s)`, ns, podMatcher, f(metricCPUUsage), selection.join)
	memory := fmt.Sprintf(`max by (container) (container_memory_working_set_bytes{namespace="%s"%s,container!="",container!="POD"%s} %s)`, ns, podMatcher, f(metricMemoryWorkingSet), selection.join)
	throttled := fmt.Sprintf(`sum by (container) (rate(container_cpu_cfs_throttled_periods_total{namespace="%s"%s,container!="",container!="POD"%s}[5m]) %s)`, ns, podMatcher, f(metricThrottledPeriods), selection.join)
	periods := fmt.Sprintf(`sum by (container) (rate(container_cpu_cfs_periods_total{namespace="%s"%s,container!="",container!="POD"%s}[5m]) %s)`, ns, podMatcher, f(metricCFSPeriods), selection.join)
	restarts := fmt.Sprintf(`max by (namespace,pod,container) (kube_pod_container_status_restarts_total{namespace="%s"%s,container!=""%s} %s)`, ns, podMatcher, f(metricRestarts), selection.join)
	terminations := fmt.Sprintf(`max by (namespace,pod,container,reason) (kube_pod_container_status_last_terminated_timestamp{namespace="%s"%s,container!=""%s} * on (namespace,pod,container) group_left(reason) max by (namespace,pod,container,reason) (kube_pod_container_status_last_terminated_reason{namespace="%s"%s,container!=""%s}) %s)`, ns, podMatcher, f(metricTerminatedAt), ns, podMatcher, f(metricTerminatedReason), selection.join)
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

func buildRightsizingRow(container containerSpec, resourceName string, expected int, ownerCoverage OwnerCoverage, workload rightsizingWorkload, results map[string]queryOutcome) (row RightsizingRow) {
	defer rightsizing.MarkPodLevelRequest(&row)
	row = RightsizingRow{Container: container.name, Resource: resourceName, Fit: FitInsufficientHistory, Confidence: ConfidenceLow, ExpectedSamples: expected, HPAManaged: rightsizing.HPATargets(workload.hpaManaged).Controls(container.name, resourceName), HPAEvidenceAvailable: workload.hpaAvailable, LiveInventoryUnavailable: workload.liveInventoryUnavailable}
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
		if terminations.err == nil {
			restartActivity, restartAvailable := restarts.values[container.name]
			termination := terminations.terminations[container.name]
			row.WindowOOMEvidence = termination.OOM
			row.OOMEvidenceAvailable = workload.hasOOMEvidence(container.name, restartActivity, restarts.err == nil && restartAvailable, termination)
		}
	}
	rightsizing.SetCurrentQuantities(&row, req, lim, resourceName)
	setPodLevelRequest(&row, podLevelRequestFor(container, resourceName), resourceName)
	stat := results[resourceName+"_stat"]
	coverage := results[resourceName+"_coverage"]
	if stat.err != nil {
		row.QueryError = RowUsageQueryFailed
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
	row.Observed = &ObservedStatistic{Name: statistic, Value: observed, Formatted: rightsizing.FormatObservedValue(observed, resourceName)}
	if resourceName == "cpu" {
		peak := results["cpu_peak"]
		if peak.err == nil {
			if value, present := peak.values[container.name]; present {
				row.Peak = &ObservedStatistic{Name: "P99", Value: value, Formatted: rightsizing.FormatObservedValue(value, resourceName)}
				row.Bursty = rightsizing.IsBurstyCPU(observed, value)
			}
		}
	}
	row.SampleCount = int(coverage.values[container.name])
	row.Coverage = math.Min(float64(row.SampleCount)/float64(expected), 1)
	row.Confidence = rightsizing.ConfidenceFor(row.SampleCount, row.Coverage, ownerCoverage)
	if resourceName == "cpu" {
		throttle := results["throttle"]
		if throttle.err == nil {
			if value, present := throttle.values[container.name]; present {
				row.ThrottleAvailable = true
				row.ThrottleRatio = &value
			}
		}
	}
	if row.SampleCount < rightsizing.RightsizingMinSamples {
		row.RecommendationReason = "insufficient_history"
		return row
	}
	rightsizing.ClassifyRightsizingFit(&row, observed, req, lim, resourceName)
	return row
}

func setPodLevelRequest(row *RightsizingRow, podRequest *resource.Quantity, resourceName string) {
	if podRequest != nil {
		formatted := podRequest.String()
		value := rightsizing.QuantityToFloat(*podRequest, resourceName)
		row.PodLevelRequest = &formatted
		row.PodLevelRequestValue = &value
	}
}

// RowUsageQueryFailed is the queryError a row carries when its own usage query
// failed, as opposed to a supporting query such as sample coverage.
const RowUsageQueryFailed = "usage query failed"

// PVCUsageResponse is returned by the PVC usage endpoint.
type PVCUsageResponse struct {
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	Used      int64   `json:"used"`     // bytes
	Capacity  int64   `json:"capacity"` // bytes
	Ratio     float64 `json:"ratio"`    // 0.0 - 1.0
	HasData   bool    `json:"hasData"`
	Status    string  `json:"status"` // available, no_series, invalid_data, query_failed, ambiguous_scope, scope_mismatch
}

// handlePVCUsage returns current usage for a PVC, computed from
// kubelet_volume_stats_{used,capacity}_bytes. Missing measurements do not
// establish whether the driver reports volume stats or kubelet is scraped.
func handlePVCUsage(w http.ResponseWriter, r *http.Request) {
	client, connectionErr := ClientForOperation()
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}

	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if !canRead(r, "", "persistentvolumeclaims", namespace, "get") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	g := client.ensureGKECharts(r.Context())
	var resp PVCUsageResponse
	if g == nil || g.fallback {
		resp = standardPVCUsage(r.Context(), client, namespace, name)
	}
	if g != nil && (!g.fallback || resp.Status == "no_series" || resp.Status == PVCUsageAmbiguous || resp.Status == PVCUsageScopeMismatch) {
		native := g.pvc(r.Context(), namespace, name)
		if !g.fallback || native.HasData || resp.Status == "no_series" {
			resp = native
		}
		if g != client.gkeChartStore() {
			writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func standardPVCUsage(ctx context.Context, client *Client, namespace, name string) PVCUsageResponse {
	resp := PVCUsageResponse{Namespace: namespace, Name: name, Status: PVCUsageQueryFailed}
	matchers, _, err := client.resolveScope(ctx, namespace, pvcScopeProbe(namespace, []string{name}, 0), nil, k8s.GetResourceCache())
	if failed, bad := pvcScopeFailure(err); bad {
		if failed.Status == PVCUsageQueryFailed {
			errorlog.Record("prometheus", "warning", "pvc usage scope check failed for %s/%s: %v", namespace, name, err)
		}
		resp.Status = failed.Status
		return resp
	}

	batch := queryPVCUsage(ctx, client, namespace, []string{name}, matchers)
	if batch.Status != PVCUsageAvailable {
		errorlog.Record("prometheus", "warning", "pvc usage query failed for %s/%s: %s", namespace, name, batch.Error)
		resp.Status = batch.Status
	} else if usage, ok := batch.Usage[name]; ok {
		resp.Used = usage.UsedBytes
		resp.Capacity = usage.CapacityBytes
		resp.Ratio = usage.Ratio
		resp.HasData = true
		resp.Status = PVCUsageAvailable
	} else if batch.Invalid[name] {
		resp.Status = "invalid_data"
	} else {
		resp.Status = "no_series"
	}
	return resp
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

func DemandTargetBasis(row RightsizingRow) string { return rightsizing.DemandTargetBasis(row) }
