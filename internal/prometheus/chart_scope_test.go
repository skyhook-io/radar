package prometheus

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

var chartMetricNames = []string{
	prom.MetricCPUUsage, prom.MetricMemoryWorkingSet, prom.MetricNetworkReceive, prom.MetricNetworkTransmit,
	prom.MetricFSWrites, prom.MetricFSReads, prom.MetricContainerRestarts,
	prom.MetricNodeCPU, prom.MetricNodeMemTotal, prom.MetricNodeMemAvailable, prom.MetricNodeFSSize, prom.MetricNodeFSAvail,
	prom.MetricHPACurrentReplicas, prom.MetricHPADesiredReplicas,
}

var chartSelectorPattern = regexp.MustCompile(`([a-zA-Z_:]+)(\{([^}]*)\})?`)

// chartSelectors returns each chart metric's selectors in a query, a bare
// metric name as an empty selector.
func chartSelectors(query string) map[string][]string {
	out := map[string][]string{}
	for _, m := range chartSelectorPattern.FindAllStringSubmatch(query, -1) {
		if slices.Contains(chartMetricNames, m[1]) {
			out[m[1]] = append(out[m[1]], m[3])
		}
	}
	return out
}

func allSelectorsCarry(query, filter string) bool {
	selectors := chartSelectors(query)
	if len(selectors) == 0 {
		return false
	}
	for _, sels := range selectors {
		for _, sel := range sels {
			if !strings.HasSuffix(sel, ","+filter) && sel != filter {
				return false
			}
		}
	}
	return true
}

// twoClusterStore holds the same-named objects in clusters a and b: a query
// whose every selector carries cluster="a" sees a's value (1); any other sees
// both, summed (3).
type twoClusterStore struct {
	mu      sync.Mutex
	queries []string
	answer  func(query string) (*prom.QueryResult, error)
}

func (s *twoClusterStore) QueryRange(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	if s.answer != nil {
		return s.answer(query)
	}
	value := 3.0
	if allSelectorsCarry(query, clusterAFilter) {
		value = 1
	}
	return &prom.QueryResult{ResultType: "matrix", Series: []prom.Series{vec(map[string]string{}, value)}}, nil
}

var readbackMetricPattern = regexp.MustCompile(`"radar_metric", "([^"]+)"`)

// scopeStoreResolver decides scopes against a fake store: labels proven as
// cluster="a" with every metric reading them back (labeled), or no labels
// anywhere and identities as identity returns them.
func scopeStoreResolver(labels map[string]string, identity func(metric string) []map[string]string) (ChartScopeResolver, *[]ViewScopeRequest) {
	var seen []ViewScopeRequest
	var mu sync.Mutex
	return func(ctx context.Context, req ViewScopeRequest) (ViewScope, error) {
		req, err := normalizeViewScopeRequest(req)
		if err != nil {
			return unknownViewScope(), err
		}
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
			var out []prom.Series
			for _, m := range readbackMetricPattern.FindAllStringSubmatch(query, -1) {
				if isReadback(query) {
					out = append(out, viewRow(m[1], labels))
					continue
				}
				for _, id := range identity(m[1]) {
					out = append(out, viewRow(m[1], id))
				}
			}
			return rows(out...)
		}}
		return decideViewScope(ctx, q, req, time.Now(), labels, false)
	}, &seen
}

type chartCase struct {
	name string
	run  func(context.Context, chartDeps) (results []*prom.QueryResult, scope *ClusterScope, partial bool, err error)
	// golden is what an unlabeled store must receive.
	golden []string
}

