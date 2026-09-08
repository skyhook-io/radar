package mcp

import "testing"

// Mirrors internal/server's test: builtin kinds resolve without discovery so
// the unknown-kind passthrough never covers them.
func TestLookupResourceNameResolvesBuiltinsWithoutDiscovery(t *testing.T) {
	cases := []struct {
		kind, group, wantGroup, want string
	}{
		{"HorizontalPodAutoscaler", "autoscaling", "autoscaling", "horizontalpodautoscalers"},
		{"HorizontalPodAutoscaler", "", "autoscaling", "horizontalpodautoscalers"},
		{"Secret", "", "", "secrets"},
		{"Node", "", "", "nodes"},
		{"HorizontalPodAutoscaler", "vendor.example.com", "", ""},
		{"ScaledObject", "keda.sh", "", ""},
	}
	for _, tc := range cases {
		gotGroup, got := lookupResourceGVR(tc.kind, tc.group)
		if got != tc.want || gotGroup != tc.wantGroup {
			t.Errorf("lookupResourceGVR(%q, %q) = (%q, %q), want (%q, %q)", tc.kind, tc.group, gotGroup, got, tc.wantGroup, tc.want)
		}
	}
}
