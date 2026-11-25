package openobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	uidB = "7b930c6c-dcbb-4d1f-9afd-67ea890873d7"
	uidC = "dd1bfeff-e60c-4827-b8e0-b822ab22a9e8"
)

// route answers queries containing every substring in match.
type route struct {
	match []string
	rows  []map[string]string
	err   error
}

// routeStore is a fake OpenObserve answering by query text, the way the
// phase 0 environment answered (clusters A collector, B remote write with
// radar_cluster_uid, C remote write named cluster1).
type routeStore struct {
	mu       sync.Mutex
	routes   []route
	queries  []string
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
}

func (s *routeStore) Do(ctx context.Context, _, path string, params url.Values) ([]byte, error) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		m := s.maxSeen.Load()
		if n <= m || s.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if path != "/api/v1/query" {
		return nil, fmt.Errorf("unexpected path %s", path)
	}
	q := params.Get("query")
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()
	for _, r := range s.routes {
		all := true
		for _, sub := range r.match {
			if !strings.Contains(q, sub) {
				all = false
				break
			}
		}
		if all {
			if r.err != nil {
				return nil, r.err
			}
			return vectorBody(r.rows), nil
		}
	}
	return vectorBody(nil), nil
}

func (s *routeStore) Address() string { return "fake" }

func (s *routeStore) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func vectorBody(rows []map[string]string) []byte {
	type sample struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	result := make([]sample, 0, len(rows))
	for _, r := range rows {
		result = append(result, sample{Metric: r, Value: [2]any{1700000000, "1"}})
	}
	body, _ := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	return body
}

func init() {
	retryDelay = func(int) time.Duration { return 0 }
}

func TestDiscoverIDs(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{"kube_pod_info{radar_cluster_uid=~"}, rows: []map[string]string{
			{"radar_cluster_uid": uidB, "cluster": "oo-b"},
			// OpenObserve < v1.2 can return rows the matcher should have excluded.
			{"cluster": "cluster1"},
			{"k8s_cluster": "cluster1"},
		}},
		{match: []string{"k8s_pod_cpu_time{k8s_cluster_uid=~"}, rows: []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "cluster1"}}},
		{match: []string{"k8s_pod_cpu_time[7d]"}, rows: []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "cluster1"}}},
	}}
	got, err := DiscoverIDs(context.Background(), store, []string{uidA, uidB, uidC, uidB})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("matches = %+v", got)
	}
	if _, ok := got[uidC]; ok {
		t.Fatal("C has no ID label and must not match by ID")
	}

	b := got[uidB]
	wantB := ClusterMatch{
		By: "id", Dialect: DialectStandard,
		Options: RewriteOptions{Matchers: []Matcher{{Name: "radar_cluster_uid", Value: uidB}}, Dialect: DialectStandard},
		Label:   "radar_cluster_uid", Value: uidB, Name: "oo-b",
	}
	if !reflect.DeepEqual(b, wantB) {
		t.Fatalf("B = %+v\nwant %+v", b, wantB)
	}

	a := got[uidA]
	if a.By != "id" || a.Dialect != DialectCollector || a.Label != "k8s_cluster_uid" || a.Value != uidA || a.Name != "cluster1" {
		t.Fatalf("A = %+v", a)
	}
	wantOpts := RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: "cluster1"}}, Dialect: DialectCollector, CollectorUID: uidA}
	if !reflect.DeepEqual(a.Options, wantOpts) {
		t.Fatalf("A options = %+v", a.Options)
	}
	if !strings.Contains(a.Warning, `"cluster1"`) {
		t.Fatalf("A warning = %q", a.Warning)
	}
	wantMissing := []Family{FamilyThrottling, FamilyFilesystem, FamilyRestarts, FamilyVolumes, FamilyNodes, FamilyHPA}
	if !reflect.DeepEqual(a.Missing, wantMissing) {
		t.Fatalf("A missing = %v", a.Missing)
	}
	// The options must be usable as is.
	if _, err := Rewrite(`sum(rate(container_cpu_usage_seconds_total[5m]))`, a.Options); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverIDsCollectorNameUniqueness(t *testing.T) {
	const other = "11111111-2222-3333-4444-555555555555"
	cases := []struct {
		name    string
		usage   []map[string]string
		allowed bool
	}{
		{"unique", []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "prod-a"}}, true},
		{"shared with another collector", []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "prod-a"}, {"k8s_cluster_uid": other, "k8s_cluster": "prod-a"}}, false},
		{"shared with an agent sending no uid", []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "prod-a"}, {"k8s_cluster": "prod-a"}}, false},
		// A cluster rebuilt under the same name: the old collector stopped
		// within the 7 days charts read, so its name-only series still chart.
		{"sent by a rebuilt cluster's old collector this week", []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "prod-a"}, {"k8s_cluster_uid": other, "k8s_cluster": "prod-a"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &routeStore{routes: []route{
				{match: []string{"k8s_pod_cpu_time{k8s_cluster_uid=~"}, rows: []map[string]string{{"k8s_cluster_uid": uidA, "k8s_cluster": "prod-a"}}},
				{match: []string{"k8s_pod_cpu_time[7d]"}, rows: tc.usage},
			}}
			got, err := DiscoverIDs(context.Background(), store, []string{uidA})
			if err != nil {
				t.Fatal(err)
			}
			a := got[uidA]
			if a.Options.GatewayMetricsAllowed != tc.allowed {
				t.Fatalf("GatewayMetricsAllowed = %v", a.Options.GatewayMetricsAllowed)
			}
			if tc.allowed {
				if a.Warning != "" || !reflect.DeepEqual(a.Missing, []Family{FamilyVolumes, FamilyNodes}) {
					t.Fatalf("A = %+v", a)
				}
			} else if !strings.Contains(a.Warning, "More than one") {
				t.Fatalf("warning = %q", a.Warning)
			}
		})
	}
}

