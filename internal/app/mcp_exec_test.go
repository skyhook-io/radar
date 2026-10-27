package app

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestMCPExecFlagGating(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	previousDisable := k8s.ForceDisableExec
	t.Cleanup(func() { SetGlobals(AppConfig{DisableExec: previousDisable}) })
	for _, tc := range []struct {
		name                                        string
		optIn, disableExec, readOnly, enabled, want bool
	}{
		{name: "default", enabled: true},
		{name: "on", optIn: true, enabled: true, want: true},
		{name: "disable-exec", optIn: true, disableExec: true, enabled: true},
		{name: "read-only", optIn: true, readOnly: true, enabled: true},
		{name: "MCP disabled", optIn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			previousLog := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previousLog) })
			cfg := AppConfig{MCPEnabled: tc.enabled, MCPExecTools: tc.optIn, DisableExec: tc.disableExec, MCPReadOnly: tc.readOnly}
			SetGlobals(cfg)
			srv := CreateServer(cfg)
			if strings.Contains(logs.String(), "opt-in suppressed") != (tc.optIn && !tc.want) {
				t.Fatalf("suppression log: %s", logs.String())
			}
			configRec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(configRec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
			var configResult struct {
				Effective struct {
					MCPExecTools *bool `json:"mcpExecTools"`
				} `json:"effective"`
			}
			if err := json.Unmarshal(configRec.Body.Bytes(), &configResult); err != nil {
				t.Fatal(err)
			}
			if configResult.Effective.MCPExecTools == nil || *configResult.Effective.MCPExecTools != tc.want {
				t.Fatalf("effective config: %s", configRec.Body.String())
			}
			req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", "2025-06-18")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if tc.enabled && !tc.readOnly {
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"get_resource"`) {
					t.Fatalf("catalog: %d %s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != http.StatusNotFound {
				t.Fatalf("disabled full MCP: %d", rec.Code)
			}
			if got := strings.Contains(rec.Body.String(), `"name":"exec_pod"`); got != tc.want {
				t.Fatalf("exec registered=%v want=%v", got, tc.want)
			}
			capsRec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(capsRec, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
			var caps k8s.Capabilities
			if err := json.Unmarshal(capsRec.Body.Bytes(), &caps); err != nil {
				t.Fatal(err)
			}
			if capsRec.Code != http.StatusOK || caps.MCPExecTools != tc.want {
				t.Fatalf("exec capabilities: %d %s; want exec=%v", capsRec.Code, capsRec.Body.String(), tc.want)
			}
			applyReq := httptest.NewRequest(http.MethodPost, "http://localhost/mcp-apply", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			applyReq.Header = req.Header.Clone()
			applyRec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(applyRec, applyReq)
			if tc.enabled && !tc.readOnly {
				if applyRec.Code != http.StatusOK || !strings.Contains(applyRec.Body.String(), `"name":"apply_resource"`) {
					t.Fatalf("Apply catalog: %d %s", applyRec.Code, applyRec.Body.String())
				}
			} else if applyRec.Code != http.StatusNotFound {
				t.Fatalf("disabled Apply mount: %d", applyRec.Code)
			}
			if strings.Contains(applyRec.Body.String(), `"name":"exec_pod"`) || strings.Contains(applyRec.Body.String(), `"name":"delete_resource"`) {
				t.Fatal("restricted tool registered on the Apply mount")
			}
		})
	}
}
