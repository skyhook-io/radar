package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHandleOpenContextTabRejectsCrossOriginRequest(t *testing.T) {
	srv := &Server{contextTabs: true}
	req := httptest.NewRequest(http.MethodPost, "/api/contexts/prod/tab", nil)
	req.Header.Set("Origin", "https://example.com")
	response := httptest.NewRecorder()

	srv.handleOpenContextTab(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestWaitForContextTabReady(t *testing.T) {
	t.Run("reads published port", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ready")
		if err := os.WriteFile(path, []byte("43127\n"), 0o600); err != nil {
			t.Fatalf("write readiness file: %v", err)
		}

		port, err := waitForContextTabReady(t.Context(), path, make(chan struct{}), time.Second)
		if err != nil {
			t.Fatalf("waitForContextTabReady() error = %v", err)
		}
		if port != 43127 {
			t.Fatalf("port = %d, want 43127", port)
		}
	})

	t.Run("rejects invalid port", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ready")
		if err := os.WriteFile(path, []byte("70000\n"), 0o600); err != nil {
			t.Fatalf("write readiness file: %v", err)
		}

		_, err := waitForContextTabReady(t.Context(), path, make(chan struct{}), time.Second)
		if err == nil {
			t.Fatal("waitForContextTabReady() error = nil, want invalid port error")
		}
	})

	t.Run("reports process exit", func(t *testing.T) {
		done := make(chan struct{})
		close(done)
		_, err := waitForContextTabReady(t.Context(), filepath.Join(t.TempDir(), "missing"), done, time.Second)
		if err == nil {
			t.Fatal("waitForContextTabReady() error = nil, want process exit error")
		}
	})

	t.Run("reports cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := waitForContextTabReady(ctx, filepath.Join(t.TempDir(), "missing"), make(chan struct{}), time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForContextTabReady() error = %v, want context.Canceled", err)
		}
	})
}
