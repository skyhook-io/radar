package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Read-only inspection inside PostgreSQL, with the same fixed-SQL-over-the-
// caller's-pods/exec model as the Sessions view: what a restored cluster
// holds, and which values declared PostgreSQL parameters have on each
// instance. Nothing here writes, and nothing a caller sends is SQL text.

// handleCNPGRestoreChecks serves GET /api/cnpg/clusters/{ns}/{name}/restore-checks
// for a Cluster bootstrapped from a backup (400 otherwise): read-only facts
// from its primary over the caller's pods/exec. Without exec the answer is a
// 200 whose state is denied.
func (s *Server) handleCNPGRestoreChecks(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGRuntime(w, r, namespace, "clusters") {
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
	resp, err := reader.RestoreChecks(r.Context(), cache, cluster)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

// handleCNPGClusterParameters serves GET /api/cnpg/clusters/{ns}/{name}/parameters:
// observation only — it reads, on every instance, the parameters the Cluster
// declares. Without exec the answer is a 200 whose state is denied.
func (s *Server) handleCNPGClusterParameters(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGRuntime(w, r, namespace, "clusters") {
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
	resp, err := reader.Parameters(r.Context(), cache, cluster)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}
