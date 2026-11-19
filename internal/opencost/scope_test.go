package opencost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

const clusterA = `cluster="a"`

var costMetricNames = []string{
	pkgopencost.MetricCPUAllocation, pkgopencost.MetricMemoryAllocation, pkgopencost.MetricCPUCostTotal, pkgopencost.MetricMemoryCostTotal,
	pkgopencost.MetricNodeTotalHourlyCost, pkgopencost.MetricNodeCPUHourlyCost, pkgopencost.MetricNodeRAMHourlyCost,
	pkgopencost.MetricNodeGPUCount, pkgopencost.MetricNodeGPUHourlyCost,
	pkgopencost.MetricPVHourlyCost, pkgopencost.MetricPVCapacityBytes, pkgopencost.MetricPVClaimRef,
	pkgopencost.MetricCPUUsage, pkgopencost.MetricMemoryWorkingSet, pkgopencost.MetricPodOwner, pkgopencost.MetricReplicaSetOwner,
}

var costSelectorPattern = func() *regexp.Regexp {
	names := slices.Clone(costMetricNames)
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return regexp.MustCompile(`\b(` + strings.Join(names, "|") + `)\b(\{([^}]*)\})?`)
}()

func costSelectors(query string) map[string][]string {
	out := map[string][]string{}
	for _, m := range costSelectorPattern.FindAllStringSubmatch(query, -1) {
		out[m[1]] = append(out[m[1]], m[3])
	}
	return out
}

func everySelectorCarries(query, filter string) bool {
	selectors := costSelectors(query)
	if len(selectors) == 0 {
		return false
	}
	for _, sels := range selectors {
		for _, sel := range sels {
			if sel != filter && !strings.HasSuffix(sel, ","+filter) {
				return false
			}
		}
	}
	return true
}

// twoClusterCostStore holds clusters a and b with the same node, namespace,
// Pod and workload names. A query whose every selector carries cluster="a"
// sees a's value (1); any other sees both clusters summed (3).
type twoClusterCostStore struct {
	mu      sync.Mutex
	queries []string
	partial bool
}

func (s *twoClusterCostStore) client(t *testing.T) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.FormValue("query")
		s.mu.Lock()
		s.queries = append(s.queries, q)
		s.mu.Unlock()
		value := "3"
		if everySelectorCarries(q, clusterA) {
			value = "1"
		}
		flag := ""
		if s.partial {
			flag = `,"isPartial":true`
		}
		labels := `{"namespace":"shop","pod":"api-1","node":"n1","owner_kind":"Deployment","owner_name":"api"}`
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(`{"status":"success"` + flag + `,"data":{"resultType":"matrix","result":[{"metric":` + labels + `,"values":[[1700000000,"` + value + `"],[1700003600,"` + value + `"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success"` + flag + `,"data":{"resultType":"vector","result":[{"metric":` + labels + `,"value":[1700000000,"` + value + `"]}]}}`))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

func (s *twoClusterCostStore) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queries
	s.queries = nil
	return out
}

type scopeMode int

const (
	modeLabeled scopeMode = iota
	modeUnlabeled
	modeWithheld
	modeUsageWithheld
)

// fakeCostResolver decides scopes as a store would in each mode, and records
// the requests.
type fakeCostResolver struct {
	mode     scopeMode
	mu       sync.Mutex
	requests []prometheuspkg.ViewScopeRequest
}

func isUsageMetric(name string) bool {
	return name == pkgopencost.MetricCPUUsage || name == pkgopencost.MetricMemoryWorkingSet
}

func (f *fakeCostResolver) resolve(_ context.Context, req prometheuspkg.ViewScopeRequest) (prometheuspkg.ViewScope, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	withheld := prometheuspkg.ViewScope{State: prometheuspkg.ViewScopeSeveralClusters, Withheld: true, Note: "several clusters"}
	filters := map[string]string{}
	for _, m := range req.Metrics {
		if f.mode == modeUsageWithheld && isUsageMetric(m.Name) {
			return prometheuspkg.ViewScope{State: prometheuspkg.ViewScopeLabelMissing, Withheld: true, Note: "usage label missing"}, nil
		}
		filters[m.Name] = clusterA
		if f.mode == modeUnlabeled {
			filters[m.Name] = ""
		}
	}
	switch f.mode {
	case modeWithheld:
		return withheld, nil
	case modeUnlabeled:
		return prometheuspkg.ViewScope{State: prometheuspkg.ViewScopeUnlabeled, Filters: filters, Note: "unlabeled"}, nil
	}
	return prometheuspkg.ViewScope{State: prometheuspkg.ViewScopeVerified, Labels: map[string]string{"cluster": "a"}, Filters: filters, Note: "verified"}, nil
}

