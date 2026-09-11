package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/auth"
)

// The remembered cluster shares settings.json with the user's preferences, so
// /api/settings must strip it both ways — otherwise every viewer of a shared
// instance learns which cluster this $HOME last ran Desktop against. The PUT
// half also pins that handlePutSettings stays a patch: a body that never
// mentions lastDesktopContext must not erase it.
func TestSettingsEndpointNeverCarriesTheRememberedCluster(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if _, err := settings.Update(func(st *settings.Settings) {
		st.LastDesktopContext = &settings.LastContext{Name: "prod-eu"}
	}); err != nil {
		t.Fatalf("seed desktop state: %v", err)
	}

	for _, tc := range []struct {
		name   string
		server *Server
	}{
		{"local", &Server{}},
		{"auth-enabled", &Server{authConfig: auth.Config{Mode: "oidc"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			get := httptest.NewRecorder()
			tc.server.handleGetSettings(get, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
			assertNoRememberedCluster(t, "GET", get.Body.String())

			put := httptest.NewRecorder()
			tc.server.handlePutSettings(put, httptest.NewRequest(
				http.MethodPut, "/api/settings", strings.NewReader(`{"theme":"light"}`)))
			assertNoRememberedCluster(t, "PUT", put.Body.String())

			// ...and the PUT must not have erased it on disk either.
			if settings.Load().LastDesktopContext == nil {
				t.Error("a PUT that never mentioned it dropped the remembered cluster")
			}
		})
	}
}

func assertNoRememberedCluster(t *testing.T, verb, body string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("%s decode: %v", verb, err)
	}
	if v, has := payload["lastDesktopContext"]; has {
		t.Errorf("%s /api/settings carried the remembered cluster: %v", verb, v)
	}
	if strings.Contains(body, "prod-eu") {
		t.Errorf("%s /api/settings body mentions the remembered cluster: %s", verb, body)
	}
}
