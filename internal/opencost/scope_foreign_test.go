package opencost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

var scopeProbeTag = regexp.MustCompile(`"(radar_[a-z_]+)", "([^"]+)"`)

// unlabeledCostStore is a metrics store whose OpenCost and cAdvisor metrics
// carry no cluster label, as when two clusters leave it off one scrape job,
// next to kube-state-metrics that does. Every metric shows one identity
// holding this cluster's objects; foreign names the metrics whose series
// also hold objects kube-state-metrics lists only under another cluster.
// foreignPodsIn, when set, is the one namespace holding another cluster's
// Pods among every Pod-keyed metric: a check whose selector keeps to another
// namespace, under either spelling, doesn't find them.
type unlabeledCostStore struct {
	foreign       map[string]bool
	foreignPodsIn string
	mu            sync.Mutex
	probes        []string
	costs         []string
}

func (s *unlabeledCostStore) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.FormValue("query")
		if q == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		tags := scopeProbeTag.FindAllStringSubmatchIndex(q, -1)
		var rows []string
		row := func(label, value string) {
			rows = append(rows, fmt.Sprintf(`{"metric":{%q:%q},"value":[1700000000,"1"]}`, label, value))
		}
		s.mu.Lock()
		switch {
		case len(tags) > 0:
			s.probes = append(s.probes, q)
			partStart := 0
			for _, idx := range tags {
				tag, metric, part := q[idx[2]:idx[3]], q[idx[4]:idx[5]], q[partStart:idx[0]]
				partStart = idx[1]
				switch tag {
				case "radar_metric":
					// A readback finds no series with this cluster's
					// label; an identity check finds one, with none.
					if !strings.Contains(part, `cluster="a"`) {
						row(tag, metric)
					}
				case "radar_foreign":
					if s.foreign[metric] || s.foreignPodsSeen(part, metric) {
						row(tag, metric)
					}
				default:
					// Anchors are found, and kube-state-metrics holds this
					// cluster's objects and another cluster's.
					row(tag, metric)
				}
			}
		case strings.Contains(q, "uid"):
			// The Pod-UID proof finds no cluster label.
		default:
			s.costs = append(s.costs, q)
			rows = append(rows, `{"metric":{"namespace":"shop","pod":"api-1","node":"n1"},"value":[1700000000,"1"]}`)
		}
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

var namespaceMatcher = regexp.MustCompile(`(?:^|,)(?:exported_namespace|namespace)="([^"]*)"`)

// foreignPodsSeen reports whether one foreign-check part, checking metric
// against kube_pod_info, selects the namespace holding another cluster's
// Pods: its metric selector keeps to no namespace, or to that one.
func (s *unlabeledCostStore) foreignPodsSeen(part, metric string) bool {
	if s.foreignPodsIn == "" || !strings.Contains(part, "kube_pod_info{") {
		return false
	}
	sel := regexp.MustCompile(regexp.QuoteMeta(metric) + `\{([^}]*)\}`).FindStringSubmatch(part)
	if sel == nil {
		return false
	}
	for _, m := range namespaceMatcher.FindAllStringSubmatch(sel[1], -1) {
		if m[1] != "" && m[1] != s.foreignPodsIn {
			return false
		}
	}
	return true
}

func (s *unlabeledCostStore) foreignProbes() (reference, foreign int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.probes {
		switch {
		case strings.Contains(q, `"radar_foreign"`):
			foreign++
		case strings.Contains(q, `"radar_reference"`):
			reference++
		}
	}
	return reference, foreign
}

