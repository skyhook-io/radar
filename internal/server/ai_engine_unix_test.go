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

func TestReadOnlyMCPStartsAIEngine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_AI_CLI_BIN", bin)
	stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := &Server{mcpReadOnlyHandler: stub, mcpInvestigationHandler: stub, aiInvestigationRefs: investigationrefs.NewRegistry()}
	t.Cleanup(func() {
		if runs := s.aiRunManager(); runs != nil {
			runs.Shutdown()
		}
	})
	if !listAgentsEnabled(t, s) {
		t.Fatal("read-only MCP did not start AI engine")
	}
	response := httptest.NewRecorder()
	s.handleListAgents(response, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var result struct {
		Agents []struct {
			Apply        bool `json:"apply"`
			Verification bool `json:"verification"`
		} `json:"agents"`
		Eligible bool `json:"eligible"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || len(result.Agents) == 0 {
		t.Fatalf("agents: %s", response.Body.String())
	}
	for _, agent := range result.Agents {
		if agent.Apply || !agent.Verification {
			t.Fatalf("agent capabilities: %+v", agent)
		}
	}
}

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
// diagnoseTestServer is a connected, auth-off Server whose only installed
// agent CLI is a stub claude.
func diagnoseTestServer(t *testing.T) *Server {
	t.Helper()
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
	return s
}

func TestDiagnoseStartRefusesAnAgentThatIsNotInstalled(t *testing.T) {
	s := diagnoseTestServer(t)
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

// A run opens with a resource, a question, or both; the question's scope is
// validated before any agent is chosen.
func TestDiagnoseStartValidatesTheOpening(t *testing.T) {
	s := diagnoseTestServer(t)
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"nothing":            {`{}`, "a resource or a question is required"},
		"kind without name":  {`{"kind":"Pod","question":"why?"}`, "kind and name are required"},
		"scope without text": {`{"namespaces":["shop"]}`, "namespaces and lookBack need a question"},
		"bad look-back":      {`{"question":"why?","lookBack":"2d"}`, "lookBack must be one of 15m, 1h, 6h, 24h"},
		"bad namespace":      {`{"question":"why?","namespaces":["Shop"]}`, "invalid namespace"},
		"too long":           {`{"question":"` + strings.Repeat("x", 2001) + `"}`, "question is too long"},
	} {
		rec := httptest.NewRecorder()
		s.handleDiagnoseStart(rec, httptest.NewRequest(http.MethodPost, "/api/diagnose/runs", strings.NewReader(tc.body)))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s, want 400 %q", name, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

// The body is bounded before it is read, so an oversized one is refused as
// too large rather than parsed and blamed on its content.
func TestDiagnoseStartBoundsTheBody(t *testing.T) {
	s := diagnoseTestServer(t)
	rec := httptest.NewRecorder()
	body := `{"question":"` + strings.Repeat("x", diagnoseStartBodyLimit) + `"}`
	s.handleDiagnoseStart(rec, httptest.NewRequest(http.MethodPost, "/api/diagnose/runs", strings.NewReader(body)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized start = %d %s, want 413", rec.Code, rec.Body.String())
	}
}

func agentSignin(t *testing.T, s *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleAgentSignin(rec, httptest.NewRequest(http.MethodGet, "/api/agents/signin?"+query, nil))
	return rec
}

// The sign-in check spawns the agent CLI, so it is available exactly where
// investigations are, and runs nothing anywhere else.
func TestAgentSigninFollowsTheInvestigationGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	marker := filepath.Join(home, "ran")
	bin := filepath.Join(home, "codex")
	script := "#!/bin/sh\ntouch " + marker + "\necho 'Not logged in' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_AI_CLI_BIN", bin)
	mcp := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	off := &Server{
		authConfig:              auth.Config{Mode: "proxy"},
		mcpInvestigationHandler: mcp,
		aiInvestigationRefs:     investigationrefs.NewRegistry(),
	}
	off.refreshAIEngine(t.Context())
	if rec := agentSignin(t, off, "agent=codex"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("auth-enabled Radar: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("auth-enabled Radar ran the agent CLI")
	}

	on := &Server{
		authConfig:              auth.Config{Mode: "none"},
		mcpInvestigationHandler: mcp,
		aiInvestigationRefs:     investigationrefs.NewRegistry(),
	}
	on.refreshAIEngine(t.Context())
	t.Cleanup(func() {
		if runs := on.aiRunManager(); runs != nil {
			runs.Shutdown()
		}
	})
	if rec := agentSignin(t, on, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no agent: status=%d", rec.Code)
	}
	if rec := agentSignin(t, on, "agent=codex&profile=nope"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown profile: status=%d", rec.Code)
	}
	if rec := agentSignin(t, on, "agent=claude"); rec.Code != http.StatusNotFound {
		t.Fatalf("agent that isn't a backend: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := agentSignin(t, on, "agent=codex")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["agent"] != "codex" || got["state"] != "signed-out" || got["signinCmd"] != "codex login" {
		t.Fatalf("answer: %s", rec.Body.String())
	}
}
