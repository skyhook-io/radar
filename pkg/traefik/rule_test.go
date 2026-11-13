package traefik

import (
	"reflect"
	"testing"
)

func TestRuleHosts(t *testing.T) {
	for _, tc := range []struct {
		match string
		want  []string
	}{
		{"Host(`a.example.com`)", []string{"a.example.com"}},
		{"Host(`a.example.com`) && PathPrefix(`/api`)", []string{"a.example.com"}},
		{"Host(`a.example.com`) || Host(`b.example.com`)", []string{"a.example.com", "b.example.com"}},
		{"Host(`a.example.com`,`b.example.com`)", []string{"a.example.com", "b.example.com"}},
		{"HostRegexp(`{sub:[a-z]+}.example.com`)", []string{"{sub:[a-z]+}.example.com"}},
		{"HostRegexp(`^.+\\.example\\.com$`)", []string{"^.+\\.example\\.com$"}},
		{"Host(\"quoted.example.com\")", []string{"quoted.example.com"}},
		{"PathPrefix(`/`)", nil},
		{"Host(`unterminated", nil},
		{"Host(`A.Example.com`) || Host(`a.example.com`)", []string{"a.example.com"}},
		{"HostSNI(`tcp.example.com`)", nil},
		{"Host(\t`tab.example.com`\t)", []string{"tab.example.com"}},
		{"Host(\n  `a.example.com`,\n  `b.example.com`\n)", []string{"a.example.com", "b.example.com"}},
	} {
		if got := RuleHosts(tc.match); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("RuleHosts(%q) = %#v, want %#v", tc.match, got, tc.want)
		}
	}
}

func TestNormalizeMatch(t *testing.T) {
	if a, b := NormalizeMatch("Host(`b`) || Host(`a`)"), NormalizeMatch("Host(`a`)||Host(`b`)"); a != b {
		t.Errorf("host order and spacing should not matter: %q vs %q", a, b)
	}
	if NormalizeMatch("Host(`a`) && PathPrefix(`/x`)") == NormalizeMatch("Host(`a`) && PathPrefix(`/y`)") {
		t.Error("different paths must not normalize equal")
	}
	if a, b := NormalizeMatch("Host(`b`,`a`)"), NormalizeMatch("Host(`a`,`b`)"); a != b {
		t.Errorf("arguments of one Host call should be sorted: %q vs %q", a, b)
	}
}
