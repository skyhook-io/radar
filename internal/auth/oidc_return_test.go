package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
)

func TestOIDCLoginResumesMCPAuthorization(t *testing.T) {
	for _, basePath := range []string{"", "/radar"} {
		t.Run(basePath, func(t *testing.T) {
			idp := newFakeIDP(t, oidc.ES256)
			h := idp.newHandlerUnderBasePath(t, Config{Secret: "test-secret", OIDCEnablePKCE: true}, basePath)
			target := basePath + "/auth/mcp/authorize?request=pending-request"
			login := httptest.NewRecorder()
			h.HandleLogin(login, httptest.NewRequest("GET", "https://radar.example.com/auth/login?return_to="+url.QueryEscape(target), nil))
			location, err := url.Parse(login.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			callback := httptest.NewRequest("GET", "https://radar.example.com/auth/callback?code=test-code&state="+location.Query().Get("state"), nil)
			for _, cookie := range login.Result().Cookies() {
				callback.AddCookie(cookie)
			}
			response := httptest.NewRecorder()
			h.HandleCallback(response, callback)
			if response.Code != http.StatusFound || response.Header().Get("Location") != target {
				t.Fatalf("callback status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
			}
			if sessionFrom(t, h, response).User.Username != "alice@example.com" || idp.gotVerifier == "" {
				t.Fatal("OIDC identity or PKCE did not survive MCP continuation")
			}
		})
	}
}

func TestMCPReturnCannotBecomeOpenRedirect(t *testing.T) {
	h := &OIDCHandler{cfg: Config{Secret: "test-secret"}, basePath: "/radar"}
	for _, target := range []string{
		"https://attacker.example/auth/mcp/authorize", "//attacker.example/auth/mcp/authorize",
		"/radar/auth/mcp/authorize/../logout", "/auth/mcp/authorize", "/radar/api/config",
		"/radar/auth/mcp/authorize#fragment", "/radar/auth/mcp/authorize\r\nX-Test: injected",
		"/radar/auth/mcp/authorize\\evil", "/radar/auth/mcp/authorize?x=" + strings.Repeat("x", 2048),
	} {
		t.Run(target[:min(len(target), 80)], func(t *testing.T) {
			w := httptest.NewRecorder()
			h.setMCPReturnCookie(w, httptest.NewRequest("GET", "/auth/login?return_to="+url.QueryEscape(target), nil), "state")
			if c := w.Result().Cookies()[0]; c.Value != "" || c.MaxAge != -1 {
				t.Fatal("unsafe continuation was stored")
			}
		})
	}
}

func TestMCPReturnBoundToLoginAndSignature(t *testing.T) {
	h := &OIDCHandler{cfg: Config{Secret: "test-secret"}}
	w := httptest.NewRecorder()
	h.setMCPReturnCookie(w, httptest.NewRequest("GET", "/auth/login?return_to="+url.QueryEscape("/auth/mcp/authorize?request=opaque"), nil), "first-login")
	cookie := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/auth/callback", nil)
	r.AddCookie(cookie)
	if h.mcpReturnTarget(r, "second-login") != "/" {
		t.Fatal("continuation crossed login flows")
	}
	cookie.Value = "AAAA" + cookie.Value[4:]
	r = httptest.NewRequest("GET", "/auth/callback", nil)
	r.AddCookie(cookie)
	if h.mcpReturnTarget(r, "first-login") != "/" {
		t.Fatal("tampered continuation accepted")
	}
}
