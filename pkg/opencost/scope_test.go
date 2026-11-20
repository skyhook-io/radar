package opencost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

var allCostMetrics = []string{
	MetricCPUAllocation, MetricMemoryAllocation, MetricCPUCostTotal, MetricMemoryCostTotal,
	MetricNodeTotalHourlyCost, MetricNodeCPUHourlyCost, MetricNodeRAMHourlyCost, MetricNodeGPUCount, MetricNodeGPUHourlyCost,
	MetricPVHourlyCost, MetricPVCapacityBytes, MetricPVClaimRef,
	MetricCPUUsage, MetricMemoryWorkingSet, MetricPodOwner, MetricReplicaSetOwner,
}

var costSelectorPattern = func() *regexp.Regexp {
	names := slices.Clone(allCostMetrics)
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	return regexp.MustCompile(`\b(` + strings.Join(names, "|") + `)\b(\{([^}]*)\})?`)
}()

// costSelectors returns each cost metric's selectors in a query, a bare
// metric name as an empty selector.
func costSelectors(query string) map[string][]string {
	out := map[string][]string{}
	for _, m := range costSelectorPattern.FindAllStringSubmatch(query, -1) {
		out[m[1]] = append(out[m[1]], m[3])
	}
	return out
}

func clusterFilterFor(metric string) string {
	return `cluster="a",radar_m="` + metric + `"`
}

// queriesByCase runs every compute path through scope and returns each
// case's queries, unquoted.
func queriesByCase(t *testing.T, scope *StoreScope) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range costGoldenLines(t, costGoldenCases(scope)) {
		name, rest, _ := strings.Cut(line, "\t")
		_, quoted, _ := strings.Cut(rest, " ")
		q, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = append(out[name], q)
	}
	return out
}

func TestScopedCostQueriesFilterEverySelector(t *testing.T) {
	byCase := queriesByCase(t, &StoreScope{Filter: clusterFilterFor})
	if len(byCase) == 0 {
		t.Fatal("no queries")
	}
	for name, queries := range byCase {
		for _, q := range queries {
			selectors := costSelectors(q)
			if len(selectors) == 0 {
				t.Fatalf("%s: no cost metric in %s", name, q)
			}
			for metric, sels := range selectors {
				for _, s := range sels {
					if s != clusterFilterFor(metric) && !strings.HasSuffix(s, ","+clusterFilterFor(metric)) {
						t.Errorf("%s: %s selector {%s} lacks its filter in %s", name, metric, s, q)
					}
				}
			}
		}
	}
}

