package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
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

func TestOIDCCallback_ContextCanceledIsNot500(t *testing.T) {
	h := newTestOIDCHandler()
	// Unreachable token endpoint; the canceled context makes Exchange fail with
	// context.Canceled before any network round-trip completes.
	h.oauth = oauth2.Config{
		ClientID: "radar",
		Endpoint: oauth2.Endpoint{TokenURL: "http://127.0.0.1:1/token"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client already gone

	r := httptest.NewRequest("GET", "/auth/callback?state=abc&code=xyz", nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "abc"})
	w := httptest.NewRecorder()

	h.HandleCallback(w, r)

	if w.Code == http.StatusInternalServerError {
		t.Errorf("a client-canceled token exchange must not return 500, got %d", w.Code)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"localhost":       true,
		"127.0.0.1":       true,
		"::1":             true,
		"radar.localhost": true,
		"example.com":     false,
		"10.0.0.5":        false,
		"":                false,
	}
	for host, want := range cases {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestWarnIfInsecureOIDCOrigin(t *testing.T) {
	cases := map[string]bool{
		"http://radar.example.com/auth/callback":    true,  // non-secure, non-local → warn
		"https://radar.example.com/auth/callback":   false, // secure → no warn
		"http://localhost:8080/auth/callback":       false, // loopback exempt
		"http://radar.localhost:4962/auth/callback": false,
	}
	for redirectURL, wantWarn := range cases {
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		warnIfInsecureOIDCOrigin(redirectURL)
		log.SetOutput(orig)

		warned := strings.Contains(buf.String(), "not HTTPS")
		if warned != wantWarn {
			t.Errorf("warnIfInsecureOIDCOrigin(%q) warned=%v, want %v (log: %q)", redirectURL, warned, wantWarn, buf.String())
		}
	}
}

func TestHandleCallback_ClearsVerifierWithSecure(t *testing.T) {
	h := newTestOIDCHandler()
	h.pkceEnabled = true
	h.oauth = oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: "http://127.0.0.1:1/token"}}

	// Behind a TLS-terminating proxy: the verifier cookie was issued Secure, so
	// its deletion must be Secure too or the browser may not evict it.
	r := httptest.NewRequest("GET", "/auth/callback?state=abc&code=xyz", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "abc"})
	r.AddCookie(&http.Cookie{Name: oidcVerifierCookieName, Value: "some-verifier"})
	w := httptest.NewRecorder()

	h.HandleCallback(w, r) // exchange fails (no IdP), but clears are written first

	var cleared *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcVerifierCookieName && c.MaxAge < 0 {
			cleared = c
		}
	}
	if cleared == nil {
		t.Fatal("verifier cookie was not cleared")
	}
	if !cleared.Secure {
		t.Error("verifier deletion cookie must be Secure to evict a Secure cookie over HTTPS")
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
	}, "")
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
	}, "")
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
	}, "")
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
	}, "")
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

func TestNewOIDCHandler_InternalIssuerUsesInternalEndpoints(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	publicIssuer := "https://auth.example.com"
	provider := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{
			PublicKey: priv.Public(),
			KeyID:     "test-key",
			Algorithm: oidc.RS256,
		}},
	}
	provider.SetIssuer(publicIssuer)
	srv := httptest.NewServer(provider)
	defer srv.Close()

	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:               "oidc",
		OIDCIssuer:         publicIssuer,
		OIDCInternalIssuer: srv.URL,
		OIDCClientID:       "radar",
		OIDCClientSecret:   "secret",
		OIDCRedirectURL:    "http://localhost/callback",
	}, "")
	if err != nil {
		t.Fatalf("expected success with internal issuer, got: %v", err)
	}

	if got, want := h.oauth.Endpoint.AuthURL, publicIssuer+"/auth"; got != want {
		t.Errorf("AuthURL = %q, want %q", got, want)
	}
	if got, want := h.oauth.Endpoint.TokenURL, srv.URL+"/token"; got != want {
		t.Errorf("TokenURL = %q, want %q", got, want)
	}

	claims := fmt.Sprintf(`{
		"iss": %q,
		"aud": "radar",
		"sub": "alice",
		"exp": %d
	}`, publicIssuer, time.Now().Add(time.Hour).Unix())
	rawIDToken := oidctest.SignIDToken(priv, "test-key", oidc.RS256, claims)
	if _, err := h.verifier.Verify(context.Background(), rawIDToken); err != nil {
		t.Fatalf("expected token verification through internal JWKS URL to succeed: %v", err)
	}

	wrongIssuerClaims := fmt.Sprintf(`{
		"iss": %q,
		"aud": "radar",
		"sub": "alice",
		"exp": %d
	}`, srv.URL, time.Now().Add(time.Hour).Unix())
	wrongIssuerToken := oidctest.SignIDToken(priv, "test-key", oidc.RS256, wrongIssuerClaims)
	if _, err := h.verifier.Verify(context.Background(), wrongIssuerToken); err == nil {
		t.Fatal("expected token with internal issuer claim to be rejected")
	}
}

