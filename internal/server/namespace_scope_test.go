package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// newTestServer constructs a Server with just the state needed by the
// namespace-pick helpers. Avoids the full New() path so we can drive the
// helpers directly without spinning up auth providers or a router.
//
// Only restores the context name on cleanup — does NOT call ResetTestState
// (which would nuke the connection state TestMain established).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	prev := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prev) })
	return &Server{}
}

func reqAs(username string) *http.Request {
	r := httptest.NewRequest("GET", "/api/cluster/namespace", nil)
	if username != "" {
		ctx := pkgauth.ContextWithUser(r.Context(), &pkgauth.User{Username: username})
		r = r.WithContext(ctx)
	}
	return r
}

func TestNsPreferenceKey_PerUserIsolation(t *testing.T) {
	// Different users must produce distinct keys. Without the username,
	// one user's pick would shadow another's.
	if nsPreferenceKey("alice", "ctx") == nsPreferenceKey("bob", "ctx") {
		t.Error("alice and bob produced the same nsPreferenceKey")
	}
	// Same user, same context — keys must match.
	if nsPreferenceKey("alice", "ctx") != nsPreferenceKey("alice", "ctx") {
		t.Error("nsPreferenceKey is not deterministic")
	}
	// Empty username (no-auth) collapses to a per-context key.
	if nsPreferenceKey("", "ctx-a") == nsPreferenceKey("", "ctx-b") {
		t.Error("no-auth keys for different contexts should differ")
	}
	// Substring confusion: alice/foo must not collide with alic/efoo etc.
	// The \x00 separator makes this safe — verify by counterexample.
	if nsPreferenceKey("alice", "foo") == nsPreferenceKey("ali", "cefoo") {
		t.Error("nsPreferenceKey separator is ambiguous")
	}
}

func TestSetAndGetActiveNamespaceForUser_PerUser(t *testing.T) {
	s := newTestServer(t)

	// Alice picks alpha; Bob picks beta + gamma. Each must read back their own picks.
	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha"})
	s.setActiveNamespaceForUser(reqAs("bob"), []string{"beta", "gamma"})

	if got := s.getActiveNamespaceForUser(reqAs("alice")); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("alice: got %v, want [alpha]", got)
	}
	if got := s.getActiveNamespaceForUser(reqAs("bob")); !slices.Equal(got, []string{"beta", "gamma"}) {
		t.Errorf("bob: got %v, want [beta gamma]", got)
	}

	// A third user with no pick gets the empty default.
	if got := s.getActiveNamespaceForUser(reqAs("carol")); len(got) != 0 {
		t.Errorf("carol: expected empty pick, got %v", got)
	}
}

func TestSetActiveNamespaceForUser_EmptyClears(t *testing.T) {
	s := newTestServer(t)

	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha", "beta"})
	s.setActiveNamespaceForUser(reqAs("alice"), nil) // clear

	if got := s.getActiveNamespaceForUser(reqAs("alice")); len(got) != 0 {
		t.Errorf("expected empty after nil-clear, got %v", got)
	}

	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha"})
	s.setActiveNamespaceForUser(reqAs("alice"), []string{}) // empty slice also clears

	if got := s.getActiveNamespaceForUser(reqAs("alice")); len(got) != 0 {
		t.Errorf("expected empty after empty-slice clear, got %v", got)
	}
}

func TestSetActiveNamespaceForUser_NoAuth(t *testing.T) {
	s := newTestServer(t)

	// Auth disabled — empty username path. The key is still per-context.
	s.setActiveNamespaceForUser(reqAs(""), []string{"alpha"})
	if got := s.getActiveNamespaceForUser(reqAs("")); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("no-auth: got %v, want [alpha]", got)
	}
}

