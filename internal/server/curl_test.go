package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurlServiceInvalidJSON(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleCurlService(w, httptest.NewRequest(http.MethodPost, "/api/services/curl", strings.NewReader("{")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
