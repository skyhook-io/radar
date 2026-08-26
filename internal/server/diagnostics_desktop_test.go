package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/desktopenv"
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/version"
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

func diagnosticsBody(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	rec := httptest.NewRecorder()
	(&Server{}).handleDiagnostics(rec, httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	return body
}
