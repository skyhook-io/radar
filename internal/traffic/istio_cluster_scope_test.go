package traffic

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	promclient "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// istioStore answers Istio queries from per-cluster series. A query carrying
// cluster="x" sees only cluster x's; one without any cluster matcher sees all.
type istioStore struct {
	mu        sync.Mutex
	queries   []string
	http, tcp map[string]bool // clusters whose series exist ("" = unlabeled)
	failTCP   bool
}

func (s *istioStore) query(_ context.Context, q string) (*prom.QueryResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()
	series := func(clusters map[string]bool, prefix string) *prom.QueryResult {
		out := emptyResult()
		for cluster := range clusters {
			if strings.Contains(q, `cluster="`) && !strings.Contains(q, `cluster="`+cluster+`"`) {
				continue
			}
			l := map[string]string{
				"source_workload": prefix + "client-" + cluster, "source_workload_namespace": "shop",
				"destination_workload": prefix + "server-" + cluster, "destination_workload_namespace": "shop",
				"destination_service_name": prefix + "server-" + cluster, "request_protocol": "http",
			}
			if strings.HasPrefix(q, "count by (") {
				l = map[string]string{}
				if cluster != "" {
					l["cluster"] = cluster
				}
			}
			out.Series = append(out.Series, promSeries(l, 2))
		}
		return out
	}
	switch {
	case strings.Contains(q, istioTCPMetric):
		if s.failTCP {
			return nil, errors.New("query timed out")
		}
		return series(s.tcp, "tcp-"), nil
	case strings.Contains(q, "istio_requests_total") && !strings.Contains(q, `=~"5.."`):
		return series(s.http, "http-"), nil
	}
	return emptyResult(), nil
}

func (s *istioStore) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func scopedIstio(store *istioStore) *IstioSource { return newTestIstio(store.query) }

func TestIstioQueriesCarryClusterMatchers(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, clusterA)
	store := &istioStore{http: map[string]bool{"a": true, "b": true}, tcp: map[string]bool{"a": true, "b": true}}
	resp, err := scopedIstio(store).GetFlows(t.Context(), FlowOptions{Namespace: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	names := flowNames(resp.Flows)
	if !names["http-server-a"] || !names["tcp-server-a"] || names["http-server-b"] || names["tcp-server-b"] {
		t.Errorf("flows = %v, want only cluster a's", names)
	}
	for _, q := range store.sent() {
		if want := strings.Count(q, "{"); strings.Count(q, `cluster="a"`) != want {
			t.Errorf("every selector must carry the cluster matcher: %s", q)
		}
	}
}

// A store Radar discovered in the cluster holds this cluster's metrics.
func TestIstioNotFilteredWithoutConfiguredURL(t *testing.T) {
	calls := stubScope(t, clusterA)
	store := &istioStore{http: map[string]bool{"": true}}
	resp, err := scopedIstio(store).GetFlows(t.Context(), FlowOptions{})
	if err != nil || len(resp.Flows) == 0 {
		t.Fatalf("GetFlows: %v %+v", err, resp)
	}
	if *calls != 0 {
		t.Error("cluster scope resolved without a configured metrics URL")
	}
	for _, q := range store.sent() {
		if strings.Contains(q, "cluster") {
			t.Errorf("query filtered without a configured URL: %s", q)
		}
	}
}

// TCP series scraped without the cluster label must not empty the HTTP flows,
// and the map says why the TCP connections are missing.
func TestIstioFamiliesScopedSeparately(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, clusterA)
	store := &istioStore{http: map[string]bool{"a": true, "b": true}, tcp: map[string]bool{"": true}}
	resp, err := scopedIstio(store).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := flowNames(resp.Flows); !names["http-server-a"] || names["http-server-b"] || names["tcp-server-"] {
		t.Errorf("flows = %v, want cluster a's HTTP flows and no unattributed TCP", names)
	}
	if !strings.Contains(resp.Warning, "TCP connections aren't shown") || !strings.Contains(resp.Warning, "none of it has a cluster label") {
		t.Errorf("warning %q does not explain the missing TCP connections", resp.Warning)
	}
	// Whole edges are missing, so the warning must outlast a viewer's filters.
	if resp.WarningKind != WarningIncomplete {
		t.Errorf("warning kind = %s, want %s", resp.WarningKind, WarningIncomplete)
	}
}

