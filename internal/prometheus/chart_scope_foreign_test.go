package prometheus

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// The overview and namespace charts sum their metrics, so each carries the
// Pod foreign check; per-object charts name this cluster's objects and don't.
func TestChartScopeRequestsCarryForeignCheck(t *testing.T) {
	for _, cat := range prom.AllCategories() {
		for name, req := range map[string]ViewScopeRequest{
			"cluster":   clusterChartScopeRequest(cat, time.Hour, nil),
			"namespace": namespaceChartScopeRequest("shop", cat, time.Hour, nil),
		} {
			for _, m := range req.Metrics {
				if !reflect.DeepEqual(m.Foreign, PodForeignCheck(false)) {
					t.Errorf("%s/%s: %s carries %+v", name, cat, m.Name, m.Foreign)
				}
			}
		}
		for name, req := range map[string]ViewScopeRequest{
			"pod":      podChartScopeRequest("shop", "api-1", cat, time.Hour, nil),
			"node":     nodeChartScopeRequest("worker-1", cat, time.Hour, nil),
			"workload": PodSetScopeRequest("workload_chart", prom.SelectPods("shop", []string{"api-1"}), []prom.MetricCategory{cat}, time.Hour),
			"hpa":      hpaChartScopeRequest("shop", "api", time.Hour, nil),
		} {
			for _, m := range req.Metrics {
				if m.Foreign.Reference != "" {
					t.Errorf("%s/%s: per-object chart's %s carries a foreign check", name, cat, m.Name)
				}
			}
		}
	}
}

// Two clusters send cAdvisor without a cluster label; kube-state-metrics,
// labeled, lists cluster b's Pods. The overview and namespace charts would
// sum both clusters, so they are withheld and query nothing. With only this
// cluster's Pods in the series they are drawn with the unfiltered queries,
// and a store with no cluster labels sends no foreign-check probe.
func TestSummedChartsWithTwoUnlabeledClusters(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-a"}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	cache := k8s.GetResourceCache()

	ksm := []map[string]string{
		ksmPod(clusterA, "kube-system", "coredns-a"), ksmPod(clusterA, "shop", "api-a"),
		ksmPod(clusterB, "kube-system", "coredns-b"), ksmPod(clusterB, "shop", "api-b"),
	}
	cadvisor := func(pods ...[2]string) []map[string]string {
		var out []map[string]string
		for _, p := range pods {
			out = append(out, unlabeledSeries(prom.MetricCPUUsage, p[0], p[1], "container", "app"))
		}
		return out
	}
	both := cadvisor([2]string{"kube-system", "coredns-a"}, [2]string{"shop", "api-a"}, [2]string{"kube-system", "coredns-b"}, [2]string{"shop", "api-b"})
	ours := cadvisor([2]string{"kube-system", "coredns-a"}, [2]string{"shop", "api-a"})

	charts := map[string]struct {
		run    func(context.Context, chartDeps) (*prom.QueryResult, *ClusterScope, error)
		golden string
	}{
		"cluster": {golden: prom.BuildClusterQuery(prom.CategoryCPU), run: func(ctx context.Context, d chartDeps) (*prom.QueryResult, *ClusterScope, error) {
			r, err := clusterChart(ctx, d, prom.CategoryCPU, "1h")
			return r.Result, r.ClusterScope, err
		}},
		"namespace": {golden: prom.BuildNamespaceQuery("shop", prom.CategoryCPU), run: func(ctx context.Context, d chartDeps) (*prom.QueryResult, *ClusterScope, error) {
			r, err := namespaceChart(ctx, d, "shop", prom.CategoryCPU, "1h")
			return r.Result, r.ClusterScope, err
		}},
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		series []map[string]string
		want   ViewScopeState
	}{
		{"both clusters' Pods", clusterA, both, ViewScopeLabelMissing},
		{"only this cluster's Pods", clusterA, ours, ViewScopeUnlabeled},
		{"no cluster labels", nil, ours, ViewScopeUnlabeled},
	} {
		for name, chart := range charts {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				probes := &evaluatingStore{t: t, series: append(slices.Clone(ksm), tc.series...)}
				resolve := func(ctx context.Context, req ViewScopeRequest) (ViewScope, error) {
					req, err := normalizeViewScopeRequest(req)
					if err != nil {
						return unknownViewScope(), err
					}
					return decideViewScope(ctx, probes, req, time.Now(), tc.labels, false)
				}
				store := &twoClusterStore{}
				result, scope, err := chart.run(context.Background(), chartDeps{q: store, resolve: resolve, cache: cache})
				if err != nil {
					t.Fatal(err)
				}
				if scope == nil || scope.State != tc.want {
					t.Fatalf("clusterScope = %+v", scope)
				}
				references, _ := countForeignProbes(probes.queries)
				switch {
				case tc.want == ViewScopeLabelMissing:
					if len(store.queries) != 0 || len(result.Series) != 0 || !strings.Contains(scope.Note, "another cluster") {
						t.Errorf("withheld chart: queries %v, result %+v, note %q", store.queries, result, scope.Note)
					}
				case tc.labels == nil:
					if references != 0 {
						t.Errorf("store without cluster labels sent %d foreign-check probes", references)
					}
					fallthrough
				default:
					if !slices.Equal(store.queries, []string{chart.golden}) {
						t.Errorf("queries = %q, want the unfiltered golden", store.queries)
					}
				}
				if tc.labels != nil && references != 1 {
					t.Errorf("reference probes = %d, want 1", references)
				}
			})
		}
	}
}
