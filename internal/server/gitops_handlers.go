package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
// parsed request. The live root unstructured is returned alongside the tree
// so downstream consumers (insights) can derive views without re-fetching.
func (s *Server) buildGitOpsTree(ctx context.Context, req *gitopsRequest) (*gitopstree.ResourceTree, *unstructured.Unstructured, error) {
	opts := topology.DefaultBuildOptions()
	opts.Namespaces = req.AllowedNamespaces
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	topoBuilder := topology.NewBuilder(k8s.NewTopologyResourceProvider(req.Cache)).
		WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := topoBuilder.Build(opts)
	if err != nil {
		return nil, nil, err
	}

	return gitopstree.NewBuilder(req.Cache, topo).
		WithAllowedNamespaces(req.AllowedNamespaces).
		Build(ctx, req.Kind, req.Namespace, req.Name, req.Group)
}

// writeGitOpsBuildError maps tree-build errors to HTTP status codes.
// Uses errors.Is on typed sentinels rather than string matching so the HTTP
// status doesn't drift if an upstream error message gets reworded.
func (s *Server) writeGitOpsBuildError(w http.ResponseWriter, req *gitopsRequest, err error) {
	switch {
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, err.Error())
	default:
		log.Printf("[gitops] Failed to build tree for %s %s/%s (group=%q): %v", req.Kind, req.Namespace, req.Name, req.Group, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
	}
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
	tree, _, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		s.writeGitOpsBuildError(w, req, err)
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
	tree, root, err := s.buildGitOpsTree(r.Context(), req)
	if err != nil {
		s.writeGitOpsBuildError(w, req, err)
		return
	}
	s.writeJSON(w, gitopsinsights.Build(root, tree))
}
