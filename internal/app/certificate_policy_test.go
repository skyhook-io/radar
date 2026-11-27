package app

import (
	"encoding/json"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/health"
	"net/http/httptest"
	"testing"
)

func TestCreateServerExposesCertificateStartupPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RADAR_CLOUD_MODE", "false")
	t.Setenv(settings.OperatorFileEnv, "")
	t.Cleanup(func() { settings.SetOperatorConfig(nil) })
	p := health.CertificateExpiryPolicy{WarningPercent: 20}
	srv := CreateServer(AppConfig{CertificateExpiryPolicy: p, ListenAddress: "127.0.0.1"})
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest("GET", "http://localhost/api/config", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var result struct{ Effective config.Config }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	actual, err := result.Effective.CertificateExpiryPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if actual != p {
		t.Fatalf("effective=%+v want=%+v", actual, p)
	}
}
