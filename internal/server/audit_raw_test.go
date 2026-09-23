package server

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestAuditResourceExactGroup(t *testing.T) {
	t.Cleanup(func() { auditCache.mu.Lock(); auditCache.results = nil; auditCache.mu.Unlock() })
	findings := []bp.Finding{
		{Kind: "Job", Group: "batch", Namespace: "default", Name: "shared", CheckID: "core"},
		{Kind: "Job", Group: "batch.volcano.sh", Namespace: "default", Name: "shared", CheckID: "volcano"},
		{Kind: "IngressRoute", Group: "traefik.io", Namespace: "default", Name: "shared", CheckID: "modern"},
		{Kind: "IngressRoute", Group: "traefik.containo.us", Namespace: "default", Name: "shared", CheckID: "legacy"},
	}
	for _, tc := range []struct {
		kind, group, want string
		status            int
	}{
		{"Job", "", "core", 200}, {"Job", "batch.volcano.sh", "volcano", 200},
		{"IngressRoute", "traefik.io", "modern", 200}, {"IngressRoute", "", "", 400},
	} {
		t.Run(tc.kind+tc.group, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/audit/resource/"+tc.kind+"/default/shared?raw=true&namespaces=default&group="+tc.group, nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("kind", tc.kind)
			route.URLParams.Add("namespace", "default")
			route.URLParams.Add("name", "shared")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			auditCache.mu.Lock()
			auditCache.results = &bp.ScanResults{Findings: findings}
			auditCache.key = auditKey(k8s.GetResourceCache(), testServerSrv.parseNamespacesForUser(req), testServerSrv.auditOptions(req))
			auditCache.expiresAt = time.Now().Add(time.Minute)
			auditCache.mu.Unlock()
			rec := httptest.NewRecorder()
			testServerSrv.handleAuditResource(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 {
				var got []bp.Finding
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if len(got) != 1 || got[0].CheckID != tc.want {
					t.Fatalf("wrong findings: %+v", got)
				}
			}
		})
	}
}
