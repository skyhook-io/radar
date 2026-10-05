package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// fallbackFixture is a dynamic cache whose identity may not list CRDs
// cluster-wide, so it watches them namespace by namespace, starting with
// fallbacks. forbid says where Radar's identity may not list either; fail makes
// a namespace's Cluster list error without denying it; slow delays it by
// slowList.
type fallbackFixture struct {
	fallbacks          []string
	forbid, fail, slow func(ns string) bool
}

const slowList = 2 * time.Second

func inNamespaces(names ...string) func(string) bool {
	return func(ns string) bool { return slices.Contains(names, ns) }
}

// seedCNPGFallbackCache seeds Clusters (namespace, name pairs) into f.
func seedCNPGFallbackCache(t *testing.T, f fallbackFixture, clusters ...string) {
	t.Helper()
	release := make(chan struct{})
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range cnpgWorkspaceTestKinds {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	var objs []runtime.Object
	for i := 0; i+1 < len(clusters); i += 2 {
		objs = append(objs, cnpgObj("postgresql.cnpg.io/v1", "Cluster", clusters[i], clusters[i+1], nil, nil))
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
	dyn.PrependReactor("list", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		ns := a.GetNamespace()
		if ns == "" || (f.forbid != nil && f.forbid(ns)) {
			gr := a.GetResource().GroupResource()
			return true, nil, apierrors.NewForbidden(gr, "", errors.New("radar may not list here"))
		}
		if a.GetResource().Resource != "clusters" {
			return false, nil, nil
		}
		if f.fail != nil && f.fail(ns) {
			return true, nil, errors.New("temporary list failure")
		}
		if f.slow != nil && f.slow(ns) {
			select {
			case <-time.After(slowList):
			case <-release:
			}
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCacheWithFallbacks(dyn, cnpgWorkspaceTestKinds, f.fallbacks); err != nil {
		t.Fatalf("seed cnpg: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(func() { close(release) })
}

func sortedNames(objs []any) []string {
	names := objectNames(objs)
	sort.Strings(names)
	return names
}

// All namespaces: the Clusters Radar watches are listed and the rest reads as
// not read, rather than the kind syncing forever.
func TestCNPGWorkspace_FallbackCacheAllNamespaces(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}}, "a", "pg-a", "b", "pg-b")

	got := getWorkspaceNoAuth(t, "")
	cov := got.Coverage["clusters"]
	if cov.State != kindCoveragePartial {
		t.Fatalf("clusters coverage = %+v, want partial", cov)
	}
	if len(cov.UncachedNamespaces) != 0 {
		t.Errorf("uncached = %v: namespaces the caller did not name must not be disclosed", cov.UncachedNamespaces)
	}
	if names := sortedNames(got.Objects["clusters"]); len(names) != 1 || names[0] != "pg-a" {
		t.Errorf("clusters = %v, want [pg-a]", names)
	}
	if cov := got.Coverage["clusterImageCatalogs"]; cov.State != kindCoverageUncached {
		t.Errorf("clusterImageCatalogs coverage = %+v: a cluster-scoped kind Radar may not watch is uncached, not an error", cov)
	}
}

// A namespace the caller names is read on its own, which starts its watch even
// outside the startup fallbacks; one Radar may not watch is named as not cached.
func TestCNPGWorkspace_FallbackCacheNamedNamespaces(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}, forbid: inNamespaces("c")}, "a", "pg-a", "b", "pg-b", "c", "pg-c")

	b := getWorkspaceNoAuth(t, "?namespaces=b")
	if cov := b.Coverage["clusters"]; cov.State != kindCoverageFull {
		t.Errorf("namespaces=b: coverage = %+v, want full", cov)
	}
	if names := sortedNames(b.Objects["clusters"]); len(names) != 1 || names[0] != "pg-b" {
		t.Errorf("namespaces=b: clusters = %v, want [pg-b]", names)
	}

	ac := getWorkspaceNoAuth(t, "?namespaces=a,c")
	cov := ac.Coverage["clusters"]
	if cov.State != kindCoveragePartial || len(cov.UncachedNamespaces) != 1 || cov.UncachedNamespaces[0] != "c" {
		t.Errorf("namespaces=a,c: coverage = %+v, want partial with c not cached", cov)
	}
	if names := sortedNames(ac.Objects["clusters"]); len(names) != 1 || names[0] != "pg-a" {
		t.Errorf("namespaces=a,c: clusters = %v, want [pg-a]", names)
	}

	c := getWorkspaceNoAuth(t, "?namespaces=c")
	if cov := c.Coverage["clusters"]; cov.State != kindCoverageUncached || len(cov.UncachedNamespaces) != 1 || cov.UncachedNamespaces[0] != "c" {
		t.Errorf("namespaces=c: coverage = %+v, want uncached c", cov)
	}
}

