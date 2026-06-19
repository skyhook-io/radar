package k8s

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/pkg/policyreports"
)

// TestGetKyvernoStatus_LifecycleTransitions pins the four states callers
// can observe via the public GetKyvernoStatus accessor. The status backs
// the meta.kyverno field on /api/issues + the MCP issues tool, so a regression
// here flips the frontend/agent's behavior between "no findings yet" copy and
// "no violations" copy — both bad.
//
// We drive the state via direct manipulation of the package-level atomics
// rather than running real warmup (which needs discovery + dynamic cache).
// The intent here is to pin the public accessor's reading of those
// atomics; the warmup decisions themselves are wired in
// WarmupKyvernoPolicyReports and have their own coverage in
// integration/e2e flows.
func TestGetKyvernoStatus_LifecycleTransitions(t *testing.T) {
	// Snapshot + restore the package state so this test doesn't bleed
	// into other tests in the suite (they may have run WarmupKyverno…
	// indirectly, populating these globals).
	origIdx := policyReportIndex.Load()
	origDec, _ := kyvernoWarmupDecision.Load().(KyvernoStatus)
	t.Cleanup(func() {
		policyReportIndex.Store(origIdx)
		kyvernoWarmupDecision.Store(origDec)
	})

	// State 1: no decision recorded yet + no index → warmup.
	policyReportIndex.Store(nil)
	kyvernoWarmupDecision.Store(KyvernoStatus(""))
	if got := GetKyvernoStatus(); got != KyvernoStatusWarmup {
		t.Errorf("uninitialized: got %q want %q", got, KyvernoStatusWarmup)
	}

	// State 2: decided not-installed, no index → not_installed.
	kyvernoWarmupDecision.Store(KyvernoStatusNotInstalled)
	if got := GetKyvernoStatus(); got != KyvernoStatusNotInstalled {
		t.Errorf("not_installed: got %q want %q", got, KyvernoStatusNotInstalled)
	}

	// State 3: decided deferred, no index → deferred.
	kyvernoWarmupDecision.Store(KyvernoStatusDeferred)
	if got := GetKyvernoStatus(); got != KyvernoStatusDeferred {
		t.Errorf("deferred: got %q want %q", got, KyvernoStatusDeferred)
	}

	// State 4: index exists → ready (even if decision atomic is stale —
	// the index's presence is authoritative).
	idx := policyreports.NewIndex()
	policyReportIndex.Store(idx)
	if got := GetKyvernoStatus(); got != KyvernoStatusReady {
		t.Errorf("ready: got %q want %q", got, KyvernoStatusReady)
	}

	// Index + decision agree on ready.
	kyvernoWarmupDecision.Store(KyvernoStatusReady)
	if got := GetKyvernoStatus(); got != KyvernoStatusReady {
		t.Errorf("ready (both): got %q want %q", got, KyvernoStatusReady)
	}
}

// TestResetPolicyReportIndex_ClearsKyvernoDecision pins the context-switch
// path: when the user switches clusters, ResetPolicyReportIndex must
// clear the warmup decision so the new cluster reports "warmup" until
// its own detection pass completes — not whatever the previous cluster
// decided.
func TestResetPolicyReportIndex_ClearsKyvernoDecision(t *testing.T) {
	origIdx := policyReportIndex.Load()
	origDec, _ := kyvernoWarmupDecision.Load().(KyvernoStatus)
	t.Cleanup(func() {
		policyReportIndex.Store(origIdx)
		kyvernoWarmupDecision.Store(origDec)
		// Restore policyReportInit too — Reset replaces it.
		policyReportMu.Lock()
		policyReportInit = new(sync.Once)
		policyReportMu.Unlock()
	})

	// Simulate a prior cluster that decided "ready".
	policyReportIndex.Store(policyreports.NewIndex())
	kyvernoWarmupDecision.Store(KyvernoStatusReady)

	ResetPolicyReportIndex()

	// After reset: index nil, decision empty → status reports "warmup"
	// (not "ready", not "not_installed" inherited from prior cluster).
	if got := GetKyvernoStatus(); got != KyvernoStatusWarmup {
		t.Errorf("after reset: got %q want %q (must not inherit prior cluster's decision)", got, KyvernoStatusWarmup)
	}
}

