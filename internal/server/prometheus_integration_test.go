package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

// setupPrometheusIntegrationTest isolates the on-disk config and installs a
// bare Prometheus client whose headers stand in for whatever flags, env and
// file resolved to at startup.
func setupPrometheusIntegrationTest(t *testing.T, liveHeaders map[string]string) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetHeaders(liveHeaders)
	t.Cleanup(func() { prometheuspkg.Initialize(nil, nil, "") })
	return &Server{}
}

func applyPrometheus(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.handleApplyPrometheusURL(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/prometheus", strings.NewReader(body)))
	return rec
}

func TestApplyPrometheusURL_HeadersRequireURL(t *testing.T) {
	tests := []struct {
		name        string
		liveHeaders map[string]string
		diskEnv     map[string]string
		body        string
		wantStatus  int
	}{
		{
			name:       "url with headers is accepted",
			body:       `{"prometheusUrl":"http://prom.example:9090","headers":{"X-Scope-OrgID":"acme"}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "no url with submitted headers is refused",
			body:       `{"prometheusUrl":"","headers":{"X-Scope-OrgID":"acme"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "no url keeping live headers is refused",
			liveHeaders: map[string]string{"Authorization": "Bearer flag-sourced"},
			body:        `{"prometheusUrl":""}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:       "no url with env-sourced headers on disk is refused even when clearing submitted headers",
			diskEnv:    map[string]string{"Authorization": "PROMETHEUS_TOKEN"},
			body:       `{"prometheusUrl":"","headers":{}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "no url clearing headers with nothing on disk reverts to auto-discovery",
			liveHeaders: map[string]string{"Authorization": "Bearer flag-sourced"},
			body:        `{"prometheusUrl":"","headers":{}}`,
			wantStatus:  http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := setupPrometheusIntegrationTest(t, tt.liveHeaders)
			if len(tt.diskEnv) > 0 {
				if _, err := config.Update(func(c *config.Config) { c.PrometheusHeadersFromEnv = tt.diskEnv }); err != nil {
					t.Fatal(err)
				}
			}
			before := config.Load()

			rec := applyPrometheus(s, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusBadRequest {
				if after := config.Load(); after.PrometheusURL != before.PrometheusURL || len(after.PrometheusHeaders) != len(before.PrometheusHeaders) {
					t.Fatalf("refused request changed the on-disk config: before %#v after %#v", before, after)
				}
				if !strings.Contains(rec.Body.String(), "require a Prometheus URL") {
					t.Fatalf("error body does not name the rule: %s", rec.Body.String())
				}
			}
		})
	}
}
