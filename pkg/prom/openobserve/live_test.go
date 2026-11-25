package openobserve

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// The live tests run against the phase 0 environment (clusters A collector,
// B remote write with radar_cluster_uid, C remote write named cluster1, demo
// app in namespace demo) only when OPENOBSERVE_LIVE_URL is set:
//
//	OPENOBSERVE_LIVE_URL=http://localhost:5080 OPENOBSERVE_LIVE_USER=... OPENOBSERVE_LIVE_TOKEN=... \
//	OPENOBSERVE_LIVE_LEGACY=true go test ./prom/openobserve -run Live
//
// OPENOBSERVE_LIVE_ORG defaults to "default"; OPENOBSERVE_LIVE_LEGACY (true
// for v1.1.x, false for v1.2+) enables the filter-behaviour check.
func liveTransport(t *testing.T) *prom.HTTPTransport {
	t.Helper()
	base := os.Getenv("OPENOBSERVE_LIVE_URL")
	if base == "" {
		t.Skip("OPENOBSERVE_LIVE_URL not set")
	}
	org := os.Getenv("OPENOBSERVE_LIVE_ORG")
	if org == "" {
		org = "default"
	}
	conn := Connection{BaseURL: base, Org: org, User: os.Getenv("OPENOBSERVE_LIVE_USER"), Token: os.Getenv("OPENOBSERVE_LIVE_TOKEN")}
	tr, err := conn.NewHTTPTransport(20 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestLiveCollectorPodCPU(t *testing.T) {
	raw := liveTransport(t)
	legacy, err := LegacyFilterBehaviour(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	// A's collector name changes with the e2e run (cluster1 or oo-a), so
	// its scope comes from discovery.
	ids, err := DiscoverIDs(context.Background(), raw, []string{uidA})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := ids[uidA]
	if !ok {
		t.Fatal("A not discovered")
	}
	// As local Radar runs it: metrics checked first on an older store.
	rt := &RewritingTransport{Inner: raw, Options: a.Options, LegacyStore: legacy, SchemaFetcher: NewSchemaFetcher(raw)}
	q := `sum(rate(container_cpu_usage_seconds_total{container!='',namespace='demo'}[5m])) by (pod,namespace)`
	res, err := prom.NewClient(rt).Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	pods := map[string]bool{}
	for _, s := range res.Series {
		if s.Labels["namespace"] != "demo" || s.Labels["pod"] == "" || s.Labels["k8s_cluster"] != a.Name {
			t.Errorf("series labels %v", s.Labels)
		}
		pods[s.Labels["pod"]] = true
	}
	if !pods["db-0"] || len(pods) < 3 {
		t.Fatalf("pods = %v", pods)
	}
	t.Logf("cluster A demo pods: %v", pods)

	// The refused families stay refused against the real store.
	if !a.Options.GatewayMetricsAllowed {
		if _, err := prom.NewClient(rt).Query(context.Background(), `kube_pod_info{namespace="demo"}`); err == nil {
			t.Fatal("gateway metric answered while refused")
		}
	}
}

func TestLiveDiscoverIDs(t *testing.T) {
	raw := liveTransport(t)
	got, err := DiscoverIDs(context.Background(), raw, []string{uidA, uidB, uidC})
	if err != nil {
		t.Fatal(err)
	}
	// The e2e run renames A's collector (cluster1 or oo-a) and may add
	// radar_cluster_uid to C, so only invariants are checked.
	b, a := got[uidB], got[uidA]
	if b.By != "id" || b.Dialect != DialectStandard || b.Label != IDLabel || b.Value != uidB {
		t.Fatalf("B = %+v", b)
	}
	if a.By != "id" || a.Dialect != DialectCollector || a.Label != collectorUIDLabel || a.Value != uidA || a.Options.CollectorUID != uidA || a.Name == "" {
		t.Fatalf("A = %+v", a)
	}
	usage, err := collectorNameUsage(context.Background(), raw, "")
	if err != nil {
		t.Fatal(err)
	}
	unique := a.Name != UnsafeDefaultName && len(usage[a.Name]) == 1 && usage[a.Name][uidA]
	if a.Options.GatewayMetricsAllowed != unique {
		t.Fatalf("A (name %q, used by %v): gateway metrics allowed = %v, want %v", a.Name, usage[a.Name], a.Options.GatewayMetricsAllowed, unique)
	}
	if c, ok := got[uidC]; ok && (c.By != "id" || c.Label != IDLabel || c.Value != uidC || c.Options.Matchers[0].Value != uidC) {
		t.Fatalf("C matched, but not by its own ID: %+v", c)
	}
	t.Logf("A name %q (gateway metrics allowed %v); C matched by ID: %v", a.Name, unique, got[uidC].Value != "")
	missing, err := DetectFamilies(context.Background(), raw, a.Options)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A missing: %v; B: %+v", missing, b)
	for _, f := range []Family{FamilyCPU, FamilyMemory, FamilyNetwork} {
		for _, m := range missing {
			if m == f {
				t.Errorf("A reports %s missing", f)
			}
		}
	}
}

func TestLiveSuggestNameForC(t *testing.T) {
	raw := liveTransport(t)
	// C's demo Pods, read the way the agent would list them.
	res, err := prom.NewClient(raw).Query(context.Background(), `max by (pod, uid, cluster) (kube_pod_info{namespace="demo",cluster="cluster1"})`)
	if err != nil {
		t.Fatal(err)
	}
	sample := PodSample{Namespace: "demo"}
	for _, s := range res.Series {
		if s.Labels["cluster"] == "cluster1" && s.Labels["uid"] != "" {
			sample.Pods = append(sample.Pods, PodRef{Name: s.Labels["pod"], UID: s.Labels["uid"]})
		}
	}
	if len(sample.Pods) == 0 {
		t.Fatal("no Pods for cluster C")
	}
	sug, err := SuggestName(context.Background(), raw, sample, "")
	if err != nil {
		t.Fatal(err)
	}
	if sug.Label != "cluster" || sug.Value != "cluster1" || !sug.UnsafeDefault || sug.Conflict {
		t.Fatalf("suggestion = %+v", sug)
	}
	t.Logf("C suggestion: %+v", sug)
	if _, err := ConfirmedNameMatch(context.Background(), raw, sug.Label, sug.Value, uidC); err == nil {
		t.Fatal("cluster1 confirmed")
	}
	m, err := ConfirmedNameMatch(context.Background(), raw, "cluster", "oo-b", uidB)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("oo-b by name: %+v", m)
}

func TestLiveLegacyFilterBehaviour(t *testing.T) {
	raw := liveTransport(t)
	want := os.Getenv("OPENOBSERVE_LIVE_LEGACY")
	if want == "" {
		t.Skip("OPENOBSERVE_LIVE_LEGACY not set")
	}
	got, err := LegacyFilterBehaviour(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != (want == "true") {
		t.Fatalf("legacy = %v, want %s", got, want)
	}
}

// TestLiveLegacyStoreEscapes runs the review's escape queries for cluster B
// (radar_cluster_uid) with LegacyStore set from the store's behaviour, and
// once more forced on: nothing from clusters A or C may come back, and
// cluster B's ordinary chart queries must still answer.
func TestLiveLegacyStoreEscapes(t *testing.T) {
	raw := liveTransport(t)
	ctx := context.Background()
	detected, err := LegacyFilterBehaviour(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("legacy filter behaviour: %v", detected)
	optsB := RewriteOptions{Matchers: []Matcher{{Name: IDLabel, Value: uidB}}}
	// OpenObserve v1.1 needs a time range on label lookups.
	now := time.Now().Unix()
	window := func(match string) url.Values {
		return url.Values{"match[]": {match}, "start": {strconv.FormatInt(now-600, 10)}, "end": {strconv.FormatInt(now, 10)}}
	}
	for _, legacy := range []bool{detected, true} {
		rt := &RewritingTransport{Inner: raw, Options: optsB, LegacyStore: legacy, SchemaFetcher: NewSchemaFetcher(raw)}
		client := prom.NewClient(rt)

		escapes := []string{
			`k8s_pod_cpu_usage{k8s_namespace_name="demo"}`,
			`vector(scalar(count(k8s_pod_cpu_usage)))`,
			`up * 0 + scalar(sum(k8s_pod_cpu_usage))`,
			`count(k8s_pod_cpu_usage)`,
		}
		for _, q := range escapes {
			res, err := client.Query(ctx, q)
			switch {
			case errors.Is(err, ErrUnscopable):
				if !legacy {
					t.Errorf("legacy=%v %s: refused as unscopable on a store treated as current", legacy, q)
				}
			case err != nil:
				t.Errorf("legacy=%v %s: %v", legacy, q, err)
			default:
				for _, s := range res.Series {
					if s.Labels[IDLabel] != uidB {
						t.Errorf("legacy=%v %s: foreign series %v", legacy, q, s.Labels)
					}
					for _, p := range s.DataPoints {
						if p.Value > 0 {
							t.Errorf("legacy=%v %s: value %v from data cluster B doesn't have", legacy, q, p.Value)
						}
					}
				}
			}
		}
		if _, err := client.Query(ctx, `label_replace(k8s_pod_cpu_usage{k8s_namespace_name="demo"}, "radar_cluster_uid", "`+uidB+`", "", "")`); !errors.Is(err, ErrClusterLabelWrite) {
			t.Errorf("legacy=%v label_replace onto the cluster label: %v", legacy, err)
		}

		for _, path := range []string{"/api/v1/label/k8s_cluster_uid/values", "/api/v1/label/k8s_pod_name/values", "/api/v1/series"} {
			body, err := rt.Do(ctx, "GET", path, window("k8s_pod_cpu_usage"))
			if errors.Is(err, ErrUnscopable) {
				continue
			}
			if err != nil {
				t.Errorf("legacy=%v %s: %v", legacy, path, err)
				continue
			}
			if strings.Contains(string(body), uidA) || strings.Contains(string(body), `"web-`) {
				t.Errorf("legacy=%v %s leaked: %.300s", legacy, path, body)
			}
		}
		if legacy {
			if _, err := rt.Do(ctx, "GET", "/api/v1/labels", url.Values{}); !errors.Is(err, ErrUnscopable) {
				t.Errorf("labels without match[] on a legacy store: %v", err)
			}
		}

		// Cluster B's own charts still answer.
		res, err := client.Query(ctx, `sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{namespace="demo",container!=""}[5m]))`)
		if err != nil {
			t.Fatalf("legacy=%v B cpu: %v", legacy, err)
		}
		if len(res.Series) < 3 {
			t.Fatalf("legacy=%v B cpu: %d series", legacy, len(res.Series))
		}
		for _, s := range res.Series {
			if s.Labels[IDLabel] != uidB || s.Labels["namespace"] != "demo" {
				t.Errorf("legacy=%v B cpu series %v", legacy, s.Labels)
			}
		}
		if res, err := client.Query(ctx, `max by (pod) (kube_pod_info{namespace="demo"})`); err != nil || len(res.Series) == 0 {
			t.Fatalf("legacy=%v B kube_pod_info: %v, %v", legacy, res, err)
		}
		body, err := rt.Do(ctx, "GET", "/api/v1/label/pod/values", window(`kube_pod_info{namespace="demo"}`))
		if err != nil || !strings.Contains(string(body), "db-0") {
			t.Fatalf("legacy=%v B pod values: %v %.200s", legacy, err, body)
		}
	}
}

// TestLiveSchemaGuardOldData ports the round-2 review's leaks: on
// OpenObserve v1.1 container_spec_memory_limit_bytes has only a cluster
// column and no data in the last hour, so a time-window check judged it
// scopable while queries over older data returned every cluster's series.
// With the guard on, each is refused or empty on both versions; cluster B's
// ordinary queries keep answering, and with a store detected as current
// (v1.2) the guard is off and they answer too.
func TestLiveSchemaGuardOldData(t *testing.T) {
	raw := liveTransport(t)
	ctx := context.Background()
	fetch := NewSchemaFetcher(raw)
	fields, err := fetch(ctx, "container_spec_memory_limit_bytes")
	if err != nil && !errors.Is(err, ErrStreamNotFound) {
		t.Fatal(err)
	}
	t.Logf("container_spec_memory_limit_bytes columns: %v (err %v)", fields, err)
	fields, err = fetch(ctx, "kube_pod_info")
	if err != nil || !slices.Contains(fields, IDLabel) {
		t.Fatalf("kube_pod_info columns = %v, %v", fields, err)
	}
	if _, err := fetch(ctx, "radar_never_written_metric"); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("missing stream err = %v", err)
	}

	optsB := RewriteOptions{Matchers: []Matcher{{Name: IDLabel, Value: uidB}}}
	now := time.Now()
	unix := func(d time.Duration) string { return strconv.FormatInt(now.Add(-d).Unix(), 10) }
	guarded := &RewritingTransport{Inner: raw, Options: optsB, LegacyStore: true, SchemaFetcher: fetch}

	type req struct {
		path   string
		params url.Values
	}
	leaks := []req{
		{"/api/v1/query", url.Values{"query": {`vector(scalar(count(last_over_time(container_spec_memory_limit_bytes[30d]))))`}}},
		{"/api/v1/query", url.Values{"query": {`vector(scalar(sum(last_over_time(container_spec_memory_limit_bytes[30d]))))`}}},
		{"/api/v1/query", url.Values{"query": {`count(last_over_time(container_spec_memory_limit_bytes[30d]))`}}},
		{"/api/v1/query", url.Values{"query": {`vector(scalar(count(container_spec_memory_limit_bytes offset 30d)))`}}},
		{"/api/v1/query", url.Values{"query": {`vector(scalar(count(container_spec_memory_limit_bytes offset 1d)))`}}},
		{"/api/v1/query", url.Values{"query": {`vector(scalar(max_over_time(count(container_spec_memory_limit_bytes)[7d:1h])))`}}},
		{"/api/v1/query_range", url.Values{"query": {`vector(scalar(count(container_spec_memory_limit_bytes)))`},
			"start": {unix(7 * 24 * time.Hour)}, "end": {unix(0)}, "step": {"3600"}}},
		{"/api/v1/label/pod/values", url.Values{"match[]": {"container_spec_memory_limit_bytes"}, "start": {unix(6 * 24 * time.Hour)}, "end": {unix(0)}}},
		{"/api/v1/series", url.Values{"match[]": {"container_spec_memory_limit_bytes"}, "start": {unix(30 * 24 * time.Hour)}, "end": {unix(0)}}},
	}
	for _, r := range leaks {
		body, err := guarded.Do(ctx, "GET", r.path, r.params)
		if errors.Is(err, ErrUnscopable) {
			continue
		}
		if err != nil {
			t.Errorf("%s %v: %v", r.path, r.params, err)
			continue
		}
		if liveBodyHasData(t, r.path, body) {
			t.Errorf("%s %v leaked: %.300s", r.path, r.params, body)
		}
	}

	// A label column added after older rows were written still filters
	// those rows: an unknown cluster ID over old data returns nothing.
	for _, q := range []string{
		`count(kube_pod_info{radar_cluster_uid="radar-unknown-cluster"} offset 4d)`,
		`count(last_over_time(kube_pod_info{radar_cluster_uid="radar-unknown-cluster"}[30d]))`,
	} {
		res, err := prom.NewClient(raw).Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Series) != 0 {
			t.Errorf("%s: %+v", q, res.Series)
		}
	}

	detected, err := LegacyFilterBehaviour(ctx, raw)
	guardOn := err != nil || detected
	t.Logf("legacy detection: %v, %v; guard on: %v", detected, err, guardOn)
	if want := os.Getenv("OPENOBSERVE_LIVE_LEGACY"); want != "" && guardOn != (want == "true") {
		t.Fatalf("guard on = %v, want %s", guardOn, want)
	}
	for _, rt := range []*RewritingTransport{guarded, {Inner: raw, Options: optsB, LegacyStore: guardOn, SchemaFetcher: fetch}} {
		res, err := prom.NewClient(rt).Query(ctx, `sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{namespace="demo",container!=""}[5m]))`)
		if err != nil || len(res.Series) < 3 {
			t.Fatalf("guard=%v B cpu: %v, %v", rt.LegacyStore, res, err)
		}
		for _, s := range res.Series {
			if s.Labels[IDLabel] != uidB {
				t.Errorf("guard=%v B cpu series %v", rt.LegacyStore, s.Labels)
			}
		}
		res, err = prom.NewClient(rt).QueryRange(ctx, `max by (pod) (kube_pod_info{namespace="demo"})`, now.Add(-7*24*time.Hour), now, time.Hour)
		if err != nil || len(res.Series) == 0 {
			t.Fatalf("guard=%v B kube_pod_info over 7d: %v, %v", rt.LegacyStore, res, err)
		}
		body, err := rt.Do(ctx, "GET", "/api/v1/label/pod/values", url.Values{"match[]": {`kube_pod_info{namespace="demo"}`}, "start": {unix(6 * 24 * time.Hour)}, "end": {unix(0)}})
		if err != nil || !strings.Contains(string(body), "db-0") {
			t.Fatalf("guard=%v B pod values: %v %.200s", rt.LegacyStore, err, body)
		}
	}
}

// liveBodyHasData reports whether a Prometheus answer carries any series,
// label value or positive sample.
func liveBodyHasData(t *testing.T, path string, body []byte) bool {
	t.Helper()
	var doc struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if path != "/api/v1/query" && path != "/api/v1/query_range" {
		var list []json.RawMessage
		_ = json.Unmarshal(doc.Data, &list)
		return len(list) > 0
	}
	var res struct {
		Result []struct {
			Value  []any   `json:"value"`
			Values [][]any `json:"values"`
		} `json:"result"`
	}
	_ = json.Unmarshal(doc.Data, &res)
	for _, r := range res.Result {
		samples := r.Values
		if r.Value != nil {
			samples = append(samples, r.Value)
		}
		for _, s := range samples {
			if len(s) == 2 {
				if v, ok := s[1].(string); ok && v != "0" && v != "NaN" {
					return true
				}
			}
		}
	}
	return false
}

// TestLiveLookupWithoutRange: OpenObserve refuses a label lookup with no time
// range; the transport fills one in.
func TestLiveLookupWithoutRange(t *testing.T) {
	raw := liveTransport(t)
	rt := &RewritingTransport{Inner: raw, Options: RewriteOptions{Matchers: []Matcher{{Name: IDLabel, Value: uidB}}}, SchemaFetcher: NewSchemaFetcher(raw)}
	for _, path := range []string{"/api/v1/labels", "/api/v1/label/namespace/values"} {
		body, err := rt.Do(context.Background(), "GET", path, url.Values{"match[]": {"kube_pod_info"}})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !strings.Contains(string(body), `"success"`) {
			t.Fatalf("%s: %s", path, body)
		}
	}
}