// A namespace whose watch has not synced is unread while the others are
// listed, and is read once it syncs.
func TestCNPGWorkspace_FallbackCacheRetriesUnsyncedNamespace(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}, fail: func(ns string) bool { return ns == "x" && failing.Load() }}, "a", "pg-a", "x", "pg-x")

	first := getWorkspaceNoAuth(t, "?namespaces=a,x")
	cov := first.Coverage["clusters"]
	if cov.State != kindCoveragePartial || len(cov.UncachedNamespaces) != 1 || cov.UncachedNamespaces[0] != "x" {
		t.Fatalf("first read: coverage = %+v, want partial with x unread", cov)
	}
	if names := sortedNames(first.Objects["clusters"]); len(names) != 1 || names[0] != "pg-a" {
		t.Errorf("first read: clusters = %v, want [pg-a]", names)
	}

	failing.Store(false)
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := getWorkspaceNoAuth(t, "?namespaces=a,x")
		if got.Coverage["clusters"].State == kindCoverageFull {
			if names := sortedNames(got.Objects["clusters"]); len(names) != 2 || names[0] != "pg-a" || names[1] != "pg-x" {
				t.Errorf("after sync: clusters = %v, want [pg-a pg-x]", names)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("x never read after its list recovered: %+v", got.Coverage["clusters"])
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Radar's identity may list Clusters in neither the cluster nor its startup
// fallbacks, only in a namespace a caller named: the watch that read started
// still serves an all-namespaces read instead of the kind failing.
func TestCNPGWorkspace_FallbackCacheAllKeepsNamedWatches(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}, forbid: inNamespaces("a")}, "a", "pg-a", "b", "pg-b")

	if b := getWorkspaceNoAuth(t, "?namespaces=b"); b.Coverage["clusters"].State != kindCoverageFull {
		t.Fatalf("namespaces=b: coverage = %+v, want full", b.Coverage["clusters"])
	}
	all := getWorkspaceNoAuth(t, "")
	cov := all.Coverage["clusters"]
	if cov.State != kindCoveragePartial {
		t.Fatalf("all: coverage = %+v, want partial from the watch on b", cov)
	}
	if names := sortedNames(all.Objects["clusters"]); len(names) != 1 || names[0] != "pg-b" {
		t.Errorf("all: clusters = %v, want [pg-b]", names)
	}
}

// Waiting for unsynced watches shares one budget across the request, so
// several stalled namespaces do not add up their waits.
func TestCNPGWorkspace_FallbackCacheWaitsWithinOneBudget(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}, fail: inNamespaces("x", "y", "z")}, "a", "pg-a", "x", "pg-x", "y", "pg-y", "z", "pg-z")

	start := time.Now()
	got := getWorkspaceNoAuth(t, "?namespaces=a,x,y,z")
	if elapsed := time.Since(start); elapsed > dynamicSyncWait+2*time.Second {
		t.Errorf("request took %v; stalled namespaces must share one %v wait", elapsed, dynamicSyncWait)
	}
	cov := got.Coverage["clusters"]
	if cov.State != kindCoveragePartial || len(cov.UncachedNamespaces) != 3 {
		t.Errorf("coverage = %+v, want partial with x, y, z unread", cov)
	}
	if names := sortedNames(got.Objects["clusters"]); len(names) != 1 || names[0] != "pg-a" {
		t.Errorf("clusters = %v, want [pg-a]", names)
	}
}

