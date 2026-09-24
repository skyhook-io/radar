package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPutTelemetryRejectsCrossSiteConsent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "http://localhost:9280/api/telemetry", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	(&Server{}).handlePutTelemetry(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestPutTelemetryRequiresExplicitChoice(t *testing.T) {
	for _, body := range []string{`{}`, `{"enabled":"yes"}`, `not json`} {
		r := httptest.NewRequest(http.MethodPut, "http://localhost:9280/api/telemetry", strings.NewReader(body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		(&Server{}).handlePutTelemetry(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, w.Code)
		}
	}
}

func TestTelemetryEventRejectsUnknownNames(t *testing.T) {
	for body, want := range map[string]int{
		`{"type":"view","name":"topology"}`:                   http.StatusNoContent,
		`{"type":"view","name":"resources:deployments"}`:      http.StatusNoContent,
		`{"type":"view","name":"/resources/secrets/prod/db"}`: http.StatusBadRequest,
		`{"type":"view","name":"resources:my-crds"}`:          http.StatusBadRequest,
		`{"type":"ui","name":"command_palette"}`:              http.StatusNoContent,
		`{"type":"ui","name":"ui_error:TopologyView"}`:        http.StatusNoContent,
		`{"type":"ui","name":"typed prod-db"}`:                http.StatusBadRequest,
		`{"type":"session"}`:                                  http.StatusNoContent,
		`{"type":"active","minutes":5}`:                       http.StatusNoContent,
		`{"type":"exfiltrate","name":"x"}`:                    http.StatusBadRequest,
		`{}`:                                                  http.StatusBadRequest,
	} {
		r := httptest.NewRequest(http.MethodPost, "http://localhost:9280/api/telemetry/event", strings.NewReader(body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(&Server{}).handleTelemetryEvent(w, r)
		if w.Code != want {
			t.Fatalf("body %s: status = %d, want %d", body, w.Code, want)
		}
	}
}

func TestTelemetryMiddlewarePassesThroughWhenOff(t *testing.T) {
	called := false
	h := (&Server{}).telemetryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if !called || w.Code != http.StatusTeapot {
		t.Fatalf("handler not reached or status changed: %d", w.Code)
	}
}

func TestTelemetryEventRejectsCrossSiteAndNonJSON(t *testing.T) {
	cases := []struct {
		name, site, contentType string
		want                    int
	}{
		{"cross-site form post", "cross-site", "text/plain", http.StatusForbidden},
		{"same-origin but not JSON", "same-origin", "text/plain", http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "http://localhost:9280/api/telemetry/event", strings.NewReader(`{"type":"session"}`))
		r.Header.Set("Sec-Fetch-Site", tc.site)
		r.Header.Set("Content-Type", tc.contentType)
		if tc.site == "cross-site" {
			r.Header.Set("Origin", "https://evil.example")
		}
		w := httptest.NewRecorder()
		(&Server{}).handleTelemetryEvent(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}
