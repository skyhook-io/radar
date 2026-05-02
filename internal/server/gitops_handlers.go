package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

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
//
// The topology build itself is the dominant cost — it walks every cached
// resource of every kind. We route it through s.topoMemo so a page-load
// firing /tree + /insights, or the in-flight 2s polling, all share a single
// build. Topology is a deterministic projection of the informer cache, so
// the short TTL has no semantic effect.
func (s *Server) buildGitOpsTree(ctx context.Context, req *gitopsRequest) (*gitopstree.ResourceTree, *unstructured.Unstructured, error) {
	opts := topology.DefaultBuildOptions()
	opts.Namespaces = req.AllowedNamespaces
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	topo, err := s.topoMemo.Get(opts, func() (*topology.Topology, error) {
		return topology.NewBuilder(k8s.NewTopologyResourceProvider(req.Cache)).
			WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())).
			Build(opts)
	})
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
		log.Printf("[gitops] Failed to build tree for %s %s/%s (group=%q): %v", sanitizeForLog(req.Kind), sanitizeForLog(req.Namespace), sanitizeForLog(req.Name), sanitizeForLog(req.Group), err)
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
	resolver := newInsightsResolver(r.Context(), req.Cache, req.AllowedNamespaces)
	s.writeJSON(w, gitopsinsights.Build(root, tree, resolver))
}

// insightsResolver wires the dynamic cache + event lister into the
// gitopsinsights package without exposing internal/k8s types to the public
// pkg/gitops/insights API. Per-request: ctx + namespace allowlist captured
// once at construction; lookups are namespace-filtered to enforce RBAC.
type insightsResolver struct {
	ctx               context.Context
	cache             *k8s.ResourceCache
	allowedNamespaces []string
}

func newInsightsResolver(ctx context.Context, cache *k8s.ResourceCache, allowed []string) *insightsResolver {
	return &insightsResolver{ctx: ctx, cache: cache, allowedNamespaces: allowed}
}

// recentEventsCap bounds events returned per resource. Beyond ~5 the user
// is better served opening the standard drawer; this is meant to surface
// the headline cause inline, not be a full event log.
const recentEventsCap = 5

func (r *insightsResolver) GetLive(group, kind, namespace, name string) *unstructured.Unstructured {
	if r == nil || r.cache == nil || name == "" || kind == "" {
		return nil
	}
	if !r.namespaceAllowed(namespace) {
		return nil
	}
	obj, err := r.cache.GetDynamicWithGroup(r.ctx, kind, namespace, name, group)
	if err != nil {
		return nil
	}
	return obj
}

func (r *insightsResolver) RecentEvents(group, kind, namespace, name string) []gitopsinsights.EventSummary {
	if r == nil || r.cache == nil || r.cache.Events() == nil {
		return nil
	}
	if !r.namespaceAllowed(namespace) {
		return nil
	}
	// Lister scope: namespace-scoped lookup is cheaper than cluster-wide
	// + filter; cluster-scoped resources (namespace="") fall back to the
	// cross-namespace lister and are matched only by kind+name.
	var events []runtime.Object
	if namespace != "" {
		items, err := r.cache.Events().Events(namespace).List(labels.Everything())
		if err != nil {
			return nil
		}
		events = make([]runtime.Object, 0, len(items))
		for _, e := range items {
			events = append(events, e)
		}
	} else {
		items, err := r.cache.Events().List(labels.Everything())
		if err != nil {
			return nil
		}
		events = make([]runtime.Object, 0, len(items))
		for _, e := range items {
			events = append(events, e)
		}
	}
	matched := make([]*corev1.Event, 0, recentEventsCap)
	for _, ro := range events {
		e, ok := ro.(*corev1.Event)
		if !ok {
			continue
		}
		if e.InvolvedObject.Kind != kind || e.InvolvedObject.Name != name {
			continue
		}
		// involvedObject.apiVersion is "group/version" for grouped kinds
		// or just "version" for core. Match group when both sides
		// provide it; permit empty-on-either side to handle informers
		// that strip apiVersion or events that don't set it.
		if group != "" && e.InvolvedObject.APIVersion != "" {
			ig := e.InvolvedObject.APIVersion
			if i := indexByte(ig, '/'); i > 0 {
				ig = ig[:i]
			}
			if ig != "" && ig != group {
				continue
			}
		}
		matched = append(matched, e)
	}
	// Newest-first by lastTimestamp (falls back to eventTime / firstTimestamp
	// for events that don't fill it). Cap to recentEventsCap after sort so
	// we always return the most recent ones.
	sort.SliceStable(matched, func(i, j int) bool {
		return eventTime(matched[i]).After(eventTime(matched[j]))
	})
	if len(matched) > recentEventsCap {
		matched = matched[:recentEventsCap]
	}
	out := make([]gitopsinsights.EventSummary, 0, len(matched))
	for _, e := range matched {
		out = append(out, gitopsinsights.EventSummary{
			Type:               e.Type,
			Reason:             e.Reason,
			Message:            e.Message,
			Count:              e.Count,
			LastTimestamp:      eventTime(e).Format("2006-01-02T15:04:05Z07:00"),
			ReportingComponent: e.ReportingController,
		})
	}
	return out
}

func (r *insightsResolver) namespaceAllowed(namespace string) bool {
	if r.allowedNamespaces == nil {
		return true
	}
	if namespace == "" {
		// Cluster-scoped resources are visible to anyone with any
		// namespace access; gating them on namespace allowlist would
		// hide things like Namespaces, ClusterRoles from every user.
		return true
	}
	for _, ns := range r.allowedNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

// eventTime returns the most useful timestamp from an Event. Modern events
// (eventTime non-zero) prefer that; legacy events fall back to
// lastTimestamp then firstTimestamp.
func eventTime(e *corev1.Event) time.Time {
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.FirstTimestamp.Time
}

// indexByte is a tiny stdlib substitute kept inline so this file doesn't
// pull in strings just for one IndexByte.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