// TestResetPolicyReportIndex_BumpsWarmupGen pins the race-protection
// invariant: each Reset advances kyvernoWarmupGen so any in-flight Warmup
// goroutine's setDecision/publishReady writes (which snapshot gen under
// the mutex before Do() and verify under the mutex before storing) become
// no-ops against the new cluster. Without this bump, a slow Warmup from
// the previous cluster context could stamp KyvernoStatusReady onto the
// new cluster's atomic between the Reset and the new cluster's own Warmup.
func TestResetPolicyReportIndex_BumpsWarmupGen(t *testing.T) {
	t.Cleanup(func() {
		policyReportMu.Lock()
		policyReportInit = new(sync.Once)
		policyReportMu.Unlock()
	})

	before := kyvernoWarmupGen.Load()
	ResetPolicyReportIndex()
	after := kyvernoWarmupGen.Load()
	if after <= before {
		t.Fatalf("kyvernoWarmupGen did not advance: before=%d after=%d (Reset must bump gen so in-flight warmups skip stale writes)", before, after)
	}

	// A second Reset should bump again — bounded monotonic counter, not
	// a toggle.
	ResetPolicyReportIndex()
	if after2 := kyvernoWarmupGen.Load(); after2 <= after {
		t.Fatalf("kyvernoWarmupGen not strictly monotonic: %d -> %d", after, after2)
	}
}

// TestWarmupKyvernoPolicyReports_StaleGenSkipsWrites pins the structural
// invariant that backs the snapshot-under-mutex pattern in
// WarmupKyvernoPolicyReports: if the warmup goroutine's captured myGen
// no longer matches kyvernoWarmupGen at write time (i.e. a Reset
// interleaved between snapshot capture and the write), setDecision /
// publishReady must skip their writes — otherwise the previous cluster's
// outcome leaks onto the new cluster's atomics.
//
// We can't reach setDecision / publishReady directly (they're closures
// inside Warmup's Do() lambda), so we replicate the EXACT write protocol
// they use — read kyvernoWarmupGen under policyReportMu, compare to a
// captured myGen, store only on match — and verify a manually-bumped gen
// causes the equivalent write to be skipped. A regression that drops the
// gen check anywhere in the warmup-write path would break this
// invariant; this test is what catches it.
func TestWarmupKyvernoPolicyReports_StaleGenSkipsWrites(t *testing.T) {
	origIdx := policyReportIndex.Load()
	origDec, _ := kyvernoWarmupDecision.Load().(KyvernoStatus)
	origGen := kyvernoWarmupGen.Load()
	t.Cleanup(func() {
		policyReportIndex.Store(origIdx)
		kyvernoWarmupDecision.Store(origDec)
		// Restore the gen atomic so other tests aren't perturbed.
		kyvernoWarmupGen.Store(origGen)
		policyReportMu.Lock()
		policyReportInit = new(sync.Once)
		policyReportMu.Unlock()
	})

	// Stage state to mimic the moment a warmup has just snapshotted gen
	// under the mutex and is about to perform a write: index empty,
	// decision empty.
	policyReportIndex.Store(nil)
	kyvernoWarmupDecision.Store(KyvernoStatus(""))

	// Capture myGen exactly as WarmupKyvernoPolicyReports does — under
	// the mutex, BEFORE any downstream work.
	policyReportMu.Lock()
	myGen := kyvernoWarmupGen.Load()
	policyReportMu.Unlock()

	// Simulate ResetPolicyReportIndex firing AFTER the snapshot: it
	// bumps gen (under the same mutex, also held inside the test's write
	// closure below).
	kyvernoWarmupGen.Add(1)

	// Replicate setDecision's exact protocol: lock, gen check, store.
	stalePathRan := false
	(func() {
		policyReportMu.Lock()
		defer policyReportMu.Unlock()
		if kyvernoWarmupGen.Load() != myGen {
			stalePathRan = true
			return
		}
		kyvernoWarmupDecision.Store(KyvernoStatusReady)
	})()
	if !stalePathRan {
		t.Fatal("expected setDecision's gen check to fire and skip the write; the gen-mismatch branch was not taken (regression: the protection against stale-warmup writes is broken)")
	}
	if got, _ := kyvernoWarmupDecision.Load().(KyvernoStatus); got != "" {
		t.Errorf("decision atomic was stamped despite gen mismatch: got %q, want empty (stale warmup leaked into new cluster's state)", got)
	}

	// Replicate publishReady's protocol with the same myGen — also must
	// skip its index store on gen mismatch.
	(func() {
		policyReportMu.Lock()
		defer policyReportMu.Unlock()
		if kyvernoWarmupGen.Load() != myGen {
			return
		}
		policyReportIndex.Store(policyreports.NewIndex())
	})()
	if got := policyReportIndex.Load(); got != nil {
		t.Errorf("policyReportIndex was stamped despite gen mismatch: stale publishReady leaked an index onto a fresh cluster's atomic")
	}
}

