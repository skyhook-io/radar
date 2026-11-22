package prometheus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

type fakeViewQuerier struct {
	mu      sync.Mutex
	queries []string
	answer  func(query string) (*prom.QueryResult, error)
}

func (f *fakeViewQuerier) QueryAtPost(_ context.Context, query string, _ time.Time) (*prom.QueryResult, error) {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	return f.answer(query)
}

func viewRow(metric string, labels map[string]string) prom.Series {
	row := map[string]string{viewScopeMetricLabel: metric}
	maps.Copy(row, labels)
	return vec(row, 1)
}

func viewAnchorRow(metric string) prom.Series {
	return vec(map[string]string{viewScopeAnchorLabel: metric}, 1)
}

func isReadback(query string) bool { return !strings.Contains(query, "topk(2") }

func rows(series ...prom.Series) (*prom.QueryResult, error) {
	return &prom.QueryResult{Series: series}, nil
}

var (
	cpuMetric   = ViewMetric{Name: "container_cpu_usage_seconds_total", Subject: `namespace="shop",pod=~"api-.*"`, AnchorLabel: "pod", AnchorValues: []string{"api-1", "api-0"}}
	ownerMetric = ViewMetric{Name: "kube_pod_owner", Subject: `namespace="shop"`, AnchorLabel: "pod", AnchorValues: []string{"api-0"}}
	termMetric  = ViewMetric{Name: "kube_pod_container_status_last_terminated_reason", Subject: `namespace="shop"`, AnchorLabel: "pod", AnchorValues: []string{"api-0"}}
	clusterA    = map[string]string{"cluster": "a"}
)

func viewRequest(joined bool, metrics ...ViewMetric) ViewScopeRequest {
	req, err := normalizeViewScopeRequest(ViewScopeRequest{View: "test", Metrics: metrics, Joined: joined, Span: time.Hour})
	if err != nil {
		panic(err)
	}
	return req
}

func decide(t *testing.T, q *fakeViewQuerier, req ViewScopeRequest, labels map[string]string, configured bool) ViewScope {
	t.Helper()
	scope, err := decideViewScope(context.Background(), q, req, time.Unix(1000, 0), labels, configured)
	if err != nil && scope.State != ViewScopeUnknown {
		t.Fatalf("error %v with state %s", err, scope.State)
	}
	return scope
}

func TestViewScopeConfiguredLabelsReadBack(t *testing.T) {
	q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		if !isReadback(query) {
			t.Fatalf("unexpected identity query: %s", query)
		}
		return rows(viewRow(cpuMetric.Name, clusterA), viewRow(ownerMetric.Name, clusterA))
	}}
	scope := decide(t, q, viewRequest(true, cpuMetric, ownerMetric), clusterA, true)
	if scope.State != ViewScopeConfigured || scope.Withheld || !maps.Equal(scope.Labels, clusterA) {
		t.Fatalf("scope = %+v", scope)
	}
	for _, m := range []string{cpuMetric.Name, ownerMetric.Name} {
		if scope.Filter(m) != `cluster="a"` {
			t.Errorf("filter for %s = %q", m, scope.Filter(m))
		}
	}
	if len(q.queries) != 1 {
		t.Errorf("want one batched readback, got %d queries", len(q.queries))
	}
}

func TestViewScopeConfiguredLabelMissingOnMetric(t *testing.T) {
	q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		if isReadback(query) {
			return rows(viewRow(ownerMetric.Name, clusterA))
		}
		if strings.Contains(query, ownerMetric.Name) {
			t.Errorf("metric that read back was checked again: %s", query)
		}
		return rows(viewRow(cpuMetric.Name, nil))
	}}
	scope := decide(t, q, viewRequest(true, cpuMetric, ownerMetric), clusterA, true)
	if scope.State != ViewScopeLabelMissing || !scope.Withheld || len(scope.Filters) != 0 {
		t.Fatalf("scope = %+v", scope)
	}
	if !strings.Contains(scope.Note, cpuMetric.Name) || !strings.Contains(scope.Note, `cluster="a"`) || strings.Contains(scope.Note, ownerMetric.Name) {
		t.Errorf("note does not name the metric and label: %q", scope.Note)
	}
}

