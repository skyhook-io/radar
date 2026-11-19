package auth

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"

	"golang.org/x/net/publicsuffix"
)

// logoutThroughJar stores what the proxy set in a real RFC 6265 cookie jar,
// sends the jar's cookies to logout, applies the deletions Radar returns, and
// reports which cookie names the browser would still send to pageURL.
func logoutThroughJar(t *testing.T, pageURL string, proxySet []*http.Cookie, patterns []string, basePath string) []string {
	t.Helper()
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := url.Parse(pageURL)
	jar.SetCookies(page, proxySet)

	logoutURL := page.ResolveReference(&url.URL{Path: basePath + "/api/auth/logout"})
	req := httptest.NewRequest(http.MethodGet, logoutURL.String(), nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range jar.Cookies(logoutURL) {
		req.AddCookie(c)
	}
	jar.SetCookies(logoutURL, ClearProxyCookies(req, patterns, basePath))

	var left []string
	for _, c := range jar.Cookies(page) {
		left = append(left, c.Name)
	}
	sort.Strings(left)
	return left
}

func TestClearProxyCookies_HostOnlyCookiesOnIPHost(t *testing.T) {
	left := logoutThroughJar(t, "https://10.0.0.5/radar/", []*http.Cookie{
		{Name: "_oauth2_proxy_0", Value: "id", Path: "/", HttpOnly: true, Secure: true},
		{Name: "_oauth2_proxy_1", Value: "claims", Path: "/", HttpOnly: true, Secure: true},
		{Name: "app_theme", Value: "someone", Path: "/"},
	}, []string{"_oauth2_proxy*"}, "/radar")

	if len(left) != 1 || left[0] != "app_theme" {
		t.Fatalf("cookies left after logout = %v, want only the unmatched app_theme", left)
	}
}

func TestClearProxyCookies_ParentDomainCookie(t *testing.T) {
	left := logoutThroughJar(t, "https://radar.apps.example.com/", []*http.Cookie{
		{Name: "authelia_session", Value: "s", Domain: "example.com", Path: "/", HttpOnly: true, Secure: true},
		{Name: "_oauth2_proxy", Value: "s", Domain: "apps.example.com", Path: "/", HttpOnly: true, Secure: true},
	}, []string{"authelia_session", "_oauth2_proxy*"}, "")

	if len(left) != 0 {
		t.Fatalf("cookies left after logout = %v, want none: cookies scoped to a parent domain must be deleted too", left)
	}
}

func TestClearProxyCookies_CookieScopedToBasePath(t *testing.T) {
	left := logoutThroughJar(t, "https://radar.example.com/radar/", []*http.Cookie{
		{Name: "_oauth2_proxy_0", Value: "part0", Path: "/radar", HttpOnly: true, Secure: true},
		{Name: "_oauth2_proxy_1", Value: "part1", Path: "/radar", HttpOnly: true, Secure: true},
	}, []string{"_oauth2_proxy*"}, "/radar")

	if len(left) != 0 {
		t.Fatalf("cookies left after logout = %v, want none: split cookies on Radar's base path must be deleted", left)
	}
}

func TestClearProxyCookies_CookiesOnParentAndTrailingSlashPaths(t *testing.T) {
	left := logoutThroughJar(t, "https://example.com/platform/radar/", []*http.Cookie{
		{Name: "_oauth2_proxy", Value: "a", Path: "/platform", HttpOnly: true, Secure: true},
		{Name: "_oauth2_proxy_csrf", Value: "b", Path: "/platform/radar/", HttpOnly: true, Secure: true},
	}, []string{"_oauth2_proxy*"}, "/platform/radar")

	if len(left) != 0 {
		t.Fatalf("cookies left after logout = %v, want none: cookies on a parent path or with a trailing slash must be deleted", left)
	}
}

func TestClearProxyCookies_PrefixedCookiesDeletedWithSecure(t *testing.T) {
	// TLS ends at the proxy, which doesn't send X-Forwarded-Proto.
	req := httptest.NewRequest(http.MethodGet, "http://radar.example.com/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-session", Value: "a"})
	req.AddCookie(&http.Cookie{Name: "__secure-session", Value: "b"})
	req.AddCookie(&http.Cookie{Name: "plain_session", Value: "c"})

	for _, c := range ClearProxyCookies(req, []string{"*session"}, "") {
		wantSecure := c.Name != "plain_session"
		if c.Secure != wantSecure {
			t.Fatalf("deletion of %s (Domain %q, Path %q) has Secure=%v, want %v", c.Name, c.Domain, c.Path, c.Secure, wantSecure)
		}
	}
}

func TestClearProxyCookies_NoPatternsDeletesNothing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://radar.example.com/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "_oauth2_proxy", Value: "id"})
	if got := ClearProxyCookies(req, nil, ""); len(got) != 0 {
		t.Fatalf("ClearProxyCookies with no patterns returned %d cookies, want 0", len(got))
	}
}

func TestValidateProxyLogoutCookies(t *testing.T) {
	if err := ValidateProxyLogoutCookies([]string{"_oauth2_proxy*", "authelia_session"}); err != nil {
		t.Fatalf("valid patterns rejected: %v", err)
	}
	if err := ValidateProxyLogoutCookies([]string{"_oauth2_proxy["}); err == nil {
		t.Fatal("malformed pattern accepted, want an error")
	}
}
