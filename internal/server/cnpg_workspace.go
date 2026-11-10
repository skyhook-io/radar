package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/readscope"
)

// handleCNPGWorkspace serves GET /api/cnpg/workspace: every CloudNativePG kind
// plus instance Pods, each authorized on its own. The generic resource list
// does not gate namespaced CRDs per kind, so it cannot tell "no access" from
// "none"; this endpoint states which one it is for every kind.
func (s *Server) handleCNPGWorkspace(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	reader := s.cnpgReader(r)
	cache := reader.Observations.Cache
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	s.writeJSON(w, reader.Workspace(r.Context(), cache, namespaces, reader.ClusterContext))
}

func (s *Server) cnpgIssueRows(r *http.Request, namespaces []string) []issues.Issue {
	if readscope.NoNamespaceAccess(namespaces) {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	rows, _ := issues.ComposeWithStats(provider, issues.Filters{Namespaces: namespaces, Limit: issues.NoLimit, CanReadClusterScoped: s.issueClusterScopedAccess(r), CanReadRelated: s.issueRelatedResourceAccess(r), CanReadSubject: s.issueSubjectAccess(r), CanReadEvidence: s.issueEvidenceAccess(r)})
	return rows
}
