package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func newTestUpgradeScanMemo(start time.Time) (*upgradeScanMemo, *time.Time) {
	current := start
	memo := newUpgradeScanMemo()
	memo.now = func() time.Time { return current }
	return memo, &current
}

func countingScan(count *int) func(context.Context) (*upgradereadiness.ScanResults, error) {
	return func(context.Context) (*upgradereadiness.ScanResults, error) {
		*count++
		return &upgradereadiness.ScanResults{TargetVersion: "1.34"}, nil
	}
}

func TestUpgradeScanMemoServesCachedWithinTTL(t *testing.T) {
	memo, clock := newTestUpgradeScanMemo(time.Now())
	scans := 0
	first, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || first.FromCache {
		t.Fatalf("first get = fromCache=%v err=%v, want fresh scan", first.FromCache, err)
	}
	*clock = clock.Add(30 * time.Second)
	second, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || !second.FromCache || scans != 1 {
		t.Fatalf("second get = fromCache=%v scans=%d err=%v, want cached single scan", second.FromCache, scans, err)
	}
	if second.ScanID != first.ScanID || !second.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("cached outcome = (%s, %s), want original (%s, %s) — a cached response must keep the original scan stamp", second.ScanID, second.ObservedAt, first.ScanID, first.ObservedAt)
	}
}

func TestUpgradeScanMemoExpiresAfterTTL(t *testing.T) {
	memo, clock := newTestUpgradeScanMemo(time.Now())
	scans := 0
	first, _ := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	*clock = clock.Add(memo.ttl + time.Second)
	second, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || second.FromCache || scans != 2 {
		t.Fatalf("post-TTL get = fromCache=%v scans=%d err=%v, want fresh rescan", second.FromCache, scans, err)
	}
	if second.ScanID == first.ScanID {
		t.Fatal("post-TTL rescan kept the previous scanId — pages could silently mix two scans")
	}
}

func TestUpgradeScanMemoRefreshReplacesEntryPastCooldown(t *testing.T) {
	memo, clock := newTestUpgradeScanMemo(time.Now())
	scans := 0
	first, _ := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	*clock = clock.Add(memo.cooldown + time.Second)
	second, err := memo.get(context.Background(), "k", true, memo.currentGeneration(), countingScan(&scans))
	if err != nil || second.FromCache || scans != 2 || second.ScanID == first.ScanID {
		t.Fatalf("refresh get = fromCache=%v scans=%d err=%v, want fresh replacement scan", second.FromCache, scans, err)
	}
	third, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || !third.FromCache || third.ScanID != second.ScanID {
		t.Fatalf("post-refresh get = fromCache=%v scanID=%s, want refreshed entry served", third.FromCache, third.ScanID)
	}
}

func TestUpgradeScanMemoRefreshCooldownServesNewestScan(t *testing.T) {
	memo, clock := newTestUpgradeScanMemo(time.Now())
	scans := 0
	first, _ := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	*clock = clock.Add(memo.cooldown / 2)
	second, err := memo.get(context.Background(), "k", true, memo.currentGeneration(), countingScan(&scans))
	if err != nil || !second.FromCache || scans != 1 || second.ScanID != first.ScanID {
		t.Fatalf("refresh within cooldown = fromCache=%v scans=%d, want newest completed scan without a rescan", second.FromCache, scans)
	}
}

func TestUpgradeScanMemoSingleFlightCoalesces(t *testing.T) {
	for _, refreshJoiner := range []bool{false, true} {
		memo, _ := newTestUpgradeScanMemo(time.Now())
		started := make(chan struct{})
		release := make(chan struct{})
		scans := 0
		blockedScan := func(context.Context) (*upgradereadiness.ScanResults, error) {
			scans++
			close(started)
			<-release
			return &upgradereadiness.ScanResults{}, nil
		}

		var wg sync.WaitGroup
		var leaderOut, joinerOut UpgradeScanOutcome
		wg.Add(1)
		go func() {
			defer wg.Done()
			leaderOut, _ = memo.get(context.Background(), "k", false, memo.currentGeneration(), blockedScan)
		}()
		<-started
		wg.Add(1)
		go func() {
			defer wg.Done()
			joinerOut, _ = memo.get(context.Background(), "k", refreshJoiner, memo.currentGeneration(), countingScan(&scans))
		}()
		close(release)
		wg.Wait()
		if scans != 1 {
			t.Fatalf("refreshJoiner=%v: %d scans ran, want the joiner to coalesce into the in-flight scan", refreshJoiner, scans)
		}
		if joinerOut.ScanID != leaderOut.ScanID {
			t.Fatalf("refreshJoiner=%v: joiner got scan %s, leader %s — want one shared snapshot", refreshJoiner, joinerOut.ScanID, leaderOut.ScanID)
		}
	}
}

func TestUpgradeScanMemoFailedScanNotCached(t *testing.T) {
	memo, _ := newTestUpgradeScanMemo(time.Now())
	scanErr := errors.New("collection failed")
	if _, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), func(context.Context) (*upgradereadiness.ScanResults, error) {
		return nil, scanErr
	}); !errors.Is(err, scanErr) {
		t.Fatalf("failed scan error = %v, want %v", err, scanErr)
	}
	scans := 0
	out, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || out.FromCache || scans != 1 {
		t.Fatalf("get after failure = fromCache=%v scans=%d err=%v, want a fresh scan (failures are not cached)", out.FromCache, scans, err)
	}
}

