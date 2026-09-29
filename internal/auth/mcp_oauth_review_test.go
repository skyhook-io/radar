package auth

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMCPOAuthParallelConsentPages(t *testing.T) {
	f := newMCPOAuthFixture(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(f.s.issuer + "/auth/mcp/authorize")
	jar.SetCookies(endpoint, f.cookies)
	var forms []url.Values
	for _, resource := range []string{"/mcp", "/mcp-readonly"} {
		page := f.authorize(f.request(resource), jar.Cookies(endpoint))
		forms = append(forms, f.consentForm(page))
		jar.SetCookies(endpoint, page.Result().Cookies())
	}
	for i, form := range forms {
		r := httptest.NewRequest("POST", "/auth/mcp/authorize", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", f.s.origin)
		for _, cookie := range jar.Cookies(endpoint) {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		f.s.HandleAuthorize(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("consent page %d failed: %d %s", i, w.Code, w.Body)
		}
		jar.SetCookies(endpoint, w.Result().Cookies())
		remaining := 0
		for _, cookie := range jar.Cookies(endpoint) {
			if strings.HasPrefix(cookie.Name, mcpConsentCookie) {
				remaining++
			}
		}
		if remaining != len(forms)-i-1 {
			t.Fatalf("consent page %d cleared another approval's cookie: %d remain", i, remaining)
		}
	}
}

func TestMCPOAuthInvalidRegistrationsPreserveCapacity(t *testing.T) {
	for _, legitimatePeer := range []string{"192.0.2.10:5000", "192.0.2.20:5000"} {
		t.Run(legitimatePeer, func(t *testing.T) {
			f := newMCPOAuthFixture(t)
			register := func(body, peer string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(body))
				r.RemoteAddr = peer
				r.Header.Set("X-Forwarded-For", "203.0.113.99")
				w := httptest.NewRecorder()
				f.s.HandleRegister(w, r)
				return w
			}
			for range 30 {
				for _, invalid := range []string{`{`, `{"redirect_uris":["http://unsafe.example/callback"]}`} {
					if w := register(invalid, "192.0.2.10:5000"); w.Code != http.StatusBadRequest {
						t.Fatalf("invalid registration: %d %s", w.Code, w.Body)
					}
				}
			}
			if f.s.registrations != 1 {
				t.Fatalf("invalid requests consumed registration capacity: %d", f.s.registrations)
			}
			if w := register(`{"redirect_uris":["https://client.example/callback"]}`, legitimatePeer); w.Code != http.StatusCreated {
				t.Fatalf("legitimate registration starved: %d %s", w.Code, w.Body)
			}
		})
	}
}
