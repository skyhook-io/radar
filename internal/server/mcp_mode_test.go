package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/ai"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestPutConfigRejectsContradictoryMCPWithoutChangingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := config.Save(config.Config{Port: 9280}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{})
	response := httptest.NewRecorder()
	s.handlePutConfig(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"mcp":false,"mcpReadOnly":true}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "MCP") {
		t.Fatalf("save = %d %s; want MCP validation error", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid save changed the config file")
	}
}

func TestReadOnlyMCPKeepsAIEligible(t *testing.T) {
	stub := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := &Server{mcpReadOnlyHandler: stub, mcpInvestigationHandler: stub, aiInvestigationRefs: investigationrefs.NewRegistry()}
	if !s.aiDeploymentSupported() {
		t.Fatal("read-only MCP disabled investigations")
	}
	response := httptest.NewRecorder()
	s.handleCapabilities(response, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	var caps k8s.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !caps.MCPEnabled || !caps.MCPReadOnly {
		t.Fatalf("capabilities: %d %s", response.Code, response.Body.String())
	}
	// This setting only changes MCP exposure, not ordinary write permissions.
	full := &Server{mcpHandler: stub, mcpReadOnlyHandler: stub, mcpInvestigationHandler: stub}
	responseFull := httptest.NewRecorder()
	full.handleCapabilities(responseFull, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	var fullCaps k8s.Capabilities
	if err := json.Unmarshal(responseFull.Body.Bytes(), &fullCaps); err != nil {
		t.Fatal(err)
	}
	if fullCaps.Exec != caps.Exec || fullCaps.HelmWrite != caps.HelmWrite || fullCaps.NodeWrite != caps.NodeWrite || fullCaps.WorkloadWrites != caps.WorkloadWrites {
		t.Fatal("MCP mode changed ordinary write permissions")
	}
}

func TestReadOnlyMCPRejectsAIApply(t *testing.T) {
	m := ai.NewRunManager(nil, func() string { return "localhost:9280" }, "", func() string { return "test" }, nil)
	t.Cleanup(m.Shutdown)
	s := &Server{aiRuns: m, mcpReadOnlyHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	response := httptest.NewRecorder()
	s.handleDiagnoseTurn(response, httptest.NewRequest(http.MethodPost, "/api/diagnose/runs/test/turns", strings.NewReader(`{"apply":true,"fix":"scale to 2"}`)))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "read-only MCP mode") {
		t.Fatalf("apply: %d %s", response.Code, response.Body.String())
	}
}

func TestStartupReadOnlyMCP(t *testing.T) {
	got := strings.Join(formatStartupLogSummary(startupLogSummary{mcpEnabled: true, mcpReadOnly: true, basePath: "/radar"}, false), "\n")
	if !strings.Contains(got, "enabled (read-only at /radar/mcp-readonly)") {
		t.Fatal(got)
	}
}
