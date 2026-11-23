package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	"github.com/skyhook-io/radar/pkg/kagent"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

type kagentSessionSummaryRow struct {
	model.KagentAgentSessions
	Crashed               int                   `json:"crashed"`
	OnNewSessionsRevision int                   `json:"onNewSessionsRevision"`
	OnOtherRevisions      int                   `json:"onOtherRevisions"`
	Placement             model.KagentPlacement `json:"placement"`
}

type kagentSessionSummaryResponse struct {
	APITier  substrateapi.APITier `json:"apiTier"`
	Coverage struct {
		*substrateapi.SnapshotCoverage
		KagentAgents integration.KindCoverage `json:"kagentAgents"`
	} `json:"coverage"`
	Rows       []kagentSessionSummaryRow `json:"rows"`
	Total      int                       `json:"total"`
	LowerBound bool                      `json:"lowerBound"`
	Limit      int                       `json:"limit"`
	Offset     int                       `json:"offset"`
}

// handleSubstrateKagentSummary uses exactly the sessions endpoint's API-tier
// gate and caller's Agents list. It uses only the bounded shared snapshot;
// partial actors/templates stay lower bounds, with no per-Agent API fan-out
// or log reads. Placement is for latestSuccessfulRevision's actual template,
// never a Harness from a newer, possibly blocked edit.
func (s *Server) handleSubstrateKagentSummary(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	var operation context.Context
	if !k8s.CaptureClusterReads(func() { operation = k8s.OperationContext() }) || operation == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster connection is changing; try again")
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	if !snap.Coverage.Actors.Usable() {
		s.writeErrorCode(w, http.StatusServiceUnavailable, snap.Coverage.Actors.Code, "Radar could not read Substrate's actors")
		return
	}
	agents := s.substrateKagent(r)
	if !agents.Served {
		s.writeErrorCode(w, http.StatusNotFound, substrateapi.CodeNotFound, "this cluster does not serve kagent's Agents (api.kagent.dev)")
		return
	}
	if agents.Access.State == integration.KindCoverageDenied {
		s.writeError(w, http.StatusForbidden, "no access to list kagent Agents")
		return
	}
	resp := kagentSessionSummaryResponse{APITier: tier, Rows: []kagentSessionSummaryRow{}, Total: len(agents.Agents), LowerBound: agents.Access.State != integration.KindCoverageFull,
		Limit: boundedInt(r.URL.Query().Get("limit"), 1000, 1, 1000), Offset: boundedInt(r.URL.Query().Get("offset"), 0, 0, substrateapi.MaxActors)}
	resp.Coverage.SnapshotCoverage, resp.Coverage.KagentAgents = &snap.Coverage, agents.Access.Coverage()
	ix := agents.index(snap)
	byTemplate, prefixes := snap.Index().ByTemplate, snap.Index().KagentTemplates
	rc := model.NewResumeContext(templateUIDs(snap), snap.Workers, snap.Coverage.Workers.State == substrateapi.CoverageFull)
	sorted := slices.Clone(agents.Agents)
	slices.SortFunc(sorted, func(a, b model.KagentAgent) int { return strings.Compare(a.Ref().Key(), b.Ref().Key()) })
	templates := map[model.Ref]model.Template{}
	for _, t := range snap.Templates {
		templates[t.Ref()] = t
	}
	for _, a := range sorted[min(resp.Offset, len(sorted)):min(resp.Offset+resp.Limit, len(sorted))] {
		complete := agents.Access.Covers(a.Namespace) && !snapshotMissesAtespace(snap.Coverage.Actors, a.Namespace)
		sessions := ix.AgentSessions(a, func(t model.Ref) []model.Actor { return byTemplate[t] }, prefixes[model.KagentPrefixKey(a.Namespace, model.KagentTemplatePrefix(a.Name))], complete, time.Now(), rc)
		if sessions.HintedEarlierRevisions > 0 && snap.Coverage.Templates.State != substrateapi.CoverageFull {
			sessions.Complete = false
		}
		for i := range sessions.Attention {
			sessions.Attention[i].Reason = kagent.RedactURLsInText(sessions.Attention[i].Reason)
		}
		row := kagentSessionSummaryRow{KagentAgentSessions: sessions, Crashed: sessions.Sessions.ByState[model.StateCrashed], Placement: model.KagentPlacement{State: "unknown", Reading: "The prepared template was not read.", Pools: []model.PoolRef{}}}
		if a.LatestSuccessfulRevision == "" {
			row.Placement.Reading = "No revision has been prepared for new sessions."
		}
		for _, rev := range sessions.Revisions {
			if rev.NewSessions {
				row.OnNewSessionsRevision += rev.Sessions
			}
		}
		row.OnOtherRevisions = sessions.Sessions.Total - row.OnNewSessionsRevision
		ref := model.Ref{Atespace: a.Namespace, Name: model.KagentRevisionTemplate(a.Name, a.LatestSuccessfulRevision)}
		if t, found := templates[ref]; found {
			attr := ix.Template(ref)
			if attr.Kind == model.KagentAgentSession && attr.Agent != nil && *attr.Agent == a.Ref() {
				row.Placement = model.KagentTemplatePlacement(t, snap.Workers, snap.Coverage.Workers.State == substrateapi.CoverageFull)
			} else {
				row.Placement.Template = &ref
				row.Placement.Reading = "The prepared template could not be attributed to this Agent."
			}
		}
		resp.Rows = append(resp.Rows, row)
	}
	if operation.Err() != nil {
		s.writeErrorCode(w, http.StatusServiceUnavailable, substrateapi.CodeContextChanged, "cluster connection changed; try again")
		return
	}
	s.writeJSON(w, resp)
}