func TestNewOIDCHandler_InternalIssuerWithOverridesKeepsDiscoveryMetadata(t *testing.T) {
	publicIssuer := "https://auth.example.com/application/o/radar"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/application/o/radar/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                publicIssuer,
			"authorization_endpoint":                srv.URL + "/application/o/radar/authorize",
			"token_endpoint":                        srv.URL + "/application/o/radar/token",
			"userinfo_endpoint":                     srv.URL + "/application/o/radar/userinfo",
			"jwks_uri":                              srv.URL + "/application/o/radar/jwks",
			"end_session_endpoint":                  srv.URL + "/application/o/radar/logout",
			"backchannel_logout_supported":          true,
			"backchannel_logout_session_supported":  true,
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer srv.Close()

	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:                 "oidc",
		OIDCIssuer:           publicIssuer,
		OIDCInternalIssuer:   srv.URL + "/application/o/radar",
		OIDCAuthorizationURL: publicIssuer + "/authorize",
		OIDCTokenURL:         srv.URL + "/custom-token",
		OIDCJWKSURL:          srv.URL + "/custom-jwks",
		OIDCClientID:         "radar",
		OIDCClientSecret:     "secret",
		OIDCRedirectURL:      "http://localhost/callback",
	}, "")
	if err != nil {
		t.Fatalf("expected success with internal issuer and overrides, got: %v", err)
	}

	if got, want := h.oauth.Endpoint.AuthURL, publicIssuer+"/authorize"; got != want {
		t.Errorf("AuthURL = %q, want %q", got, want)
	}
	if got, want := h.oauth.Endpoint.TokenURL, srv.URL+"/custom-token"; got != want {
		t.Errorf("TokenURL = %q, want %q", got, want)
	}
	if got, want := h.endSessionEndpoint, publicIssuer+"/logout"; got != want {
		t.Errorf("endSessionEndpoint = %q, want %q", got, want)
	}
	if !h.backchannelLogoutSupported || !h.backchannelLogoutSessionSupported {
		t.Error("backchannel logout support flags should come from discovery")
	}
}

func TestNewOIDCHandler_ExplicitEndpointsDoNotRequireDiscovery(t *testing.T) {
	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:                 "oidc",
		OIDCIssuer:           "https://auth.example.com",
		OIDCAuthorizationURL: "https://auth.example.com/authorize",
		OIDCTokenURL:         "http://auth.authentik.svc/token",
		OIDCUserInfoURL:      "http://auth.authentik.svc/userinfo",
		OIDCJWKSURL:          "http://auth.authentik.svc/jwks",
		OIDCClientID:         "radar",
		OIDCClientSecret:     "secret",
		OIDCRedirectURL:      "http://localhost/callback",
	}, "")
	if err != nil {
		t.Fatalf("expected explicit endpoints to initialize without discovery: %v", err)
	}
	if got, want := h.oauth.Endpoint.AuthURL, "https://auth.example.com/authorize"; got != want {
		t.Errorf("AuthURL = %q, want %q", got, want)
	}
	if got, want := h.oauth.Endpoint.TokenURL, "http://auth.authentik.svc/token"; got != want {
		t.Errorf("TokenURL = %q, want %q", got, want)
	}
	if got, want := h.provider.UserInfoEndpoint(), "http://auth.authentik.svc/userinfo"; got != want {
		t.Errorf("UserInfoEndpoint = %q, want %q", got, want)
	}
}

