package auth

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
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

func TestSessionIssued(t *testing.T) {
	issued := CreateSessionCookie(&User{Username: "alice"}, NewSessionID(), "", "secret", time.Hour, false)
	if !sessionIssued(issued) {
		t.Error("a normal session cookie set should be considered issued")
	}
	clearsOnly := []*http.Cookie{
		{Name: DefaultCookieName, MaxAge: -1},
		{Name: DefaultCookieName + "_chunks", MaxAge: -1},
	}
	if sessionIssued(clearsOnly) {
		t.Error("a clearing-only set (oversized refusal) should not be considered issued")
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

func TestHandleLogout_NoEndSessionEndpoint(t *testing.T) {
	h := newTestOIDCHandler()
	// endSessionEndpoint is empty by default
	r := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	h.HandleLogout(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "logged out" {
		t.Errorf("status = %q, want %q", resp["status"], "logged out")
	}
	if _, ok := resp["redirectTo"]; ok {
		t.Error("redirectTo should not be present when end_session_endpoint is empty")
	}
}

func TestHandleLogout_WithEndSessionEndpoint(t *testing.T) {
	h := newTestOIDCHandler()
	h.endSessionEndpoint = "https://idp.example.com/logout"
	h.cfg.OIDCClientID = "radar-client"

	// Create a session cookie with an ID token
	user := &User{Username: "alice"}
	cookie := CreateSessionCookie(user, NewSessionID(), "my-id-token", h.cfg.Secret, 1*time.Hour, false)[0]

	r := httptest.NewRequest("GET", "/auth/logout", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()

	h.HandleLogout(w, r)

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	redirectTo := resp["redirectTo"]
	if redirectTo == "" {
		t.Fatal("redirectTo should be present")
	}
	if !strings.HasPrefix(redirectTo, "https://idp.example.com/logout") {
		t.Errorf("redirectTo = %q, want prefix https://idp.example.com/logout", redirectTo)
	}
	if !strings.Contains(redirectTo, "id_token_hint=my-id-token") {
		t.Errorf("redirectTo should contain id_token_hint, got %q", redirectTo)
	}
	// Should not contain client_id when id_token_hint is present
	if strings.Contains(redirectTo, "client_id=") {
		t.Errorf("redirectTo should not contain client_id when id_token_hint is present")
	}

	// Session cookie should be cleared
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == DefaultCookieName && c.MaxAge == -1 {
			found = true
		}
	}
	if !found {
		t.Error("session cookie should be cleared")
	}
}

func TestHandleLogout_WithPostLogoutRedirectURL(t *testing.T) {
	h := newTestOIDCHandler()
	h.endSessionEndpoint = "https://idp.example.com/logout"
	h.cfg.OIDCPostLogoutRedirectURL = "https://radar.example.com/"

	r := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	h.HandleLogout(w, r)

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	redirectTo := resp["redirectTo"]
	if !strings.Contains(redirectTo, "post_logout_redirect_uri=") {
		t.Errorf("redirectTo should contain post_logout_redirect_uri, got %q", redirectTo)
	}
}

func TestHandleLogout_NoIDTokenInCookie(t *testing.T) {
	h := newTestOIDCHandler()
	h.endSessionEndpoint = "https://idp.example.com/logout"
	h.cfg.OIDCClientID = "radar-client"

	// Session cookie without ID token (old session from before upgrade)
	user := &User{Username: "alice"}
	cookie := CreateSessionCookie(user, NewSessionID(), "", h.cfg.Secret, 1*time.Hour, false)[0]

	r := httptest.NewRequest("GET", "/auth/logout", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()

	h.HandleLogout(w, r)

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	redirectTo := resp["redirectTo"]
	if redirectTo == "" {
		t.Fatal("redirectTo should be present even without id_token")
	}
	// Should fall back to client_id
	if !strings.Contains(redirectTo, "client_id=radar-client") {
		t.Errorf("redirectTo should contain client_id fallback, got %q", redirectTo)
	}
	if strings.Contains(redirectTo, "id_token_hint=") {
		t.Errorf("redirectTo should not contain id_token_hint when cookie has no token")
	}
}

func TestHandleLogout_SetsForceLoginCookie(t *testing.T) {
	h := newTestOIDCHandler()
	// No end_session_endpoint — simulates Google
	r := httptest.NewRequest("GET", "/auth/logout", nil)
	w := httptest.NewRecorder()

	h.HandleLogout(w, r)

	// Should set the force-login cookie
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcForceLoginCookieName && c.Value == "1" {
			found = true
			if c.MaxAge != 300 {
				t.Errorf("force-login cookie MaxAge = %d, want 300", c.MaxAge)
			}
		}
	}
	if !found {
		t.Error("logout should set force-login cookie")
	}
}

func TestHandleLogin_ForceLoginPrompt(t *testing.T) {
	h := newTestOIDCHandler()
	// Set up minimal oauth config so AuthCodeURL works
	h.oauth = oauth2.Config{
		ClientID: "test-client",
		Endpoint: oauth2.Endpoint{
			AuthURL: "https://accounts.google.com/o/oauth2/v2/auth",
		},
		RedirectURL: "http://localhost:9280/auth/callback",
		Scopes:      []string{"openid"},
	}

	// Request with force-login cookie set
	r := httptest.NewRequest("GET", "/auth/login", nil)
	r.AddCookie(&http.Cookie{Name: oidcForceLoginCookieName, Value: "1"})
	w := httptest.NewRecorder()

	h.HandleLogin(w, r)

	// Should redirect to IdP with prompt=login
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "prompt=login") {
		t.Errorf("redirect URL should contain prompt=login, got %q", location)
	}

	// Should clear the force-login cookie
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcForceLoginCookieName && c.MaxAge == -1 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("force-login cookie should be cleared after use")
	}
}

func TestHandleLogin_NoForceLoginWithoutCookie(t *testing.T) {
	h := newTestOIDCHandler()
	h.oauth = oauth2.Config{
		ClientID: "test-client",
		Endpoint: oauth2.Endpoint{
			AuthURL: "https://accounts.google.com/o/oauth2/v2/auth",
		},
		RedirectURL: "http://localhost:9280/auth/callback",
		Scopes:      []string{"openid"},
	}

	// Request WITHOUT force-login cookie
	r := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	location := w.Header().Get("Location")
	if strings.Contains(location, "prompt=login") {
		t.Errorf("redirect URL should NOT contain prompt=login on normal login, got %q", location)
	}
}

// newTLSOIDCServer starts an httptest.NewTLSServer that serves a minimal OIDC
// discovery document. The caller must call Close() when done.
func newTLSOIDCServer() *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/auth",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	return srv
}

