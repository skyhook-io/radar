package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/packages"
)

// Pins the error wording produced by k8s.ResourceCache.ListDynamicWithGroup
// when the requested CRD isn't installed. If that wording changes,
// graceful degradation breaks for clusters without ArgoCD/FluxCD —
// every Radar install would suddenly show error banners on /api/packages.
// See internal/k8s/cache.go ListDynamicWithGroup.
func TestIsMissingCRDErr_PinsK8scoreErrorString(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"with-group", fmt.Errorf("unknown resource kind: %s (group: %s)", "Application", "argoproj.io"), true},
		{"without-group", fmt.Errorf("unknown resource kind: %s", "Application"), true},
		{"case-insensitive", errors.New("UNKNOWN RESOURCE KIND: Application"), true},
		{"unrelated", errors.New("connection refused"), false},
		{"forbidden", errors.New("namespaces is forbidden"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMissingCRDErr(c.err); got != c.want {
				t.Errorf("isMissingCRDErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestPackagesCacheKey_DistinguishesUserAndNamespaces(t *testing.T) {
	// Same user, different namespace sets → different keys.
	a := packagesCacheKeyFor("alice", []string{"prod"})
	b := packagesCacheKeyFor("alice", []string{"staging"})
	if a == b {
		t.Errorf("expected different keys for different namespaces, got %q", a)
	}
	// Different users, same namespaces → different keys.
	c := packagesCacheKeyFor("bob", []string{"prod"})
	if a == c {
		t.Errorf("expected different keys for different users, got %q", a)
	}
	// nil namespaces vs empty slice should be different (empty = no access).
	all := packagesCacheKeyFor("alice", nil)
	none := packagesCacheKeyFor("alice", []string{})
	if all == none {
		t.Errorf("nil (all namespaces) and empty slice (no access) must differ; both = %q", all)
	}
	// Order independence: same set in different orders → same key.
	x := packagesCacheKeyFor("alice", []string{"a", "b"})
	y := packagesCacheKeyFor("alice", []string{"b", "a"})
	if x != y {
		t.Errorf("namespace order should not affect key: %q vs %q", x, y)
	}
}

func TestSourcesUsed_StableCanonicalOrder(t *testing.T) {
	rows := []packages.PackageRow{
		{Sources: []packages.SourceCode{packages.SourceFluxCD, packages.SourceArgoCD}},
		{Sources: []packages.SourceCode{packages.SourceCRDs}},
		{Sources: []packages.SourceCode{packages.SourceLabels, packages.SourceHelm}},
	}
	got := sourcesUsed(rows)
	want := []packages.SourceCode{packages.SourceHelm, packages.SourceLabels, packages.SourceCRDs, packages.SourceArgoCD, packages.SourceFluxCD}
	if !sourceCodesEqual(got, want) {
		t.Errorf("sourcesUsed = %v, want %v", got, want)
	}
}

func TestFilterByChartSubstring_CaseInsensitive(t *testing.T) {
	rows := []packages.PackageRow{
		{Chart: "cert-manager"},
		{Chart: "Karpenter"},
		{Chart: "external-dns"},
	}
	out := filterByChartSubstring(rows, "karpen")
	if len(out) != 1 || out[0].Chart != "Karpenter" {
		t.Errorf("expected Karpenter row only, got %+v", out)
	}
}

// Behavioral guard against the most security-sensitive regression in
// this module: two users hitting the same namespace must NOT see each
// other's Helm rows. We pre-populate the cache with two distinct
// (user, ns) entries and verify ListPackages returns each user's data
// from their respective entry.
//
// This catches: forgetting User in packagesCacheKeyFor; reordering the
// cache lookup before the user is wired in; or any refactor that drops
// user identity from the key path.
func TestListPackages_UserScopedCacheReturnsDistinctData(t *testing.T) {
	withCleanCache(t, func() {
		now := time.Now()
		aliceKey := packagesCacheKeyFor("alice", []string{"prod"})
		bobKey := packagesCacheKeyFor("bob", []string{"prod"})

		packagesCacheMu.Lock()
		packagesCache[aliceKey] = packagesCacheEntry{
			at: now,
			rows: []packages.PackageRow{{
				Chart: "alice-only-chart", Namespace: "prod", ReleaseName: "alice-app",
				Sources: []packages.SourceCode{packages.SourceHelm}, Health: packages.HealthHealthy,
			}},
		}
		packagesCache[bobKey] = packagesCacheEntry{
			at: now,
			rows: []packages.PackageRow{{
				Chart: "bob-only-chart", Namespace: "prod", ReleaseName: "bob-app",
				Sources: []packages.SourceCode{packages.SourceHelm}, Health: packages.HealthHealthy,
			}},
		}
		packagesCacheMu.Unlock()

		aliceResp, err := ListPackages(context.Background(), ListPackagesParams{
			Namespaces: []string{"prod"}, User: "alice",
		})
		if err != nil {
			t.Fatalf("alice ListPackages: %v", err)
		}
		if len(aliceResp.Packages) != 1 || aliceResp.Packages[0].Chart != "alice-only-chart" {
			t.Fatalf("alice got %+v, want chart=alice-only-chart", aliceResp.Packages)
		}

		bobResp, err := ListPackages(context.Background(), ListPackagesParams{
			Namespaces: []string{"prod"}, User: "bob",
		})
		if err != nil {
			t.Fatalf("bob ListPackages: %v", err)
		}
		if len(bobResp.Packages) != 1 || bobResp.Packages[0].Chart != "bob-only-chart" {
			t.Fatalf("bob got %+v, want chart=bob-only-chart", bobResp.Packages)
		}
	})
}

// Behavioral guard for the auth-restricted-to-zero-namespaces path:
// callers passing an empty (non-nil) namespace slice must get an empty
// response without consulting the cache OR the backend. A regression
// where empty slice gets confused with nil ("all namespaces") would
// leak every package in the cluster to a zero-access user.
func TestListPackages_EmptyNamespacesShortCircuits(t *testing.T) {
	withCleanCache(t, func() {
		// Pre-populate "all namespaces" cache to make sure the
		// short-circuit doesn't accidentally read it.
		nilKey := packagesCacheKeyFor("alice", nil)
		packagesCacheMu.Lock()
		packagesCache[nilKey] = packagesCacheEntry{
			at: time.Now(),
			rows: []packages.PackageRow{{
				Chart: "should-not-appear", Sources: []packages.SourceCode{packages.SourceHelm}, Health: packages.HealthHealthy,
			}},
		}
		packagesCacheMu.Unlock()

		resp, err := ListPackages(context.Background(), ListPackagesParams{
			Namespaces: []string{}, User: "alice",
		})
		if err != nil {
			t.Fatalf("ListPackages: %v", err)
		}
		if len(resp.Packages) != 0 {
			t.Errorf("want 0 packages, got %d: %+v", len(resp.Packages), resp.Packages)
		}
		if len(resp.SourcesUsed) != 0 {
			t.Errorf("want empty SourcesUsed, got %v", resp.SourcesUsed)
		}
		if len(resp.SourcesErrored) != 0 {
			t.Errorf("want empty SourcesErrored, got %v", resp.SourcesErrored)
		}
		// Verify the empty-slice path didn't write a cache entry.
		emptyKey := packagesCacheKeyFor("alice", []string{})
		packagesCacheMu.Lock()
		_, hit := packagesCache[emptyKey]
		packagesCacheMu.Unlock()
		if hit {
			t.Error("empty-namespace path should not write a cache entry")
		}
	})
}

// Behavioral guard for the cached-response timestamp: after a cache
// hit, GeneratedAt must reflect the cached entry's age, NOT time.Now().
// Otherwise the wire format lies about freshness — agents trust the
// timestamp and don't re-fetch even when data is up to TTL old.
func TestListPackages_CachedResponseUsesEntryTimestamp(t *testing.T) {
	withCleanCache(t, func() {
		entryAt := time.Now().Add(-30 * time.Second)
		key := packagesCacheKeyFor("alice", []string{"prod"})
		packagesCacheMu.Lock()
		packagesCache[key] = packagesCacheEntry{
			at:   entryAt,
			rows: []packages.PackageRow{},
		}
		packagesCacheMu.Unlock()

		resp, err := ListPackages(context.Background(), ListPackagesParams{
			Namespaces: []string{"prod"}, User: "alice",
		})
		if err != nil {
			t.Fatalf("ListPackages: %v", err)
		}
		if !resp.GeneratedAt.Equal(entryAt) {
			t.Errorf("GeneratedAt = %v, want cached entry time %v", resp.GeneratedAt, entryAt)
		}
	})
}

// Behavioral guard for cache size cap: once packagesCacheMaxEntries is
// reached, the next compute must evict the oldest entry rather than
// growing the map indefinitely.
func TestListPackages_CacheCapEvictsOldest(t *testing.T) {
	withCleanCache(t, func() {
		// Fill cache to cap with entries at staggered timestamps —
		// "old" must be evicted first.
		now := time.Now()
		oldKey := packagesCacheKeyFor("zero", []string{"old"})
		packagesCacheMu.Lock()
		packagesCache[oldKey] = packagesCacheEntry{at: now.Add(-time.Hour)}
		for i := 1; i < packagesCacheMaxEntries; i++ {
			k := packagesCacheKeyFor(fmt.Sprintf("u%d", i), []string{fmt.Sprintf("ns%d", i)})
			packagesCache[k] = packagesCacheEntry{at: now}
		}
		// Sanity: at cap.
		if len(packagesCache) != packagesCacheMaxEntries {
			t.Fatalf("setup: want %d entries, got %d", packagesCacheMaxEntries, len(packagesCache))
		}
		// Trigger eviction by manually calling — the eviction path runs
		// inside the locked block before insertion in ListPackages, but
		// we don't have a backend wired up so we test the helper directly.
		evictOldestPackagesCacheEntry()
		if _, hit := packagesCache[oldKey]; hit {
			t.Errorf("oldest entry %q should have been evicted", oldKey)
		}
		if len(packagesCache) != packagesCacheMaxEntries-1 {
			t.Errorf("after eviction want %d entries, got %d", packagesCacheMaxEntries-1, len(packagesCache))
		}
		packagesCacheMu.Unlock()
	})
}

// withCleanCache snapshots, clears, then restores the package-level
// cache so tests don't leak state into each other (or into other tests
// in the same package).
func withCleanCache(t *testing.T, fn func()) {
	t.Helper()
	packagesCacheMu.Lock()
	saved := packagesCache
	packagesCache = map[string]packagesCacheEntry{}
	packagesCacheMu.Unlock()
	defer func() {
		packagesCacheMu.Lock()
		packagesCache = saved
		packagesCacheMu.Unlock()
	}()
	fn()
}

func sourceCodesEqual(a, b []packages.SourceCode) bool {
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

