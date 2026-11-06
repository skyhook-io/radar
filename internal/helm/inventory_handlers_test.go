package helm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func inventoryRouter(h *Handlers) http.Handler {
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	return r
}

func TestObservedReleaseRouteNeedsTheCache(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/helm/inventory/apps/api", nil)
	inventoryRouter(&Handlers{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when objects can't be read", rec.Code)
	}
}

func TestWriteInventoryError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		status   int
		wantCode string
	}{
		{"cache not ready", ErrObjectsUnavailable, http.StatusServiceUnavailable, ""},
		{"connection changed", ErrConnectionChanged, http.StatusConflict, "connection_changed"},
		{"other", errors.New("boom"), http.StatusInternalServerError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeInventoryError(rec, httptest.NewRequest(http.MethodGet, "/", nil), tc.err)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error_code"] != tc.wantCode {
				t.Fatalf("error_code = %q, want %q", body["error_code"], tc.wantCode)
			}
		})
	}
}

func TestWriteInventoryErrorSaysNothingToACallerWhoLeft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	writeInventoryError(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx), errors.New("context canceled"))
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
