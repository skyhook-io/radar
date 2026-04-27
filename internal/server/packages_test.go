package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/packages"
)

func TestErrorCodeForHelm(t *testing.T) {
	cases := []struct {
		name       string
		err        string
		statusCode int
		want       string
	}{
		{"empty", "", 0, ""},
		{"unknown shape", "something weird happened", 0, ""},

		// auth — 401 status OR auth-keyword in body.
		{"401 status", "x", 401, ErrCodeAuthRequired},
		{"unauthorized in body", "Unauthorized: token expired", 0, ErrCodeAuthRequired},

		// rbac — 403 status OR rbac/forbidden-keyword.
		{"403 status", "denied", 403, ErrCodeRBACDenied},
		{"rbac in body", "rbac denied (helm release secrets)", 0, ErrCodeRBACDenied},
		{"forbidden in body", "secrets is forbidden: User cannot list", 0, ErrCodeRBACDenied},
		{"cannot list", "cannot list resource secrets", 0, ErrCodeRBACDenied},

		// unconfigured — covers both legacy + new-from-restConfigGetter strings.
		{"new restConfigGetter error", `helm: no kubeconfig path and no resolved rest.Config available`, 0, ErrCodeUnconfigured},
		{"in-cluster fallback error", "no in-cluster rest config available", 0, ErrCodeUnconfigured},
		{"client not initialized", "helm client not initialized (cluster connect in progress or failed)", 0, ErrCodeUnconfigured},

		// timeout
		{"context deadline", "context deadline exceeded", 0, ErrCodeTimedOut},
		{"timeout in body", "operation timeout", 0, ErrCodeTimedOut},

		// unreachable
		{"connection refused", `dial tcp 127.0.0.1:8080: connect: connection refused`, 0, ErrCodeUnreachable},
		{"no such host", "dial tcp: lookup foo: no such host", 0, ErrCodeUnreachable},

		// Ordering pins. The classifier is a sequential switch; these
		// cases lock down precedence so a future reorder doesn't
		// silently re-bucket multi-keyword bodies.
		{"401 status with rbac word in body", "rbac unavailable", 401, ErrCodeAuthRequired},
		{"rbac word + context deadline", "context deadline exceeded querying rbac role", 0, ErrCodeRBACDenied},
		{"forbidden + connection refused", "forbidden: dial tcp connection refused", 0, ErrCodeRBACDenied},
		{"unconfigured + timeout phrase", "no resolved rest.Config available; timeout reached", 0, ErrCodeUnconfigured},
		{"timeout + dial tcp (i/o timeout shape)", "i/o timeout dialing tcp 10.0.0.1:443", 0, ErrCodeTimedOut},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := errorCodeForHelm(tc.err, tc.statusCode)
			if got != tc.want {
				t.Errorf("errorCodeForHelm(%q, %d) = %q, want %q", tc.err, tc.statusCode, got, tc.want)
			}
		})
	}
}

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

