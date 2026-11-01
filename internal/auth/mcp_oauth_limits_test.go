package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPOAuthRegistrationBodyLimit(t *testing.T) {
	const limit = 8 * 1024
	const registration = `{"redirect_uris":["https://client.example/callback"]}`
	for _, tc := range []struct {
		name string
		body string
		want int
	}{
		{"oversized JSON", `{"client_name":"` + strings.Repeat("x", limit) + `"}`, http.StatusRequestEntityTooLarge},
		{"oversized trailing whitespace", registration + strings.Repeat(" ", limit+1-len(registration)), http.StatusRequestEntityTooLarge},
		{"exactly at limit", registration + strings.Repeat(" ", limit-len(registration)), http.StatusCreated},
		{"malformed JSON", `{`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMCPOAuthFixture(t)
			r := httptest.NewRequest(http.MethodPost, "/auth/mcp/register", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.s.HandleRegister(w, r)
			if w.Code != tc.want {
				t.Fatalf("registration: got %d, want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
}

func TestMCPOAuthFormBodyLimits(t *testing.T) {
	for _, endpoint := range []struct {
		name   string
		handle func(*MCPOAuthServer, http.ResponseWriter, *http.Request)
	}{
		{"authorize", (*MCPOAuthServer).HandleAuthorize},
		{"token", (*MCPOAuthServer).HandleToken},
		{"revoke", (*MCPOAuthServer).HandleRevoke},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				body string
				want int
			}{
				{"oversized form", "padding=" + strings.Repeat("x", 8*1024), http.StatusRequestEntityTooLarge},
				{"malformed form", "padding=%zz", http.StatusBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := newMCPOAuthFixture(t)
					r := httptest.NewRequest(http.MethodPost, "/auth/mcp/"+endpoint.name, strings.NewReader(tc.body))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.Header.Set("Origin", f.s.origin)
					for _, cookie := range f.cookies {
						r.AddCookie(cookie)
					}
					w := httptest.NewRecorder()
					endpoint.handle(f.s, w, r)
					if w.Code != tc.want {
						t.Fatalf("%s: got %d, want %d: %s", endpoint.name, w.Code, tc.want, w.Body)
					}
				})
			}
		})
	}
}
