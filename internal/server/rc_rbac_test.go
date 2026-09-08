package server

import "testing"

// Discovery is nil in unit tests, so any kind that resolves here does so
// without it. Builtin kinds must, or the unknown-kind passthrough would grant
// their refs (and, for HPAs, the attached diagnosis) while discovery is cold.
func TestLookupResourceNameResolvesBuiltinsWithoutDiscovery(t *testing.T) {
	cases := []struct {
		kind, group, want string
	}{
		{"HorizontalPodAutoscaler", "autoscaling", "horizontalpodautoscalers"},
		{"HorizontalPodAutoscaler", "", "horizontalpodautoscalers"},
		{"Secret", "", "secrets"},
		{"Deployment", "apps", "deployments"},
		{"Node", "", "nodes"},
		// A same-kind CRD in another group must not borrow the builtin's SAR.
		{"HorizontalPodAutoscaler", "vendor.example.com", ""},
		{"ScaledObject", "keda.sh", ""},
	}
	for _, tc := range cases {
		if got := lookupResourceName(tc.kind, tc.group); got != tc.want {
			t.Errorf("lookupResourceName(%q, %q) = %q, want %q", tc.kind, tc.group, got, tc.want)
		}
	}
}