func metricsIn(queries []string) []string {
	seen := map[string]bool{}
	for _, q := range queries {
		for metric := range costSelectors(q) {
			seen[metric] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in ...[]string) []string {
	var out []string
	for _, s := range in {
		out = append(out, s...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// The metric lists the scope checks are built from must be exactly what each
// path's queries read, fallbacks included.
// A configured label value may hold any character; a % in one must reach
// the store as written, never be read as a formatting verb.
func TestScopedCostQueriesKeepPercentInLabelValues(t *testing.T) {
	filter := func(metric string) string { return `cluster="prod%eu",radar_m="` + metric + `"` }
	byCase := queriesByCase(t, &StoreScope{Filter: filter})
	if len(byCase) == 0 {
		t.Fatal("no queries")
	}
	for name, queries := range byCase {
		for _, q := range queries {
			if strings.Contains(q, "%!") || strings.Contains(q, "%%") {
				t.Errorf("%s: formatting corrupted the query: %s", name, q)
			}
			for metric, sels := range costSelectors(q) {
				for _, s := range sels {
					if s != filter(metric) && !strings.HasSuffix(s, ","+filter(metric)) {
						t.Errorf("%s: %s selector {%s} lacks its filter in %s", name, metric, s, q)
					}
				}
			}
		}
	}
}

func TestCostMetricListsMatchQueries(t *testing.T) {
	byCase := queriesByCase(t, nil)
	join := func(names ...string) []string {
		var out []string
		for _, n := range names {
			out = append(out, byCase[n]...)
		}
		return out
	}
	summaryCosts, summaryUsage := SummaryMetrics(false)
	skipCosts, skipUsage := SummaryMetrics(true)
	workloadCosts, workloadUsage := WorkloadsMetrics()
	for _, tc := range []struct {
		name    string
		queries []string
		want    []string
	}{
		{"summary", join("summary", "summary-fallback"), sortedCopy(summaryCosts, summaryUsage)},
		// The skip-node-cost case never fails over, so it lacks the fallback
		// cost totals the list keeps for when it does.
		{"summary-skip-node-cost", join("summary-skip-node-cost"), sortedCopy(slices.DeleteFunc(slices.Clone(skipCosts), func(m string) bool {
			return m == MetricCPUCostTotal || m == MetricMemoryCostTotal
		}), skipUsage)},
		{"workloads", join("workloads", "workloads-fallback"), sortedCopy(workloadCosts, workloadUsage)},
		{"nodes", join("nodes"), sortedCopy(NodeMetrics())},
		{"trend", join("trend"), sortedCopy(TrendMetrics())},
		{"workload-trend-deployment", join("workload-trend-deployment", "workload-trend-fallback"), sortedCopy(WorkloadTrendMetrics("Deployment"))},
		{"workload-trend-statefulset", join("workload-trend-statefulset"), sortedCopy(slices.DeleteFunc(WorkloadTrendMetrics("StatefulSet"), func(m string) bool {
			return m == MetricCPUCostTotal || m == MetricMemoryCostTotal
		}))},
		{"application-trend", join("application-trend", "application-trend-fallback"), sortedCopy(ApplicationTrendMetrics(goldenAppRefs))},
	} {
		if got := metricsIn(tc.queries); !slices.Equal(got, tc.want) {
			t.Errorf("%s reads %v, list says %v", tc.name, got, tc.want)
		}
	}
	if slices.Contains(WorkloadTrendMetrics("StatefulSet"), MetricReplicaSetOwner) {
		t.Error("a StatefulSet trend reads no ReplicaSet ownership")
	}
	if slices.Contains(ApplicationTrendMetrics([]ApplicationWorkloadRef{{Kind: "DaemonSet"}}), MetricReplicaSetOwner) {
		t.Error("an application without Deployments reads no ReplicaSet ownership")
	}
}

// annotatedStore answers every query with one sample; partial makes the
// answers partial, as a store with a backend down reports them.
func annotatedStore(t *testing.T, partial bool, queries *[]string) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if queries != nil {
			*queries = append(*queries, r.FormValue("query"))
		}
		flag := ""
		if partial {
			flag = `,"isPartial":true`
		}
		labels := `{"namespace":"shop","pod":"api-1","node":"n1","owner_kind":"Deployment","owner_name":"api"}`
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(`{"status":"success"` + flag + `,"data":{"resultType":"matrix","result":[{"metric":` + labels + `,"values":[[1700000000,"1"],[1700003600,"1"]]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success"` + flag + `,"data":{"resultType":"vector","result":[{"metric":` + labels + `,"value":[1700000000,"1"]}]}}`))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

func TestStrictScopeTurnsPartialAnswersUnavailable(t *testing.T) {
	ctx := context.Background()
	wire := &ClusterScope{State: "scoped_verified", Note: "n"}
	strict := StoreScope{Strict: true, ClusterScope: wire}
	for _, partial := range []bool{false, true} {
		c := annotatedStore(t, partial, nil)
		check := func(name string, available bool, reason string, scope *ClusterScope) {
			t.Helper()
			if partial && (available || reason != ReasonPartialData) {
				t.Errorf("%s: partial answer gave available=%v reason=%q", name, available, reason)
			}
			if !partial && !available {
				t.Errorf("%s: complete answer unavailable (%s)", name, reason)
			}
			if scope != wire {
				t.Errorf("%s: clusterScope not echoed", name)
			}
		}
		s := ComputeCostSummaryFromProm(ctx, c, SummaryOptions{Scope: strict})
		check("summary", s.Available, s.Reason, s.ClusterScope)
		w := ComputeWorkloadsFromPromScoped(ctx, c, "shop", nil, strict)
		check("workloads", w.Available, w.Reason, w.ClusterScope)
		n := ComputeNodeCostsScoped(ctx, c, strict)
		check("nodes", n.Available, n.Reason, n.ClusterScope)
		tr := ComputeCostTrendFromProm(ctx, c, TrendPromOptions{Range: "24h", Scope: strict})
		check("trend", tr.Available, tr.Reason, tr.ClusterScope)
		wt := ComputeWorkloadCostTrendFromProm(ctx, c, WorkloadTrendOptions{Range: "24h", Namespace: "shop", Kind: "Deployment", Name: "api", Scope: strict})
		check("workload-trend", wt.Available, wt.Reason, wt.ClusterScope)
		at := ComputeApplicationCostTrendFromProm(ctx, c, ApplicationTrendOptions{
			Range: "24h", Workloads: []ApplicationWorkloadRef{{Kind: "Deployment", Namespace: "shop", Name: "api"}},
			ScopeFor: func(string) (StoreScope, string) { return strict, "" },
		})
		check("application-trend", at.Available, at.Reason, at.ClusterScope)
	}

	// Without a strict scope, a partial answer still counts toward the totals.
	s := ComputeCostSummaryFromProm(ctx, annotatedStore(t, true, nil), SummaryOptions{})
	if !s.Available {
		t.Fatalf("unscoped partial answer = %+v", s)
	}
}

func TestUsageWithheldKeepsCostsAndMarksEfficiencyUnavailable(t *testing.T) {
	ctx := context.Background()
	usageNote := &ClusterScope{State: "label_missing", Note: "usage"}
	scope := StoreScope{Strict: true, UsageWithheld: true, UsageClusterScope: usageNote}
	var queries []string
	c := annotatedStore(t, false, &queries)

	s := ComputeCostSummaryFromProm(ctx, c, SummaryOptions{Scope: scope})
	if !s.Available || s.TotalHourlyCost == 0 || !s.EfficiencyUnavailable || s.UsageClusterScope != usageNote || s.ClusterEfficiency != 0 {
		t.Fatalf("summary = %+v", s)
	}
	for _, row := range s.Namespaces {
		if !row.UsageUnavailable || row.Efficiency != 0 || row.IdleCost != 0 {
			t.Fatalf("row %+v reads usage it never measured", row)
		}
	}
	w := ComputeWorkloadsFromPromScoped(ctx, c, "shop", nil, scope)
	if !w.Available || !w.EfficiencyUnavailable || len(w.Workloads) == 0 {
		t.Fatalf("workloads = %+v", w)
	}
	for _, wl := range w.Workloads {
		if wl.CPUUsageAvailable || wl.MemoryUsageAvailable || wl.Efficiency != 0 {
			t.Fatalf("workload %+v reads usage it never measured", wl)
		}
	}
	for _, q := range queries {
		if strings.Contains(q, MetricCPUUsage) || strings.Contains(q, MetricMemoryWorkingSet) {
			t.Fatalf("withheld usage still queried: %s", q)
		}
	}
}

func TestApplicationTrendWithheldNamespaceSendsNoQuery(t *testing.T) {
	var queries []string
	c := annotatedStore(t, false, &queries)
	withheld := &ClusterScope{State: "several_clusters", Note: "several"}
	resp := ComputeApplicationCostTrendFromProm(context.Background(), c, ApplicationTrendOptions{
		Range:     "24h",
		Workloads: []ApplicationWorkloadRef{{Kind: "Deployment", Namespace: "shop", Name: "api"}},
		ScopeFor: func(string) (StoreScope, string) {
			return StoreScope{ClusterScope: withheld}, ReasonClusterScope
		},
	})
	if resp.Available || resp.Reason != ReasonClusterScope || resp.ClusterScope != withheld || len(queries) != 0 {
		t.Fatalf("resp = %+v, queries = %v", resp, queries)
	}
	if len(resp.Coverage.Unavailable) != 1 || resp.Coverage.Unavailable[0].Reason != ReasonClusterScope {
		t.Fatalf("coverage = %+v", resp.Coverage)
	}
}

// A namespace whose check failed carries no scope, and must not stop a later
// namespace withheld for cluster scope from naming the scope that withheld it.
func TestApplicationTrendShowsTheWithheldNamespacesScope(t *testing.T) {
	c := annotatedStore(t, false, nil)
	verified := &ClusterScope{State: "scoped_verified", Note: "verified"}
	withheld := &ClusterScope{State: "several_clusters", Note: "several"}
	resp := ComputeApplicationCostTrendFromProm(context.Background(), c, ApplicationTrendOptions{
		Range: "24h",
		Workloads: []ApplicationWorkloadRef{
			{Kind: "Deployment", Namespace: "a-failed", Name: "api"},
			{Kind: "Deployment", Namespace: "b-verified", Name: "api"},
			{Kind: "Deployment", Namespace: "c-withheld", Name: "api"},
		},
		ScopeFor: func(namespace string) (StoreScope, string) {
			switch namespace {
			case "a-failed":
				return StoreScope{}, ReasonQueryError
			case "b-verified":
				return StoreScope{Strict: true, ClusterScope: verified}, ""
			}
			return StoreScope{ClusterScope: withheld}, ReasonClusterScope
		},
	})
	if resp.ClusterScope != withheld {
		t.Fatalf("clusterScope = %+v, want the withheld namespace's", resp.ClusterScope)
	}
}
