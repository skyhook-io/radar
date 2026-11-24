package openobserve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestRewriteRefusesWritingClusterLabel(t *testing.T) {
	std := RewriteOptions{Matchers: []Matcher{{Name: "radar_cluster_uid", Value: "mine"}}}
	refused := []string{
		`label_replace(k8s_pod_cpu_usage, "radar_cluster_uid", "mine", "", "")`,
		`label_replace(k8s_pod_cpu_usage, "Radar_Cluster_UID", "mine", "", "")`,
		`label_join(k8s_pod_cpu_usage, "radar_cluster_uid", "", "x")`,
		`sum by (radar_cluster_uid) (label_replace(k8s_pod_cpu_usage, "radar_cluster_uid", "mine", "", ""))`,
		`count_values("radar_cluster_uid", k8s_pod_cpu_usage)`,
		`label_replace(vector(1), ("radar_cluster_uid"), "mine", "", "")`,
	}
	for _, q := range refused {
		if out, err := Rewrite(q, std); !errors.Is(err, ErrClusterLabelWrite) {
			t.Errorf("Rewrite(%q) = %q, %v; want ErrClusterLabelWrite", q, out, err)
		}
	}

	col := RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: "prod-a"}}, Dialect: DialectCollector, CollectorUID: uidA, GatewayMetricsAllowed: true}
	for _, q := range []string{
		`label_replace(kube_pod_info, "k8s_cluster", "prod-a", "", "")`,
		`label_replace(container_cpu_time, "k8s_cluster_uid", "x", "", "")`,
		`count_values("k8s_cluster", container_cpu_time)`,
		`label_join(container_cpu_time, "k8s_cluster_uid", "", "pod")`,
	} {
		if out, err := Rewrite(q, col); !errors.Is(err, ErrClusterLabelWrite) {
			t.Errorf("collector Rewrite(%q) = %q, %v; want ErrClusterLabelWrite", q, out, err)
		}
	}

	// Writing other labels, and the rewriter's own label_replace calls
	// (vector stamping, collector renames), still work.
	allowed := map[string]RewriteOptions{
		`label_replace(up, "workload", "$1", "pod", "(.*)")`:                        std,
		`label_join(up, "id", "/", "namespace", "pod")`:                             std,
		`count_values("v", up)`:                                                     std,
		`up or vector(0)`:                                                           std,
		`sum by (pod) (rate(container_cpu_usage_seconds_total{container!=""}[5m]))`: col,
		`label_replace(up, "cluster", "x", "", "")`:                                 col,
	}
	for q, opts := range allowed {
		if _, err := Rewrite(q, opts); err != nil {
			t.Errorf("Rewrite(%q): %v", q, err)
		}
	}
	got, err := Rewrite(`sum(rate(container_cpu_usage_seconds_total{namespace="demo",container!=""}[5m])) by (pod)`, col)
	if err != nil || !strings.Contains(got, `label_replace(`) {
		t.Fatalf("collector rename = %q, %v", got, err)
	}
}

// legacyStore fakes OpenObserve before v1.2: a matcher on a label the
// metric's stream has no column for is ignored. Its schema endpoint lists
// each stream's columns; every query answers a canned series.
type legacyStore struct {
	addr    string
	columns map[string][]string // metric -> label columns
	// schemaErr, when set, fails every schema lookup.
	schemaErr error

	mu      sync.Mutex
	probes  []string
	queries []string
	paths   []string
	block   chan struct{}
}

// schema is the store's SchemaFetcher. It records each lookup as
// "metric" in probes.
func (s *legacyStore) schema(_ context.Context, metric string) ([]string, error) {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	s.probes = append(s.probes, metric)
	err := s.schemaErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	cols, ok := s.columns[metric]
	if !ok {
		return nil, fmt.Errorf("stream %s: %w", metric, ErrStreamNotFound)
	}
	return append([]string{"__name__", "_timestamp", "value"}, cols...), nil
}

func (s *legacyStore) transport(opts RewriteOptions) *RewritingTransport {
	return &RewritingTransport{Inner: s, Options: opts, LegacyStore: true, SchemaFetcher: s.schema}
}