func chartCases() []chartCase {
	var cases []chartCase
	workload := &PodScope{Kind: "Deployment", Namespace: "shop", Name: "api", CurrentPods: []string{"api-1", "api-2"}, CurrentTotal: 2, Selection: prom.SelectPods("shop", []string{"api-1", "api-2"})}
	for _, cat := range []prom.MetricCategory{prom.CategoryCPU, prom.CategoryMemory, prom.CategoryNetworkRX, prom.CategoryNetworkTX} {
		cases = append(cases,
			chartCase{name: "cluster/" + string(cat), golden: []string{prom.BuildClusterQuery(cat)}, run: func(ctx context.Context, d chartDeps) ([]*prom.QueryResult, *ClusterScope, bool, error) {
				r, err := clusterChart(ctx, d, cat, "1h")
				return []*prom.QueryResult{r.Result}, r.ClusterScope, r.Partial, err
			}},
			chartCase{name: "namespace/" + string(cat), golden: []string{prom.BuildNamespaceQuery("shop", cat)}, run: func(ctx context.Context, d chartDeps) ([]*prom.QueryResult, *ClusterScope, bool, error) {
				r, err := namespaceChart(ctx, d, "shop", cat, "1h")
				return []*prom.QueryResult{r.Result}, r.ClusterScope, r.Partial, err
			}},
		)
	}
	for _, cat := range prom.AllCategories() {
		cases = append(cases,
			chartCase{name: "pod/" + string(cat), golden: []string{prom.BuildQuery("Pod", "shop", "api-1", cat)}, run: func(ctx context.Context, d chartDeps) ([]*prom.QueryResult, *ClusterScope, bool, error) {
				r, err := resourceChart(ctx, d, "Pod", "shop", "api-1", cat, "1h", nil)
				return []*prom.QueryResult{r.Result}, r.ClusterScope, r.Partial, err
			}},
			chartCase{name: "workload/" + string(cat), golden: []string{prom.BuildScopedQuery(workload.Selection, cat, prom.AggregatePerPod, true)}, run: func(ctx context.Context, d chartDeps) ([]*prom.QueryResult, *ClusterScope, bool, error) {
				r, err := resourceChart(ctx, d, "Deployment", "shop", "api", cat, "1h", workload)
				return []*prom.QueryResult{r.Result}, r.ClusterScope, r.Partial, err
			}},
		)
	}
	for _, cat := range prom.CategoriesForKind("Node") {
		cases = append(cases, chartCase{name: "node/" + string(cat), golden: []string{prom.BuildQuery("Node", "", "worker-1", cat)}, run: func(ctx context.Context, d chartDeps) ([]*prom.QueryResult, *ClusterScope, bool, error) {
			r, err := resourceChart(ctx, d, "Node", "", "worker-1", cat, "1h", nil)
			return []*prom.QueryResult{r.Result}, r.ClusterScope, r.Partial, err
		}})
	}
	current, desired := prom.BuildHPAReplicasQueries("shop", "api")
	cases = append(cases, chartCase{name: "hpa", golden: []string{current, desired}, run: func(ctx context.Context, d chartDeps) ([]*prom.QueryResult, *ClusterScope, bool, error) {
		r, err := hpaChart(ctx, d, "shop", "api", "1h")
		return []*prom.QueryResult{r.Current, r.Desired}, r.ClusterScope, r.Partial, err
	}})
	return cases
}

func TestChartsTwoClustersKeepToThisCluster(t *testing.T) {
	resolve, _ := scopeStoreResolver(clusterA, nil)
	for _, tc := range chartCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := &twoClusterStore{}
			results, scope, _, err := tc.run(context.Background(), chartDeps{q: store, resolve: resolve})
			if err != nil {
				t.Fatal(err)
			}
			if len(store.queries) != len(tc.golden) {
				t.Fatalf("queries = %v", store.queries)
			}
			for _, q := range store.queries {
				if !allSelectorsCarry(q, clusterAFilter) {
					t.Errorf("selector without this cluster's filter: %s", q)
				}
			}
			for _, r := range results {
				if len(r.Series) != 1 || r.Series[0].DataPoints[0].Value != 1 {
					t.Errorf("values mixed with the other cluster: %+v", r.Series)
				}
			}
			if scope == nil || scope.State != ViewScopeVerified || scope.Labels["cluster"] != "a" {
				t.Errorf("clusterScope = %+v", scope)
			}
		})
	}
}

func TestChartsWithheldSendNoDataQuery(t *testing.T) {
	several := func(string) []map[string]string { return []map[string]string{clusterA, {"cluster": "b"}} }
	resolvers := map[ViewScopeState]ChartScopeResolver{}
	resolvers[ViewScopeSeveralClusters], _ = scopeStoreResolver(nil, several)
	resolvers[ViewScopeLabelMissing] = func(ctx context.Context, req ViewScopeRequest) (ViewScope, error) {
		req, _ = normalizeViewScopeRequest(req)
		q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
			var out []prom.Series
			for _, m := range readbackMetricPattern.FindAllStringSubmatch(query, -1) {
				if isReadback(query) {
					out = append(out, viewRow(m[1], nil))
				}
			}
			return rows(out...)
		}}
		return decideViewScope(ctx, q, req, time.Now(), clusterA, true)
	}
	resolvers[ViewScopeUnknown] = func(context.Context, ViewScopeRequest) (ViewScope, error) {
		return unknownViewScope(), errors.New("store unreachable")
	}
	for state, resolve := range resolvers {
		for _, tc := range chartCases() {
			t.Run(string(state)+"/"+tc.name, func(t *testing.T) {
				store := &twoClusterStore{}
				results, scope, _, err := tc.run(context.Background(), chartDeps{q: store, resolve: resolve})
				if err != nil {
					t.Fatalf("withheld chart failed: %v", err)
				}
				if len(store.queries) != 0 {
					t.Fatalf("withheld chart queried the store: %v", store.queries)
				}
				for _, r := range results {
					if r == nil || r.Series == nil || len(r.Series) != 0 {
						t.Errorf("withheld result = %+v, want an empty series list", r)
					}
				}
				if scope == nil || scope.State != state || scope.Note == "" {
					t.Errorf("clusterScope = %+v, want %s with a note", scope, state)
				}
			})
		}
	}
}

