package urlutil

import "testing"

func TestNormalizeOrigin(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"https://Example.COM", "https://example.com:443", true},
		{"https://example.com:443/path?q=1", "https://example.com:443", true},
		{"http://example.com", "http://example.com:80", true},
		{"HTTP://example.com:080", "http://example.com:80", true},
		{"https://example.com:8443", "https://example.com:8443", true},
		{"https://[::1]:9090", "https://::1:9090", true},
		{"https://example.com:99999", "", false},
		{"https://example.com:http", "", false},
		{"/relative/path", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizeOrigin(c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q, %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}
