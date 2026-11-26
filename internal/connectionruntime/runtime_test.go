package connectionruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func installTestBuild(t *testing.T, cfg *rest.Config) {
	t.Helper()
	k8s.RetireServingConnection()
	k8s.SetTestConfig(cfg)
	k8s.SetTestClient(kubernetes.NewForConfigOrDie(cfg))
}

func admitRuntime(t *testing.T, runtime *Runtime) Set {
	t.Helper()
	if !k8s.AdmitTestIntegrations(runtime.Build) {
		t.Fatal("could not admit the fixture")
	}
	set := SetFor(k8s.Current())
	if set.ArgoCD == nil || set.Cost == nil || set.Currency == nil || set.Traffic == nil || set.Helm == nil {
		t.Fatal("the first capture did not see all integration members")
	}
	return set
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatal("barrier was not reached")
		var zero T
		return zero
	}
}

const metricsAnswer = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`

func TestSettingsSwapKeepsUnchangedMembersAndCancelsOldQuery(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	started := make(chan string, 2)
	firstRelease, cleanup := make(chan struct{}), make(chan struct{})
	defer close(cleanup)
	oldStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch q := r.URL.Query().Get("query"); q {
		case "kept", "retired":
			started <- q
			select {
			case <-r.Context().Done():
				return
			case <-cleanup:
				return
			case <-firstRelease:
				if q == "retired" {
					<-r.Context().Done()
					return
				}
			}
		}
		_, _ = io.WriteString(w, metricsAnswer)
	}))
	t.Cleanup(oldStore.Close)
	var nextCalls atomic.Int32
	nextStore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "retired" {
			t.Error("old request followed the replacement")
		}
		nextCalls.Add(1)
		_, _ = io.WriteString(w, metricsAnswer)
	}))
	t.Cleanup(nextStore.Close)
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	runtime := New(nil, Settings{Metrics: prom.Connection{URL: oldStore.URL}})
	old := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	conn, id := k8s.Current(), k8s.ServingConnectionID()

	query := func(q string) <-chan error {
		result := make(chan error, 1)
		go func() {
			_, err := old.Metrics.Query(t.Context(), q)
			result <- err
		}()
		if got := await(t, started); got != q {
			t.Fatalf("started query = %q, want %q", got, q)
		}
		return result
	}
	kept := query("kept")
	edit := runtime.PrepareShared(conn, runtime.Snapshot(), func(settings *Settings) {
		settings.Cost.Source = opencost.SourcePrometheus
	})
	if err := edit.Commit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	current := SetFor(conn)
	if current.Metrics != old.Metrics || current.ArgoCD != old.ArgoCD || current.Traffic != old.Traffic ||
		current.Helm != old.Helm || current.Cost == old.Cost || current.Currency == old.Currency {
		t.Fatal("cost edit did not preserve the other members")
	}
	if _, err := old.Cost.Selected(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("old cost member was not retired: %v", err)
	}
	close(firstRelease)
	if err := await(t, kept); err != nil {
		t.Fatalf("unchanged metrics request failed: %v", err)
	}

	retired := query("retired")
	edit = runtime.PrepareShared(conn, runtime.Snapshot(), func(settings *Settings) {
		settings.Metrics.URL = nextStore.URL
	})
	if err := edit.Commit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	next := SetFor(conn)
	if next.Metrics == current.Metrics || next.Cost == current.Cost || next.Traffic == current.Traffic ||
		next.Currency == current.Currency || next.Helm != current.Helm || next.ArgoCD != current.ArgoCD {
		t.Fatal("metrics edit did not replace exactly metrics and its dependents")
	}
	if !old.Metrics.Closed() || await(t, retired) == nil {
		t.Fatal("retired metrics still served its request")
	}
	if _, err := next.Metrics.Query(t.Context(), "new"); err != nil || nextCalls.Load() == 0 {
		t.Fatalf("replacement could not query its backend: %v", err)
	}
	if k8s.Current() != conn || k8s.ServingConnectionID() != id {
		t.Fatal("settings edit changed the admission")
	}
}

func TestSwitchFencesLateDiscoveryAndReadmissionCreatesFreshMembers(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	started, release := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	done := make(chan struct{})
	defer releaseOnce.Do(func() { close(release) })
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/services" {
			once.Do(func() {
				close(started)
				<-release
				close(done)
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"ServiceList","apiVersion":"v1","items":[]}`)
	}))
	t.Cleanup(a.Close)
	var bCalls atomic.Int32
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"ServiceList","apiVersion":"v1","items":[]}`)
	}))
	t.Cleanup(b.Close)
	installTestBuild(t, &rest.Config{Host: a.URL})
	runtime := New(nil, Settings{})
	old := admitRuntime(t, runtime)
	originalID := k8s.ServingConnectionID()
	t.Cleanup(k8s.RetireServingConnection)
	await(t, started)

	installTestBuild(t, &rest.Config{Host: b.URL})
	next := admitRuntime(t, runtime)
	if !old.Metrics.Closed() || next.Metrics == old.Metrics || next.ArgoCD == old.ArgoCD ||
		next.Cost == old.Cost || next.Traffic == old.Traffic || next.Currency == old.Currency || next.Helm == old.Helm {
		t.Fatal("switch left an old integration member in use")
	}
	_, _, _ = next.Metrics.EnsureConnected(t.Context())
	if got := bCalls.Load(); got == 0 {
		t.Fatal("replacement discovered against the previous build")
	}
	releaseOnce.Do(func() { close(release) })
	await(t, done)
	if _, _, err := old.Metrics.EnsureConnected(t.Context()); err == nil || old.Metrics.GetStatus().Connected || next.Metrics.GetStatus().Connected {
		t.Fatalf("retired discovery published a connection: %v", err)
	}
	installTestBuild(t, &rest.Config{Host: a.URL})
	again := admitRuntime(t, runtime)
	if again.Metrics == old.Metrics || again.Helm == old.Helm || originalID == k8s.ServingConnectionID() {
		t.Fatal("returning to A resurrected its retired admission")
	}
}

func TestRetirementClosesBlockedTrafficConnectAndFlowStream(t *testing.T) {
	for _, action := range []string{"edit", "switch", "shutdown"} {
		t.Run(action, func(t *testing.T) { testTrafficRetirement(t, action) })
	}
}

func testTrafficRetirement(t *testing.T, action string) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	started := make(chan struct{})
	var once sync.Once
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query" && r.URL.Query().Get("query") == "up" {
			_, _ = io.WriteString(w, metricsAnswer)
			return
		}
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	t.Cleanup(store.Close)
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	runtime := New(nil, Settings{Metrics: prom.Connection{URL: store.URL}})
	set := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	if err := set.Traffic.SetActiveSource("caretta"); err != nil {
		t.Fatal(err)
	}
	stream, err := set.Traffic.StreamFlows(t.Context(), traffic.DefaultFlowOptions())
	if err != nil {
		t.Fatal(err)
	}
	connect := make(chan bool, 1)
	go func() {
		info, err := set.Traffic.Connect(t.Context())
		connect <- err == nil && info.Connected
	}()
	await(t, started)
	retired := make(chan error, 1)
	go func() {
		switch action {
		case "edit":
			edit := runtime.PrepareShared(k8s.Current(), runtime.Snapshot(), func(settings *Settings) { settings.Metrics.URL = "" })
			retired <- edit.Commit(func() error { return nil })
			return
		case "switch":
			k8s.RetireServingConnection()
		case "shutdown":
			k8s.CloseConnectionAdmissions()
		}
		retired <- nil
	}()
	if err := await(t, retired); err != nil {
		t.Fatalf("retirement failed: %v", err)
	}
	select {
	case _, ok := <-stream:
		if ok {
			t.Fatal("retired stream still serves flows")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("retirement left the flow stream open")
	}
	if await(t, connect) || !set.Metrics.Closed() {
		t.Fatal("old work survived retirement")
	}
	if action == "edit" && SetFor(k8s.Current()).Traffic == set.Traffic {
		t.Fatal("metrics edit reused the blocked traffic member")
	}
}

func TestOfflineBuildReadsSavedOpenObserveInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestProfileSource("/test/kubeconfig", "local", "user"))
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	runtime := New(connections.NewResolver(config.NewProfileStore(), target, nil), Settings{})
	runtime.RefreshProfiles()
	if err := config.Save(config.Config{OpenObserve: &config.OpenObserveConfig{BaseURL: "https://metrics.example"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := runtime.Build(t.Context(), k8s.Current())
	set := raw.(*Set)
	defer set.Close()
	if set.settings.observeConfig == nil || set.settings.observeConfig.BaseURL != "https://metrics.example" {
		t.Fatal("the next build did not see the OpenObserve settings saved while offline")
	}
	if set.ArgoCD == nil || set.Metrics == nil {
		t.Fatal("offline OpenObserve setup interfered with other integrations")
	}
}

func TestRejectedEditClosesOnlyNewMembers(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	runtime := New(nil, Settings{})
	before := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	edit := runtime.PrepareShared(k8s.Current(), runtime.Snapshot(), func(settings *Settings) {
		settings.Metrics.URL = "http://127.0.0.1:1"
	})
	refused := errors.New("persistence rejected")
	if err := edit.Commit(func() error { return refused }); !errors.Is(err, refused) {
		t.Fatalf("commit error = %v, want rejection", err)
	}
	after := SetFor(k8s.Current())
	if after.Metrics != before.Metrics || before.Metrics.Closed() || !edit.Set().Metrics.Closed() ||
		after.ArgoCD != before.ArgoCD || after.Helm != before.Helm {
		t.Fatal("failed edit changed the live set or kept its candidate open")
	}
	if _, err := edit.Set().Cost.Selected(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("rejected dependent was not closed: %v", err)
	}
}

func TestCommitSucceedsWhenConnectionRetiresDuringPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(k8s.SnapshotTestConnection())
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	runtime := New(nil, Settings{})
	before := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)

	const metricsURL = "http://127.0.0.1:1/metrics"
	const argoURL = "http://127.0.0.1:1/argo"
	edit := runtime.PrepareShared(k8s.Current(), runtime.Snapshot(), func(settings *Settings) {
		settings.Metrics.URL = metricsURL
		settings.ArgoCD.URL = argoURL
	})
	prepared := edit.Set()
	if prepared.Metrics == before.Metrics || prepared.ArgoCD == before.ArgoCD || prepared.Cost == before.Cost || prepared.Traffic == before.Traffic {
		t.Fatal("expected a replacement for each changed member")
	}
	if err := edit.Commit(func() error {
		_, err := config.Update(func(c *config.Config) { c.PrometheusURL, c.ArgoCDURL = metricsURL, argoURL })
		k8s.RetireServingConnection()
		return err
	}); err != nil {
		t.Fatalf("save reported failure after persistence: %v", err)
	}
	if !prepared.Metrics.Closed() {
		t.Fatal("unpublished metrics member is still open")
	}
	if err := prepared.ArgoCD.Probe(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("unpublished Argo CD member is still open: %v", err)
	}
	if _, err := prepared.Cost.Selected(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("unpublished cost member is still open: %v", err)
	}
	if _, err := prepared.Traffic.Connect(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("unpublished traffic member is still open: %v", err)
	}
	next := admitRuntime(t, runtime)
	gotURL, _ := next.Metrics.CurrentConfig()
	saved := config.Load()
	if saved.PrometheusURL != metricsURL || saved.ArgoCDURL != argoURL || gotURL != saved.PrometheusURL || next.settings.ArgoCD.URL != saved.ArgoCDURL {
		t.Fatalf("next admission used Metrics %q and Argo CD %q, saved URLs are %q and %q", gotURL, next.settings.ArgoCD.URL, saved.PrometheusURL, saved.ArgoCDURL)
	}
	if next.Metrics == prepared.Metrics || next.Metrics.Closed() || next.ArgoCD == prepared.ArgoCD || next.Cost == prepared.Cost || next.Traffic == prepared.Traffic {
		t.Fatal("next admission reused a discarded member")
	}
}

func TestOfflineMetricsSaveKeepsScopeClearedNotice(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	k8s.RetireServingConnection()
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	runtime := New(nil, Settings{Scope: prom.WorkloadMetricsScope{SingleCluster: true}})
	edit := runtime.PrepareShared(k8s.Current(), runtime.Snapshot(), func(settings *Settings) {
		settings.Metrics.URL = "http://127.0.0.1:1/metrics"
	})
	if err := edit.Commit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	next := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	if !next.settings.scopeCleared || next.settings.Scope.SingleCluster {
		t.Fatal("next admission lost the scope-cleared notice")
	}
}

func TestRescopeAndRollbackReplaceTheIntegrationSet(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	prevNS := k8s.GetNamespaceScopeTarget()
	t.Cleanup(func() { k8s.SetNamespaceScopeOverride(prevNS) })
	t.Cleanup(k8s.ResetAllSubsystems)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"major":"1","minor":"35","gitVersion":"v1.35.0"}`)
		case "/api":
			_, _ = io.WriteString(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case "/apis":
			_, _ = io.WriteString(w, `{"kind":"APIGroupList","groups":[]}`)
		case "/api/v1":
			_, _ = io.WriteString(w, `{"kind":"APIResourceList","groupVersion":"v1","resources":[]}`)
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
		}
	}))
	t.Cleanup(api.Close)
	installTestBuild(t, &rest.Config{Host: api.URL})
	k8s.SetNamespaceScopeOverride("initial")
	runtime := New(nil, Settings{})
	k8s.RegisterIntegrationBuilder(runtime.Build)
	t.Cleanup(func() { k8s.RegisterIntegrationBuilder(nil) })
	k8s.RegisterTimelineFuncs(nil, func() error {
		if k8s.GetNamespaceScopeTarget() == "refused" {
			return errors.New("test namespace cannot initialize")
		}
		return nil
	})
	t.Cleanup(func() { k8s.RegisterTimelineFuncs(nil, nil) })
	before := admitRuntime(t, runtime)
	if err := k8s.PerformNamespaceRescope("selected"); err != nil {
		t.Fatal(err)
	}
	selected := SetFor(k8s.Current())
	id := k8s.ServingConnectionID()
	if !before.Metrics.Closed() || selected.Metrics == nil || selected.Metrics == before.Metrics || selected.Helm == before.Helm {
		t.Fatal("rescope did not replace the set")
	}
	if err := k8s.PerformNamespaceRescope("refused"); err == nil {
		t.Fatal("the refused scope unexpectedly initialized")
	}
	rolledBack := SetFor(k8s.Current())
	if !selected.Metrics.Closed() || rolledBack.Metrics == nil || rolledBack.Metrics == selected.Metrics ||
		rolledBack.Helm == selected.Helm || k8s.ServingConnectionID() == id || k8s.GetNamespaceScopeTarget() != "selected" {
		t.Fatal("rollback did not admit fresh members on the previous namespace")
	}
}

func TestMetricsAvailabilityWithoutASet(t *testing.T) {
	got := (Set{}).MetricsAvailability(t.Context())
	if got.State != prometheus.AvailabilityAbsent || got.Err != nil || got.Address != "" {
		t.Fatalf("availability = %+v, want absent with no error", got)
	}
}
