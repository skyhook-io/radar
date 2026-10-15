package server

import (
	"net/http"
	"sort"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// typedKindScope resolves where the caller may list a typed kind and which of
// those namespaces Radar's informer actually holds. The informer may itself be
// namespace-scoped when Radar's own identity cannot list the kind
// cluster-wide; what it does not hold is unread, not empty, and not denied.
// read is nil for "every namespace".
func (s *Server) typedKindScope(r *http.Request, cache *k8s.ResourceCache, namespaces []string, group, resource string) (acc integration.KindAccess, read []string) {
	return s.typedKindScopeWithCandidates(r, cache, namespaces, group, resource, allNamespaceNames)
}

func (s *Server) typedKindScopeWithCandidates(r *http.Request, cache *k8s.ResourceCache, namespaces []string, group, resource string, candidates func() []string) (acc integration.KindAccess, read []string) {
	allowed, denied, partial, ok := s.listScopeWithCandidates(r, namespaces, group, resource, candidates)
	if !ok {
		return integration.KindAccess{State: integration.KindCoverageDenied}, []string{}
	}
	assessment := cnpgsvc.AssessKindRead(cache, resource, allowed)
	var uncached []string
	if namespaces != nil {
		uncached = append(uncached, assessment.Uncovered...)
		if assessment.Readiness == k8score.KindUnavailable {
			uncached = append([]string(nil), allowed...)
		}
		sort.Strings(uncached)
	}
	if len(assessment.Uncovered) > 0 && len(assessment.Uncovered) == len(allowed) {
		return integration.KindAccess{State: integration.KindCoverageUncached, Denied: denied, Uncached: uncached}, []string{}
	}
	switch assessment.Readiness {
	case k8score.KindPending:
		return integration.KindAccess{State: integration.KindCoverageSyncing, Denied: denied, Uncached: uncached}, []string{}
	case k8score.KindFailed:
		return integration.KindAccess{State: integration.KindCoverageError, Denied: denied, Uncached: uncached}, []string{}
	case k8score.KindUnavailable:
		return integration.KindAccess{State: integration.KindCoverageUncached, Denied: denied, Uncached: uncached}, []string{}
	}
	if !assessment.Complete && len(assessment.Read) == 0 {
		return integration.KindAccess{State: integration.KindCoverageUncached, Denied: denied, Uncached: uncached}, []string{}
	}
	acc = integration.AccessFromScope(assessment.Read, partial || !assessment.Complete)
	acc.Denied, acc.Uncached = denied, uncached
	return acc, assessment.Read
}