func useFakeResolver(t *testing.T, mode scopeMode) *fakeCostResolver {
	t.Helper()
	f := &fakeCostResolver{mode: mode}
	t.Cleanup(SetScopeResolverForTesting(f.resolve))
	return f
}

// costEndpoint is one metrics-store cost endpoint: its run through PromCost
// and the same call unscoped, as it was made before scoping existed.
type costEndpoint struct {
	name     string
	run      func(context.Context, PromCost) (available bool, reason string, scope *pkgopencost.ClusterScope, total float64)
	unscoped func(context.Context, *prom.Client)
	metrics  []string
	usage    []string
	span     time.Duration
}

var appRefs = []pkgopencost.ApplicationWorkloadRef{{Kind: "Deployment", Namespace: "shop", Name: "api"}}

func costEndpoints() []costEndpoint {
	summaryCosts, summaryUsage := pkgopencost.SummaryMetrics(false)
	workloadCosts, workloadUsage := pkgopencost.WorkloadsMetrics()
	day := 24 * time.Hour
	return []costEndpoint{
		{
			name: "summary", metrics: summaryCosts, usage: summaryUsage, span: time.Hour + 5*time.Minute,
			run: func(ctx context.Context, p PromCost) (bool, string, *pkgopencost.ClusterScope, float64) {
				r := p.Summary(ctx, pkgopencost.SummaryOptions{})
				return r.Available, r.Reason, r.ClusterScope, r.TotalAllocatedCost
			},
			unscoped: func(ctx context.Context, c *prom.Client) {
				pkgopencost.ComputeCostSummaryFromProm(ctx, c, pkgopencost.SummaryOptions{})
			},
		},
		{
			name: "workloads", metrics: workloadCosts, usage: workloadUsage, span: time.Hour + 5*time.Minute,
			run: func(ctx context.Context, p PromCost) (bool, string, *pkgopencost.ClusterScope, float64) {
				r := p.Workloads(ctx, "shop", nil)
				var total float64
				for _, w := range r.Workloads {
					total += w.HourlyCost
				}
				return r.Available, r.Reason, r.ClusterScope, total
			},
			unscoped: func(ctx context.Context, c *prom.Client) { pkgopencost.ComputeWorkloadsFromProm(ctx, c, "shop", nil) },
		},
		{
			name: "nodes", metrics: pkgopencost.NodeMetrics(), span: 5 * time.Minute,
			run: func(ctx context.Context, p PromCost) (bool, string, *pkgopencost.ClusterScope, float64) {
				r := p.Nodes(ctx)
				var total float64
				for _, n := range r.Nodes {
					total += n.HourlyCost
				}
				return r.Available, r.Reason, r.ClusterScope, total
			},
			unscoped: func(ctx context.Context, c *prom.Client) { pkgopencost.ComputeNodeCosts(ctx, c) },
		},
		{
			name: "trend", metrics: pkgopencost.TrendMetrics(), span: day + time.Hour + 5*time.Minute,
			run: func(ctx context.Context, p PromCost) (bool, string, *pkgopencost.ClusterScope, float64) {
				r := p.Trend(ctx, pkgopencost.TrendPromOptions{Range: "24h"})
				var total float64
				for _, s := range r.Series {
					total += s.DataPoints[len(s.DataPoints)-1].Value
				}
				return r.Available, r.Reason, r.ClusterScope, total
			},
			unscoped: func(ctx context.Context, c *prom.Client) {
				pkgopencost.ComputeCostTrendFromProm(ctx, c, pkgopencost.TrendPromOptions{Range: "24h"})
			},
		},
		{
			name: "workload-trend", metrics: pkgopencost.WorkloadTrendMetrics("Deployment"), span: day + time.Hour + 5*time.Minute,
			run: func(ctx context.Context, p PromCost) (bool, string, *pkgopencost.ClusterScope, float64) {
				r := p.WorkloadTrend(ctx, pkgopencost.WorkloadTrendOptions{Range: "24h", Namespace: "shop", Kind: "Deployment", Name: "api"})
				var last float64
				if len(r.DataPoints) > 0 {
					last = r.DataPoints[len(r.DataPoints)-1].Value
				}
				return r.Available, r.Reason, r.ClusterScope, last
			},
			unscoped: func(ctx context.Context, c *prom.Client) {
				pkgopencost.ComputeWorkloadCostTrendFromProm(ctx, c, pkgopencost.WorkloadTrendOptions{Range: "24h", Namespace: "shop", Kind: "Deployment", Name: "api"})
			},
		},
		{
			name: "application-trend", metrics: pkgopencost.ApplicationTrendMetrics(appRefs), span: day + time.Hour + 5*time.Minute,
			run: func(ctx context.Context, p PromCost) (bool, string, *pkgopencost.ClusterScope, float64) {
				r := p.ApplicationTrend(ctx, pkgopencost.ApplicationTrendOptions{Range: "24h", Workloads: appRefs})
				var last float64
				if len(r.DataPoints) > 0 {
					last = r.DataPoints[len(r.DataPoints)-1].Value
				}
				return r.Available, r.Reason, r.ClusterScope, last
			},
			unscoped: func(ctx context.Context, c *prom.Client) {
				pkgopencost.ComputeApplicationCostTrendFromProm(ctx, c, pkgopencost.ApplicationTrendOptions{Range: "24h", Workloads: appRefs})
			},
		},
	}
}

