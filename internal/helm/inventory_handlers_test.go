package helm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		{"access check failed", ErrAccessCheckFailed, http.StatusServiceUnavailable, "user_permission_check_failed"},
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

func TestWriteGetReleaseErrorTagsOnlyTheCallersDenial(t *testing.T) {
	forbidden := func(message string) error {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New(message))
	}
	for _, tc := range []struct {
		name     string
		err      error
		status   int
		wantCode string
	}{
		{"the caller's denial", forbidden(`User "dev" cannot list resource "secrets"`), http.StatusForbidden, ReleaseReadDeniedCode},
		{"Radar can't impersonate", forbidden(`User "radar" cannot impersonate resource "users"`), http.StatusForbidden, ""},
		{"rejected credentials", apierrors.NewUnauthorized("Unauthorized"), http.StatusForbidden, ""},
		{"server error", errors.New("etcd is down"), http.StatusInternalServerError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeGetReleaseError(rec, tc.err)
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || body["error_code"] != tc.wantCode {
				t.Fatalf("status %d code %q, want %d %q", rec.Code, body["error_code"], tc.status, tc.wantCode)
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
