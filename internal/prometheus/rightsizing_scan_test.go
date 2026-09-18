package prometheus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

type fakeScanQuerier struct {
	mu           sync.Mutex
	queries      []string
	rangeQueries []string
	queryFn      func(string) (*prom.QueryResult, error)
	rangeFn      func(string) (*prom.QueryResult, error)
}

func (f *fakeScanQuerier) Query(_ context.Context, query string) (*prom.QueryResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	if f.queryFn != nil {
		return f.queryFn(query)
	}
	return &prom.QueryResult{}, nil
}

func (f *fakeScanQuerier) QueryRange(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
	f.mu.Lock()
	f.rangeQueries = append(f.rangeQueries, query)
	f.mu.Unlock()
	if f.rangeFn != nil {
		return f.rangeFn(query)
	}
	return &prom.QueryResult{}, nil
}

func scanTestWorkload(kind, namespace, name, container string) scanWorkload {
	return scanWorkload{kind: kind, namespace: namespace, name: name, replicas: 3, workload: rightsizingWorkload{
		containers:    []containerSpec{{name: container, cpuReq: mustQuantityNoTest("500m"), memReq: mustQuantityNoTest("512Mi")}},
		currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true,
	}}
}

func mustQuantityNoTest(value string) *resource.Quantity {
	quantity := resource.MustParse(value)
	return &quantity
}

func ksmAvailable() *prom.QueryResult {
	return &prom.QueryResult{Series: []prom.Series{{DataPoints: []prom.DataPoint{{Value: 1}}}}}
}

func scanMatrixSeries(namespace, kind, workload, container string, values []float64) prom.Series {
	points := make([]prom.DataPoint, len(values))
	for i, value := range values {
		points[i] = prom.DataPoint{Timestamp: int64(i), Value: value}
	}
	return prom.Series{Labels: map[string]string{
		"namespace": namespace, "workload_kind": kind, "workload": workload, "container": container,
	}, DataPoints: points}
}

func repeatedValues(value float64) []float64 {
	values := make([]float64, rightsizingMinSamples)
	for i := range values {
		values[i] = value
	}
	return values
}

func TestRightsizingScanQueriesPreserveWorkloadIdentity(t *testing.T) {
	batch := []scanWorkload{
		scanTestWorkload("Deployment", "team-a", "api.v1", "app"),
		scanTestWorkload("StatefulSet", "team-b", "db", "db"),
	}
	queries := buildRightsizingScanQueries(batch)
	for key, query := range queries {
		for _, want := range []string{"namespace,workload_kind,workload,container", "group_left(workload_kind,workload)"} {
			if !strings.Contains(query, want) {
				t.Errorf("%s query missing %q: %s", key, want, query)
			}
		}
	}
	owner := rightsizingScanOwnerVector(batch)
	for _, want := range []string{`namespace="team-a"`, `owner_name=~"^(api\\.v1)$"`, `"workload_kind", "Deployment"`, `namespace="team-b"`, `"workload_kind", "StatefulSet"`} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner vector missing %q: %s", want, owner)
		}
	}
}

func TestComputeRightsizingScanKeepsSameNamedContainersSeparate(t *testing.T) {
	workloads := []scanWorkload{
		scanTestWorkload("Deployment", "alpha", "api", "app"),
		scanTestWorkload("Deployment", "beta", "worker", "app"),
	}
	client := &fakeScanQuerier{
		queryFn: func(query string) (*prom.QueryResult, error) {
			if query == "count(kube_pod_owner)" || query == "count(kube_replicaset_owner)" {
				return ksmAvailable(), nil
			}
			if strings.Contains(query, "kube_pod_container_status_restarts_total") {
				return &prom.QueryResult{Series: []prom.Series{
					scanMatrixSeries("alpha", "Deployment", "api", "app", []float64{0}),
					scanMatrixSeries("beta", "Deployment", "worker", "app", []float64{0}),
				}}, nil
			}
			return &prom.QueryResult{}, nil
		},
		rangeFn: func(query string) (*prom.QueryResult, error) {
			if strings.Contains(query, "container_cpu_usage_seconds_total") {
				return &prom.QueryResult{Series: []prom.Series{
					scanMatrixSeries("alpha", "Deployment", "api", "app", repeatedValues(0.1)),
					scanMatrixSeries("beta", "Deployment", "worker", "app", repeatedValues(0.4)),
				}}, nil
			}
			if strings.Contains(query, "container_memory_working_set_bytes") {
				return &prom.QueryResult{Series: []prom.Series{
					scanMatrixSeries("alpha", "Deployment", "api", "app", repeatedValues(100*1024*1024)),
					scanMatrixSeries("beta", "Deployment", "worker", "app", repeatedValues(400*1024*1024)),
				}}, nil
			}
			return &prom.QueryResult{}, nil
		},
	}
	resp := computeRightsizingScan(context.Background(), client, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}))
	if resp.State != RightsizingScanComplete || len(resp.Workloads) != 2 {
		t.Fatalf("unexpected scan response: %+v", resp)
	}
	first, second := resp.Workloads[0].Rows[0].Observed, resp.Workloads[1].Rows[0].Observed
	if first == nil || second == nil || first.Value != 0.1 || second.Value != 0.4 {
		t.Fatalf("same-named containers collided: first=%+v second=%+v", first, second)
	}
	if resp.Workloads[0].Replicas != 3 || resp.Workloads[0].Rows[0].ExpectedSamples != 2017 {
		t.Fatalf("scan impact metadata = %+v", resp.Workloads[0])
	}
}

