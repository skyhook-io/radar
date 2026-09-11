package prometheus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/apimachinery/pkg/api/resource"
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
	markScanHPAAvailable(workloads, "team-a")
	if !workloads["a"].workload.hpaAvailable || workloads["b"].workload.hpaAvailable {
		t.Fatalf("HPA evidence availability crossed namespaces: %+v", workloads)
	}
}

type fakeScanCoverage struct {
	clusterWide map[string]bool
	namespaces  map[string][]string
}

func (f fakeScanCoverage) IsKindClusterWide(resource string) bool { return f.clusterWide[resource] }
func (f fakeScanCoverage) KindNamespaces(resource string) []string {
	return f.namespaces[resource]
}

func TestScopeClampNarrowsAllNamespacesToWhatTheCacheHolds(t *testing.T) {
	// A nil namespace list means "every namespace" to the listers, but a
	// namespace-scoped informer only ever held a subset — without the clamp a
	// one-namespace cache is reported as a completed cluster scan.
	cache := fakeScanCoverage{
		clusterWide: map[string]bool{"daemonsets": true},
		namespaces:  map[string][]string{"deployments": {"prod"}},
	}

	effective, partial := clampScopeToCacheCoverage(cache, map[string][]string{
		"Deployment": nil,
		"DaemonSet":  nil,
	})

	if got := effective["Deployment"]; !slices.Equal(got, []string{"prod"}) {
		t.Errorf("Deployment scope = %v, want the cached namespaces", got)
	}
	if got := effective["DaemonSet"]; got != nil {
		t.Errorf("a cluster-wide kind must stay unclamped, got %v", got)
	}
	if !slices.Equal(partial, []string{"Deployment"}) {
		t.Errorf("partially cached kinds = %v, want [Deployment]", partial)
	}
}

func TestScopeClampIntersectsAnExplicitRequest(t *testing.T) {
	cache := fakeScanCoverage{namespaces: map[string][]string{"deployments": {"prod"}}}

	effective, partial := clampScopeToCacheCoverage(cache, map[string][]string{
		"Deployment": {"prod", "staging"},
	})

	if got := effective["Deployment"]; !slices.Equal(got, []string{"prod"}) {
		t.Errorf("Deployment scope = %v, want only the cached namespace", got)
	}
	if !slices.Equal(partial, []string{"Deployment"}) {
		t.Errorf("dropping a requested namespace must report partial, got %v", partial)
	}
}

func TestScopeClampLeavesFullyCoveredRequestsAlone(t *testing.T) {
	cache := fakeScanCoverage{namespaces: map[string][]string{"deployments": {"prod", "staging"}}}

	effective, partial := clampScopeToCacheCoverage(cache, map[string][]string{
		"Deployment": {"prod"},
	})

	if got := effective["Deployment"]; !slices.Equal(got, []string{"prod"}) {
		t.Errorf("Deployment scope = %v, want it untouched", got)
	}
	if len(partial) != 0 {
		t.Errorf("a fully covered request is not partial, got %v", partial)
	}
}

func TestOverlappingCoverageListsDoNotFakeAllKindsUnavailable(t *testing.T) {
	// A kind readable in only some requested namespaces lands in RestrictedKinds
	// and, if its informer covers fewer still, in PartiallyCachedKinds too.
	// Summing the lists reaches 3 with DaemonSet still fully readable, which
	// would claim every workload kind is unreadable.
	resp := newRightsizingScanResponse(time.Now(), RightsizingScanScope{
		RestrictedKinds: []string{"Deployment", "StatefulSet"},
	})
	resp.Coverage.PartiallyCachedKinds = []string{"Deployment"}

	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State != RightsizingScanPartial || resp.Reason != "limited_scope_no_workloads" {
		t.Fatalf("two limited kinds must stay partial, got %+v", resp)
	}
}

func TestPartiallyCachedKindMakesAnEmptyScanPartialNotComplete(t *testing.T) {
	// A namespace-scoped informer that happens to hold no workloads must not
	// report "no_workloads" as a completed cluster scan.
	resp := newRightsizingScanResponse(time.Now(), RightsizingScanScope{})
	resp.Coverage.PartiallyCachedKinds = []string{"Deployment"}

	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State != RightsizingScanPartial || resp.Reason != "limited_scope_no_workloads" {
		t.Fatalf("partially cached empty response = %+v", resp)
	}
}

func TestAllKindsPartiallyCachedIsPartialNotUnavailable(t *testing.T) {
	// A namespace-scoped Radar falls back to namespace informers for all three
	// kinds. If the covered namespaces hold no Deployment, StatefulSet or
	// DaemonSet, every kind is partially cached and no workload is found — but
	// all three are readable, so the answer is "nothing here in this scope",
	// not "none of these kinds can be read".
	resp := newRightsizingScanResponse(time.Now(), RightsizingScanScope{})
	resp.Coverage.PartiallyCachedKinds = []string{"Deployment", "StatefulSet", "DaemonSet"}

	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State == RightsizingScanUnavailable {
		t.Fatalf("partial caching is not unreadability: %+v", resp)
	}
	if resp.Reason == "workload_kinds_unavailable" {
		t.Errorf("readable-but-narrowed kinds reported as unavailable: %+v", resp)
	}
	if resp.State != RightsizingScanPartial || resp.Reason != "limited_scope_no_workloads" {
		t.Errorf("expected partial/limited_scope_no_workloads, got %+v", resp)
	}
}

func TestAllKindsUnreadableStaysUnavailable(t *testing.T) {
	// The other side of the same threshold: when every kind really is denied or
	// has no metrics, the scan must still say so rather than report an empty
	// cluster.
	resp := newRightsizingScanResponse(time.Now(), RightsizingScanScope{
		RestrictedKinds: []string{"Deployment", "StatefulSet"},
	})
	resp.Coverage.UnavailableKinds = []string{"DaemonSet"}

	resp = computeRightsizingScan(context.Background(), &fakeScanQuerier{}, nil, resp)
	if resp.State != RightsizingScanUnavailable || resp.Reason != "workload_kinds_unavailable" {
		t.Fatalf("expected unavailable/workload_kinds_unavailable, got %+v", resp)
	}
}

func TestScanWarningsDoNotCarryWholeQueryURLs(t *testing.T) {
	// prom.HTTPTransport errors embed the full query_range URL. Forwarded
	// verbatim these ran to tens of kilobytes and crowded out the rows they
	// annotate; consumers key on Code, so the detail can be bounded.
	longQuery := strings.Repeat("sum(rate(container_cpu_usage_seconds_total[5m]))+", 400)
	raw := `Get "http://prom:9090/api/v1/query_range?query=` + longQuery + `&start=1&end=2": dial tcp: i/o timeout`

	var resp RightsizingScanResponse
	appendScanWarning(&resp, "owner_metrics_query_failed", raw)

	if len(resp.Warnings) != 1 {
		t.Fatalf("expected one warning, got %d", len(resp.Warnings))
	}
	got := resp.Warnings[0].Message
	if len(got) > maxWarningMessageBytes+len("… (truncated)") {
		t.Errorf("warning message not bounded: %d bytes", len(got))
	}
	if strings.Contains(got, longQuery) {
		t.Error("the PromQL expression must not be forwarded verbatim")
	}
	if !strings.Contains(got, "query_range") {
		t.Errorf("the bounded message should still identify the failing call: %q", got)
	}
}

func TestBoundWarningMessageLeavesShortMessagesAlone(t *testing.T) {
	const short = "kube_pod_owner returned no series"
	if got := boundWarningMessage(short); got != short {
		t.Errorf("a short diagnostic must survive intact, got %q", got)
	}
}