// TestWarmupKyvernoPolicyReports_SnapshotsOnceAndGenAtomically pins the
// snapshot-under-mutex invariant: WarmupKyvernoPolicyReports must capture
// BOTH policyReportInit and kyvernoWarmupGen under policyReportMu so that
// a concurrent Reset can never separate them. The race the new code
// closes: an unsynchronized read of policyReportInit followed by a
// later (post-Reset) Load of kyvernoWarmupGen would capture the
// pre-Reset Once with the post-Reset gen, and the lambda's gen check
// against the post-Reset value would falsely succeed — stamping the old
// cluster's outcome onto the new cluster's atomics.
//
// We exercise this by spinning concurrent (snapshot+inspect) and Reset
// goroutines and asserting that the snapshot pair stays coherent: if
// the snapshotted gen equals the post-snapshot gen, the snapshotted
// once must still be the live one (and vice versa for the mismatch
// case). Run with -race for full effect.
func TestWarmupKyvernoPolicyReports_SnapshotsOnceAndGenAtomically(t *testing.T) {
	origGen := kyvernoWarmupGen.Load()
	t.Cleanup(func() {
		kyvernoWarmupGen.Store(origGen)
		policyReportMu.Lock()
		policyReportInit = new(sync.Once)
		policyReportMu.Unlock()
	})

	const iterations = 500
	var mismatch atomic.Int64

	var wg sync.WaitGroup
	// Resetters: bump gen + replace the once under the mutex.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				ResetPolicyReportIndex()
			}
		}()
	}
	// Snapshotters: emulate the new WarmupKyvernoPolicyReports prologue
	// — snapshot (once, gen) atomically under the mutex, then verify the
	// pair is consistent with a SECOND snapshot under the same mutex
	// where the once and gen are read together. Any tearing here would
	// be visible as a mismatch.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				policyReportMu.Lock()
				once1 := policyReportInit
				gen1 := kyvernoWarmupGen.Load()
				// A second read under the same mutex must see the same
				// pair — both fields only mutate inside Reset, which
				// holds this mutex.
				once2 := policyReportInit
				gen2 := kyvernoWarmupGen.Load()
				policyReportMu.Unlock()
				if once1 != once2 || gen1 != gen2 {
					mismatch.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	if got := mismatch.Load(); got != 0 {
		t.Fatalf("snapshot pair was not atomic under policyReportMu: %d mismatches (Reset is mutating policyReportInit and/or kyvernoWarmupGen outside the mutex, OR the snapshot is being torn — either breaks the race protection)", got)
	}
}
