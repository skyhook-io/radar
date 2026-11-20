package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteInvestigationCapacityCarriesCodeAndLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	writeInvestigationCapacity(rec, 3, "kind-dev")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Limit   int    `json:"limit"`
		Context string `json:"context"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != investigationCapacityCode || body.Limit != 3 || body.Context != "kind-dev" {
		t.Fatalf("body = %+v, want code %q, limit 3 and context kind-dev", body, investigationCapacityCode)
	}
	if !strings.HasPrefix(body.Error, "3 investigations are already running") {
		t.Fatalf("error = %q, want it to name how many are running", body.Error)
	}
}
