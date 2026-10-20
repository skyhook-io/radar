package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
)

// These previews use caller clients and server dry-run. They do not persist
// configuration, read Secrets or probe remote object storage.
func (s *Server) handleCNPGArchivingPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	var req struct {
		ReviewedContext string `json:"reviewedContext"`
		cnpgsvc.ArchivingParams
	}
	if err := decodeBoundedJSONBody(w, r, integration.ActionBodyLimit, &req); err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		s.writeError(w, status, "invalid preview request: "+err.Error())
		return
	}
	reader := s.cnpgReader(r)
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if reader.dynamic == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	if err := integration.CheckReviewedContext(req.ReviewedContext, reader.actionContext); err != nil {
		s.writeCNPGActionError(w, err, "configureArchiving", namespace, name)
		return
	}
	out, err := reader.PreviewArchiving(r.Context(), cnpgsvc.ActionClients{Dynamic: reader.dynamic}, reader.actionContext, namespace, name, req.ArchivingParams)
	if err != nil {
		s.writeCNPGActionError(w, err, "configureArchiving", namespace, name)
		return
	}
	s.writeJSON(w, out)
}

func (s *Server) handleCNPGScheduleMethodPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	reader := s.cnpgReader(r)
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if reader.dynamic == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	out, err := reader.PreviewScheduleMethod(r.Context(), cnpgsvc.ActionClients{Dynamic: reader.dynamic}, reader.actionContext, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "repairMethod", namespace, name)
		return
	}
	s.writeJSON(w, out)
}

// A draft has no ScheduledBackup lastCheckTime. Read its target Cluster as
// the caller and count from now rather than inventing an existing schedule.
func (s *Server) handleCNPGDraftSchedulePreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	reader := s.cnpgReader(r)
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if reader.dynamic == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	out, err := reader.PreviewDraftSchedule(r.Context(), reader.dynamic, namespace, name, r.URL.Query().Get("schedule"), time.Now())
	if err != nil {
		s.writeCNPGActionError(w, err, "schedule-preview", namespace, name)
		return
	}
	s.writeJSON(w, out)
}