func TestResolveOIDCProviderMetadata_ExplicitEndpointsSeedSigningAlgorithms(t *testing.T) {
	metadata, err := resolveOIDCProviderMetadata(context.Background(), Config{
		Mode:                 "oidc",
		OIDCIssuer:           "https://auth.example.com",
		OIDCAuthorizationURL: "https://auth.example.com/authorize",
		OIDCTokenURL:         "http://auth.authentik.svc/token",
		OIDCJWKSURL:          "http://auth.authentik.svc/jwks",
		OIDCClientID:         "radar",
	})
	if err != nil {
		t.Fatalf("resolveOIDCProviderMetadata: %v", err)
	}
	// Without discovery the verifier would default to RS256 only; the resolver
	// must seed the supported set so non-RS256 IdPs still verify.
	got := map[string]bool{}
	for _, alg := range metadata.ProviderConfig.Algorithms {
		got[alg] = true
	}
	for _, want := range []string{oidc.RS256, oidc.ES256, oidc.PS256, oidc.EdDSA} {
		if !got[want] {
			t.Errorf("explicit-endpoint algorithms %v missing %q", metadata.ProviderConfig.Algorithms, want)
		}
	}
}

func TestNewOIDCHandler_ExplicitEndpointsVerifyES256Token(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	issuer := "https://auth.example.com"
	provider := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{
			PublicKey: priv.Public(),
			KeyID:     "es-key",
			Algorithm: oidc.ES256,
		}},
	}
	provider.SetIssuer(issuer)
	srv := httptest.NewServer(provider)
	defer srv.Close()

	// Explicit-endpoint mode (no internalIssuer) skips discovery, so without the
	// seeded algorithm set go-oidc would narrow the verifier to RS256 and reject
	// this valid ES256 token.
	h, err := NewOIDCHandler(context.Background(), Config{
		Mode:                 "oidc",
		OIDCIssuer:           issuer,
		OIDCAuthorizationURL: issuer + "/auth",
		OIDCTokenURL:         srv.URL + "/token",
		OIDCJWKSURL:          srv.URL + "/keys",
		OIDCClientID:         "radar",
		OIDCClientSecret:     "secret",
		OIDCRedirectURL:      "http://localhost/callback",
	}, "")
	if err != nil {
		t.Fatalf("expected explicit-endpoint init to succeed: %v", err)
	}

	claims := fmt.Sprintf(`{
		"iss": %q,
		"aud": "radar",
		"sub": "alice",
		"exp": %d
	}`, issuer, time.Now().Add(time.Hour).Unix())
	rawIDToken := oidctest.SignIDToken(priv, "es-key", oidc.ES256, claims)
	if _, err := h.verifier.Verify(context.Background(), rawIDToken); err != nil {
		t.Fatalf("expected ES256 token verification to succeed in explicit-endpoint mode: %v", err)
	}
}

// fakeIDP is a controllable OIDC provider for exercising the full callback flow
// (discovery + JWKS via oidctest.Server, plus a token endpoint we drive).
type fakeIDP struct {
	server      *httptest.Server
	signer      crypto.Signer
	keyID       string
	alg         string
	issuer      string
	tokenStatus int    // if >= 400, /token returns an error instead of a token
	omitIDToken bool   // if true, /token omits the id_token from the response
	claims      string // id_token claims JSON; empty means defaultClaims()
	gotVerifier string // code_verifier the /token endpoint received (PKCE)
}