func TestDiscoverIDsBatches(t *testing.T) {
	var uids []string
	for i := 0; i < 130; i++ {
		uids = append(uids, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
	}
	store := &routeStore{}
	if _, err := DiscoverIDs(context.Background(), store, uids); err != nil {
		t.Fatal(err)
	}
	batches := 0
	for _, q := range store.sent() {
		if strings.Contains(q, "kube_pod_info") {
			batches++
			if n := strings.Count(q, "|") + 1; n > 64 {
				t.Fatalf("batch of %d UIDs", n)
			}
		}
	}
	if batches != 3 {
		t.Fatalf("batches = %d", batches)
	}
	if _, err := DiscoverIDs(context.Background(), store, []string{`a"|.*`}); err == nil {
		t.Fatal("unsafe UID accepted")
	}
}

func samplePods(ns string, pods ...string) PodSample {
	s := PodSample{Namespace: ns}
	for i := 0; i+1 < len(pods); i += 2 {
		s.Pods = append(s.Pods, PodRef{Name: pods[i], UID: pods[i+1]})
	}
	return s
}

const (
	cDB  = "5d8da521-0fc8-4ae1-8fa3-472e4ae52619"
	cWeb = "0b9d1d1e-4b8a-4a59-9a2a-1f0c4a1b2c3d"
)

func TestSuggestNameForC(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{`kube_pod_info{namespace="demo",uid=~`}, rows: []map[string]string{
			{"uid": cDB, "cluster": "cluster1"},
			{"uid": cWeb, "cluster": "cluster1"},
		}},
		// A's collector Pods use k8s_cluster="cluster1" and do not clash with C's cluster label.
		{match: []string{`cluster="cluster1"`}, rows: []map[string]string{
			{"uid": cDB, "pod": "db-0", "cluster": "cluster1"},
			{"uid": cWeb, "pod": "web-1", "cluster": "cluster1"},
		}},
	}}
	got, err := SuggestName(context.Background(), store, samplePods("demo", "db-0", cDB, "web-1", cWeb), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "cluster" || got.Value != "cluster1" || !got.UnsafeDefault || got.Conflict {
		t.Fatalf("suggestion = %+v", got)
	}
}

