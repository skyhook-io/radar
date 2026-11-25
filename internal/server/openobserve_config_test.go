package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
)

func saveOpenObserveEntry(t *testing.T) config.OpenObserveConfig {
	t.Helper()
	entry := config.OpenObserveConfig{
		BaseURL: "http://oo.example:5080", Org: "default", User: "sa@example.com",
		Token: "oo-secret-token", TokenEnv: "OO_TOKEN",
		Matches: map[string]config.OpenObserveMatch{"uid-b": {Label: "radar_cluster_uid", Value: "uid-b", By: config.OpenObserveMatchByID}},
	}
	if _, err := config.Update(func(c *config.Config) {
		cp := entry.Clone()
		c.OpenObserve = &cp
	}); err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestGetConfigRedactsOpenObserveToken(t *testing.T) {
	s := setupArgoCDTest(t)
	saveOpenObserveEntry(t)
	effective := config.Load()
	s.effectiveConfig = &effective

	w := httptest.NewRecorder()
	s.handleGetConfig(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "oo-secret-token") {
		t.Fatalf("GET /api/config leaked the OpenObserve token: %s", w.Body.String())
	}
	var resp struct {
		File      config.Config `json:"file"`
		Effective config.Config `json:"effective"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]config.Config{"file": resp.File, "effective": resp.Effective} {
		if c.OpenObserve == nil || c.OpenObserve.BaseURL != "http://oo.example:5080" || c.OpenObserve.TokenEnv != "OO_TOKEN" || c.OpenObserve.Token != "" {
			t.Errorf("%s openobserve = %+v", name, c.OpenObserve)
		}
	}
	// Serving it redacted leaves the stored and running entries alone.
	if got := config.Load().OpenObserve; got == nil || got.Token != "oo-secret-token" {
		t.Fatalf("stored entry changed: %+v", got)
	}
	if s.effectiveConfig.OpenObserve.Token != "oo-secret-token" {
		t.Fatal("running config changed")
	}
}

func TestPutConfigPreservesOpenObserveEntry(t *testing.T) {
	s := setupArgoCDTest(t)
	entry := saveOpenObserveEntry(t)

	// The Settings dialog round-trips GET's redacted entry, or drops it.
	for _, body := range []string{
		`{"port": 9999}`,
		`{"port": 9998, "openobserve": {"baseUrl": "http://evil.example", "org": "x", "user": "y"}}`,
	} {
		w := httptest.NewRecorder()
		s.handlePutConfig(w, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "oo-secret-token") {
			t.Fatalf("PUT /api/config response leaked the OpenObserve token: %s", w.Body.String())
		}
		saved := config.Load()
		if saved.OpenObserve == nil || saved.OpenObserve.Token != entry.Token || saved.OpenObserve.BaseURL != entry.BaseURL || saved.OpenObserve.Matches["uid-b"] != entry.Matches["uid-b"] {
			t.Fatalf("after %s: openobserve = %+v", body, saved.OpenObserve)
		}
	}
	if config.Load().Port != 9998 {
		t.Fatal("other fields not saved")
	}
}