func TestViewScopeStoreIgnoringFilter(t *testing.T) {
	for name, back := range map[string][]map[string]string{
		"rows without the label": {nil},
		"other values":           {clusterA, {"cluster": "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
				if !isReadback(query) {
					t.Fatalf("an ignored filter must not fall through to the identity check: %s", query)
				}
				var out []prom.Series
				for _, l := range back {
					out = append(out, viewRow(cpuMetric.Name, l))
				}
				return rows(out...)
			}}
			scope := decide(t, q, viewRequest(false, cpuMetric), clusterA, true)
			if scope.State != ViewScopeLabelMissing || !scope.Withheld {
				t.Fatalf("scope = %+v", scope)
			}
		})
	}
}

func TestViewScopeProvenLabelsUnlabeledMetric(t *testing.T) {
	for _, anchored := range []bool{true, false} {
		q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
			if isReadback(query) {
				return rows(viewRow(ownerMetric.Name, clusterA))
			}
			if !strings.Contains(query, `pod=~"api-0|api-1"`) {
				t.Errorf("anchor probe missing or unsorted: %s", query)
			}
			out := []prom.Series{viewRow(cpuMetric.Name, nil)}
			if anchored {
				out = append(out, viewAnchorRow(cpuMetric.Name))
			}
			return rows(out...)
		}}
		scope := decide(t, q, viewRequest(true, cpuMetric, ownerMetric), clusterA, false)
		if !anchored {
			if scope.State != ViewScopeLabelMissing || !scope.Withheld {
				t.Fatalf("foreign-only unlabeled metric accepted: %+v", scope)
			}
			continue
		}
		if scope.State != ViewScopeUnlabeled || scope.Withheld || scope.Filter(cpuMetric.Name) != "" || scope.Filter(ownerMetric.Name) != `cluster="a"` {
			t.Fatalf("anchored unlabeled metric: %+v", scope)
		}
		if _, ok := scope.Filters[cpuMetric.Name]; !ok {
			t.Error("accepted metric has no filter entry")
		}
	}
}

func TestViewScopeAnchoredIdentityNamingAnotherCluster(t *testing.T) {
	q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		if isReadback(query) {
			return rows()
		}
		return rows(viewRow(cpuMetric.Name, map[string]string{"cluster": "b"}), viewAnchorRow(cpuMetric.Name))
	}}
	if scope := decide(t, q, viewRequest(false, cpuMetric), clusterA, false); scope.State != ViewScopeLabelMissing {
		t.Fatalf("another cluster's series accepted by name: %+v", scope)
	}
}

func TestViewScopeTwoIdentitiesWithoutReadback(t *testing.T) {
	q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		if isReadback(query) {
			return rows()
		}
		return rows(viewRow(cpuMetric.Name, nil), viewRow(cpuMetric.Name, map[string]string{"cluster_name": "x"}), viewAnchorRow(cpuMetric.Name))
	}}
	if scope := decide(t, q, viewRequest(false, cpuMetric), clusterA, false); scope.State != ViewScopeLabelMissing {
		t.Fatalf("several identities accepted: %+v", scope)
	}
}

// Metrics a joined view reads unfiltered on the strength of their anchors
// must name one cluster between them, as they must without labels.
func TestViewScopeJoinedUnfilteredIdentitiesAgree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		joined bool
		ids    [2]map[string]string
		want   ViewScopeState
	}{
		{name: "joined, different clusters", joined: true, ids: [2]map[string]string{{"cluster_name": "b"}, {"cluster_name": "c"}}, want: ViewScopeLabelMissing},
		{name: "joined, same cluster", joined: true, ids: [2]map[string]string{{"cluster_name": "b"}, {"cluster_name": "b"}}, want: ViewScopeUnlabeled},
		{name: "joined, one without identity labels", joined: true, ids: [2]map[string]string{{"cluster_name": "b"}, nil}, want: ViewScopeUnlabeled},
		{name: "not joined, different clusters", ids: [2]map[string]string{{"cluster_name": "b"}, {"cluster_name": "c"}}, want: ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
				if isReadback(query) {
					return rows(viewRow(termMetric.Name, clusterA))
				}
				return rows(viewRow(cpuMetric.Name, tc.ids[0]), viewAnchorRow(cpuMetric.Name), viewRow(ownerMetric.Name, tc.ids[1]), viewAnchorRow(ownerMetric.Name))
			}}
			scope := decide(t, q, viewRequest(tc.joined, cpuMetric, ownerMetric, termMetric), clusterA, false)
			if scope.State != tc.want {
				t.Fatalf("state = %s, want %s (%+v)", scope.State, tc.want, scope)
			}
			if tc.want == ViewScopeLabelMissing {
				if !scope.Withheld || len(scope.Filters) != 0 || !strings.Contains(scope.Note, cpuMetric.Name) || !strings.Contains(scope.Note, ownerMetric.Name) || strings.Contains(scope.Note, termMetric.Name) {
					t.Errorf("withheld scope = %+v", scope)
				}
				return
			}
			if scope.Withheld || scope.Filter(cpuMetric.Name) != "" || scope.Filter(ownerMetric.Name) != "" || scope.Filter(termMetric.Name) != `cluster="a"` {
				t.Errorf("scope = %+v", scope)
			}
		})
	}
}