func TestRightsizingScanBatchesAtFiftyWithConstantQueries(t *testing.T) {
	workloads := make([]scanWorkload, 101)
	for i := range workloads {
		workloads[i] = scanTestWorkload("Deployment", "prod", fmt.Sprintf("workload-%03d", i), "app")
	}
	client := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
		if query == "count(kube_pod_owner)" || query == "count(kube_replicaset_owner)" {
			return ksmAvailable(), nil
		}
		return &prom.QueryResult{}, nil
	}}
	resp := computeRightsizingScan(context.Background(), client, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}))
	if resp.Coverage.Batches != 3 || resp.Coverage.CompletedBatches != 3 {
		t.Fatalf("coverage = %+v, want three complete batches", resp.Coverage)
	}
	if got := len(client.rangeQueries); got != 9 {
		t.Fatalf("range query count = %d, want three queries x three batches", got)
	}
	if got := len(client.queries); got != 8 {
		t.Fatalf("instant query count = %d, want two KSM probes + two safety queries x three batches", got)
	}
}

func TestRightsizingScanSafetyQueriesCoverHistoricalPodsAndReasons(t *testing.T) {
	queries := buildRightsizingScanQueries([]scanWorkload{
		scanTestWorkload("Deployment", "prod", "api", "app"),
	})
	for _, want := range []string{"increase(", "kube_pod_container_status_restarts_total", "[7d:5m]", "group_left(workload_kind,workload)"} {
		if !strings.Contains(queries["restart_activity"], want) {
			t.Errorf("restart query missing %q: %s", want, queries["restart_activity"])
		}
	}
	for _, want := range []string{"max_over_time(", "last_terminated_timestamp", "last_terminated_reason", "reason", "[7d:5m]", "group_left(workload_kind,workload)"} {
		if !strings.Contains(queries["termination_history"], want) {
			t.Errorf("termination query missing %q: %s", want, queries["termination_history"])
		}
	}
}

func TestScanMemoryReductionRequiresVerifiedRestartHistory(t *testing.T) {
	key := scanKey{namespace: "prod", kind: "Deployment", workload: "api", container: "app"}
	workload := scanTestWorkload("Deployment", "prod", "api", "app")
	tests := []struct {
		name         string
		restarts     map[scanKey]float64
		terminations map[scanKey]terminationEvidence
		wantReason   string
		wantOOM      bool
		wantRec      bool
	}{
		{name: "clean", restarts: map[scanKey]float64{key: 0}, terminations: map[scanKey]terminationEvidence{}, wantRec: true},
		{name: "non OOM restart", restarts: map[scanKey]float64{key: 1}, terminations: map[scanKey]terminationEvidence{key: {Any: true}}, wantRec: true},
		{name: "missing reason", restarts: map[scanKey]float64{key: 1}, terminations: map[scanKey]terminationEvidence{}, wantReason: "oom_evidence_unavailable"},
		{name: "historical OOM", restarts: map[scanKey]float64{key: 2}, terminations: map[scanKey]terminationEvidence{key: {Any: true, OOM: true}}, wantReason: "oom_evidence", wantOOM: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evidence := scanBatchEvidence{
				memory:   map[scanKey][]float64{key: repeatedValues(50 * 1024 * 1024)},
				restarts: tc.restarts, terminations: tc.terminations, errors: map[string]error{},
			}
			row := buildScanRow(workload.workload.containers[0], "memory", key, rightsizingMinSamples, workload.workload, evidence)
			if row.RecommendationReason != tc.wantReason || row.WindowOOMEvidence != tc.wantOOM || (row.RecommendedReq != nil) != tc.wantRec {
				t.Fatalf("row = %+v", row)
			}
		})
	}
}