func TestIstioScopeFollowsCurrentCluster(t *testing.T) {
	useConfiguredStore(t)
	current := "a"
	stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
		return `cluster="` + current + `"`, promclient.SeriesIsolation{Mode: promclient.SeriesIsolationVerified, Labels: map[string]string{"cluster": current}}, nil
	})
	src := scopedIstio(&istioStore{http: map[string]bool{"a": true, "b": true}})
	for _, cluster := range []string{"a", "b", "a"} {
		current = cluster
		resp, err := src.GetFlows(t.Context(), FlowOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if names := flowNames(resp.Flows); len(names) != 2 || !names["http-server-"+cluster] {
			t.Errorf("on cluster %s: flows = %v", cluster, names)
		}
	}
}

func TestIstioEmptyMapClusterWarnings(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, clusterA)
	for _, tc := range []struct {
		name      string
		http, tcp map[string]bool
		want      string
	}{
		{"labeled, not this cluster", map[string]bool{"b": true}, nil, "none of it is labeled cluster=a"},
		{"no cluster label", map[string]bool{"": true}, nil, "none of it has a cluster label"},
		{"only TCP, not this cluster", nil, map[string]bool{"b": true}, "none of it is labeled cluster=a"},
		{"store has no traffic", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := scopedIstio(&istioStore{http: tc.http, tcp: tc.tcp}).GetFlows(t.Context(), FlowOptions{})
			if err != nil || len(resp.Flows) != 0 {
				t.Fatalf("GetFlows: %v %+v", err, resp)
			}
			if tc.want == "" {
				if resp.Warning != "" {
					t.Errorf("warning on a store with no traffic: %q", resp.Warning)
				}
				return
			}
			if resp.WarningKind != WarningCluster || !strings.Contains(resp.Warning, tc.want) {
				t.Errorf("warning = %q (%s), want %q", resp.Warning, resp.WarningKind, tc.want)
			}
		})
	}
}

// The traffic carries two cluster names and nothing proves which is this one:
// the flows draw with the note. A TCP read that failed stays retried under it.
func TestIstioMixedClustersNote(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
		return "", promclient.SeriesIsolation{}, promclient.ErrScopeAmbiguous
	})
	resp, err := scopedIstio(&istioStore{http: map[string]bool{"a": true, "b": true}}).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := flowNames(resp.Flows); !names["http-server-a"] || !names["http-server-b"] {
		t.Errorf("flows = %v, want every cluster's", names)
	}
	if resp.WarningKind != WarningCluster || !strings.Contains(resp.Warning, mixedClustersNote) {
		t.Errorf("warning = %q (%s), want the mixed-clusters note", resp.Warning, resp.WarningKind)
	}

	failed, err := scopedIstio(&istioStore{http: map[string]bool{"a": true}, failTCP: true}).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if failed.WarningKind != WarningTransient || !failed.WarningClusterScope || !strings.Contains(failed.Warning, mixedClustersNote) {
		t.Errorf("warning = %q (%s), want the note on a retried response", failed.Warning, failed.WarningKind)
	}
}