func TestNewOIDCHandler_FailsWithSelfSignedCert(t *testing.T) {
	srv := newTLSOIDCServer()
	defer srv.Close()

	_, err := NewOIDCHandler(context.Background(), Config{
		Mode:             "oidc",
		OIDCIssuer:       srv.URL,
		OIDCClientID:     "test",
		OIDCClientSecret: "secret",
		OIDCRedirectURL:  "http://localhost/callback",
	})
	if err == nil {
		t.Fatal("expected TLS error, got nil")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("expected certificate error, got: %v", err)
	}
}

func TestNewOIDCHandler_InsecureSkipVerify(t *testing.T) {
	srv := newTLSOIDCServer()
	defer srv.Close()

	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:                   "oidc",
		OIDCIssuer:             srv.URL,
		OIDCClientID:           "test",
		OIDCClientSecret:       "secret",
		OIDCRedirectURL:        "http://localhost/callback",
		OIDCInsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("expected success with InsecureSkipVerify, got: %v", err)
	}
	if h.httpClient == nil {
		t.Error("httpClient should be set when InsecureSkipVerify is true")
	}
}

func TestNewOIDCHandler_CACert(t *testing.T) {
	srv := newTLSOIDCServer()
	defer srv.Close()

	// Write the test server's CA cert to a temp file
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: srv.TLS.Certificates[0].Certificate[0],
	})
	f, err := os.CreateTemp("", "oidc-ca-*.pem")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(certPEM); err != nil {
		t.Fatal(err)
	}
	f.Close()

	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:             "oidc",
		OIDCIssuer:       srv.URL,
		OIDCClientID:     "test",
		OIDCClientSecret: "secret",
		OIDCRedirectURL:  "http://localhost/callback",
		OIDCCACert:       f.Name(),
	})
	if err != nil {
		t.Fatalf("expected success with CA cert, got: %v", err)
	}
	if h.httpClient == nil {
		t.Error("httpClient should be set when CACert is provided")
	}
}

