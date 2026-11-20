package prometheus

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const clusterAFilter = `cluster="a"`

func filteredRightsizingScope(filters map[string]string) ViewScope {
	all := map[string]string{}
	for _, m := range rightsizingMetricNames {
		all[m] = clusterAFilter
	}
	for m, f := range filters {
		all[m] = f
	}
	return ViewScope{State: ViewScopeVerified, Labels: map[string]string{"cluster": "a"}, Filters: all, Note: viewScopeVerifiedNote}
}

var metricSelectorPattern = regexp.MustCompile(`([a-z_:]+)\{([^}]*)\}`)

// selectorsOf returns each rightsizing metric selector's matchers in a query.
func selectorsOf(query string) map[string][]string {
	out := map[string][]string{}
	for _, match := range metricSelectorPattern.FindAllStringSubmatch(query, -1) {
		if slices.Contains(rightsizingMetricNames, match[1]) {
			out[match[1]] = append(out[match[1]], match[2])
		}
	}
	return out
}

// assertSelectorsScoped checks every selector of every rightsizing metric in
// the queries: filtered metrics carry the filter, unfiltered ones no cluster
// matcher at all.
func assertSelectorsScoped(t *testing.T, queries []string, scope ViewScope) {
	t.Helper()
	seen := 0
	for _, query := range queries {
		for metric, selectors := range selectorsOf(query) {
			for _, sel := range selectors {
				seen++
				want := scope.Filter(metric)
				if want == "" && strings.Contains(sel, "cluster=") {
					t.Errorf("unfiltered %s carries a cluster matcher: {%s}", metric, sel)
				}
				if want != "" && sel != want && !strings.HasSuffix(sel, ","+want) {
					t.Errorf("%s selector misses %s: {%s} in %s", metric, want, sel, query)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no rightsizing selectors found")
	}
}

// twoClusterQuerier holds payments/api in clusters a and b. A query whose
// every selector carries cluster="a" sees a's series only; any other query
// sees both, and b's larger usage wins the max.
func twoClusterQuerier(q *recordingRightsizingQuerier) {
	ours := podLevelQuerier(map[string]float64{"app": 0.1}, map[string]float64{"app": 100 << 20}, 2016)
	both := podLevelQuerier(map[string]float64{"app": 2}, map[string]float64{"app": 4 << 30}, 4032)
	q.answer = func(query string) (*prom.QueryResult, error) {
		for _, selectors := range selectorsOf(query) {
			for _, sel := range selectors {
				if !strings.Contains(sel, clusterAFilter) {
					return both(query)
				}
			}
		}
		return ours(query)
	}
}

func TestRightsizingTwoClustersKeepsToThisCluster(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet"} {
		t.Run(kind, func(t *testing.T) {
			q := &recordingRightsizingQuerier{}
			twoClusterQuerier(q)
			scope := filteredRightsizingScope(nil)
			resp := computeRightsizing(context.Background(), q, kind, "payments", "api", goldenWorkload("api-1"), scope)
			assertSelectorsScoped(t, q.queries, scope)
			if resp.OwnerCoverage != OwnerCoverageKSMHistory {
				t.Fatalf("ownership join not used: %+v", resp)
			}
			cpu, memory := rowFor(resp.Rows, "app", "cpu"), rowFor(resp.Rows, "app", "memory")
			if cpu.Observed == nil || cpu.Observed.Value != 0.1 || memory.Observed == nil || memory.Observed.Value != 100<<20 || cpu.SampleCount != 2016 {
				t.Fatalf("values mixed with the other cluster: cpu=%+v memory=%+v", cpu.Observed, memory.Observed)
			}
			if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeVerified || resp.ClusterScope.Labels["cluster"] != "a" {
				t.Errorf("clusterScope = %+v", resp.ClusterScope)
			}
		})
	}
}

func TestRightsizingCurrentPodsFallbackIsScoped(t *testing.T) {
	q := &recordingRightsizingQuerier{}
	scope := filteredRightsizingScope(nil)
	resp := computeRightsizing(context.Background(), q, "Deployment", "payments", "api", goldenWorkload("api-1"), scope)
	if resp.OwnerCoverage != OwnerCoverageCurrentPods {
		t.Fatalf("coverage = %s", resp.OwnerCoverage)
	}
	assertSelectorsScoped(t, q.queries, scope)
}

func TestRightsizingLabeledOwnershipUnlabeledAnchoredUsage(t *testing.T) {
	cadvisor := []string{metricCPUUsage, metricMemoryWorkingSet, metricThrottledPeriods, metricCFSPeriods}
	req, err := normalizeViewScopeRequest(rightsizingScopeRequest(`namespace="payments"`, "payments", true, rightsizingAnchors{"payments", []string{"api-1"}, "payments", []string{"api-6c"}}, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `"`+viewScopeReferenceLabel+`"`) {
			// kube_pod_info lists this cluster's Pods and no other cluster's.
			return rows(vec(map[string]string{viewScopeReferenceLabel: PodForeignCheck(false).id()}, 1))
		}
		if isReadback(query) {
			var out []prom.Series
			for _, m := range rightsizingMetricNames {
				if !slices.Contains(cadvisor, m) {
					out = append(out, viewRow(m, clusterA))
				}
			}
			return rows(out...)
		}
		var out []prom.Series
		for _, m := range cadvisor {
			if !strings.Contains(query, `"`+m+`"`) {
				t.Errorf("identity check skipped %s: %s", m, query)
			}
			out = append(out, viewRow(m, nil), viewAnchorRow(m))
		}
		return rows(out...)
	}}
	scope, err := decideViewScope(context.Background(), store, req, time.Now(), clusterA, false)
	if err != nil || scope.Withheld {
		t.Fatalf("scope = %+v err=%v", scope, err)
	}
	for _, m := range rightsizingMetricNames {
		want := clusterAFilter
		if slices.Contains(cadvisor, m) {
			want = ""
		}
		if got, ok := scope.Filters[m]; !ok || got != want {
			t.Errorf("filter for %s = %q (present %v), want %q", m, got, ok, want)
		}
	}
	q := &recordingRightsizingQuerier{answer: ownerProbeAnswers}
	resp := computeRightsizing(context.Background(), q, "Deployment", "payments", "api", goldenWorkload("api-1"), scope)
	assertSelectorsScoped(t, q.queries, scope)
	if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeUnlabeled {
		t.Errorf("clusterScope = %+v", resp.ClusterScope)
	}
}

func TestRightsizingWithheldScopeGivesNoRecommendation(t *testing.T) {
	for _, scope := range []ViewScope{
		{State: ViewScopeSeveralClusters, Withheld: true, Note: viewScopeSeveralNote},
		{State: ViewScopeLabelMissing, Withheld: true, Labels: clusterA, Note: "The container_cpu_usage_seconds_total series don't carry this cluster's label"},
		unknownViewScope(),
	} {
		t.Run(string(scope.State), func(t *testing.T) {
			q := &recordingRightsizingQuerier{}
			twoClusterQuerier(q)
			resp := computeRightsizing(context.Background(), q, "Deployment", "payments", "api", goldenWorkload("api-1"), scope)
			if len(q.queries) != 0 {
				t.Errorf("withheld view still queried: %v", q.queries)
			}
			if resp.SampleAvailable || len(resp.Rows) != 0 || resp.Reason != ReasonClusterScopeWithheld {
				t.Fatalf("response = %+v", resp)
			}
			if resp.ClusterScope == nil || resp.ClusterScope.State != scope.State || resp.ClusterScope.Note != scope.Note {
				t.Errorf("clusterScope = %+v", resp.ClusterScope)
			}
		})
	}
}

func TestRightsizingPartialAnswersAreUnavailable(t *testing.T) {
	for name, annotations := range map[string]prom.QueryAnnotations{
		"partial": {Partial: true},
		"warning": {Warnings: []string{"store down"}},
	} {
		t.Run(name, func(t *testing.T) {
			full := podLevelQuerier(map[string]float64{"app": 0.1}, map[string]float64{"app": 100 << 20}, 2016)
			q := &recordingRightsizingQuerier{answer: func(query string) (*prom.QueryResult, error) {
				res, err := full(query)
				if strings.HasPrefix(query, "sum(count_over_time") {
					return res, err
				}
				res.Annotations = annotations
				return res, err
			}}
			resp := computeRightsizing(context.Background(), q, "Deployment", "payments", "api", goldenWorkload("api-1"), unlabeledRightsizingScope())
			if resp.SampleAvailable || resp.Reason != ReasonRightsizingPartialData {
				t.Fatalf("partial answer produced a recommendation: %+v", resp)
			}
			for _, row := range resp.Rows {
				if row.RecommendedReq != nil || row.Observed != nil || row.QueryError == "" {
					t.Errorf("row built from a partial answer: %+v", row)
				}
			}
		})
	}
}

// A partial ownership answer may have dropped Pods the workload ran this
// week, so it withholds rather than falling back to the current Pods.
func TestRightsizingPartialOwnershipIsUnavailable(t *testing.T) {
	for name, annotations := range map[string]prom.QueryAnnotations{
		"partial": {Partial: true},
		"warning": {Warnings: []string{"store down"}},
	} {
		t.Run(name, func(t *testing.T) {
			full := podLevelQuerier(map[string]float64{"app": 0.1}, map[string]float64{"app": 100 << 20}, 2016)
			q := &recordingRightsizingQuerier{answer: func(query string) (*prom.QueryResult, error) {
				res, err := full(query)
				if strings.HasPrefix(query, "sum(count_over_time") {
					res.Annotations = annotations
				}
				return res, err
			}}
			resp := computeRightsizing(context.Background(), q, "Deployment", "payments", "api", goldenWorkload("api-1"), unlabeledRightsizingScope())
			if resp.SampleAvailable || resp.Reason != ReasonRightsizingPartialData || len(resp.Rows) != 0 {
				t.Fatalf("partial ownership produced a recommendation: %+v", resp)
			}
			if len(resp.Warnings) != 1 || resp.Warnings[0].Code != "owner_metrics_query_failed" {
				t.Errorf("warnings = %+v", resp.Warnings)
			}
			if len(q.queries) != 1 {
				t.Errorf("usage queries ran after a partial ownership answer: %q", q.queries)
			}
			if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeUnlabeled {
				t.Errorf("clusterScope = %+v", resp.ClusterScope)
			}
		})
	}
}

func TestRightsizingScopeRequestShape(t *testing.T) {
	at := time.Unix(1700000000, 0)
	req := rightsizingScopeRequest(`namespace="payments"`, "payments", true, rightsizingAnchors{"payments", []string{"api-2", "api-1"}, "payments", []string{"api-6c"}}, at)
	if !req.Joined || req.Span != 7*24*time.Hour+10*time.Minute || !req.At.Equal(at) || req.Namespace != "payments" || req.View != "rightsizing" {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Metrics) != len(rightsizingMetricNames) {
		t.Fatalf("metrics = %+v", req.Metrics)
	}
	for _, m := range req.Metrics {
		if m.Subject != `namespace="payments"` {
			t.Errorf("%s subject %q", m.Name, m.Subject)
		}
		wantLabel, wantValues := "pod", []string{"api-1", "api-2"}
		if m.Name == metricReplicaSetOwner {
			wantLabel, wantValues = "replicaset", []string{"api-6c"}
		}
		if m.AnchorLabel != wantLabel || !slices.Equal(m.AnchorValues, wantValues) || m.AnchorNamespace != "payments" {
			t.Errorf("%s anchors %s=%v in %q", m.Name, m.AnchorLabel, m.AnchorValues, m.AnchorNamespace)
		}
		wantForeign := PodForeignCheck(false)
		if m.Name == metricReplicaSetOwner {
			wantForeign = ForeignCheck{}
		}
		if !reflect.DeepEqual(m.Foreign, wantForeign) {
			t.Errorf("%s foreign check %+v, want %+v", m.Name, m.Foreign, wantForeign)
		}
	}
	if _, err := normalizeViewScopeRequest(req); err != nil {
		t.Fatal(err)
	}
	noDeployments := rightsizingScopeRequest(`namespace="payments"`, "payments", false, rightsizingAnchors{}, at)
	for _, m := range noDeployments.Metrics {
		if m.Name == metricReplicaSetOwner {
			t.Error("ReplicaSet ownership checked though no Deployment is sized")
		}
	}

	scan := scanRightsizingScopeRequest([]scanWorkload{
		scanTestWorkload("StatefulSet", "payments", "db", "db"),
		scanTestWorkload("DaemonSet", "infra", "agent", "agent"),
	}, nil, at)
	if scan.Namespace != "" || !scan.At.Equal(at) || scan.Metrics[0].Subject != `namespace=~"^(infra|payments)$"` {
		t.Fatalf("scan request = %+v", scan)
	}
	if slices.ContainsFunc(scan.Metrics, func(m ViewMetric) bool { return m.Name == metricReplicaSetOwner }) {
		t.Error("scan without Deployments checks ReplicaSet ownership")
	}
}

// Two clusters send ownership and cAdvisor without a cluster label, and both
// run a Deployment named api in shop; kube-state-metrics' labeled Pod list
// names cluster b's Pod. The scan joins Pods by owner name, so it would size
// api from both clusters: it is withheld. With only this cluster's Pods in
// those series, it reads them unfiltered.
func TestRightsizingWithTwoUnlabeledClusters(t *testing.T) {
	ksm := []map[string]string{ksmPod(clusterA, "shop", "api-a-1"), ksmPod(clusterB, "shop", "api-b-1")}
	series := func(pods ...string) []map[string]string {
		var out []map[string]string
		for _, pod := range pods {
			out = append(out,
				unlabeledSeries(metricPodOwner, "shop", pod, "owner_kind", "ReplicaSet", "owner_name", "api-6c"),
				unlabeledSeries(metricCPUUsage, "shop", pod, "container", "app"))
		}
		return out
	}
	req := scanRightsizingScopeRequestFrom([]scanWorkload{scanTestWorkload("Deployment", "shop", "api", "api")},
		func(string) []string { return []string{"api-a-1"} }, func(string) []string { return nil }, time.Time{})
	norm, err := normalizeViewScopeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		series []map[string]string
		want   ViewScopeState
	}{
		{"both clusters' Pods", series("api-a-1", "api-b-1"), ViewScopeLabelMissing},
		{"only this cluster's Pods", series("api-a-1"), ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &evaluatingStore{t: t, series: append(slices.Clone(ksm), tc.series...)}
			scope, err := decideViewScope(context.Background(), store, norm, time.Now(), clusterA, false)
			if err != nil {
				t.Fatal(err)
			}
			if scope.State != tc.want {
				t.Fatalf("scope = %+v, want %s", scope, tc.want)
			}
			if tc.want == ViewScopeLabelMissing && !strings.Contains(scope.Note, "another cluster") {
				t.Errorf("note %q", scope.Note)
			}
		})
	}
}

