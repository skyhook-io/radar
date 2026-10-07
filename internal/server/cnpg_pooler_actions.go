package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

// Pooler detail and pause/resume. spec.pgbouncer.paused is desired state: the
// pooler's instance manager applies it to each PgBouncer with PAUSE/RESUME.
// Whether a PgBouncer is paused is observed separately (SHOW STATE over the
// caller's pods/exec); the exporter does not publish it.

func (s *Server) handleCNPGPoolerCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	dyn, contextName, typed := reader.dynamic, reader.actionContext, reader.Clients.Typed
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp, err := reader.PoolerCapabilities(r.Context(), cnpgsvc.ActionClients{Dynamic: dyn, Typed: typed}, contextName, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "capabilities", namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleCNPGPoolerAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name, action := chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), chi.URLParam(r, "action")
	if action != "pause" && action != "resume" {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown Pooler action %q: must be pause or resume", action))
		return
	}
	req, _, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	errAction := "pooler" + strings.ToUpper(action[:1]) + action[1:]
	clients, err := s.cnpgActionClients(r, req)
	if err != nil {
		s.writeCNPGActionError(w, err, errAction, namespace, name)
		return
	}
	auth.AuditLog(r, namespace, name)
	res, err := cnpgsvc.RunCNPGPoolerAction(r.Context(), clients, namespace, name, action, req)
	if err != nil {
		s.writeCNPGActionError(w, err, errAction, namespace, name)
		return
	}
	log.Printf("[cnpg] %s on Pooler %s/%s requested", action, sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, res)
}

// ---------- observed PgBouncer state ----------

func (s *Server) handleCNPGPgBouncerState(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGRuntime(w, r, namespace, "poolers") {
		return
	}
	reader := s.cnpgReader(r)
	cache := reader.Observations.Cache
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource cache not available")
		return
	}
	pooler, err := reader.Pooler(r.Context(), cache, namespace, name)
	if err != nil || pooler == nil {
		s.writeError(w, http.StatusNotFound, "CloudNativePG Pooler "+namespace+"/"+name+" not found")
		return
	}
	resp, err := reader.PgBouncerState(r.Context(), cache, pooler)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}
