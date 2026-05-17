package k8s

import (
	"sync"
	"testing"

	"github.com/skyhook-io/radar/pkg/policyreports"
)

// TestGetKyvernoStatus_LifecycleTransitions pins the four states callers
// can observe via the public GetKyvernoStatus accessor. The status backs
// the meta.kyverno field on /api/issues + the MCP issues tool, so a regression
// here flips the SPA/agent's behavior between "no findings yet" copy and
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

