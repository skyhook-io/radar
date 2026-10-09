package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// GET /api/cnpg/scheduledbackups/{ns}/{name}/schedule-preview?schedule=
// Pure computation over the schedule read as the caller; writes nothing.
func (s *Server) handleCNPGSchedulePreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	dyn := reader.dynamic
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	preview, err := reader.PreviewSchedule(r.Context(), dyn, namespace, name, r.URL.Query().Get("schedule"), time.Now())
	if err != nil {
		s.writeCNPGActionError(w, err, "schedule-preview", namespace, name)
		return
	}
	s.writeJSON(w, preview)
}
