package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/desktopenv"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestDiagnosticsReportsRunningOpenCostCurrency(t *testing.T) {
	rec := httptest.NewRecorder()
	s := serverWithTestIntegrations(t, connectionruntime.Settings{Currency: "GBP"})
	s.diagConfig = &DiagConfig{OpenCostCurrency: "USD"}
	s.handleDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))

	var snapshot DiagnosticsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Config == nil || snapshot.Config.OpenCostCurrency != "GBP" {
		t.Fatalf("diagnostics currency = %#v, want GBP", snapshot.Config)
	}
}

// The CLI and the desktop app share this endpoint. A CLI snapshot must not
// carry a Desktop section at all — an empty one in a bug report reads as
// "we looked and the host reported nothing", which is a different claim.
func TestDiagnosticsOmitsDesktopSectionForCLI(t *testing.T) {
	version.SetDesktop(false)
	t.Cleanup(func() { version.SetDesktop(false) })

	if _, ok := diagnosticsBody(t)["desktop"]; ok {
		t.Error("CLI diagnostics carry a desktop section, want none")
	}
}

// Under the desktop app the section is present exactly when the platform has
// host render settings to report — every Linux desktop run, no macOS or
// Windows run.
func TestDiagnosticsIncludesDesktopSectionForDesktopApp(t *testing.T) {
	version.SetDesktop(true)
	t.Cleanup(func() { version.SetDesktop(false) })

	_, got := diagnosticsBody(t)["desktop"]
	want := desktopenv.Collect() != nil
	if got != want {
		t.Errorf("desktop section present = %v, want %v for this platform", got, want)
	}
}

func TestDiagnosticsRedactsLocalKubeconfigError(t *testing.T) {
	previous := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.PublishTestConnectionStatus(previous) })
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{
		State:     k8s.StateDisconnected,
		Error:     `failed to build kubeconfig from "/Users/alice/.kube/config": yaml: line 3`,
		ErrorType: "config",
	})

	var connection DiagConnection
	if err := json.Unmarshal(diagnosticsBody(t)["connection"], &connection); err != nil {
		t.Fatalf("decode connection diagnostics: %v", err)
	}
	if connection.Error != "Kubeconfig initialization failed; see local Radar logs for details" {
		t.Fatalf("connection error = %q", connection.Error)
	}
}

func TestDiagnosticsDuringConnectionReturnsPartialSnapshot(t *testing.T) {
	previous := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.PublishTestConnectionStatus(previous) })
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		ProgressMsg: "Loading workloads...",
	})

	body := diagnosticsBody(t)
	var connection DiagConnection
	if err := json.Unmarshal(body["connection"], &connection); err != nil {
		t.Fatal(err)
	}
	if connection.ProgressMessage != "Loading workloads..." || body["runtime"] == nil || body["cluster"] != nil {
		t.Fatalf("progress = %q, runtime present = %t, cluster present = %t", connection.ProgressMessage, body["runtime"] != nil, body["cluster"] != nil)
	}
}

func TestDiagnosticsKeepPrometheusOnStableDisconnect(t *testing.T) {
	setupPrometheusIntegrationTest(t, nil)
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected}))
	if body := diagnosticsBody(t); body["prometheus"] == nil {
		t.Fatal("a stable disconnect hid the Prometheus diagnostics")
	}
}