func (s *legacyStore) Do(_ context.Context, _, path string, params url.Values) ([]byte, error) {
	q := params.Get("query")
	s.mu.Lock()
	s.paths = append(s.paths, path)
	if q != "" {
		s.queries = append(s.queries, q)
	} else {
		s.queries = append(s.queries, strings.Join(params["match[]"], " "))
	}
	s.mu.Unlock()
	if path == "/api/v1/series" {
		return []byte(`{"status":"success","data":[{"__name__":"x","radar_cluster_uid":"mine"}]}`), nil
	}
	if strings.HasPrefix(path, "/api/v1/label") {
		return []byte(`{"status":"success","data":["a"]}`), nil
	}
	return []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"radar_cluster_uid":"mine"},"value":[1,"1"]}]}}`), nil
}

func (s *legacyStore) Address() string { return s.addr }

func (s *legacyStore) sentProbes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.probes...)
}

func (s *legacyStore) forwarded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func newLegacyStore(t *testing.T) *legacyStore {
	return &legacyStore{
		addr: "legacy-" + t.Name(),
		columns: map[string][]string{
			// B writes radar_cluster_uid on these.
			"kube_pod_info":                     {"cluster", "radar_cluster_uid", "namespace", "pod"},
			"container_cpu_usage_seconds_total": {"cluster", "radar_cluster_uid", "namespace", "pod"},
			// Only the collector writes these: no radar_cluster_uid column.
			"k8s_pod_cpu_usage":  {"k8s_cluster", "k8s_cluster_uid", "k8s_pod_name"},
			"container_cpu_time": {"k8s_cluster", "k8s_cluster_uid", "k8s_pod_name"},
			// Scraped by the collector gateway: name only.
			"kube_deployment_spec_replicas": {"k8s_cluster", "namespace"},
		},
	}
}

func legacyQuery(rt *RewritingTransport, path, q string) ([]byte, error) {
	return rt.Do(context.Background(), "GET", path, url.Values{"query": {q}})
}

var optsMine = RewriteOptions{Matchers: []Matcher{{Name: "radar_cluster_uid", Value: "mine"}}}

// TestLegacyStoreRefusesUnscopableMetrics also covers a metric with columns
// for other clusters' labels but none for ours, whatever time range the
// query reads: the schema decides, not recent data.
func TestLegacyStoreRefusesUnscopableMetrics(t *testing.T) {
	store := newLegacyStore(t)
	store.columns["container_spec_memory_limit_bytes"] = []string{"cluster", "namespace", "pod"}
	rt := store.transport(optsMine)

	for _, q := range []string{
		`k8s_pod_cpu_usage{k8s_namespace_name="demo"}`,
		`vector(scalar(count(k8s_pod_cpu_usage)))`,
		`up * 0 + scalar(sum(k8s_pod_cpu_usage))`,
		`sum(rate(container_cpu_usage_seconds_total[5m])) / scalar(count(k8s_pod_cpu_usage))`,
		`{__name__=~"k8s_.+"}`,
		`vector(scalar(count(last_over_time(container_spec_memory_limit_bytes[30d]))))`,
		`count(container_spec_memory_limit_bytes offset 30d)`,
		`count(container_spec_memory_limit_bytes @ 1700000000)`,
		`max_over_time(count(container_spec_memory_limit_bytes)[7d:1h])`,
		`never_written_metric`,
	} {
		_, err := legacyQuery(rt, "/api/v1/query", q)
		if !errors.Is(err, ErrUnscopable) || !errors.Is(err, ErrNotCollected) {
			t.Errorf("%s: err = %v, want ErrUnscopable", q, err)
		}
	}
	if _, err := rt.Do(context.Background(), "GET", "/api/v1/label/pod/values", url.Values{"match[]": {"container_spec_memory_limit_bytes"}, "start": {"0"}}); !errors.Is(err, ErrUnscopable) {
		t.Errorf("label values with an old start: %v", err)
	}
	if got := store.forwarded(); len(got) != 0 {
		t.Fatalf("unscopable queries reached the store: %v", got)
	}

	// The error names the metric the way an ErrNotCollected error does.
	_, err := legacyQuery(rt, "/api/v1/query", `k8s_pod_cpu_usage`)
	if !strings.HasPrefix(err.Error(), ErrNotCollected.Error()+": k8s_pod_cpu_usage ") {
		t.Fatalf("error text = %q", err)
	}

	if _, err := legacyQuery(rt, "/api/v1/query_range", `sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="demo"}[5m]))`); err != nil {
		t.Fatalf("scopable query refused: %v", err)
	}
	if _, err := legacyQuery(rt, "/api/v1/query", `count(kube_pod_info offset 30d)`); err != nil {
		t.Fatalf("scopable query over an old range refused: %v", err)
	}
	if _, err := legacyQuery(rt, "/api/v1/query", `vector(1)`); err != nil {
		t.Fatalf("query without selectors refused: %v", err)
	}
	if got := store.forwarded(); len(got) != 3 {
		t.Fatalf("forwarded = %v", got)
	}
}

func TestLegacyStoreSchemaColumnsCaseInsensitive(t *testing.T) {
	store := newLegacyStore(t)
	store.columns["kube_pod_info"] = []string{"Radar_Cluster_UID"}
	if _, err := legacyQuery(store.transport(optsMine), "/api/v1/query", `KUBE_POD_INFO`); err != nil {
		t.Fatal(err)
	}
	if p := store.sentProbes(); len(p) != 1 || p[0] != "kube_pod_info" {
		t.Fatalf("schema lookups = %v", p)
	}
}

// A failed schema lookup refuses the request and isn't remembered.
func TestLegacyStoreSchemaErrorRefusesUncached(t *testing.T) {
	store := newLegacyStore(t)
	store.schemaErr = errors.New("boom")
	rt := store.transport(optsMine)
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	limited := ErrRateLimited{RetryAfter: 3 * time.Second}
	store.mu.Lock()
	store.schemaErr = limited
	store.mu.Unlock()
	var got ErrRateLimited
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`); !errors.As(err, &got) || got.RetryAfter != 3*time.Second {
		t.Fatalf("rate limited err = %v", err)
	}
	if got := store.forwarded(); len(got) != 0 {
		t.Fatalf("forwarded after a failed lookup: %v", got)
	}
	store.mu.Lock()
	store.schemaErr = nil
	store.mu.Unlock()
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`); err != nil {
		t.Fatal(err)
	}
	if p := store.sentProbes(); len(p) != 3 {
		t.Fatalf("schema lookups = %v, want a new one after each failure", p)
	}
}

func TestLegacyStoreWithoutSchemaFetcherRefuses(t *testing.T) {
	store := newLegacyStore(t)
	rt := &RewritingTransport{Inner: store, Options: optsMine, LegacyStore: true}
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`); err == nil {
		t.Fatal("answered without a schema check")
	}
	if got := store.forwarded(); len(got) != 0 {
		t.Fatalf("forwarded = %v", got)
	}
}

