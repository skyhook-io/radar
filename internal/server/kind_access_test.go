package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	integration "github.com/skyhook-io/radar/internal/integration"
)

func kindScopeRequest(env *authTestEnv, user string, perms *auth.UserPermissions) *http.Request {
	env.srv.permCache.Set(user, nil, perms)
	r := httptest.NewRequest(http.MethodGet, "/api/cnpg/workspace", nil)
	return r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: user}))
}

// A namespace the caller may list but Radar's informer does not hold was not
// read, and it is not a denial: the caller has the permission.
func TestTypedKindScope_UncachedIsNotDenied(t *testing.T) {
	env := newAuthTestServer(t)
	cache := capacityTestInformerScope{namespaces: map[string][]string{"pods": {"a"}}}

	perms := &auth.UserPermissions{}
	allow(perms, "", "pods", "", false)
	allow(perms, "", "pods", "a", true)
	allow(perms, "", "pods", "b", true)
	allow(perms, "", "pods", "c", false)
	r := kindScopeRequest(env, "alice", perms)

	t.Run("readable but uncached", func(t *testing.T) {
		acc, read := env.srv.typedKindScope(r, cache, []string{"a", "b"}, "", "pods")
		cov := acc.Coverage()
		if cov.State != integration.KindCoveragePartial || len(cov.DeniedNamespaces) != 0 || !slices.Equal(cov.UncachedNamespaces, []string{"b"}) || !slices.Equal(cov.AllowedNamespaces, []string{"a"}) {
			t.Errorf("coverage = %+v, want partial, uncached [b], allowed [a], nothing denied", cov)
		}
		if !slices.Equal(read, []string{"a"}) {
			t.Errorf("read = %v, want [a]", read)
		}
	})

	t.Run("wholly uncached", func(t *testing.T) {
		acc, read := env.srv.typedKindScope(r, cache, []string{"b"}, "", "pods")
		cov := acc.Coverage()
		if cov.State != integration.KindCoverageUncached || len(cov.DeniedNamespaces) != 0 || !slices.Equal(cov.UncachedNamespaces, []string{"b"}) || cov.AllowedNamespaces != nil {
			t.Errorf("coverage = %+v, want uncached naming b, nothing denied", cov)
		}
		if read == nil || len(read) != 0 {
			t.Errorf("read = %#v, want an empty non-nil scope (nil reads every namespace)", read)
		}
		if acc.Covers("b") {
			t.Error("an uncached namespace counts as covered")
		}
	})

	t.Run("denied and uncached together", func(t *testing.T) {
		acc, read := env.srv.typedKindScope(r, cache, []string{"a", "b", "c"}, "", "pods")
		cov := acc.Coverage()
		if cov.State != integration.KindCoveragePartial || !slices.Equal(cov.DeniedNamespaces, []string{"c"}) || !slices.Equal(cov.UncachedNamespaces, []string{"b"}) || !slices.Equal(cov.AllowedNamespaces, []string{"a"}) {
			t.Errorf("coverage = %+v, want partial, denied [c], uncached [b], allowed [a]", cov)
		}
		if !slices.Equal(read, []string{"a"}) {
			t.Errorf("read = %v, want [a]", read)
		}
	})

	t.Run("wholly uncached with a denial", func(t *testing.T) {
		acc, _ := env.srv.typedKindScope(r, cache, []string{"b", "c"}, "", "pods")
		cov := acc.Coverage()
		if cov.State != integration.KindCoverageUncached || !slices.Equal(cov.DeniedNamespaces, []string{"c"}) || !slices.Equal(cov.UncachedNamespaces, []string{"b"}) {
			t.Errorf("coverage = %+v, want uncached, denied [c], uncached [b]", cov)
		}
	})
}

// Uncached namespaces follow the same disclosure rule as denied ones: named
// only when the caller supplied the candidates.
func TestTypedKindScope_UncachedNamesFollowTheDisclosureRule(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{}
	allow(perms, "", "pods", "", true)
	r := kindScopeRequest(env, "wide", perms)

	limited := capacityTestInformerScope{namespaces: map[string][]string{"pods": {"a"}}}
	acc, read := env.srv.typedKindScope(r, limited, nil, "", "pods")
	cov := acc.Coverage()
	if cov.State != integration.KindCoveragePartial || len(cov.UncachedNamespaces) != 0 || !slices.Equal(cov.AllowedNamespaces, []string{"a"}) {
		t.Errorf("unfiltered: coverage = %+v, want partial with allowed [a] and no uncached names", cov)
	}
	if !slices.Equal(read, []string{"a"}) {
		t.Errorf("unfiltered: read = %v, want [a]", read)
	}

	empty := capacityTestInformerScope{namespaces: map[string][]string{}}
	acc, _ = env.srv.typedKindScope(r, empty, nil, "", "pods")
	if cov := acc.Coverage(); cov.State != integration.KindCoverageUncached || len(cov.UncachedNamespaces) != 0 {
		t.Errorf("unfiltered, nothing cached: coverage = %+v, want uncached without names", cov)
	}

	clusterWide := capacityTestInformerScope{clusterWide: map[string]bool{"pods": true}}
	acc, read = env.srv.typedKindScope(r, clusterWide, nil, "", "pods")
	if cov := acc.Coverage(); cov.State != integration.KindCoverageFull || read != nil {
		t.Errorf("cluster-wide informer: coverage = %+v read = %v, want full over every namespace", cov, read)
	}
}
