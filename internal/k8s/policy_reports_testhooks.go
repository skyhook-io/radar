package k8s

import (
	"fmt"

	"github.com/skyhook-io/radar/pkg/policyreports"
)

// Test hooks for cross-package tests that need to inject lifecycle state
// into the PolicyReport index without running real warmup (which requires
// discovery + dynamic cache singletons). These are deliberately exported
// (capitalized "ForTest" suffix) so they can be called from
// internal/server's _test.go files.
//
// Naming: "ForTest" suffix is the convention used elsewhere in this
// codebase (e.g. timeline.ResetStore, k8s.InitTestResourceCache); it
// keeps them grep-able and unambiguously not part of the runtime surface.
//
// We don't gate these behind a `testing` build tag because Go doesn't
// support such a tag and the alternative (e.g. //go:build !prod) is
// noisy. The functions cost nothing at runtime (no init wiring) and
// callers don't accidentally invoke them — the names make the intent
// obvious.

// LoadKyvernoDecisionForTest reads the current warmup decision atomic.
// Empty string means "no decision recorded yet" (the implicit warmup
// state).
func LoadKyvernoDecisionForTest() KyvernoStatus {
	v, _ := kyvernoWarmupDecision.Load().(KyvernoStatus)
	return v
}

// StoreKyvernoDecisionForTest sets the warmup decision atomic. Use
// KyvernoStatus("") to clear it (re-arms the implicit "warmup" state).
func StoreKyvernoDecisionForTest(s KyvernoStatus) {
	kyvernoWarmupDecision.Store(s)
}

// LoadKyvernoIndexForTest returns the current PolicyReport index pointer
// (typed as `any` so callers don't need to import pkg/policyreports just
// to round-trip the value through cleanup).
func LoadKyvernoIndexForTest() any {
	idx := policyReportIndex.Load()
	if idx == nil {
		// Return untyped nil so the caller's `if idx == nil` works
		// without unwrapping a typed-nil interface.
		return nil
	}
	return idx
}

// StoreKyvernoIndexForTest sets the PolicyReport index pointer. Pass nil
// to clear (e.g. for not_installed / deferred / warmup states); pass the
// result of NewEmptyKyvernoIndexForTest for the ready state.
func StoreKyvernoIndexForTest(v any) {
	if v == nil {
		policyReportIndex.Store(nil)
		return
	}
	idx, ok := v.(*policyreports.Index)
	if !ok {
		// Test-only hook: a wrong type here is a test bug, not a runtime
		// condition to handle gracefully. Panic immediately so the test
		// fails at the misuse site instead of producing confusing
		// downstream failures.
		panic(fmt.Sprintf("StoreKyvernoIndexForTest: want *policyreports.Index, got %T", v))
	}
	policyReportIndex.Store(idx)
}

// NewEmptyKyvernoIndexForTest returns a fresh empty index instance.
// Useful for simulating the "ready but no findings yet" state when
// testing handler behavior — the index exists, but All() returns nil.
func NewEmptyKyvernoIndexForTest() any {
	return policyreports.NewIndex()
}
