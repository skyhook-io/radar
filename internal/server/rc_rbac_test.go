package server

import "testing"

// Discovery is nil in unit tests, so any kind that resolves here does so
// without it. Builtin kinds must, or the unknown-kind passthrough would grant
// their refs (and, for HPAs, the attached diagnosis) while discovery is cold;
// and the group must come back resolved, because topology refs carry an empty
// group for builtins in that same window.
func TestLookupResourceNameResolvesBuiltinsWithoutDiscovery(t *testing.T) {
	cases := []struct {
		kind, group, wantGroup, want string
	}{
		{"HorizontalPodAutoscaler", "autoscaling", "autoscaling", "horizontalpodautoscalers"},
		{"HorizontalPodAutoscaler", "", "autoscaling", "horizontalpodautoscalers"},
		{"Secret", "", "", "secrets"},
		{"Deployment", "apps", "apps", "deployments"},
		{"Node", "", "", "nodes"},
		// A same-kind CRD in another group must not borrow the builtin's SAR.
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