func TestSetActiveNamespaceForUser_DefensiveCopy(t *testing.T) {
	// Mutating the caller's slice after a Set must not corrupt stored state.
	s := newTestServer(t)
	picks := []string{"alpha", "beta"}
	s.setActiveNamespaceForUser(reqAs("alice"), picks)
	picks[0] = "MUTATED"

	got := s.getActiveNamespaceForUser(reqAs("alice"))
	if !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("stored picks were mutated by caller: got %v", got)
	}
}

func TestSetActiveNamespaceForUser_NoContext(t *testing.T) {
	// When no kubeconfig context is set (e.g. before initial connection),
	// set/get must be no-ops — there's no cluster to scope to.
	prev := k8s.SetTestContextName("")
	t.Cleanup(func() { k8s.SetTestContextName(prev) })
	s := &Server{}

	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha"})
	if got := s.getActiveNamespaceForUser(reqAs("alice")); len(got) != 0 {
		t.Errorf("expected empty without context, got %v", got)
	}
}

func TestClearAllNamespacePreferences(t *testing.T) {
	s := newTestServer(t)

	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha"})
	s.setActiveNamespaceForUser(reqAs("bob"), []string{"beta", "gamma"})
	s.setActiveNamespaceForUser(reqAs(""), []string{"delta"})

	s.clearAllNamespacePreferences()

	for _, user := range []string{"alice", "bob", ""} {
		if got := s.getActiveNamespaceForUser(reqAs(user)); len(got) != 0 {
			t.Errorf("user=%q: expected cleared, got %v", user, got)
		}
	}
}

