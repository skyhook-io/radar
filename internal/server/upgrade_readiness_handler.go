package server

import (
	"errors"
	"net/http"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func (s *Server) handleUpgradeReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Cache not initialized")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	noAccess := noNamespaceAccess(namespaces)
	var scanInput *k8s.ResourceCache
	if !noAccess {
		scanInput = cache
	}
	results, err := audit.RunUpgradeReadinessFromCache(scanInput, namespaces, k8s.GetServerVersion(), r.URL.Query().Get("target"))
	if err != nil {
		switch {
		case errors.Is(err, upgradereadiness.ErrInvalidTargetVersion), errors.Is(err, upgradereadiness.ErrNonForwardTarget):
			s.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, upgradereadiness.ErrInvalidCurrentVersion):
			s.writeError(w, http.StatusServiceUnavailable, "Unable to determine the cluster Kubernetes version")
		default:
			s.writeError(w, http.StatusInternalServerError, "Upgrade readiness scan failed")
		}
		return
	}
	if noAccess {
		results.Coverage.State = "no_access"
		results.Coverage.UnavailableKinds = nil
	}

	s.writeJSON(w, results)
}
