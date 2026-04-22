package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// echoUser is a handler that returns the authenticated user as JSON, or 204 if no user.
func echoUser(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	json.NewEncoder(w).Encode(user)
}

func proxyConfig() Config {
	return Config{
		Mode:         "proxy",
		Secret:       "test-secret",
		CookieTTL:    1 * time.Hour,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
	}
}

func TestMiddleware_ExemptPaths(t *testing.T) {
	mw := Authenticate(proxyConfig())
	handler := mw(http.HandlerFunc(echoUser))

	tests := []struct {
		path string
		want int
	}{
		{"/api/health", http.StatusNoContent},      // exempt
		{"/api/connection", http.StatusNoContent},   // exempt
		{"/auth/login", http.StatusNoContent},       // exempt
		{"/auth/callback", http.StatusNoContent},    // exempt
		{"/", http.StatusNoContent},                 // static asset — exempt
		{"/index.html", http.StatusNoContent},       // static asset — exempt
		{"/assets/main.js", http.StatusNoContent},   // static asset — exempt
		{"/api/resources/pods", http.StatusUnauthorized}, // requires auth
		{"/api/topology", http.StatusUnauthorized},       // requires auth
		{"/mcp", http.StatusUnauthorized},                // requires auth
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("path %s: status = %d, want %d", tt.path, rec.Code, tt.want)
			}
		})
	}
}

func TestMiddleware_ProxyHeaders(t *testing.T) {
	mw := Authenticate(proxyConfig())
	handler := mw(http.HandlerFunc(echoUser))

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Forwarded-Groups", "devs, admins")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var user User
	json.NewDecoder(rec.Body).Decode(&user)
	if user.Username != "alice" {
		t.Errorf("username = %q, want %q", user.Username, "alice")
	}
	if len(user.Groups) != 2 || user.Groups[0] != "devs" || user.Groups[1] != "admins" {
		t.Errorf("groups = %v, want [devs admins]", user.Groups)
	}

	// Should also set a session cookie
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == DefaultCookieName {
			found = true
		}
	}
	if !found {
		t.Error("proxy auth should set session cookie")
	}
}

func TestMiddleware_ProxyHeaders_NoUser(t *testing.T) {
	mw := Authenticate(proxyConfig())
	handler := mw(http.HandlerFunc(echoUser))

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	// No proxy headers
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "authentication required" {
		t.Errorf("error = %q, want %q", resp["error"], "authentication required")
	}
	if resp["authMode"] != "proxy" {
		t.Errorf("authMode = %q, want %q", resp["authMode"], "proxy")
	}
}

func TestMiddleware_SessionCookie(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Create a valid session cookie
	user := &User{Username: "bob", Groups: []string{"ops"}}
	cookie := CreateSessionCookie(user, NewSessionID(), "", cfg.Secret, cfg.CookieTTL, false)

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var parsed User
	json.NewDecoder(rec.Body).Decode(&parsed)
	if parsed.Username != "bob" {
		t.Errorf("username = %q, want %q", parsed.Username, "bob")
	}
}

func TestMiddleware_SessionCookie_TakesPrecedence(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Cookie says "bob", proxy header says "alice"
	cookie := CreateSessionCookie(&User{Username: "bob"}, NewSessionID(), "", cfg.Secret, cfg.CookieTTL, false)

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-Forwarded-User", "alice")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var parsed User
	json.NewDecoder(rec.Body).Decode(&parsed)
	if parsed.Username != "bob" {
		t.Errorf("cookie should take precedence: got %q, want %q", parsed.Username, "bob")
	}
}

func TestMiddleware_SoftAuthPath(t *testing.T) {
	mw := Authenticate(proxyConfig())
	handler := mw(http.HandlerFunc(echoUser))

	// /api/auth/me without auth should pass through (not 401)
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("soft-auth path should pass through: status = %d, want 204", rec.Code)
	}
}

