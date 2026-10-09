package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func (s *Server) handleCNPGClusterHistory(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.requireConnected(w) {
		return
	}
	if integration.NoNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.canRead(r, cnpgsvc.Group, "clusters", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to clusters.postgresql.cnpg.io in namespace "+namespace)
		return
	}
	rng, ok := prometheuspkg.ParseCNPGHistoryRange(r.URL.Query().Get("range"))
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid range "+r.URL.Query().Get("range")+" (expected 15m, 1h, 6h or 24h)")
		return
	}
	reader := s.cnpgReader(r)
	cache := reader.Observations.Cache
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	_, cluster, err := reader.Observations.Cluster(r.Context(), namespace, name)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}

	s.writeJSON(w, reader.ClusterHistory(r.Context(), cache, cluster, rng))
}

// ---------- fleet ----------

func (s *Server) handleCNPGFleetMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	reader := s.cnpgReader(r)
	cache := reader.Observations.Cache
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	namespaces := s.parseNamespacesForUser(r)
	s.writeJSON(w, reader.FleetMetrics(r.Context(), cache, namespaces))
}
