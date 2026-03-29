package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestOIDCHandler creates a minimal OIDCHandler for testing validation paths.
// The provider/oauth/verifier fields are nil — only use for tests that return
// before token exchange.
func newTestOIDCHandler() *OIDCHandler {
	return &OIDCHandler{
		cfg: Config{
			Mode:   "oidc",
			Secret: "test-secret",
		},
	}
}

func TestOIDCCallback_MissingStateCookie(t *testing.T) {
	h := newTestOIDCHandler()
	r := httptest.NewRequest("GET", "/auth/callback?state=abc&code=xyz", nil)
	// No state cookie set
	w := httptest.NewRecorder()

	h.HandleCallback(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if body := w.Body.String(); body == "" {
		t.Error("expected error message in body")
	}
}

func TestOIDCCallback_MismatchedState(t *testing.T) {
	h := newTestOIDCHandler()
	r := httptest.NewRequest("GET", "/auth/callback?state=wrong&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "expected"})
	w := httptest.NewRecorder()

	h.HandleCallback(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestOIDCCallback_MissingCode(t *testing.T) {
	h := newTestOIDCHandler()
	r := httptest.NewRequest("GET", "/auth/callback?state=abc", nil)
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "abc"})
	w := httptest.NewRecorder()

	h.HandleCallback(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