func TestMiddleware_SoftAuthPath_WithUser(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// /api/auth/me with valid cookie should include user
	cookie := CreateSessionCookie(&User{Username: "carol"}, NewSessionID(), "", cfg.Secret, cfg.CookieTTL, false)
	req := httptest.NewRequest("GET", "/api/auth/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var parsed User
	json.NewDecoder(rec.Body).Decode(&parsed)
	if parsed.Username != "carol" {
		t.Errorf("username = %q, want %q", parsed.Username, "carol")
	}
}

func TestMiddleware_OIDCMode_NoCookie(t *testing.T) {
	cfg := Config{Mode: "oidc", Secret: "test-secret"}
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["authMode"] != "oidc" {
		t.Errorf("authMode = %q, want %q", resp["authMode"], "oidc")
	}
}

func TestMiddleware_ProxyHeaders_GroupsTrimmed(t *testing.T) {
	mw := Authenticate(proxyConfig())
	handler := mw(http.HandlerFunc(echoUser))

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Forwarded-Groups", " devs , , admins ")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var user User
	json.NewDecoder(rec.Body).Decode(&user)
	// Empty strings should be filtered, spaces trimmed
	if len(user.Groups) != 2 {
		t.Errorf("groups = %v, want 2 groups (empty filtered)", user.Groups)
	}
	if user.Groups[0] != "devs" || user.Groups[1] != "admins" {
		t.Errorf("groups = %v, want [devs admins]", user.Groups)
	}
}

func TestUserFromContext_NoUser(t *testing.T) {
	ctx := context.Background()
	user := UserFromContext(ctx)
	if user != nil {
		t.Error("UserFromContext should return nil for context without user")
	}
}

func TestUserFromContext_WithUser(t *testing.T) {
	user := &User{Username: "alice", Groups: []string{"devs"}}
	ctx := ContextWithUser(context.Background(), user)
	got := UserFromContext(ctx)
	if got == nil {
		t.Fatal("UserFromContext returned nil")
	}
	if got.Username != "alice" {
		t.Errorf("username = %q, want %q", got.Username, "alice")
	}
}

func TestIsExemptPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/api/health", true},
		{"/api/health/detailed", true},
		{"/api/connection", true},
		{"/api/connection/retry", true},
		{"/auth/login", true},
		{"/auth/callback", true},
		{"/", true},
		{"/index.html", true},
		{"/assets/main.js", true},
		{"/api/resources/pods", false},
		{"/api/topology", false},
		{"/api/auth/me", false},
		{"/mcp", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isExemptPath(tt.path)
			if got != tt.want {
				t.Errorf("isExemptPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestIsExemptPath_CloudMode verifies that cloud-mode narrows the exempt
// set to /api/health + /auth/*. Under cloud-mode, static assets,
// /api/connection, and /debug/pprof/* must all require auth — a regression
// that silently re-added them would let an unauthenticated request through
// the Cloud tunnel reach those paths.
func TestIsExemptPath_CloudMode(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")

	tests := []struct {
		path string
		want bool
	}{
		{"/api/health", true},
		{"/auth/login", true},
		{"/auth/callback", true},
		// Under non-cloud mode these would be exempt. Under cloud-mode
		// they must require auth.
		{"/api/connection", false},
		{"/api/connection/retry", false},
		{"/", false},
		{"/index.html", false},
		{"/assets/main.js", false},
		{"/debug/pprof/heap", false},
		{"/debug/pprof/goroutine", false},
		{"/api/resources/pods", false},
		{"/api/topology", false},
		{"/mcp", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isExemptPath(tt.path)
			if got != tt.want {
				t.Errorf("isExemptPath(%q) under cloud-mode = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsSoftAuthPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/api/auth/me", true},
		{"/api/resources/pods", false},
		{"/api/auth/me/extra", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isSoftAuthPath(tt.path)
			if got != tt.want {
				t.Errorf("isSoftAuthPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// --- Sliding TTL tests ---

// makeCookieWithExpiry creates a signed session cookie with a specific ExpiresAt.
func makeCookieWithExpiry(user *User, sid, secret string, expiresAt time.Time) *http.Cookie {
	// Use the public constructor, but we need to craft a specific expiry.
	// We compute the TTL that would produce the desired ExpiresAt from now.
	ttl := time.Until(expiresAt)
	return CreateSessionCookie(user, sid, "", secret, ttl, false)
}

func TestMiddleware_SlidingTTL_ReissuesPastHalfLife(t *testing.T) {
	cfg := proxyConfig() // CookieTTL = 1h
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Cookie that expires in 15 minutes (past half-life of 1h)
	sid := NewSessionID()
	cookie := makeCookieWithExpiry(&User{Username: "alice"}, sid, cfg.Secret, time.Now().Add(15*time.Minute))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Should have re-issued a session cookie
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName {
			found = true
			// New cookie should have ~1h MaxAge (the configured TTL)
			if c.MaxAge < 3500 || c.MaxAge > 3700 {
				t.Errorf("re-issued cookie MaxAge = %d, want ~3600", c.MaxAge)
			}
		}
	}
	if !found {
		t.Error("expected Set-Cookie for sliding TTL re-issue past half-life")
	}
}

func TestMiddleware_SlidingTTL_NoReissueWhenFresh(t *testing.T) {
	cfg := proxyConfig() // CookieTTL = 1h
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Cookie that expires in 50 minutes (within first half of 1h TTL — fresh)
	sid := NewSessionID()
	cookie := makeCookieWithExpiry(&User{Username: "alice"}, sid, cfg.Secret, time.Now().Add(50*time.Minute))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Should NOT have set a cookie (still fresh)
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName {
			t.Error("should not re-issue cookie when still in fresh half of TTL")
		}
	}
}

func TestMiddleware_SlidingTTL_ReissuesOnTTLDowngrade(t *testing.T) {
	cfg := proxyConfig()
	cfg.CookieTTL = 4 * time.Hour // simulate downgrade to 4h
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Old cookie with 20h remaining (issued under 24h TTL)
	sid := NewSessionID()
	cookie := makeCookieWithExpiry(&User{Username: "alice"}, sid, cfg.Secret, time.Now().Add(20*time.Hour))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Should snap to new TTL
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName {
			found = true
			// New cookie should have ~4h MaxAge
			expected := int(4 * time.Hour / time.Second)
			if c.MaxAge < expected-100 || c.MaxAge > expected+100 {
				t.Errorf("re-issued cookie MaxAge = %d, want ~%d", c.MaxAge, expected)
			}
		}
	}
	if !found {
		t.Error("expected Set-Cookie for TTL downgrade snap")
	}
}

func TestMiddleware_SlidingTTL_PreservesSID(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Cookie past half-life so it gets re-issued
	sid := "deadbeef01234567deadbeef01234567"
	cookie := makeCookieWithExpiry(&User{Username: "alice"}, sid, cfg.Secret, time.Now().Add(10*time.Minute))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Parse the re-issued cookie to verify sid
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName {
			parseReq := httptest.NewRequest("GET", "/", nil)
			parseReq.AddCookie(c)
			session := ParseSessionCookie(parseReq, cfg.Secret)
			if session == nil {
				t.Fatal("failed to parse re-issued cookie")
			}
			if session.SID != sid {
				t.Errorf("re-issued SID = %q, want %q (should be preserved)", session.SID, sid)
			}
			return
		}
	}
	t.Error("expected Set-Cookie for sliding re-issue")
}

func TestMiddleware_SlidingTTL_PreservesIDToken(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Cookie past half-life with an ID token (needed for RP-Initiated Logout)
	sid := NewSessionID()
	idToken := "eyJhbGciOiJSUzI1NiJ9.test-payload.test-sig"
	ttl := time.Until(time.Now().Add(10 * time.Minute))
	cookie := CreateSessionCookie(&User{Username: "alice"}, sid, idToken, cfg.Secret, ttl, false)

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Parse the re-issued cookie to verify IDToken survived
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName {
			parseReq := httptest.NewRequest("GET", "/", nil)
			parseReq.AddCookie(c)
			session := ParseSessionCookie(parseReq, cfg.Secret)
			if session == nil {
				t.Fatal("failed to parse re-issued cookie")
			}
			if session.IDToken != idToken {
				t.Errorf("re-issued IDToken = %q, want %q (must survive for RP-Initiated Logout)", session.IDToken, idToken)
			}
			return
		}
	}
	t.Error("expected Set-Cookie for sliding re-issue")
}

func TestMiddleware_SlidingTTL_LegacyCookieMintsSID(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Simulate a legacy cookie without SID by crafting a cookie whose parsed SID is ""
	// We do this by using the old schema format (no "s" field)
	legacyCookie := makeLegacyCookie("alice", cfg.Secret, time.Now().Add(10*time.Minute))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(legacyCookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Re-issued cookie should have a minted SID
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName {
			parseReq := httptest.NewRequest("GET", "/", nil)
			parseReq.AddCookie(c)
			session := ParseSessionCookie(parseReq, cfg.Secret)
			if session == nil {
				t.Fatal("failed to parse re-issued cookie")
			}
			if session.SID == "" {
				t.Error("re-issued cookie should have a minted SID for legacy cookie")
			}
			if len(session.SID) != 32 {
				t.Errorf("minted SID length = %d, want 32", len(session.SID))
			}
			return
		}
	}
	t.Error("expected Set-Cookie for legacy cookie re-issue")
}

// makeLegacyCookie creates a signed cookie using the old schema (no SID field).
func makeLegacyCookie(username, secret string, expiresAt time.Time) *http.Cookie {
	type legacyPayload struct {
		Username  string   `json:"u"`
		Groups    []string `json:"g,omitempty"`
		ExpiresAt int64    `json:"e"`
	}

	payload := legacyPayload{
		Username:  username,
		ExpiresAt: expiresAt.Unix(),
	}

	data, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(data)

	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprint(mac, encoded)
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return &http.Cookie{
		Name:  DefaultCookieName,
		Value: encoded + "." + sig,
	}
}

func TestMiddleware_NoReissueOnExpiredCookie(t *testing.T) {
	cfg := proxyConfig()
	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Expired cookie
	cookie := makeCookieWithExpiry(&User{Username: "alice"}, NewSessionID(), cfg.Secret, time.Now().Add(-1*time.Minute))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Should get 401, not 200 with re-issue
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for expired cookie", rec.Code)
	}
}

// --- Revocation tests ---

func TestMiddleware_RevokedSession_Returns401(t *testing.T) {
	cfg := proxyConfig()
	revoker := NewMemoryRevoker()
	defer revoker.Stop()
	cfg.Revoker = revoker

	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Create a valid session cookie
	sid := "revoke-me-sid-1234567890abcdef"
	cookie := CreateSessionCookie(&User{Username: "alice"}, sid, "", cfg.Secret, cfg.CookieTTL, false)

	// Revoke the session
	revoker.Revoke(sid, time.Now().Add(1*time.Hour))

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for revoked session", rec.Code)
	}

	// Response should indicate session was revoked
	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] != "session revoked" {
		t.Errorf("error = %q, want %q", resp["error"], "session revoked")
	}

	// Session cookie should be cleared
	for _, c := range rec.Result().Cookies() {
		if c.Name == DefaultCookieName && c.MaxAge == -1 {
			return // found the clear cookie
		}
	}
	t.Error("revoked session should clear the session cookie")
}

func TestMiddleware_NonRevokedSession_PassesThrough(t *testing.T) {
	cfg := proxyConfig()
	revoker := NewMemoryRevoker()
	defer revoker.Stop()
	cfg.Revoker = revoker

	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	// Create a valid session cookie (NOT revoked)
	sid := NewSessionID()
	cookie := CreateSessionCookie(&User{Username: "bob"}, sid, "", cfg.Secret, cfg.CookieTTL, false)

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for non-revoked session", rec.Code)
	}
}

func TestMiddleware_NoRevoker_SkipsCheck(t *testing.T) {
	cfg := proxyConfig()
	// No revoker configured (backchannel logout not enabled)
	cfg.Revoker = nil

	mw := Authenticate(cfg)
	handler := mw(http.HandlerFunc(echoUser))

	sid := NewSessionID()
	cookie := CreateSessionCookie(&User{Username: "carol"}, sid, "", cfg.Secret, cfg.CookieTTL, false)

	req := httptest.NewRequest("GET", "/api/topology", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when no revoker configured", rec.Code)
	}
}