func TestSuggestNameConflict(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{`uid=~`}, rows: []map[string]string{{"uid": cDB, "cluster": "shop"}}},
		{match: []string{`cluster="shop"`}, rows: []map[string]string{
			{"uid": cDB, "pod": "db-0", "cluster": "shop"},
			{"uid": "ffffffff-0000-0000-0000-000000000000", "pod": "db-0", "cluster": "shop"},
			{"uid": "eeeeeeee-0000-0000-0000-000000000000", "pod": "other-pod", "cluster": "shop"},
		}},
	}}
	got, err := SuggestName(context.Background(), store, samplePods("demo", "db-0", cDB), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "shop" || !got.Conflict || got.UnsafeDefault || !strings.Contains(got.Reason, "demo/db-0") {
		t.Fatalf("suggestion = %+v", got)
	}
}

// A Pod replaced under the same name (a StatefulSet's db-0) leaves its old
// UID in the 15-minute lookback; only Pods still reporting are another
// cluster writing the name.
func TestSuggestNameIgnoresReplacedPods(t *testing.T) {
	const replaced = "ffffffff-0000-0000-0000-000000000000"
	store := &routeStore{routes: []route{
		{match: []string{`uid=~`}, rows: []map[string]string{{"uid": cDB, "cluster": "shop"}}},
		{match: []string{`cluster="shop"`, "[5m]"}, rows: []map[string]string{
			{"uid": cDB, "pod": "db-0", "cluster": "shop"},
		}},
		{match: []string{`cluster="shop"`, "[15m]"}, rows: []map[string]string{
			{"uid": cDB, "pod": "db-0", "cluster": "shop"},
			{"uid": replaced, "pod": "db-0", "cluster": "shop"},
		}},
	}}
	got, err := SuggestName(context.Background(), store, samplePods("demo", "db-0", cDB), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "shop" || got.Conflict {
		t.Fatalf("a replaced Pod counted as another cluster: %+v", got)
	}
}

func TestSuggestNameOverrideAndNotFound(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{`uid=~`, "team_cluster, cluster,"}, rows: []map[string]string{{"uid": cDB, "cluster": "x", "team_cluster": "payments-eu"}}},
	}}
	got, err := SuggestName(context.Background(), store, samplePods("demo", "db-0", cDB), "Team_Cluster")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "team_cluster" || got.Value != "payments-eu" {
		t.Fatalf("suggestion = %+v", got)
	}

	empty := &routeStore{}
	got, err = SuggestName(context.Background(), empty, samplePods("demo", "db-0", cDB), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "" || got.Value != "" || got.Reason == "" {
		t.Fatalf("nothing found = %+v", got)
	}

	for _, bad := range []PodSample{{}, {Namespace: "demo"}, samplePods("demo", "db-0", `x"|.*`)} {
		if _, err := SuggestName(context.Background(), empty, bad, ""); err == nil {
			t.Fatalf("sample %+v accepted", bad)
		}
	}
	if _, err := SuggestName(context.Background(), empty, samplePods("demo", "db-0", cDB), `bad"label`); err == nil {
		t.Fatal("bad override label accepted")
	}
}

// OpenObserve strips the cluster label on remote write by default, so a
// setup that set it finds the Pods with no cluster label at all.
func TestSuggestNamePodsWithoutClusterLabel(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{`uid=~`}, rows: []map[string]string{{"uid": cDB}}},
	}}
	got, err := SuggestName(context.Background(), store, samplePods("demo", "db-0", cDB), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "" || got.Value != "" || !strings.Contains(got.Reason, "carry none of the cluster labels") ||
		!strings.Contains(got.Reason, "OpenObserve removes it on remote write by default") {
		t.Fatalf("suggestion = %+v", got)
	}
}