func TestLegacyStoreLabelLookups(t *testing.T) {
	store := newLegacyStore(t)
	rt := store.transport(optsMine)
	ctx := context.Background()

	for _, tc := range []struct {
		path    string
		matches []string
	}{
		{"/api/v1/labels", nil},
		{"/api/v1/label/k8s_cluster_uid/values", nil},
		{"/api/v1/label/k8s_cluster_uid/values", []string{"k8s_pod_cpu_usage"}},
		{"/api/v1/label/pod/values", []string{"kube_pod_info", "k8s_pod_cpu_usage"}},
		{"/api/v1/labels", []string{"k8s_pod_cpu_usage"}},
		{"/api/v1/series", []string{"k8s_pod_cpu_usage"}},
		{"/api/v1/series", []string{`{__name__=~".+"}`}},
		{"/api/v1/labels", []string{"kube_pod_info"}},
	} {
		params := url.Values{}
		if tc.matches != nil {
			params["match[]"] = tc.matches
		}
		if _, err := rt.Do(ctx, "GET", tc.path, params); !errors.Is(err, ErrUnscopable) {
			t.Errorf("%s %v: err = %v, want ErrUnscopable", tc.path, tc.matches, err)
		}
	}
	if got := store.forwarded(); len(got) != 0 {
		t.Fatalf("refused lookups reached the store: %v", got)
	}

	for _, path := range []string{"/api/v1/label/pod/values", "/api/v1/series"} {
		if _, err := rt.Do(ctx, "GET", path, url.Values{"match[]": {"kube_pod_info"}}); err != nil {
			t.Errorf("%s on a scopable metric: %v", path, err)
		}
	}

	// A store known to filter correctly needs no match[] and no lookups.
	current := newLegacyStore(t)
	current.addr += "-current"
	rt = &RewritingTransport{Inner: current, Options: rt.Options, SchemaFetcher: current.schema}
	if _, err := rt.Do(ctx, "GET", "/api/v1/labels", url.Values{}); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyQuery(rt, "/api/v1/query", `k8s_pod_cpu_usage`); err != nil {
		t.Fatal(err)
	}
	if p := current.sentProbes(); len(p) != 0 {
		t.Fatalf("schema lookups on a current store: %v", p)
	}
}

