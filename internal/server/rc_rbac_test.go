package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/ownership"
)

// The reference checker tells a refusal from a check it couldn't make, so a
// declared name it can't check reads unverified.
func TestRequestScopedCheckerDecidesRefusedAndUnchecked(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	perms.SetCanI("get", "", "secrets", "alpha", false)
	perms.SetCanI("get", "", "configmaps", "alpha", true)
	env.srv.permCache.Set("reader", nil, perms)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "reader"}))
	checker := env.srv.newRequestScopedChecker(r)
	for _, tc := range []struct {
		kind, namespace string
		want            ownership.Decision
	}{
		{"ConfigMap", "alpha", ownership.Allowed},
		{"Secret", "alpha", ownership.Denied},
		{"Secret", "beta", ownership.Undecided},
	} {
		if got := checker.DecideRead(r.Context(), "", tc.kind, tc.namespace); got != tc.want {
			t.Errorf("%s in %s = %v, want %v", tc.kind, tc.namespace, got, tc.want)
		}
		if got := checker.CanRead(r.Context(), "", tc.kind, tc.namespace); got != (tc.want == ownership.Allowed) {
			t.Errorf("%s in %s: CanRead = %v", tc.kind, tc.namespace, got)
		}
	}
}