func TestViewScopeAbsentOptionalMetric(t *testing.T) {
	q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
		if isReadback(query) {
			return rows(viewRow(cpuMetric.Name, clusterA))
		}
		return rows()
	}}
	scope := decide(t, q, viewRequest(true, cpuMetric, termMetric), clusterA, false)
	if scope.State != ViewScopeVerified || scope.Withheld || scope.Filter(termMetric.Name) != `cluster="a"` {
		t.Fatalf("absent metric withheld or unfiltered: %+v", scope)
	}
	if viewScopeTTL(scope) != viewScopeScopedTTL {
		t.Errorf("filtered-or-absent decision TTL = %v", viewScopeTTL(scope))
	}
}

func TestViewScopeWithoutLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		joined bool
		rows   []prom.Series
		want   ViewScopeState
	}{
		{name: "one identity", rows: []prom.Series{viewRow(cpuMetric.Name, nil), viewRow(ownerMetric.Name, nil)}, want: ViewScopeUnlabeled},
		{name: "two identities", rows: []prom.Series{viewRow(cpuMetric.Name, clusterA), viewRow(cpuMetric.Name, map[string]string{"cluster": "b"})}, want: ViewScopeSeveralClusters},
		{name: "all absent", want: ViewScopeUnlabeled},
		{name: "joined differing identities", joined: true, rows: []prom.Series{viewRow(cpuMetric.Name, clusterA), viewRow(ownerMetric.Name, map[string]string{"cluster": "b"})}, want: ViewScopeSeveralClusters},
		{name: "joined with an empty identity", joined: true, rows: []prom.Series{viewRow(cpuMetric.Name, clusterA), viewRow(ownerMetric.Name, nil)}, want: ViewScopeUnlabeled},
		{name: "unjoined differing identities", rows: []prom.Series{viewRow(cpuMetric.Name, clusterA), viewRow(ownerMetric.Name, map[string]string{"cluster": "b"})}, want: ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
				if isReadback(query) || strings.Contains(query, viewScopeAnchorLabel) {
					t.Fatalf("unexpected query without labels: %s", query)
				}
				return rows(tc.rows...)
			}}
			scope := decide(t, q, viewRequest(tc.joined, cpuMetric, ownerMetric), nil, false)
			if scope.State != tc.want {
				t.Fatalf("state = %s, want %s (%+v)", scope.State, tc.want, scope)
			}
			if tc.want == ViewScopeSeveralClusters {
				if !scope.Withheld || !strings.Contains(scope.Note, "--prometheus-cluster-label") {
					t.Errorf("several clusters: %+v", scope)
				}
				return
			}
			if scope.Withheld || scope.Filter(cpuMetric.Name) != "" || len(scope.Filters) != 2 || viewScopeTTL(scope) != viewScopeUnscopedTTL {
				t.Errorf("unlabeled: %+v", scope)
			}
		})
	}
}

