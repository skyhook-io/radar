package mcp

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// withTestUserPerms primes the MCP perm cache for `username` with `allowed`
// (nil = cluster-admin pass-through, [] = no access). Returns a context that
// has the user attached. Resets the cache on cleanup so tests don't bleed.
func withTestUserPerms(t *testing.T, username string, groups []string, allowed []string) context.Context {
	t.Helper()
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: username, Groups: groups})
	getPermCache().Set(username, &pkgauth.UserPermissions{AllowedNamespaces: allowed})
	t.Cleanup(func() {
		getPermCache().Invalidate()
	})
	return ctx
}

func TestFilterNamespacesForUser_NoAuth(t *testing.T) {
	// No user on context (auth disabled): pass-through whatever was requested.
	got := filterNamespacesForUser(context.Background(), []string{"a", "b"})
	if !equalSlice(got, []string{"a", "b"}) {
		t.Errorf("got %v, want [a b]", got)
	}
	if got := filterNamespacesForUser(context.Background(), nil); got != nil {
		t.Errorf("got %v, want nil (no filter)", got)
	}
}

func TestFilterNamespacesForUser_ClusterAdmin(t *testing.T) {
	// nil AllowedNamespaces means cluster-admin: requests pass through.
	ctx := withTestUserPerms(t, "alice", nil, nil)
	if got := filterNamespacesForUser(ctx, nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
	if got := filterNamespacesForUser(ctx, []string{"foo"}); !equalSlice(got, []string{"foo"}) {
		t.Errorf("got %v, want [foo]", got)
	}
}

func TestFilterNamespacesForUser_Restricted(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, []string{"team-a", "team-b"})

	// "All namespaces" request → only allowed set.
	got := filterNamespacesForUser(ctx, nil)
	if !equalSlice(got, []string{"team-a", "team-b"}) {
		t.Errorf("got %v, want [team-a team-b]", got)
	}

	// Specific allowed namespace → returned.
	got = filterNamespacesForUser(ctx, []string{"team-a"})
	if !equalSlice(got, []string{"team-a"}) {
		t.Errorf("got %v, want [team-a]", got)
	}

	// Specific denied namespace → empty (not nil).
	got = filterNamespacesForUser(ctx, []string{"secret-ops"})
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want empty slice", got)
	}
}

func TestCheckNamespaceAccess(t *testing.T) {
	// checkNamespaceAccess is for namespaced reads only. Empty namespace
	// always returns false — callers must route cluster-scoped reads
	// through canReadClusterScopedKind, where per-kind SAR is the gate.

	// No auth, namespaced: pass-through.
	if !checkNamespaceAccess(context.Background(), "anything") {
		t.Error("no-auth should pass all namespaces")
	}
	// No auth, cluster-scoped: false (caller misuse — must use the cluster helper).
	if checkNamespaceAccess(context.Background(), "") {
		t.Error(`checkNamespaceAccess("") must return false — use canReadClusterScopedKind for cluster-scoped reads`)
	}

	// Cluster-admin (nil AllowedNamespaces): namespaced passes, cluster-
	// scoped routes to canReadClusterScopedKind (returns false here too).
	adminCtx := withTestUserPerms(t, "admin", nil, nil)
	if !checkNamespaceAccess(adminCtx, "kube-system") {
		t.Error("cluster-admin should access any namespace")
	}
	if checkNamespaceAccess(adminCtx, "") {
		t.Error(`checkNamespaceAccess("") must return false even for cluster-admin`)
	}

	// Restricted: only allowed namespaces pass.
	restrictedCtx := withTestUserPerms(t, "carol", nil, []string{"prod"})
	if !checkNamespaceAccess(restrictedCtx, "prod") {
		t.Error("restricted user should access their namespace")
	}
	if checkNamespaceAccess(restrictedCtx, "kube-system") {
		t.Error("restricted user should not access denied namespace")
	}
	if checkNamespaceAccess(restrictedCtx, "") {
		t.Error("restricted user should not access cluster-scoped reads")
	}
}

// stubSubjectCanI swaps the SubjectAccessReview call for the duration of a
// test, so we can exercise canReadClusterScopedKind's fail-closed paths
// without standing up a fake apiserver. Returns the previous value via cleanup.
func stubSubjectCanI(t *testing.T, fn func(ctx context.Context, client kubernetes.Interface, username string, groups []string, namespace, group, resource, verb string) (bool, error)) {
	t.Helper()
	prev := subjectCanI
	subjectCanI = fn
	t.Cleanup(func() { subjectCanI = prev })
}

func TestCanReadClusterScopedKind_NoUser(t *testing.T) {
	// Auth disabled (no user on context) — passthrough, no SAR call.
	called := false
	stubSubjectCanI(t, func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		called = true
		return false, nil
	})
	if !canReadClusterScopedKind(context.Background(), "nodes", "", "list") {
		t.Error("no-auth caller should be allowed (passthrough)")
	}
	if called {
		t.Error("no-auth path should not invoke SubjectAccessReview")
	}
}

func TestCanReadClusterScopedKind_NamespacedKind(t *testing.T) {
	// A namespaced kind classifies as not-cluster-scoped, so the helper
	// passes through without any SAR — namespace-based gating handles it.
	ctx := withTestUserPerms(t, "alice", nil, []string{"alpha"})
	called := false
	stubSubjectCanI(t, func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		called = true
		return true, nil
	})
	if !canReadClusterScopedKind(ctx, "pods", "", "list") {
		t.Error("namespaced kinds should pass through")
	}
	if called {
		t.Error("namespaced-kind passthrough should not invoke SAR")
	}
}

func TestCanReadClusterScopedKind_SARError_FailsClosed(t *testing.T) {
	// SAR returning an error must deny — apiserver hiccup must not let a
	// caller through and must not poison the cache.
	ctx := withTestUserPerms(t, "alice", nil, []string{"alpha"})
	stubSubjectCanI(t, func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		return true, errors.New("apiserver unreachable") // 'allowed' must be ignored on err
	})
	// The helper's k8s.GetClient() returns nil here (test harness has no
	// client wired up) which is also a fail-closed path — verify that.
	if got := canReadClusterScopedKind(ctx, "nodes", "", "list"); got {
		t.Error("fail-closed: must deny when apiserver is unavailable or SAR errors")
	}

	// And the deny must NOT be cached — the next call should also flow
	// through the fail-closed path, not a stale cached false.
	perms := getPermCache().Get("alice")
	if perms == nil {
		t.Fatal("user perms unexpectedly evicted")
	}
	if _, ok := perms.CanI("list", "", "nodes", ""); ok {
		t.Error("SAR error / no-client path must not poison the canI cache")
	}
}

func TestCanReadClusterScopedKind_AllowedCachesResult(t *testing.T) {
	// On success, the result is stored in the per-user canI cache so
	// subsequent calls short-circuit without hitting the apiserver.
	ctx := withTestUserPerms(t, "alice", nil, []string{"alpha"})
	calls := 0
	stubSubjectCanI(t, func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		calls++
		return true, nil
	})
	// First call — without a real K8s client this still hits the fail-closed
	// path (no client). To exercise the cache hit, seed it directly:
	perms := getPermCache().Get("alice")
	perms.SetCanI("list", "", "nodes", "", true)

	if !canReadClusterScopedKind(ctx, "nodes", "", "list") {
		t.Error("seeded allow should pass through cache")
	}
	if calls != 0 {
		t.Errorf("cache hit should skip SAR; got %d calls", calls)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