// readCNPGClusters runs the workspace's kind reads as its handler does, under
// one budget, and returns the Clusters. The issues the handler composes
// afterwards read through the same watches and are not bounded by it.
func readCNPGClusters(namespaces []string) (KindCoverage, []*unstructured.Unstructured, time.Duration) {
	r := httptest.NewRequest(http.MethodGet, "/api/cnpg/workspace", nil)
	budget := newSyncBudget(r.Context())
	start := time.Now()
	var cov KindCoverage
	var clusters []*unstructured.Unstructured
	for _, k := range cnpgWorkspaceKinds {
		acc, list := testServerSrv.cnpgWorkspaceReadKind(r, k8s.GetResourceCache(), k, namespaces, budget)
		if k.key == cnpgWorkspaceClusterKey {
			cov, clusters = acc.coverage(), list
		}
	}
	return cov, clusters, time.Since(start)
}

// Starting a watch probes the apiserver, and slow probes spend the same budget
// as slow syncs. Watches the budget cut short carry on, and a later read finds
// them.
func TestCNPGWorkspace_FallbackCacheSlowWatchStartsWithinOneBudget(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}, slow: inNamespaces("x", "y", "z")}, "a", "pg-a", "x", "pg-x", "y", "pg-y", "z", "pg-z")
	namespaces := []string{"a", "x", "y", "z"}

	cov, clusters, elapsed := readCNPGClusters(namespaces)
	if elapsed > dynamicSyncWait+time.Second {
		t.Errorf("kind reads took %v; starting three slow watches must share one %v budget", elapsed, dynamicSyncWait)
	}
	if cov.State != kindCoveragePartial || len(cov.UncachedNamespaces) != 3 {
		t.Errorf("coverage = %+v, want partial with x, y, z unread", cov)
	}
	if len(clusters) != 1 || clusters[0].GetName() != "pg-a" {
		t.Errorf("clusters = %d objects, want only pg-a", len(clusters))
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		cov, clusters, _ := readCNPGClusters(namespaces)
		if cov.State == kindCoverageFull {
			if len(clusters) != 4 {
				t.Errorf("after the watches started: %d clusters, want 4", len(clusters))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watches never finished starting: %+v", cov)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// All namespaces over several stalled fallback watches waits once, then reads
// the watches that synced.
func TestCNPGWorkspace_FallbackCacheAllWaitsWithinOneBudget(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a", "x", "y", "z"}, fail: inNamespaces("x", "y", "z")}, "a", "pg-a", "x", "pg-x")

	start := time.Now()
	got := getWorkspaceNoAuth(t, "")
	if elapsed := time.Since(start); elapsed > dynamicSyncWait+time.Second {
		t.Errorf("request took %v; stalled fallback watches must share one %v wait", elapsed, dynamicSyncWait)
	}
	if cov := got.Coverage["clusters"]; cov.State != kindCoveragePartial || len(cov.UncachedNamespaces) != 0 {
		t.Errorf("coverage = %+v, want partial, naming nothing", cov)
	}
	if names := sortedNames(got.Objects["clusters"]); len(names) != 1 || names[0] != "pg-a" {
		t.Errorf("clusters = %v, want [pg-a]", names)
	}
}

// A cancelled request stops waiting even with budget left.
func TestSyncBudget_StopsWhenRequestEnds(t *testing.T) {
	seedCNPGFallbackCache(t, fallbackFixture{fallbacks: []string{"a"}, slow: inNamespaces("x")}, "x", "pg-x")
	gvr, ok := k8s.GetResourceDiscovery().GetGVRWithGroup("Cluster", "postgresql.cnpg.io")
	if !ok {
		t.Fatal("Cluster not discovered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	budget := &syncBudget{ctx: ctx, deadline: time.Now().Add(time.Minute)}
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, err := budget.listBlocking(k8s.GetDynamicResourceCache(), gvr, "x")
	if !errors.Is(err, errDynamicNotSynced) {
		t.Errorf("err = %v, want errDynamicNotSynced", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("listBlocking took %v after its request was cancelled at 200ms", elapsed)
	}
}