func TestIstioScopeCheckFailure(t *testing.T) {
	useConfiguredStore(t)
	t.Run("HTTP", func(t *testing.T) {
		stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
			return "", promclient.SeriesIsolation{}, errors.New("query timed out")
		})
		store := &istioStore{http: map[string]bool{"a": true, "b": true}}
		resp, err := scopedIstio(store).GetFlows(t.Context(), FlowOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Flows) != 0 || resp.WarningKind != WarningTransient || len(store.sent()) != 0 {
			t.Errorf("flows %d, kind %s, %d queries: want nothing shown or queried unchecked", len(resp.Flows), resp.WarningKind, len(store.sent()))
		}
	})
	t.Run("TCP", func(t *testing.T) {
		stubScope(t, func(metric string) (string, promclient.SeriesIsolation, error) {
			if metric == istioTCPMetric {
				return "", promclient.SeriesIsolation{}, errors.New("query timed out")
			}
			return clusterA(metric)
		})
		store := &istioStore{http: map[string]bool{"a": true}, tcp: map[string]bool{"a": true, "b": true}}
		resp, err := scopedIstio(store).GetFlows(t.Context(), FlowOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if names := flowNames(resp.Flows); !names["http-server-a"] || names["tcp-server-a"] {
			t.Errorf("flows = %v, want HTTP flows without TCP", names)
		}
		if !strings.Contains(resp.Warning, "TCP connections") {
			t.Errorf("warning %q does not name the missing TCP connections", resp.Warning)
		}
		for _, q := range store.sent() {
			if strings.Contains(q, istioTCPMetric) {
				t.Errorf("queried TCP series without a cluster scope: %s", q)
			}
		}
	})
}

// The TCP family gets its own proof: HTTP's labels say nothing about how the
// TCP series are labeled.
func TestIstioTCPCheckedOnItsOwn(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(metric string) (string, promclient.SeriesIsolation, error) {
		if metric == istioTCPMetric {
			return `cluster="b"`, promclient.SeriesIsolation{Mode: promclient.SeriesIsolationVerified, Labels: map[string]string{"cluster": "b"}}, nil
		}
		return clusterA(metric)
	})
	resp, err := scopedIstio(&istioStore{http: map[string]bool{"a": true}, tcp: map[string]bool{"a": true, "b": true}}).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := flowNames(resp.Flows); !names["http-server-a"] || !names["tcp-server-b"] || names["tcp-server-a"] {
		t.Errorf("flows = %v, want HTTP by the HTTP proof and TCP by the TCP proof", names)
	}
}

// The note about missing TCP edges keeps its kind under the mixed-clusters
// note, or a viewer's filters would drop both.
func TestIstioMissingFamilyNoteSurvivesMixedNote(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(metric string) (string, promclient.SeriesIsolation, error) {
		if metric == istioTCPMetric {
			return clusterA(metric)
		}
		return "", promclient.SeriesIsolation{}, promclient.ErrScopeAmbiguous
	})
	resp, err := scopedIstio(&istioStore{http: map[string]bool{"a": true, "b": true}, tcp: map[string]bool{"": true}}).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Warning, mixedClustersNote) || !strings.Contains(resp.Warning, "TCP connections aren't shown") {
		t.Fatalf("warning %q, want both notes", resp.Warning)
	}
	if resp.WarningKind != WarningIncomplete {
		t.Errorf("warning kind = %s, want %s", resp.WarningKind, WarningIncomplete)
	}
}

// A failed TCP query must not hide why the HTTP traffic is missing: the map is
// empty for both reasons, and the retry stays.
func TestIstioMismatchSurvivesFailedTCPQuery(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, clusterA)
	resp, err := scopedIstio(&istioStore{http: map[string]bool{"b": true}, failTCP: true}).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Flows) != 0 || resp.WarningKind != WarningTransient || !resp.WarningClusterScope {
		t.Fatalf("flows = %d, kind = %s, cluster scope = %v", len(resp.Flows), resp.WarningKind, resp.WarningClusterScope)
	}
	if !strings.Contains(resp.Warning, "TCP connections") || !strings.Contains(resp.Warning, "cluster=a") {
		t.Errorf("warning %q should name both the failed TCP query and the cluster mismatch", resp.Warning)
	}
}
