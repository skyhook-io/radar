package openobserve

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/openobserve/oofake"
	"github.com/skyhook-io/radar/internal/prometheus"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

const (
	uidB = "7b930c6c-dcbb-4d1f-9afd-67ea890873d7"
	uidC = "dd1bfeff-e60c-4827-b8e0-b822ab22a9e8"
)

func kubeWithUID(uid string, pods ...*corev1.Pod) *fake.Clientset {
	objs := []any{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}}
	for _, p := range pods {
		objs = append(objs, p)
	}
	c := fake.NewClientset()
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Namespace:
			_ = c.Tracker().Add(v)
		case *corev1.Pod:
			_ = c.Tracker().Add(v)
		}
	}
	return c
}

type testActivator struct {
	*Activator
	done  chan struct{}
	mu    sync.Mutex
	saved map[string]config.OpenObserveMatch
}

func newTestActivator(t *testing.T, cfg *config.OpenObserveConfig, inCluster bool, client *prometheus.Client) *testActivator {
	t.Helper()
	ta := &testActivator{Activator: NewConnectionActivator(client, client.Lifetime()), done: make(chan struct{}, 8), saved: map[string]config.OpenObserveMatch{}}
	ta.loadConfig = func() *config.OpenObserveConfig {
		if cfg == nil {
			return nil
		}
		c := cfg.Clone()
		return &c
	}
	ta.inCluster = func() bool { return inCluster }
	ta.saveIDMatch = func(_ config.OpenObserveConfig, uid string, m config.OpenObserveMatch) {
		ta.mu.Lock()
		ta.saved[uid] = m
		ta.mu.Unlock()
	}
	ta.retryAfter = time.Hour
	ta.Activator.done = func() { ta.done <- struct{}{} }
	t.Cleanup(ta.Clear)
	return ta
}

func (ta *testActivator) wait(t *testing.T) {
	t.Helper()
	select {
	case <-ta.done:
	case <-time.After(10 * time.Second):
		t.Fatal("match did not finish")
	}
}

// entryFor is a saved connection with an ID match stored for each of uids.
func entryFor(url string, uids ...string) *config.OpenObserveConfig {
	cfg := &config.OpenObserveConfig{BaseURL: url, Org: "default", User: "sa@example.com", Token: "secret"}
	for _, uid := range uids {
		if cfg.Matches == nil {
			cfg.Matches = map[string]config.OpenObserveMatch{}
		}
		cfg.Matches[uid] = config.OpenObserveMatch{Label: oo.IDLabel, Value: uid, By: config.OpenObserveMatchByID}
	}
	return cfg
}

func TestIDMatchRoutesEveryQueryIncludingFreeFormThroughTheForcedFilter(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	f := &oofake.Server{IDs: map[string]string{uidB: "oo-b"}}
	srv := oofake.Start(t, f)
	cfg := entryFor(srv.URL, uidB)
	// A stale cached label is refreshed; the context name stays.
	cfg.Matches[uidB] = config.OpenObserveMatch{Label: "k8s_cluster_uid", Value: uidB, By: config.OpenObserveMatchByID, Context: "kind-oo-b"}
	ta := newTestActivator(t, cfg, false, client)

	ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
	ta.wait(t)

	st := client.GetStatus()
	if st.Source == nil || st.Source.Kind != "openobserve" || st.Source.Via != "local" || st.Source.MatchedBy != "id" {
		t.Fatalf("status source = %+v", st.Source)
	}
	if st.Source.ClusterLabel == nil || st.Source.ClusterLabel.Name != "radar_cluster_uid" || st.Source.ClusterLabel.Value != uidB {
		t.Fatalf("cluster label = %+v", st.Source.ClusterLabel)
	}
	if !strings.Contains(st.Address, "/api/default/prometheus") || strings.Contains(st.Address, "secret") {
		t.Fatalf("address = %q", st.Address)
	}
	if got := ta.saved[uidB]; got.By != config.OpenObserveMatchByID || got.Label != oo.IDLabel || got.Value != uidB {
		t.Fatalf("ID match label not refreshed: %+v", got)
	}

	// The AI's free-form PromQL goes through PromForMCP.
	before := len(f.Recorded())
	res, err := client.PromForMCP().Query(context.Background(), `sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="demo"}[5m]))`)
	if err != nil {
		t.Fatal(err)
	}
	sent := f.Recorded()[before:]
	if len(sent) != 1 || !strings.Contains(sent[0], `radar_cluster_uid="`+uidB+`"`) {
		t.Fatalf("free-form query reached OpenObserve without the cluster filter: %q", sent)
	}
	for _, s := range res.Series {
		if s.Labels["radar_cluster_uid"] != uidB {
			t.Fatalf("another cluster's series reached the caller: %v", s.Labels)
		}
	}
	// A user-supplied matcher on the cluster label can't widen the scope:
	// the forced equality matcher is still there beside it.
	before = len(f.Recorded())
	_, _ = client.PromForMCP().Query(context.Background(), `up{radar_cluster_uid=~".+"}`)
	if sent := f.Recorded()[before:]; len(sent) != 1 || !strings.Contains(sent[0], `radar_cluster_uid="`+uidB+`"`) {
		t.Fatalf("query sent without the forced matcher: %q", sent)
	}
}

