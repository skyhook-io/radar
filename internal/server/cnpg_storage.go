package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleCNPGClusterStorage serves GET /api/cnpg/clusters/{namespace}/{name}/storage.
// Reading the Cluster is the gate; its claims, their usage and the WAL facts
// each need their own grant and report their own coverage.
func (s *Server) handleCNPGClusterStorage(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	resp, err := reader.ClusterStorage(r.Context(), namespace, name)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeCNPGMetricsResponse(w, reader, resp)
}

// ---------- fleet ----------

func (s *Server) handleCNPGFleetDisk(w http.ResponseWriter, r *http.Request) {
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
	s.writeCNPGMetricsResponse(w, reader, reader.FleetDisk(r.Context(), cache, namespaces))
}