func TestConfirmedNameMatch(t *testing.T) {
	if _, err := ConfirmedNameMatch(context.Background(), &routeStore{}, "cluster", "cluster1", uidC); !errors.Is(err, ErrUnsafeDefault) {
		t.Fatalf("cluster1 err = %v", err)
	}

	ooc := []map[string]string{{"cluster": "oo-c"}}
	store := &routeStore{routes: []route{
		{match: []string{`kube_pod_info{cluster="oo-c"}`}, rows: ooc},
		{match: []string{`container_cpu_usage_seconds_total{cluster="oo-c"}`}, rows: ooc},
		{match: []string{`container_memory_working_set_bytes{cluster="oo-c"}`}, rows: ooc},
		{match: []string{`container_network_receive_bytes_total{cluster="oo-c"}`}, rows: ooc},
		{match: []string{`container_cpu_cfs_throttled_periods_total{cluster="oo-c"}`}, rows: ooc},
		{match: []string{`kube_pod_container_status_restarts_total{cluster="oo-c"}`}, rows: ooc},
		{match: []string{`node_cpu_seconds_total{cluster="oo-c"}`}, rows: ooc},
		// A store ignoring the matcher answers for another cluster: still missing.
		{match: []string{`kube_horizontalpodautoscaler_status_current_replicas`}, rows: []map[string]string{{"cluster": "oo-b"}}},
	}}
	m, err := ConfirmedNameMatch(context.Background(), store, "Cluster", "oo-c", uidC)
	if err != nil {
		t.Fatal(err)
	}
	want := ClusterMatch{
		By: "name", Dialect: DialectStandard,
		Options: RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "oo-c"}}, Dialect: DialectStandard},
		Label:   "cluster", Value: "oo-c", Name: "oo-c",
		Missing: []Family{FamilyFilesystem, FamilyVolumes, FamilyHPA},
		Warning: NameMatchWarning,
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("match = %+v\nwant  %+v", m, want)
	}

	if _, err := ConfirmedNameMatch(context.Background(), &routeStore{routes: []route{{match: []string{"kube_pod_info"}, rows: []map[string]string{{"cluster": "else"}}}}}, "cluster", "oo-c", uidC); err == nil {
		t.Fatal("name with no data accepted")
	}
}

func TestConfirmedNameMatchCollector(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{`k8s_pod_cpu_time{k8s_cluster="prod-a"}`}, rows: []map[string]string{{"k8s_cluster": "prod-a", "k8s_cluster_uid": uidA}}},
	}}
	m, err := ConfirmedNameMatch(context.Background(), store, "k8s_cluster", "prod-a", uidA)
	if err != nil {
		t.Fatal(err)
	}
	want := RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: "prod-a"}}, Dialect: DialectCollector, CollectorUID: uidA, GatewayMetricsAllowed: true}
	if m.Dialect != DialectCollector || !reflect.DeepEqual(m.Options, want) || m.By != "name" {
		t.Fatalf("match = %+v", m)
	}

	shared := &routeStore{routes: []route{
		{match: []string{`k8s_pod_cpu_time{k8s_cluster="prod-a"}`}, rows: []map[string]string{{"k8s_cluster": "prod-a", "k8s_cluster_uid": uidA}, {"k8s_cluster": "prod-a", "k8s_cluster_uid": uidB}}},
	}}
	if _, err := ConfirmedNameMatch(context.Background(), shared, "k8s_cluster", "prod-a", uidA); err == nil {
		t.Fatal("name shared by two collectors accepted")
	}

	// A cluster rebuilt under the same name: only this collector sends it
	// now, but the old one sent it this week, so its name-only series are
	// still in the span charts read. The match stands without them.
	rebuilt := &routeStore{routes: []route{
		{match: []string{`k8s_pod_cpu_time{k8s_cluster="prod-a"}`, "[15m]"}, rows: []map[string]string{{"k8s_cluster": "prod-a", "k8s_cluster_uid": uidA}}},
		{match: []string{`k8s_pod_cpu_time{k8s_cluster="prod-a"}`, "[7d]"}, rows: []map[string]string{{"k8s_cluster": "prod-a", "k8s_cluster_uid": uidA}, {"k8s_cluster": "prod-a", "k8s_cluster_uid": uidB}}},
	}}
	rm, err := ConfirmedNameMatch(context.Background(), rebuilt, "k8s_cluster", "prod-a", uidA)
	if err != nil {
		t.Fatalf("rebuilt cluster's name refused: %v", err)
	}
	if rm.Options.GatewayMetricsAllowed || !strings.Contains(rm.Warning, "in the last 7 days") {
		t.Fatalf("rebuilt cluster's match = %+v", rm)
	}
	if strings.Contains(m.Warning, "in the last 7 days") {
		t.Fatalf("a name only this collector sent warned about sharing: %q", m.Warning)
	}

	// The name's only collector reports another cluster's ID.
	if _, err := ConfirmedNameMatch(context.Background(), store, "k8s_cluster", "prod-a", uidB); !errors.Is(err, ErrNameBelongsToOtherCluster) {
		t.Fatalf("other cluster's name err = %v", err)
	}
	// An agent that sends no ID can still be confirmed by name.
	noUID := &routeStore{routes: []route{
		{match: []string{`k8s_pod_cpu_time{k8s_cluster="prod-a"}`}, rows: []map[string]string{{"k8s_cluster": "prod-a"}}},
	}}
	nm, err := ConfirmedNameMatch(context.Background(), noUID, "k8s_cluster", "prod-a", uidB)
	if err != nil {
		t.Fatalf("collector without ID refused: %v", err)
	}
	if !strings.Contains(nm.Warning, CollectorNoIDWarning) {
		t.Fatalf("collector without ID warning = %q", nm.Warning)
	}
	if strings.Contains(m.Warning, CollectorNoIDWarning) {
		t.Fatalf("collector with ID warned about a missing ID: %q", m.Warning)
	}
}

