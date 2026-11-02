package mcp

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/search"
)

func TestSearchIPHint(t *testing.T) {
	ip := "10.244.1.7"
	hit := func(site string) search.Hit {
		return search.Hit{Matched: []search.MatchedField{{Token: ip, Site: site}}}
	}
	if h := searchIPHint(ip, []search.Hit{hit("ip:podIP"), hit("content:data.url")}); h != "" {
		t.Fatalf("running holder present, got hint %q", h)
	}
	for name, hits := range map[string][]search.Hit{
		"mention only":    {hit("content:data.url")},
		"exited pod only": {hit("ip:exitedPodIP")},
		"no hits":         nil,
	} {
		if h := searchIPHint(ip, hits); !strings.Contains(h, "no running Pod, Service or Node") || !strings.Contains(h, ip) {
			t.Errorf("%s: hint = %q", name, h)
		}
	}
}

func TestIPOnlyQuery(t *testing.T) {
	for q, want := range map[string]bool{
		"10.244.1.7:6379": true,
		// Tokens are AND'd: two IPs rarely share a holder, so no absence claim.
		"10.244.1.7 10.96.0.10":  false,
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