func newFakeIDP(t *testing.T, alg string) *fakeIDP {
	t.Helper()
	var signer crypto.Signer
	var err error
	if alg == oidc.ES256 {
		signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	} else {
		signer, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}

	idp := &fakeIDP{signer: signer, keyID: "test-key", alg: alg}
	inner := &oidctest.Server{
		Algorithms: []string{alg},
		PublicKeys: []oidctest.PublicKey{{PublicKey: signer.Public(), KeyID: idp.keyID, Algorithm: alg}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		idp.gotVerifier = r.FormValue("code_verifier")
		if idp.tokenStatus >= 400 {
			w.WriteHeader(idp.tokenStatus)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		resp := map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600}
		if !idp.omitIDToken {
			claims := idp.claims
			if claims == "" {
				claims = idp.defaultClaims()
			}
			resp["id_token"] = oidctest.SignIDToken(signer, idp.keyID, alg, claims)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.Handle("/", inner)

	idp.server = httptest.NewServer(mux)
	inner.SetIssuer(idp.server.URL)
	idp.issuer = idp.server.URL
	t.Cleanup(idp.server.Close)
	return idp
}

func (f *fakeIDP) defaultClaims() string {
	return fmt.Sprintf(`{"iss":%q,"aud":"radar","email":"alice@example.com","sub":"alice","exp":%d,"groups":["dev","ops"]}`,
		f.issuer, time.Now().Add(time.Hour).Unix())
}

func (f *fakeIDP) newHandler(t *testing.T, cfg Config) *OIDCHandler {
	t.Helper()
	return f.newHandlerUnderBasePath(t, cfg, "")
}

func (f *fakeIDP) newHandlerUnderBasePath(t *testing.T, cfg Config, basePath string) *OIDCHandler {
	t.Helper()
	cfg.Mode = "oidc"
	cfg.OIDCIssuer = f.issuer
	if cfg.OIDCClientID == "" {
		cfg.OIDCClientID = "radar"
	}
	cfg.OIDCClientSecret = "secret"
	cfg.OIDCRedirectURL = "https://radar.example.com/auth/callback"
	if cfg.OIDCGroupsClaim == "" {
		cfg.OIDCGroupsClaim = "groups"
	}
	if cfg.CookieTTL == 0 {
		// Without a TTL the session cookie expires at the current second, so a
		// clock tick between issue and parse would flake the callback asserts.
		cfg.CookieTTL = time.Hour
	}
	h, err := NewOIDCHandler(context.Background(), cfg, basePath)
	if err != nil {
		t.Fatalf("NewOIDCHandler: %v", err)
	}
	return h
}

func runCallback(h *OIDCHandler) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/auth/callback?state=s&code=c", nil)
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "s"})
	w := httptest.NewRecorder()
	h.HandleCallback(w, r)
	return w
}

func sessionFrom(t *testing.T, h *OIDCHandler, w *httptest.ResponseRecorder) *Session {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	s := ParseSessionCookie(r, h.cfg.Secret)
	if s == nil {
		t.Fatal("no session parsed from response cookies")
	}
	return s
}

