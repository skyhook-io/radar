package server

import (
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
)

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
	if c := currentConsents(); c["safeguarded"] || c["full-local"] {
		t.Fatalf("fresh HOME must have no consent, got %v", c)
	}
	if err := config.RecordAIConsent("safeguarded"); err != nil {
		t.Fatal(err)
	}
	c := currentConsents()
	if !c["safeguarded"] || c["full-local"] {
		t.Fatalf("safeguarded consent must not cover full-local: %v", c)
	}
	// A stale (older-version) acknowledgment must not count.
	if _, err := config.Update(func(c *config.Config) {
		c.AIConsent["full-local"] = "v0"
	}); err != nil {
		t.Fatal(err)
	}
	if currentConsents()["full-local"] {
		t.Fatal("an older disclosure version must not satisfy consent")
	}
}
