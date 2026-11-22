package server

import (
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	substrateapi "github.com/skyhook-io/radar/internal/substrate"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

// kagent runs every Agent's chat sessions as Substrate actors and links them
// only by name: an Agent revision's template is named for the Agent and the
// revision, and Substrate holds no label or annotation naming either. Which
// Agent an actor belongs to therefore depends on the Agents the caller can
// read, so it is joined per request (model.KagentIndex) from a read of
// Agents as the caller, and never stored in the shared Substrate read.
var kagentAgentKind = integration.WorkspaceKind{Key: "agents", Group: model.KagentGroup, Kind: "Agent", Resource: "agents"}

// substrateKagentRead is one request's read of kagent's Agents.
type substrateKagentRead struct {
	// Served: the cluster serves kagent's Agent kind. Without it nothing is
	// read and no actor is attributed.
	Served bool
	Agents []model.KagentAgent
	Access integration.KindAccess
}

// index matches actors against the Agents read; namespaces whose Agents
// were not all read stay unknown (agentNotReadable). templates are the
// templates read, whose creation times prove an earlier revision's.
func (k substrateKagentRead) index(templates []model.Template) *model.KagentIndex {
	return model.NewKagentIndex(k.Agents, k.Access.Covers, templates)
}

// coverage is the Agents' coverage for a response; nil when kagent is not
// served.
func (k substrateKagentRead) coverage() *integration.KindCoverage {
	if !k.Served {
		return nil
	}
	c := k.Access.Coverage()
	return &c
}

// substrateKagentAgents reads kagent's Agents in namespaces as the caller
// (nil: every namespace the caller may read), through the workspace coverage
// machinery: a namespace the caller may not list, or Radar does not hold, is
// unread, never "no Agents". Tests replace it.
var substrateKagentAgents = func(s *Server, r *http.Request, namespaces []string) substrateKagentRead {
	if !substrateKagentServed() {
		return substrateKagentRead{}
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return substrateKagentRead{Served: true, Access: integration.KindAccess{State: integration.KindCoverageSyncing}}
	}
	budget := newSyncBudget(r.Context())
	// Only the fields below are kept, and the objects are not changed.
	budget.readOnly = true
	access, items := s.readWorkspaceKind(r, cache, kagentAgentKind, namespaces, []string{model.KagentGroup}, budget)
	agents := make([]model.KagentAgent, 0, len(items))
	for _, u := range items {
		agents = append(agents, kagentAgentOf(u))
	}
	return substrateKagentRead{Served: true, Agents: agents, Access: access}
}

func kagentAgentOf(u *unstructured.Unstructured) model.KagentAgent {
	desired, _, _ := unstructured.NestedString(u.Object, "status", "desiredRevision")
	latest, _, _ := unstructured.NestedString(u.Object, "status", "latestSuccessfulRevision")
	return model.KagentAgent{Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()), DesiredRevision: desired, LatestSuccessfulRevision: latest,
		Created: u.GetCreationTimestamp().UTC()}
}

// substrateKagent reads the Agents the caller may read in every namespace,
// for the lists that attribute actors and templates.
func (s *Server) substrateKagent(r *http.Request) substrateKagentRead {
	return substrateKagentAgents(s, r, s.parseNamespacesForUser(r))
}

// kagentRow attributes one row's actor; nil for an actor that is not a
// kagent session, or when kagent is not served.
func kagentRow(ix *model.KagentIndex, a model.Actor) *model.KagentActor {
	if ix == nil {
		return nil
	}
	return ix.Actor(a)
}

// substrateKagentSessionsResponse is GET
// /api/substrate/kagent/agents/{namespace}/{name}/sessions.
type substrateKagentSessionsResponse struct {
	APITier substrateapi.APITier `json:"apiTier"`
	model.KagentAgentSessions
	// Sources says what the actors came from: snapshot (the shared read) or
	// atespace (a direct read of the Agent's atespace, when the shared read
	// does not hold all of it).
	Sources  []string `json:"sources"`
	Coverage struct {
		// Actors is the coverage of the source that answered.
		Actors       substrateapi.Coverage    `json:"actors"`
		KagentAgents integration.KindCoverage `json:"kagentAgents"`
	} `json:"coverage"`
}