func TestViewScopeIncompleteAnswersAreUnknown(t *testing.T) {
	for name, answer := range map[string]func() (*prom.QueryResult, error){
		"partial": func() (*prom.QueryResult, error) {
			return &prom.QueryResult{Series: []prom.Series{viewRow(cpuMetric.Name, clusterA)}, Annotations: prom.QueryAnnotations{Partial: true}}, nil
		},
		"warning": func() (*prom.QueryResult, error) {
			return &prom.QueryResult{Series: []prom.Series{viewRow(cpuMetric.Name, clusterA)}, Annotations: prom.QueryAnnotations{Warnings: []string{"store down"}}}, nil
		},
		"error":      func() (*prom.QueryResult, error) { return nil, errors.New("boom") },
		"stray rows": func() (*prom.QueryResult, error) { return rows(vec(map[string]string{"cluster": "a"}, 1)) },
	} {
		t.Run(name, func(t *testing.T) {
			for _, labels := range []map[string]string{clusterA, nil} {
				q := &fakeViewQuerier{answer: func(string) (*prom.QueryResult, error) { return answer() }}
				scope := decide(t, q, viewRequest(false, cpuMetric), labels, true)
				if scope.State != ViewScopeUnknown || !scope.Withheld || scope.Note != viewScopeUnknownNote || viewScopeTTL(scope) != 0 {
					t.Fatalf("labels=%v: %+v", labels, scope)
				}
			}
		})
	}
}

func TestViewScopeQueryShape(t *testing.T) {
	req := viewRequest(true, cpuMetric, ViewMetric{Name: "kube_pod_info"})
	got := viewReadbackQuery(req.Metrics, []string{"cluster", "region"}, `cluster="a",region="eu"`, req.Span)
	want := `label_replace(count by (cluster,region) (count_over_time(container_cpu_usage_seconds_total{namespace="shop",pod=~"api-.*",cluster="a",region="eu"}[3600s])), "radar_metric", "container_cpu_usage_seconds_total", "", "")` +
		` or label_replace(count by (cluster,region) (count_over_time(kube_pod_info{cluster="a",region="eu"}[3600s])), "radar_metric", "kube_pod_info", "", "")`
	if got != want {
		t.Fatalf("readback query\n got: %s\nwant: %s", got, want)
	}
	identity := viewIdentityQuery(req.Metrics[:1], []string{"cluster"}, req.Span, true)
	wantIdentity := `label_replace(topk(2, count by (cluster) (count_over_time(container_cpu_usage_seconds_total{namespace="shop",pod=~"api-.*"}[3600s]))), "radar_metric", "container_cpu_usage_seconds_total", "", "")` +
		` or label_replace(count(count_over_time(container_cpu_usage_seconds_total{namespace="shop",pod=~"api-0|api-1"}[3600s])), "radar_anchor", "container_cpu_usage_seconds_total", "", "")`
	if identity != wantIdentity {
		t.Fatalf("identity query\n got: %s\nwant: %s", identity, wantIdentity)
	}
}

// An anchor restricted to a namespace must not let a same-named object in
// another namespace (possibly another cluster's) vouch for this cluster.
func TestViewScopeAnchorNamespace(t *testing.T) {
	m := ViewMetric{Name: "container_cpu_usage_seconds_total", AnchorLabel: "pod", AnchorValues: []string{"agent-0"}, AnchorNamespace: `kube-"system`}
	got := viewIdentityQuery([]ViewMetric{m}, []string{"cluster"}, time.Hour, true)
	if !strings.Contains(got, `{namespace="kube-\"system",pod=~"agent-0"}`) {
		t.Fatalf("anchor count lacks the namespace matcher: %s", got)
	}

	withNS, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{m}})
	if err != nil {
		t.Fatal(err)
	}
	without := m
	without.AnchorNamespace = ""
	withoutNS, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{without}})
	if err != nil {
		t.Fatal(err)
	}
	other := m
	other.AnchorNamespace = "payments"
	otherNS, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{other}})
	if err != nil {
		t.Fatal(err)
	}
	key := func(req ViewScopeRequest) string {
		return viewScopeKey(1, 1, prom.WorkloadMetricsScope{}, "", false, req, true, time.Time{})
	}
	if key(withNS) == key(withoutNS) || key(withNS) == key(otherNS) {
		t.Fatal("anchor namespace must be part of the cache key")
	}

	// Without anchors to check, the namespace means nothing and is dropped.
	bare := ViewMetric{Name: "kube_pod_info", AnchorNamespace: "shop"}
	norm, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{bare}})
	if err != nil {
		t.Fatal(err)
	}
	if norm.Metrics[0].AnchorNamespace != "" {
		t.Fatalf("anchor namespace kept without anchors: %+v", norm.Metrics[0])
	}
}

