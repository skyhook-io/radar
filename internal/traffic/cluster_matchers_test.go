package traffic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	promclient "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// stubScope stands in for the shared cluster proof and counts its calls.
func stubScope(t *testing.T, fn func(metric string) (string, promclient.SeriesIsolation, error)) *int {
	t.Helper()
	calls := 0
	var mu sync.Mutex
	prev := resolveTrafficScope
	resolveTrafficScope = func(_ context.Context, metric, _ string) (string, promclient.SeriesIsolation, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return fn(metric)
	}
	t.Cleanup(func() { resolveTrafficScope = prev })
	return &calls
}

// stubSpans stands in for the count of cluster names in the traffic.
func stubSpans(t *testing.T, spans bool, err error) {
	t.Helper()
	prev := trafficSpansClusters
	trafficSpansClusters = func(context.Context, string, string) (bool, error) { return spans, err }
	t.Cleanup(func() { trafficSpansClusters = prev })
}

func clusterA(string) (string, promclient.SeriesIsolation, error) {
	return `cluster="a"`, promclient.SeriesIsolation{Mode: promclient.SeriesIsolationVerified, Labels: map[string]string{"cluster": "a"}}, nil
}

func useConfiguredStore(t *testing.T) {
	t.Helper()
	SetMetricsConfig("http://metrics.example", nil)
	t.Cleanup(func() { SetMetricsConfig("", nil) })
}

// recordingProm answers every query with one Caretta-shaped sample and keeps the
// queries it was sent.
func recordingProm(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	return carettaProm(t, func(string) []map[string]string {
		return []map[string]string{{"client_name": "frontend", "server_name": "backend", "server_port": "80"}}
	})
}

