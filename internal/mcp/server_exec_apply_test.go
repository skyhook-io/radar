package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/server"
)

func TestApplyMountExcludesExecTools(t *testing.T) {
	enableExecToolsForTest(t)
	for _, base := range []string{"", "/radar"} {
		t.Run(base, func(t *testing.T) {
			srv := server.New(server.Config{BasePath: base, MCPHandler: NewHandler(), MCPApplyHandler: NewApplyHandler()})
			for _, path := range []string{"/mcp", "/mcp-apply"} {
				req := httptest.NewRequest(http.MethodPost, "http://localhost"+base+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", "2025-06-18")
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"apply_resource"`) {
					t.Fatalf("%s catalog: %d %s", path, rec.Code, rec.Body.String())
				}
				if got := strings.Contains(rec.Body.String(), `"name":"exec_pod"`); got != (path == "/mcp") {
					t.Fatalf("%s exposes exec_pod=%v", path, got)
				}
			}
		})
	}
}
