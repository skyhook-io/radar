package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	bp "github.com/skyhook-io/radar/pkg/audit"
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