// A cluster OpenObserve has data for is never used without a stored match,
// and gets no source note, not even a passing "checking" one.
func TestClusterWithoutStoredMatchNeverUsesOpenObserve(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   func(url string) *config.OpenObserveConfig
		async bool
	}{
		{name: "no matches saved", cfg: func(url string) *config.OpenObserveConfig { return entryFor(url) }},
		{name: "another cluster matched", cfg: func(url string) *config.OpenObserveConfig { return entryFor(url, uidC) }, async: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
			t.Cleanup(func() { _ = client.Close() })
			f := &oofake.Server{IDs: map[string]string{uidB: "oo-b"}}
			srv := oofake.Start(t, f)
			ta := newTestActivator(t, tc.cfg(srv.URL), false, client)
			target := Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)}

			for range 2 { // UID unknown, then remembered
				ta.Apply(target)
				if st := client.GetStatus(); st.Source != nil {
					t.Fatalf("source = %+v, want none", st.Source)
				}
				if tc.async {
					ta.wait(t)
					tc.async = false // the second Apply knows the UID
				} else {
					select {
					case <-ta.done:
						t.Fatal("a background match ran for a cluster without a stored match")
					case <-time.After(200 * time.Millisecond):
					}
				}
				if client.RelayActive() {
					t.Fatal("a cluster without a stored match must not use OpenObserve")
				}
				if st := client.GetStatus(); st.Source != nil {
					t.Fatalf("source = %+v, want none", st.Source)
				}
			}
			if sent := f.Recorded(); len(sent) != 0 {
				t.Fatalf("OpenObserve was asked about an unmatched cluster: %q", sent)
			}
		})
	}
}

func TestStoredNameMatchActivates(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	srv := oofake.Start(t, &oofake.Server{PodCluster: "oo-c"})
	cfg := entryFor(srv.URL)
	cfg.Matches = map[string]config.OpenObserveMatch{uidC: {Label: "cluster", Value: "oo-c", By: config.OpenObserveMatchByName, Context: "kind-oo-c"}}
	ta := newTestActivator(t, cfg, false, client)

	ta.Apply(Target{Context: "kind-oo-c", Fingerprint: "fp-c", Client: kubeWithUID(uidC)})
	ta.wait(t)

	st := client.GetStatus()
	if !client.RelayActive() || st.Source == nil || st.Source.MatchedBy != "name" || st.Source.ClusterLabel == nil || st.Source.ClusterLabel.Value != "oo-c" {
		t.Fatalf("status source = %+v", st.Source)
	}
	if len(ta.saved) != 0 {
		t.Fatalf("a name match saved an ID entry: %+v", ta.saved)
	}
}

