package server

import (
	"net/http"
	"testing"
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