func TestRightsizingScanReportsMissingKSMAndPartialEvidence(t *testing.T) {
	workloads := []scanWorkload{scanTestWorkload("Deployment", "prod", "api", "app")}
	missing := &fakeScanQuerier{queryFn: func(string) (*prom.QueryResult, error) { return &prom.QueryResult{}, nil }}
	resp := computeRightsizingScan(context.Background(), missing, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}))
	if resp.State != RightsizingScanUnavailable || resp.Reason != "owner_metrics_missing" || len(missing.rangeQueries) != 0 {
		t.Fatalf("missing KSM response = %+v, range queries=%d", resp, len(missing.rangeQueries))
	}

	partial := &fakeScanQuerier{
		queryFn: func(query string) (*prom.QueryResult, error) {
			if query == "count(kube_pod_owner)" || query == "count(kube_replicaset_owner)" {
				return ksmAvailable(), nil
			}
			return &prom.QueryResult{}, nil
		},
		rangeFn: func(query string) (*prom.QueryResult, error) {
			if strings.Contains(query, "container_memory_working_set_bytes") {
				return nil, errors.New("memory backend timeout")
			}
			return &prom.QueryResult{}, nil
		},
	}
	resp = computeRightsizingScan(context.Background(), partial, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}))
	if resp.State != RightsizingScanPartial || resp.Reason != "some_evidence_unavailable" {
		t.Fatalf("partial response = %+v", resp)
	}
	if len(resp.Workloads) != 1 || resp.Workloads[0].Rows[1].QueryError != "usage query failed" {
		t.Fatalf("memory failure not attached to row: %+v", resp.Workloads)
	}
}

func TestRightsizingScanKeepsNonDeploymentsWhenReplicaSetOwnersMissing(t *testing.T) {
	workloads := []scanWorkload{
		scanTestWorkload("Deployment", "prod", "api", "app"),
		scanTestWorkload("StatefulSet", "prod", "db", "db"),
	}
	client := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
		if query == "count(kube_pod_owner)" {
			return ksmAvailable(), nil
		}
		return &prom.QueryResult{}, nil
	}}
	resp := computeRightsizingScan(context.Background(), client, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}))
	if resp.State != RightsizingScanPartial || resp.Coverage.WorkloadsEvaluated != 1 || len(resp.Workloads) != 1 || resp.Workloads[0].Kind != "StatefulSet" {
		t.Fatalf("non-deployment coverage was lost: %+v", resp)
	}
	if fmt.Sprint(resp.Coverage.UnavailableKinds) != "[Deployment]" {
		t.Fatalf("unavailable kinds = %v, want Deployment", resp.Coverage.UnavailableKinds)
	}
}

func TestRightsizingScanReportsMissingReplicaSetOwnersForDeploymentOnlyScope(t *testing.T) {
	client := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
		if query == "count(kube_pod_owner)" {
			return ksmAvailable(), nil
		}
		return &prom.QueryResult{}, nil
	}}
	resp := computeRightsizingScan(context.Background(), client, []scanWorkload{
		scanTestWorkload("Deployment", "prod", "api", "app"),
	}, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}))
	if resp.State != RightsizingScanUnavailable || resp.Reason != "deployment_owner_metrics_missing" {
		t.Fatalf("deployment-only missing owner response = %+v", resp)
	}
}

func TestRightsizingScanDoesNotCallRestrictedEmptyScopeComplete(t *testing.T) {
	resp := newRightsizingScanResponse(time.Now(), RightsizingScanScope{RestrictedKinds: []string{"Deployment"}})
	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State != RightsizingScanPartial || resp.Reason != "limited_scope_no_workloads" {
		t.Fatalf("partially restricted empty response = %+v", resp)
	}

	resp = newRightsizingScanResponse(time.Now(), RightsizingScanScope{RestrictedKinds: []string{"Deployment", "StatefulSet", "DaemonSet"}})
	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State != RightsizingScanUnavailable || resp.Reason != "workload_kinds_unavailable" {
		t.Fatalf("fully restricted empty response = %+v", resp)
	}
}