// carettaProm answers each query with the rows answer returns for it.
func carettaProm(t *testing.T, answer func(query string) []map[string]string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		result := []any{}
		for _, row := range answer(q) {
			result = append(result, map[string]any{"metric": row, "value": []any{1.0, "3"}})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": result},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

func connectedCaretta(addr, metricsURL string) *CarettaSource {
	return &CarettaSource{
		httpClient:      &http.Client{Timeout: 2 * time.Second},
		prometheusAddr:  addr,
		metricsURL:      metricsURL,
		isConnected:     true,
		backendVerified: true,
	}
}

// A store the operator configured may hold several clusters; each Caretta query
// has to keep to this cluster's series or the graph merges clusters.
func TestCarettaQueriesCarryClusterMatchers(t *testing.T) {
	stubScope(t, clusterA)
	srv, sent := recordingProm(t)
	c := connectedCaretta(srv.URL, srv.URL)
	for _, opts := range []FlowOptions{{}, {Namespace: "shop"}} {
		resp, err := c.GetFlows(t.Context(), opts)
		if err != nil || len(resp.Flows) == 0 {
			t.Fatalf("GetFlows(%+v): %v %+v", opts, err, resp)
		}
	}
	queries := sent()
	if len(queries) != 2 {
		t.Fatalf("expected one flow query per GetFlows call, got %q", queries)
	}
	for _, q := range queries {
		if want := strings.Count(q, "{"); strings.Count(q, `cluster="a"`) != want {
			t.Errorf("every selector must carry the cluster matcher: %s", q)
		}
	}
}

// Without a configured URL Caretta reads its own caretta-vm, which holds only
// this cluster and carries no cluster label: filtering it would empty the map.
func TestCarettaOwnStoreIsNotFiltered(t *testing.T) {
	useConfiguredStore(t)
	calls := stubScope(t, clusterA)
	srv, sent := recordingProm(t)
	c := connectedCaretta(srv.URL, "")
	c.boundIsCarettaStore = true
	resp, err := c.GetFlows(t.Context(), FlowOptions{})
	if err != nil || len(resp.Flows) != 1 {
		t.Fatalf("GetFlows: %v %+v", err, resp)
	}
	if *calls != 0 {
		t.Errorf("cluster scope resolved for Caretta's own store")
	}
	if got := sent(); len(got) != 1 || got[0] != "caretta_links_observed" {
		t.Errorf("queries = %q, want the unfiltered query", got)
	}
}

// The "is the data there" check decides whether a store holds Caretta at all. A
// cluster label that matches nothing must not turn into "holds no Caretta
// metrics": the empty-map warning explains that case.
func TestCarettaPresenceCheckIsUnfiltered(t *testing.T) {
	stubScope(t, clusterA)
	srv, sent := recordingProm(t)
	c := connectedCaretta(srv.URL, srv.URL)
	c.mu.Lock()
	present := c.carettaMetricsPresentLocked(t.Context(), srv.URL)
	c.mu.Unlock()
	if !present {
		t.Fatal("store with Caretta series reported as without")
	}
	for _, q := range sent() {
		if strings.Contains(q, "cluster") {
			t.Errorf("presence check carries cluster matchers: %s", q)
		}
	}
}

// beylaStore answers Beyla queries from per-cluster series. A query carrying
// cluster="a" sees only cluster a's; one without any cluster matcher sees all.
type beylaStore struct {
	mu      sync.Mutex
	queries []string
	l4, l7  map[string]bool // clusters whose L4 / HTTP series exist ("" = unlabeled)
}

func (b *beylaStore) query(_ context.Context, q string) (*prom.QueryResult, error) {
	b.mu.Lock()
	b.queries = append(b.queries, q)
	b.mu.Unlock()
	series := func(clusters map[string]bool, labels func(cluster string) map[string]string) *prom.QueryResult {
		out := emptyResult()
		for cluster := range clusters {
			if !strings.Contains(q, `cluster="`) || strings.Contains(q, `cluster="`+cluster+`"`) {
				l := labels(cluster)
				if cluster != "" && strings.Contains(q, "count by (") {
					l = map[string]string{"cluster": cluster}
				} else if strings.Contains(q, "count by (") {
					l = map[string]string{}
				}
				out.Series = append(out.Series, promSeries(l, 2))
			}
		}
		return out
	}
	switch {
	case strings.Contains(q, `direction="unknown"`), strings.Contains(q, `direction="response"`), strings.HasPrefix(q, "1 - "):
		return emptyResult(), nil
	case strings.Contains(q, beylaFlowMetric):
		return series(b.l4, func(c string) map[string]string {
			return map[string]string{
				"k8s_src_owner_name": "frontend-" + c, "k8s_src_namespace": "web", "k8s_src_owner_type": "Deployment",
				"k8s_dst_owner_name": "backend-" + c, "k8s_dst_namespace": "api", "k8s_dst_owner_type": "Deployment",
				"dst_port": "8080", "transport": "TCP",
			}
		}), nil
	case strings.Contains(q, beylaL7Metric) && !strings.Contains(q, "_sum") && !strings.Contains(q, `=~"5.."`):
		return series(b.l7, func(c string) map[string]string {
			return map[string]string{"k8s_namespace_name": "api", "k8s_owner_name": "http-" + c, "server_port": "8080",
				"http_request_method": "GET", "http_route": "/", "http_response_status_code": "200"}
		}), nil
	}
	return emptyResult(), nil
}

func (b *beylaStore) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.queries...)
}

func newScopedBeyla(store *beylaStore) *BeylaSource {
	src := &BeylaSource{k8sClient: fake.NewSimpleClientset()}
	src.queryFn = store.query
	return src
}

func flowNames(flows []Flow) map[string]bool {
	out := map[string]bool{}
	for _, f := range flows {
		out[f.Source.Name] = true
		out[f.Destination.Name] = true
	}
	return out
}

func TestBeylaQueriesCarryClusterMatchers(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, clusterA)
	store := &beylaStore{l4: map[string]bool{"a": true, "b": true}, l7: map[string]bool{"a": true, "b": true}}
	resp, err := newScopedBeyla(store).GetFlows(t.Context(), FlowOptions{Namespace: "api"})
	if err != nil {
		t.Fatal(err)
	}
	names := flowNames(resp.Flows)
	if !names["backend-a"] || names["backend-b"] || names["http-b"] {
		t.Errorf("flows = %v, want only cluster a's", names)
	}
	for _, q := range store.sent() {
		if want := strings.Count(q, "{"); strings.Count(q, `cluster="a"`) != want {
			t.Errorf("every selector must carry the cluster matcher: %s", q)
		}
	}
}

