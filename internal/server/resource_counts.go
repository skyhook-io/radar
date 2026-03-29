package server

import (
	"log"
	"net/http"
	"sync"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type ResourceCountsResponse struct {
	Counts    map[string]int `json:"counts"`
	Forbidden []string       `json:"forbidden,omitempty"`
}

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

	// 1. Typed resources from allKindListers
	for _, kl := range k8score.AllKindListers() {
		l := kl.Lister()(cache.ResourceCache)
		if l == nil {
			forbidden = append(forbidden, kl.CountKey())
			continue
		}
		n := k8score.ListCountNamespaced(l, namespaces)
		if n > 0 {
			counts[kl.CountKey()] = n
		}
	}

	// 2. Dynamic resources (CRDs) — counted concurrently since each Count() hits a separate informer indexer
	discovery := k8s.GetResourceDiscovery()
	dynamicCache := k8s.GetDynamicResourceCache()
	if discovery != nil && dynamicCache != nil {
		resources, err := discovery.GetAPIResources()
		if err != nil {
			log.Printf("[resource-counts] Failed to discover API resources for CRD counts: %v", err)
		} else {
			// Deduplicate CRDs by group+kind
			type crdInfo struct {
				kind       string
				group      string
				namespaced bool
			}
			seen := make(map[string]bool)
			var crds []crdInfo
			for _, res := range resources {
				if !res.IsCRD {
					continue
				}
				key := res.Group + "/" + res.Kind
				if !seen[key] {
					seen[key] = true
					crds = append(crds, crdInfo{kind: res.Kind, group: res.Group, namespaced: res.Namespaced})
				}
			}

			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, crd := range crds {
				wg.Add(1)
				go func(c crdInfo) {
					defer wg.Done()
					gvr, ok := discovery.GetGVRWithGroup(c.kind, c.group)
					if !ok {
						return
					}
					// For cluster-scoped CRDs, skip namespace filtering (same as typed resources)
					ns := namespaces
					if !c.namespaced {
						ns = nil
					}
					n, err := dynamicCache.Count(gvr, ns)
					if err != nil {
						log.Printf("[resource-counts] Failed to count CRD %s/%s: %v", c.group, c.kind, err)
						return
					}
					if n == 0 {
						return
					}
					countKey := c.group + "/" + c.kind
					mu.Lock()
					counts[countKey] = n
					mu.Unlock()
				}(crd)
			}
			wg.Wait()
		}
	}

	s.writeJSON(w, ResourceCountsResponse{
		Counts:    counts,
		Forbidden: forbidden,
	})
}
