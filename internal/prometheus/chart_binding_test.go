package prometheus

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// replaceChartResolver swaps the charts' check for one that runs during and
// then approves unfiltered queries, and counts its runs.
func replaceChartResolver(t *testing.T, during func()) *int {
	t.Helper()
	checks := 0
	prev := chartResolverFor
	chartResolverFor = func(*Client, *k8s.ResourceCache) ChartScopeResolver {
		return func(_ context.Context, req ViewScopeRequest) (ViewScope, error) {
			checks++
			during()
			filters := map[string]string{}
			for _, m := range req.Metrics {
				filters[m.Name] = ""
			}
			return ViewScope{State: ViewScopeSingleClusterAsserted, Filters: filters, Note: viewScopeSingleNote}, nil
		}
	}
	t.Cleanup(func() { chartResolverFor = prev })
	return &checks
}

// chartBindingCase runs one chart and reports whether it was withheld, with the
// unknown scope and no samples.
type chartBindingCase struct {
	name string
	run  func(context.Context, chartDeps) (withheldUnknown bool, err error)
}

func chartBindingCases() []chartBindingCase {
	unknown := func(s *ClusterScope) bool { return s != nil && s.State == ViewScopeUnknown }
	empty := func(r *prom.QueryResult) bool { return r != nil && len(r.Series) == 0 }
	return []chartBindingCase{
		{name: "pod", run: func(ctx context.Context, d chartDeps) (bool, error) {
			resp, err := resourceChart(ctx, d, "Pod", "shop", "api-1", prom.CategoryCPU, "1h", nil)
			return unknown(resp.ClusterScope) && empty(resp.Result), err
		}},
		{name: "namespace", run: func(ctx context.Context, d chartDeps) (bool, error) {
			resp, err := namespaceChart(ctx, d, "shop", prom.CategoryCPU, "1h")
			return unknown(resp.ClusterScope) && empty(resp.Result), err
		}},
		{name: "cluster", run: func(ctx context.Context, d chartDeps) (bool, error) {
			resp, err := clusterChart(ctx, d, prom.CategoryCPU, "1h")
			return unknown(resp.ClusterScope) && empty(resp.Result), err
		}},
		{name: "hpa", run: func(ctx context.Context, d chartDeps) (bool, error) {
			resp, err := hpaChart(ctx, d, "shop", "api", "1h")
			return unknown(resp.ClusterScope) && empty(resp.Current) && empty(resp.Desired), err
		}},
	}
}

// A chart's check and its queries must describe one backend: when the
// connection moves from store A to store B after the chart took its store
// handle, B's approval of unfiltered queries must not send them to A.
func TestChartCheckOnAMovedConnectionWithholds(t *testing.T) {
	for _, tc := range chartBindingCases() {
		t.Run(tc.name, func(t *testing.T) {
			initBindingTestCluster(t)
			storeA, queriesA := bindingTestStore(t)
			storeB, queriesB := bindingTestStore(t)
			client := connectBindingTestClient(t, storeA.URL)
			ctx := context.Background()
			d := productionChartDeps(ctx, client)
			checks := replaceChartResolver(t, func() {})
			connectBindingTestClient(t, storeB.URL)
			d.resolve = BoundResolver(client, d.q.(boundStore).conn, chartResolverFor(client, d.cache))

			withheld, err := tc.run(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			if !withheld {
				t.Error("want the chart withheld with the unknown scope and no samples")
			}
			if *checks != 0 {
				t.Errorf("checked %d times on a connection the store no longer belongs to", *checks)
			}
			if got := queriesA(); len(got) != 0 {
				t.Errorf("chart queries reached the old store: %q", got)
			}
			if got := queriesB(); len(got) != 0 {
				t.Errorf("chart queries reached the new store: %q", got)
			}
		})
	}
}

// The production deps bind the check: a connection moving during it
// withholds the chart, and an unmoved one queries the store it approved.
func TestProductionChartDepsBindTheCheck(t *testing.T) {
	for _, moved := range []bool{true, false} {
		for _, tc := range chartBindingCases() {
			name := tc.name + "/unmoved"
			if moved {
				name = tc.name + "/moved during the check"
			}
			t.Run(name, func(t *testing.T) {
				initBindingTestCluster(t)
				storeA, queriesA := bindingTestStore(t)
				storeB, queriesB := bindingTestStore(t)
				client := connectBindingTestClient(t, storeA.URL)
				checks := replaceChartResolver(t, func() {
					if moved {
						connectBindingTestClient(t, storeB.URL)
					}
				})
				ctx := context.Background()

				withheld, err := tc.run(ctx, productionChartDeps(ctx, client))
				if err != nil {
					t.Fatal(err)
				}
				if *checks != 1 {
					t.Fatalf("checks = %d, want 1", *checks)
				}
				if !moved {
					if withheld || len(queriesA()) == 0 {
						t.Fatalf("unmoved chart withheld=%v, queries=%q", withheld, queriesA())
					}
					return
				}
				if !withheld {
					t.Error("want the chart withheld with the unknown scope and no samples")
				}
				if got := queriesA(); len(got) != 0 {
					t.Errorf("chart queries reached the old store: %q", got)
				}
				if got := queriesB(); len(got) != 0 {
					t.Errorf("chart queries reached the new store: %q", got)
				}
			})
		}
	}
}
