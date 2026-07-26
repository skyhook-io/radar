package ai

import "testing"

func TestExecutionProfiles(t *testing.T) {
	cases := []struct {
		agent string
		want  []ExecutionProfile
	}{
		{"claude", []ExecutionProfile{ExecutionProfileSafeguarded}},
		{"codex", []ExecutionProfile{ExecutionProfileSafeguarded, ExecutionProfileFullLocal}},
		{"cursor-agent", []ExecutionProfile{ExecutionProfileFullLocal}},
		{"gemini", nil},
	}
	for _, c := range cases {
		got := ProfilesFor(c.agent)
		if len(got) != len(c.want) {
			t.Errorf("ProfilesFor(%q) = %v, want %v", c.agent, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ProfilesFor(%q) = %v, want %v", c.agent, got, c.want)
			}
		}
	}
	if SupportsProfile("claude", ExecutionProfileFullLocal) {
		t.Error("Claude must not advertise full-local until it has an intentional implementation")
	}
}

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
	if ConsentSurfaceFor(DefaultProfileFor(EffectiveAgent("", agents))) != "full-local" {
		t.Error("empty pick with Cursor as default must gate on the full-local surface")
	}
}
