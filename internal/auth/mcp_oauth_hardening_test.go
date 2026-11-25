package auth

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMCPOAuthRejectsLookAlikeCallbackHosts(t *testing.T) {
	f := newMCPOAuthFixture(t)
	for _, uri := range []string{
		"https://сlaude.ai/callback",      // Cyrillic "с"
		"https://%D1%81laude.ai/callback", // the same, percent-encoded
		"https://claude.ai​/callback",     // zero-width space
		"http://lоcalhost:5000/callback",  // Cyrillic "о" in localhost
	} {
		body, _ := json.Marshal(map[string]any{"client_name": "x", "redirect_uris": []string{uri}})
		w := httptest.NewRecorder()
		f.s.HandleRegister(w, httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(string(body))))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_redirect_uri") {
			t.Errorf("%q: %d %s", uri, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	f.s.HandleRegister(w, httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(`{"redirect_uris":["https://xn--laude-0we.ai/callback"]}`)))
	if w.Code != 201 {
		t.Fatalf("encoded international host must stay allowed: %d %s", w.Code, w.Body)
	}
}

func TestMCPOAuthClientNameDropsInvisibleCharacters(t *testing.T) {
	f := newMCPOAuthFixture(t)
	body, _ := json.Marshal(map[string]any{"client_name": "Claude‮ Code​\x07", "redirect_uris": []string{"http://127.0.0.1:5000/cb"}})
	w := httptest.NewRecorder()
	f.s.HandleRegister(w, httptest.NewRequest("POST", "/auth/mcp/register", strings.NewReader(string(body))))
	var client mcpClient
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &client) != nil {
		t.Fatalf("registration: %d %s", w.Code, w.Body)
	}
	if client.Name != "Claude Code" || f.s.clients[client.ID].Name != "Claude Code" {
		t.Fatalf("name kept invisible characters: %q", client.Name)
	}
}

func TestMCPOAuthPendingRequestBoundToStartingBrowser(t *testing.T) {
	f := newMCPOAuthFixture(t)
	start := f.authorize(f.request("/mcp"), nil)
	location, _ := url.Parse(start.Header().Get("Location"))
	continuation, _ := url.Parse(location.Query().Get("return_to"))

	// Another signed-in user opening the link without the starting browser's cookie.
	other := CreateSessionCookie(&User{Username: "bob@example.com"}, "bob-sid", "", "test-secret", time.Hour, true)
	if w := f.authorize(continuation.Query(), other); w.Code != 400 || strings.Contains(w.Body.String(), "<form") {
		t.Fatalf("another browser resumed the pending request: %d %s", w.Code, w.Body)
	}
	// The starting browser still can.
	page := f.authorize(continuation.Query(), append(f.cookies, start.Result().Cookies()...))
	if page.Code != 200 || !strings.Contains(page.Body.String(), "alice@example.com") {
		t.Fatalf("starting browser could not resume: %d %s", page.Code, page.Body)
	}
}

func TestMCPOAuthConsentSaysThisComputer(t *testing.T) {
	f := newMCPOAuthFixture(t)
	page := f.authorize(f.request("/mcp"), f.cookies)
	if !strings.Contains(page.Body.String(), "An application on this computer.") || strings.Contains(page.Body.String(), "your computer") {
		t.Fatalf("wording: %s", page.Body)
	}
}

func TestMCPOAuthCallbackCarriesIssuer(t *testing.T) {
	f := newMCPOAuthFixture(t)
	metadata := httptest.NewRecorder()
	f.s.HandleMetadata(metadata, httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil))
	if strings.Contains(metadata.Body.String(), "authorization_response_iss_parameter_supported") {
		t.Fatalf("advertising iss support makes clients that drop it fail: %s", metadata.Body)
	}
	for _, decision := range []string{"allow", "deny"} {
		page := f.authorize(f.request("/mcp"), f.cookies)
		form := f.consentForm(page)
		form.Set("decision", decision)
		w := f.consent(page, form, f.s.origin, f.cookies)
		u, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != 303 || u.Query().Get("iss") != f.s.issuer {
			t.Errorf("%s: callback %s lacks iss=%s", decision, u, f.s.issuer)
		}
	}
}