// The cost summary from a store where two clusters' OpenCost metrics carry
// no cluster label: once kube-state-metrics shows another cluster's Pods
// among the allocations, the totals would add both clusters and are
// withheld. With only this cluster's objects they show, read as before; a
// store with no cluster labels runs no foreign check.
func TestCostSummaryWithTwoUnlabeledClusters(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns-a", UID: "aaaaaaaa-0000-4000-8000-000000000001"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"}},
	}
	if err := k8s.InitTestResourceCache(fakeclientset.NewSimpleClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(func() {
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	for _, tc := range []struct {
		name       string
		configured bool
		foreign    map[string]bool
		want       prometheuspkg.ViewScopeState
	}{
		{"another cluster's Pods among the allocations", true, map[string]bool{pkgopencost.MetricCPUAllocation: true}, prometheuspkg.ViewScopeLabelMissing},
		{"another cluster's Node among the prices", true, map[string]bool{pkgopencost.MetricNodeCPUHourlyCost: true}, prometheuspkg.ViewScopeLabelMissing},
		{"only this cluster's objects", true, nil, prometheuspkg.ViewScopeUnlabeled},
		{"no cluster labels", false, nil, prometheuspkg.ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &unlabeledCostStore{foreign: tc.foreign}
			prometheuspkg.Reset()
			prometheuspkg.Initialize(nil, nil, "test")
			prometheuspkg.SetManualURL(store.serve(t).URL)
			if tc.configured {
				if err := prometheuspkg.SetWorkloadMetricsScope(prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "a"}}, ""); err != nil {
					t.Fatal(err)
				}
			}
			client, err := prometheuspkg.ClientForOperation()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.EnsureConnected(context.Background()); err != nil {
				t.Fatal(err)
			}
			resp := NewPromCost(client).Summary(context.Background(), pkgopencost.SummaryOptions{})
			if resp.ClusterScope == nil || resp.ClusterScope.State != string(tc.want) {
				t.Fatalf("summary = %+v, clusterScope = %+v", resp, resp.ClusterScope)
			}
			references, foreign := store.foreignProbes()
			if tc.want == prometheuspkg.ViewScopeLabelMissing {
				if resp.Available || resp.Reason != pkgopencost.ReasonClusterScope || !strings.Contains(resp.ClusterScope.Note, "another cluster") {
					t.Errorf("withheld summary = %+v", resp)
				}
				for name := range tc.foreign {
					if !strings.Contains(resp.ClusterScope.Note, name) {
						t.Errorf("note %q doesn't name %s", resp.ClusterScope.Note, name)
					}
				}
				if len(store.costs) != 0 {
					t.Errorf("withheld summary still queried: %q", store.costs)
				}
				if references != 1 || foreign != 1 {
					t.Errorf("probes: %d reference, %d foreign", references, foreign)
				}
				return
			}
			if !resp.Available {
				t.Fatalf("summary = %+v", resp)
			}
			unscoped := &twoClusterCostStore{}
			pkgopencost.ComputeCostSummaryFromProm(context.Background(), unscoped.client(t), pkgopencost.SummaryOptions{})
			if want := unscoped.take(); !slices.Equal(store.costs, want) {
				t.Errorf("cost queries differ from the unscoped ones\n got: %q\nwant: %q", store.costs, want)
			}
			// The cost and the cost-plus-usage checks each run the foreign
			// check once a metric is accepted unlabeled.
			if tc.configured && (references != 2 || foreign != 2) {
				t.Errorf("probes: %d reference, %d foreign", references, foreign)
			}
			if !tc.configured && references+foreign != 0 {
				t.Errorf("store without cluster labels sent %d foreign-check probes", references+foreign)
			}
		})
	}
}

// A namespace's costs check its allocations for other clusters' Pods in that
// namespace only, under either spelling of it: another cluster's Pods in
// other namespaces never reach its sums. The cluster-wide summary still
// checks the whole store.
func TestNamespaceCostsCheckOnlyTheirNamespace(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns-a", UID: "aaaaaaaa-0000-4000-8000-000000000001"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-1", UID: "aaaaaaaa-0000-4000-8000-000000000002"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"}},
	}
	if err := k8s.InitTestResourceCache(fakeclientset.NewSimpleClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(func() {
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	workloads := func(p PromCost) (bool, string, *pkgopencost.ClusterScope) {
		r := p.Workloads(context.Background(), "shop", nil)
		return r.Available, r.Reason, r.ClusterScope
	}
	summary := func(p PromCost) (bool, string, *pkgopencost.ClusterScope) {
		r := p.Summary(context.Background(), pkgopencost.SummaryOptions{})
		return r.Available, r.Reason, r.ClusterScope
	}
	for _, tc := range []struct {
		name      string
		run       func(PromCost) (bool, string, *pkgopencost.ClusterScope)
		foreignIn string
		want      prometheuspkg.ViewScopeState
	}{
		{"namespace view, another cluster's Pods in another namespace", workloads, "pay", prometheuspkg.ViewScopeUnlabeled},
		{"namespace view, another cluster's Pods in the namespace", workloads, "shop", prometheuspkg.ViewScopeLabelMissing},
		{"cluster view, another cluster's Pods in any namespace", summary, "pay", prometheuspkg.ViewScopeLabelMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &unlabeledCostStore{foreignPodsIn: tc.foreignIn}
			prometheuspkg.Reset()
			prometheuspkg.Initialize(nil, nil, "test")
			prometheuspkg.SetManualURL(store.serve(t).URL)
			if err := prometheuspkg.SetWorkloadMetricsScope(prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "a"}}, ""); err != nil {
				t.Fatal(err)
			}
			client, err := prometheuspkg.ClientForOperation()
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.EnsureConnected(context.Background()); err != nil {
				t.Fatal(err)
			}
			available, reason, scope := tc.run(NewPromCost(client))
			if scope == nil || scope.State != string(tc.want) {
				t.Fatalf("available=%v reason=%q clusterScope=%+v", available, reason, scope)
			}
			if withheld := tc.want == prometheuspkg.ViewScopeLabelMissing; available == withheld || (reason == pkgopencost.ReasonClusterScope) != withheld {
				t.Fatalf("available=%v reason=%q clusterScope=%+v", available, reason, scope)
			}
			if tc.want == prometheuspkg.ViewScopeLabelMissing && !strings.Contains(scope.Note, pkgopencost.MetricCPUAllocation) {
				t.Errorf("note %q doesn't name the allocations", scope.Note)
			}
		})
	}
}
