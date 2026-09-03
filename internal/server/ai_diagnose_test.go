package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/ai"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
)

// TestListAgents_Eligible pins the eligibility signal that drives the UI's
// "install an agent to enable this" nudge: true only when the deployment mode
// supports local BYO-agent diagnosis (no proxy/OIDC auth AND /mcp mounted) —
// the same gate the boot-time engine init uses.
func TestListAgents_Eligible(t *testing.T) {
	mcp := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	cases := []struct {
		name string
		mode string
		mcp  http.Handler
		want bool
	}{
		{"local mode + mcp mounted", "none", mcp, true},
		{"auth enabled", "proxy", mcp, false},
		{"mcp disabled", "none", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{authConfig: auth.Config{Mode: c.mode}, mcpHandler: c.mcp}
			rec := httptest.NewRecorder()
			s.handleListAgents(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
			var resp struct {
				Eligible bool `json:"eligible"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Eligible != c.want {
				t.Errorf("eligible = %v, want %v", resp.Eligible, c.want)
			}
		})
	}
}

// TestDiagnoseConsentOriginGate pins the CSRF guard on the process-spawning
// diagnose POSTs at the handler layer (regression for RAD-450). The guard must
// admit a genuinely same-origin browser POST even on a non-loopback listener
// (Origin == the authority the browser actually connected to) and still reject
// a foreign origin. We assert only whether the origin gate blocked the request
// (403) — a request that clears the gate falls through to later checks whose
// status is irrelevant here, so the test must not couple to it.
func TestDiagnoseConsentOriginGate(t *testing.T) {
	cases := []struct {
		name        string
		host        string
		origin      string
		wantBlocked bool // true => origin gate must 403; false => gate must let it through
	}{
		{"same-origin non-loopback listener", "192.168.1.100:9280", "http://192.168.1.100:9280", false},
		{"same-origin loopback", "127.0.0.1:9280", "http://127.0.0.1:9280", false},
		{"non-browser client (no Origin)", "192.168.1.100:9280", "", false},
		{"vite dev proxy loopback-to-loopback", "localhost:9280", "http://localhost:9273", false},
		{"foreign origin", "192.168.1.100:9280", "https://evil.example", true},
		{"loopback origin against non-loopback host", "192.168.1.100:9280", "http://127.0.0.1:9280", true},
		{"lookalike hostname", "192.168.1.100:9280", "http://192.168.1.100.evil.com", true},
		{"opaque (null) origin", "192.168.1.100:9280", "null", true},
	}
	s := &Server{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/diagnose/consent", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			s.handleDiagnoseConsent(w, r)
			blocked := w.Code == http.StatusForbidden
			if blocked != c.wantBlocked {
				t.Errorf("origin gate blocked = %v (status %d), want blocked = %v", blocked, w.Code, c.wantBlocked)
			}
		})
	}
}

// TestConsentMachineScoped pins the shared consent store: recording via the
// endpoint's config path must be visible to currentConsents (what /api/agents
// reports to the panel AND what the CLI reads) — one acknowledgment covers
// both surfaces' checks, per disclosure surface.
func TestConsentMachineScoped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if c := currentConsents(); c["codex:safeguarded"] || c["codex:full-local"] {
		t.Fatalf("fresh HOME must have no consent, got %v", c)
	}
	if err := config.RecordAIConsent("codex:safeguarded"); err != nil {
		t.Fatal(err)
	}
	c := currentConsents()
	if !c["codex:safeguarded"] || c["codex:full-local"] {
		t.Fatalf("safeguarded consent must not cover full-local: %v", c)
	}
	if c["claude:safeguarded"] {
		t.Fatalf("Codex consent must not cover Claude's distinct disclosure: %v", c)
	}
	// A stale (older-version) acknowledgment must not count.
	if _, err := config.Update(func(c *config.Config) {
		c.AIConsent["codex:full-local"] = "v0"
	}); err != nil {
		t.Fatal(err)
	}
	if currentConsents()["codex:full-local"] {
		t.Fatal("an older disclosure version must not satisfy consent")
	}
}

func TestEveryAgentConsentSurfaceHasAVersion(t *testing.T) {
	for _, surface := range ai.AllConsentSurfaces() {
		if config.AIConsentVersion(surface) == "" {
			t.Errorf("consent surface %q has no disclosure version", surface)
		}
	}
}

func TestMergeDetectedWithDrivableUsesTheActualBackendSet(t *testing.T) {
	detected := []ai.AgentInfo{
		{Name: "claude", Supported: true, Profiles: ai.ProfilesFor("claude")},
		{Name: "codex", Supported: true, Profiles: ai.ProfilesFor("codex")},
	}
	drivable := []ai.AgentInfo{
		{Name: "codex", Path: "/override/codex", Present: true, Supported: true, Profiles: ai.ProfilesFor("codex")},
		{Name: "cursor-agent", Path: "/override/cursor-agent", Present: true, Supported: true, Profiles: ai.ProfilesFor("cursor-agent")},
	}

	got := mergeDetectedWithDrivable(detected, drivable)
	if got[0].Name != "claude" || got[0].Supported || len(got[0].Profiles) != 0 {
		t.Fatalf("PATH-only Claude must not be advertised as drivable: %+v", got[0])
	}
	if got[1].Name != "codex" || !got[1].Supported || got[1].Path != "/override/codex" {
		t.Fatalf("Codex must use the actual backend metadata: %+v", got[1])
	}
	if len(got) != 3 || got[2].Name != "cursor-agent" || !got[2].Supported {
		t.Fatalf("an override-only backend must still be advertised: %+v", got)
	}
}
