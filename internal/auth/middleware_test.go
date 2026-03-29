package auth

import (
	"context"
	"encoding/json"
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
	cookie := CreateSessionCookie(user, cfg.Secret, cfg.CookieTTL, false)

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
	cookie := CreateSessionCookie(&User{Username: "bob"}, cfg.Secret, cfg.CookieTTL, false)

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
	cookie := CreateSessionCookie(&User{Username: "carol"}, cfg.Secret, cfg.CookieTTL, false)
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
