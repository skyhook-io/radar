package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSessionToken(t *testing.T) {
	first, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 {
		t.Fatalf("token length = %d, want 43", len(first))
	}
	if first == second {
		t.Fatal("consecutive session tokens are equal")
	}
}

func TestRequireBearerToken(t *testing.T) {
	called := false
	handler := requireBearerToken("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, auth := range []string{"", "secret", "Bearer wrong"} {
		called = false
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", auth)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want %d", auth, rec.Code, http.StatusUnauthorized)
		}
		if called {
			t.Errorf("Authorization %q reached protected handler", auth)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Authorization %q: WWW-Authenticate = %q", auth, got)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("valid token: status = %d, called = %v", rec.Code, called)
	}
}

func TestNewHandlerRequiresConfiguredToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()
	NewHandler("secret").ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	rec = httptest.NewRecorder()
	NewHandler("").ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("empty token unexpectedly enabled bearer authentication")
	}
}
