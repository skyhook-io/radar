package server

import (
	"net/http"
)

// handleCNPGOperator serves GET /api/cnpg/operator: the operator and plugin
// Deployments, their versions and readiness, and where the operator's
// configuration lives.
//
// The operator runs in its own namespace (cnpg-system by default) while
// people filter the view to their application namespaces. Following the view
// filter would report "no operator" to anyone looking at their databases, so
// scope follows permission here, as it does for the catalog reverse lookups.
func (s *Server) handleCNPGOperator(w http.ResponseWriter, r *http.Request) {
	resp, err := s.cnpgReader(r).Operator(r.Context())
	if err != nil {
		s.writeCNPGCachedReadError(w, err, "", "operator")
		return
	}
	s.writeJSON(w, resp)
}