// handleSubstrateKagentAgentSessions serves the sessions summary for one
// kagent Agent: its attributed session actors by state, the first few that
// need attention, its current revisions' templates, and how many sessions
// run on templates named for it but for no current revision (a hint, never
// counted). It needs Substrate's API (substrateGate) and the caller's read of
// Agents in the Agent's namespace: a list, since an Agent is attributed only
// when no other Agent there produces the same template name.
func (s *Server) handleSubstrateKagentAgentSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	snap, tier, r, ok := s.substrateSnapshot(w, r)
	if !ok {
		return
	}
	ref := model.AgentRef{Namespace: chi.URLParam(r, "namespace"), Name: chi.URLParam(r, "name")}
	if failure := outsideNamespacePin(ref.Namespace); failure != nil {
		s.writeReadFailure(w, "agents", failure, nil)
		return
	}
	if !substrateKagentServed() {
		s.writeErrorCode(w, http.StatusNotFound, substrateapi.CodeNotFound, "this cluster does not serve kagent's Agents (api.kagent.dev)")
		return
	}
	namespaces := s.getUserNamespaces(r, []string{ref.Namespace})
	if readscope.NoNamespaceAccess(namespaces) {
		s.writeError(w, http.StatusForbidden, "no access to kagent Agents in namespace "+ref.Namespace)
		return
	}
	agents := substrateKagentAgents(s, r, []string{ref.Namespace})
	if !agents.Access.Covers(ref.Namespace) {
		if agents.Access.State == integration.KindCoverageDenied || slices.Contains(agents.Access.Denied, ref.Namespace) {
			s.writeError(w, http.StatusForbidden, "no access to list kagent Agents in namespace "+ref.Namespace)
			return
		}
		s.writeErrorCode(w, http.StatusServiceUnavailable, substrateapi.CodeIncomplete, "Radar has not read kagent's Agents in namespace "+ref.Namespace+" ("+agents.Access.State+")")
		return
	}
	ix := agents.index(snap.Templates)
	agent, found := ix.Agent(ref)
	if !found {
		s.writeErrorCode(w, http.StatusNotFound, substrateapi.CodeNotFound, "no kagent Agent "+ref.Key())
		return
	}

	resp := substrateKagentSessionsResponse{APITier: tier, Sources: []string{"snapshot"}}
	resp.Coverage.Actors, resp.Coverage.KagentAgents = snap.Coverage.Actors, agents.Access.Coverage()
	byTemplate, prefixes := snap.Index().ByTemplate, snap.Index().KagentTemplates
	complete := !snapshotMissesAtespace(snap.Coverage.Actors, ref.Namespace)
	if !complete {
		direct, cov, tierErr := s.substrateManager().AtespaceActors(r.Context(), ref.Namespace)
		if tierErr != nil {
			s.writeSubstrateTier(w, *tierErr)
			return
		}
		resp.Coverage.Actors, resp.Sources = cov, []string{"atespace"}
		byTemplate = model.GroupByTemplate(sortedNewest(direct))
		refs := make([]model.Ref, 0, len(byTemplate))
		for t := range byTemplate {
			refs = append(refs, t)
		}
		prefixes = model.KagentTemplatesByPrefix(refs)
		complete = cov.State == substrateapi.CoverageFull
	}
	if !resp.Coverage.Actors.Usable() {
		s.writeErrorCode(w, http.StatusServiceUnavailable, resp.Coverage.Actors.Code, "Radar could not read Substrate's actors in atespace "+ref.Namespace)
		return
	}
	now := time.Now()
	rc := model.NewResumeContext(templateUIDs(snap), snap.Workers, snap.Coverage.Workers.State == substrateapi.CoverageFull)
	resp.KagentAgentSessions = ix.AgentSessions(agent, func(t model.Ref) []model.Actor { return byTemplate[t] },
		prefixes[model.KagentPrefixKey(ref.Namespace, model.KagentTemplatePrefix(ref.Name))], complete, now, rc)
	s.writeJSON(w, resp)
}