func TestDiagnosticsClusterUsesOnlyCurrentCacheObservations(t *testing.T) {
	t.Cleanup(k8s.SwapResourceCacheForTest(nil))
	var requests atomic.Int32
	var serveVersion atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/version" && serveVersion.Load() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"gitVersion":"v1.35.3"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)
	client := kubernetes.NewForConfigOrDie(&rest.Config{Host: api.URL})
	previousClient := k8s.SetTestClient(client)
	previousStatus := k8s.GetConnectionStatus()
	k8s.InvalidateServerVersionCache()
	k8s.InvalidatePlatformCache()
	t.Cleanup(func() {
		k8s.SetTestClient(previousClient)
		k8s.ResetResourceCache()
		k8s.InvalidateServerVersionCache()
		k8s.InvalidatePlatformCache()
		k8s.PublishTestConnectionStatus(previousStatus)
	})
	initCache := func() {
		t.Helper()
		if err := k8s.InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
			t.Fatal(err)
		}
	}
	initCache()
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})

	if body := diagnosticsBody(t); body["cluster"] != nil || requests.Load() != 0 {
		t.Fatalf("cold diagnostics cluster = %s, API requests = %d", body["cluster"], requests.Load())
	}
	if _, err := k8s.GetClusterInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, at := k8s.GetResourceCache().LastClusterInfo(); !at.IsZero() {
		t.Error("failed first version read was published as an observation")
	}
	k8s.InvalidateServerVersionCache()
	serveVersion.Store(true)
	info, err := k8s.GetClusterInfo(context.Background())
	if err != nil || info.KubernetesVersion != "v1.35.3" {
		t.Fatalf("fixture: version = %q, err = %v", info.KubernetesVersion, err)
	}
	reads := requests.Load()
	if reads == 0 {
		t.Fatal("fixture did not exercise the live cluster detection fallback")
	}
	var cluster DiagCluster
	if err := json.Unmarshal(diagnosticsBody(t)["cluster"], &cluster); err != nil || cluster.ObservedAt == "" || requests.Load() != reads {
		t.Fatalf("cached observation = %+v, error = %v, extra API reads = %d", cluster, err, requests.Load()-reads)
	}
	before, at := k8s.GetResourceCache().LastClusterInfo()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = k8s.GetClusterInfo(cancelled)
	if _, after := k8s.GetResourceCache().LastClusterInfo(); after != at {
		t.Error("cancelled cluster read replaced the observation")
	}
	k8s.InvalidateServerVersionCache()
	serveVersion.Store(false)
	info, err = k8s.GetClusterInfo(context.Background())
	if err != nil || info.KubernetesVersion != "" {
		t.Fatalf("fixture: outage version = %q, err = %v", info.KubernetesVersion, err)
	}
	if after, afterAt := k8s.GetResourceCache().LastClusterInfo(); after != before || afterAt != at {
		t.Errorf("failed version read changed a good observation: %+v at %v", after, afterAt)
	}
	reads = requests.Load()
	k8s.ResetResourceCache()
	initCache()
	if body := diagnosticsBody(t); body["cluster"] != nil || requests.Load() != reads {
		t.Fatalf("replacement cache reused cluster data or contacted API: %s, reads %d", body["cluster"], requests.Load()-reads)
	}
}

