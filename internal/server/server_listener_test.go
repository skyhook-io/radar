package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalTCPHandlerCloudModeExposesExactHealthOnly(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	var reached []string
	handler := localTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/api/health", want: http.StatusNoContent},
		{path: "/api/health?probe=1", want: http.StatusNoContent},
		{path: "/api/health/", want: http.StatusNotFound},
		{path: "/auth/login", want: http.StatusNotFound},
		{path: "/api/topology", want: http.StatusNotFound},
		{path: "/", want: http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("X-Forwarded-User", "attacker")
			req.Header.Set("X-Forwarded-Groups", "cloud:owner")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
	if len(reached) != 2 || reached[0] != "/api/health" || reached[1] != "/api/health" {
		t.Fatalf("underlying handler reached for %v, want only exact health paths", reached)
	}
}

func TestLocalTCPHandlerNonCloudPreservesApplication(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	handler := localTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	for _, path := range []string{"/api/health", "/auth/login", "/api/topology", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("path %q status = %d, want %d", path, rec.Code, http.StatusAccepted)
		}
	}
}
