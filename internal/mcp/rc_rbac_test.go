package mcp

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
)

// The reference checker tells a refusal from a check it couldn't make, so a
// declared name it can't check reads unverified.
func TestRequestScopedCheckerDecidesRefusedAndUnchecked(t *testing.T) {
	ctx := withTestUserPerms(t, "reader", nil, []string{"alpha"})
	perms := getPermCache().Get("reader", nil)
	perms.SetCanI("get", "", "secrets", "alpha", false)
	perms.SetCanI("get", "", "configmaps", "alpha", true)
	checker := newMCPRequestScopedChecker(ctx)
	for _, tc := range []struct {
		kind, namespace string
		want            ownership.Decision
	}{
		{"ConfigMap", "alpha", ownership.Allowed},
		{"Secret", "alpha", ownership.Denied},
		{"Secret", "beta", ownership.Undecided},
	} {
		if got := checker.DecideRead(ctx, "", tc.kind, tc.namespace); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.kind, tc.namespace, got, tc.want)
		}
		if got := checker.CanRead(ctx, "", tc.kind, tc.namespace); got != (tc.want == ownership.Allowed) {
			t.Errorf("%s in %s: CanRead = %v", tc.kind, tc.namespace, got)
		}
	}
}