func TestLegacyStoreCollectorChecksUIDLabel(t *testing.T) {
	store := newLegacyStore(t)
	opts := RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: "prod-a"}}, Dialect: DialectCollector, CollectorUID: uidA, GatewayMetricsAllowed: true}
	rt := store.transport(opts)

	if _, err := legacyQuery(rt, "/api/v1/query", `sum by (pod) (rate(container_cpu_usage_seconds_total{container!=""}[5m]))`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_deployment_spec_replicas`); err != nil {
		t.Fatal(err)
	}
	// One lookup per (metric, label): k8s_cluster and k8s_cluster_uid for
	// the agent metric, k8s_cluster for the gateway one.
	got := strings.Join(store.sentProbes(), " ")
	if got != "container_cpu_time container_cpu_time kube_deployment_spec_replicas" {
		t.Fatalf("schema lookups = %s", got)
	}
	// B's metric has no k8s_cluster column: the collector scope can't use it.
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`); !errors.Is(err, ErrUnscopable) {
		t.Fatalf("kube_pod_info err = %v", err)
	}
	// An agent metric whose stream lacks k8s_cluster_uid is refused.
	store.columns["k8s_pod_memory_usage"] = []string{"k8s_cluster", "k8s_pod_name"}
	if _, err := legacyQuery(rt, "/api/v1/query", `k8s_pod_memory_usage`); !errors.Is(err, ErrUnscopable) {
		t.Fatalf("agent metric without the uid column: %v", err)
	}
}

func TestLegacyStoreSchemaCache(t *testing.T) {
	now := time.Now()
	legacyNow = func() time.Time { return now }
	t.Cleanup(func() { legacyNow = time.Now })

	store := newLegacyStore(t)
	rt := store.transport(optsMine)
	q := `kube_pod_info and on (pod) k8s_pod_cpu_usage and on (pod) brand_new_metric`
	for range 3 {
		_, _ = legacyQuery(rt, "/api/v1/query", q)
		_, _ = legacyQuery(rt, "/api/v1/query", `kube_pod_info`)
	}
	// The first refusal stops the check: kube_pod_info, then k8s_pod_cpu_usage.
	if p := store.sentProbes(); len(p) != 2 {
		t.Fatalf("lookups = %v, want one per metric", p)
	}
	_, _ = legacyQuery(rt, "/api/v1/query", `brand_new_metric`)
	// Another transport for the same store reuses the answers.
	rt2 := store.transport(rt.Options)
	_, _ = legacyQuery(rt2, "/api/v1/query", q)
	_, _ = legacyQuery(rt2, "/api/v1/query", `brand_new_metric`)
	if p := store.sentProbes(); len(p) != 3 {
		t.Fatalf("lookups after a new transport = %v", p)
	}

	// A missing stream expires first: someone may start writing it.
	now = now.Add(legacyNoStreamTTL + time.Second)
	store.columns["brand_new_metric"] = []string{"cluster"}
	_, _ = legacyQuery(rt, "/api/v1/query", `brand_new_metric`)
	if p := store.sentProbes(); len(p) != 4 || p[3] != "brand_new_metric" {
		t.Fatalf("lookups after the no-stream TTL = %v", p)
	}
	_, _ = legacyQuery(rt, "/api/v1/query", `k8s_pod_cpu_usage`)
	if p := store.sentProbes(); len(p) != 4 {
		t.Fatalf("an unscopable answer expired with the no-stream TTL: %v", p)
	}
	now = now.Add(legacyUnscopableTTL)
	_, _ = legacyQuery(rt, "/api/v1/query", `k8s_pod_cpu_usage`)
	if p := store.sentProbes(); len(p) != 5 || p[4] != "k8s_pod_cpu_usage" {
		t.Fatalf("lookups after the unscopable TTL = %v", p)
	}
	_, _ = legacyQuery(rt, "/api/v1/query", `kube_pod_info`)
	if p := store.sentProbes(); len(p) != 5 {
		t.Fatalf("a scopable answer expired early: %v", p)
	}
	now = now.Add(legacyScopableTTL)
	_, _ = legacyQuery(rt, "/api/v1/query", `kube_pod_info`)
	if p := store.sentProbes(); len(p) != 6 {
		t.Fatalf("lookups after the scopable TTL = %v", p)
	}
}

