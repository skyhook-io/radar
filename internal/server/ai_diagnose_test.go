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

// TestLocalOriginOK pins the cross-origin guard on the process-spawning POST
// endpoints: same-origin and exact loopback pass; look-alike hosts don't.
func TestLocalOriginOK(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"", true}, // same-origin / non-browser
		{"http://localhost:9301", true},
		{"http://127.0.0.1:3000", true},
		{"https://localhost", true},
		{"http://[::1]:9301", true},
		{"http://localhost.evil.com", false}, // substring trap
		{"http://127.0.0.1.evil.com", false},
		{"https://evil.com", false},
		{"null", false},
	}
	for _, c := range cases {
		r := &http.Request{Header: http.Header{}}
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := localOriginOK(r); got != c.want {
			t.Errorf("localOriginOK(%q) = %v, want %v", c.origin, got, c.want)
		}
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
