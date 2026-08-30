package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// reqAsGroups builds a request whose context carries an authenticated user with
// the given username AND group set — the two inputs getUserNamespaces keys the
// permission cache on.
func reqAsGroups(username string, groups []string) *http.Request {
	r := httptest.NewRequest("GET", "/api/resources/pods", nil)
	ctx := auth.ContextWithUser(r.Context(), &auth.User{Username: username, Groups: groups})
	return r.WithContext(ctx)
}

// TestGetUserNamespaces_GroupChangeNotServedStaleCeiling pins the caller-layer
// behavior of the permission-cache group-key fix at the real namespace-
// resolution path (getUserNamespaces). The cached UserPermissions entry is
// produced by SubjectAccessReviews run with username AND groups, so the same
// username arriving with a DIFFERENT group set must never be served the other
// identity's cached namespace ceiling — it must recompute.
//
// In this unit env there is no live apiserver (k8s.GetClient() == nil), so a
// recompute fail-closes to no-access ([]). That is exactly the load-bearing
// signal: a different group set is a cache MISS (recompute), not a hit on the
// other identity's entry. Both directions are covered so a stale entry from
// either the stronger or the weaker identity can never leak to the other.
//
// If the cache regressed to username-only keying, the uncached group set would
// hit the seeded entry and inherit its ceiling — the assertions below flip to
// failures.
func TestGetUserNamespaces_GroupChangeNotServedStaleCeiling(t *testing.T) {
	strong := []string{"platform-admins"}
	weak := []string{"viewers"}
	requested := []string{"default", "kube-system"}

	t.Run("stronger cached, weaker must not inherit", func(t *testing.T) {
		s := newAuthServer(auth.Config{Mode: "proxy"})
		// alice/[platform-admins]: cluster-admin ceiling (nil = all namespaces).
		s.permCache.Set("alice", strong, &auth.UserPermissions{AllowedNamespaces: nil})

		// Sanity: the cached identity resolves to its own (cluster-admin) ceiling.
		if got := s.getUserNamespaces(reqAsGroups("alice", strong), requested); !slices.Equal(got, requested) {
			t.Fatalf("cached strong identity ceiling = %v, want %v (cluster-admin sees all requested)", got, requested)
		}

		// alice/[viewers] is a different identity: it must recompute (miss →
		// fail-closed here), NOT be served the admin ceiling.
		got := s.getUserNamespaces(reqAsGroups("alice", weak), requested)
		if len(got) != 0 {
			t.Fatalf("weaker identity inherited the admin ceiling from a different group set: got %v, want [] (recompute)", got)
		}
	})

	t.Run("weaker cached, stronger must not inherit", func(t *testing.T) {
		s := newAuthServer(auth.Config{Mode: "proxy"})
		// bob/[viewers]: namespace-restricted ceiling.
		s.permCache.Set("bob", weak, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})

		// Sanity: the cached identity resolves to its own restricted ceiling.
		if got := s.getUserNamespaces(reqAsGroups("bob", weak), requested); !slices.Equal(got, []string{"default"}) {
			t.Fatalf("cached weak identity ceiling = %v, want [default]", got)
		}

		// bob/[platform-admins] is a different identity: it must recompute
		// (miss → fail-closed), NOT be served the restricted ["default"] entry.
		got := s.getUserNamespaces(reqAsGroups("bob", strong), []string{"default"})
		if len(got) != 0 {
			t.Fatalf("stronger identity was served the weaker identity's cached ceiling: got %v, want [] (recompute)", got)
		}
	})
}