func TestLegacyStoreConcurrentLookupsShareOne(t *testing.T) {
	store := newLegacyStore(t)
	store.block = make(chan struct{})
	rt := store.transport(optsMine)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = legacyQuery(rt, "/api/v1/query", `kube_pod_info`)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(store.block)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p := store.sentProbes(); len(p) != 1 {
		t.Fatalf("lookups = %v", p)
	}
}

func TestLegacyCacheBounded(t *testing.T) {
	store := newLegacyStore(t)
	rt := store.transport(optsMine)
	for i := range legacyCacheMax + 10 {
		if _, err := legacyQuery(rt, "/api/v1/query", fmt.Sprintf("m%d", i)); !errors.Is(err, ErrUnscopable) {
			t.Fatalf("m%d: %v", i, err)
		}
	}
	legacyCache.mu.Lock()
	n := len(legacyCache.entries)
	legacyCache.mu.Unlock()
	if n > legacyCacheMax {
		t.Fatalf("cache holds %d entries", n)
	}
}

// TestNewSchemaFetcher checks the request (path, auth, organization) and the
// answers OpenObserve v1.1 and v1.2 give: a field list, an empty one, and
// 404 for a stream that doesn't exist.
func TestNewSchemaFetcher(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/oo/api/my-org/streams/kube_pod_info/schema":
			_, _ = w.Write([]byte(`{"name":"kube_pod_info","stream_type":"metrics","schema":[{"name":"__name__","type":"Utf8"},{"name":"cluster","type":"Utf8"},{"name":"radar_cluster_uid","type":"Utf8"}],"total_fields":3}`))
		case "/oo/api/my-org/streams/empty_metric/schema":
			_, _ = w.Write([]byte(`{"name":"empty_metric","schema":[],"total_fields":0}`))
		case "/oo/api/my-org/streams/broken/schema":
			_, _ = w.Write([]byte(`{"name":"broken"}`))
		case "/oo/api/my-org/streams/limited/schema":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"stream not found"}`))
		}
	}))
	defer srv.Close()
	conn := Connection{BaseURL: srv.URL + "/oo/", Org: "my-org", User: "u", Token: "t"}
	tr, err := conn.NewHTTPTransport(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fetch := NewSchemaFetcher(tr)
	ctx := context.Background()

	fields, err := fetch(ctx, "KUBE_POD_INFO")
	if err != nil || strings.Join(fields, ",") != "__name__,cluster,radar_cluster_uid" {
		t.Fatalf("fields = %v, %v", fields, err)
	}
	if gotQuery != "type=metrics" || gotAuth != conn.AuthHeader() || gotPath != "/oo/api/my-org/streams/kube_pod_info/schema" {
		t.Fatalf("request %s?%s auth %q", gotPath, gotQuery, gotAuth)
	}
	if fields, err := fetch(ctx, "empty_metric"); err != nil || len(fields) != 0 {
		t.Fatalf("empty = %v, %v", fields, err)
	}
	if _, err := fetch(ctx, "nobody_writes_this"); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("missing stream err = %v", err)
	}
	if _, err := fetch(ctx, "broken"); err == nil || errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("answer without schema err = %v", err)
	}
	var limited ErrRateLimited
	if _, err := fetch(ctx, "limited"); !errors.As(err, &limited) {
		t.Fatalf("429 err = %v", err)
	}

	// Through a transport: an empty schema or missing stream is unscopable.
	store := newLegacyStore(t)
	rt := &RewritingTransport{Inner: store, Options: optsMine, LegacyStore: true, SchemaFetcher: fetch}
	for _, q := range []string{`empty_metric`, `nobody_writes_this`} {
		if _, err := legacyQuery(rt, "/api/v1/query", q); !errors.Is(err, ErrUnscopable) {
			t.Errorf("%s: %v", q, err)
		}
	}
	if _, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`); err != nil {
		t.Fatal(err)
	}
}

