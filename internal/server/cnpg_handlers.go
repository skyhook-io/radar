package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

// handleCNPGCatalogUsers returns the Clusters pinned to an image catalog.
//
//	GET /api/cnpg/imagecatalogs/{namespace}/{name}/clusters
//	GET /api/cnpg/clusterimagecatalogs/{name}/clusters
//
// A ClusterImageCatalog is cluster-scoped and may be referenced from any
// namespace, so the answer's scope is not the subject's. Asking the generic
// resource list without a namespace would inherit the caller's namespace view
// filter — a browsing preference — and report "nothing uses this" on the
// strength of whichever namespaces they happen to be looking at. Scope follows
// permission here, as it does for the RBAC reverse lookups.
func (s *Server) handleCNPGCatalogUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		s.writeError(w, http.StatusBadRequest, "catalog name is required")
		return
	}
	// Empty for the cluster-scoped route, which is what tells the two apart:
	// a Cluster's imageCatalogRef.kind must match the catalog it names.
	namespace := chi.URLParam(r, "namespace")
	wantKind := "ClusterImageCatalog"
	if namespace != "" {
		wantKind = "ImageCatalog"
	}

	if !s.canRead(r, cnpgsvc.Group, "clusters", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to CloudNativePG clusters")
		return
	}
	reader := s.cnpgReader(r)
	cache := reader.Observations.Cache
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	resp, err := reader.CatalogUsers(r.Context(), cache, namespace, name, wantKind)
	if err != nil {
		s.writeCNPGCachedReadError(w, err, namespace, name)
		return
	}
	s.writeJSON(w, resp)
}
