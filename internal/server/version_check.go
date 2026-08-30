package server

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

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

	today := time.Now().UTC().Format("2006-01-02")
	slots, claimed := s.claimBrowserCheck(today)
	if !claimed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer func() { <-slots }()

	if err := version.ReportBrowserUpdateCheck(context.WithoutCancel(r.Context()), today); err != nil {
		log.Printf("[version] browser update check failed: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
