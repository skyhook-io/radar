package mcp

import (
	"context"
	"testing"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// TestResolveUserPerms_GroupChangeNotServedStaleCeiling pins the caller-layer
// behavior of the permission-cache group-key fix at the MCP namespace-
// resolution path (resolveUserPerms). The cached UserPermissions entry is
// produced by SubjectAccessReviews run with username AND groups, so the same
// username arriving with a DIFFERENT group set must never be served the other
// identity's cached ceiling — it must recompute.
//
// In this unit env there is no live apiserver (k8s.GetClient() == nil), so a
// recompute fail-closes to no-access (AllowedNamespaces == []). That is the
// load-bearing signal: a different group set is a cache MISS (recompute), not a
// hit on the other identity's entry. Both directions are covered so a stale
// entry from either the stronger or the weaker identity can never leak.
//
// If the cache regressed to username-only keying, the uncached group set would
// hit the seeded entry and inherit its ceiling — the assertions below fail.
func TestResolveUserPerms_GroupChangeNotServedStaleCeiling(t *testing.T) {
	strong := []string{"platform-admins"}
	weak := []string{"viewers"}

	userCtx := func(username string, groups []string) context.Context {
		return pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: username, Groups: groups})
	}

	t.Run("stronger cached, weaker must not inherit", func(t *testing.T) {
		getPermCache().Invalidate()
		t.Cleanup(func() { getPermCache().Invalidate() })

		// alice/[platform-admins]: cluster-admin ceiling (nil = all namespaces).
		getPermCache().Set("alice", strong, &pkgauth.UserPermissions{AllowedNamespaces: nil})

		// Sanity: the cached identity resolves to its own (cluster-admin) ceiling.
		if _, p := resolveUserPerms(userCtx("alice", strong)); p == nil || p.AllowedNamespaces != nil {
			t.Fatalf("cached strong identity perms = %+v, want cluster-admin (AllowedNamespaces == nil)", p)
		}

		// alice/[viewers] is a different identity: it must recompute (miss →
		// fail-closed here), NOT inherit the cluster-admin ceiling.
		_, p := resolveUserPerms(userCtx("alice", weak))
		if p == nil {
			t.Fatal("resolveUserPerms returned nil perms for an authenticated user")
		}
		if p.AllowedNamespaces == nil {
			t.Fatal("weaker identity inherited the cluster-admin ceiling (nil = all namespaces) from a different group set")
		}
	})

	t.Run("weaker cached, stronger must not inherit", func(t *testing.T) {
		getPermCache().Invalidate()
		t.Cleanup(func() { getPermCache().Invalidate() })

		// bob/[viewers]: namespace-restricted ceiling.
		getPermCache().Set("bob", weak, &pkgauth.UserPermissions{AllowedNamespaces: []string{"team-a"}})

		// Sanity: the cached identity resolves to its own restricted ceiling.
		if _, p := resolveUserPerms(userCtx("bob", weak)); p == nil || !equalSlice(p.AllowedNamespaces, []string{"team-a"}) {
			t.Fatalf("cached weak identity perms = %+v, want AllowedNamespaces [team-a]", p)
		}

		// bob/[platform-admins] is a different identity: it must recompute
		// (miss → fail-closed), NOT be served the restricted [team-a] entry.
		_, p := resolveUserPerms(userCtx("bob", strong))
		if p == nil {
			t.Fatal("resolveUserPerms returned nil perms for an authenticated user")
		}
		if equalSlice(p.AllowedNamespaces, []string{"team-a"}) {
			t.Fatal("stronger identity was served the weaker identity's cached ceiling [team-a] from a different group set")
		}
	})
}