// A store Radar discovered in the cluster holds this cluster's metrics.
func TestBeylaNotFilteredWithoutConfiguredURL(t *testing.T) {
	calls := stubScope(t, clusterA)
	store := &beylaStore{l4: map[string]bool{"": true}}
	resp, err := newScopedBeyla(store).GetFlows(t.Context(), FlowOptions{})
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

// Each family is checked on its own: HTTP series scraped without the cluster
// label must not empty the network flows.
func TestBeylaFamiliesScopedSeparately(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(metric string) (string, promclient.SeriesIsolation, error) {
		if metric == beylaL7Metric {
			return "", promclient.SeriesIsolation{Labels: map[string]string{"cluster": "a"}}, promclient.ErrScopeMismatch
		}
		return clusterA(metric)
	})
	store := &beylaStore{l4: map[string]bool{"a": true, "b": true}, l7: map[string]bool{"": true}}
	resp, err := newScopedBeyla(store).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := flowNames(resp.Flows); !names["backend-a"] || names["backend-b"] || names["http-"] {
		t.Errorf("flows = %v, want cluster a's network flows and no unattributed HTTP", names)
	}
	if !strings.Contains(resp.Warning, "HTTP request rates, errors and latency are missing") || !strings.Contains(resp.Warning, "none of it has a cluster label") {
		t.Errorf("warning %q does not explain the missing HTTP figures", resp.Warning)
	}
}

// A context switch re-proves the cluster: the next request must use the new
// cluster's labels, not ones remembered from the last.
func TestBeylaScopeFollowsCurrentCluster(t *testing.T) {
	useConfiguredStore(t)
	current := "a"
	stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
		return `cluster="` + current + `"`, promclient.SeriesIsolation{Mode: promclient.SeriesIsolationVerified, Labels: map[string]string{"cluster": current}}, nil
	})
	store := &beylaStore{l4: map[string]bool{"a": true, "b": true}}
	src := newScopedBeyla(store)
	for _, cluster := range []string{"a", "b", "a"} {
		current = cluster
		resp, err := src.GetFlows(t.Context(), FlowOptions{})
		if err != nil {
			t.Fatal(err)
		}
		names := flowNames(resp.Flows)
		if len(names) != 2 || !names["backend-"+cluster] {
			t.Errorf("on cluster %s: flows = %v", cluster, names)
		}
	}
}

