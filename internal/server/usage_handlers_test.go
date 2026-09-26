package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/usagedata"
)

func TestPutUsageDataRejectsCrossSiteConsent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "http://localhost:9280/api/usage-data", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	(&Server{}).handlePutUsageData(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestPutUsageDataRequiresExplicitChoice(t *testing.T) {
	for _, body := range []string{`{}`, `{"enabled":"yes"}`, `not json`} {
		r := httptest.NewRequest(http.MethodPut, "http://localhost:9280/api/usage-data", strings.NewReader(body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		(&Server{}).handlePutUsageData(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, w.Code)
		}
	}
}

func TestUsageDataEventRejectsUnknownNames(t *testing.T) {
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
		r := httptest.NewRequest(http.MethodPost, "http://localhost:9280/api/usage-data/event", strings.NewReader(body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(&Server{}).handleUsageEvent(w, r)
		if w.Code != want {
			t.Fatalf("body %s: status = %d, want %d", body, w.Code, want)
		}
	}
}

func TestUsageDataMiddlewarePassesThroughWhenOff(t *testing.T) {
	called := false
	h := (&Server{}).usageMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if !called || w.Code != http.StatusTeapot {
		t.Fatalf("handler not reached or status changed: %d", w.Code)
	}
}

func TestUsageDataEventRejectsCrossSiteAndNonJSON(t *testing.T) {
	cases := []struct {
		name, site, contentType string
		want                    int
	}{
		{"cross-site form post", "cross-site", "text/plain", http.StatusForbidden},
		{"same-origin but not JSON", "same-origin", "text/plain", http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "http://localhost:9280/api/usage-data/event", strings.NewReader(`{"type":"session"}`))
		r.Header.Set("Sec-Fetch-Site", tc.site)
		r.Header.Set("Content-Type", tc.contentType)
		if tc.site == "cross-site" {
			r.Header.Set("Origin", "https://evil.example")
		}
		w := httptest.NewRecorder()
		(&Server{}).handleUsageEvent(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.name, w.Code, tc.want)
		}
	}
}

// A Radar others can reach, with no sign-in, can't tell an owner from a
// viewer, so nobody may switch usage data on for everyone from the UI.
func TestSharedInstallWithoutSignInIsDecidedByConfigOnly(t *testing.T) {
	shared := &Server{listenAddress: "0.0.0.0"}
	r := httptest.NewRequest(http.MethodPut, "http://radar.internal/api/usage-data", strings.NewReader(`{"enabled":true}`))
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	shared.handlePutUsageData(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}

	in := usagedata.Status{State: usagedata.StateUndecided, Source: usagedata.SourceDefault, CanChange: true, FirstRunPrompt: true, Ask: true, Shared: true}
	got := shared.usageStatusFor(httptest.NewRequest(http.MethodGet, "/api/usage-data", nil), in)
	if got.CanChange || got.FirstRunPrompt || got.Ask {
		t.Fatalf("shared install without sign-in should not ask or allow changes: %+v", got)
	}
}

func TestUsageDecider(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	cases := []struct {
		name                           string
		shared, ownersDecide, signedIn bool
		mayPatch                       func() bool
		want                           bool
	}{
		{"local install: the user decides", false, false, false, no, true},
		{"shared, no sign-in: config only", true, false, false, yes, false},
		{"shared with owners, anonymous caller", true, true, false, yes, false},
		{"shared with owners, viewer", true, true, true, no, false},
		{"shared with owners, owner", true, true, true, yes, true},
		{"shared local listener with sign-in: config only", true, false, true, yes, false},
	}
	for _, tc := range cases {
		if got := usageDecider(tc.shared, tc.ownersDecide, tc.signedIn, tc.mayPatch); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