func TestUpgradeScanMemoWaiterGetsLeaderError(t *testing.T) {
	memo, _ := newTestUpgradeScanMemo(time.Now())
	scanErr := errors.New("leader canceled")
	entry := &upgradeScanEntry{done: make(chan struct{}), generation: memo.generation}
	memo.entries["k"] = entry
	go func() {
		// Complete the in-flight scan exactly as an errored leader does.
		memo.mu.Lock()
		entry.err = scanErr
		delete(memo.entries, "k")
		close(entry.done)
		memo.mu.Unlock()
	}()
	if _, err := memo.waitForEntry(context.Background(), entry); !errors.Is(err, scanErr) {
		t.Fatalf("waiter error = %v, want the leader's error (no poisoned entry, no silent success)", err)
	}
}

func TestUpgradeScanMemoWaiterHonorsContextCancellation(t *testing.T) {
	memo, _ := newTestUpgradeScanMemo(time.Now())
	entry := &upgradeScanEntry{done: make(chan struct{}), generation: memo.generation}
	memo.entries["k"] = entry
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := memo.waitForEntry(ctx, entry); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
	}
}

func TestUpgradeScanMemoContextSwitchDuringScanReturnsNoPayload(t *testing.T) {
	memo, _ := newTestUpgradeScanMemo(time.Now())
	out, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), func(context.Context) (*upgradereadiness.ScanResults, error) {
		memo.invalidate()
		return &upgradereadiness.ScanResults{}, nil
	})
	if !errors.Is(err, ErrUpgradeScanStaleContext) || out.Results != nil {
		t.Fatalf("switch-during-scan = results=%v err=%v, want stale-context error with no payload", out.Results, err)
	}
	scans := 0
	if next, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans)); err != nil || next.FromCache {
		t.Fatalf("get after stale scan = fromCache=%v err=%v, want fresh scan (stale result must not be cached)", next.FromCache, err)
	}
}

func TestUpgradeScanMemoStaleGenerationEntryNotServed(t *testing.T) {
	// A hit racing the invalidation: the entry survives in the map but its
	// generation is behind. It must be rescanned, never served.
	memo, _ := newTestUpgradeScanMemo(time.Now())
	scans := 0
	if _, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans)); err != nil {
		t.Fatal(err)
	}
	memo.mu.Lock()
	memo.generation++ // bump without clearing entries, simulating the read-before-clear window
	memo.mu.Unlock()
	out, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || out.FromCache || scans != 2 {
		t.Fatalf("stale-generation hit = fromCache=%v scans=%d err=%v, want fresh rescan under the new generation", out.FromCache, scans, err)
	}
}

func TestUpgradeScanMemoContextSwitchDropsEntries(t *testing.T) {
	memo, _ := newTestUpgradeScanMemo(time.Now())
	scans := 0
	if _, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans)); err != nil {
		t.Fatal(err)
	}
	memo.invalidate()
	out, err := memo.get(context.Background(), "k", false, memo.currentGeneration(), countingScan(&scans))
	if err != nil || out.FromCache || scans != 2 {
		t.Fatalf("post-switch get = fromCache=%v scans=%d err=%v, want fresh scan of the new cluster", out.FromCache, scans, err)
	}
}

func TestUpgradeScanKeySeparatesIdentityTargetAndScope(t *testing.T) {
	base := upgradeScanKey("alice", nil, "1.34", nil)
	for name, other := range map[string]string{
		"different user":       upgradeScanKey("bob", nil, "1.34", nil),
		"different groups":     upgradeScanKey("alice", []string{"admins"}, "1.34", nil),
		"different target":     upgradeScanKey("alice", nil, "1.35", nil),
		"namespace ceiling":    upgradeScanKey("alice", nil, "1.34", []string{"team-a"}),
		"no-access vs cluster": upgradeScanKey("alice", nil, "1.34", []string{}),
	} {
		if other == base {
			t.Fatalf("%s produced the same memo key — identities would share scans", name)
		}
	}
	if upgradeScanKey("alice", nil, "1.34", []string{"b", "a"}) != upgradeScanKey("alice", nil, "1.34", []string{"a", "b"}) {
		t.Fatal("namespace order changed the memo key — the same ceiling would fork the memo")
	}
	if upgradeScanKey("alice", []string{"dev", "ops"}, "1.34", nil) != upgradeScanKey("alice", []string{"ops", "dev"}, "1.34", nil) {
		t.Fatal("group order changed the memo key — the same identity would fork the memo")
	}
}

func TestUpgradeScanMemoRefusesGenerationCapturedBeforeSwitch(t *testing.T) {
	// The caller captures the generation before resolving cluster-dependent
	// inputs; a switch completing after the capture must fail the request
	// rather than cache those inputs under the new cluster's generation.
	memo, _ := newTestUpgradeScanMemo(time.Now())
	gen := memo.currentGeneration()
	memo.invalidate()
	scans := 0
	out, err := memo.get(context.Background(), "k", false, gen, countingScan(&scans))
	if !errors.Is(err, ErrUpgradeScanStaleContext) || out.Results != nil || scans != 0 {
		t.Fatalf("pre-switch generation = results=%v scans=%d err=%v, want refused with no scan run", out.Results, scans, err)
	}
}