func TestFinalizePostContextSwitch_ClearsBothCaches(t *testing.T) {
	// Pin the load-bearing claim from the comment on finalizePostContextSwitch:
	// it MUST clear permCache AND every user's namespace pick. A regression
	// that drops either side leaves stale state attached to the new cluster.
	s := newTestServer(t)
	s.permCache = pkgauth.NewPermissionCache()
	s.permCache.Set("alice", &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha"})
	s.setActiveNamespaceForUser(reqAs("bob"), []string{"beta", "gamma"})

	s.finalizePostContextSwitch()

	if got := s.permCache.Get("alice"); got != nil {
		t.Errorf("permCache.Get(alice) = %+v after finalize, want nil", got)
	}
	if got := s.getActiveNamespaceForUser(reqAs("alice")); len(got) != 0 {
		t.Errorf("alice ns pick survived: %v", got)
	}
	if got := s.getActiveNamespaceForUser(reqAs("bob")); len(got) != 0 {
		t.Errorf("bob ns pick survived: %v", got)
	}
}

func TestFinalizePostContextSwitch_NilPermCacheNoCrash(t *testing.T) {
	// finalizePostContextSwitch is called from CAPI connect / context switch
	// before s.permCache may have been initialized in some paths; guarding
	// nil is the contract.
	s := newTestServer(t)
	s.permCache = nil
	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha"})

	s.finalizePostContextSwitch() // must not panic

	if got := s.getActiveNamespaceForUser(reqAs("alice")); len(got) != 0 {
		t.Errorf("ns pick survived nil-permCache finalize: %v", got)
	}
}

func TestClearAllNamespacePreferences_OnContextSwitch(t *testing.T) {
	// Picks made under context A must not survive a switch to context B —
	// they reference namespaces that don't exist on the new cluster.
	s := newTestServer(t)

	k8s.SetTestContextName("ctx-a")
	s.setActiveNamespaceForUser(reqAs("alice"), []string{"alpha", "beta"})

	// Switch context (callers do this via PerformContextSwitch which calls
	// clearAllNamespacePreferences before swapping context).
	s.clearAllNamespacePreferences()
	k8s.SetTestContextName("ctx-b")

	if got := s.getActiveNamespaceForUser(reqAs("alice")); len(got) != 0 {
		t.Errorf("pick survived context switch: got %v", got)
	}
}

func TestIntersectPicksWithAllowed(t *testing.T) {
	tests := []struct {
		name    string
		picks   []string
		allowed []string
		want    []string
	}{
		{
			name:    "empty picks returns nil (no narrowing)",
			picks:   nil,
			allowed: []string{"alpha", "beta"},
			want:    nil,
		},
		{
			name:    "nil allowed = cluster-admin pass-through",
			picks:   []string{"alpha", "beta"},
			allowed: nil,
			want:    []string{"alpha", "beta"},
		},
		{
			name:    "all picks allowed",
			picks:   []string{"alpha", "beta"},
			allowed: []string{"alpha", "beta", "gamma"},
			want:    []string{"alpha", "beta"},
		},
		{
			name:    "partial revocation drops only stale entries",
			picks:   []string{"alpha", "beta", "gamma"},
			allowed: []string{"alpha", "gamma"},
			want:    []string{"alpha", "gamma"},
		},
		{
			name:    "full revocation returns empty (caller decides to clear)",
			picks:   []string{"alpha", "beta"},
			allowed: []string{"gamma", "delta"},
			want:    []string{},
		},
		{
			name:    "preserves pick order",
			picks:   []string{"gamma", "alpha", "beta"},
			allowed: []string{"alpha", "beta", "gamma"},
			want:    []string{"gamma", "alpha", "beta"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intersectPicksWithAllowed(tt.picks, tt.allowed)
			if !slices.Equal(got, tt.want) {
				t.Errorf("intersectPicksWithAllowed(%v, %v) = %v, want %v", tt.picks, tt.allowed, got, tt.want)
			}
		})
	}
}

func TestResolveHelmNamespaces_NoAuthUsesBackendFallback(t *testing.T) {
	s := newTestServer(t)
	restoreHelmNamespaceFallbackState(t)

	got, ok := s.resolveHelmNamespaces(reqAs(""))
	if !ok {
		t.Fatal("resolveHelmNamespaces returned ok=false")
	}
	if !slices.Equal(got, []string{"backend-fallback"}) {
		t.Fatalf("namespaces = %v, want backend fallback namespace", got)
	}
}

func TestResolveHelmNamespaces_AuthenticatedClusterWideUserDoesNotUseBackendFallback(t *testing.T) {
	s := newTestServer(t)
	restoreHelmNamespaceFallbackState(t)

	s.permCache = pkgauth.NewPermissionCache()
	s.permCache.Set("alice", &pkgauth.UserPermissions{AllowedNamespaces: nil})

	got, ok := s.resolveHelmNamespaces(reqAs("alice"))
	if !ok {
		t.Fatal("resolveHelmNamespaces returned ok=false")
	}
	if got != nil {
		t.Fatalf("namespaces = %v, want nil so Helm lists as the impersonated user cluster-wide", got)
	}
}

func restoreHelmNamespaceFallbackState(t *testing.T) {
	t.Helper()

	prevTimeout := k8s.NamespaceListTimeout
	k8s.NamespaceListTimeout = 100 * time.Millisecond
	t.Cleanup(func() { k8s.NamespaceListTimeout = prevTimeout })

	prevClient := k8s.SetTestClient(nil)
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })

	dummyClient, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("creating dummy client: %v", err)
	}
	k8s.SetTestClient(dummyClient)

	k8s.SetFallbackNamespace("backend-fallback")
	t.Cleanup(func() { k8s.SetFallbackNamespace("") })
}

func TestParseNamespacesForUser_ForcedCacheScope(t *testing.T) {
	s := newTestServer(t)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("prod")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})

	cases := []struct {
		name string
		url  string
		want []string
	}{
		{name: "no query uses cache scope", url: "/api/resources/pods", want: []string{"prod"}},
		{name: "query including cache scope narrows to cache scope", url: "/api/resources/pods?namespaces=prod,staging", want: []string{"prod"}},
		{name: "query outside cache scope returns no access", url: "/api/resources/pods?namespace=staging", want: []string{}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.url, nil)
			if got := s.parseNamespacesForUser(req); !slices.Equal(got, tt.want) {
				t.Fatalf("parseNamespacesForUser(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