// A scan's anchors come from one namespace, never names flattened across its
// namespaces: otherwise another cluster's same-named Pod in a different
// namespace could vouch for this cluster's.
func TestRightsizingScanAnchorsOneNamespace(t *testing.T) {
	live := map[string][]string{"infra": {"agent-0"}, "payments": {"api-1"}, "web": {"web-1"}}
	liveRS := map[string][]string{"payments": {"api-6c"}, "web": {"web-7d"}}
	scan := scanRightsizingScopeRequestFrom([]scanWorkload{
		scanTestWorkload("Deployment", "web", "web", "web"),
		scanTestWorkload("DaemonSet", "infra", "agent", "agent"),
		scanTestWorkload("Deployment", "payments", "api", "api"),
		scanTestWorkload("StatefulSet", "empty", "db", "db"),
	}, func(ns string) []string { return live[ns] }, func(ns string) []string { return liveRS[ns] }, time.Time{})
	for _, m := range scan.Metrics {
		wantLabel, wantNS, wantValues := "pod", "infra", []string{"agent-0"}
		if m.Name == metricReplicaSetOwner {
			wantLabel, wantNS, wantValues = "replicaset", "payments", []string{"api-6c"}
		}
		if m.AnchorLabel != wantLabel || m.AnchorNamespace != wantNS || !slices.Equal(m.AnchorValues, wantValues) {
			t.Errorf("%s anchors %s=%v in %q, want %s=%v in %q", m.Name, m.AnchorLabel, m.AnchorValues, m.AnchorNamespace, wantLabel, wantValues, wantNS)
		}
		if m.Subject != `namespace=~"^(empty|infra|payments|web)$"` {
			t.Errorf("%s subject %q", m.Name, m.Subject)
		}
	}
	if !slices.ContainsFunc(scan.Metrics, func(m ViewMetric) bool { return m.Name == metricReplicaSetOwner }) {
		t.Error("scan with Deployments skips ReplicaSet ownership")
	}
	req, err := normalizeViewScopeRequest(scan)
	if err != nil {
		t.Fatal(err)
	}
	identity := viewIdentityQuery(req.Metrics[:1], []string{"cluster"}, req.Span, true)
	if !strings.Contains(identity, `namespace="infra",pod=~"agent-0"`) {
		t.Fatalf("anchor count not kept to its namespace: %s", identity)
	}
}