func TestHandleCallback_Matrix(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	tests := []struct {
		name       string
		alg        string
		cfg        Config
		setup      func(f *fakeIDP)
		wantStatus int
		wantUser   string
		wantGroups []string
	}{
		{name: "success RS256", alg: oidc.RS256, wantStatus: http.StatusFound, wantUser: "alice@example.com", wantGroups: []string{"dev", "ops"}},
		{name: "success ES256", alg: oidc.ES256, wantStatus: http.StatusFound, wantUser: "alice@example.com"},
		{name: "token endpoint error", alg: oidc.RS256, setup: func(f *fakeIDP) { f.tokenStatus = 500 }, wantStatus: http.StatusInternalServerError},
		{name: "no id_token in response", alg: oidc.RS256, setup: func(f *fakeIDP) { f.omitIDToken = true }, wantStatus: http.StatusInternalServerError},
		{name: "wrong issuer", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":"https://evil.example.com","aud":"radar","email":"a@b.com","exp":%d}`, future)
		}, wantStatus: http.StatusUnauthorized},
		{name: "wrong audience", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":"someone-else","email":"a@b.com","exp":%d}`, f.issuer, future)
		}, wantStatus: http.StatusUnauthorized},
		{name: "expired token", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":"radar","email":"a@b.com","exp":%d}`, f.issuer, past)
		}, wantStatus: http.StatusUnauthorized},
		{name: "no username claim", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":"radar","exp":%d}`, f.issuer, future)
		}, wantStatus: http.StatusBadRequest},
		{name: "sub fallback when no email", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":"radar","sub":"bob","exp":%d}`, f.issuer, future)
		}, wantStatus: http.StatusFound, wantUser: "bob"},
		{name: "groups as single string", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":"radar","email":"a@b.com","exp":%d,"groups":"solo"}`, f.issuer, future)
		}, wantStatus: http.StatusFound, wantUser: "a@b.com", wantGroups: []string{"solo"}},
		{name: "aud as array containing client id (Keycloak/Azure style)", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":["radar","account"],"azp":"radar","email":"a@b.com","exp":%d}`, f.issuer, future)
		}, wantStatus: http.StatusFound, wantUser: "a@b.com"},
		{name: "aud array without client id", alg: oidc.RS256, setup: func(f *fakeIDP) {
			f.claims = fmt.Sprintf(`{"iss":%q,"aud":["account","other"],"email":"a@b.com","exp":%d}`, f.issuer, future)
		}, wantStatus: http.StatusUnauthorized},
		{name: "username and groups prefixes applied", alg: oidc.RS256, cfg: Config{OIDCUsernamePrefix: "oidc:", OIDCGroupsPrefix: "oidc:"},
			wantStatus: http.StatusFound, wantUser: "oidc:alice@example.com", wantGroups: []string{"oidc:dev", "oidc:ops"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFakeIDP(t, tc.alg)
			if tc.setup != nil {
				tc.setup(idp)
			}
			h := idp.newHandler(t, tc.cfg)
			w := runCallback(h)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantStatus == http.StatusFound && w.Header().Get("Location") != "/" {
				t.Errorf("Location = %q, want /", w.Header().Get("Location"))
			}
			if tc.wantUser != "" {
				s := sessionFrom(t, h, w)
				if s.User.Username != tc.wantUser {
					t.Errorf("username = %q, want %q", s.User.Username, tc.wantUser)
				}
				if tc.wantGroups != nil && !equalStringSlices(s.User.Groups, tc.wantGroups) {
					t.Errorf("groups = %v, want %v", s.User.Groups, tc.wantGroups)
				}
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUnreachableInternalEndpoints(t *testing.T) {
	cfg := Config{
		OIDCIssuer:         "https://sso.example.com",
		OIDCInternalIssuer: "http://sso.internal.svc:5556",
	}
	// token endpoint is on a different host (rewrite could not derive it) -> flagged;
	// jwks/userinfo are on the internal host -> reachable.
	md := oidc.ProviderConfig{
		TokenURL:    "https://token.example.com/token",
		JWKSURL:     "http://sso.internal.svc:5556/keys",
		UserInfoURL: "http://sso.internal.svc:5556/userinfo",
	}
	if got := unreachableInternalEndpoints(cfg, md); !equalStringSlices(got, []string{"token"}) {
		t.Errorf("got %v, want [token]", got)
	}

	// An explicit override is trusted as configured -> not flagged.
	withOverride := cfg
	withOverride.OIDCTokenURL = "https://token.example.com/token"
	if got := unreachableInternalEndpoints(withOverride, md); len(got) != 0 {
		t.Errorf("explicit override should be trusted, got %v", got)
	}

	// No internal issuer -> nothing to check.
	if got := unreachableInternalEndpoints(Config{OIDCIssuer: "https://sso.example.com"}, md); got != nil {
		t.Errorf("no internal issuer should return nil, got %v", got)
	}
}

func TestResolveOIDCProviderMetadata_IssuerMismatchRejected(t *testing.T) {
	idp := newFakeIDP(t, oidc.RS256) // discovery reports issuer == idp.issuer

	// Discovery is fetched from the internal issuer but reports idp.issuer, which
	// must equal the configured (public) issuer. Here it does not.
	_, err := resolveOIDCProviderMetadata(context.Background(), Config{
		OIDCIssuer:         "https://configured-but-wrong.example.com",
		OIDCInternalIssuer: idp.issuer,
		OIDCClientID:       "radar",
	})
	if err == nil {
		t.Fatal("expected issuer-mismatch to be rejected")
	}
	if !strings.Contains(err.Error(), "did not match") {
		t.Errorf("error = %v, want issuer-mismatch", err)
	}
}

func TestValidateOIDCProviderMetadata(t *testing.T) {
	valid := oidc.ProviderConfig{
		AuthURL:  "https://idp.example.com/auth",
		TokenURL: "https://idp.example.com/token",
		JWKSURL:  "https://idp.example.com/keys",
	}
	if err := validateOIDCProviderMetadata(valid); err != nil {
		t.Fatalf("valid metadata should pass: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*oidc.ProviderConfig)
	}{
		{"missing authorization endpoint", func(m *oidc.ProviderConfig) { m.AuthURL = "" }},
		{"missing token endpoint", func(m *oidc.ProviderConfig) { m.TokenURL = "" }},
		{"missing jwks endpoint", func(m *oidc.ProviderConfig) { m.JWKSURL = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := valid
			tc.mut(&m)
			if err := validateOIDCProviderMetadata(m); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
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
	}, "")
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
	}, "")
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

// A completed login must land inside the app. Under a no-strip subpath ingress
// only {basePath}/* is routed to Radar, so redirecting to a bare "/" would drop
// the user onto whatever else owns the origin root right after authenticating.
func TestHandleCallback_RedirectsUnderBasePath(t *testing.T) {
	for _, basePath := range []string{"", "/radar", "/tools/radar"} {
		t.Run("basePath="+basePath, func(t *testing.T) {
			idp := newFakeIDP(t, oidc.RS256)
			h := idp.newHandlerUnderBasePath(t, Config{}, basePath)
			w := runCallback(h)

			if w.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302 (body: %s)", w.Code, w.Body.String())
			}
			if got, want := w.Header().Get("Location"), basePath+"/"; got != want {
				t.Errorf("Location = %q, want %q", got, want)
			}
		})
	}
}

// cookieByName returns the cookie with the given name from a recorded response,
// or nil if absent.
func cookieByName(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// loginRedirectQuery runs HandleLogin and returns the parsed query of the
// resulting authorization redirect URL.
func loginRedirectQuery(t *testing.T, w *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if w.Code != http.StatusFound {
		t.Fatalf("HandleLogin status = %d, want 302", w.Code)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect Location: %v", err)
	}
	return loc.Query()
}

func TestHandleLogin_PKCEEnabled(t *testing.T) {
	h := newTestOIDCHandler()
	h.pkceEnabled = true
	h.oauth = oauth2.Config{
		ClientID:    "test-client",
		Endpoint:    oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth"},
		RedirectURL: "http://localhost:9280/auth/callback",
		Scopes:      []string{"openid"},
	}

	r := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	h.HandleLogin(w, r)

	verifierCookie := cookieByName(w, oidcVerifierCookieName)
	if verifierCookie == nil || verifierCookie.Value == "" {
		t.Fatal("expected verifier cookie to be set when PKCE enabled")
	}
	// Verifier cookie must mirror the state cookie's flow attributes.
	if verifierCookie.MaxAge != 300 || !verifierCookie.HttpOnly || verifierCookie.SameSite != http.SameSiteLaxMode || verifierCookie.Path != "/" {
		t.Errorf("verifier cookie attributes drift from state cookie: %+v", verifierCookie)
	}

	q := loginRedirectQuery(t, w)
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	// The challenge must be derived from the exact verifier stored in the cookie.
	want := oauth2.S256ChallengeFromVerifier(verifierCookie.Value)
	if got := q.Get("code_challenge"); got != want {
		t.Errorf("code_challenge = %q, want %q (S256 of cookie verifier)", got, want)
	}
}

func TestHandleLogin_PKCEDisabled(t *testing.T) {
	h := newTestOIDCHandler() // pkceEnabled defaults to false
	h.oauth = oauth2.Config{
		ClientID:    "test-client",
		Endpoint:    oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth"},
		RedirectURL: "http://localhost:9280/auth/callback",
		Scopes:      []string{"openid"},
	}

	r := httptest.NewRequest("GET", "/auth/login", nil)
	w := httptest.NewRecorder()
	h.HandleLogin(w, r)

	if c := cookieByName(w, oidcVerifierCookieName); c != nil {
		t.Errorf("no verifier cookie expected when PKCE disabled, got %+v", c)
	}
	q := loginRedirectQuery(t, w)
	if q.Get("code_challenge") != "" || q.Get("code_challenge_method") != "" {
		t.Errorf("no PKCE params expected when disabled, got challenge=%q method=%q", q.Get("code_challenge"), q.Get("code_challenge_method"))
	}
}

func TestHandleCallback_PKCEMissingVerifier(t *testing.T) {
	h := newTestOIDCHandler()
	h.pkceEnabled = true

	// Valid state, but no verifier cookie present.
	r := httptest.NewRequest("GET", "/auth/callback?state=abc&code=xyz", nil)
	r.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "abc"})
	w := httptest.NewRecorder()
	h.HandleCallback(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PKCE verifier") {
		t.Errorf("expected PKCE verifier error, got %q", w.Body.String())
	}
}

// TestPKCEFullFlow drives the real login→callback flow against a fake IdP with
// PKCE enabled: HandleLogin mints the verifier + challenge, and the extracted
// state + verifier cookie are fed back into HandleCallback. Asserts the exchange
// succeeds AND the token endpoint received the code_verifier bound to the
// challenge the browser was redirected with.
func TestPKCEFullFlow(t *testing.T) {
	idp := newFakeIDP(t, oidc.RS256)
	h := idp.newHandler(t, Config{OIDCEnablePKCE: true})

	// Drive HandleLogin to obtain the real state + verifier the handler generated.
	lw := httptest.NewRecorder()
	h.HandleLogin(lw, httptest.NewRequest("GET", "/auth/login", nil))
	q := loginRedirectQuery(t, lw)
	state := q.Get("state")
	if state == "" {
		t.Fatal("no state in login redirect")
	}
	verifierCookie := cookieByName(lw, oidcVerifierCookieName)
	if verifierCookie == nil || verifierCookie.Value == "" {
		t.Fatal("no verifier cookie from HandleLogin")
	}
	if got, want := q.Get("code_challenge"), oauth2.S256ChallengeFromVerifier(verifierCookie.Value); got != want {
		t.Fatalf("login challenge %q not bound to verifier cookie (want %q)", got, want)
	}

	// Feed BOTH the state and the verifier cookie into HandleCallback.
	cr := httptest.NewRequest("GET", "/auth/callback?state="+url.QueryEscape(state)+"&code=c", nil)
	cr.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: state})
	cr.AddCookie(verifierCookie)
	cw := httptest.NewRecorder()
	h.HandleCallback(cw, cr)

	if cw.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302 (body: %s)", cw.Code, cw.Body.String())
	}
	if idp.gotVerifier != verifierCookie.Value {
		t.Errorf("token endpoint code_verifier = %q, want %q", idp.gotVerifier, verifierCookie.Value)
	}
	// Verifier cookie must be cleared after a successful callback.
	if c := cookieByName(cw, oidcVerifierCookieName); c == nil || c.MaxAge != -1 {
		t.Errorf("verifier cookie should be cleared (MaxAge -1) after callback, got %+v", c)
	}
}

// TestPKCEDisabledSendsNoVerifier confirms the opt-out path is byte-for-byte
// unchanged: with PKCE off, the token endpoint receives no code_verifier.
func TestPKCEDisabledSendsNoVerifier(t *testing.T) {
	idp := newFakeIDP(t, oidc.RS256)
	h := idp.newHandler(t, Config{}) // PKCE off
	w := runCallback(h)
	if w.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302 (body: %s)", w.Code, w.Body.String())
	}
	if idp.gotVerifier != "" {
		t.Errorf("token endpoint received code_verifier %q with PKCE disabled", idp.gotVerifier)
	}
}
