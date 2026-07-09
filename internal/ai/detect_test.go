package ai

import "testing"

func TestEffectiveAgentMatchesServerResolution(t *testing.T) {
	agents := []AgentInfo{
		{Name: "claude", Supported: false},
		{Name: "cursor-agent", Supported: true},
		{Name: "codex", Supported: true},
	}
	cases := []struct{ pick, want string }{
		{"", "cursor-agent"},       // empty pick → first SUPPORTED, not first listed
		{"codex", "codex"},         // supported pick honored
		{"claude", "cursor-agent"}, // unsupported pick falls back like AgentName
		{"nope", "cursor-agent"},   // unknown pick falls back
	}
	for _, c := range cases {
		if got := EffectiveAgent(c.pick, agents); got != c.want {
			t.Errorf("EffectiveAgent(%q) = %q, want %q", c.pick, got, c.want)
		}
	}
	if got := EffectiveAgent("", nil); got != "" {
		t.Errorf("no supported agents should resolve to \"\", got %q", got)
	}
	if ConsentSurfaceFor(EffectiveAgent("", agents)) != "cursor" {
		t.Error("empty pick with Cursor as default must gate on the cursor surface")
	}
}
