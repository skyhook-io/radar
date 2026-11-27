package connectionruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/openobserve/oofake"
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

func TestMetricsEditPreservesAvailableTrafficSource(t *testing.T) {
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/pods" && r.URL.Query().Get("labelSelector") == "k8s-app=hubble-relay":
			_, _ = io.WriteString(w, `{"kind":"PodList","apiVersion":"v1","items":[{"metadata":{"namespace":"kube-system","name":"hubble-relay"},"status":{"phase":"Running"}}]}`)
		case r.URL.Path == "/api/v1/namespaces/kube-system/services/hubble-relay":
			_, _ = io.WriteString(w, `{"kind":"Service","apiVersion":"v1","metadata":{"namespace":"kube-system","name":"hubble-relay"},"spec":{"clusterIP":"127.0.0.1","ports":[{"port":1}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(cluster.Close)
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "up" || (!strings.HasPrefix(r.URL.Path, "/without-beyla/") && strings.HasPrefix(query, "count(beyla_network_flow_bytes_total{")) {
			_, _ = io.WriteString(w, metricsAnswer)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
	}))
	t.Cleanup(metrics.Close)

	for _, tc := range []struct{ path, want string }{
		{"/with-beyla", "beyla"},
		{"/without-beyla", "hubble"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Cleanup(k8s.SnapshotTestConnection())
			t.Cleanup(k8s.SetTestLocalMode())
			restConfig := &rest.Config{Host: cluster.URL}
			installTestBuild(t, restConfig)
			runtime := New(nil, Settings{Metrics: prom.Connection{URL: metrics.URL}})
			old := admitRuntime(t, runtime)
			t.Cleanup(k8s.RetireServingConnection)
			if err := old.Traffic.SetActiveSource("beyla"); err != nil {
				t.Fatal(err)
			}
			conn := k8s.Current()
			edit := runtime.PrepareShared(conn, runtime.Snapshot(), func(settings *Settings) {
				settings.Metrics.URL = metrics.URL + tc.path
			})
			if err := edit.Commit(func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			next := SetFor(conn).Traffic
			if next == old.Traffic || next.SourceSnapshot() != "" {
				t.Fatal("replacement must detect before reporting an active source")
			}
			if sources, err := next.DetectSources(t.Context()); err != nil || sources.Active != tc.want {
				t.Fatalf("detection after edit = %+v, %v, want active %q", sources, err, tc.want)
			}
			installTestBuild(t, restConfig)
			fresh := admitRuntime(t, runtime).Traffic
			if sources, err := fresh.DetectSources(t.Context()); err != nil || sources.Active != "hubble" {
				t.Fatalf("detection after readmission = %+v, %v, want active hubble", sources, err)
			}
		})
	}
}

func TestSharedStartupInvalidKubecostURL(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, metricsAnswer)
	}))
	t.Cleanup(metrics.Close)
	for _, fromEnv := range []bool{false, true} {
		name := "persisted"
		if fromEnv {
			name = "environment"
		}
		t.Run(name, func(t *testing.T) {
			t.Cleanup(k8s.SnapshotTestConnection())
			t.Cleanup(k8s.SetTestLocalMode())
			k8s.ForceInCluster = true
			for _, name := range []string{"RADAR_COST_SOURCE", "RADAR_KUBECOST_URL", "RADAR_KUBECOST_API_KEY", "RADAR_KUBECOST_CLUSTER_ID"} {
				t.Setenv(name, "")
			}
			cost := opencost.ManagerConfig{Source: opencost.SourceKubecost, URL: "invalid-url"}
			if fromEnv {
				t.Setenv("RADAR_KUBECOST_URL", cost.URL)
				cost.URL = ""
			}
			installTestBuild(t, &rest.Config{Host: "http://127.0.0.1:1"})
			startup := Settings{Metrics: prom.Connection{URL: metrics.URL}}
			startup.Cost, startup.CostEnv, startup.CostError = opencost.ResolveEnvironmentConfig(cost)
			set := admitRuntime(t, New(nil, startup))
			t.Cleanup(k8s.RetireServingConnection)
			selected, err := set.Cost.Selected(t.Context())
			if fromEnv {
				if !errors.Is(err, opencost.ErrCostSourceEnvConfig) {
					t.Fatalf("selection error = %v, want invalid environment configuration", err)
				}
			} else if err != nil || selected.Source != opencost.SourcePrometheus || set.Cost.ConfigSnapshot().Source != opencost.SourceAuto {
				t.Fatalf("selection = %q, %v, want Auto to select Prometheus", selected.Source, err)
			}
		})
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

func TestRefreshProfilesKeepsMembersAfterOpenObserveRecordsIDMatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestProfileSource("/test/kubeconfig", "local", "user"))
	const uid = "7b930c6c-dcbb-4d1f-9afd-67ea890873d7"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/namespaces/kube-system", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"Namespace","apiVersion":"v1","metadata":{"name":"kube-system","uid":"`+uid+`"}}`)
	})
	kube := httptest.NewServer(mux)
	t.Cleanup(kube.Close)
	installTestBuild(t, &rest.Config{Host: kube.URL})
	backend := oofake.Start(t, &oofake.Server{IDs: map[string]string{uid: "local"}})
	if err := config.Save(config.Config{OpenObserve: &config.OpenObserveConfig{
		BaseURL: backend.URL, Org: "default", User: "test", Token: "test-token",
		Matches: map[string]config.OpenObserveMatch{uid: {By: config.OpenObserveMatchByID, Context: "local"}},
	}}); err != nil {
		t.Fatal(err)
	}
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	runtime := New(connections.NewResolver(config.NewProfileStore(), target, nil), Settings{})
	initial := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	deadline := time.Now().Add(10 * time.Second)
	for !initial.Metrics.RelayActive() {
		if time.Now().After(deadline) {
			t.Fatalf("OpenObserve did not activate: %+v", initial.Metrics.GetStatus())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if match := config.Load().OpenObserve.Matches[uid]; match.Label != "radar_cluster_uid" || match.Value != uid {
		t.Fatalf("activation did not record its ID match: %+v", match)
	}

	conn := k8s.Current()
	before := ownedSetFor(conn)
	runtime.RefreshProfiles()
	if ownedSetFor(conn) != before {
		t.Fatal("the recorded ID match changed the integration set")
	}
	if _, err := config.Update(func(c *config.Config) {
		c.OpenObserve.Matches["other"] = config.OpenObserveMatch{By: config.OpenObserveMatchByName, Label: "cluster", Value: "other"}
		c.OpenObserve.ClusterLabel = "cluster_name"
	}); err != nil {
		t.Fatal(err)
	}
	runtime.RefreshProfiles()
	if ownedSetFor(conn) != before {
		t.Fatal("another cluster's match changed the integration set")
	}
	runtime.RefreshProfiles(config.IntegrationArgoCD)
	if got := SetFor(conn); got.ArgoCD == initial.ArgoCD || got.Metrics != initial.Metrics || got.Traffic != initial.Traffic ||
		got.Cost != initial.Cost || got.Currency != initial.Currency {
		t.Fatal("an Argo CD rebuild did not preserve the active metrics and dependent members")
	}

	if _, err := config.Update(func(c *config.Config) {
		delete(c.OpenObserve.Matches, uid)
	}); err != nil {
		t.Fatal(err)
	}
	runtime.RefreshProfiles()
	if got := SetFor(conn); got.Metrics == initial.Metrics || !initial.Metrics.Closed() {
		t.Fatal("removing this cluster's match did not replace its Metrics member")
	}
}

func TestSwitchDoesNotCarryManualMetricsConfigurationToAnotherCluster(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestProfileSource("/test/kubeconfig", "first", "user"))
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	t.Cleanup(k8s.RetireServingConnection)
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	resolver := connections.NewResolver(config.NewProfileStore(), target, nil)
	runtime := New(resolver, Settings{})
	url := "http://127.0.0.1:1/metrics"
	selected := resolver.Resolve(target, config.IntegrationMetrics, false)
	pending, err := resolver.Prepare(target, connections.Update{
		Target: target, Revision: selected.View.Revision, Kind: config.IntegrationMetrics, Action: "save", URL: &url,
		Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "Bearer test-token"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Commit(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	first := admitRuntime(t, runtime)
	if got, headers := first.Metrics.CurrentConfig(); got != url || headers["Authorization"] != "Bearer test-token" {
		t.Fatal("first admission did not read its saved metrics profile")
	}
	k8s.RetireServingConnection()
	t.Cleanup(k8s.SetTestProfileSource("/test/kubeconfig", "second", "user"))
	k8s.SetTestConfig(&rest.Config{Host: "http://127.0.0.1:2"})
	second := admitRuntime(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	if got, headers := second.Metrics.CurrentConfig(); !first.Metrics.Closed() || got != "" || len(headers) != 0 {
		t.Fatal("switch failed to retire the first Metrics member or isolate its manual settings")
	}
}

func TestArgoEnvDiscoveryTokenStaysBoundToLaunchConnection(t *testing.T) {
	t.Cleanup(k8s.SnapshotTestConnection())
	t.Cleanup(k8s.SetTestLocalMode())
	t.Setenv("RADAR_ARGOCD_TOKEN", "env-token")
	t.Setenv("RADAR_ARGOCD_TOKEN_FILE", "")
	t.Setenv("RADAR_ARGOCD_URL", "")
	t.Setenv("RADAR_ARGOCD_INSECURE_TLS", "")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"kind":"ServiceList","apiVersion":"v1","items":[]}`)
	}))
	t.Cleanup(api.Close)
	t.Cleanup(k8s.SetTestProfileSource("/test/kubeconfig", "cluster-a", "user"))
	installTestBuild(t, &rest.Config{Host: api.URL})
	t.Cleanup(k8s.RetireServingConnection)
	argo, managed, err := argocd.EnvironmentConfiguration()
	if err != nil || !managed {
		t.Fatalf("could not resolve the environment token: %v", err)
	}
	runtime := New(nil, Settings{ArgoCD: argo, ArgoEnv: managed})
	for _, cluster := range []string{"cluster-a", "cluster-b", "cluster-a"} {
		t.Run(cluster, func(t *testing.T) {
			t.Cleanup(k8s.SetTestProfileSource("/test/kubeconfig", cluster, "user"))
			member, _ := runtime.Build(t.Context(), k8s.Current())
			defer member.Close()
			manager := member.(*Set).ArgoCD
			if !manager.IsEnvManaged() || !manager.IsConfigured() {
				t.Fatal("replacement lost the environment configuration")
			}
			err := manager.Probe(t.Context())
			if cluster == "cluster-b" {
				if !errors.Is(err, argocd.ErrTokenInvalid) || !strings.Contains(err.Error(), "token not valid for the current cluster context") {
					t.Fatalf("Probe = %v, want token context mismatch", err)
				}
			} else if !errors.Is(err, argocd.ErrUnreachable) || !strings.Contains(err.Error(), "no Service labeled") {
				t.Fatalf("Probe = %v, want discovery to reach the empty fixture", err)
			}
		})
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
	if live := edit.Set(); live.Metrics != nil || live.ArgoCD != nil || live.Cost != nil || live.Traffic != nil || live.Currency != nil || live.Helm != nil {
		t.Fatal("successful offline save exposed an unpublished member")
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
