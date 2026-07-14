package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
)

// fakeArgoCDServer accepts "good-token" (and no token) and rejects the rest.
func fakeArgoCDServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version": "v2.13.0"}`))
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": "invalid session", "message": "token is expired"}`))
			return
		}
		_, _ = w.Write([]byte(`{"loggedIn": true, "username": "admin"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestArgoCDStatus pins the Settings Overview status endpoint: unconfigured
// reports configured/connected false, and a probed connection reports both true
// plus the resolved address.
func TestArgoCDStatus(t *testing.T) {
	s := setupArgoCDTest(t)

	var out struct {
		Configured bool   `json:"configured"`
		Connected  bool   `json:"connected"`
		Address    string `json:"address"`
	}
	getStatus := func() {
		rec := httptest.NewRecorder()
		s.handleArgoCDStatus(rec, httptest.NewRequest(http.MethodGet, "/api/integrations/argocd/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}

	getStatus()
	if out.Configured || out.Connected {
		t.Fatalf("unconfigured: configured=%v connected=%v, want false/false", out.Configured, out.Connected)
	}

	srv := fakeArgoCDServer(t)
	argocd.SetConfig(srv.URL, "good-token", false, true)
	if err := argocd.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}

	getStatus()
	if !out.Configured || !out.Connected {
		t.Fatalf("connected: configured=%v connected=%v, want true/true", out.Configured, out.Connected)
	}
	if out.Address != srv.URL {
		t.Fatalf("address = %q, want %q", out.Address, srv.URL)
	}
}

// setupArgoCDTest isolates the on-disk config and the live argocd manager.
func setupArgoCDTest(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })
	return &Server{}
}

func putArgoCD(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/integrations/argocd", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleApplyArgoCDConfig(w, req)
	return w
}

func TestApplyArgoCDConfig_ReplaceToken(t *testing.T) {
	s := setupArgoCDTest(t)
	srv := fakeArgoCDServer(t)

	w := putArgoCD(t, s, `{"argoCdUrl": "`+srv.URL+`", "argoCdToken": "good-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
		TokenSet  bool   `json:"tokenSet"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Connected || resp.Address != srv.URL || !resp.TokenSet {
		t.Errorf("resp = %+v", resp)
	}

	saved := config.Load()
	if saved.ArgoCDToken != "good-token" || saved.ArgoCDURL != srv.URL {
		t.Errorf("persisted config = %+v", saved)
	}
}

func TestApplyArgoCDConfig_PreserveTokenWhenAbsent(t *testing.T) {
	s := setupArgoCDTest(t)
	srv := fakeArgoCDServer(t)

	// Stored URL matches the URL being re-submitted — the real round-trip
	// case (same origin, redacted token omitted).
	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = srv.URL
		c.ArgoCDToken = "good-token"
	}); err != nil {
		t.Fatal(err)
	}

	// No argoCdToken key at all — the GET-redaction round-trip case.
	w := putArgoCD(t, s, `{"argoCdUrl": "`+srv.URL+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().ArgoCDToken; got != "good-token" {
		t.Errorf("token = %q, want preserved good-token", got)
	}
}

// TestApplyArgoCDConfig_RejectsTokenReuseOnOriginChange pins the critical
// invariant: a stored token is never forwarded to a different Argo origin. A
// URL change without a re-supplied token is rejected, and neither the running
// client nor disk sends the old token to the new host.
func TestApplyArgoCDConfig_RejectsTokenReuseOnOriginChange(t *testing.T) {
	s := setupArgoCDTest(t)
	srv := fakeArgoCDServer(t)

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = srv.URL
		c.ArgoCDToken = "good-token"
	}); err != nil {
		t.Fatal(err)
	}

	// Change only the URL (to a different host), omit the token → must reject.
	w := putArgoCD(t, s, `{"argoCdUrl": "https://attacker.example.com"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().ArgoCDURL; got != srv.URL {
		t.Errorf("URL = %q, want unchanged %q (nothing persisted)", got, srv.URL)
	}
	if got := config.Load().ArgoCDToken; got != "good-token" {
		t.Errorf("token = %q, want unchanged", got)
	}
}

func TestApplyArgoCDConfig_ClearToken(t *testing.T) {
	s := setupArgoCDTest(t)
	srv := fakeArgoCDServer(t)

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDToken = "good-token"
	}); err != nil {
		t.Fatal(err)
	}

	w := putArgoCD(t, s, `{"argoCdUrl": "`+srv.URL+`", "argoCdToken": ""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := config.Load().ArgoCDToken; got != "" {
		t.Errorf("token = %q, want cleared", got)
	}
}

func TestApplyArgoCDConfig_ProbeFailPersistsNothing(t *testing.T) {
	s := setupArgoCDTest(t)

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = "https://old.example.com"
		c.ArgoCDToken = "old-token"
	}); err != nil {
		t.Fatal(err)
	}

	// Supply a token so the request reaches the probe (a reused token against a
	// new origin is rejected earlier — that path has its own test).
	w := putArgoCD(t, s, `{"argoCdUrl": "http://127.0.0.1:1", "argoCdToken": "new-token"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unreachable") {
		t.Errorf("body = %s, want unreachable message", w.Body.String())
	}

	saved := config.Load()
	if saved.ArgoCDURL != "https://old.example.com" || saved.ArgoCDToken != "old-token" {
		t.Errorf("config changed after failed probe: %+v", saved)
	}
}

func TestApplyArgoCDConfig_TokenInvalidDistinctMessage(t *testing.T) {
	s := setupArgoCDTest(t)
	srv := fakeArgoCDServer(t)

	w := putArgoCD(t, s, `{"argoCdUrl": "`+srv.URL+`", "argoCdToken": "bad-token"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rejected the token") {
		t.Errorf("body = %s, want token-rejection message", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "unreachable") {
		t.Errorf("token rejection must not read as unreachable: %s", w.Body.String())
	}
	if got := config.Load().ArgoCDToken; got != "" {
		t.Errorf("token persisted after failed probe: %q", got)
	}
}

func TestApplyArgoCDConfig_InvalidURL(t *testing.T) {
	s := setupArgoCDTest(t)
	w := putArgoCD(t, s, `{"argoCdUrl": "not a url"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestApplyArgoCDConfig_RejectsCredentialBearingURL pins that the PUT applies the
// same strict validation as the env input — a URL embedding userinfo (or a query)
// is rejected, so it can't be persisted and later surfaced in GET /api/config.
func TestApplyArgoCDConfig_RejectsCredentialBearingURL(t *testing.T) {
	s := setupArgoCDTest(t)
	for _, bad := range []string{
		`{"argoCdUrl": "https://user:pass@argocd.example.com"}`,
		`{"argoCdUrl": "https://argocd.example.com/api?token=secret"}`,
	} {
		w := putArgoCD(t, s, bad)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", bad, w.Code)
		}
		if saved := config.Load(); saved.ArgoCDURL != "" {
			t.Fatalf("a rejected URL must not be persisted, got %q", saved.ArgoCDURL)
		}
	}
}

// TestApplyArgoCDConfig_RefusedWhenEnvManaged pins the read-only invariant: an
// environment-provisioned integration refuses UI edits with 409, so a Settings
// change can't silently no-op against the declarative source of truth.
func TestApplyArgoCDConfig_RefusedWhenEnvManaged(t *testing.T) {
	s := setupArgoCDTest(t)
	argocd.SeedFromEnv("https://argocd.example.com", "env-token", false)

	w := putArgoCD(t, s, `{"argoCdUrl": "https://other.example.com", "argoCdToken": "new-token"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 when env-managed; body = %s", w.Code, w.Body.String())
	}
}

// TestGetConfigReflectsEnvManaged pins that the config endpoint surfaces the
// effective env URL/TLS (not stale disk values) and the env-managed + token-set
// signals, all without leaking the token.
func TestGetConfigReflectsEnvManaged(t *testing.T) {
	s := setupArgoCDTest(t)

	// Stale disk config that env-managed mode must override in the response.
	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = "https://disk.example.com"
		c.ArgoCDInsecureTLS = false
	}); err != nil {
		t.Fatal(err)
	}
	argocd.SeedFromEnv("https://env.example.com", "env-token", true)

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	s.handleGetConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "env-token") {
		t.Error("GET /api/config leaked the env-managed Argo CD token")
	}

	var resp struct {
		File             config.Config `json:"file"`
		ArgoCDTokenSet   bool          `json:"argoCdTokenSet"`
		ArgoCDEnvManaged bool          `json:"argoCdEnvManaged"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.ArgoCDEnvManaged {
		t.Error("argoCdEnvManaged should be true when provisioned from the environment")
	}
	if !resp.ArgoCDTokenSet {
		t.Error("argoCdTokenSet should be true when env-managed")
	}
	if resp.File.ArgoCDURL != "https://env.example.com" || !resp.File.ArgoCDInsecureTLS {
		t.Errorf("config should reflect the effective env endpoint, got url=%q insecure=%v",
			resp.File.ArgoCDURL, resp.File.ArgoCDInsecureTLS)
	}
}

// TestGetConfigSurfacesEnvError pins that a failed env provisioning surfaces the
// reason (argoCdEnvError) with env-managed=true and tokenSet=false — so the UI
// shows an error state instead of a phantom "configured", and the stale disk URL
// is not presented as the effective endpoint.
func TestGetConfigSurfacesEnvError(t *testing.T) {
	s := setupArgoCDTest(t)

	// Stale disk config with BOTH a URL and a token — neither may surface in the
	// errored state (the token must not even flip argoCdTokenSet).
	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = "https://stale.example.com"
		c.ArgoCDToken = "stale-disk-token"
	}); err != nil {
		t.Fatal(err)
	}
	argocd.SeedFromEnvFailed("invalid RADAR_ARGOCD_URL: must include a host")

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	s.handleGetConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var resp struct {
		File             config.Config `json:"file"`
		ArgoCDTokenSet   bool          `json:"argoCdTokenSet"`
		ArgoCDEnvManaged bool          `json:"argoCdEnvManaged"`
		ArgoCDEnvError   string        `json:"argoCdEnvError"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.ArgoCDEnvManaged || resp.ArgoCDEnvError == "" {
		t.Errorf("errored env provisioning should surface envManaged=true + a reason, got managed=%v err=%q",
			resp.ArgoCDEnvManaged, resp.ArgoCDEnvError)
	}
	if resp.ArgoCDTokenSet {
		t.Error("argoCdTokenSet must be false in the errored state (no token)")
	}
	if resp.File.ArgoCDURL == "https://stale.example.com" {
		t.Error("the stale disk URL must not be presented as the effective endpoint")
	}
}

// TestApplyArgoCDConfig_RefusedWhenEnvErrored pins that the read-only invariant
// holds even when env provisioning failed — a UI edit still can't override the
// declarative (broken) config; the operator must fix the deployment.
func TestApplyArgoCDConfig_RefusedWhenEnvErrored(t *testing.T) {
	s := setupArgoCDTest(t)
	argocd.SeedFromEnvFailed("some reason")

	w := putArgoCD(t, s, `{"argoCdUrl": "https://other.example.com", "argoCdToken": "new-token"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 when env-managed (errored); body = %s", w.Code, w.Body.String())
	}
}

func TestGetConfigRedactsArgoCDToken(t *testing.T) {
	s := setupArgoCDTest(t)

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = "https://argocd.example.com"
		c.ArgoCDToken = "super-secret"
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	s.handleGetConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "super-secret") {
		t.Error("GET /api/config leaked the Argo CD token")
	}

	var resp struct {
		File           config.Config `json:"file"`
		ArgoCDTokenSet bool          `json:"argoCdTokenSet"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.File.ArgoCDToken != "" {
		t.Error("file.argoCdToken should be redacted")
	}
	if !resp.ArgoCDTokenSet {
		t.Error("argoCdTokenSet should be true when a token is configured")
	}
	if resp.File.ArgoCDURL != "https://argocd.example.com" {
		t.Errorf("argoCdUrl = %q, should not be redacted", resp.File.ArgoCDURL)
	}
}

func TestPutConfigPreservesArgoCDToken(t *testing.T) {
	s := setupArgoCDTest(t)

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDToken = "super-secret"
	}); err != nil {
		t.Fatal(err)
	}

	// Full-replacement PUT without the (redacted) token — must not wipe it.
	req := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(`{"port": 9999}`))
	w := httptest.NewRecorder()
	s.handlePutConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "super-secret") {
		t.Error("PUT /api/config response leaked the Argo CD token")
	}

	saved := config.Load()
	if saved.ArgoCDToken != "super-secret" {
		t.Errorf("token = %q, want preserved super-secret", saved.ArgoCDToken)
	}
	if saved.Port != 9999 {
		t.Errorf("Port = %d, want 9999", saved.Port)
	}
}

