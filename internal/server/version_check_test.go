package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestHandleVersionCheckBrowserValidatesAndAcceptsDailyReport(t *testing.T) {
	k8s.ForceInCluster = true
	t.Cleanup(func() { k8s.ForceInCluster = false })
	s := &Server{authConfig: auth.Config{Mode: "proxy"}}

	t.Run("valid", func(t *testing.T) {
		body := `{"reportDay":"` + time.Now().UTC().Format("2006-01-02") + `","reportId":"c66ce4e8-fb90-4e0e-a2af-2172bb868b9e"}`
		request := httptest.NewRequest(http.MethodPost, "/api/version-check/browser", strings.NewReader(body))
		response := httptest.NewRecorder()
		s.handleVersionCheckBrowser(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	})

	t.Run("invalid uuid", func(t *testing.T) {
		body := `{"reportDay":"` + time.Now().UTC().Format("2006-01-02") + `","reportId":"not-a-uuid"}`
		request := httptest.NewRequest(http.MethodPost, "/api/version-check/browser", strings.NewReader(body))
		response := httptest.NewRecorder()
		s.handleVersionCheckBrowser(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.Code)
		}
	})

	t.Run("non-v4 uuid", func(t *testing.T) {
		body := `{"reportDay":"` + time.Now().UTC().Format("2006-01-02") + `","reportId":"00000000-0000-1000-8000-000000000000"}`
		request := httptest.NewRequest(http.MethodPost, "/api/version-check/browser", strings.NewReader(body))
		response := httptest.NewRecorder()
		s.handleVersionCheckBrowser(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.Code)
		}
	})

	t.Run("outside clock skew window", func(t *testing.T) {
		body := `{"reportDay":"` + time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02") + `","reportId":"04af1e12-bf89-47cf-9cc9-f401852af21e"}`
		request := httptest.NewRequest(http.MethodPost, "/api/version-check/browser", strings.NewReader(body))
		response := httptest.NewRecorder()
		s.handleVersionCheckBrowser(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.Code)
		}
	})

	t.Run("cloud mode excluded", func(t *testing.T) {
		t.Setenv("RADAR_CLOUD_MODE", "true")
		body := `{"reportDay":"` + time.Now().UTC().Format("2006-01-02") + `","reportId":"89d7b3a3-d907-4e36-aef5-e2257036fc79"}`
		request := httptest.NewRequest(http.MethodPost, "/api/version-check/browser", strings.NewReader(body))
		response := httptest.NewRecorder()
		s.handleVersionCheckBrowser(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.Code)
		}
	})
}

func TestClaimBrowserReportDeduplicatesAndCapsEachDay(t *testing.T) {
	s := &Server{}
	day := time.Now().UTC().Format("2006-01-02")
	id := "c66ce4e8-fb90-4e0e-a2af-2172bb868b9e"

	first, capped := s.claimBrowserReport(day, id)
	if !first || capped {
		t.Fatalf("first claim = (%v, %v), want accepted", first, capped)
	}
	first, capped = s.claimBrowserReport(day, id)
	if first || capped {
		t.Fatalf("duplicate claim = (%v, %v), want duplicate", first, capped)
	}

	failedID := "04af1e12-bf89-47cf-9cc9-f401852af21e"
	s.claimBrowserReport(day, failedID)
	s.abandonBrowserReport(day, failedID)
	first, capped = s.claimBrowserReport(day, failedID)
	if !first || capped {
		t.Fatalf("abandoned claim = (%v, %v), want retry accepted", first, capped)
	}
	s.abandonBrowserReport(day, failedID)

	for i := 1; i < maxBrowserReportsPerDay; i++ {
		reportID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		first, capped = s.claimBrowserReport(day, reportID)
		if !first || capped {
			t.Fatalf("claim %d was unexpectedly rejected", i)
		}
	}
	first, capped = s.claimBrowserReport(day, "00000000-0000-4000-8000-999999999999")
	if first || !capped {
		t.Fatalf("over-cap claim = (%v, %v), want capped", first, capped)
	}
}

func TestBrowserReportConcurrencyIsBounded(t *testing.T) {
	s := &Server{browserReportSlots: make(chan struct{}, maxConcurrentBrowserReports)}
	for i := 0; i < maxConcurrentBrowserReports; i++ {
		if _, acquired := s.acquireBrowserReportSlot(); !acquired {
			t.Fatalf("slot %d was unexpectedly rejected", i)
		}
	}
	if _, acquired := s.acquireBrowserReportSlot(); acquired {
		t.Fatal("request above the concurrency limit was accepted")
	}
}