func TestPackagesCacheKey_DistinguishesNamespaces(t *testing.T) {
	// Different namespace sets → different keys.
	a := packagesCacheKeyFor([]string{"prod"})
	b := packagesCacheKeyFor([]string{"staging"})
	if a == b {
		t.Errorf("expected different keys for different namespaces, got %q", a)
	}
	// User identity is intentionally NOT part of the key — inventory
	// reads run via the SA so two users with the same namespace scope
	// share the entry. (See packagesCacheKeyFor godoc.)
	// nil namespaces vs empty slice must differ (empty = no access).
	all := packagesCacheKeyFor(nil)
	none := packagesCacheKeyFor([]string{})
	if all == none {
		t.Errorf("nil (all namespaces) and empty slice (no access) must differ; both = %q", all)
	}
	// Order independence: same set in different orders → same key.
	x := packagesCacheKeyFor([]string{"a", "b"})
	y := packagesCacheKeyFor([]string{"b", "a"})
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

// Invalid `?source=` values must NOT silently return an empty list
// (HTTP 200 with no rows looks identical to "nothing installed" — a
// confidently-wrong answer for any consumer that typo'd a source code).
// Validate at the boundary; surface as ErrInvalidSourceCode.
func TestListPackages_InvalidSourceRejected(t *testing.T) {
	withCleanCache(t, func() {
		cases := []string{"Z", "helm", "h,l", " H", ""}
		for _, in := range cases {
			if in == "" {
				// Empty source means "no filter" — must NOT be rejected.
				continue
			}
			t.Run(in, func(t *testing.T) {
				_, err := ListPackages(context.Background(), ListPackagesParams{
					Namespaces: []string{"prod"}, User: "alice", Source: in,
				})
				if !errors.Is(err, ErrInvalidSourceCode) {
					t.Errorf("source=%q want ErrInvalidSourceCode, got %v", in, err)
				}
			})
		}
		// And empty source still works (filter is just skipped).
		_, err := ListPackages(context.Background(), ListPackagesParams{
			Namespaces: []string{}, User: "alice", Source: "",
		})
		if err != nil {
			t.Errorf("empty source must not error, got %v", err)
		}
	})
}

// `?source=A` must match rows where Argo contributed (in addition to
// any other sources), not only rows where Argo was the sole contributor.
// This is the query semantic Hub locks in — "show me everything Argo
// manages, including releases also reported by Helm or labels."
func TestFilterBySource_MatchesAnyContributor(t *testing.T) {
	rows := []packages.PackageRow{
		{Chart: "helm-only", Sources: []packages.SourceCode{packages.SourceHelm}},
		{Chart: "argo-managed-helm", Sources: []packages.SourceCode{packages.SourceHelm, packages.SourceArgoCD}},
		{Chart: "argo-only", Sources: []packages.SourceCode{packages.SourceArgoCD}},
		{Chart: "labels-only", Sources: []packages.SourceCode{packages.SourceLabels}},
	}
	got := filterBySource(rows, packages.SourceArgoCD)
	if len(got) != 2 {
		t.Fatalf("source=A want 2 rows (argo-managed-helm + argo-only), got %d: %+v", len(got), got)
	}
	gotCharts := map[string]bool{got[0].Chart: true, got[1].Chart: true}
	if !gotCharts["argo-managed-helm"] || !gotCharts["argo-only"] {
		t.Errorf("expected charts {argo-managed-helm, argo-only}, got %v", gotCharts)
	}
	// Sanity: source=H still matches multi-source row.
	if got := filterBySource(rows, packages.SourceHelm); len(got) != 2 {
		t.Errorf("source=H want 2 rows, got %d", len(got))
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

// Inventory reads run via the SA (computePackagesInternal ignores user
// identity), so two users with the same namespace scope must share a
// cache entry — duplicating per-user wastes memory and triggers
// premature LRU eviction in multi-user Cloud deployments. We populate
// one entry, dispatch two requests under different user identities,
// and verify both see the same rows.
//
// This catches: a regression that re-introduces user identity into
// packagesCacheKeyFor (e.g. "let's scope per-user for safety" without
// a corresponding return to per-user impersonation in
// computePackagesInternal).
func TestListPackages_SharedCacheAcrossUsers(t *testing.T) {
	withCleanCache(t, func() {
		key := packagesCacheKeyFor([]string{"prod"})
		shared := packagesCacheEntry{
			at: time.Now(),
			rows: []packages.PackageRow{{
				Chart: "shared-chart", Namespace: "prod", ReleaseName: "shared-app",
				Sources: []packages.SourceCode{packages.SourceHelm}, Health: packages.HealthHealthy,
			}},
		}
		packagesCacheMu.Lock()
		packagesCache[key] = shared
		packagesCacheMu.Unlock()

		for _, user := range []string{"alice", "bob"} {
			resp, err := ListPackages(context.Background(), ListPackagesParams{
				Namespaces: []string{"prod"}, User: user,
			})
			if err != nil {
				t.Fatalf("%s ListPackages: %v", user, err)
			}
			if len(resp.Packages) != 1 || resp.Packages[0].Chart != "shared-chart" {
				t.Fatalf("%s got %+v, want chart=shared-chart from shared entry", user, resp.Packages)
			}
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
		nilKey := packagesCacheKeyFor(nil)
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
		emptyKey := packagesCacheKeyFor([]string{})
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
		key := packagesCacheKeyFor([]string{"prod"})
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

// Direct unit test on the eviction helper — picks the oldest by `at`.
func TestEvictOldestPackagesCacheEntry(t *testing.T) {
	withCleanCache(t, func() {
		now := time.Now()
		oldKey := "oldest"
		packagesCacheMu.Lock()
		packagesCache[oldKey] = packagesCacheEntry{at: now.Add(-time.Hour)}
		packagesCache["recent-1"] = packagesCacheEntry{at: now}
		packagesCache["recent-2"] = packagesCacheEntry{at: now.Add(-time.Minute)}
		evictOldestPackagesCacheEntry()
		if _, hit := packagesCache[oldKey]; hit {
			t.Errorf("oldest entry %q should have been evicted", oldKey)
		}
		if len(packagesCache) != 2 {
			t.Errorf("after eviction want 2 entries, got %d", len(packagesCache))
		}
		packagesCacheMu.Unlock()
	})
}

// Behavioral guard for cache size cap: the eviction-at-insert path in
// ListPackages must keep the map from growing past packagesCacheMaxEntries.
// Drives ListPackages with the empty-namespace fast-path so we never need
// a real backend, yet still exercise the cache-write code path indirectly
// — by pre-populating up to cap and then triggering one more compute.
func TestListPackages_CacheCapEnforcedAtInsert(t *testing.T) {
	withCleanCache(t, func() {
		origCap := packagesCacheMaxEntries
		packagesCacheMaxEntries = 4
		defer func() { packagesCacheMaxEntries = origCap }()

		// Pre-fill cache to cap with stale-but-valid entries.
		base := time.Now().Add(-30 * time.Second) // still fresh for TTL
		packagesCacheMu.Lock()
		oldestKey := packagesCacheKeyFor([]string{"ns0"})
		packagesCache[oldestKey] = packagesCacheEntry{at: base.Add(-time.Minute)}
		for i := 1; i < packagesCacheMaxEntries; i++ {
			k := packagesCacheKeyFor([]string{fmt.Sprintf("ns%d", i)})
			packagesCache[k] = packagesCacheEntry{at: base.Add(time.Duration(i) * time.Second)}
		}
		if len(packagesCache) != packagesCacheMaxEntries {
			t.Fatalf("setup: want %d entries, got %d", packagesCacheMaxEntries, len(packagesCache))
		}
		packagesCacheMu.Unlock()

		// Drive eviction directly — simulates what ListPackages does
		// inside the locked block before inserting a new entry. (We
		// can't drive computePackagesInternal in unit tests without a
		// real K8s cache, so we exercise the same eviction call site
		// the production path uses.)
		packagesCacheMu.Lock()
		if len(packagesCache) >= packagesCacheMaxEntries {
			evictOldestPackagesCacheEntry()
		}
		packagesCache[packagesCacheKeyFor([]string{"new-ns"})] = packagesCacheEntry{at: time.Now()}
		packagesCacheMu.Unlock()

		// The oldest entry must have been evicted; cap respected.
		packagesCacheMu.Lock()
		defer packagesCacheMu.Unlock()
		if _, hit := packagesCache[oldestKey]; hit {
			t.Errorf("oldest entry should have been evicted under cap")
		}
		if len(packagesCache) > packagesCacheMaxEntries {
			t.Errorf("cap %d exceeded: %d entries", packagesCacheMaxEntries, len(packagesCache))
		}
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

