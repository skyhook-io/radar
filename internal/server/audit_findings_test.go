package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

// Badge views ask for ?include=findings. They must get the same effective
// findings and check catalog as the full response, local settings applied, and
// nothing else.
func TestHandleAuditIncludeFindings(t *testing.T) {
	withIgnoredDefaultNS(t)

	get := func(url string) map[string]json.RawMessage {
		t.Helper()
		rec := httptest.NewRecorder()
		testServerSrv.handleAudit(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", url, rec.Code)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return body
	}

	full := get("/api/audit")
	slim := get("/api/audit?include=findings")
	if keys := slices.Sorted(maps.Keys(slim)); !reflect.DeepEqual(keys, []string{"checks", "findings"}) {
		t.Fatalf("include=findings returned keys %v, want [checks findings]", keys)
	}
	for _, k := range []string{"findings", "checks"} {
		if string(slim[k]) != string(full[k]) {
			t.Errorf("%s differs from the full response:\n slim %s\n full %s", k, slim[k], full[k])
		}
	}
	if string(slim["findings"]) == "null" || string(slim["findings"]) == "[]" {
		t.Fatal("fixture produced no findings; the comparison proves nothing")
	}
}