// TestLegacyStoreCancelledLeaderDoesNotFailWaiter: the caller that started a
// shared schema lookup going away must not fail the callers waiting on it.
func TestLegacyStoreCancelledLeaderDoesNotFailWaiter(t *testing.T) {
	store := newLegacyStore(t)
	store.block = make(chan struct{})
	rt := store.transport(optsMine)
	key := legacyKey{store: store.addr, metric: "kube_pod_info", label: "radar_cluster_uid"}

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := rt.Do(leaderCtx, "GET", "/api/v1/query", url.Values{"query": {"kube_pod_info"}})
		leaderErr <- err
	}()
	waitInflight(t, key)
	waiterErr := make(chan error, 1)
	go func() {
		_, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`)
		waiterErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v", err)
	}
	close(store.block)
	if err := <-waiterErr; err != nil {
		t.Fatalf("waiter failed with the leader's cancellation: %v", err)
	}
	if p := store.sentProbes(); len(p) != 1 {
		t.Fatalf("lookups = %v, want the one shared lookup", p)
	}
}

// TestLegacyStoreSharedFailureNotHandedOn: a lookup that failed with one
// caller's credentials (another tenant on the same organization) is not the
// answer for a caller waiting with its own.
func TestLegacyStoreSharedFailureNotHandedOn(t *testing.T) {
	store := newLegacyStore(t)
	release := make(chan struct{})
	rejected := func(context.Context, string) ([]string, error) {
		<-release
		return nil, &prom.HTTPError{StatusCode: http.StatusUnauthorized}
	}
	other := &RewritingTransport{Inner: store, Options: optsMine, LegacyStore: true, SchemaFetcher: rejected}
	mine := store.transport(optsMine)
	key := legacyKey{store: store.addr, metric: "kube_pod_info", label: "radar_cluster_uid"}

	otherErr := make(chan error, 1)
	go func() {
		_, err := legacyQuery(other, "/api/v1/query", `kube_pod_info`)
		otherErr <- err
	}()
	waitInflight(t, key)
	mineErr := make(chan error, 1)
	go func() {
		_, err := legacyQuery(mine, "/api/v1/query", `kube_pod_info`)
		mineErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	var httpErr *prom.HTTPError
	if err := <-otherErr; !errors.As(err, &httpErr) {
		t.Fatalf("rejected caller err = %v", err)
	}
	if err := <-mineErr; err != nil {
		t.Fatalf("waiter got another caller's failure: %v", err)
	}
}

func waitInflight(t *testing.T, key legacyKey) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		legacyCache.mu.Lock()
		_, ok := legacyCache.inflight[key]
		legacyCache.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("schema lookup never started")
}

// TestSchemaFetcherOtherNotFoundIsAnError: only OpenObserve's own "stream
// not found" answer means the metric has no stream. Any other 404 (a reverse
// proxy exposing only /prometheus) refuses the query, uncached, rather than
// showing the metric as not collected.
func TestSchemaFetcherOtherNotFoundIsAnError(t *testing.T) {
	var lookups int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lookups++
		mu.Unlock()
		switch r.URL.Path {
		case "/api/my-org/streams/gone/schema":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"Stream Not Found"}`))
		case "/api/my-org/streams/json_other/schema":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"route not found"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	conn := Connection{BaseURL: srv.URL, Org: "my-org", User: "u", Token: "t"}
	tr, err := conn.NewHTTPTransport(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fetch := NewSchemaFetcher(tr)
	if _, err := fetch(context.Background(), "gone"); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("stream not found err = %v", err)
	}
	for _, m := range []string{"kube_pod_info", "json_other"} {
		_, err := fetch(context.Background(), m)
		var httpErr *prom.HTTPError
		if !errors.Is(err, ErrSchemaUnreachable) || errors.Is(err, ErrStreamNotFound) || !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: err = %v", m, err)
		}
		if !strings.Contains(err.Error(), "schema endpoint isn't reachable") {
			t.Fatalf("%s: message = %q", m, err)
		}
	}

	store := newLegacyStore(t)
	rt := &RewritingTransport{Inner: store, Options: optsMine, LegacyStore: true, SchemaFetcher: fetch}
	mu.Lock()
	lookups = 0
	mu.Unlock()
	for range 2 {
		_, err := legacyQuery(rt, "/api/v1/query", `kube_pod_info`)
		if err == nil || errors.Is(err, ErrNotCollected) || !errors.Is(err, ErrSchemaUnreachable) {
			t.Fatalf("query err = %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if lookups != 2 {
		t.Fatalf("schema lookups = %d, want 2 (a failure is not cached)", lookups)
	}
	if got := store.forwarded(); len(got) != 0 {
		t.Fatalf("forwarded = %v", got)
	}
}
