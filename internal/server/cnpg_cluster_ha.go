package server

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/metadata"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

// GET /api/cnpg/clusters/{namespace}/{name}/ha: the facts that decide whether
// a Cluster survives losing an instance, and what a planned switchover will
// meet. Reading the Cluster never implies reading anything else: Nodes, Jobs,
// PodDisruptionBudgets, Leases, EndpointSlices, Secret metadata and the
// FailoverQuorum are each authorized on their own and report their own state.

func (s *Server) handleCNPGClusterHA(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	reader := s.cnpgReader(r)
	cache, cluster, err := reader.Observations.Cluster(r.Context(), namespace, name)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	typed, dyn, cfg := reader.Clients.Typed, reader.dynamic, reader.config
	if typed == nil || dyn == nil || cfg == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		log.Printf("[cnpg] Failed to build metadata client for %s/%s: %v", sanitizeForLog(namespace), sanitizeForLog(name), err)
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	s.writeJSON(w, reader.ClusterHA(r.Context(), cnpgsvc.HAClients{Typed: typed, Dynamic: dyn, Metadata: meta}, cache, cluster, time.Now().UTC()))
}
