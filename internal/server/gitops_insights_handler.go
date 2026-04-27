package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/k8s"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"github.com/skyhook-io/radar/pkg/topology"
)

func (s *Server) handleGitOpsInsights(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	kind := normalizeKind(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	if namespace == "_" {
		namespace = ""
	}
	if namespace != "" {
		allowed := s.getUserNamespaces(r, []string{namespace})
		if noNamespaceAccess(allowed) {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("no access to namespace %q", namespace))
			return
		}
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, gitopsinsights.Insight{})
		return
	}

	root, err := cache.GetDynamicWithGroup(r.Context(), kind, namespace, name, group)
	if err != nil {
		if strings.Contains(err.Error(), "unknown resource kind") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if apierrors.IsNotFound(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if root.GetKind() == "" {
		root.SetKind(kind)
	}

	opts := topology.DefaultBuildOptions()
	opts.Namespaces = namespaces
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	topoBuilder := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
		WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := topoBuilder.Build(opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resourceTree, err := gitopstree.NewBuilder(cache, topo).WithAllowedNamespaces(namespaces).Build(r.Context(), kind, namespace, name, group)
	if err != nil {
		if strings.Contains(err.Error(), "unknown resource kind") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if apierrors.IsNotFound(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, gitopsinsights.Build(root, resourceTree))
}