func TestEmptyMapClusterWarnings(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, clusterA)
	for _, tc := range []struct {
		name     string
		clusters map[string]bool
		want     string
	}{
		{"labeled, not this cluster", map[string]bool{"b": true}, "none of it is labeled cluster=a"},
		{"no cluster label", map[string]bool{"": true}, "none of it has a cluster label"},
		{"store has no traffic", map[string]bool{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := newScopedBeyla(&beylaStore{l4: tc.clusters}).GetFlows(t.Context(), FlowOptions{})
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

func TestCarettaEmptyMapClusterWarning(t *testing.T) {
	stubScope(t, clusterA)
	srv, _ := carettaProm(t, func(q string) []map[string]string {
		if strings.Contains(q, `cluster="a"`) {
			return nil
		}
		if strings.HasPrefix(q, "count by (") {
			return []map[string]string{{"cluster": "b"}}
		}
		return []map[string]string{{"client_name": "x", "server_name": "y", "cluster": "b"}}
	})
	resp, err := connectedCaretta(srv.URL, srv.URL).GetFlows(t.Context(), FlowOptions{})
	if err != nil || len(resp.Flows) != 0 {
		t.Fatalf("GetFlows: %v %+v", err, resp)
	}
	if resp.WarningKind != WarningCluster || !strings.Contains(resp.Warning, "none of it is labeled cluster=a") {
		t.Errorf("warning = %q (%s)", resp.Warning, resp.WarningKind)
	}
}

// When Radar can't work out the cluster, the map shows what the store has. It
// says the map may mix clusters only when the traffic carries two or more
// cluster names.
func TestMixedClustersNote(t *testing.T) {
	useConfiguredStore(t)
	unverified := promclient.SeriesIsolation{Mode: promclient.SeriesIsolationUnverified}
	mismatch := func(string) (string, promclient.SeriesIsolation, error) {
		return "", unverified, promclient.ErrScopeMismatch
	}
	for _, tc := range []struct {
		name     string
		resolve  func(string) (string, promclient.SeriesIsolation, error)
		spans    bool
		spansErr error
		clusters map[string]bool
		note     bool
	}{
		{name: "two cluster names", resolve: func(string) (string, promclient.SeriesIsolation, error) {
			return "", promclient.SeriesIsolation{}, promclient.ErrScopeAmbiguous
		}, clusters: map[string]bool{"a": true, "b": true}, note: true},
		// The check already found at most one cluster name in the traffic.
		{name: "nothing proves the cluster, one cluster name", resolve: func(string) (string, promclient.SeriesIsolation, error) { return "", unverified, nil },
			clusters: map[string]bool{"b": true}, note: false},
		{name: "kube-state-metrics unlabeled, traffic under one cluster name", resolve: mismatch,
			clusters: map[string]bool{"a": true}, note: false},
		{name: "kube-state-metrics unlabeled, traffic under two cluster names", resolve: mismatch, spans: true,
			clusters: map[string]bool{"a": true, "b": true}, note: true},
		{name: "counting the cluster names failed", resolve: mismatch, spansErr: errors.New("timeout"),
			clusters: map[string]bool{"a": true}, note: true},
		// A store holding this cluster without cluster labels, like a dedicated
		// Prometheus: the map reads the unlabeled series, as it always has.
		{name: "cluster unlabeled everywhere", resolve: func(string) (string, promclient.SeriesIsolation, error) {
			return `cluster=""`, unverified, nil
		}, clusters: map[string]bool{"": true}, note: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubScope(t, tc.resolve)
			stubSpans(t, tc.spans, tc.spansErr)
			resp, err := newScopedBeyla(&beylaStore{l4: tc.clusters}).GetFlows(t.Context(), FlowOptions{})
			if err != nil || len(resp.Flows) != len(tc.clusters) {
				t.Fatalf("GetFlows: %v, %d flows", err, len(resp.Flows))
			}
			got := resp.WarningKind == WarningCluster && strings.Contains(resp.Warning, mixedClustersNote)
			if got != tc.note {
				t.Errorf("note = %v, want %v (warning %q)", got, tc.note, resp.Warning)
			}
		})
	}
}

// Caretta on a configured store whose traffic carries two cluster names draws
// it with the note.
func TestCarettaMixedClustersNote(t *testing.T) {
	stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
		return "", promclient.SeriesIsolation{}, promclient.ErrScopeAmbiguous
	})
	srv, _ := carettaProm(t, func(string) []map[string]string {
		return []map[string]string{{"client_name": "x", "server_name": "y", "cluster": "a"}}
	})
	resp, err := connectedCaretta(srv.URL, srv.URL).GetFlows(t.Context(), FlowOptions{})
	if err != nil || len(resp.Flows) == 0 {
		t.Fatalf("GetFlows: %v %+v", err, resp)
	}
	if resp.WarningKind != WarningCluster || !resp.WarningClusterScope || !strings.HasPrefix(resp.Warning, mixedClustersNote) {
		t.Errorf("warning = %q (%s), want the mixed-clusters note", resp.Warning, resp.WarningKind)
	}
}

// An empty map has nothing that could be another cluster's, so it never gets
// the note.
func TestNoMixedClustersNoteOnEmptyMap(t *testing.T) {
	resp := &FlowsResponse{Flows: []Flow{}}
	applyClusterScopeWarning(resp, "", true)
	if resp.Warning != "" || resp.WarningKind != "" {
		t.Errorf("got %q (%s), want no warning", resp.Warning, resp.WarningKind)
	}
}

// The note outranks a warning about the flows' details, and keeps it.
func TestMixedClustersNoteJoinsPartialWarning(t *testing.T) {
	resp := &FlowsResponse{Flows: []Flow{{}}, Warning: "Beyla is not exporting dst.port.", WarningKind: WarningPartial}
	applyClusterScopeWarning(resp, "", true)
	if resp.WarningKind != WarningCluster || !strings.HasPrefix(resp.Warning, mixedClustersNote) || !strings.Contains(resp.Warning, "dst.port") {
		t.Errorf("got %q (%s)", resp.Warning, resp.WarningKind)
	}
}

