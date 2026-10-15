package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Diagnosis inside PostgreSQL. Radar runs fixed SQL with psql in the
// instance's postgres container through the caller's own pods/exec — the same
// access that lets them open psql themselves, which is why query text is
// shown to them. Nothing a caller sends is ever part of the SQL text: the only
// inputs (a backend's pid and start time) are validated and passed as psql
// variables, which psql quotes as literals.

// handleCNPGClusterSessions serves GET /api/cnpg/clusters/{ns}/{name}/sessions:
// the blocker → victim relations on one instance (the primary unless ?pod=
// names another instance), read with fixed SQL over the caller's pods/exec.
// Without exec the answer is a 200 whose state is denied, never empty.
func (s *Server) handleCNPGClusterSessions(w http.ResponseWriter, r *http.Request) {
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
	resp, err := reader.Sessions(r.Context(), cache, cluster, r.URL.Query().Get("pod"))
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}
