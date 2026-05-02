package server

import (
	"context"
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

// gitopsRequest is the parsed identity of a single GitOps tree/insights
// request, with the namespace allowlist already resolved against RBAC.
type gitopsRequest struct {
	Kind, Namespace, Name, Group string
	Cache                        *k8s.ResourceCache
	AllowedNamespaces            []string
}

// HasNamespaceAccess reports whether the caller is allowed to inspect any
// namespace's resources. False means handlers should short-circuit with an
// empty success response (see the per-handler empty value).
func (g *gitopsRequest) HasNamespaceAccess() bool {
	return !noNamespaceAccess(g.AllowedNamespaces)
}

// parseGitOpsRequest pulls the GitOps URL params and runs the namespace
// access check shared by /api/gitops/{tree,insights}/.... Returns ok=false
// after writing an error response (caller must return immediately).
func (s *Server) parseGitOpsRequest(w http.ResponseWriter, r *http.Request) (*gitopsRequest, bool) {
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
			return nil, false
		}
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return nil, false
	}

	return &gitopsRequest{
		Kind:              kind,
		Namespace:         namespace,
		Name:              name,
		Group:             group,
		Cache:             cache,
		AllowedNamespaces: s.parseNamespacesForUser(r),
	}, true
}

// buildGitOpsTree constructs the topology + GitOps resource tree for a
// parsed request. The returned tree's RootObject is the live root, so
// downstream consumers (insights) don't need a second cache lookup.
func (s *Server) buildGitOpsTree(ctx context.Context, req *gitopsRequest) (*gitopstree.ResourceTree, error) {
	opts := topology.DefaultBuildOptions()
	opts.Namespaces = req.AllowedNamespaces
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	topoBuilder := topology.NewBuilder(k8s.NewTopologyResourceProvider(req.Cache)).
		WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := topoBuilder.Build(opts)
	if err != nil {
		return nil, err
	}

	return gitopstree.NewBuilder(req.Cache, topo).
		WithAllowedNamespaces(req.AllowedNamespaces).
		Build(ctx, req.Kind, req.Namespace, req.Name, req.Group)
}

// writeGitOpsBuildError maps tree-build errors to HTTP status codes.
func (s *Server) writeGitOpsBuildError(w http.ResponseWriter, err error) {
	if strings.Contains(err.Error(), "unknown resource kind") {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if apierrors.IsNotFound(err) || strings.Contains(strings.ToLower(err.Error()), "not found") {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeError(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) handleGitOpsTree(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	req, ok := s.parseGitOpsRequest(w, r)
	if !ok {
		return
	}
	if !req.HasNamespaceAccess() {
		s.writeJSON(w, &gitopstree.ResourceTree{
			Nodes: []gitopstree.Node{},
			Edges: []gitopstree.Edge{},
		})
		return
	}
	tree, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		s.writeGitOpsBuildError(w, err)
		return
	}
	s.writeJSON(w, tree)
}

func (s *Server) handleGitOpsInsights(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	req, ok := s.parseGitOpsRequest(w, r)
	if !ok {
		return
	}
	if !req.HasNamespaceAccess() {
		s.writeJSON(w, gitopsinsights.Insight{})
		return
	}
	tree, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		s.writeGitOpsBuildError(w, err)
		return
	}
	s.writeJSON(w, gitopsinsights.Build(tree.RootObject, tree))
}
