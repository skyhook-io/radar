package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

// Destroying an instance mirrors `kubectl cnpg destroy CLUSTER INSTANCE
// [--keep-pvc]`: the instance's PVCs are detached (keep) or deleted first, so
// the operator never sees a dangling PVC and recreates the Pod on it; then the
// Pod is deleted, then the instance's Jobs. The operator replaces the instance
// with a new one under a new serial. Unlike upstream, Radar refuses the
// primary (an unplanned failover, which a switchover does safely) and
// requires the instance to be fenced first: the operator never promotes a
// fenced instance, which is the only thing that keeps a failover from making
// it primary between the last check and a delete. The fence on the destroyed
// name is lifted last.

func (s *Server) handleCNPGDestroyPlan(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name, pod := chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), chi.URLParam(r, "pod")
	reader := s.cnpgReader(r)
	dyn, contextName, typed := reader.dynamic, reader.actionContext, reader.Clients.Typed
	if dyn == nil || typed == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	plan, err := reader.DestroyPlan(r.Context(), cnpgsvc.ActionClients{Dynamic: dyn, Typed: typed}, contextName, namespace, name, pod)
	if err != nil {
		s.writeCNPGActionError(w, err, "destroyInstance", namespace, name)
		return
	}
	s.writeJSON(w, plan)
}
