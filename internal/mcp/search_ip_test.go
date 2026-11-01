package mcp

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/search"
)

func TestSearchIPHint(t *testing.T) {
	podHit := search.Hit{Kind: "Pod", Name: "redis-0", Matched: []search.MatchedField{{Token: "10.244.1.7", Site: "ip:podIP"}}}
	mention := search.Hit{Kind: "ConfigMap", Name: "cfg", Matched: []search.MatchedField{{Token: "10.244.1.7", Site: "content:data.url"}}}

	if h := searchIPHint([]string{"10.244.1.7"}, []search.Hit{podHit, mention}); h != "" {
		t.Fatalf("holder present, got hint %q", h)
	}
	h := searchIPHint([]string{"10.244.1.7"}, []search.Hit{mention})
	if !strings.Contains(h, "10.244.1.7") || !strings.Contains(h, "only mention") {
		t.Fatalf("mention-only hint = %q", h)
	}
	if h := searchIPHint([]string{"10.244.1.7"}, nil); !strings.Contains(h, "deleted pod") {
		t.Fatalf("no-hit hint = %q", h)
	}
}

func TestIPOnlyQuery(t *testing.T) {
	for q, want := range map[string]bool{
		"10.244.1.7:6379":        true,
		"10.244.1.7 10.96.0.10":  true,
		"10.244.1.7 redis":       false,
		"kind:Pod 10.244.1.7":    false,
		"ns:prod 10.244.1.7":     false,
		"label:app=x 10.244.1.7": false,
		"redis":                  false,
	} {
		if got := ipOnlyQuery(search.Parse(q)); got != want {
			t.Errorf("ipOnlyQuery(%q) = %v, want %v", q, got, want)
		}
	}
}
