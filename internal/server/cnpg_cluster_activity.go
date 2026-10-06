package server

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/timeline"
)

const (
	cnpgActivityDefaultWindow = 24 * time.Hour
	cnpgActivityDefaultLimit  = 200
	cnpgActivityMaxLimit      = 1000
)

// handleCNPGClusterActivity serves the Cluster's history from the timeline
// store: the Cluster, its instance Pods, and the CNPG objects attributed to it
// — including ones since deleted — with the K8s Events about each. Rows about
// a kind the caller cannot list in the namespace are dropped.
func (s *Server) handleCNPGClusterActivity(w http.ResponseWriter, r *http.Request) {
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

	now := time.Now()
	since := now.Add(-cnpgActivityDefaultWindow)
	if raw := r.URL.Query().Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid since "+strconv.Quote(raw)+" (expected RFC3339)")
			return
		}
		since = t
	}
	var until time.Time
	if raw := r.URL.Query().Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil || !t.After(since) {
			s.writeError(w, http.StatusBadRequest, "invalid until "+strconv.Quote(raw)+" (expected RFC3339 after since)")
			return
		}
		until = t
	}
	limit := cnpgActivityDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			s.writeError(w, http.StatusBadRequest, "invalid limit "+strconv.Quote(raw)+" (expected a positive integer)")
			return
		}
		limit = min(n, cnpgActivityMaxLimit)
	}

	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not available")
		return
	}
	reader := s.cnpgReader(r)
	response, err := reader.ClusterActivity(r.Context(), store, reader.ClusterContext, namespace, name, cnpgsvc.ActivityOptions{Since: since, Until: until, Limit: limit})
	if err != nil {
		operation := "query activity"
		var readErr *cnpgsvc.ActivityReadError
		if errors.As(err, &readErr) {
			operation = readErr.Operation
		}
		log.Printf("[cnpg] Failed to %s for %s/%s: %v", operation, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, response)
}
