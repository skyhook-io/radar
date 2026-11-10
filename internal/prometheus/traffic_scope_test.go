package prometheus

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestWithScopeEmptySelector(t *testing.T) {
	if got := withScope("", `cluster="a"`); got != `cluster="a"` {
		t.Errorf("withScope(\"\", m) = %q, want the matchers alone", got)
	}
	if got := withScope(`job="x"`, `cluster="a"`); got != `job="x",cluster="a"` {
		t.Errorf("got %q", got)
	}
}

// The traffic map's probe has no selector of its own for Caretta, so every
// query it builds must still be valid PromQL.
func TestTrafficScopeQueriesAreValidWithoutSelector(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "a"}, 1)}}, nil
	}}
	m, iso, err := decideScope(context.Background(), q, trafficScopeProbe("caretta_links_observed", ""), map[string]string{"cluster": "a"})
	if err != nil || m != `cluster="a"` || iso.Mode != SeriesIsolationVerified {
		t.Fatalf("m=%q iso=%+v err=%v", m, iso, err)
	}
	for _, query := range q.queries {
		if strings.Contains(query, "{,") {
			t.Errorf("invalid selector in %s", query)
		}
	}
}

// The empty-map warning names the cluster labels the traffic failed to match,
// so a mismatch has to keep them.
func TestTrafficScopeMismatchKeepsProvenLabels(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(query string) (*prom.QueryResult, error) {
		if strings.Contains(query, `cluster="a"`) {
			return &prom.QueryResult{}, nil
		}
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "b"}, 1)}}, nil
	}}
	_, iso, err := decideScope(context.Background(), q, trafficScopeProbe("caretta_links_observed", ""), map[string]string{"cluster": "a"})
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	if !maps.Equal(iso.Labels, map[string]string{"cluster": "a"}) {
		t.Errorf("mismatch dropped the proven labels: %+v", iso)
	}
}

func TestTrafficScopeWithoutProof(t *testing.T) {
	probe := trafficScopeProbe("beyla_network_flow_bytes_total", `job=~".*beyla.*"`)
	one := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(nil, 4)}}, nil
	}}
	m, iso, err := decideScope(context.Background(), one, probe, nil)
	if err != nil || m != "" || iso.Mode != SeriesIsolationUnverified {
		t.Fatalf("one unlabeled identity: m=%q iso=%+v err=%v", m, iso, err)
	}
	for _, query := range one.queries {
		if !strings.Contains(query, `job=~".*beyla.*"`) {
			t.Errorf("probe lost the source's selector: %s", query)
		}
	}
	two := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "a"}, 1), vec(map[string]string{"cluster": "b"}, 1)}}, nil
	}}
	if _, _, err := decideScope(context.Background(), two, probe, nil); !errors.Is(err, ErrScopeAmbiguous) {
		t.Fatalf("two identities: err = %v, want ambiguous", err)
	}
}

// The multi-cluster note counts cluster names only from the cluster identity
// labels, so a region or environment label never splits one cluster in two.
func TestTrafficSpansClusters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		series []prom.Series
		want   bool
	}{
		{"one cluster name", []prom.Series{vec(map[string]string{"cluster": "a"}, 9)}, false},
		{"no cluster label", []prom.Series{vec(nil, 9)}, false},
		{"two cluster names", []prom.Series{vec(map[string]string{"cluster": "a"}, 4), vec(map[string]string{"cluster": "b"}, 5)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
				return &prom.QueryResult{Series: tc.series}, nil
			}}
			got, err := trafficSpansClusters(context.Background(), q, "beyla_network_flow_bytes_total", `job="beyla"`)
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
			for _, query := range q.queries {
				if !strings.Contains(query, "count by ("+strings.Join(ClusterIdentityLabels(), ",")+")") || !strings.Contains(query, `job="beyla"`) {
					t.Errorf("query should group by the cluster identity labels only, under the selector: %s", query)
				}
			}
		})
	}
	failing := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) { return nil, errors.New("timeout") }}
	if _, err := trafficSpansClusters(context.Background(), failing, "m", ""); err == nil {
		t.Error("a failed query must surface as an error")
	}
}

func TestResolveTrafficScopeUsesOperatorLabels(t *testing.T) {
	c := &Client{workloadScope: &prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "east"}}}
	m, iso, err := c.ResolveTrafficScope(context.Background(), "caretta_links_observed", "", nil)
	if err != nil || m != `cluster="east"` || iso.Mode != SeriesIsolationConfigured {
		t.Fatalf("m=%q iso=%+v err=%v", m, iso, err)
	}
}

func TestTrafficAnchorNamespace(t *testing.T) {
	pod := func(ns, name string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	}
	podsOnly := map[string]bool{k8score.Pods: true}
	if got := trafficAnchorNamespace(nil); got != "kube-system" {
		t.Errorf("no cache: %q", got)
	}
	if got := trafficAnchorNamespace(scopeTestCache(t, podsOnly, pod("shop", "a"), pod("kube-system", "coredns"))); got != "kube-system" {
		t.Errorf("kube-system present: %q", got)
	}
	// A cache pinned to one namespace never sees kube-system's Pods.
	if got := trafficAnchorNamespace(scopeTestCache(t, podsOnly, pod("shop", "a"), pod("payments", "b"))); got != "payments" {
		t.Errorf("namespace-scoped cache: %q", got)
	}
}

// Two identities among the series the proven labels select are still this
// cluster's; the traffic map keeps filtering by those labels.
func TestTrafficScopeAmbiguityKeepsProvenLabels(t *testing.T) {
	q := &fakeCNPGQuerier{instant: func(string) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{vec(map[string]string{"cluster": "a"}, 1), vec(map[string]string{"cluster": "a", "cluster_name": "a"}, 1)}}, nil
	}}
	_, iso, err := decideScope(context.Background(), q, trafficScopeProbe("caretta_links_observed", ""), map[string]string{"cluster": "a"})
	if !errors.Is(err, ErrScopeAmbiguous) {
		t.Fatalf("err = %v, want ambiguous", err)
	}
	if !maps.Equal(iso.Labels, map[string]string{"cluster": "a"}) {
		t.Errorf("ambiguity dropped the proven labels: %+v", iso)
	}
}
