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
type unlabeledCostStore struct {
	foreign map[string]bool
	mu      sync.Mutex
	probes  []string
	costs   []string
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
		tags := scopeProbeTag.FindAllStringSubmatch(q, -1)
		var rows []string
		row := func(label, value string) {
			rows = append(rows, fmt.Sprintf(`{"metric":{%q:%q},"value":[1700000000,"1"]}`, label, value))
		}
		s.mu.Lock()
		switch {
		case len(tags) > 0:
			s.probes = append(s.probes, q)
			for _, tag := range tags {
				switch tag[1] {
				case "radar_metric":
					// A readback finds no series with this cluster's
					// label; an identity check finds one, with none.
					if strings.Contains(q, "topk(2") {
						row(tag[1], tag[2])
					}
				case "radar_foreign":
					if s.foreign[tag[2]] {
						row(tag[1], tag[2])
					}
				default:
					// Anchors are found, and kube-state-metrics holds this
					// cluster's objects and another cluster's.
					row(tag[1], tag[2])
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