func TestRecommendationSafetySuppressesUnknownHPAAndOOM(t *testing.T) {
	cpu := RightsizingRow{HPAEvidenceAvailable: false}
	classifyRightsizingFit(&cpu, 0.05, mustQuantity(t, "200m"), mustQuantity(t, "1"), "cpu")
	if cpu.RecommendedReq != nil || cpu.RecommendationReason != "hpa_evidence_unavailable" {
		t.Fatalf("unknown HPA evidence did not suppress CPU recommendation: %+v", cpu)
	}

	memory := RightsizingRow{HPAEvidenceAvailable: true, OOMEvidenceAvailable: false}
	classifyRightsizingFit(&memory, 50*1024*1024, mustQuantity(t, "256Mi"), mustQuantity(t, "1Gi"), "memory")
	if memory.RecommendedReq != nil || memory.RecommendationReason != "oom_evidence_unavailable" {
		t.Fatalf("unknown OOM evidence did not suppress memory downsize: %+v", memory)
	}
}

func TestUnknownHPAEvidencePermitsIncreaseGuidance(t *testing.T) {
	for _, test := range []struct {
		name string
		req  *resource.Quantity
		fit  RightsizingFit
	}{
		{name: "under-requested", req: mustQuantity(t, "100m"), fit: FitUnderRequested},
		{name: "missing request", fit: FitMissingRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := RightsizingRow{HPAEvidenceAvailable: false}
			classifyRightsizingFit(&row, 0.2, test.req, nil, "cpu")
			if row.Fit != test.fit || row.RecommendedReq == nil || row.RecommendationReason != "" {
				t.Fatalf("unknown HPA evidence suppressed increase guidance: %+v", row)
			}
		})
	}
}

func TestMarkScanHPAAvailableIsNamespaceScoped(t *testing.T) {
	workloads := map[string]*scanWorkload{
		"a": {namespace: "team-a"},
		"b": {namespace: "team-b"},
	}
	markScanHPAAvailable(scopeTestCache(t, map[string]bool{k8score.HorizontalPodAutoscalers: true}), workloads, "team-a")
	if !workloads["a"].workload.hpaAvailable || workloads["b"].workload.hpaAvailable {
		t.Fatalf("HPA evidence availability crossed namespaces: %+v", workloads)
	}
}

// A denied pod lister and a workload whose pods are simply healthy both leave
// currentPodOOM empty. Only the flag separates them, and a recommendation that
// silently drops OOM evidence looks identical to one that found none.
func TestScanMarksWorkloadsWhenPodInventoryIsUnreadable(t *testing.T) {
	oomed := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:                 "api",
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
		}}},
	}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6", OwnerReferences: []metav1.OwnerReference{scopeOwner("Deployment", "api")}}}

	newWorkloads := func() map[string]*scanWorkload {
		return map[string]*scanWorkload{
			workloadIdentity("Deployment", "shop", "api"): {
				kind: "Deployment", namespace: "shop", name: "api",
				workload: rightsizingWorkload{currentPodOOM: map[string]bool{}},
			},
		}
	}
	scopes := map[string][]string{"Deployment": {"shop"}}

	t.Run("pods readable", func(t *testing.T) {
		cache := scopeTestCache(t, map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true}, replicaSet, oomed)
		workloads := newWorkloads()
		enrichScanCurrentOOM(context.Background(), cache, scopes, workloads)
		got := workloads[workloadIdentity("Deployment", "shop", "api")].workload
		if got.liveInventoryUnavailable {
			t.Error("pods were readable; the row must not claim the inventory was denied")
		}
		if !got.currentPodOOM["api"] {
			t.Error("an OOMKilled container was in the cache but did not reach the workload")
		}
	})

	t.Run("pod lister denied", func(t *testing.T) {
		cache := scopeTestCache(t, map[string]bool{k8score.ReplicaSets: true}, replicaSet)
		workloads := newWorkloads()
		enrichScanCurrentOOM(context.Background(), cache, scopes, workloads)
		got := workloads[workloadIdentity("Deployment", "shop", "api")].workload
		if !got.liveInventoryUnavailable {
			t.Error("the pod lister was denied, so the empty OOM map must be reported as unanswered")
		}
		if got.currentPodOOM["api"] {
			t.Error("no pods were readable; nothing may be claimed about OOM")
		}
	})
}