func TestFailedClusterUIDReadIsRetried(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{uidB: "oo-b"}})
	cfg := entryFor(srv.URL, uidB)
	cfg.Matches[uidB] = config.OpenObserveMatch{Label: oo.IDLabel, Value: uidB, By: config.OpenObserveMatchByID, Context: "kind-oo-b"}
	ta := newTestActivator(t, cfg, false, client)
	ta.retryAfter = 200 * time.Millisecond

	kube := kubeWithUID(uidB)
	var reads atomic.Int32
	kube.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		if reads.Add(1) == 1 {
			return true, nil, errors.New("apiserver timeout")
		}
		return false, nil, nil
	})

	ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kube})
	ta.wait(t)
	if st := client.GetStatus(); client.RelayActive() || st.Source == nil || !strings.Contains(st.Source.Error, "Couldn't read this cluster's ID") {
		t.Fatalf("after the failed read: relay=%v source=%+v", client.RelayActive(), st.Source)
	}
	ta.wait(t)
	if st := client.GetStatus(); !client.RelayActive() || st.Source == nil || st.Source.MatchedBy != "id" {
		t.Fatalf("after the retry: relay=%v source=%+v", client.RelayActive(), st.Source)
	}
}

func TestStoredIDMatchWithoutDataSaysSo(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	srv := oofake.Start(t, &oofake.Server{})
	ta := newTestActivator(t, entryFor(srv.URL, uidC), false, client)

	ta.Apply(Target{Context: "kind-oo-c", Fingerprint: "fp-c", Client: kubeWithUID(uidC)})
	if st := client.GetStatus(); st.Source != nil {
		t.Fatalf("source before the UID is read = %+v, want none", st.Source)
	}
	ta.wait(t)

	if client.RelayActive() {
		t.Fatal("no data for the ID: OpenObserve must not serve")
	}
	st := client.GetStatus()
	if st.Source == nil || !strings.Contains(st.Source.Error, "no metrics with this cluster's ID") {
		t.Fatalf("source = %+v", st.Source)
	}
}

func TestUnreachableOpenObserveForAMatchedContextNeverFallsBackToPrometheus(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	var promHits atomic.Int32
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		promHits.Add(1)
		oofake.WriteVector(w, nil)
	}))
	t.Cleanup(promSrv.Close)
	client.Configure(promSrv.URL, nil)

	srv := oofake.Start(t, &oofake.Server{FailStatus: http.StatusBadGateway, FailBody: "down"})
	cfg := entryFor(srv.URL)
	cfg.Matches = map[string]config.OpenObserveMatch{uidB: {Label: "radar_cluster_uid", Value: uidB, By: config.OpenObserveMatchByID}}
	ta := newTestActivator(t, cfg, false, client)

	ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
	ta.wait(t)

	st := client.GetStatus()
	if st.Source == nil || !st.Source.Unavailable || !strings.HasPrefix(st.Source.Error, UnavailablePrefix) {
		t.Fatalf("source = %+v", st.Source)
	}
	hits := promHits.Load()
	_, err := client.PromForMCP().Query(context.Background(), "up")
	if err == nil || !strings.HasPrefix(err.Error(), UnavailablePrefix) {
		t.Fatalf("query error = %v, want OpenObserve unavailable", err)
	}
	if promHits.Load() != hits {
		t.Fatal("a query fell back to Prometheus")
	}
}

// A matched cluster uses OpenObserve even with a Prometheus URL set by hand;
// the URL stays configured underneath and serves again once the match goes.
func TestMatchedClusterWinsOverHandSetURL(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	var promHits atomic.Int32
	promSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		promHits.Add(1)
		oofake.WriteVector(w, nil)
	}))
	t.Cleanup(promSrv.Close)
	// What local activation does for a context with a URL set by hand.
	client.Configure(promSrv.URL, map[string]string{"X-Scope-OrgID": "team"})

	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{uidB: "oo-b"}})
	ta := newTestActivator(t, entryFor(srv.URL, uidB), false, client)
	ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
	ta.wait(t)
	if !client.RelayActive() {
		t.Fatal("matched cluster should use OpenObserve")
	}
	if st := client.GetStatus(); !strings.Contains(st.Address, "OpenObserve") {
		t.Fatalf("address = %q", st.Address)
	}
	if _, err := client.PromForMCP().Query(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}

	// A later save of the hand-set URL keeps the relay.
	client.Configure(promSrv.URL+"/v2", nil)
	if !client.RelayActive() {
		t.Fatal("configuring Prometheus ended the OpenObserve relay")
	}
	if _, err := client.PromForMCP().Query(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	if promHits.Load() != 0 {
		t.Fatal("a query reached the hand-set Prometheus while OpenObserve serves")
	}

	ta.Clear()
	if url, _ := client.CurrentConfig(); url != promSrv.URL+"/v2" {
		t.Fatalf("configured URL after the match went = %q", url)
	}
	if st := client.GetStatus(); st.Source != nil {
		t.Fatalf("source = %+v, want none", st.Source)
	}
}

