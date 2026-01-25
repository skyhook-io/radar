package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
	"github.com/skyhook-io/skyhook-explorer/internal/timeline"
)

// handleTimeline returns timeline events with optional grouping
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	namespace := r.URL.Query().Get("namespace")
	groupBy := r.URL.Query().Get("group_by")
	filterPreset := r.URL.Query().Get("filter")
	sinceStr := r.URL.Query().Get("since")
	untilStr := r.URL.Query().Get("until")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	includeManaged := r.URL.Query().Get("include_managed") == "true"
	includeK8sEvents := r.URL.Query().Get("include_k8s_events") != "false"

	// Parse since timestamp
	var since time.Time
	if sinceStr != "" {
		if ts, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = ts
		}
	}

	// Parse until timestamp
	var until time.Time
	if untilStr != "" {
		if ts, err := time.Parse(time.RFC3339, untilStr); err == nil {
			until = ts
		}
	}

	// Parse limit (default 200)
	limit := 200
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
			if limit > 10000 {
				limit = 10000
			}
		}
	}

	// Parse offset
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// Parse grouping mode
	var groupMode timeline.GroupingMode
	switch groupBy {
	case "owner":
		groupMode = timeline.GroupByOwner
	case "app":
		groupMode = timeline.GroupByApp
	case "namespace":
		groupMode = timeline.GroupByNamespace
	default:
		groupMode = timeline.GroupByNone
	}

	// Default filter preset
	if filterPreset == "" {
		filterPreset = "default"
	}

	// Build query options
	opts := timeline.QueryOptions{
		Namespace:        namespace,
		Since:            since,
		Until:            until,
		Limit:            limit,
		Offset:           offset,
		GroupBy:          groupMode,
		FilterPreset:     filterPreset,
		IncludeManaged:   includeManaged,
		IncludeK8sEvents: includeK8sEvents,
	}

	// Get the timeline store
	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not initialized")
		return
	}

	// Query the store
	response, err := store.QueryGrouped(r.Context(), opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, response)
}

// handleTimelineFilters returns available filter presets
func (s *Server) handleTimelineFilters(w http.ResponseWriter, r *http.Request) {
	presets := timeline.DefaultFilterPresets()
	s.writeJSON(w, presets)
}

// handleTimelineStats returns statistics about the timeline store
func (s *Server) handleTimelineStats(w http.ResponseWriter, r *http.Request) {
	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not initialized")
		return
	}

	stats := store.Stats()
	s.writeJSON(w, stats)
}

// handleTimelineChildren returns child resource events for a given parent workload
func (s *Server) handleTimelineChildren(w http.ResponseWriter, r *http.Request) {
	ownerKind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	ownerName := chi.URLParam(r, "name")
	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")

	var since time.Time
	if sinceStr != "" {
		if ts, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = ts
		}
	} else {
		// Default to last hour
		since = time.Now().Add(-1 * time.Hour)
	}

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not initialized")
		return
	}

	children, err := store.GetChangesForOwner(r.Context(), ownerKind, namespace, ownerName, since, limit)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, children)
}

// lookupK8sEventOwner tries to resolve the owner for a K8s Event
func lookupK8sEventOwner(cache *k8s.ResourceCache, e *corev1.Event) *timeline.OwnerInfo {
	if e.InvolvedObject.Kind == "Pod" {
		if pod, podErr := cache.Pods().Pods(e.Namespace).Get(e.InvolvedObject.Name); podErr == nil && pod != nil {
			for _, ref := range pod.OwnerReferences {
				if ref.Controller != nil && *ref.Controller {
					return &timeline.OwnerInfo{
						Kind: ref.Kind,
						Name: ref.Name,
					}
				}
			}
		}
	} else if e.InvolvedObject.Kind == "ReplicaSet" {
		if rs, rsErr := cache.ReplicaSets().ReplicaSets(e.Namespace).Get(e.InvolvedObject.Name); rsErr == nil && rs != nil {
			for _, ref := range rs.OwnerReferences {
				if ref.Controller != nil && *ref.Controller {
					return &timeline.OwnerInfo{
						Kind: ref.Kind,
						Name: ref.Name,
					}
				}
			}
		}
	}
	return nil
}
