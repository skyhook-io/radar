package server

import (
	"net/http"

	cnpgsvc "github.com/skyhook-io/radar/internal/cnpg"
)

// handleCNPGOperatorStatus serves GET /api/cnpg/operator/status?namespaces=a,b:
// for each namespace, whether the operator that watches it is leading and
// whether its admission webhook can answer. Cheap enough for the fleet and
// cluster pages; the Operator screen has the full diagnosis.
func (s *Server) handleCNPGOperatorStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	resp := cnpgsvc.CNPGOperatorStatusResponse{Namespaces: map[string]cnpgsvc.CNPGOperatorVerdict{}}
	wanted := parseNamespaces(r.URL.Query())
	if len(wanted) == 0 {
		s.writeJSON(w, resp)
		return
	}
	resp = s.cnpgReader(r).OperatorStatus(r.Context(), wanted)
	s.writeJSON(w, resp)
}