// A pod names the ReplicaSet that owns it, never the Deployment. Without the
// ReplicaSet lister every Deployment silently collects no OOM evidence, while
// a StatefulSet in the same scan is unaffected — so the flag has to follow the
// kind that actually lost its ownership path.
func TestScanMarksOnlyDeploymentsWhenReplicaSetOwnershipIsUnreadable(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7f6-a", OwnerReferences: []metav1.OwnerReference{scopeOwner("ReplicaSet", "api-7f6")}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:                 "api",
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
		}}},
	}
	cache := scopeTestCache(t, map[string]bool{k8score.Pods: true}, pod)
	workloads := map[string]*scanWorkload{
		workloadIdentity("Deployment", "shop", "api"): {
			kind: "Deployment", namespace: "shop", name: "api",
			workload: rightsizingWorkload{currentPodOOM: map[string]bool{}},
		},
		workloadIdentity("StatefulSet", "shop", "cache"): {
			kind: "StatefulSet", namespace: "shop", name: "cache",
			workload: rightsizingWorkload{currentPodOOM: map[string]bool{}},
		},
	}
	enrichScanCurrentOOM(context.Background(), cache, map[string][]string{"Deployment": {"shop"}}, workloads)

	if !workloads[workloadIdentity("Deployment", "shop", "api")].workload.liveInventoryUnavailable {
		t.Error("no ReplicaSet lister, so the Deployment never sees its pods; that has to be reported")
	}
	if workloads[workloadIdentity("StatefulSet", "shop", "cache")].workload.liveInventoryUnavailable {
		t.Error("a StatefulSet owns its pods directly and lost nothing; it must not be flagged")
	}
}

// A synced, non-nil pod lister still only answers for the namespaces its
// informer watches. Radar runs this way whenever cluster-wide list is denied
// and the probe falls back to specific namespaces, so a scan can legitimately
// span one namespace it can read and one it cannot — and an empty result for
// the second is no answer, not an absence of OOM kills.
func TestScanMarksWorkloadsOutsideTheInformersNamespaces(t *testing.T) {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(),
		ResourceTypes: map[string]bool{k8score.Pods: true},
		DeferredTypes: map[string]bool{},
		ResourceScopes: map[string]k8score.ResourceScope{
			k8score.Pods: {Enabled: true, Namespace: "team-a"},
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}

	workloads := map[string]*scanWorkload{
		workloadIdentity("StatefulSet", "team-a", "watched"): {
			kind: "StatefulSet", namespace: "team-a", name: "watched",
			workload: rightsizingWorkload{currentPodOOM: map[string]bool{}},
		},
		workloadIdentity("StatefulSet", "team-b", "unwatched"): {
			kind: "StatefulSet", namespace: "team-b", name: "unwatched",
			workload: rightsizingWorkload{currentPodOOM: map[string]bool{}},
		},
	}
	enrichScanCurrentOOM(context.Background(), cache, map[string][]string{"StatefulSet": {"team-a", "team-b"}}, workloads)

	if workloads[workloadIdentity("StatefulSet", "team-a", "watched")].workload.liveInventoryUnavailable {
		t.Error("team-a is watched, so its empty pod list is authoritative and must not be flagged")
	}
	if !workloads[workloadIdentity("StatefulSet", "team-b", "unwatched")].workload.liveInventoryUnavailable {
		t.Error("team-b is outside the informer's scope; its empty result proves nothing and must be reported")
	}
}

// A scan holding no Deployments resolves every pod through a direct owner, so
// it must not spend the warming budget on the deferred ReplicaSet informer.
func TestScanSkipsReplicaSetWaitWithoutDeployments(t *testing.T) {
	cache := scopeTestCache(t, map[string]bool{k8score.Pods: true})
	workloads := map[string]*scanWorkload{
		workloadIdentity("DaemonSet", "shop", "agent"): {
			kind: "DaemonSet", namespace: "shop", name: "agent",
			workload: rightsizingWorkload{currentPodOOM: map[string]bool{}},
		},
	}
	if scanNeedsReplicaSets(workloads) {
		t.Fatal("a DaemonSet-only scan reported that it needs ReplicaSet ownership")
	}
	start := time.Now()
	enrichScanCurrentOOM(context.Background(), cache, map[string][]string{"DaemonSet": {"shop"}}, workloads)
	if elapsed := time.Since(start); elapsed >= warmingRetryBudget {
		t.Errorf("waited %v on ReplicaSets a DaemonSet-only scan never reads", elapsed)
	}
}

