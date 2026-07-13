package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
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

// The shared TestMain cache holds exactly two namespaces: "default" and
// "broken". Anything else counts as deleted from the cluster.

func TestParseNamespacesForUser_EvictsDeletedSavedPick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)

	// A pick saved in a previous session names a namespace that has since
	// been deleted from the cluster.
	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"test-ctx": {"ghost"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	if got := s.parseNamespacesForUser(req); got != nil {
		t.Fatalf("parseNamespacesForUser = %v, want nil (unfiltered) after stale-pick eviction", got)
	}
	if picks := s.getActiveNamespaceForUser(reqAs("")); len(picks) != 0 {
		t.Errorf("stale pick survived in memory: %v", picks)
	}
	// The eviction must reach settings.json — otherwise loadSavedNamespace-
	// Preference re-seeds the stale pick on the next request.
	if saved := settings.Load().ActiveNamespaces["test-ctx"]; len(saved) != 0 {
		t.Errorf("stale pick survived in settings: %v", saved)
	}
}

func TestParseNamespacesForUser_TrimsPartiallyDeletedPick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)

	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"test-ctx": {"default", "ghost"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	if got := s.parseNamespacesForUser(req); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("parseNamespacesForUser = %v, want [default]", got)
	}
	if picks := s.getActiveNamespaceForUser(reqAs("")); !slices.Equal(picks, []string{"default"}) {
		t.Errorf("in-memory pick = %v, want [default]", picks)
	}
	if saved := settings.Load().ActiveNamespaces["test-ctx"]; !slices.Equal(saved, []string{"default"}) {
		t.Errorf("saved pick = %v, want [default]", saved)
	}
}

func TestParseNamespacesForUser_ValidSavedPickStillFilters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	s.setActiveNamespaceForUser(reqAs(""), []string{"default"})

	req := httptest.NewRequest("GET", "/api/resources/pods", nil)
	if got := s.parseNamespacesForUser(req); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("parseNamespacesForUser = %v, want [default]", got)
	}
	if picks := s.getActiveNamespaceForUser(reqAs("")); !slices.Equal(picks, []string{"default"}) {
		t.Errorf("valid pick was disturbed: %v", picks)
	}
}

func TestPruneToExistingNamespaces(t *testing.T) {
	tests := []struct {
		name     string
		picks    []string
		existing []string
		want     []string
	}{
		{
			name:     "nil existing (informer unavailable) leaves picks alone",
			picks:    []string{"alpha", "beta"},
			existing: nil,
			want:     []string{"alpha", "beta"},
		},
		{
			name:     "empty existing leaves picks alone",
			picks:    []string{"alpha"},
			existing: []string{},
			want:     []string{"alpha"},
		},
		{
			name:     "deleted namespace dropped, survivors kept",
			picks:    []string{"alpha", "ghost"},
			existing: []string{"alpha", "beta"},
			want:     []string{"alpha"},
		},
		{
			name:     "all deleted returns empty",
			picks:    []string{"ghost", "phantom"},
			existing: []string{"alpha"},
			want:     []string{},
		},
		{
			// Duplicates are preserved when they still exist — the prune must
			// not dedup, so a no-op prune leaves the pick byte-for-byte equal.
			name:     "duplicates preserved when all exist",
			picks:    []string{"alpha", "alpha"},
			existing: []string{"alpha", "beta"},
			want:     []string{"alpha", "alpha"},
		},
		{
			// Duplicate survives, the deleted sibling is dropped — the survivor
			// count differs from the input, so the eviction still commits.
			name:     "duplicate kept, deleted sibling dropped",
			picks:    []string{"alpha", "alpha", "ghost"},
			existing: []string{"alpha", "beta"},
			want:     []string{"alpha", "alpha"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pruneToExistingNamespaces(tt.picks, tt.existing)
			if !slices.Equal(got, tt.want) {
				t.Errorf("pruneToExistingNamespaces(%v, %v) = %v, want %v", tt.picks, tt.existing, got, tt.want)
			}
		})
	}
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

// The prune validates a snapshot of the pick; if a concurrent POST replaces
// the pick before the prune's write, the write must be skipped — otherwise a
// slow read reverts the user's fresh pick to pruned survivors of the old one.
func TestPruneDeletedNamespacePicks_SkipsWhenPickChangedMidFlight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	req := reqAs("")

	// The stale snapshot a slow read is working from.
	snapshot := []string{"default", "ghost"}
	s.setActiveNamespaceForUser(req, snapshot)

	// A user POST lands while the read is pruning its snapshot.
	s.setActiveNamespaceForUser(req, []string{"broken"})

	survivors := s.pruneDeletedNamespacePicks(req, "test-ctx", snapshot)
	if !slices.Equal(survivors, []string{"default"}) {
		t.Fatalf("survivors = %v, want [default] (this request still filters by its own snapshot)", survivors)
	}
	// The fresh pick must be untouched — the prune's write was skipped.
	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, []string{"broken"}) {
		t.Errorf("fresh pick reverted by stale prune: %v, want [broken]", picks)
	}
	if saved := settings.Load().ActiveNamespaces["test-ctx"]; len(saved) != 0 {
		t.Errorf("stale prune persisted survivors over the fresh pick: %v", saved)
	}
}

