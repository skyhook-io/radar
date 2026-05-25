package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// TestIssuesHandler_KyvernoMetaEmittedOnOptIn pins the meta.kyverno field
// emission on /api/issues when source=kyverno (or include_kyverno=true)
// is requested. Without this, a Kyverno-aware caller can't distinguish
// "Kyverno not installed" from "warmup deferred" from "ready but no
// violations" — they all look like an empty issues list.
//
// We exercise three of the four states by manipulating the package-level
// warmup-decision atomic directly. The fourth state ("warmup") is the
// implicit default before any decision is recorded; we cover the
// transitions in the policy_reports_test.go state-machine test.
func TestIssuesHandler_KyvernoMetaEmittedOnOptIn(t *testing.T) {
	// Snapshot + restore the kyverno globals so we don't bleed into
	// other server tests running against the same testServer singleton.
	origDecision := loadKyvernoDecisionForTest()
	origIdx := loadKyvernoIndexForTest()
	t.Cleanup(func() {
		storeKyvernoDecisionForTest(k8s.KyvernoStatus(origDecision))
		storeKyvernoIndexForTest(origIdx)
	})

	cases := []struct {
		name       string
		setup      func()
		wantMeta   string
		queryParam string // "include_kyverno=true" or "source=kyverno"
	}{
		{
			name: "not_installed surfaces in meta.kyverno",
			setup: func() {
				storeKyvernoIndexForTest(nil)
				storeKyvernoDecisionForTest(k8s.KyvernoStatusNotInstalled)
			},
			wantMeta:   "not_installed",
			queryParam: "include_kyverno=true",
		},
		{
			name: "deferred surfaces in meta.kyverno",
			setup: func() {
				storeKyvernoIndexForTest(nil)
				storeKyvernoDecisionForTest(k8s.KyvernoStatusDeferred)
			},
			wantMeta:   "deferred",
			queryParam: "source=kyverno",
		},
		{
			name: "ready surfaces in meta.kyverno (empty findings list is meaningful)",
			setup: func() {
				// Real index instance, no findings populated → ready but empty.
				storeKyvernoIndexForTest(newEmptyIndexForTest())
				storeKyvernoDecisionForTest(k8s.KyvernoStatusReady)
			},
			wantMeta:   "ready",
			queryParam: "include_kyverno=true",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()

			resp, err := http.Get(testServer.URL + "/api/issues?" + tc.queryParam)
			if err != nil {
				t.Fatalf("GET /api/issues: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d want 200", resp.StatusCode)
			}

			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}

			meta, ok := body["meta"].(map[string]any)
			if !ok {
				t.Fatalf("response missing meta object: %+v", body)
			}
			gotKyv, _ := meta["kyverno"].(string)
			if gotKyv != tc.wantMeta {
				t.Errorf("meta.kyverno: got %q want %q (full body: %+v)", gotKyv, tc.wantMeta, body)
			}
		})
	}
}

// TestIssuesHandler_KyvernoMetaOmittedWhenNotRequested pins the inverse:
// when the caller did NOT ask for Kyverno (no source=kyverno, no
// include_kyverno), we don't emit meta.kyverno. This keeps default
// responses lean — agents not aware of Kyverno don't get a noisy field,
// and the SPA's default issue view stays clean.
func TestIssuesHandler_KyvernoMetaOmittedWhenNotRequested(t *testing.T) {
	origDecision := loadKyvernoDecisionForTest()
	origIdx := loadKyvernoIndexForTest()
	t.Cleanup(func() {
		storeKyvernoDecisionForTest(k8s.KyvernoStatus(origDecision))
		storeKyvernoIndexForTest(origIdx)
	})
	// Even if Kyverno is "ready", omitted from response when caller
	// didn't request it.
	storeKyvernoIndexForTest(newEmptyIndexForTest())
	storeKyvernoDecisionForTest(k8s.KyvernoStatusReady)

	resp, err := http.Get(testServer.URL + "/api/issues")
	if err != nil {
		t.Fatalf("GET /api/issues: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["meta"]; ok {
		t.Errorf("default request should not emit meta.kyverno; got %+v", body["meta"])
	}
}

func TestIssuesHandlerRejectsAuditSource(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/issues?source=audit")
	if err != nil {
		t.Fatalf("GET /api/issues: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "use GET /api/audit") {
		t.Fatalf("error did not point caller to /api/audit: %q", string(body))
	}
}

// --- Test helpers: cross-package access to k8s package state via exported
// hooks. We use go:linkname-style accessors registered as test-only helpers
// in the k8s package; here we route through the public API where possible
// and otherwise via the package's own globals through a small bridge.
//
// We can't `go:linkname` into internal/k8s from another package easily
// without a forward declaration, so we route through small exported
// helpers in the k8s package's _test.go side. Since k8s already exposes
// ResetPolicyReportIndex publicly, we reuse that for resetting; for
// arbitrary state injection (needed here) we add bridge funcs in the
// k8s package guarded by a build tag-free in-package test file (see
// policy_reports_test_export_test.go-equivalent below).

// loadKyvernoDecisionForTest / store / loadIndex / store / newEmptyIndex
// are thin wrappers around exported test hooks in internal/k8s.

func loadKyvernoDecisionForTest() string {
	return string(k8s.LoadKyvernoDecisionForTest())
}
func storeKyvernoDecisionForTest(s k8s.KyvernoStatus) {
	k8s.StoreKyvernoDecisionForTest(s)
}
func loadKyvernoIndexForTest() any {
	return k8s.LoadKyvernoIndexForTest()
}
func storeKyvernoIndexForTest(v any) {
	k8s.StoreKyvernoIndexForTest(v)
}
func newEmptyIndexForTest() any {
	return k8s.NewEmptyKyvernoIndexForTest()
}
