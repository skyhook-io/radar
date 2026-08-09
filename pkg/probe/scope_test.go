package probe

import (
	"context"
	"testing"
	"time"
)

func TestClassifyAddressScope(t *testing.T) {
	cases := []struct {
		name  string
		addrs []string
		want  string
	}{
		{"globally routable", []string{"93.184.216.34"}, ScopePublic},
		{"rfc1918", []string{"10.0.0.5"}, ScopePrivate},
		{"rfc1918 172.16", []string{"172.16.4.1"}, ScopePrivate},
		{"loopback", []string{"127.0.0.1"}, ScopePrivate},
		// Common in managed clusters and overlay networks; IsPrivate misses it,
		// and calling it public would claim a public path that does not exist.
		{"carrier-grade NAT", []string{"100.64.1.1"}, ScopePrivate},
		{"CGNAT upper bound", []string{"100.127.255.255"}, ScopePrivate},
		{"just outside CGNAT is public", []string{"100.128.0.1"}, ScopePublic},
		{"link-local", []string{"169.254.1.1"}, ScopePrivate},
		{"ipv6 ULA", []string{"fd00::1"}, ScopePrivate},
		{"ipv6 global", []string{"2606:2800:220:1:248:1893:25c8:1946"}, ScopePublic},
		// Split-horizon DNS resolves the same name both ways; neither claim is
		// safe on its own.
		{"split horizon", []string{"10.0.0.5", "93.184.216.34"}, ScopeMixed},
		{"nothing resolved", nil, ""},
		{"unparseable", []string{"not-an-ip"}, ""},
	}
	for _, c := range cases {
		if got := classifyAddressScope(c.addrs); got != c.want {
			t.Errorf("%s: classifyAddressScope(%v) = %q, want %q", c.name, c.addrs, got, c.want)
		}
	}
}

// Through the real resolver, so the fields are proven populated by DNS() and not
// only by the classifier. localhost only - no external lookup in a unit test.
func TestDNSPopulatesAddressesAndScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := DNS(ctx, "localhost", VantageLocal)
	if !r.OK {
		t.Skipf("resolver unavailable: %v", r.Error)
	}
	if len(r.Addresses) == 0 {
		t.Fatal("Addresses not populated by DNS()")
	}
	if r.AddressScope != ScopePrivate {
		t.Errorf("localhost AddressScope = %q, want %q", r.AddressScope, ScopePrivate)
	}
}