func TestDiagnosticsConnectionUpdatesDuringCollection(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		if err := k8s.InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetResourceCache)
	}
	previousStatus := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.PublishTestConnectionStatus(previousStatus) })
	previousName := k8s.SetTestContextName("old")
	t.Cleanup(func() { k8s.SetTestContextName(previousName) })
	t.Cleanup(k8s.SetTestContextCount(25))
	timeline.CaptureStore().RecordDrop("Pod", "default", "old-pod", "no_diff", "update", k8s.ActiveClusterContext())
	t.Cleanup(func() { timeline.GetMetrics().Reset() })

	for _, tc := range []struct {
		name  string
		start k8s.ConnectionState
		next  k8s.ConnectionStatus
	}{
		{"switch", k8s.StateConnected, k8s.ConnectionStatus{State: k8s.StateConnecting, Context: "new"}},
		{"startup progress", k8s.StateConnecting, k8s.ConnectionStatus{State: k8s.StateConnecting, Context: "old", ProgressMsg: "Loading workloads"}},
		{"connection lost", k8s.StateConnected, k8s.ConnectionStatus{State: k8s.StateDisconnected, Context: "old", Error: "connection lost"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: tc.start, Context: "old"})
			switched := tc.next.Context != "old"
			var reviewed atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/version" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"gitVersion":"v1.35.3"}`))
					return
				}
				if r.URL.Path == "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
					reviewed.Store(true)
					if switched {
						k8s.CancelOngoingOperations()
					}
					k8s.PublishTestConnectionStatus(tc.next)
				}
				http.Error(w, "unavailable", http.StatusForbidden)
			}))
			t.Cleanup(api.Close)
			previous := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: api.URL}))
			t.Cleanup(func() { k8s.SetTestClient(previous) })
			k8s.InvalidateServerVersionCache()
			t.Cleanup(k8s.InvalidateServerVersionCache)
			if info, err := k8s.GetClusterInfo(t.Context()); err != nil || info.KubernetesVersion != "v1.35.3" {
				t.Fatalf("could not seed cluster observation: %+v, %v", info, err)
			}

			s := &Server{broadcaster: &SSEBroadcaster{}, permCache: auth.NewPermissionCache().WithConnectionKey(currentPermissionConnection), diagConfig: &DiagConfig{Port: 9280}}
			s.permCache.Set("viewer", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
			rec := httptest.NewRecorder()
			s.handleDiagnostics(rec, requestWithUser("GET", "/api/diagnostics", &auth.User{Username: "viewer"}))
			var snap DiagnosticsSnapshot
			if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
				t.Fatal(err)
			}
			if !reviewed.Load() || snap.Connection == nil || snap.Connection.Context != tc.next.Context || snap.Connection.State != string(tc.next.State) || snap.Connection.ProgressMessage != tc.next.ProgressMsg || snap.Runtime == nil {
				t.Errorf("reviewed=%v connection=%+v runtime present=%v", reviewed.Load(), snap.Connection, snap.Runtime != nil)
			}
			if snap.Kubeconfig == nil || snap.Kubeconfig.ContextCount != 25 || (snap.Kubeconfig.CurrentContextUsesExec == nil) != switched {
				t.Errorf("kubeconfig = %+v, switched = %v", snap.Kubeconfig, switched)
			}
			if (snap.Cache == nil) != switched || (snap.EventPipeline == nil) != switched || (snap.Informers == nil) != switched {
				t.Errorf("cache=%t events=%t informers=%t, switched=%v", snap.Cache != nil, snap.EventPipeline != nil, snap.Informers != nil, switched)
			}
			// The same cache keeps its last cluster observation through progress
			// and outages; a switch drops it with an explanation.
			if switched {
				if snap.Cluster != nil || len(snap.Errors) == 0 {
					t.Errorf("after a switch: cluster=%+v errors=%v", snap.Cluster, snap.Errors)
				}
			} else if snap.Cluster == nil || snap.Cluster.ObservedAt == "" || snap.Cluster.KubernetesVersion != "v1.35.3" {
				t.Errorf("same cache, %s: cluster=%+v errors=%v", tc.next.State, snap.Cluster, snap.Errors)
			}
			if snap.Traffic != nil {
				t.Errorf("traffic reported while not connected: %+v", snap.Traffic)
			}
			if snap.Config == nil || snap.Config.Port != 9280 || (snap.Config.OpenCostCurrency == "") != switched {
				t.Errorf("configuration = %+v, switched = %v", snap.Config, switched)
			}
		})
	}
}

// A status published late (a reachability check that began before a switch)
// can name the previous context while the current cache serves the new one:
// the cache's observation must not be reported under that context.
func TestDiagnosticsOmitsClusterObservedForAnotherContext(t *testing.T) {
	t.Cleanup(k8s.SwapResourceCacheForTest(nil))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"gitVersion":"v1.35.3"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)
	previousClient := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: api.URL}))
	previousStatus := k8s.GetConnectionStatus()
	previousName := k8s.SetTestContextName("new")
	k8s.InvalidateServerVersionCache()
	t.Cleanup(func() {
		k8s.SetTestClient(previousClient)
		k8s.SetTestContextName(previousName)
		k8s.ResetResourceCache()
		k8s.InvalidateServerVersionCache()
		k8s.PublishTestConnectionStatus(previousStatus)
	})
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
		t.Fatal(err)
	}
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "new"})
	if info, err := k8s.GetClusterInfo(context.Background()); err != nil || info.KubernetesVersion == "" {
		t.Fatalf("could not seed cluster observation: %+v, %v", info, err)
	}
	_, seededAt := k8s.GetResourceCache().LastClusterInfo()

	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected, Context: "old", Error: "cluster unreachable"})
	body := diagnosticsBody(t)
	var errs []string
	_ = json.Unmarshal(body["errors"], &errs)
	if body["cluster"] != nil || !strings.Contains(strings.Join(errs, " "), "connection status settles") {
		t.Fatalf("cluster = %s, errors = %v", body["cluster"], errs)
	}

	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected, Context: "new", Error: "cluster unreachable"})
	// A cluster-info read during the outage succeeds on the cached version and
	// informer data; it must not make the observation look fresh.
	time.Sleep(10 * time.Millisecond)
	if _, err := k8s.GetClusterInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, at := k8s.GetResourceCache().LastClusterInfo(); !at.Equal(seededAt) {
		t.Fatalf("a read during the outage moved the observation from %v to %v", seededAt, at)
	}
	if body := diagnosticsBody(t); body["cluster"] == nil {
		t.Fatal("the matching context's observation was omitted during an outage")
	}
}

