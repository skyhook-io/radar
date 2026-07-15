package k8s

import (
	"testing"
	"time"
)

// snapshotCacheGlobals isolates a test from (and restores) the package-level
// cache singleton state the generation guards operate on.
func snapshotCacheGlobals(t *testing.T) {
	t.Helper()
	cacheMu.Lock()
	prevRC, prevSC, prevGen := resourceCache, syncingCache, cacheGeneration
	resourceCache, syncingCache = nil, nil
	cacheMu.Unlock()
	t.Cleanup(func() {
		cacheMu.Lock()
		resourceCache, syncingCache, cacheGeneration = prevRC, prevSC, prevGen
		cacheMu.Unlock()
	})
}

func currentGen() uint64 {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return cacheGeneration
}

func bumpGen() {
	cacheMu.Lock()
	cacheGeneration++
	cacheMu.Unlock()
}

func TestPublishSyncingCache_GenerationGuard(t *testing.T) {
	snapshotCacheGlobals(t)
	w := &ResourceCache{}

	gen := currentGen()
	publishSyncingCache(w, gen)
	if GetSyncingResourceCache() != w {
		t.Fatal("publish with current generation did not install the handle")
	}

	// A context switch bumps the generation; a stale construction's publish
	// must be a no-op — otherwise the old cluster's cache resurfaces.
	bumpGen()
	cacheMu.Lock()
	syncingCache = nil
	cacheMu.Unlock()
	publishSyncingCache(w, gen)
	if GetSyncingResourceCache() != nil {
		t.Fatal("stale-generation publish installed an old cluster's handle")
	}
}

func TestPromoteCache_GenerationGuard(t *testing.T) {
	snapshotCacheGlobals(t)
	w := &ResourceCache{}

	gen := currentGen()
	publishSyncingCache(w, gen)
	if !promoteCache(w, gen) {
		t.Fatal("promotion with current generation failed")
	}
	if GetResourceCache() != w {
		t.Fatal("promotion did not install the singleton")
	}
	if GetSyncingResourceCache() != nil {
		t.Fatal("promotion did not retire the mid-sync handle")
	}

	// Stale promotion after a switch must refuse (caller stops the core).
	cacheMu.Lock()
	resourceCache = nil
	cacheMu.Unlock()
	bumpGen()
	if promoteCache(w, gen) {
		t.Fatal("stale-generation promotion succeeded")
	}
	if GetResourceCache() != nil {
		t.Fatal("stale promotion installed the singleton")
	}
}

func TestClearSyncingCacheForGen_OnlyOwnGeneration(t *testing.T) {
	snapshotCacheGlobals(t)
	old := &ResourceCache{}
	oldGen := currentGen()

	// A newer construction published its handle; the old generation's
	// failure cleanup must not clear it.
	bumpGen()
	fresh := &ResourceCache{}
	publishSyncingCache(fresh, currentGen())

	clearSyncingCacheForGen(oldGen)
	if GetSyncingResourceCache() != fresh {
		t.Fatal("stale-generation clear removed the newer construction's handle")
	}
	_ = old
}

func TestResetResourceCache_InvalidatesInFlightConstruction(t *testing.T) {
	snapshotCacheGlobals(t)
	w := &ResourceCache{}
	gen := currentGen()
	publishSyncingCache(w, gen)

	ResetResourceCache()

	if GetSyncingResourceCache() != nil {
		t.Fatal("reset left the mid-sync handle published")
	}
	if promoteCache(w, gen) {
		t.Fatal("reset did not invalidate the in-flight construction's generation")
	}
}

func TestParseDebugSyncDelays(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]time.Duration
	}{
		{"", nil},
		{"60s", map[string]time.Duration{"pods": 60 * time.Second}},
		{"pods=90s,deployments=10s", map[string]time.Duration{"pods": 90 * time.Second, "deployments": 10 * time.Second}},
		{"bogus", nil},
		{"pods=bogus,nodes=5s", map[string]time.Duration{"nodes": 5 * time.Second}},
	}
	for _, c := range cases {
		got := parseDebugSyncDelays(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseDebugSyncDelays(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("parseDebugSyncDelays(%q)[%s] = %v, want %v", c.in, k, got[k], v)
			}
		}
	}
}
