package auth

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
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
