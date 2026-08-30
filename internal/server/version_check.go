package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

type browserVersionCheckRequest struct {
	ReportDay string `json:"reportDay"`
	ReportID  string `json:"reportId"`
}

const (
	maxBrowserReportsPerDay     = 5000
	maxConcurrentBrowserReports = 8
)

func (s *Server) claimBrowserReport(day, id string) (first, capped bool) {
	s.browserReportMu.Lock()
	defer s.browserReportMu.Unlock()

	if s.browserReports == nil {
		s.browserReports = make(map[string]map[string]struct{})
	}
	today := time.Now().UTC()
	acceptedDays := map[string]struct{}{
		today.AddDate(0, 0, -1).Format("2006-01-02"): {},
		today.Format("2006-01-02"):                   {},
		today.AddDate(0, 0, 1).Format("2006-01-02"):  {},
	}
	for storedDay := range s.browserReports {
		if _, accepted := acceptedDays[storedDay]; !accepted {
			delete(s.browserReports, storedDay)
		}
	}

	reports := s.browserReports[day]
	if reports == nil {
		reports = make(map[string]struct{})
		s.browserReports[day] = reports
	}
	if _, exists := reports[id]; exists {
		return false, false
	}
	if len(reports) >= maxBrowserReportsPerDay {
		return false, true
	}
	reports[id] = struct{}{}
	return true, false
}

func (s *Server) abandonBrowserReport(day, id string) {
	s.browserReportMu.Lock()
	defer s.browserReportMu.Unlock()
	reports := s.browserReports[day]
	if reports == nil {
		return
	}
	delete(reports, id)
	if len(reports) == 0 {
		delete(s.browserReports, day)
	}
}

func (s *Server) acquireBrowserReportSlot() (chan struct{}, bool) {
	s.browserReportMu.Lock()
	if s.browserReportSlots == nil {
		s.browserReportSlots = make(chan struct{}, maxConcurrentBrowserReports)
	}
	slots := s.browserReportSlots
	s.browserReportMu.Unlock()

	select {
	case slots <- struct{}{}:
		return slots, true
	default:
		return nil, false
	}
}

func (s *Server) handleVersionCheckBrowser(w http.ResponseWriter, r *http.Request) {
	if deploymentMode() != k8s.DeploymentModeInCluster {
		s.writeError(w, http.StatusNotFound, "browser update reporting is only available on in-cluster deployments")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body browserVersionCheckRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "request body must contain one JSON object")
		return
	}

	reportID, err := uuid.Parse(body.ReportID)
	if err != nil || reportID.String() != strings.ToLower(body.ReportID) || reportID.Version() != 4 || reportID.Variant() != uuid.RFC4122 {
		s.writeError(w, http.StatusBadRequest, "reportId must be a canonical UUIDv4")
		return
	}
	reportDay, err := time.Parse("2006-01-02", body.ReportDay)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "reportDay must use YYYY-MM-DD")
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if reportDay.Before(today.AddDate(0, 0, -1)) || reportDay.After(today.AddDate(0, 0, 1)) {
		s.writeError(w, http.StatusBadRequest, "reportDay is outside the accepted clock-skew window")
		return
	}

	first, capped := s.claimBrowserReport(body.ReportDay, body.ReportID)
	if capped {
		s.writeError(w, http.StatusTooManyRequests, "daily browser update report limit reached")
		return
	}
	if !first {
		info := version.CheckForUpdateRelease(r.Context())
		s.writeJSON(w, info)
		return
	}
	slots, acquired := s.acquireBrowserReportSlot()
	if !acquired {
		s.abandonBrowserReport(body.ReportDay, body.ReportID)
		s.writeError(w, http.StatusTooManyRequests, "too many concurrent browser update reports")
		return
	}
	defer func() { <-slots }()

	info, reported := version.CheckForUpdateBrowser(r.Context(), body.ReportDay, body.ReportID, s.authConfig.Enabled())
	if !reported {
		s.abandonBrowserReport(body.ReportDay, body.ReportID)
		s.writeError(w, http.StatusBadGateway, "browser update report could not reach the release service")
		return
	}
	s.writeJSON(w, info)
}