func TestDiagnosticsKeepsKubeconfigDuringSwitch(t *testing.T) {
	t.Cleanup(k8s.SetTestContextCount(25))
	t.Cleanup(k8s.SetTestContextOperationInProgress(true))
	var cfg map[string]any
	if err := json.Unmarshal(diagnosticsBody(t)["kubeconfig"], &cfg); err != nil {
		t.Fatal(err)
	}
	if _, hasCurrentExec := cfg["currentContextUsesExec"]; cfg["contextCount"] != float64(25) || hasCurrentExec {
		t.Fatalf("kubeconfig during switch: %+v", cfg)
	}
}

func TestDiagnosticsWhileKubeconfigWriterIsBusy(t *testing.T) {
	previous := k8s.GetConnectionStatus()
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: k8s.GetContextName()})
	t.Cleanup(func() { k8s.PublishTestConnectionStatus(previous) })
	restore, err := k8s.SetTestResourceDiscovery(fake.NewSimpleClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	timeline.CaptureStore().RecordDrop("Pod", "default", "private-pod", "no_diff", "update", k8s.ActiveClusterContext())
	t.Cleanup(func() { timeline.GetMetrics().Reset() })

	s := &Server{broadcaster: &SSEBroadcaster{}, diagConfig: &DiagConfig{Port: 9280}}
	rec := httptest.NewRecorder()
	finished := make(chan struct{})
	unlock := k8s.HoldTestClientWriteLock()
	go func() {
		defer close(finished)
		s.handleDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))
	}()
	select {
	case <-finished:
		unlock()
	case <-time.After(2 * time.Second):
		unlock()
		<-finished
		t.Fatal("diagnostics waited for the kubeconfig writer")
	}
	var snap DiagnosticsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	// Discovery lives on the connection, read without the kubeconfig lock, so it
	// is reported even while a kubeconfig writer holds that lock.
	if snap.Kubeconfig != nil || snap.APIDiscovery == nil || snap.EventPipeline == nil || len(snap.EventPipeline.RecentDrops) != 0 || snap.Runtime == nil {
		t.Fatalf("incomplete diagnostics while kubeconfig is busy: %+v", snap)
	}
}

func TestDiagnosticsDuringTrafficSourceDetection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var expired atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			close(started)
			select {
			case <-release:
			case <-time.After(3 * time.Second):
				expired.Store(true)
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)
	previous := k8s.GetConnectionStatus()
	k8s.PublishTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.PublishTestConnectionStatus(previous) })
	manager := traffic.NewManager(kubernetes.NewForConfigOrDie(&rest.Config{Host: api.URL}), nil, "test", "", nil)
	t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{Traffic: manager}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = manager.DetectSources(ctx)
	}()
	t.Cleanup(func() { cancel(); close(release); <-done })
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("traffic source detection did not reach the API")
	}
	body := diagnosticsBody(t)
	if body["runtime"] == nil || body["traffic"] != nil || !strings.Contains(string(body["errors"]), "Traffic source details") || expired.Load() {
		t.Fatalf("expected partial diagnostics during detection, got traffic=%s errors=%s", body["traffic"], body["errors"])
	}
}

func diagnosticsBody(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	rec := httptest.NewRecorder()
	(&Server{broadcaster: &SSEBroadcaster{}}).handleDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	return body
}