func TestNewOIDCHandler_CACertTakesPrecedence(t *testing.T) {
	srv := newTLSOIDCServer()
	defer srv.Close()

	// Write CA cert
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: srv.TLS.Certificates[0].Certificate[0],
	})
	f, err := os.CreateTemp("", "oidc-ca-*.pem")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(certPEM); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Both flags set — CA cert should win (InsecureSkipVerify should be false on transport)
	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:                   "oidc",
		OIDCIssuer:             srv.URL,
		OIDCClientID:           "test",
		OIDCClientSecret:       "secret",
		OIDCRedirectURL:        "http://localhost/callback",
		OIDCCACert:             f.Name(),
		OIDCInsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if h.httpClient == nil {
		t.Fatal("httpClient should be set")
	}
	// Verify InsecureSkipVerify is NOT set (CA cert takes precedence)
	transport := h.httpClient.Transport.(*http.Transport)
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be false when CA cert is provided")
	}
	if transport.TLSClientConfig.RootCAs == nil {
		t.Error("RootCAs should be set when CA cert is provided")
	}
}

func TestNewOIDCHandler_InvalidCACertPath(t *testing.T) {
	_, err := NewOIDCHandler(context.Background(), Config{
		Mode:             "oidc",
		OIDCIssuer:       "https://example.com",
		OIDCClientID:     "test",
		OIDCClientSecret: "secret",
		OIDCRedirectURL:  "http://localhost/callback",
		OIDCCACert:       "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Fatal("expected error for invalid CA cert path")
	}
	if !strings.Contains(err.Error(), "failed to read") {
		t.Errorf("expected 'failed to read' error, got: %v", err)
	}
}

func TestNewOIDCHandler_InvalidCACertContent(t *testing.T) {
	f, err := os.CreateTemp("", "oidc-bad-ca-*.pem")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString("not a certificate")
	f.Close()

	_, err = NewOIDCHandler(context.Background(), Config{
		Mode:             "oidc",
		OIDCIssuer:       "https://example.com",
		OIDCClientID:     "test",
		OIDCClientSecret: "secret",
		OIDCRedirectURL:  "http://localhost/callback",
		OIDCCACert:       f.Name(),
	})
	if err == nil {
		t.Fatal("expected error for invalid CA cert content")
	}
	if !strings.Contains(err.Error(), "no valid certificates") {
		t.Errorf("expected 'no valid certificates' error, got: %v", err)
	}
}

func TestOIDCHandler_CallbackUsesCustomClient(t *testing.T) {
	h := newTestOIDCHandler()
	h.httpClient = &http.Client{} // non-nil signals custom client is set

	// The callback should inject the client into the context.
	// We test this indirectly: a valid state + code but nil oauth config
	// will fail at Exchange, not at client injection.
	r := httptest.NewRequest("GET", "/auth/callback?state=abc&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "abc"})
	w := httptest.NewRecorder()

	h.HandleCallback(w, r)

	// Should fail at token exchange (oauth config is nil), not panic
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (exchange failure, not panic)", w.Code)
	}
}

// --- Backchannel logout handler pre-verification tests ---

func TestBackchannelLogout_NoRevoker(t *testing.T) {
	h := newTestOIDCHandler()
	// h.revoker is nil — backchannel logout not configured
	r := httptest.NewRequest("POST", "/auth/backchannel-logout", nil)
	w := httptest.NewRecorder()

	h.HandleBackchannelLogout(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("Cache-Control: no-store header missing (spec requirement)")
	}
}

func TestBackchannelLogout_MissingToken(t *testing.T) {
	h := newTestOIDCHandler()
	h.revoker = NewMemoryRevoker()
	defer h.revoker.Stop()

	r := httptest.NewRequest("POST", "/auth/backchannel-logout",
		strings.NewReader(""))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleBackchannelLogout(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestBackchannelLogout_CacheControlAlwaysSet(t *testing.T) {
	// Cache-Control: no-store must be set even on error responses (spec §2.5)
	h := newTestOIDCHandler()
	r := httptest.NewRequest("POST", "/auth/backchannel-logout", nil)
	w := httptest.NewRecorder()

	h.HandleBackchannelLogout(w, r)

	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("Cache-Control: no-store must be set on all responses")
	}
}

// Note: testing invalid/valid JWT verification requires a real OIDC provider
// with JWKS. The pre-verification tests above cover all paths before Verify().