// The availability flag gates whether a reduction may be suggested at all, so
// an HPA informer that does not watch a namespace must not report that it
// looked there and found no autoscaler.
func TestScanHPAAvailabilityFollowsInformerCoverage(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(),
		ResourceTypes: map[string]bool{k8score.HorizontalPodAutoscalers: true},
		DeferredTypes: map[string]bool{},
		ResourceScopes: map[string]k8score.ResourceScope{
			string(k8score.HorizontalPodAutoscalers): {Enabled: true, Namespace: "team-a"},
		},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}

	workloads := map[string]*scanWorkload{
		"a": {kind: "Deployment", namespace: "team-a", name: "watched"},
		"b": {kind: "Deployment", namespace: "team-b", name: "unwatched"},
	}
	markScanHPAAvailable(cache, workloads, "")

	if !workloads["a"].workload.hpaAvailable {
		t.Error("team-a is watched; its empty HPA list is authoritative and must count as evidence")
	}
	if workloads["b"].workload.hpaAvailable {
		t.Error("team-b is outside the informer's scope, so no autoscaler could have been seen there")
	}
}

// A cache with no scope restrictions watches every namespace, so there is no
// namespace whose HPAs went unread: every workload's empty result is a real
// answer, and the evidence counts for all of them.
func TestScanHPAAvailabilityWithFullAccess(t *testing.T) {
	cache := scopeTestCache(t, map[string]bool{k8score.HorizontalPodAutoscalers: true})
	workloads := map[string]*scanWorkload{
		"a": {kind: "Deployment", namespace: "team-a", name: "one"},
		"b": {kind: "Deployment", namespace: "team-b", name: "two"},
	}
	markScanHPAAvailable(cache, workloads, "")
	if !workloads["a"].workload.hpaAvailable || !workloads["b"].workload.hpaAvailable {
		t.Fatalf("full access must mark every workload available: %+v", workloads)
	}
}

// The real caller, not just the marking helper: a utilization HPA in the
// watched namespace must both flag its workload as HPA-managed and count as
// evidence, while a workload in an unwatched namespace gets neither — nothing
// was ever listed there, so "no autoscaler" was never established.
func TestEnrichScanHPAHonoursCoverageEndToEnd(t *testing.T) {
	utilization := int32(80)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "api"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"},
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name:   "cpu",
					Target: autoscalingv2.MetricTarget{AverageUtilization: &utilization},
				},
			}},
		},
	}
	for _, tc := range []struct {
		name           string
		scopes         map[string]k8score.ResourceScope
		wantAAvailable bool
		wantBAvailable bool
	}{
		{"full access", nil, true, true},
		{"scoped to team-a", map[string]k8score.ResourceScope{
			string(k8score.HorizontalPodAutoscalers): {Enabled: true, Namespace: "team-a"},
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, err := k8score.NewResourceCache(k8score.CacheConfig{
				Client:         fake.NewClientset(hpa),
				ResourceTypes:  map[string]bool{k8score.HorizontalPodAutoscalers: true},
				DeferredTypes:  map[string]bool{},
				ResourceScopes: tc.scopes,
			})
			if err != nil {
				t.Fatalf("NewResourceCache: %v", err)
			}
			t.Cleanup(core.Stop)
			cache := &k8s.ResourceCache{ResourceCache: core}

			workloads := map[string]*scanWorkload{
				workloadIdentity("Deployment", "team-a", "api"): {
					kind: "Deployment", namespace: "team-a", name: "api",
					workload: rightsizingWorkload{hpaManaged: map[string]bool{}},
				},
				workloadIdentity("Deployment", "team-b", "other"): {
					kind: "Deployment", namespace: "team-b", name: "other",
					workload: rightsizingWorkload{hpaManaged: map[string]bool{}},
				},
			}
			// A kind mapped to a nil namespace list is the cluster-wide scope; an
			// absent key yields an empty list, which is a different branch.
			enrichScanHPA(context.Background(), cache, map[string][]string{"Deployment": nil}, workloads)

			a := workloads[workloadIdentity("Deployment", "team-a", "api")].workload
			b := workloads[workloadIdentity("Deployment", "team-b", "other")].workload
			if a.hpaAvailable != tc.wantAAvailable {
				t.Errorf("team-a hpaAvailable = %v, want %v", a.hpaAvailable, tc.wantAAvailable)
			}
			if b.hpaAvailable != tc.wantBAvailable {
				t.Errorf("team-b hpaAvailable = %v, want %v", b.hpaAvailable, tc.wantBAvailable)
			}
			if !a.hpaManaged["cpu"] {
				t.Error("the HPA targets this Deployment's CPU; that must survive the coverage check")
			}
		})
	}
}