func TestChartsUnlabeledStoreSendsGoldenQueries(t *testing.T) {
	for name, identity := range map[string]func(string) []map[string]string{
		"one unlabeled identity": func(string) []map[string]string { return []map[string]string{nil} },
		"no series":              func(string) []map[string]string { return nil },
	} {
		resolve, _ := scopeStoreResolver(nil, identity)
		for _, tc := range chartCases() {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				store := &twoClusterStore{}
				_, scope, _, err := tc.run(context.Background(), chartDeps{q: store, resolve: resolve})
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(store.queries, tc.golden) {
					t.Fatalf("queries changed for an unlabeled store\n got: %q\nwant: %q", store.queries, tc.golden)
				}
				if scope == nil || scope.State != ViewScopeUnlabeled {
					t.Errorf("clusterScope = %+v", scope)
				}
			})
		}
	}
}

func TestChartsContainerFilterFallbackIsFiltered(t *testing.T) {
	resolve, _ := scopeStoreResolver(clusterA, nil)
	for _, tc := range chartCases() {
		if !strings.HasSuffix(tc.name, "/cpu") && !strings.HasSuffix(tc.name, "/memory") || strings.HasPrefix(tc.name, "node/") {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			store := &twoClusterStore{}
			store.answer = func(query string) (*prom.QueryResult, error) {
				if strings.Contains(query, "container!=''") {
					return &prom.QueryResult{ResultType: "matrix"}, nil
				}
				return &prom.QueryResult{ResultType: "matrix", Series: []prom.Series{vec(map[string]string{}, 1)}}, nil
			}
			results, _, _, err := tc.run(context.Background(), chartDeps{q: store, resolve: resolve})
			if err != nil {
				t.Fatal(err)
			}
			if len(store.queries) != 2 || strings.Contains(store.queries[1], "container!=''") {
				t.Fatalf("no fallback without the container filter: %v", store.queries)
			}
			for _, q := range store.queries {
				if !allSelectorsCarry(q, clusterAFilter) {
					t.Errorf("unfiltered selector: %s", q)
				}
			}
			if len(results[0].Series) != 1 {
				t.Errorf("fallback series not returned: %+v", results[0])
			}
		})
	}
}

func TestChartsPartialAnswersAreMarked(t *testing.T) {
	resolve, _ := scopeStoreResolver(nil, func(string) []map[string]string { return nil })
	for _, tc := range chartCases() {
		t.Run(tc.name, func(t *testing.T) {
			store := &twoClusterStore{answer: func(string) (*prom.QueryResult, error) {
				return &prom.QueryResult{ResultType: "matrix", Series: []prom.Series{vec(map[string]string{}, 1)}, Annotations: prom.QueryAnnotations{Partial: true}}, nil
			}}
			results, _, partial, err := tc.run(context.Background(), chartDeps{q: store, resolve: resolve})
			if err != nil {
				t.Fatal(err)
			}
			if !partial || len(results[0].Series) != 1 {
				t.Errorf("partial = %v, series = %+v; want drawn and marked", partial, results[0])
			}
		})
	}
}

func TestChartsCallerLeavingDuringScopeCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resolve := func(ctx context.Context, _ ViewScopeRequest) (ViewScope, error) { return unknownViewScope(), ctx.Err() }
	for _, tc := range chartCases() {
		store := &twoClusterStore{}
		if _, _, _, err := tc.run(ctx, chartDeps{q: store, resolve: resolve}); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v, want the caller's cancellation", tc.name, err)
		}
		if len(store.queries) != 0 {
			t.Errorf("%s: queried after the caller left", tc.name)
		}
	}
}