func TestViewScopeSpanBucketNeverShrinks(t *testing.T) {
	for span, want := range map[time.Duration]time.Duration{
		0:                               5 * time.Minute,
		65*time.Minute + 30*time.Second: 70 * time.Minute,
		7*24*time.Hour + 5*time.Minute:  7*24*time.Hour + time.Hour,
		time.Hour + 5*time.Minute:       65 * time.Minute,
		59*time.Minute + time.Second:    time.Hour,
	} {
		if got := viewScopeSpanBucket(span); got != want || got < span {
			t.Errorf("bucket(%v) = %v, want %v", span, got, want)
		}
	}
}

func TestBoundViewAnchors(t *testing.T) {
	names := []string{"b", "", "a", "b"}
	for i := 0; i < 100; i++ {
		names = append(names, "z"+strings.Repeat("x", i))
	}
	got := BoundViewAnchors(names)
	if len(got) != viewScopeAnchorLimit || got[0] != "a" || got[1] != "b" {
		t.Fatalf("anchors = %v", got[:3])
	}
	if BoundViewAnchors([]string{""}) != nil {
		t.Error("empty anchors not nil")
	}
}

// viewScopeStore answers the client's HTTP queries and counts the ones that
// aren't connection probes.
type viewScopeStore struct {
	mu      sync.Mutex
	queries []string
	answer  func(query string) []byte
}

func newViewScopeClient(t *testing.T, store *viewScopeStore) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query := r.Form.Get("query")
		if query == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		store.mu.Lock()
		store.queries = append(store.queries, query)
		store.mu.Unlock()
		_, _ = w.Write(store.answer(query))
	}))
	t.Cleanup(srv.Close)
	return &Client{manualURL: srv.URL, httpClient: srv.Client()}
}

func (s *viewScopeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

func TestResolveViewScopeSingleClusterAssertion(t *testing.T) {
	store := &viewScopeStore{answer: func(string) []byte { t.Fatal("query sent for an asserted single cluster"); return nil }}
	c := newViewScopeClient(t, store)
	c.workloadScope = &prom.WorkloadMetricsScope{SingleCluster: true}
	scope, err := c.ResolveViewScope(context.Background(), ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}}, nil)
	if err != nil || scope.State != ViewScopeSingleClusterAsserted || scope.Withheld || scope.Filter(cpuMetric.Name) != "" {
		t.Fatalf("scope = %+v err=%v", scope, err)
	}
	if _, ok := scope.Filters[cpuMetric.Name]; !ok || store.count() != 0 {
		t.Fatalf("filters=%v queries=%d", scope.Filters, store.count())
	}
}

func TestResolveViewScopeCache(t *testing.T) {
	store := &viewScopeStore{answer: func(query string) []byte {
		if isReadback(query) {
			return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, clusterA)}, "")
		}
		return attributionEvidence(nil, "")
	}}
	c := newViewScopeClient(t, store)
	c.workloadScope = &prom.WorkloadMetricsScope{ClusterLabels: clusterA}
	req := ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}, Span: time.Hour}
	resolve := func() ViewScope {
		t.Helper()
		scope, err := c.ResolveViewScope(context.Background(), req, nil)
		if err != nil || scope.State != ViewScopeConfigured {
			t.Fatalf("scope = %+v err=%v", scope, err)
		}
		return scope
	}
	first := resolve()
	if store.count() != 1 {
		t.Fatalf("first check sent %d queries", store.count())
	}
	first.Filters[cpuMetric.Name] = "mutated"
	if got := resolve(); got.Filter(cpuMetric.Name) != `cluster="a"` || store.count() != 1 {
		t.Fatalf("cache miss or shared map: filter=%q queries=%d", got.Filter(cpuMetric.Name), store.count())
	}
	for key, entry := range c.viewScopes {
		if ttl := time.Until(entry.expires); ttl < 4*time.Minute || ttl > viewScopeScopedTTL {
			t.Errorf("scoped entry %s TTL %v", key, ttl)
		}
	}
	req.Span = 61 * time.Minute
	resolve()
	if store.count() != 2 {
		t.Fatalf("different span bucket shared a decision: %d queries", store.count())
	}
	req.Span = time.Hour
	c.mu.Lock()
	c.connectionEpoch++
	c.mu.Unlock()
	resolve()
	if store.count() != 3 {
		t.Fatalf("decision crossed a connection epoch: %d queries", store.count())
	}
	c.mu.Lock()
	c.workloadScope = &prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "a", "region": "eu"}}
	c.mu.Unlock()
	store.answer = func(string) []byte {
		return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, map[string]string{"cluster": "a", "region": "eu"})}, "")
	}
	resolve()
	if store.count() != 4 {
		t.Fatalf("decision survived a config change: %d queries", store.count())
	}
	c.mu.Lock()
	c.beylaJobSelector = `job="beyla"`
	c.mu.Unlock()
	resolve()
	if store.count() != 5 {
		t.Fatalf("decision survived a Beyla selector change: %d queries", store.count())
	}
}

