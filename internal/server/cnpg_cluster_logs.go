package server

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
)

// authorizeCNPGClusterLogs gates on reading the Cluster, listing its Pods and
// reading their logs — before the Cluster is looked up, so a denied caller
// cannot probe which Clusters exist.
func (s *Server) authorizeCNPGClusterLogs(w http.ResponseWriter, r *http.Request, namespace string) bool {
	if !s.requireConnected(w) {
		return false
	}
	if integration.NoNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return false
	}
	if !s.canRead(r, cnpgsvc.Group, "clusters", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to clusters.postgresql.cnpg.io in namespace "+namespace)
		return false
	}
	if !s.canRead(r, "", "pods", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to pods in namespace "+namespace)
		return false
	}
	return s.authorizePodLogRead(w, r, namespace)
}

func (s *Server) handleCNPGClusterLogs(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGClusterLogs(w, r, namespace) {
		return
	}
	query, err := cnpgsvc.ParseLogQuery(r.URL.Query(), time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := s.cnpgReader(r).PrepareLogs(r.Context(), namespace, name, query)
	if err != nil {
		s.writeCNPGCachedReadError(w, err)
		return
	}
	response, err := target.Snapshot(r.Context(), query)
	if err != nil {
		s.writeCNPGCachedReadError(w, err)
		return
	}
	s.writeJSON(w, response)
}

// The service emits log events; only this adapter frames them as browser SSE.
func (s *Server) handleCNPGClusterLogsStream(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !s.authorizeCNPGClusterLogs(w, r, namespace) {
		return
	}
	query, err := cnpgsvc.ParseLogQuery(r.URL.Query(), time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := s.cnpgReader(r).PrepareLogs(r.Context(), namespace, name, query)
	if err != nil {
		s.writeCNPGCachedReadError(w, err)
		return
	}
	if err := target.RequireClient(); err != nil {
		s.writeCNPGCachedReadError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("[cnpg] Failed to stream logs for %s/%s: response writer does not support flushing", namespace, name)
		s.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	target.Follow(r.Context(), query, func(event string, data any) { sendSSEEvent(w, flusher, event, data) })
}
