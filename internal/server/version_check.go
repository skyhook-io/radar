package server

import (
	"context"
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

func (s *Server) handleVersionCheckBrowser(w http.ResponseWriter, r *http.Request) {
	if deploymentMode() != k8s.DeploymentModeInCluster {
		s.writeError(w, http.StatusNotFound, "browser update checks are only available on in-cluster deployments")
		return
	}

	if err := version.RelayUpdateCheck(context.WithoutCancel(r.Context())); err != nil {
		log.Printf("[version] relayed update check failed: %v", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