func collectorFamilyStore(gateway bool) *routeStore {
	a := []map[string]string{{"k8s_cluster": "cluster1"}}
	routes := []route{
		{match: []string{"container_cpu_time{"}, rows: a},
		{match: []string{"container_memory_working_set{"}, rows: a},
		{match: []string{"k8s_pod_network_io{"}, rows: a},
	}
	if gateway {
		routes = append(routes,
			route{match: []string{"container_fs_writes_bytes_total{"}, rows: a},
			route{match: []string{"kube_pod_container_status_restarts_total{"}, rows: a},
			route{match: []string{"kube_horizontalpodautoscaler_status_current_replicas{"}, rows: a},
		)
	}
	return &routeStore{routes: routes}
}

func TestDetectFamiliesCollector(t *testing.T) {
	got, err := DetectFamilies(context.Background(), collectorFamilyStore(false), collectorOptsA)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Family{FamilyThrottling, FamilyFilesystem, FamilyRestarts, FamilyVolumes, FamilyNodes, FamilyHPA}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gateway refused: missing = %v", got)
	}

	allowed := collectorOptsA
	allowed.GatewayMetricsAllowed = true
	store := collectorFamilyStore(true)
	got, err = DetectFamilies(context.Background(), store, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Family{FamilyThrottling, FamilyVolumes, FamilyNodes}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gateway allowed: missing = %v", got)
	}
	for _, q := range store.sent() {
		assertEverySelectorScoped(t, q, "k8s_cluster", "cluster1")
	}

	// A collector configured to keep throttling has it.
	store = collectorFamilyStore(true)
	store.routes = append(store.routes, route{match: []string{"container_cpu_cfs_throttled_periods_total{"}, rows: []map[string]string{{"k8s_cluster": "cluster1"}}})
	got, err = DetectFamilies(context.Background(), store, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Family{FamilyVolumes, FamilyNodes}; !reflect.DeepEqual(got, want) {
		t.Fatalf("throttling kept: missing = %v", got)
	}
}

func TestDetectFamiliesConcurrencyCap(t *testing.T) {
	store := &routeStore{delay: 20 * time.Millisecond}
	if _, err := DetectFamilies(context.Background(), store, clusterOpts); err != nil {
		t.Fatal(err)
	}
	if n := store.maxSeen.Load(); n > 4 || n < 2 {
		t.Fatalf("max concurrent queries = %d", n)
	}
	if len(store.sent()) != len(AllFamilies) {
		t.Fatalf("queries = %d", len(store.sent()))
	}
}