func TestCostEndpointsKeepToThisClusterInASharedStore(t *testing.T) {
	for _, ep := range costEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			resolver := useFakeResolver(t, modeLabeled)
			store := &twoClusterCostStore{}
			available, reason, scope, total := ep.run(context.Background(), PromCost{Store: store.client(t)})
			if !available || reason != "" {
				t.Fatalf("available=%v reason=%q", available, reason)
			}
			if scope == nil || scope.State != string(prometheuspkg.ViewScopeVerified) || scope.Labels["cluster"] != "a" {
				t.Fatalf("clusterScope = %+v", scope)
			}
			if total <= 0 || total >= 3 {
				t.Fatalf("total = %v, want only cluster a's share", total)
			}
			queries := store.take()
			if len(queries) == 0 {
				t.Fatal("no cost queries")
			}
			for _, q := range queries {
				if !everySelectorCarries(q, clusterA) {
					t.Errorf("unfiltered selector in %s", q)
				}
			}
			checkCostRequests(t, ep, resolver.requests)
		})
	}
}

// checkCostRequests checks the endpoint asked about exactly the metrics its
// queries read, joined, over the span they read, each anchored on the
// objects it is keyed by.
func checkCostRequests(t *testing.T, ep costEndpoint, requests []prometheuspkg.ViewScopeRequest) {
	t.Helper()
	want := 1
	if len(ep.usage) > 0 {
		want = 2
	}
	if len(requests) != want {
		t.Fatalf("%d scope checks, want %d", len(requests), want)
	}
	names := func(req prometheuspkg.ViewScopeRequest) []string {
		var out []string
		for _, m := range req.Metrics {
			out = append(out, m.Name)
		}
		sort.Strings(out)
		return out
	}
	expect := [][]string{slices.Sorted(slices.Values(ep.metrics))}
	if len(ep.usage) > 0 {
		expect = append(expect, slices.Sorted(slices.Values(slices.Concat(ep.metrics, ep.usage))))
	}
	for i, req := range requests {
		if got := names(req); !slices.Equal(got, expect[i]) {
			t.Errorf("check %d metrics = %v, want %v", i, got, expect[i])
		}
		if !req.Joined || req.Span != ep.span || !req.At.IsZero() {
			t.Errorf("check %d: joined=%v span=%v at=%v, want joined over %v at now", i, req.Joined, req.Span, req.At, ep.span)
		}
		for _, m := range req.Metrics {
			label := "pod"
			switch {
			case strings.HasPrefix(m.Name, "node_"):
				label = "node"
			case m.Name == pkgopencost.MetricReplicaSetOwner:
				label = "replicaset"
			case strings.HasPrefix(m.Name, "pv_") || strings.HasPrefix(m.Name, "kube_persistentvolume"):
				label = "persistentvolume"
			}
			if m.AnchorLabel != label {
				t.Errorf("%s anchored on %q, want %q", m.Name, m.AnchorLabel, label)
			}
			namespaced := m.Name == pkgopencost.MetricCPUUsage || m.Name == pkgopencost.MetricMemoryWorkingSet ||
				m.Name == pkgopencost.MetricPodOwner || m.Name == pkgopencost.MetricReplicaSetOwner
			if wantSubject := `namespace="shop"`; namespaced && req.Namespace == "shop" && m.Subject != wantSubject {
				t.Errorf("%s subject %q, want %q", m.Name, m.Subject, wantSubject)
			}
			if (!namespaced || req.Namespace == "") && m.Subject != "" {
				t.Errorf("%s subject %q, want the whole store", m.Name, m.Subject)
			}
			// Anchor checks stay where the anchors live: the endpoint's
			// namespace, or the cluster-wide anchor namespace.
			wantAnchorNS := req.Namespace
			if wantAnchorNS == "" {
				wantAnchorNS, _ = prometheuspkg.ClusterPodAnchors(k8s.GetResourceCache())
			}
			if label == "node" || label == "persistentvolume" {
				wantAnchorNS = ""
			}
			if m.AnchorNamespace != wantAnchorNS {
				t.Errorf("%s anchor namespace %q, want %q", m.Name, m.AnchorNamespace, wantAnchorNS)
			}
			if wantExported := label == "pod" && !namespaced; m.AnchorNamespaceExported != wantExported {
				t.Errorf("%s accepts exported_namespace=%v, want %v", m.Name, m.AnchorNamespaceExported, wantExported)
			}
		}
	}
}

