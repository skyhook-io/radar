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
	writeInvestigationCapacity(rec, 3)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != investigationCapacityCode || body.Limit != 3 {
		t.Fatalf("body = %+v, want code %q and limit 3", body, investigationCapacityCode)
	}
	if !strings.HasPrefix(body.Error, "3 investigations are already running") {
		t.Fatalf("error = %q, want it to name how many are running", body.Error)
	}
}