func TestLegacyFilterBehaviour(t *testing.T) {
	legacy := &routeStore{routes: []route{{match: []string{"kube_pod_info"}, rows: []map[string]string{{}}}}}
	if got, err := LegacyFilterBehaviour(context.Background(), legacy); err != nil || !got {
		t.Fatalf("legacy = %v, %v", got, err)
	}
	modern := &routeStore{routes: []route{
		{match: []string{"radar_probe_never_written"}},
		{match: []string{"kube_pod_info"}, rows: []map[string]string{{}}},
	}}
	if got, err := LegacyFilterBehaviour(context.Background(), modern); err != nil || got {
		t.Fatalf("modern = %v, %v", got, err)
	}
	// Only collector data: the probe falls through to k8s_pod_cpu_time.
	collectorOnly := &routeStore{routes: []route{{match: []string{"k8s_pod_cpu_time{radar_probe_never_written"}, rows: []map[string]string{{}}}}}
	if got, err := LegacyFilterBehaviour(context.Background(), collectorOnly); err != nil || !got {
		t.Fatalf("collector only = %v, %v", got, err)
	}
	if _, err := LegacyFilterBehaviour(context.Background(), &routeStore{}); !errors.Is(err, ErrNoProbeData) {
		t.Fatalf("empty store err = %v", err)
	}
}

type flakyStore struct {
	calls int
	fails int
	err   error
}

func (f *flakyStore) Do(context.Context, string, string, url.Values) ([]byte, error) {
	f.calls++
	if f.calls <= f.fails {
		return nil, f.err
	}
	return vectorBody([]map[string]string{{}}), nil
}

func (f *flakyStore) Address() string { return "flaky" }

func TestDiscoveryRetriesTransportError(t *testing.T) {
	busy := &prom.HTTPError{StatusCode: 400, Body: []byte(`{"errorType":"bad_data","error":"transport error"}`)}
	ok := &flakyStore{fails: 2, err: busy}
	if got, err := LegacyFilterBehaviour(context.Background(), ok); err != nil || !got || ok.calls != 3 {
		t.Fatalf("got %v, %v after %d calls", got, err, ok.calls)
	}
	down := &flakyStore{fails: 100, err: busy}
	if _, err := LegacyFilterBehaviour(context.Background(), down); err == nil || down.calls != 3 {
		t.Fatalf("err %v after %d calls", err, down.calls)
	}
	bad := &flakyStore{fails: 100, err: &prom.HTTPError{StatusCode: 400, Body: []byte(`{"error":"parse error"}`)}}
	if _, err := LegacyFilterBehaviour(context.Background(), bad); err == nil || bad.calls != 1 {
		t.Fatalf("non-retryable: err %v after %d calls", err, bad.calls)
	}
}

func TestListClusterNames(t *testing.T) {
	store := &routeStore{routes: []route{
		{match: []string{"count by (team, cluster,", "kube_pod_info[15m]"}, rows: []map[string]string{
			{"cluster": "oo-c"},
			{"cluster": "oo-b", "cluster_name": "b-name"},
			{"team": "payments"},
			{"cluster": "oo-c"},
			{},
		}},
		{match: []string{"count by (k8s_cluster)", "k8s_pod_cpu_time[15m]"}, rows: []map[string]string{
			{"k8s_cluster": "cluster1"},
			{"k8s_cluster": ""},
		}},
	}}
	got, err := ListClusterNames(context.Background(), store, "Team")
	if err != nil {
		t.Fatal(err)
	}
	want := []LabelValue{
		{"cluster_name", "b-name"},
		{"k8s_cluster", "cluster1"},
		{"cluster", "oo-b"},
		{"cluster", "oo-c"},
		{"team", "payments"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %+v, want %+v", got, want)
	}

	var many []map[string]string
	for i := range MaxClusterNames + 10 {
		many = append(many, map[string]string{"cluster": fmt.Sprintf("c%03d", i)})
	}
	capped, err := ListClusterNames(context.Background(), &routeStore{routes: []route{{match: []string{"kube_pod_info"}, rows: many}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != MaxClusterNames || capped[0].Value != "c000" {
		t.Fatalf("capped = %d names, first %+v", len(capped), capped[0])
	}

	if _, err := ListClusterNames(context.Background(), &routeStore{}, `bad"label`); err == nil {
		t.Fatal("bad override label accepted")
	}
	failing := &routeStore{routes: []route{{match: []string{"k8s_pod_cpu_time"}, err: errors.New("boom")}}}
	if _, err := ListClusterNames(context.Background(), failing, ""); err == nil {
		t.Fatal("query error swallowed")
	}
}
