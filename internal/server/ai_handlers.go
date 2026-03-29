package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/internal/k8s"
)

// parseVerbosity reads the ?verbosity= query parameter and returns the matching level.
func parseVerbosity(r *http.Request, defaultLevel aicontext.VerbosityLevel) aicontext.VerbosityLevel {
	switch r.URL.Query().Get("verbosity") {
	case "summary":
		return aicontext.LevelSummary
	case "detail":
		return aicontext.LevelDetail
	case "compact":
		return aicontext.LevelCompact
	default:
		return defaultLevel
	}
}

// handleAIListResources returns a minified list of resources for AI consumption.
// GET /api/ai/resources/{kind}?namespace=X&group=X&verbosity=summary|detail|compact
func (s *Server) handleAIListResources(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []any{})
		return
	}
	group := r.URL.Query().Get("group")
	level := parseVerbosity(r, aicontext.LevelSummary)

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Try typed cache first
	objs, err := k8s.FetchResourceList(cache, kind, namespaces)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		s.aiListDynamic(w, r, cache, kind, namespaces, group, level)
		return
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "forbidden:") {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to list %s", kind))
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	results, err := aicontext.MinifyList(objs, level)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, results)
}

// aiListDynamic handles the CRD/dynamic fallback for AI list.
func (s *Server) aiListDynamic(w http.ResponseWriter, r *http.Request, cache *k8s.ResourceCache, kind string, namespaces []string, group string, level aicontext.VerbosityLevel) {
	var allItems []*unstructured.Unstructured

	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			items, err := cache.ListDynamicWithGroup(r.Context(), kind, ns, group)
			if err != nil {
				if strings.Contains(err.Error(), "unknown resource kind") {
					s.writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				s.writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			allItems = append(allItems, items...)
		}
	} else {
		items, err := cache.ListDynamicWithGroup(r.Context(), kind, "", group)
		if err != nil {
			if strings.Contains(err.Error(), "unknown resource kind") {
				s.writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			s.writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		allItems = items
	}

	results := make([]any, 0, len(allItems))
	for _, item := range allItems {
		results = append(results, aicontext.MinifyUnstructured(item, level))
	}

	s.writeJSON(w, results)
}

// handleAIGetResource returns a single minified resource for AI consumption.
// GET /api/ai/resources/{kind}/{namespace}/{name}?group=X&verbosity=summary|detail|compact
func (s *Server) handleAIGetResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := normalizeKind(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	level := parseVerbosity(r, aicontext.LevelDetail)

	// Handle cluster-scoped resources: "_" is used as placeholder for empty namespace
	if namespace == "_" {
		namespace = ""
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Try typed cache first
	obj, err := k8s.FetchResource(cache, kind, namespace, name)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		u, dynErr := cache.GetDynamicWithGroup(r.Context(), kind, namespace, name, group)
		if dynErr != nil {
			if strings.Contains(dynErr.Error(), "unknown resource kind") {
				s.writeError(w, http.StatusBadRequest, dynErr.Error())
				return
			}
			if strings.Contains(dynErr.Error(), "not found") {
				s.writeError(w, http.StatusNotFound, dynErr.Error())
				return
			}
			s.writeError(w, http.StatusInternalServerError, dynErr.Error())
			return
		}
		s.writeJSON(w, aicontext.MinifyUnstructured(u, level))
		return
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "forbidden:") {
			s.writeError(w, http.StatusForbidden, fmt.Sprintf("insufficient permissions to access %s", kind))
			return
		}
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}

	k8s.SetTypeMeta(obj)
	result, err := aicontext.Minify(obj, level)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, result)
}