func TestCostEndpointsWithheldSendNoCostQuery(t *testing.T) {
	for _, ep := range costEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			useFakeResolver(t, modeWithheld)
			store := &twoClusterCostStore{}
			available, reason, scope, total := ep.run(context.Background(), PromCost{Store: store.client(t)})
			if available || reason != pkgopencost.ReasonClusterScope || total != 0 {
				t.Fatalf("available=%v reason=%q total=%v", available, reason, total)
			}
			if scope == nil || scope.State != string(prometheuspkg.ViewScopeSeveralClusters) || scope.Note == "" {
				t.Fatalf("clusterScope = %+v", scope)
			}
			if q := store.take(); len(q) != 0 {
				t.Fatalf("withheld endpoint still queried: %v", q)
			}
		})
	}
}

func TestCostEndpointsUnlabeledSendTodaysQueries(t *testing.T) {
	for _, ep := range costEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			useFakeResolver(t, modeUnlabeled)
			scoped, unscoped := &twoClusterCostStore{}, &twoClusterCostStore{}
			available, _, scope, _ := ep.run(context.Background(), PromCost{Store: scoped.client(t)})
			ep.unscoped(context.Background(), unscoped.client(t))
			if !available || scope == nil || scope.State != string(prometheuspkg.ViewScopeUnlabeled) {
				t.Fatalf("available=%v clusterScope=%+v", available, scope)
			}
			if got, want := scoped.take(), unscoped.take(); !slices.Equal(got, want) {
				t.Fatalf("queries differ\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

func TestCostEndpointsPartialAnswersAreUnavailable(t *testing.T) {
	for _, ep := range costEndpoints() {
		t.Run(ep.name, func(t *testing.T) {
			useFakeResolver(t, modeLabeled)
			store := &twoClusterCostStore{partial: true}
			available, reason, scope, _ := ep.run(context.Background(), PromCost{Store: store.client(t)})
			if available || reason != pkgopencost.ReasonPartialData || scope == nil {
				t.Fatalf("available=%v reason=%q clusterScope=%+v", available, reason, scope)
			}
		})
	}
}

func TestCostEndpointsWithOnlyUsageWithheldKeepCosts(t *testing.T) {
	useFakeResolver(t, modeUsageWithheld)
	store := &twoClusterCostStore{}
	p := PromCost{Store: store.client(t)}

	summary := p.Summary(context.Background(), pkgopencost.SummaryOptions{})
	if !summary.Available || summary.TotalAllocatedCost <= 0 || !summary.EfficiencyUnavailable || summary.ClusterEfficiency != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ClusterScope == nil || summary.ClusterScope.State != string(prometheuspkg.ViewScopeVerified) ||
		summary.UsageClusterScope == nil || summary.UsageClusterScope.State != string(prometheuspkg.ViewScopeLabelMissing) {
		t.Fatalf("scopes = %+v / %+v", summary.ClusterScope, summary.UsageClusterScope)
	}
	workloads := p.Workloads(context.Background(), "shop", nil)
	if !workloads.Available || !workloads.EfficiencyUnavailable || len(workloads.Workloads) == 0 || workloads.Workloads[0].CPUUsageAvailable {
		t.Fatalf("workloads = %+v", workloads)
	}
	for _, q := range store.take() {
		if strings.Contains(q, pkgopencost.MetricCPUUsage) || strings.Contains(q, pkgopencost.MetricMemoryWorkingSet) {
			t.Fatalf("withheld usage still queried: %s", q)
		}
		if !everySelectorCarries(q, clusterA) {
			t.Fatalf("unfiltered selector in %s", q)
		}
	}
}

func TestCostScopeCheckEndingWithTheCallerIsAQueryError(t *testing.T) {
	t.Cleanup(SetScopeResolverForTesting(func(ctx context.Context, _ prometheuspkg.ViewScopeRequest) (prometheuspkg.ViewScope, error) {
		return prometheuspkg.ViewScope{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &twoClusterCostStore{}
	resp := PromCost{Store: store.client(t)}.Summary(ctx, pkgopencost.SummaryOptions{})
	if resp.Available || resp.Reason != pkgopencost.ReasonQueryError || len(store.take()) != 0 {
		t.Fatalf("summary = %+v", resp)
	}
}

func TestSkipNodeCostSummaryChecksNoNodeTotals(t *testing.T) {
	resolver := useFakeResolver(t, modeLabeled)
	store := &twoClusterCostStore{}
	PromCost{Store: store.client(t)}.Summary(context.Background(), pkgopencost.SummaryOptions{SkipNodeCost: true})
	for _, req := range resolver.requests {
		for _, m := range req.Metrics {
			if m.Name == pkgopencost.MetricNodeTotalHourlyCost || m.Name == pkgopencost.MetricNodeGPUCount {
				t.Fatalf("checked %s, which a namespace-filtered summary never reads", m.Name)
			}
		}
	}
}

func TestApplicationClusterScopePrefersWithheldNamespace(t *testing.T) {
	ok := &pkgopencost.ClusterScope{State: "scoped_verified"}
	withheld := &pkgopencost.ClusterScope{State: "several_clusters"}
	got := ApplicationClusterScope(map[string]*pkgopencost.WorkloadCostResponse{
		"a": {Available: true, ClusterScope: ok},
		"b": {Reason: pkgopencost.ReasonClusterScope, ClusterScope: withheld},
	})
	if got != withheld {
		t.Fatalf("got %+v", got)
	}
	if got := ApplicationClusterScope(map[string]*pkgopencost.WorkloadCostResponse{"a": {Available: true, ClusterScope: ok}}); got != ok {
		t.Fatalf("got %+v", got)
	}
}

// A store holding several clusters' OpenCost metrics is a Prometheus cost
// source even when this cluster's costs are withheld: auto selection keeps
// it, never calls it absent, and never falls over to Kubecost.
func TestAutoSelectionKeepsAWithheldStore(t *testing.T) {
	if err := k8s.InitTestResourceCache(fakeclientset.NewSimpleClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"namespace":"shop","cluster":"a"},"value":[1700000000,"1"]},` +
			`{"metric":{"namespace":"shop","cluster":"b"},"value":[1700000000,"2"]}]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(promServer.URL)
	t.Cleanup(func() {
		promServer.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})
	useFakeResolver(t, modeWithheld)

	if state := detectPrometheusCostState(context.Background()); state != prometheusCostAvailable {
		t.Fatalf("probe state = %v, want available", state)
	}
	m := &Manager{config: ManagerConfig{Source: SourceAuto}}
	connection, err := m.Selected(context.Background())
	if err != nil || connection.Source != SourcePrometheus {
		t.Fatalf("selected %+v, %v; want the metrics store", connection, err)
	}
	client, err := prometheuspkg.ClientForOperation()
	if err != nil {
		t.Fatal(err)
	}
	resp := NewPromCost(client).Summary(context.Background(), pkgopencost.SummaryOptions{})
	if resp.Available || resp.Reason != pkgopencost.ReasonClusterScope || resp.ClusterScope == nil {
		t.Fatalf("summary = %+v", resp)
	}
}
