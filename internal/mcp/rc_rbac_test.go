package mcp

import "testing"

// Mirrors internal/server's test: builtin kinds resolve without discovery so
// the unknown-kind passthrough never covers them.
func TestLookupResourceNameResolvesBuiltinsWithoutDiscovery(t *testing.T) {
	cases := []struct {
		kind, group, want string
	}{
		{"HorizontalPodAutoscaler", "autoscaling", "horizontalpodautoscalers"},
		{"HorizontalPodAutoscaler", "", "horizontalpodautoscalers"},
		{"Secret", "", "secrets"},
		{"Node", "", "nodes"},
		{"HorizontalPodAutoscaler", "vendor.example.com", ""},
		{"ScaledObject", "keda.sh", ""},
	}
	for _, tc := range cases {
		if got := lookupResourceName(tc.kind, tc.group); got != tc.want {
			t.Errorf("lookupResourceName(%q, %q) = %q, want %q", tc.kind, tc.group, got, tc.want)
		}
	}
}