func TestChartScopeRequestsAnchorOnLiveObjects(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns-1"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-0-1.ec2.internal"}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	cache := k8s.GetResourceCache()

	identity := func(req ViewScopeRequest) string {
		req, err := normalizeViewScopeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		return viewIdentityQuery(req.Metrics, []string{"cluster"}, req.Span, true)
	}
	for _, tc := range []struct {
		name string
		req  ViewScopeRequest
		want []string
	}{
		{"cluster", clusterChartScopeRequest(prom.CategoryCPU, time.Hour, cache), []string{
			`container_cpu_usage_seconds_total{pod=~"coredns-1"}[4200s]`,
		}},
		{"namespace", namespaceChartScopeRequest("shop", prom.CategoryMemory, time.Hour, cache), []string{
			`container_memory_working_set_bytes{namespace="shop",pod=~"api-1"}[4200s]`,
		}},
		{"pod restarts", podChartScopeRequest("shop", "api-1", prom.CategoryRestarts, time.Hour, cache), []string{
			`kube_pod_container_status_restarts_total{namespace="shop",pod=~"api-1"}[7500s]`,
		}},
		{"node memory", nodeChartScopeRequest("ip-10-0-0-1.ec2.internal", prom.CategoryMemory, time.Hour, cache), []string{
			`node_memory_MemTotal_bytes{instance=~"ip-10-0-0-1\\.ec2\\.internal(:[0-9]+)?",instance=~"(ip-10-0-0-1\\.ec2\\.internal)(:[0-9]+)?"}`,
			`node_memory_MemAvailable_bytes{instance=~"ip-10-0-0-1\\.ec2\\.internal(:[0-9]+)?",instance=~"(ip-10-0-0-1\\.ec2\\.internal)(:[0-9]+)?"}`,
		}},
		{"hpa", hpaChartScopeRequest("shop", "api", time.Hour, cache), []string{
			`kube_horizontalpodautoscaler_status_current_replicas{namespace="shop",horizontalpodautoscaler=~"api"}[3900s]`,
			`kube_horizontalpodautoscaler_status_desired_replicas{namespace="shop",horizontalpodautoscaler=~"api"}[3900s]`,
		}},
		{"workload", PodSetScopeRequest("workload_chart", prom.SelectPods("shop", []string{"api-2", "api-1"}), []prom.MetricCategory{prom.CategoryNetworkRX}, time.Hour), []string{
			`container_network_receive_bytes_total{namespace="shop",pod=~"api-1|api-2"}`,
		}},
	} {
		got := identity(tc.req)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: identity query lacks %s\n%s", tc.name, want, got)
			}
		}
	}

	// An object that isn't live here anchors nothing.
	for name, req := range map[string]ViewScopeRequest{
		"pod":  podChartScopeRequest("shop", "gone", prom.CategoryCPU, time.Hour, cache),
		"node": nodeChartScopeRequest("gone", prom.CategoryCPU, time.Hour, cache),
		"hpa":  hpaChartScopeRequest("shop", "gone", time.Hour, cache),
	} {
		if got := identity(req); strings.Contains(got, viewScopeAnchorLabel) {
			t.Errorf("%s: anchored on an object this cluster doesn't have: %s", name, got)
		}
	}
}

// The HTTP routes answer a withheld chart with 200, no series and the reason.
func TestChartRoutesWithheldAnswer200(t *testing.T) {
	SetAuthGate(nil)
	f := setupAuthFakeProm(t)
	f.queryBody = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	f.queryFn = func(query string) string {
		if !strings.HasPrefix(query, "label_replace(topk(2") {
			return ""
		}
		var b strings.Builder
		b.WriteString(`{"status":"success","data":{"resultType":"vector","result":[`)
		for i, m := range readbackMetricPattern.FindAllStringSubmatch(query, -1) {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"metric":{"radar_metric":"` + m[1] + `","cluster":"a"},"value":[1700000000,"1"]},`)
			b.WriteString(`{"metric":{"radar_metric":"` + m[1] + `","cluster":"b"},"value":[1700000000,"1"]}`)
		}
		b.WriteString(`]}}`)
		return b.String()
	}
	h := metricsRouter()
	for _, path := range []string{
		"/prometheus/cluster?category=cpu",
		"/prometheus/namespace/alpha?category=memory",
		"/prometheus/resources/Pod/alpha/web-7f6-a?category=restarts",
		"/prometheus/resources/Deployment/alpha/web?category=cpu",
		"/prometheus/resources/Node/worker-1?category=cpu",
		"/prometheus/hpa/alpha/web",
	} {
		rec := getMetrics(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d: %s", path, rec.Code, rec.Body.String())
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"clusterScope":{"state":"several_clusters"`) || strings.Contains(body, `"values"`) {
			t.Errorf("%s: body = %s", path, body)
		}
	}
	if got := f.queries(t); len(got) != 0 {
		t.Fatalf("withheld charts sent data queries: %v", got)
	}
}
