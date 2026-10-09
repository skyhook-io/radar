package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
)

// The MCP mounts sit outside /api, so the server's same-origin middleware does
// not cover them. Each one must refuse a POST a browser sends from another
// site on its own.
func TestMCPHandlersRefuseCrossSitePosts(t *testing.T) {
	t.Setenv("RADAR_MCP_DISABLE_ORIGIN_PROTECTION", "")
	// The investigation mount checks its scope first, so give it a live one to
	// reach the origin check behind it.
	scope := strings.Repeat("a", 26)
	refs := investigationrefs.NewRegistry()
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	handlers := map[string]http.Handler{
		"/mcp":               NewHandler(),
		"/mcp-readonly":      NewReadOnlyHandler(),
		"/mcp-investigation": NewInvestigationHandler(refs),
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	for name, handler := range handlers {
		for _, fetchSite := range []string{"cross-site", ""} {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9280/?scope="+scope, strings.NewReader(body))
			r.Header.Set("Origin", "https://evil.example")
			if fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", fetchSite)
			}
			r.Header.Set("Content-Type", "text/plain")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s, Sec-Fetch-Site %q: status = %d, want 403", name, fetchSite, w.Code)
			}
		}
	}
}
