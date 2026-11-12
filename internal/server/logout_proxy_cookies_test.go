package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// expiredCookieNames returns the names of the cookies a response deletes.
func expiredCookieNames(res *http.Response) map[string]bool {
	names := make(map[string]bool)
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			names[c.Name] = true
		}
	}
	return names
}

func logoutRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "https://10.0.0.1/radar/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "_oauth2_proxy_0", Value: "x"})
	req.AddCookie(&http.Cookie{Name: "_oauth2_proxy_1", Value: "y"})
	req.AddCookie(&http.Cookie{Name: "unrelated", Value: "z"})
	return req
}

func TestLogoutDeletesConfiguredProxyCookies(t *testing.T) {
	s := &Server{basePath: "/radar", authConfig: auth.Config{Mode: "proxy", ProxyLogoutCookies: []string{"_oauth2_proxy*"}}}
	rec := httptest.NewRecorder()
	s.handleLogout(rec, logoutRequest())

	deleted := expiredCookieNames(rec.Result())
	for _, name := range []string{"_oauth2_proxy_0", "_oauth2_proxy_1"} {
		if !deleted[name] {
			t.Errorf("logout did not delete %s", name)
		}
	}
	if deleted["unrelated"] {
		t.Error("logout deleted a cookie that matches no configured name")
	}
}

func TestLogoutKeepsProxyCookiesOutsideProxyMode(t *testing.T) {
	s := &Server{basePath: "/radar", authConfig: auth.Config{Mode: "oidc", ProxyLogoutCookies: []string{"_oauth2_proxy*"}}}
	rec := httptest.NewRecorder()
	s.handleLogout(rec, logoutRequest())

	if deleted := expiredCookieNames(rec.Result()); deleted["_oauth2_proxy_0"] {
		t.Error("logout deleted proxy cookies while not in proxy mode")
	}
}

func TestAuthMeReportsLogoutConfiguredForCookiesAlone(t *testing.T) {
	s := &Server{authConfig: auth.Config{Mode: "proxy", ProxyLogoutCookies: []string{"_oauth2_proxy*"}}}
	rec := httptest.NewRecorder()
	s.handleAuthMe(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["proxyLogoutConfigured"] != true {
		t.Errorf("proxyLogoutConfigured = %v, want true", body["proxyLogoutConfigured"])
	}
}