func TestCachedDecisionAppliesWithoutAskingOpenObserveAgain(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	f := &oofake.Server{IDs: map[string]string{uidB: "oo-b"}}
	srv := oofake.Start(t, f)
	ta := newTestActivator(t, entryFor(srv.URL, uidB), false, client)
	target := Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)}
	ta.Apply(target)
	ta.wait(t)
	ta.Clear()
	n := len(f.Recorded())

	ta.Apply(target)
	if !client.RelayActive() {
		t.Fatal("cached match should apply at once")
	}
	if len(f.Recorded()) != n {
		t.Fatal("cached match asked OpenObserve again")
	}
}

func TestInClusterIgnoresTheEntryAndLogsOnce(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	srv := oofake.Start(t, &oofake.Server{IDs: map[string]string{uidB: "oo-b"}})
	ta := newTestActivator(t, entryFor(srv.URL), true, client)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	ta.Apply(Target{Context: "in-cluster", Client: kubeWithUID(uidB)})
	ta.Apply(Target{Context: "in-cluster", Client: kubeWithUID(uidB)})
	ta.LogIfIgnored()

	if n := strings.Count(buf.String(), IgnoredInClusterMessage); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, buf.String())
	}
	if client.RelayActive() || client.GetStatus().Source != nil {
		t.Fatal("in-cluster Radar must not configure OpenObserve")
	}
}

func TestLegacyStoreRefusesUnscopableMetricsAsNotCollected(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	f := &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, Legacy: true, LegacyUnscoped: []string{"k8s_pod_cpu_usage"}}
	srv := oofake.Start(t, f)
	ta := newTestActivator(t, entryFor(srv.URL, uidB), false, client)

	ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
	ta.wait(t)
	st := client.GetStatus()
	if st.Source == nil || st.Source.VersionWarning != oo.VersionWarning {
		t.Fatalf("status source = %+v", st.Source)
	}
	for _, m := range st.Source.MissingFamilies {
		if m == "cpu" {
			t.Fatalf("missing families = %v", st.Source.MissingFamilies)
		}
	}

	mcp := client.PromForMCP()
	before := len(f.Recorded())
	res, err := mcp.Query(context.Background(), `sum(k8s_pod_cpu_usage)`)
	if err != nil || len(res.Series) != 0 {
		t.Fatalf("unscopable metric: %+v, %v", res, err)
	}
	if sent := f.Recorded()[before:]; len(sent) != 1 || sent[0] != oofake.SchemaQuery("k8s_pod_cpu_usage") {
		t.Fatalf("unscopable query reached OpenObserve: %q", sent)
	}

	before = len(f.Recorded())
	if _, err := mcp.Query(context.Background(), `sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="demo"}[5m]))`); err != nil {
		t.Fatal(err)
	}
	sent := f.Recorded()[before:]
	if len(sent) != 2 || sent[0] != oofake.SchemaQuery("container_cpu_usage_seconds_total") || !strings.Contains(sent[1], `radar_cluster_uid="`+uidB+`"`) {
		t.Fatalf("scopable query: %q", sent)
	}
}