// commitPickMutation is the single guarded writer every read-path pick
// mutation goes through. A stale snapshot must never revert a concurrent POST.
func TestCommitPickMutation_SkipsWhenPickChangedMidFlight(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	req := reqAs("")

	// The snapshot a slow read computed its survivors from.
	snapshot := []string{"default", "ghost"}
	s.setActiveNamespaceForUser(req, snapshot)

	// A user POST replaces the pick before the read commits.
	s.setActiveNamespaceForUser(req, []string{"broken"})

	// The read tries to trim its stale snapshot down to survivors.
	s.commitPickMutation(req, "test-ctx", snapshot, []string{"default"}, false)

	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, []string{"broken"}) {
		t.Errorf("fresh pick reverted by stale mutation: %v, want [broken]", picks)
	}
}

// A commit whose snapshot still matches the live pick must go through — the
// guard only skips on mismatch, it doesn't block the common case.
func TestCommitPickMutation_AppliesWhenSnapshotStillLive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	req := reqAs("")

	snapshot := []string{"default", "ghost"}
	s.setActiveNamespaceForUser(req, snapshot)

	s.commitPickMutation(req, "test-ctx", snapshot, []string{"default"}, false)

	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, []string{"default"}) {
		t.Errorf("in-memory pick = %v, want [default]", picks)
	}
	// persist=false: the in-memory trim must NOT touch settings.json.
	if saved := settings.Load().ActiveNamespaces["test-ctx"]; len(saved) != 0 {
		t.Errorf("view-filter trim leaked to settings: %v", saved)
	}
}

// persist=false must not touch settings even when it clears the pick — the
// empty-fallback clear and RBAC trim are in-memory view filters.
func TestCommitPickMutation_ClearWithoutPersistKeepsSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	req := reqAs("")

	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"test-ctx": {"default"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}
	s.setActiveNamespaceForUser(req, []string{"default"})

	s.commitPickMutation(req, "test-ctx", []string{"default"}, nil, false)

	if picks := s.getActiveNamespaceForUser(req); len(picks) != 0 {
		t.Errorf("pick not cleared in memory: %v", picks)
	}
	if saved := settings.Load().ActiveNamespaces["test-ctx"]; !slices.Equal(saved, []string{"default"}) {
		t.Errorf("in-memory clear leaked to settings: %v, want [default] preserved", saved)
	}
}

// A context switch between the caller's snapshot and the helper's write must
// skip the whole mutation — the helper keys off the passed snapshot ctxName,
// never the live context. Regression guard for a helper that re-derived the
// key from k8s.GetContextName() at write time: it would compare against one
// context's pick and store under another's.
func TestCommitPickMutation_SkipsAcrossContextSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t) // live context = test-ctx
	req := reqAs("")

	snapshot := []string{"default"}
	s.setActiveNamespaceForUser(req, snapshot) // pick under test-ctx

	// Switch context and preseed the SAME-valued pick under the new context.
	// A helper that keyed off the live context would find expected==current
	// here and wrongly clear it; the snapshot-pinned helper must not.
	prev := k8s.SetTestContextName("other-ctx")
	s.setActiveNamespaceForUser(req, []string{"default"}) // pick under other-ctx

	// Caller snapshotted test-ctx; the switch to other-ctx already happened.
	s.commitPickMutation(req, "test-ctx", snapshot, nil, false)

	// other-ctx pick untouched — the context guard skipped the whole mutation.
	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, []string{"default"}) {
		t.Errorf("other-ctx pick mutated under the wrong key: %v, want [default]", picks)
	}
	k8s.SetTestContextName(prev)
	// test-ctx pick also untouched.
	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, snapshot) {
		t.Errorf("test-ctx pick mutated after context switch: %v, want %v", picks, snapshot)
	}
}

// The first-request seed reads settings then stores. If a POST installs a
// fresh pick between the two, the seed must not overwrite it with the disk value.
func TestLoadSavedNamespacePreference_SeedDoesNotClobberFreshPick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	req := reqAs("")

	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"test-ctx": {"default"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	// A POST already set a different pick in memory.
	s.setActiveNamespaceForUser(req, []string{"broken"})

	s.loadSavedNamespacePreference(req)

	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, []string{"broken"}) {
		t.Errorf("seed clobbered fresh pick with disk value: %v, want [broken]", picks)
	}
}

// A context switch between snapshot and write must also skip the mutation —
// old-context survivors must not persist under the new context's key.
func TestPruneDeletedNamespacePicks_SkipsAcrossContextSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	req := reqAs("")

	snapshot := []string{"default", "ghost"}
	s.setActiveNamespaceForUser(req, snapshot)

	// The pick was snapshotted under "test-ctx"; the context flips to
	// "other-ctx" before the prune runs. Preseed other-ctx with the SAME value,
	// so a prune that keyed off the live context would find its stored pick
	// equal to the snapshot and wrongly trim it. Prune carries the snapshot
	// ctx, so the commit guard (live context != snapshot ctx) skips the write.
	prev := k8s.SetTestContextName("other-ctx")
	s.setActiveNamespaceForUser(req, snapshot) // other-ctx pick == snapshot
	survivorsUnderOther := s.pruneDeletedNamespacePicks(req, "test-ctx", snapshot)
	_ = survivorsUnderOther

	// other-ctx's coincidentally-matching pick must NOT be trimmed to survivors.
	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, snapshot) {
		t.Errorf("new-context pick trimmed by a stale-snapshot prune: %v, want %v", picks, snapshot)
	}
	if saved := settings.Load().ActiveNamespaces["other-ctx"]; len(saved) != 0 {
		t.Errorf("survivors persisted under the new context's key: %v", saved)
	}
	k8s.SetTestContextName(prev)

	// The original context's pick is also untouched.
	if picks := s.getActiveNamespaceForUser(req); !slices.Equal(picks, snapshot) {
		t.Errorf("old-context pick mutated across switch: %v, want %v", picks, snapshot)
	}
}