func TestResolveViewScopeUnlabeledTTLAndUnknownNotCached(t *testing.T) {
	warn := true
	store := &viewScopeStore{answer: func(string) []byte {
		if warn {
			return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, nil)}, "warnings")
		}
		return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, nil)}, "")
	}}
	c := newViewScopeClient(t, store)
	req := ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}, Span: time.Hour}
	for i := 1; i <= 2; i++ {
		scope, err := c.ResolveViewScope(context.Background(), req, nil)
		if err != nil || scope.State != ViewScopeUnknown || store.count() != i || len(c.viewScopes) != 0 {
			t.Fatalf("call %d: scope=%+v err=%v queries=%d cached=%d", i, scope, err, store.count(), len(c.viewScopes))
		}
	}
	warn = false
	scope, err := c.ResolveViewScope(context.Background(), req, nil)
	if err != nil || scope.State != ViewScopeUnlabeled {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	for _, entry := range c.viewScopes {
		if ttl := time.Until(entry.expires); ttl > viewScopeUnscopedTTL {
			t.Errorf("unfiltered decision cached for %v", ttl)
		}
	}
	if _, _ = c.ResolveViewScope(context.Background(), req, nil); store.count() != 3 {
		t.Fatalf("unlabeled decision not reused: %d queries", store.count())
	}
}

func TestResolveViewScopeCallerCancellation(t *testing.T) {
	release := make(chan struct{})
	store := &viewScopeStore{answer: func(string) []byte {
		<-release
		return attributionEvidence(nil, "")
	}}
	c := newViewScopeClient(t, store)
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ResolveViewScope(ctx, ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}}, nil)
		done <- err
	}()
	waitFor(t, "check to start", func() bool { return store.count() == 1 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestViewScopeCacheIsBounded(t *testing.T) {
	c := &Client{}
	for i := 0; i < viewScopeCacheLimit+10; i++ {
		c.storeViewScope(strings.Repeat("k", i+1), ViewScope{State: ViewScopeUnlabeled}, time.Minute, 0, 0)
	}
	if len(c.viewScopes) != viewScopeCacheLimit {
		t.Fatalf("cache holds %d entries", len(c.viewScopes))
	}
	if _, ok := c.viewScopes["k"]; ok {
		t.Error("oldest entry not evicted")
	}
	c.connectionEpoch = 1
	c.storeViewScope("stale", ViewScope{}, time.Minute, 0, 0)
	if _, ok := c.viewScopes["stale"]; ok {
		t.Error("decision from a previous connection stored")
	}
}

func TestHistoricalPartitionMemoChecksConnectionEpoch(t *testing.T) {
	config := prom.WorkloadMetricsScope{ClusterLabels: clusterA}
	c := &Client{discoveryGen: 3, connectionEpoch: 1, workloadPartition: &workloadPartitionMemo{generation: 3, epoch: 1, expires: time.Now().Add(time.Minute), scope: config}}
	if got, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: "shop"}, nil); err != nil || got.ClusterLabels["cluster"] != "a" {
		t.Fatalf("valid proof not reused: %+v %v", got, err)
	}
	c.connectionEpoch++
	if got, _ := c.historicalClusterScope(context.Background(), PodScope{Namespace: "shop"}, nil); len(got.ClusterLabels) > 0 {
		t.Fatal("proof crossed a connection epoch")
	}
	store := &viewScopeStore{answer: func(string) []byte { return attributionEvidence(nil, "") }}
	c = newViewScopeClient(t, store)
	identities := []prom.WorkloadPodIdentity{{Name: "api-0", UID: "u"}}
	c.workloadPartition = &workloadPartitionMemo{expires: time.Now().Add(time.Minute), unlabeled: true, namespace: "shop", identities: fmt.Sprint(identities)}
	if proof, err := c.historicalClusterProof(context.Background(), PodScope{Namespace: "shop", Identities: identities}, nil); err != nil || !proof.unlabeled || store.count() != 0 {
		t.Fatalf("unlabeled proof not reused: %+v err=%v queries=%d", proof, err, store.count())
	}
	c.mu.Lock()
	c.connectionEpoch++
	c.mu.Unlock()
	if proof, err := c.historicalClusterProof(context.Background(), PodScope{Namespace: "shop", Identities: identities}, nil); err != nil || proof.unlabeled || store.count() != 1 {
		t.Fatalf("unlabeled proof crossed a connection epoch: %+v err=%v queries=%d", proof, err, store.count())
	}
}

