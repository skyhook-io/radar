package kagent

import (
	"slices"
	"strings"
)

// AttentionGroup is one row of the workspace's Needs attention: a finding, or the
// ModelConfigs whose API key Secret is the same, with the Agent findings
// folded beneath them. Folding is presentation for one caller: it changes no
// finding, and an Issue is never folded.
type AttentionGroup struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	// Secret: the group's findings are each a ModelConfig's whose API key is
	// this Secret.
	Secret *ObjectRef `json:"secret,omitempty"`
	// Findings head the group; Folded are Agent findings beneath them.
	Findings []string        `json:"findings"`
	Folded   []FoldedFinding `json:"folded,omitempty"`
}

// FoldedFinding is an Agent finding shown under the dependency finding its
// kagent message names as the first cause.
type FoldedFinding struct {
	Finding string `json:"finding"`
	Under   string `json:"under"`
}

// Fold groups the findings one caller receives, after authorization and the
// namespace filter: an Agent finding whose Cause names a dependency with a
// finding of the matching form in findings is shown under it; ModelConfig
// findings about one API key Secret share a group. Anything else, an Agent's
// other findings included, is a group of its own. A group's severity is the
// worst of what it holds, so an Agent that can start no session keeps its
// critical severity under a dependency's warning.
func Fold(findings []ObjectFinding) []AttentionGroup {
	type dependencyKey struct {
		target ObjectRef
		form   string
	}
	dependencies := make(map[dependencyKey]string, len(findings))
	byID := make(map[string]ObjectFinding, len(findings))
	for _, f := range findings {
		byID[f.ID] = f
		if f.Cause == nil {
			key := dependencyKey{f.Subject, formFor(f.Reason)}
			if _, exists := dependencies[key]; !exists {
				dependencies[key] = f.ID
			}
		}
	}
	under := map[string]string{}
	for _, f := range findings {
		if f.Cause == nil {
			continue
		}
		if id, ok := dependencies[dependencyKey{f.Cause.Target, f.Cause.Form}]; ok && id != f.ID {
			under[f.ID] = id
		}
	}
	folded := map[string][]string{}
	for _, f := range findings {
		if lead, ok := under[f.ID]; ok {
			folded[lead] = append(folded[lead], f.ID)
		}
	}
	var groups []AttentionGroup
	secretGroup := map[string]int{}
	for _, f := range findings {
		if _, ok := under[f.ID]; ok {
			continue
		}
		if f.Secret != nil {
			key := f.Secret.Key()
			if at, ok := secretGroup[key]; ok {
				groups[at].Findings = append(groups[at].Findings, f.ID)
				continue
			}
			secret := *f.Secret
			secretGroup[key] = len(groups)
			groups = append(groups, AttentionGroup{ID: "secret:" + key, Secret: &secret, Findings: []string{f.ID}})
			continue
		}
		groups = append(groups, AttentionGroup{ID: f.ID, Findings: []string{f.ID}})
	}
	for i := range groups {
		g := &groups[i]
		g.Severity = SeverityWarning
		for _, id := range g.Findings {
			g.Severity = worse(g.Severity, byID[id].Severity)
			for _, agent := range folded[id] {
				g.Folded = append(g.Folded, FoldedFinding{Finding: agent, Under: id})
				g.Severity = worse(g.Severity, byID[agent].Severity)
			}
		}
	}
	slices.SortStableFunc(groups, func(a, b AttentionGroup) int {
		if a.Severity != b.Severity {
			if a.Severity == SeverityCritical {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	if groups == nil {
		groups = []AttentionGroup{}
	}
	return groups
}

func worse(a, b Severity) Severity {
	if a == SeverityCritical || b == SeverityCritical {
		return SeverityCritical
	}
	return SeverityWarning
}

// Count is a number with whether it is a lower bound: the read behind it was
// partial, so there may be more.
type Count struct {
	N          int  `json:"n"`
	LowerBound bool `json:"lowerBound"`
}

// GroupCount is the number of Needs attention groups. It is no bound: with
// more read, findings now apart can fold together.
type GroupCount struct {
	N          int  `json:"n"`
	Incomplete bool `json:"incomplete"`
}

// Counts are the workspace's numbers for one caller's view.
type Counts struct {
	// Findings and Critical count atomic findings, before any folding.
	Findings Count      `json:"findings"`
	Critical Count      `json:"critical"`
	Groups   GroupCount `json:"groups"`
	Agents   Count      `json:"agents"`
	// AgentsByLatestChange counts Agents by LatestChange.State.
	AgentsByLatestChange map[string]int `json:"agentsByLatestChange"`
	// AgentsAffected counts the distinct Agents a dependency finding affects.
	AgentsAffected     Count `json:"agentsAffected"`
	MCPServers         Count `json:"mcpServers"`
	MCPServersFailing  Count `json:"mcpServersFailing"`
	Models             Count `json:"models"`
	ModelsWithProblems Count `json:"modelsWithProblems"`
}

// CountsOf counts a view. complete reports, per kind, whether every object in
// the view's namespaces was read.
func CountsOf(w *Workspace, groups []AttentionGroup, complete func(kind string) bool) Counts {
	statusKinds := []string{KindAgent, KindAgentTemplate, KindHarness, KindModelConfig, KindRemoteMCPServer, KindSandboxTemplate, KindModelProviderConfig}
	findingsComplete := true
	for _, k := range statusKinds {
		findingsComplete = findingsComplete && complete(k)
	}
	if w.ix != nil {
		findingsComplete = findingsComplete && w.consumersComplete(KindModelConfig)
	}
	for _, a := range w.Agents {
		if a.Incomplete {
			findingsComplete = false
		}
	}
	c := Counts{AgentsByLatestChange: map[string]int{}}
	c.Findings = Count{N: len(w.Findings), LowerBound: !findingsComplete}
	c.Critical.LowerBound = !findingsComplete
	affected := map[string]bool{}
	affectedBound := !findingsComplete
	for _, f := range w.Findings {
		if f.Severity == SeverityCritical {
			c.Critical.N++
		}
		if f.Affects != nil {
			affectedBound = affectedBound || f.Affects.LowerBound
			agents := f.Affects.Agents
			if w.ix != nil {
				agents = w.AffectedAgents(f.Subject, f.Reason)
			}
			for _, a := range agents {
				affected[a.Agent.Key()] = true
			}
		}
	}
	c.AgentsAffected = Count{N: len(affected), LowerBound: affectedBound}
	c.Groups = GroupCount{N: len(groups), Incomplete: !findingsComplete}
	c.Agents = Count{N: len(w.Agents), LowerBound: !complete(KindAgent)}
	for _, a := range w.Agents {
		c.AgentsByLatestChange[a.LatestChange.State]++
	}
	c.MCPServers = Count{N: len(w.MCPServers), LowerBound: !complete(KindRemoteMCPServer)}
	c.MCPServersFailing.LowerBound = c.MCPServers.LowerBound
	for _, s := range w.MCPServers {
		if s.Failing {
			c.MCPServersFailing.N++
		}
	}
	c.Models = Count{N: len(w.Models), LowerBound: !complete(KindModelConfig)}
	c.ModelsWithProblems.LowerBound = c.Models.LowerBound
	withFinding := map[string]bool{}
	for _, f := range w.Findings {
		if f.Subject.Kind == KindModelConfig && f.Subject.Group == Group && f.Reason != ReasonDependencyMissing && f.Reason != ReasonDependencyNotPermitted {
			withFinding[f.Subject.Key()] = true
		}
	}
	c.ModelsWithProblems.N = len(withFinding)
	return c
}