// Failing to check which cluster the series belong to must not fall back to
// drawing the whole store.
func TestScopeCheckFailureShowsNoTraffic(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
		return "", promclient.SeriesIsolation{}, errors.New("query timed out")
	})
	store := &beylaStore{l4: map[string]bool{"a": true, "b": true}}
	resp, err := newScopedBeyla(store).GetFlows(t.Context(), FlowOptions{})
	if err != nil || len(resp.Flows) != 0 || resp.WarningKind != WarningTransient || !resp.WarningClusterScope {
		t.Fatalf("GetFlows: %v %+v", err, resp)
	}
	if len(store.sent()) != 0 {
		t.Errorf("queried flows without a cluster scope: %q", store.sent())
	}
}

// Detection decides whether Beyla is in the store at all, so it reads every
// series under the job selector.
func TestBeylaDetectIsUnfiltered(t *testing.T) {
	useConfiguredStore(t)
	calls := stubScope(t, clusterA)
	store := &beylaStore{l4: map[string]bool{"b": true}}
	result, err := newScopedBeyla(store).Detect(t.Context())
	if err != nil || !result.Available {
		t.Fatalf("Detect: %v %+v", err, result)
	}
	if *calls != 0 {
		t.Error("detection resolved a cluster scope")
	}
}

// Series carrying this cluster's labels that still drew no flows (idle, or not
// placeable on the map) mean this cluster is quiet, not mislabeled.
func TestNoClusterWarningWhenThisClusterHasSeries(t *testing.T) {
	sc := clusterScope{matchers: `cluster="a"`, labels: map[string]string{"cluster": "a"}}
	rows := []map[string]string{{"cluster": "a"}, {"cluster": "b"}}
	if got := sc.clusterMismatchWarning(rows, "Beyla"); got != "" {
		t.Errorf("warning for a quiet cluster: %q", got)
	}
}

// Proven labels stay when this cluster's series carry them under more than one
// identity (two scrape pipelines, one adding cluster_name): dropping them would
// draw other clusters' traffic.
func TestAmbiguityKeepsProvenLabels(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(string) (string, promclient.SeriesIsolation, error) {
		return "", promclient.SeriesIsolation{Mode: promclient.SeriesIsolationVerified, Labels: map[string]string{"cluster": "a"}}, promclient.ErrScopeAmbiguous
	})
	resp, err := newScopedBeyla(&beylaStore{l4: map[string]bool{"a": true, "b": true}}).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := flowNames(resp.Flows); len(names) != 2 || !names["backend-a"] {
		t.Errorf("flows = %v, want cluster a's flows only", names)
	}
	// Two identities under the same labels may also be two clusters sharing them.
	if resp.WarningKind != WarningCluster {
		t.Errorf("warning = %q (%s), want the may-include-other-clusters note", resp.Warning, resp.WarningKind)
	}
}

// HTTP series Radar couldn't scope are left out and named, without taking the
// network flows with them.
func TestHTTPScopeFailureKeepsNetworkFlows(t *testing.T) {
	useConfiguredStore(t)
	stubScope(t, func(metric string) (string, promclient.SeriesIsolation, error) {
		if metric == beylaL7Metric {
			return "", promclient.SeriesIsolation{}, errors.New("query timed out")
		}
		return clusterA(metric)
	})
	store := &beylaStore{l4: map[string]bool{"a": true}, l7: map[string]bool{"a": true}}
	resp, err := newScopedBeyla(store).GetFlows(t.Context(), FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if names := flowNames(resp.Flows); !names["backend-a"] || names["http-a"] {
		t.Errorf("flows = %v, want the network flows without HTTP", names)
	}
	if !strings.Contains(resp.Warning, "HTTP request rates") {
		t.Errorf("warning %q does not name the missing HTTP figures", resp.Warning)
	}
	for _, q := range store.sent() {
		if strings.Contains(q, beylaL7Metric) {
			t.Errorf("queried HTTP series without a cluster scope: %s", q)
		}
	}
}