func manyScanWorkloads(n int) []scanWorkload {
	out := make([]scanWorkload, n)
	for i := range out {
		out[i] = scanTestWorkload("StatefulSet", "payments", fmt.Sprintf("db-%03d", i), "db")
	}
	return out
}

func TestRightsizingScanResolvesScopeOncePerScan(t *testing.T) {
	workloads := manyScanWorkloads(2*rightsizingScanBatchSize + 5)
	client := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
		if strings.HasPrefix(query, "count(") {
			return ksmAvailable(), nil
		}
		return &prom.QueryResult{}, nil
	}}
	scope := filteredRightsizingScope(nil)
	var mu sync.Mutex
	calls := 0
	resolve := func(_ context.Context, got []scanWorkload) (ViewScope, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if len(got) != len(workloads) {
			t.Errorf("resolved over %d of %d workloads", len(got), len(workloads))
		}
		return scope, nil
	}
	resp := computeRightsizingScanProgress(context.Background(), client, resolve, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}), func(RightsizingScanResponse) {})
	if calls != 1 || resp.Coverage.AttemptedBatches != 3 {
		t.Fatalf("scope resolved %d times over %d batches", calls, resp.Coverage.AttemptedBatches)
	}
	if !slices.Contains(client.queries, `count(kube_pod_owner{cluster="a"})`) {
		t.Errorf("ownership presence probe unscoped: %v", client.queries)
	}
	assertSelectorsScoped(t, append(slices.Clone(client.queries), client.rangeQueries...), scope)
	if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeVerified {
		t.Errorf("clusterScope = %+v", resp.ClusterScope)
	}
}

