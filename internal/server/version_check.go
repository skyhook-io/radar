package server

import (
	"context"
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

const maxConcurrentBrowserChecks = 8

func (s *Server) acquireBrowserCheckSlot() bool {
	s.browserCheckMu.Lock()
	defer s.browserCheckMu.Unlock()

	if s.browserCheckSlots == nil {
		s.browserCheckSlots = make(chan struct{}, maxConcurrentBrowserChecks)
	}
	select {
	case s.browserCheckSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseBrowserCheckSlot() {
	<-s.browserCheckSlots
}

func (s *Server) handleVersionCheckBrowser(w http.ResponseWriter, r *http.Request) {
	if deploymentMode() != k8s.DeploymentModeInCluster {
		s.writeError(w, http.StatusNotFound, "browser update checks are only available on in-cluster deployments")
		return
	}

	if !s.acquireBrowserCheckSlot() {
		log.Printf("[version] browser update check dropped by relay concurrency limit")
		s.writeError(w, http.StatusTooManyRequests, "browser update check capacity reached")
		return
	}
	defer s.releaseBrowserCheckSlot()

	if err := version.ReportBrowserUpdateCheck(context.WithoutCancel(r.Context())); err != nil {
		log.Printf("[version] browser update check failed: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