// TestPutConfigPreservesIntegrationFields pins that the startup-config PUT owns
// NONE of the live integration connection fields: a full-replacement PUT that
// carries different (or empty) prometheusUrl / argoCdUrl / argoCdInsecureTls
// must leave the stored integration config untouched. This is the guardrail
// against a startup Save racing an in-flight Apply/Connect and reverting — or
// clearing the token on — a live integration.
func TestPutConfigPreservesIntegrationFields(t *testing.T) {
	s := setupArgoCDTest(t)

	if _, err := config.Update(func(c *config.Config) {
		c.ArgoCDURL = "https://argo.internal:8080"
		c.ArgoCDToken = "tok"
		c.ArgoCDInsecureTLS = true
		c.PrometheusURL = "http://prom.internal:9090"
	}); err != nil {
		t.Fatal(err)
	}

	// Startup PUT that echoes back stale/empty integration fields.
	req := httptest.NewRequest(http.MethodPut, "/api/config",
		strings.NewReader(`{"port": 9999, "argoCdUrl": "https://attacker.example", "prometheusUrl": ""}`))
	w := httptest.NewRecorder()
	s.handlePutConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	saved := config.Load()
	if saved.ArgoCDURL != "https://argo.internal:8080" {
		t.Errorf("argoCdUrl = %q, want unchanged (integration-owned)", saved.ArgoCDURL)
	}
	if saved.ArgoCDToken != "tok" {
		t.Errorf("token = %q, want preserved (origin unchanged, not cleared)", saved.ArgoCDToken)
	}
	if !saved.ArgoCDInsecureTLS {
		t.Error("argoCdInsecureTls flipped by a startup PUT")
	}
	if saved.PrometheusURL != "http://prom.internal:9090" {
		t.Errorf("prometheusUrl = %q, want unchanged", saved.PrometheusURL)
	}
	if saved.Port != 9999 {
		t.Errorf("Port = %d, want the startup field applied", saved.Port)
	}
}