func utilizationHPA(namespace, name string) *autoscalingv2.HorizontalPodAutoscaler {
	utilization := int32(80)
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: name},
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name:   "cpu",
					Target: autoscalingv2.MetricTarget{AverageUtilization: &utilization},
				},
			}},
		},
	}
}

// stalledDeferredCache watches HPAs cluster-wide alongside a second deferred
// informer that never starts. The HPA informer syncs normally; the deferred
// phase as a whole never reports done — the shape of a cluster where one
// deferred kind timed out, which is permanent for the process lifetime.
func stalledDeferredCache(t *testing.T, hpa *autoscalingv2.HorizontalPodAutoscaler) *k8s.ResourceCache {
	t.Helper()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:          fake.NewClientset(hpa),
		ResourceTypes:   map[string]bool{k8score.HorizontalPodAutoscalers: true, k8score.Jobs: true},
		DeferredTypes:   map[string]bool{k8score.HorizontalPodAutoscalers: true, k8score.Jobs: true},
		DebugSyncDelays: map[string]time.Duration{k8score.Jobs: time.Hour},
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if synced, known := core.InformerSynced(k8score.HorizontalPodAutoscalers); known && synced {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HPA informer never synced")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cache := &k8s.ResourceCache{ResourceCache: core}
	if cache.IsDeferredSynced() {
		t.Fatal("deferred phase reported done; this cache must reproduce a stalled one")
	}
	return cache
}

// A readable, fully synced HPA informer is evidence even when an unrelated
// deferred informer is stuck. Gating on the deferred phase instead withheld a
// correct reduction and blamed autoscaling for it.
func TestEnrichScanHPAIgnoresUnrelatedDeferredInformers(t *testing.T) {
	cache := stalledDeferredCache(t, utilizationHPA("team-a", "api"))
	key := workloadIdentity("Deployment", "team-a", "api")
	workloads := map[string]*scanWorkload{
		key: {
			kind: "Deployment", namespace: "team-a", name: "api",
			workload: rightsizingWorkload{hpaManaged: map[string]bool{}},
		},
	}
	enrichScanHPA(context.Background(), cache, map[string][]string{"Deployment": nil}, workloads)

	got := workloads[key].workload
	if !got.hpaAvailable {
		t.Error("the HPA informer is synced and cluster-wide; an unrelated stuck kind must not withhold its answer")
	}
	if !got.hpaManaged["cpu"] {
		t.Error("the HPA targets this Deployment's CPU and must be seen")
	}
}

// The single-workload path carries the same gate as the scan.
func TestLoadHPAManagedResourcesIgnoresUnrelatedDeferredInformers(t *testing.T) {
	cache := stalledDeferredCache(t, utilizationHPA("team-a", "api"))
	managed, available := loadHPAManagedResources(context.Background(), cache, "Deployment", "team-a", "api")
	if !available {
		t.Error("the HPA informer is synced and cluster-wide; an unrelated stuck kind must not withhold its answer")
	}
	if !managed["cpu"] {
		t.Error("the HPA targets this Deployment's CPU and must be seen")
	}
}

// A kind this cache never watched still has no evidence to offer: the nil
// lister is the answer, and the unknown informer key must not be read as one.
func TestLoadHPAManagedResourcesReportsAnUnwatchedKind(t *testing.T) {
	cache := scopeTestCache(t, map[string]bool{k8score.Pods: true})
	if _, available := loadHPAManagedResources(context.Background(), cache, "Deployment", "team-a", "api"); available {
		t.Error("HPAs are not watched here, so no autoscaler was ever ruled out")
	}
}