func TestRightsizingScanDeploymentOwnerVectorIsScoped(t *testing.T) {
	scope := filteredRightsizingScope(map[string]string{metricCPUUsage: ""})
	queries := buildRightsizingScanQueries([]scanWorkload{
		scanTestWorkload("Deployment", "payments", "api", "app"),
		scanTestWorkload("DaemonSet", "infra", "agent", "agent"),
	}, scope)
	var all []string
	for _, query := range queries {
		all = append(all, query)
	}
	assertSelectorsScoped(t, all, scope)
	if !strings.Contains(queries["cpu"], `kube_replicaset_owner{namespace="payments",owner_kind="Deployment",owner_name=~"^(api)$",owner_is_controller="true",cluster="a"}`) {
		t.Errorf("ReplicaSet ownership unscoped: %s", queries["cpu"])
	}
}

func TestRightsizingScanWithheldScope(t *testing.T) {
	workloads := []scanWorkload{scanTestWorkload("Deployment", "payments", "api", "app"), scanTestWorkload("StatefulSet", "payments", "db", "db")}
	client := &fakeScanQuerier{}
	scope := ViewScope{State: ViewScopeSeveralClusters, Withheld: true, Note: viewScopeSeveralNote}
	resolve := func(context.Context, []scanWorkload) (ViewScope, error) { return scope, nil }
	resp := computeRightsizingScanProgress(context.Background(), client, resolve, workloads, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}), func(RightsizingScanResponse) {})
	if len(client.queries)+len(client.rangeQueries) != 0 {
		t.Errorf("withheld scan still queried: %v %v", client.queries, client.rangeQueries)
	}
	if resp.State != RightsizingScanUnavailable || resp.Reason != ScanReasonClusterScopeWithheld || resp.Coverage.WorkloadsWithData != 0 {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Code != ScanReasonClusterScopeWithheld || resp.Warnings[0].Message != scope.Note {
		t.Errorf("warnings = %+v", resp.Warnings)
	}
	if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeSeveralClusters {
		t.Errorf("clusterScope = %+v", resp.ClusterScope)
	}
	if len(resp.Workloads) != 2 {
		t.Fatalf("workloads = %+v", resp.Workloads)
	}
	for _, workload := range resp.Workloads {
		for _, row := range workload.Rows {
			if row.Observed != nil || row.RecommendedReq != nil || row.RecommendationReason != ScanReasonClusterScopeWithheld || !IsWithheldRecommendationReason(row.RecommendationReason) {
				t.Errorf("row = %+v", row)
			}
		}
	}
}

func TestRightsizingScanCancelledDuringScopeCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resolve := func(ctx context.Context, _ []scanWorkload) (ViewScope, error) {
		cancel()
		return unknownViewScope(), ctx.Err()
	}
	client := &fakeScanQuerier{}
	resp := computeRightsizingScanProgress(ctx, client, resolve, []scanWorkload{scanTestWorkload("StatefulSet", "payments", "db", "db")}, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}), func(RightsizingScanResponse) {})
	if resp.Reason != "scan_incomplete" || len(client.queries) != 0 || resp.ClusterScope != nil {
		t.Fatalf("response = %+v queries=%v", resp, client.queries)
	}
}

func TestRightsizingScanPartialBatchIsUnavailableEvidence(t *testing.T) {
	client := &fakeScanQuerier{
		queryFn: func(query string) (*prom.QueryResult, error) {
			if strings.HasPrefix(query, "count(") {
				return ksmAvailable(), nil
			}
			return &prom.QueryResult{}, nil
		},
		rangeFn: func(query string) (*prom.QueryResult, error) {
			return &prom.QueryResult{Series: []prom.Series{scanMatrixSeries("payments", "StatefulSet", "db", "db", repeatedValues(0.1))}, Annotations: prom.QueryAnnotations{Partial: true}}, nil
		},
	}
	resolve := func(context.Context, []scanWorkload) (ViewScope, error) { return unlabeledRightsizingScope(), nil }
	resp := computeRightsizingScanProgress(context.Background(), client, resolve, []scanWorkload{scanTestWorkload("StatefulSet", "payments", "db", "db")}, newRightsizingScanResponse(time.Now(), RightsizingScanScope{}), func(RightsizingScanResponse) {})
	if resp.State != RightsizingScanPartial || resp.Coverage.WorkloadsWithData != 0 {
		t.Fatalf("partial answer read as evidence: %+v", resp)
	}
	if !slices.ContainsFunc(resp.Warnings, func(w RightsizingScanWarning) bool {
		return w.Code == "cpu_query_failed" && strings.Contains(w.Message, prom.ErrPartialResponse.Error())
	}) {
		t.Errorf("warnings = %+v", resp.Warnings)
	}
	for _, row := range resp.Workloads[0].Rows {
		if row.Observed != nil || row.QueryError == "" {
			t.Errorf("row = %+v", row)
		}
	}
	if resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeUnlabeled {
		t.Errorf("clusterScope = %+v", resp.ClusterScope)
	}
}
