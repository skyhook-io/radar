package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

type browserVersionCheckRequest struct {
	ReportDay string `json:"reportDay"`
}

const (
	maxConcurrentBrowserChecks = 8
	maxBrowserChecksPerDay     = 5000
)

func (s *Server) claimBrowserCheck(day string) (chan struct{}, bool) {
	s.browserCheckMu.Lock()
	defer s.browserCheckMu.Unlock()

	if s.browserCheckDay != day {
		s.browserCheckDay = day
		s.browserCheckCount = 0
	}
	if s.browserCheckCount >= maxBrowserChecksPerDay {
		return nil, false
	}
	if s.browserCheckSlots == nil {
		s.browserCheckSlots = make(chan struct{}, maxConcurrentBrowserChecks)
	}
	select {
	case s.browserCheckSlots <- struct{}{}:
		s.browserCheckCount++
		return s.browserCheckSlots, true
	default:
		return nil, false
	}
}

func (s *Server) handleVersionCheckBrowser(w http.ResponseWriter, r *http.Request) {
	if deploymentMode() != k8s.DeploymentModeInCluster {
		s.writeError(w, http.StatusNotFound, "browser update checks are only available on in-cluster deployments")
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

	slots, claimed := s.claimBrowserCheck(today.Format("2006-01-02"))
	if !claimed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer func() { <-slots }()

	if err := version.ReportBrowserUpdateCheck(r.Context(), body.ReportDay); err != nil {
		log.Printf("[version] browser update check failed: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
