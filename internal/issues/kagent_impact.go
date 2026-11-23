package issues

import (
	"cmp"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/kagent"
)

// factKagentAffectedAgents lists the Agents a ModelConfig's or
// RemoteMCPServer's Issue affects. Like every affected-resources fact, its
// links are redacted to what the caller can read and no count describes the
// rest.
const factKagentAffectedAgents = "kagent_affected_agents"

var kagentAffectedAgentsMessage = map[string]string{
	kagent.KindModelConfig:     "kagent Agents whose latest change uses this ModelConfig: as their model, a subagent's, or their Harness's summarizer or memory model.",
	kagent.KindRemoteMCPServer: "kagent Agents whose latest change binds this server's tools.",
}

// The resources an Agent's use of a dependency is read from, by the kind it
// passes through.
var kagentEvidenceResources = map[string]string{
	kagent.KindAgent:         "agents",
	kagent.KindAgentTemplate: "agenttemplates",
	kagent.KindHarness:       "harnesses",
}

// enrichKagentImpact adds to each kagent ModelConfig's and RemoteMCPServer's
// Issue the Agents whose latest change uses it, through pkg/kagent's
// composition of the request's Agents, AgentTemplates and Harnesses. It runs
// after the per-object detector, whose Issues it never folds or rewrites: an
// Agent keeps its own Issue, critical included. An Agent is added only when
// the caller can make the reads its use rests on (its Agents list, and the
// template or Harness the use passes through); a caller who can make none
// gets the dependency's Issue unchanged. AffectedRefs hold every such Agent
// for related-issue lookup; the fact's links are capped.
func enrichKagentImpact(in []Issue, p Provider, f Filters) []Issue {
	var deps []int
	for i, iss := range in {
		if iss.Group == kagent.Group && kagentAffectedAgentsMessage[iss.Kind] != "" {
			deps = append(deps, i)
		}
	}
	if len(deps) == 0 || f.CanReadEvidence == nil && !f.AllowUnfilteredEvidence {
		return in
	}
	canRead := func(read EvidenceRead) bool {
		if f.CanReadEvidence == nil {
			return f.AllowUnfilteredEvidence
		}
		return f.CanReadEvidence(read)
	}
	inv, ok := kagentImpactInventory(p, f.Namespaces, canRead)
	if !ok {
		return in
	}
	ws := kagent.Compose(inv)
	for _, i := range deps {
		iss := in[i]
		target := kagent.ObjectRef{Group: kagent.Group, Kind: iss.Kind, Namespace: iss.Namespace, Name: iss.Name}
		var refs []Ref
		reads := map[EvidenceRead]bool{}
		for _, a := range ws.AffectedAgents(target, iss.Reason) {
			if required, ok := kagentUseReads(a, canRead); ok {
				for _, read := range required {
					reads[read] = true
				}
				refs = append(refs, Ref{Group: kagent.Group, Kind: kagent.KindAgent, Namespace: a.Agent.Namespace, Name: a.Agent.Name})
			}
		}
		if len(refs) == 0 {
			continue
		}
		slices.SortFunc(refs, func(a, b Ref) int { return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name)) })
		in[i].Degraded = &DegradedFinding{Message: iss.Message, Cause: iss.Cause, Action: iss.Action, KeepSubjectOnset: true}
		for read := range reads {
			in[i].RequiredReads = append(in[i].RequiredReads, read)
		}
		in[i].AffectedRefs = append(slices.Clone(iss.AffectedRefs), refs...)
		in[i].DiagnosticContext = withFact(iss.DiagnosticContext, affectedRefsFact(factKagentAffectedAgents, kagentAffectedAgentsMessage[iss.Kind], refs))
	}
	return in
}

func kagentUseReads(a kagent.AffectedAgent, canRead func(EvidenceRead) bool) ([]EvidenceRead, bool) {
	var reads []EvidenceRead
	for _, kind := range append([]string{kagent.KindAgent}, a.Through...) {
		resource := kagentEvidenceResources[kind]
		read := EvidenceRead{Group: kagent.Group, Resource: resource, Namespace: a.Agent.Namespace, Verb: "list", Exact: true}
		if resource == "" || !canRead(read) {
			return nil, false
		}
		reads = append(reads, read)
	}
	return reads, true
}

// kagentImpactInventory lists, in the compose's namespaces (nil: every watched
// scope), the kinds an Agent's use of a dependency is read from. The Issues
// add only Agents positively read and claim no completeness, so a namespace
// that failed or is still syncing only leaves Agents out. ok is false when
// Radar watches no kagent Agents.
func kagentImpactInventory(p Provider, namespaces []string, canRead func(EvidenceRead) bool) (kagent.Inventory, bool) {
	gvrs := map[string]schema.GroupVersionResource{}
	for _, gvr := range p.WatchedDynamic() {
		if gvr.Group != kagent.Group {
			continue
		}
		switch kind := p.KindForGVR(gvr); kind {
		case kagent.KindAgent, kagent.KindAgentTemplate, kagent.KindHarness, kagent.KindModelConfig, kagent.KindRemoteMCPServer:
			gvrs[kind] = gvr
		}
	}
	if _, ok := gvrs[kagent.KindAgent]; !ok {
		return kagent.Inventory{}, false
	}
	inv := kagent.Inventory{Now: time.Now(), Kinds: map[string]kagent.Objects{}}
	for kind, gvr := range gvrs {
		var items []*unstructured.Unstructured
		read := map[string]bool{}
		all := false
		if namespaces == nil {
			list, err := p.ListDynamicAllNamespaces(gvr)
			if err == nil {
				items, all = list, true
			}
		} else {
			for _, ns := range namespaces {
				list, err := p.ListDynamic(gvr, ns)
				if err != nil {
					continue
				}
				items = append(items, list...)
				read[ns] = true
			}
		}
		items = slices.DeleteFunc(slices.Clone(items), func(u *unstructured.Unstructured) bool {
			return !canRead(EvidenceRead{Group: kagent.Group, Resource: gvr.Resource, Namespace: u.GetNamespace(), Verb: "list", Exact: true})
		})
		inv.Kinds[kind] = kagent.Objects{Items: items, Read: func(ns string) bool { return all || read[ns] }}
	}
	return inv, true
}

func stripKagentImpact(issue Issue) Issue {
	issue.AffectedRefs = nil
	if issue.DiagnosticContext != nil {
		dc := *issue.DiagnosticContext
		dc.Facts = slices.DeleteFunc(slices.Clone(dc.Facts), func(f issuesapi.DiagnosticFact) bool { return f.Type == factKagentAffectedAgents })
		if len(dc.Facts) == 0 {
			issue.DiagnosticContext = nil
		} else {
			issue.DiagnosticContext = &dc
		}
	}
	return issue
}
