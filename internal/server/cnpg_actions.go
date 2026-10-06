package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/auth"
	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
)

// CloudNativePG write actions. Every write mirrors what `kubectl cnpg` does
// (promote, restart, reload, fence, hibernate, backup) so the operator sees
// exactly the request its own tooling would have made. A confirmation binds
// the facts the dialog showed, not just a resourceVersion: the server re-reads
// the Cluster, compares those facts and refuses with 409 when anything the
// user reviewed has moved. Disruptive writes are never retried automatically.

func (s *Server) handleCNPGClusterCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	dyn, contextName, typed := reader.dynamic, reader.actionContext, reader.Clients.Typed
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp, err := reader.ClusterCapabilities(r.Context(), cnpgsvc.ActionClients{Dynamic: dyn, Typed: typed}, contextName, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "capabilities", namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleCNPGScheduleCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	dyn, contextName := reader.dynamic, reader.actionContext
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	resp, err := reader.ScheduleCapabilities(r.Context(), cnpgsvc.ActionClients{Dynamic: dyn}, contextName, namespace, name)
	if err != nil {
		s.writeCNPGActionError(w, err, "capabilities", namespace, name)
		return
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleCNPGClusterAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	action := chi.URLParam(r, "action")
	if !cnpgsvc.IsClusterAction(action) {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown CloudNativePG cluster action %q: must be one of %s", action, strings.Join(cnpgsvc.ClusterActions(), ", ")))
		return
	}
	req, _, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	clients, err := s.cnpgActionClients(r, req)
	if err != nil {
		s.writeCNPGActionError(w, err, action, namespace, name)
		return
	}
	if clients.Typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	auth.AuditLog(r, namespace, name)
	res, err := cnpgsvc.RunCNPGClusterAction(r.Context(), clients, namespace, name, action, req)
	if err != nil {
		s.writeCNPGActionError(w, err, action, namespace, name)
		return
	}
	log.Printf("[cnpg] %s on Cluster %s/%s requested", action, sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, res)
}

func (s *Server) handleCNPGScheduleAction(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	action := chi.URLParam(r, "action")
	if !cnpgsvc.IsScheduleAction(action) {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown ScheduledBackup action %q: must be suspend, resume, run or setSchedule", action))
		return
	}
	req, _, ok := s.decodeActionRequest(w, r)
	if !ok {
		return
	}
	clients, err := s.cnpgActionClients(r, req)
	if err != nil {
		s.writeCNPGActionError(w, err, action, namespace, name)
		return
	}
	auth.AuditLog(r, namespace, name)
	res, err := cnpgsvc.RunCNPGScheduleAction(r.Context(), clients, namespace, name, action, req)
	if err != nil {
		s.writeCNPGActionError(w, err, action, namespace, name)
		return
	}
	log.Printf("[cnpg] %s on ScheduledBackup %s/%s requested", action, sanitizeForLog(namespace), sanitizeForLog(name))
	s.writeJSON(w, res)
}

// writeCNPGActionError adds the CloudNativePG reading of an admission-webhook
// failure to the shared action error answer.
func (s *Server) writeCNPGActionError(w http.ResponseWriter, err error, action, namespace, name string) {
	var ae *integration.ActionError
	if !errors.As(err, &ae) && strings.Contains(err.Error(), "failed calling webhook") {
		err = integration.RefuseAction(http.StatusServiceUnavailable, cnpgsvc.CodeWebhook, "%s", "The CloudNativePG operator's admission webhook did not answer — the operator may be down: "+err.Error())
	}
	s.writeActionError(w, "cnpg", err, action, namespace, name, cnpgsvc.GrantFor)
}
