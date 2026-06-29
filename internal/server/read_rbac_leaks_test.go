package server

import (
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	pkgtraffic "github.com/skyhook-io/radar/pkg/traffic"
)

// flowVisibleForNamespaces is the per-flow filter that backs both the
// /api/traffic/flows and the stream endpoint for multi-namespace-restricted
// users (the source can only filter by a single namespace or none).
func TestFlowVisibleForNamespaces(t *testing.T) {
	flow := func(srcNS, dstNS string) pkgtraffic.Flow {
		return pkgtraffic.Flow{
			Source:      pkgtraffic.Endpoint{Namespace: srcNS},
			Destination: pkgtraffic.Endpoint{Namespace: dstNS},
		}
	}
	allowed := map[string]bool{"team-a": true}

	tests := []struct {
		name    string
		allowed map[string]bool
		flow    pkgtraffic.Flow
		want    bool
	}{
		{"nil set = all-namespace access", nil, flow("anything", "elsewhere"), true},
		{"source in allowed", allowed, flow("team-a", "other"), true},
		{"dest in allowed", allowed, flow("other", "team-a"), true},
		{"neither endpoint allowed", allowed, flow("other", "elsewhere"), false},
		{"external (empty) endpoints alone are not visible", allowed, flow("", ""), false},
		{"allowed source + external dest visible", allowed, flow("team-a", ""), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flowVisibleForNamespaces(tt.flow, tt.allowed); got != tt.want {
				t.Errorf("flowVisibleForNamespaces = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNamespaceLookup(t *testing.T) {
	if namespaceLookup(nil) != nil {
		t.Error("nil (all-namespace access) must map to nil, not an empty set")
	}
	set := namespaceLookup([]string{"a", "b"})
	if !set["a"] || !set["b"] || set["c"] {
		t.Errorf("namespaceLookup membership wrong: %+v", set)
	}
}

// secretReadableNamespaces gates Secret-derived data (TLS cert metadata) by
// per-user secrets RBAC, since the shared cache can hold secrets the user's
// own RBAC excludes. Mirrors the secrets gate in preflightResourceList.
func TestSecretReadableNamespaces(t *testing.T) {
	clusterWideReq := func(username string) *http.Request {
		r, _ := http.NewRequest("GET", "/api/certificates", nil)
		return r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: username}))
	}

	t.Run("cluster-wide namespaces, cluster-scope secrets denied -> none", func(t *testing.T) {
		env := newAuthTestServer(t)
		env.srv.permCache.Set("broad", &auth.UserPermissions{AllowedNamespaces: nil})
		env.srv.permCache.Get("broad").SetCanI("list", "", "secrets", "", false)

		got := env.srv.secretReadableNamespaces(clusterWideReq("broad"), nil)
		if len(got) != 0 || got == nil {
			t.Errorf("denied cluster-scope secrets must yield empty (none), got %#v", got)
		}
	})

	t.Run("cluster-wide namespaces, cluster-scope secrets allowed -> all (nil)", func(t *testing.T) {
		env := newAuthTestServer(t)
		env.srv.permCache.Set("admin", &auth.UserPermissions{AllowedNamespaces: nil})
		env.srv.permCache.Get("admin").SetCanI("list", "", "secrets", "", true)

		if got := env.srv.secretReadableNamespaces(clusterWideReq("admin"), nil); got != nil {
			t.Errorf("allowed cluster-scope secrets must yield nil (all), got %#v", got)
		}
	})

	t.Run("namespace-restricted -> per-namespace subset", func(t *testing.T) {
		env := newAuthTestServer(t)
		env.srv.permCache.Set("alice", &auth.UserPermissions{AllowedNamespaces: []string{"default", "kube-system"}})
		perms := env.srv.permCache.Get("alice")
		perms.SetCanI("list", "", "secrets", "default", true)
		perms.SetCanI("list", "", "secrets", "kube-system", false)

		got := env.srv.secretReadableNamespaces(clusterWideReq("alice"), []string{"default", "kube-system"})
		if len(got) != 1 || got[0] != "default" {
			t.Errorf("expected [default], got %#v", got)
		}
	})
}

// The Tier-1 namespace gates: a user whose namespace access excludes the
// target namespace must get 403, not data, on these read paths.
func TestProxyAuth_NamespaceGatedReadPaths(t *testing.T) {
	paths := []string{
		"/api/workloads/deployment/default/web/pods",
		"/api/metrics/pods/default/web-0",
		"/api/metrics/pods/default/web-0/history",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			env := newAuthTestServer(t)
			// carol can see "other", never "default" — getUserNamespaces
			// intersects requested(default) with allowed(other) -> empty.
			env.srv.permCache.Set("carol", &auth.UserPermissions{AllowedNamespaces: []string{"other"}})

			resp := env.authGet(t, p, "carol", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s: expected 403 for namespace-restricted user, got %d", p, resp.StatusCode)
			}
		})
	}
}
