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

	"github.com/skyhook-io/radar/internal/desktopenv"
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestDiagnosticsReportsRunningOpenCostCurrency(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &Server{
		diagConfig:       &DiagConfig{OpenCostCurrency: "USD"},
		openCostCurrency: internalopencost.NewCurrencyResolver("GBP"),
	}
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
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
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
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
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

func TestDiagnosticsClusterUsesOnlyCurrentCacheObservations(t *testing.T) {
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
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
		k8s.SetConnectionStatus(previousStatus)
	})
	initCache := func() {
		t.Helper()
		if err := k8s.InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
			t.Fatal(err)
		}
	}
	initCache()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})

	if body := diagnosticsBody(t); body["cluster"] != nil || requests.Load() != 0 {
		t.Fatalf("cold diagnostics cluster = %s, API requests = %d", body["cluster"], requests.Load())
	}
	if _, err := k8s.GetClusterInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	reads := requests.Load()
	if reads == 0 {
		t.Fatal("fixture did not exercise the live cluster detection fallback")
	}
	var cluster DiagCluster
	if err := json.Unmarshal(diagnosticsBody(t)["cluster"], &cluster); err != nil || cluster.ObservedAt == "" || requests.Load() != reads {
		t.Fatalf("cached observation = %+v, error = %v, extra API reads = %d", cluster, err, requests.Load()-reads)
	}
	k8s.ResetResourceCache()
	initCache()
	if body := diagnosticsBody(t); body["cluster"] != nil || requests.Load() != reads {
		t.Fatalf("replacement cache reused cluster data or contacted API: %s, reads %d", body["cluster"], requests.Load()-reads)
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
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
	if err := traffic.Initialize(kubernetes.NewForConfigOrDie(&rest.Config{Host: api.URL})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traffic.Reset)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = traffic.GetManager().DetectSources(ctx)
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
