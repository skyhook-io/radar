package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/settings"
	bp "github.com/skyhook-io/radar/pkg/audit"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// Badge views ask for ?include=badges. They must get exactly the badge-worthy
// findings of the full response, with local settings applied, and the catalog
// entries for those checks, nothing else.
func TestHandleAuditIncludeBadges(t *testing.T) {
	withIgnoredDefaultNS(t)

	get := func(url string) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		testServerSrv.handleAudit(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", url, rec.Code)
		}
		return rec.Body.Bytes()
	}

	var full bp.ScanResults
	if err := json.Unmarshal(get("/api/audit"), &full); err != nil {
		t.Fatal(err)
	}
	slimBody := get("/api/audit?include=badges")
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(slimBody, &keys); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(keys)); !reflect.DeepEqual(got, []string{"checks", "findings"}) {
		t.Fatalf("include=badges returned keys %v, want [checks findings]", got)
	}
	var slim struct {
		Findings []bp.Finding            `json:"findings"`
		Checks   map[string]bp.CheckMeta `json:"checks"`
	}
	if err := json.Unmarshal(slimBody, &slim); err != nil {
		t.Fatal(err)
	}

	want := auditBadgesResponse(&full)
	if !reflect.DeepEqual(slim.Findings, want.Findings) || !reflect.DeepEqual(slim.Checks, want.Checks) {
		t.Errorf("include=badges is not the badge projection of the full response:\n got %+v\nwant %+v", slim, want)
	}
	for _, f := range slim.Findings {
		if f.Namespace == "default" {
			t.Errorf("finding in the ignored namespace leaked into the badges response: %+v", f)
		}
	}

	// The shared fixture has no badge-worthy finding, so the comparison above
	// can pass on two empty answers. Seed the cached scan with findings the
	// settings must keep and drop, and check exactly what arrives.
	t.Setenv("HOME", t.TempDir())
	if _, err := settings.Update(func(s *settings.Settings) {
		s.Audit = &settings.AuditConfig{IgnoredNamespaces: []string{"default"}, DisabledChecks: []string{"ingressNoMatchingService"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}
	t.Cleanup(func() {
		auditCache.mu.Lock()
		auditCache.results = nil
		auditCache.mu.Unlock()
	})
	auditCache.mu.Lock()
	seeded := *auditCache.results
	keep := bp.Finding{Kind: "Service", Namespace: "shop", Name: "web", CheckID: "serviceNoMatchingPods"}
	seeded.Findings = append(slices.Clone(seeded.Findings),
		keep,
		bp.Finding{Kind: "Service", Namespace: "default", Name: "ignored", CheckID: "serviceNoMatchingPods"},
		bp.Finding{Kind: "Ingress", Namespace: "shop", Name: "web", CheckID: "ingressNoMatchingService"},
		bp.Finding{Kind: "Deployment", Namespace: "shop", Name: "web", CheckID: "runAsRoot"},
	)
	auditCache.results = &seeded
	auditCache.mu.Unlock()
	var seededBadges auditBadges
	if err := json.Unmarshal(get("/api/audit?include=badges"), &seededBadges); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seededBadges.Findings, []bp.Finding{keep}) {
		t.Fatalf("include=badges findings = %+v, want only %+v", seededBadges.Findings, keep)
	}
	if got := slices.Sorted(maps.Keys(seededBadges.Checks)); !reflect.DeepEqual(got, []string{"serviceNoMatchingPods"}) || !seededBadges.Checks["serviceNoMatchingPods"].BadgeWorthy {
		t.Fatalf("include=badges checks = %+v, want the serviceNoMatchingPods entry only", seededBadges.Checks)
	}
}

func TestAuditBadgesResponseKeepsOnlyBadgeWorthyChecks(t *testing.T) {
	results := &bp.ScanResults{
		Findings: []bp.Finding{
			{Kind: "Service", Namespace: "shop", Name: "web", CheckID: "noEndpoints"},
			{Kind: "Deployment", Namespace: "shop", Name: "web", CheckID: "runAsRoot"},
			{Kind: "Ingress", Namespace: "shop", Name: "web", CheckID: "missingBackend"},
			{Kind: "Pod", Namespace: "shop", Name: "web-1", CheckID: "unknownCheck"},
		},
		Checks: map[string]bp.CheckMeta{
			"noEndpoints":    {ID: "noEndpoints", BadgeWorthy: true},
			"missingBackend": {ID: "missingBackend", BadgeWorthy: true},
			"runAsRoot":      {ID: "runAsRoot"},
			"unusedCheck":    {ID: "unusedCheck", BadgeWorthy: true},
		},
	}
	got := auditBadgesResponse(results)
	want := auditBadges{
		Findings: []bp.Finding{results.Findings[0], results.Findings[2]},
		Checks: map[string]bp.CheckMeta{
			"noEndpoints":    results.Checks["noEndpoints"],
			"missingBackend": results.Checks["missingBackend"],
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	empty := auditBadgesResponse(&bp.ScanResults{})
	b, _ := json.Marshal(empty)
	if string(b) != `{"findings":[],"checks":{}}` {
		t.Fatalf("empty scan marshals as %s, want empty array and object", b)
	}
}

// A caller with no readable namespace gets an empty answer in the shape they
// asked for, so a badge view never receives the full response.
func TestHandleAuditIncludeBadgesWithoutNamespaceAccess(t *testing.T) {
	s := newTestServer(t)
	s.permCache = pkgauth.NewPermissionCache().WithConnectionKey(currentPermissionConnection)
	s.permCache.Set("alice", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{}})
	req := httptest.NewRequest(http.MethodGet, "/api/audit?include=badges", nil)
	req = req.WithContext(pkgauth.ContextWithUser(req.Context(), &pkgauth.User{Username: "alice"}))
	rec := httptest.NewRecorder()

	s.handleAudit(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"findings":[],"checks":{}}` {
		t.Fatalf("body = %s, want the empty badges shape", got)
	}
}
