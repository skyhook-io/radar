package server

import (
	"errors"
	"log"
	"net/http"
	"slices"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ResourceCountsResponse struct {
	Counts      map[string]int `json:"counts"`
	Forbidden   []string       `json:"forbidden,omitempty"`
	Unavailable []string       `json:"unavailable,omitempty"`
}

const (
	endpointSliceCountKey          = "discovery.k8s.io/EndpointSlice"
	endpointSliceCountNamespaceCap = 50
	endpointSliceCountConcurrency  = 8
)

func (s *Server) handleResourceCounts(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, ResourceCountsResponse{Counts: map[string]int{}})
		return
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	counts := make(map[string]int)
	var forbidden []string
	var unavailable []string

	countEndpointSlices := func() {
		dynamicCache := k8s.GetDynamicResourceCache()
		if dynamicCache == nil {
			unavailable = append(unavailable, endpointSliceCountKey)
			return
		}
		gvr, ok := k8s.BuiltinGVR("endpointslices", "discovery.k8s.io")
		if !ok {
			unavailable = append(unavailable, endpointSliceCountKey)
			return
		}
		total, err := dynamicCache.CountDirectProbe(r.Context(), gvr, namespaces, endpointSliceCountNamespaceCap, endpointSliceCountConcurrency)
		if err != nil {
			unavailable = append(unavailable, endpointSliceCountKey)
			if !errors.Is(err, k8score.ErrResourceCountUnavailable) {
				log.Printf("[resource-counts] Failed to count EndpointSlice: %v", err)
			}
			return
		}
		counts[endpointSliceCountKey] = total
	}

	for _, kl := range k8score.AllKindListers() {
		l := kl.Lister()(cache.ResourceCache)
		if l == nil {
			forbidden = append(forbidden, kl.CountKey())
			continue
		}
		// Cluster-scoped kinds: ListCountNamespaced ignores the namespace
		// filter and returns the cluster-wide count, so authorize the kind
		// per-user via SAR before counting.
		if k8s.IsClusterOnlyKind(kl.Kind()) {
			group, resource, ok := k8s.ClusterOnlyKindGVR(kl.Kind())
			if !ok {
				continue
			}
			// A core cluster-scoped kind always exists, so an RBAC denial is
			// surfaced as forbidden rather than silently omitted — otherwise the
			// UI shows "0 / No X found", indistinguishable from an empty cluster.
			if !s.canRead(r, group, resource, "", "list") {
				forbidden = append(forbidden, kl.CountKey())
				continue
			}
		}
		n := k8score.ListCountNamespaced(l, namespaces)
		// Namespaces is cluster-scoped but exposed as a filtered list. For
		// namespace-restricted users (non-empty filter), the lister can't
		// honor the filter, so we report the count of namespaces they're
		// allowed to see rather than leaking the cluster-wide total.
		if kl.Kind() == "Namespace" && len(namespaces) > 0 {
			n = len(namespaces)
		}
		counts[kl.CountKey()] = n
	}

	// 2. Dynamic resources (CRDs) — report counts only for already-watched informers.
	discovery := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if discovery != nil && dynamicCache != nil {
		resources, err := discovery.GetAPIResources()
		if err != nil {
			log.Printf("[resource-counts] Failed to discover API resources for CRD counts: %v", err)
		} else {
			// Deduplicate CRDs by group+kind, keeping the most stable served version.
			type crdInfo struct {
				kind       string
				group      string
				resource   string
				version    string
				namespaced bool
				gvr        schema.GroupVersionResource
			}
			seen := make(map[string]bool)
			crds := make(map[string]crdInfo)
			var order []string
			for _, res := range resources {
				if !res.IsCRD {
					continue
				}
				// Informer-backed counts only work for listable+watchable kinds.
				// Create-only review resources (LocalSubjectAccessReview, etc.)
				// never sync an informer and would log a permanent count error.
				if !slices.Contains(res.Verbs, "list") || !slices.Contains(res.Verbs, "watch") {
					continue
				}
				key := res.Group + "/" + res.Kind
				if !seen[key] {
					seen[key] = true
					order = append(order, key)
					crds[key] = crdInfo{
						kind:       res.Kind,
						group:      res.Group,
						resource:   res.Name,
						version:    res.Version,
						namespaced: res.Namespaced,
						gvr:        schema.GroupVersionResource{Group: res.Group, Version: res.Version, Resource: res.Name},
					}
				} else if k8score.IsMoreStableVersion(res.Version, crds[key].version) {
					crds[key] = crdInfo{
						kind:       res.Kind,
						group:      res.Group,
						resource:   res.Name,
						version:    res.Version,
						namespaced: res.Namespaced,
						gvr:        schema.GroupVersionResource{Group: res.Group, Version: res.Version, Resource: res.Name},
					}
				}
			}

			watchedCounts := dynamicCache.CountWatched(namespaces)
			clusterScopedWatchedCounts := watchedCounts
			if len(namespaces) > 0 {
				clusterScopedWatchedCounts = dynamicCache.CountWatched(nil)
			}
			for _, key := range order {
				crd := crds[key]
				countSource := watchedCounts
				if !crd.namespaced {
					if !s.canRead(r, crd.group, crd.resource, "", "list") {
						continue
					}
					countSource = clusterScopedWatchedCounts
				}
				if n, ok := countSource[crd.gvr]; ok {
					counts[key] = n
					continue
				}
				unavailable = append(unavailable, key)
			}
		}
	}
	countEndpointSlices()

	s.writeJSON(w, ResourceCountsResponse{
		Counts:      counts,
		Forbidden:   forbidden,
		Unavailable: unavailable,
	})
}
