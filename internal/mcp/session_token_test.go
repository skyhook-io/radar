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

func TestResolveSessionToken(t *testing.T) {
	got, err := ResolveSessionToken("  ")
	if err != nil || got != "" {
		t.Fatalf("empty: got %q err %v", got, err)
	}
	got, err = ResolveSessionToken("secret")
	if err != nil || got != "secret" {
		t.Fatalf("literal: got %q err %v", got, err)
	}
	got, err = ResolveSessionToken("auto")
	if err != nil || got == "" || got == "auto" {
		t.Fatalf("auto: got %q err %v", got, err)
	}
}

func TestRequireBearer(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	handler := RequireBearer("secret", inner)

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

	passthrough := RequireBearer("", inner)
	called = false
	rec = httptest.NewRecorder()
	passthrough.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code == http.StatusUnauthorized || !called {
		t.Fatal("empty token unexpectedly enabled bearer authentication")
	}

	wrapped := RequireBearer("secret", NewHandler())
	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrapped handler status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestNewHandlerHasNoBearerGate(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("NewHandler unexpectedly required a bearer token")
	}
}