func TestResolveViewScopePinnedTimesDoNotShareDecisions(t *testing.T) {
	store := &viewScopeStore{answer: func(string) []byte {
		return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, nil)}, "")
	}}
	c := newViewScopeClient(t, store)
	pinned := time.Now().Add(-24 * time.Hour).Truncate(time.Minute).Add(10 * time.Second)
	for i, at := range []time.Time{pinned, pinned.Add(40 * time.Second)} {
		req := ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}, Span: time.Hour, At: at}
		if _, err := c.ResolveViewScope(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		if store.count() != i+1 {
			t.Fatalf("pinned check %d reused another evaluation time's decision: %d queries", i, store.count())
		}
	}
}

func TestResolveViewScopeSupersededConnectionIsUnknown(t *testing.T) {
	release := make(chan struct{})
	store := &viewScopeStore{answer: func(string) []byte {
		<-release
		return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, nil)}, "")
	}}
	c := newViewScopeClient(t, store)
	done := make(chan ViewScope, 1)
	go func() {
		scope, _ := c.ResolveViewScope(context.Background(), ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}}, nil)
		done <- scope
	}()
	waitFor(t, "check to start", func() bool { return store.count() == 1 })
	c.mu.Lock()
	c.connectionEpoch++
	c.mu.Unlock()
	close(release)
	if scope := <-done; scope.State != ViewScopeUnknown || !scope.Withheld {
		t.Fatalf("superseded connection's decision reached the caller: %+v", scope)
	}
	if len(c.viewScopes) != 0 {
		t.Fatalf("superseded decision cached: %d", len(c.viewScopes))
	}
}

func TestResolveViewScopeLeaderDeadlineDoesNotFailWaiters(t *testing.T) {
	release := make(chan struct{})
	store := &viewScopeStore{answer: func(string) []byte {
		<-release
		return attributionEvidence([]prom.Series{viewRow(cpuMetric.Name, nil)}, "")
	}}
	c := newViewScopeClient(t, store)
	req := ViewScopeRequest{View: "test", Metrics: []ViewMetric{cpuMetric}}
	leaderCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	leader := make(chan error, 1)
	go func() {
		_, err := c.ResolveViewScope(leaderCtx, req, nil)
		leader <- err
	}()
	waitFor(t, "check to start", func() bool { return store.count() == 1 })
	waiter := make(chan ViewScope, 1)
	go func() {
		scope, _ := c.ResolveViewScope(context.Background(), req, nil)
		waiter <- scope
	}()
	if err := <-leader; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("leader err = %v", err)
	}
	close(release)
	if scope := <-waiter; scope.State != ViewScopeUnlabeled {
		t.Fatalf("waiter failed with the leader's deadline: %+v", scope)
	}
	if store.count() != 1 {
		t.Fatalf("waiter did not share the flight: %d queries", store.count())
	}
}
