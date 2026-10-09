//go:build !windows

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
)

func listAgentsEnabled(t *testing.T, s *Server) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleListAgents(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var resp struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Enabled
}

// Installing an agent CLI while Radar runs must turn investigations on the next
// time a client asks for the agent list, with no restart.
func TestListAgentsTurnsInvestigationsOnForACLIInstalledLater(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("RADAR_AI_CLI_BIN", "")

	mcp := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := &Server{
		authConfig:              auth.Config{Mode: "none"},
		mcpHandler:              mcp,
		mcpInvestigationHandler: mcp,
		aiInvestigationRefs:     investigationrefs.NewRegistry(),
	}
	t.Cleanup(func() {
		if runs := s.aiRunManager(); runs != nil {
			runs.Shutdown()
		}
	})
	s.refreshAIEngine(t.Context())
	if s.aiRunManager() != nil {
		t.Skip("an agent CLI is installed in a fixed system directory on this machine")
	}
	if listAgentsEnabled(t, s) {
		t.Fatal("investigations enabled with no agent CLI installed")
	}

	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !listAgentsEnabled(t, s) {
		t.Fatal("Claude Code was installed, but /api/agents still reports investigations off")
	}
	if got := s.aiRunManager().AgentName(""); got != "claude" {
		t.Errorf("default agent = %q, want claude", got)
	}
}

// A deployment that can't run local investigations (auth, --no-mcp) must stay
// off however many CLIs are installed.
func TestRefreshAIEngineLeavesUnsupportedDeploymentsOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	mcp := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := &Server{
		authConfig:              auth.Config{Mode: "proxy"},
		mcpHandler:              mcp,
		mcpInvestigationHandler: mcp,
		aiInvestigationRefs:     investigationrefs.NewRegistry(),
	}
	if listAgentsEnabled(t, s) {
		t.Fatal("investigations turned on in a deployment that can't run them")
	}
}

// The run manager outlives its CLIs (it keeps history); once the last CLI is
// gone, investigations must read as off, or radar diagnose skips its guidance.
func TestListAgentsReportsOffOnceTheLastCLIIsRemoved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("RADAR_AI_CLI_BIN", "")
	mcp := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := &Server{
		authConfig:              auth.Config{Mode: "none"},
		mcpHandler:              mcp,
		mcpInvestigationHandler: mcp,
		aiInvestigationRefs:     investigationrefs.NewRegistry(),
	}
	t.Cleanup(func() {
		if runs := s.aiRunManager(); runs != nil {
			runs.Shutdown()
		}
	})
	s.refreshAIEngine(t.Context())
	if s.aiRunManager() != nil {
		t.Skip("an agent CLI is installed in a fixed system directory on this machine")
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(bin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !listAgentsEnabled(t, s) {
		t.Fatal("investigations off with Claude Code installed")
	}
	if err := os.Remove(claude); err != nil {
		t.Fatal(err)
	}
	if listAgentsEnabled(t, s) {
		t.Fatal("the only agent CLI was removed, but /api/agents still reports investigations on")
	}
}

// A run that names an agent that isn't installed (picked before it was removed)
// must fail, not start a different agent the user didn't pick or consent to.
func TestDiagnoseStartRefusesAnAgentThatIsNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("RADAR_AI_CLI_BIN", "")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	mcp := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := &Server{
		authConfig:              auth.Config{Mode: "none"},
		mcpHandler:              mcp,
		mcpInvestigationHandler: mcp,
		aiInvestigationRefs:     investigationrefs.NewRegistry(),
	}
	t.Cleanup(func() {
		if runs := s.aiRunManager(); runs != nil {
			runs.Shutdown()
		}
	})
	s.refreshAIEngine(t.Context())
	if runs := s.aiRunManager(); runs == nil || runs.AgentName("codex") == "codex" {
		t.Skip("Codex is installed in a fixed system directory on this machine")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/diagnose/runs",
		strings.NewReader(`{"kind":"Pod","namespace":"prod","name":"api-0","agent":"codex"}`))
	s.handleDiagnoseStart(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Codex isn't installed") {
		t.Fatalf("start with an uninstalled agent = %d %s, want 409 naming Codex", rec.Code, rec.Body.String())
	}
}