// The scoping check stays on unless the store is positively known to
// filter correctly: a version check that fails (one 429) or finds no data
// to decide with keeps it on, without the upgrade warning.
func TestGuardStaysOnWhenVersionUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *oofake.Server
		// detection429 answers the version check's query with 429.
		detection429 bool
	}{
		{name: "rate limited", f: &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, Legacy: true, LegacyUnscoped: []string{"k8s_pod_cpu_usage"}}, detection429: true},
		{name: "no data", f: &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, Undetectable: true, LegacyUnscoped: []string{"k8s_pod_cpu_usage"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
			t.Cleanup(func() { _ = client.Close() })
			srv := oofake.Start(t, tc.f)
			addr := srv.URL
			if tc.detection429 {
				target, _ := url.Parse(srv.URL)
				proxy := httputil.NewSingleHostReverseProxy(target)
				front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.RawQuery, "radar_probe_never_written") {
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					proxy.ServeHTTP(w, r)
				}))
				t.Cleanup(front.Close)
				addr = front.URL
			}
			ta := newTestActivator(t, entryFor(addr, uidB), false, client)
			ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
			ta.wait(t)
			st := client.GetStatus()
			if st.Source == nil || st.Source.VersionWarning != "" {
				t.Fatalf("status source = %+v", st.Source)
			}
			mcp := client.PromForMCP()
			before := len(tc.f.Recorded())
			res, err := mcp.Query(context.Background(), `sum(k8s_pod_cpu_usage)`)
			if err != nil || len(res.Series) != 0 {
				t.Fatalf("unscopable metric: %+v, %v", res, err)
			}
			if sent := tc.f.Recorded()[before:]; len(sent) != 1 || sent[0] != oofake.SchemaQuery("k8s_pod_cpu_usage") {
				t.Fatalf("sent = %q", sent)
			}
			if _, err := mcp.Query(context.Background(), `sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="demo"}[5m]))`); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A store detected as filtering correctly gets no schema lookups.
func TestGuardOffWhenStoreFilters(t *testing.T) {
	client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
	t.Cleanup(func() { _ = client.Close() })
	f := &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, LegacyUnscoped: []string{"k8s_pod_cpu_usage"}}
	srv := oofake.Start(t, f)
	ta := newTestActivator(t, entryFor(srv.URL, uidB), false, client)
	ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
	ta.wait(t)
	before := len(f.Recorded())
	if _, err := client.PromForMCP().Query(context.Background(), `sum(k8s_pod_cpu_usage)`); err != nil {
		t.Fatal(err)
	}
	if sent := f.Recorded()[before:]; len(sent) != 1 || strings.HasPrefix(sent[0], "schema:") {
		t.Fatalf("sent = %q", sent)
	}
}

// The store's version decides the guards. OpenObserve v0.14 answers a label
// lookup from every series of the metric and its instant probe can come back
// empty, so the version, not the probe, turns the guards on; v1.2 needs
// neither; a version that can't be read keeps label lookups refused.
func TestGuardsFollowStoreVersion(t *testing.T) {
	for _, tc := range []struct {
		name          string
		f             *oofake.Server
		legacyWarning bool
		labelsAllowed bool
	}{
		{name: "v0.14.7", f: &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, PublicVersion: "v0.14.7", Undetectable: true}, legacyWarning: true},
		{name: "v1.1.2", f: &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, Version: "v1.1.2"}, legacyWarning: true, labelsAllowed: true},
		{name: "v1.2.0", f: &oofake.Server{IDs: map[string]string{uidB: "oo-b"}, Version: "v1.2.0", Legacy: true}, labelsAllowed: true},
		{name: "unknown, probe says current", f: &oofake.Server{IDs: map[string]string{uidB: "oo-b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := prometheus.NewClient(fake.NewClientset(), nil, "test", nil, false, nil)
			t.Cleanup(func() { _ = client.Close() })
			srv := oofake.Start(t, tc.f)
			ta := newTestActivator(t, entryFor(srv.URL, uidB), false, client)
			ta.Apply(Target{Context: "kind-oo-b", Fingerprint: "fp-b", Client: kubeWithUID(uidB)})
			ta.wait(t)
			st := client.GetStatus()
			if st.Source == nil || (st.Source.VersionWarning == oo.VersionWarning) != tc.legacyWarning {
				t.Fatalf("status source = %+v", st.Source)
			}
			before := len(tc.f.Recorded())
			end := time.Now()
			values, err := client.PromForMCP().LabelValues(context.Background(), "pod", []string{`kube_pod_info{namespace="demo"}`}, end.Add(-time.Hour), end, 10)
			if tc.labelsAllowed {
				if err != nil || len(values) != 1 {
					t.Fatalf("label values = %v, %v", values, err)
				}
				return
			}
			if !errors.Is(err, oo.ErrLabelLookupsUnscoped) {
				t.Fatalf("label values err = %v", err)
			}
			if sent := tc.f.Recorded()[before:]; len(sent) != 0 {
				t.Fatalf("label lookup reached OpenObserve: %q", sent)
			}
		})
	}
}
