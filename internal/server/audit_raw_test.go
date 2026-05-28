package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/settings"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// TestQueryTrue pins the truthy forms queryTrue accepts. The load-bearing case
// is "true": Radar Cloud's Hub fleet fan-out requests /api/audit?raw=true to
// skip local audit settings (the Hub owns effective Checks config). A silent
// drift here would re-introduce the settings-inversion the cloud unwind fixed.
func TestQueryTrue(t *testing.T) {
	cases := map[string]bool{
		"true":  true,
		"True":  true,
		"1":     true,
		"t":     true,
		"yes":   true,
		"false": false,
		"0":     false,
		"":      false,
		"raw":   false,
	}
	for val, want := range cases {
		r := httptest.NewRequest("GET", "/api/audit?raw="+val, nil)
		if got := queryTrue(r, "raw"); got != want {
			t.Errorf("queryTrue(raw=%q) = %v, want %v", val, got, want)
		}
	}
	// Absent param reads false.
	r := httptest.NewRequest("GET", "/api/audit", nil)
	if queryTrue(r, "raw") {
		t.Error("queryTrue with absent param = true, want false")
	}
}

// withIgnoredDefaultNS points local audit settings at a temp HOME with the
// "default" namespace ignored, and clears the short-TTL audit cache so the next
// scan is recomputed. The shared smoke-test cache (TestMain) has fixtures in
// "default", so ignoring it gives a clean raw-vs-filtered contrast.
func withIgnoredDefaultNS(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := settings.Update(func(s *settings.Settings) {
		s.Audit = &settings.AuditConfig{IgnoredNamespaces: []string{"default"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}
	auditCache.mu.Lock()
	auditCache.results = nil
	auditCache.mu.Unlock()
}

func nsFindingCount(sr bp.ScanResults, ns string) int {
	n := 0
	for _, f := range sr.Findings {
		if f.Namespace == ns {
			n++
		}
	}
	return n
}

// /api/audit?raw=true must skip local ~/.radar settings (the Hub owns effective
// policy); the default request must still apply them.
func TestHandleAudit_RawSkipsLocalSettings(t *testing.T) {
	withIgnoredDefaultNS(t)

	get := func(url string) bp.ScanResults {
		t.Helper()
		rec := httptest.NewRecorder()
		testServerSrv.handleAudit(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", url, rec.Code)
		}
		var sr bp.ScanResults
		if err := json.Unmarshal(rec.Body.Bytes(), &sr); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return sr
	}

	raw := get("/api/audit?raw=true")
	if nsFindingCount(raw, "default") == 0 {
		t.Fatal("raw scan should include default-namespace findings (fixtures live there)")
	}
	filtered := get("/api/audit")
	if n := nsFindingCount(filtered, "default"); n != 0 {
		t.Errorf("default request must drop ignored 'default' namespace findings, got %d", n)
	}
}

// handleAuditResource mirrors handleAudit's `?raw` gate verbatim (the same
// `if !queryTrue(r, "raw") { applyAuditSettings }`), covered by the handleAudit
// test above + TestQueryTrue. A dedicated handler test for it would need a
// core-group (group="") resource that carries findings in an ignored namespace;
// the shared fixtures only have group-qualified workloads (Deployments →
// group "apps"), which the per-resource handler's group="" lookup can't resolve
// — a separate, pre-existing limitation, not the raw gate.
