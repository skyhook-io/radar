package k8s

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPublishSyncingCache_ClosedConnection(t *testing.T) {
	withFreshTestConnection(t)
	conn := Current()
	w := &ResourceCache{}
	state := conn.cacheState()
	state.publishSyncing(w)
	if GetSyncingResourceCache() != w {
		t.Fatal("publish on the open connection did not install the handle")
	}

	ResetResourceCache()
	ResetResourceDiscovery()
	state.publishSyncing(w)
	if GetSyncingResourceCache() != nil {
		t.Fatal("stale publish installed an old cluster's handle")
	}
}

func TestPromoteCache_ClosedConnection(t *testing.T) {
	withFreshTestConnection(t)
	conn := Current()
	w := &ResourceCache{}
	state := conn.cacheState()
	state.publishSyncing(w)
	if !state.promote(w, true) {
		t.Fatal("promotion on the open connection failed")
	}
	if GetResourceCache() != w {
		t.Fatal("promotion did not install the cache")
	}
	if GetSyncingResourceCache() != nil {
		t.Fatal("promotion did not retire the mid-sync handle")
	}

	ResetResourceCache()
	ResetResourceDiscovery()
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset()})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	if state.promote(&ResourceCache{ResourceCache: core}, true) {
		t.Fatal("stale promotion succeeded")
	}
	select {
	case <-core.Done():
	default:
		t.Fatal("stale promotion left its core running")
	}
	if GetResourceCache() != nil {
		t.Fatal("stale promotion installed the cache")
	}
}

func TestClearSyncingCache_OnlyOwnConnection(t *testing.T) {
	withFreshTestConnection(t)
	old := Current()
	ResetResourceCache()
	ResetResourceDiscovery()
	fresh := &ResourceCache{}
	Current().cacheState().publishSyncing(fresh)
	old.cacheState().clearSyncing()
	if GetSyncingResourceCache() != fresh {
		t.Fatal("stale clear removed the newer construction's handle")
	}
}

func TestResetResourceCache_InvalidatesInFlightConstruction(t *testing.T) {
	withFreshTestConnection(t)
	conn := Current()
	w := &ResourceCache{}
	state := conn.cacheState()
	state.publishSyncing(w)
	ResetResourceCache()
	if GetSyncingResourceCache() != nil {
		t.Fatal("reset left the mid-sync handle published")
	}
	if state.promote(w, true) {
		t.Fatal("reset did not invalidate the in-flight construction")
	}
}

func TestCacheInit_ResetInsideDoKeepsOldConnection(t *testing.T) {
	withFreshTestConnection(t)
	conn := Current()
	state := conn.cacheState()
	once := &state.once
	once.Do(func() {
		ResetResourceCache()
		ResetResourceDiscovery()
		if state.promote(&ResourceCache{}, true) {
			t.Fatal("construction adopted the post-reset connection and promoted")
		}
	})

	next := Current().cacheState()
	if &next.once == once || next == state {
		t.Fatal("replacement did not have a fresh Once and lifecycle")
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
