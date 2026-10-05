package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/datum"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

type DatumWorkspaceResponse struct {
	Installed  bool                    `json:"installed"`
	Context    string                  `json:"context"`
	Namespaces []string                `json:"namespaces"`
	Coverage   map[string]KindCoverage `json:"coverage"`
	Objects    map[string][]any        `json:"objects"`
	Issues     []issuesapi.Issue       `json:"issues"`
}

// Experimental local reads reuse the workspace coverage contract; shared-user
// policy for credential-bearing Datum resources has not been established.
func (s *Server) handleDatumWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.configManagement() != "local" || s.authConfig.Enabled() {
		s.writeError(w, 403, "Datum workspace is available in local Radar only")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, 503, "Resource cache not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	ns := s.parseNamespacesForUser(r)
	resp := DatumWorkspaceResponse{Context: k8s.GetContextName(), Namespaces: ns, Coverage: map[string]KindCoverage{}, Objects: map[string][]any{}, Issues: []issuesapi.Issue{}}
	disc := k8s.GetResourceDiscovery()
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 6)
	resources := append([]datum.Resource(nil), datum.Resources...)
	resources = append(resources, datum.Resource{Group: "coordination.k8s.io", Version: "v1", Kind: "Lease", Plural: "leases", Namespaced: true}, datum.Resource{Group: "discovery.k8s.io", Version: "v1", Kind: "EndpointSlice", Plural: "endpointslices", Namespaced: true})
	for _, resource := range resources {
		resp.Coverage[resource.Plural] = KindCoverage{State: kindCoverageNotInstalled}
		resp.Objects[resource.Plural] = []any{}
	}
	for _, resource := range resources {
		key := resource.Plural
		if disc == nil {
			mu.Lock()
			resp.Coverage[key] = KindCoverage{State: kindCoverageSyncing}
			mu.Unlock()
			continue
		}
		if _, ok := disc.GetGVRWithGroup(resource.Kind, resource.Group); !ok {
			if !disc.ResourceAbsent(resource.Group, resource.Plural) {
				mu.Lock()
				resp.Coverage[key] = KindCoverage{State: kindCoverageSyncing}
				mu.Unlock()
			}
			continue
		}
		if resource.Group != "coordination.k8s.io" && resource.Group != "discovery.k8s.io" {
			resp.Installed = true
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				resp.Coverage[key] = KindCoverage{State: kindCoverageSyncing}
				mu.Unlock()
				return
			}
			access, items := s.readWorkspaceKind(r, cache, workspaceKind{key: key, group: resource.Group, kind: resource.Kind, resource: resource.Plural, clusterScoped: !resource.Namespaced, observeDynamic: resource.Kind == "Lease" || resource.Kind == "EndpointSlice"}, ns, []string{resource.Group})
			out := make([]any, 0, len(items))
			for _, u := range items {
				out = append(out, u.Object)
			}
			mu.Lock()
			resp.Coverage[key] = access.coverage()
			resp.Objects[key] = out
			mu.Unlock()
		}()
	}
	wg.Wait()
	if provider := issues.NewCacheProvider(); provider != nil {
		found, _ := issues.ComposeWithStats(provider, issues.Filters{Namespaces: ns, Limit: issues.NoLimit})
		for _, iss := range found {
			if _, ok := datum.Lookup(iss.Group, iss.Kind); ok {
				resp.Issues = append(resp.Issues, iss)
			}
		}
	}
	s.writeJSON(w, resp)
}

// This changes Radar's runtime connection, never the user's kubeconfig or the
// project's resources. The scoped client is verified before cache teardown.
func (s *Server) handleDatumProjectConnect(w http.ResponseWriter, r *http.Request) {
	if s.configManagement() != "local" || s.authConfig.Enabled() {
		s.writeError(w, 403, "Project navigation is available in local Radar only")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	var req struct {
		UID             string `json:"uid"`
		ReviewedContext string `json:"reviewedContext"`
	}
	if err := decodeBoundedJSONBody(w, r, 4096, &req); err != nil {
		var large *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &large) {
			status = http.StatusRequestEntityTooLarge
		}
		s.writeError(w, status, err.Error())
		return
	}
	reviewed, binding := k8s.ClusterSafetySnapshot(r.Context())
	if req.UID == "" || req.ReviewedContext != reviewed {
		s.writeErrorCode(w, 409, "context_changed", "Connection changed; reopen the project")
		return
	}
	name := chi.URLParam(r, "name")
	target, err := k8s.RegisterProjectContext(r.Context(), name, req.UID, binding)
	if err != nil {
		if errors.Is(err, k8s.ErrContextSwitchPreflight) {
			s.writeErrorCode(w, 409, "context_changed", err.Error())
		} else {
			s.writeError(w, 400, err.Error())
		}
		return
	}
	if err = k8s.PerformProjectContextSwitch(target, binding); err != nil {
		log.Printf("[datum] Project connection failed for %q: %v", name, err)
		if errors.Is(err, k8s.ErrContextSwitchPreflight) {
			s.writeErrorCode(w, 409, "context_changed", err.Error())
		} else {
			s.writeError(w, 503, err.Error())
		}
		return
	}
	s.writeJSON(w, map[string]string{"context": target, "status": "connected"})
}
