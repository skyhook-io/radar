package kagent

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type impactWantCause struct {
	Target, Role, Form string
}

type impactWantFinding struct {
	ID              string           `json:"id"`
	Severity        Severity         `json:"severity"`
	Cause           *impactWantCause `json:"cause"`
	Secret          string           `json:"secret"`
	MessageContains string           `json:"messageContains"`
	Affects         *struct {
		Agents     map[string]string `json:"agents"`
		Count      int               `json:"count"`
		LowerBound bool              `json:"lowerBound"`
		Consumers  string            `json:"consumers"`
	} `json:"affects"`
	Through map[string][]string `json:"through"`
}

type impactWantRef struct {
	Role          string `json:"role"`
	Target        string `json:"target"`
	State         string `json:"state"`
	Unread        string `json:"unread"`
	PolicyUnknown *bool  `json:"policyUnknown"`
	Fallback      bool   `json:"fallback"`
	Tools         []string
}

type impactVector struct {
	Name            string                       `json:"name"`
	Now             time.Time                    `json:"now"`
	Objects         []unstructured.Unstructured  `json:"objects"`
	Unread          map[string]string            `json:"unread"`
	NamespaceLabels map[string]map[string]string `json:"namespaceLabels"`
	Want            struct {
		Findings []impactWantFinding `json:"findings"`
		Groups   []struct {
			Findings []string   `json:"findings"`
			Folded   [][]string `json:"folded"`
			Severity Severity   `json:"severity"`
			Secret   string     `json:"secret"`
		} `json:"groups"`
		Agents map[string]struct {
			LatestChange        string   `json:"latestChange"`
			NewSessionsRevision *string  `json:"newSessionsRevision"`
			DesiredRevision     *string  `json:"desiredRevision"`
			Cycle               []string `json:"cycle"`
			MCPFailing          *int     `json:"mcpFailing"`
			ToolsNotOffered     []string `json:"toolsNotOffered"`
		} `json:"agents"`
		References map[string][]impactWantRef `json:"references"`
		Models     map[string]struct {
			Consumers string         `json:"consumers"`
			Count     int            `json:"count"`
			ByRole    map[string]int `json:"byRole"`
		} `json:"models"`
		MCPServers map[string]struct {
			Failing           bool                `json:"failing"`
			ToolsOfferedKnown bool                `json:"toolsOfferedKnown"`
			AskedNotOffered   map[string][]string `json:"askedNotOffered"`
		} `json:"mcpServers"`
		Counts struct {
			Findings       *Count `json:"findings"`
			Groups         *int   `json:"groups"`
			AgentsAffected *Count `json:"agentsAffected"`
		} `json:"counts"`
	} `json:"want"`
}

// inventoryOf reads every kind in full in every namespace, except a kind the
// vector names as unread, which reads nothing with that coverage state.
func inventoryOf(v impactVector) Inventory {
	inv := Inventory{Now: v.Now, Kinds: map[string]Objects{}}
	for _, kind := range Kinds {
		inv.Kinds[kind] = Objects{Read: func(string) bool { return true }, Complete: true, State: "full"}
	}
	for i := range v.Objects {
		u := &v.Objects[i]
		objs := inv.Kinds[u.GetKind()]
		objs.Items = append(objs.Items, u)
		inv.Kinds[u.GetKind()] = objs
	}
	for kind, state := range v.Unread {
		inv.Kinds[kind] = Objects{Read: func(string) bool { return false }, State: state}
	}
	if v.NamespaceLabels != nil {
		inv.NamespaceLabels = func(ns string) (map[string]string, bool) {
			l, ok := v.NamespaceLabels[ns]
			return l, ok
		}
	}
	return inv
}

func agentKey(r ObjectRef) string { return r.Namespace + "/" + r.Name }

func TestImpactDoesNotJoinMCPServersFromAnotherAPIGroup(t *testing.T) {
	for _, field := range []string{"apiGroup", "group"} {
		t.Run(field, func(t *testing.T) {
			for _, group := range []string{Group, "other.example.com"} {
				serverRef := map[string]any{"name": "tools", "kind": KindRemoteMCPServer, field: group}
				a := unstructured.Unstructured{Object: map[string]any{
					"apiVersion": Group + "/v1alpha3", "kind": KindAgent,
					"metadata": map[string]any{"namespace": "agents", "name": "helper"},
					"spec":     map[string]any{"template": map[string]any{"inline": map[string]any{"tools": []any{map[string]any{"mcp": map[string]any{"server": serverRef}}}}}},
				}}
				server := unstructured.Unstructured{Object: map[string]any{
					"apiVersion": Group + "/v1alpha3", "kind": KindRemoteMCPServer,
					"metadata": map[string]any{"namespace": "agents", "name": "tools"},
					"status":   map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "message": "connection refused"}}},
				}}
				ws := Compose(inventoryOf(impactVector{Now: time.Now(), Objects: []unstructured.Unstructured{a, server}}))
				want := 0
				if group == Group {
					want = 1
				}
				if ws.MCPServers[0].UsedBy.Count != want || ws.Agents[0].MCPServers.Declared != want {
					t.Fatalf("%s=%s joined the wrong server: Agents=%+v server=%+v findings=%+v", field, group, ws.Agents, ws.MCPServers, ws.Findings)
				}
				found := false
				for _, finding := range ws.Findings {
					if finding.Subject.Kind == KindRemoteMCPServer {
						found = true
						if finding.Affects == nil || finding.Affects.Count != want {
							t.Fatalf("%s=%s: wrong finding impact %+v", field, group, finding)
						}
					}
				}
				if !found {
					t.Fatal("the server's own failure must remain visible")
				}
			}
		})
	}
}

// testdata/impact.json is built from kagent 1.0.1's shapes and messages (a
// live cluster's where it had them), for Go and, later, the UI.
func TestImpactVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/impact.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []impactVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			ws := Compose(inventoryOf(v))
			groups := Fold(ws.Findings)
			checkFindings(t, ws, v)
			checkGroups(t, groups, v)
			checkViews(t, ws, v)
			complete := func(kind string) bool { return v.Unread[kind] == "" }
			counts := CountsOf(ws, groups, complete)
			if w := v.Want.Counts.Findings; w != nil && counts.Findings != *w {
				t.Errorf("findings count %+v, want %+v", counts.Findings, *w)
			}
			if w := v.Want.Counts.Groups; w != nil && counts.Groups.N != *w {
				t.Errorf("groups %d, want %d", counts.Groups.N, *w)
			}
			if w := v.Want.Counts.AgentsAffected; w != nil && counts.AgentsAffected != *w {
				t.Errorf("agents affected %+v, want %+v", counts.AgentsAffected, *w)
			}
		})
	}
}

func checkFindings(t *testing.T, ws *Workspace, v impactVector) {
	t.Helper()
	var got []string
	byID := map[string]ObjectFinding{}
	for _, f := range ws.Findings {
		got = append(got, f.ID)
		byID[f.ID] = f
	}
	var want []string
	for _, w := range v.Want.Findings {
		want = append(want, w.ID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("findings %v, want %v", got, want)
	}
	for _, w := range v.Want.Findings {
		f := byID[w.ID]
		if f.Severity != w.Severity {
			t.Errorf("%s severity %s, want %s", w.ID, f.Severity, w.Severity)
		}
		switch {
		case w.Cause == nil && f.Cause != nil:
			t.Errorf("%s cause %+v, want none", w.ID, f.Cause)
		case w.Cause != nil && (f.Cause == nil || f.Cause.Target.Key() != w.Cause.Target || f.Cause.Role != w.Cause.Role || f.Cause.Form != w.Cause.Form):
			t.Errorf("%s cause %+v, want %+v", w.ID, f.Cause, w.Cause)
		}
		secret := ""
		if f.Secret != nil {
			secret = f.Secret.NamespacedName()
		}
		if secret != w.Secret {
			t.Errorf("%s secret %q, want %q", w.ID, secret, w.Secret)
		}
		if w.MessageContains != "" && !strings.Contains(f.Message, w.MessageContains) {
			t.Errorf("%s message %q, want it to say %q", w.ID, f.Message, w.MessageContains)
		}
		if w.Affects == nil {
			if f.Affects != nil && f.Affects.Count > 0 {
				t.Errorf("%s affects %+v, want none", w.ID, f.Affects)
			}
			continue
		}
		if f.Affects == nil {
			t.Errorf("%s affects nothing, want %+v", w.ID, w.Affects)
			continue
		}
		effects := map[string]string{}
		for _, a := range f.Affects.Agents {
			effects[agentKey(a.Agent)] = a.Effect
			if want, ok := w.Through[agentKey(a.Agent)]; ok && !slices.Equal(a.Through, want) {
				t.Errorf("%s: %s through %v, want %v", w.ID, agentKey(a.Agent), a.Through, want)
			}
		}
		if !reflect.DeepEqual(effects, w.Affects.Agents) || f.Affects.Count != w.Affects.Count || f.Affects.LowerBound != w.Affects.LowerBound || f.Affects.Consumers != w.Affects.Consumers {
			t.Errorf("%s affects %v count %d lowerBound %v consumers %s, want %+v", w.ID, effects, f.Affects.Count, f.Affects.LowerBound, f.Affects.Consumers, *w.Affects)
		}
	}
}

func checkGroups(t *testing.T, groups []AttentionGroup, v impactVector) {
	t.Helper()
	if len(groups) != len(v.Want.Groups) {
		t.Fatalf("groups %+v, want %+v", groups, v.Want.Groups)
	}
	for i, w := range v.Want.Groups {
		g := groups[i]
		var folded [][]string
		for _, f := range g.Folded {
			folded = append(folded, []string{f.Finding, f.Under})
		}
		secret := ""
		if g.Secret != nil {
			secret = g.Secret.NamespacedName()
		}
		if !slices.Equal(g.Findings, w.Findings) || !reflect.DeepEqual(folded, w.Folded) || g.Severity != w.Severity || secret != w.Secret {
			t.Errorf("group %d = findings %v folded %v severity %s secret %q, want %+v", i, g.Findings, folded, g.Severity, secret, w)
		}
	}
}

func checkViews(t *testing.T, ws *Workspace, v impactVector) {
	t.Helper()
	agents := map[string]AgentView{}
	for _, a := range ws.Agents {
		agents[agentKey(a.Ref)] = a
	}
	for key, w := range v.Want.Agents {
		a, ok := agents[key]
		if !ok {
			t.Errorf("no Agent %s", key)
			continue
		}
		if w.LatestChange != "" && a.LatestChange.State != w.LatestChange {
			t.Errorf("%s latest change %s, want %s", key, a.LatestChange.State, w.LatestChange)
		}
		if w.NewSessionsRevision != nil && a.NewSessionsRevision != *w.NewSessionsRevision {
			t.Errorf("%s new sessions revision %q, want %q", key, a.NewSessionsRevision, *w.NewSessionsRevision)
		}
		if w.DesiredRevision != nil && a.DesiredRevision != *w.DesiredRevision {
			t.Errorf("%s desired revision %q, want %q", key, a.DesiredRevision, *w.DesiredRevision)
		}
		if w.Cycle != nil && !slices.Equal(a.Cycle, w.Cycle) {
			t.Errorf("%s cycle %v, want %v", key, a.Cycle, w.Cycle)
		}
		if w.MCPFailing != nil && a.MCPServers.Failing != *w.MCPFailing {
			t.Errorf("%s failing MCP servers %d, want %d", key, a.MCPServers.Failing, *w.MCPFailing)
		}
		if w.ToolsNotOffered != nil {
			var got []string
			for _, ask := range a.ToolsNotOffered {
				got = append(got, ask.Server.Key()+":"+ask.Tool)
			}
			if !slices.Equal(got, w.ToolsNotOffered) {
				t.Errorf("%s tools not offered %v, want %v", key, got, w.ToolsNotOffered)
			}
		}
	}
	for key, want := range v.Want.References {
		a, ok := agents[key]
		if !ok {
			t.Errorf("no Agent %s", key)
			continue
		}
		for _, w := range want {
			found := false
			for _, r := range a.References {
				if r.Role != w.Role || r.Target.Key() != w.Target {
					continue
				}
				found = true
				if r.State != w.State || r.Unread != w.Unread || r.Fallback != w.Fallback || (w.PolicyUnknown != nil && r.PolicyUnknown != *w.PolicyUnknown) || (w.Tools != nil && !slices.Equal(r.Tools, w.Tools)) {
					t.Errorf("%s %s %s = %+v, want %+v", key, w.Role, w.Target, r, w)
				}
			}
			if !found {
				t.Errorf("%s has no %s reference to %s: %+v", key, w.Role, w.Target, a.References)
			}
		}
	}
	models := map[string]ModelView{}
	for _, m := range ws.Models {
		models[agentKey(m.Ref)] = m
	}
	for key, w := range v.Want.Models {
		m, ok := models[key]
		if !ok {
			t.Errorf("no ModelConfig %s", key)
			continue
		}
		if m.UsedBy.Consumers != w.Consumers || m.UsedBy.Count != w.Count || (w.ByRole != nil && !reflect.DeepEqual(m.UsedBy.ByRole, w.ByRole)) {
			t.Errorf("%s used by %+v, want %+v", key, m.UsedBy, w)
		}
	}
	servers := map[string]MCPServerView{}
	for _, s := range ws.MCPServers {
		servers[agentKey(s.Ref)] = s
	}
	for key, w := range v.Want.MCPServers {
		s, ok := servers[key]
		if !ok {
			t.Errorf("no RemoteMCPServer %s", key)
			continue
		}
		asked := map[string][]string{}
		for _, ask := range s.AskedNotOffered {
			for _, a := range ask.Agents {
				asked[ask.Tool] = append(asked[ask.Tool], agentKey(a))
			}
		}
		if s.Failing != w.Failing || s.ToolsOfferedKnown != w.ToolsOfferedKnown || (w.AskedNotOffered != nil && !reflect.DeepEqual(asked, w.AskedNotOffered)) {
			t.Errorf("%s = failing %v known %v asked %v, want %+v", key, s.Failing, s.ToolsOfferedKnown, asked, w)
		}
	}
}

// Each recognized form is tested against kagent's own format strings, as
// fmt.Errorf("%q") renders a types.NamespacedName and a bare name.
func TestMatchCauseForms(t *testing.T) {
	refs := []Reference{
		{Role: RoleTemplate, Target: kagentRef(KindAgentTemplate, "agents", "base")},
		{Role: RoleHarness, Target: kagentRef(KindHarness, "agents", "default")},
		{Role: RoleModel, Target: kagentRef(KindModelConfig, "agents", "gpt")},
		{Role: RoleSubagentModel, Target: kagentRef(KindModelConfig, "shared", "small")},
		{Role: RoleSummarizer, Target: kagentRef(KindModelConfig, "agents", "summarizer")},
		{Role: RoleEmbedding, Target: kagentRef(KindModelConfig, "agents", "embedder")},
		{Role: RoleSubagentTemplate, Target: kagentRef(KindAgentTemplate, "agents", "researcher")},
		{Role: RoleMCPServer, Target: kagentRef(KindRemoteMCPServer, "tools", "github")},
		{Role: RoleSubagentMCPServer, Target: kagentRef(KindRemoteMCPServer, "agents", "jira")},
		{Role: RoleMCPServer, Target: kagentRef(KindRemoteMCPServer, "a", "dup")},
		{Role: RoleSubagentMCPServer, Target: kagentRef(KindRemoteMCPServer, "b", "dup")},
	}
	for _, c := range []struct {
		condition, message string
		target, role, form string
	}{
		{"ResolvedRefs", `resolve ModelConfig "agents/gpt": not found`, "ModelConfig/agents/gpt", RoleModel, FormNotFound},
		{"ResolvedRefs", `resolve ModelConfig "shared/small": reference from namespace "agents" denied by allowedNamespaces`, "ModelConfig/shared/small", RoleSubagentModel, FormNotPermitted},
		{"ResolvedRefs", `resolve ModelConfig "agents/gpt": secret openai not found`, "ModelConfig/agents/gpt", RoleModel, FormFailed},
		{"ResolvedRefs", `resolve ModelConfig "agents/gpt": secret openai does not contain key "KEY"`, "ModelConfig/agents/gpt", RoleModel, FormFailed},
		{"Compatible", `ModelConfig "agents/gpt": unsupported model provider: Foo`, "ModelConfig/agents/gpt", RoleModel, FormFailed},
		{"ResolvedRefs", `resolve summarizer ModelConfig "summarizer": resolve ModelConfig "agents/summarizer": not found`, "ModelConfig/agents/summarizer", RoleSummarizer, FormNotFound},
		{"ResolvedRefs", `resolve memory ModelConfig "embedder": resolve ModelConfig "agents/embedder": secret x not found`, "ModelConfig/agents/embedder", RoleEmbedding, FormFailed},
		{"Compatible", `resolve memory ModelConfig "embedder": ModelConfig "agents/embedder": unsupported model provider: Foo`, "ModelConfig/agents/embedder", RoleEmbedding, FormFailed},
		{"ResolvedRefs", `resolve AgentTemplate "agents/base": not found`, "AgentTemplate/agents/base", RoleTemplate, FormNotFound},
		{"ResolvedRefs", `resolve AgentTemplate "researcher": not found`, "AgentTemplate/agents/researcher", RoleSubagentTemplate, FormNotFound},
		{"ResolvedRefs", `resolve Harness "agents/default": not found`, "Harness/agents/default", RoleHarness, FormNotFound},
		{"ResolvedRefs", `resolve RemoteMCPServer "github": not found`, "RemoteMCPServer/tools/github", RoleMCPServer, FormNotFound},
		{"ResolvedRefs", `resolve RemoteMCPServer "jira": not found`, "RemoteMCPServer/agents/jira", RoleSubagentMCPServer, FormNotFound},
		{"ResolvedRefs", `resolve RemoteMCPServer "tools/github": reference from namespace "agents" denied by allowedNamespaces`, "RemoteMCPServer/tools/github", RoleMCPServer, FormNotPermitted},
	} {
		got := matchCause(c.condition, c.message, refs)
		if got == nil || got.Target.Key() != c.target || got.Role != c.role || got.Form != c.form {
			t.Errorf("%s: %+v, want %s %s %s", c.message, got, c.target, c.role, c.form)
		}
	}
	for _, c := range []struct{ condition, message string }{
		// Not declared by this Agent: the status may describe an earlier spec.
		{"ResolvedRefs", `resolve ModelConfig "agents/other": not found`},
		// The name is ambiguous among the declared servers.
		{"ResolvedRefs", `resolve RemoteMCPServer "dup": not found`},
		// Only quoting a recognized form is not one.
		{"ResolvedRefs", `failed: resolve ModelConfig "agents/gpt": not found`},
		// A WorkerPool is Substrate's, not a kagent dependency Radar reads.
		{"ResolvedRefs", `WorkerPool "agents/pool" not found`},
		{"Compatible", `AgentTemplate tool cycle includes "base"`},
		{"Compatible", `resolve ModelConfig "agents/gpt": not found`},
		{"Ready", `resolve ModelConfig "agents/gpt": not found`},
		{"ResolvedRefs", `resolve RemoteMCPServer "tools/github": observe source namespace "agents" for allowedNamespaces.from=Selector: forbidden`},
	} {
		if got := matchCause(c.condition, c.message, refs); got != nil {
			t.Errorf("%s (%s) matched %+v", c.message, c.condition, got)
		}
	}
}

// Folding is against the findings this caller receives: with the
// dependency's finding withheld or outside the namespace filter, the Agent's
// finding stays its own row.
func TestFoldNeedsTheDependencyRow(t *testing.T) {
	missing := kagentRef(KindModelConfig, "shared", "gpt")
	agent := ObjectFinding{ID: "Agent/team/a/KagentNoPreparedRevision", Subject: kagentRef(KindAgent, "team", "a"), Severity: SeverityCritical,
		Reason: ReasonNoPreparedRevision, Cause: &Cause{Target: missing, Role: RoleModel, Form: FormNotFound}}
	dep := ObjectFinding{ID: findingID(missing, ReasonDependencyMissing), Subject: missing, Severity: SeverityWarning, Reason: ReasonDependencyMissing}
	if g := Fold([]ObjectFinding{agent}); len(g) != 1 || len(g[0].Folded) != 0 || g[0].Findings[0] != agent.ID {
		t.Errorf("without the dependency's row = %+v", g)
	}
	g := Fold([]ObjectFinding{agent, dep})
	if len(g) != 1 || g[0].Findings[0] != dep.ID || len(g[0].Folded) != 1 || g[0].Severity != SeverityCritical {
		t.Errorf("with it = %+v", g)
	}
	ws := &Workspace{Findings: []ObjectFinding{agent, dep}}
	if in := ws.In([]string{"team"}); len(Fold(in.Findings)) != 1 || len(Fold(in.Findings)[0].Folded) != 0 {
		t.Errorf("dependency outside the filter = %+v", Fold(in.Findings))
	}
	// A cause of another form does not fold under the dependency's own finding.
	own := ObjectFinding{ID: findingID(missing, ReasonAPIKeySecretMissing), Subject: missing, Severity: SeverityWarning, Reason: ReasonAPIKeySecretMissing}
	if g := Fold([]ObjectFinding{agent, own}); len(g) != 2 {
		t.Errorf("a not-found cause folded under the object's own finding: %+v", g)
	}
}

// A partial read of Agents or templates makes every impact a lower bound and
// "no Agent consumers seen", never "no Agent uses it".
func TestImpactLowerBoundOnPartialRead(t *testing.T) {
	m := model(t, "kagent", "unused")
	inv := Inventory{Now: time.Now(), Kinds: map[string]Objects{
		KindModelConfig:   {Items: []*unstructured.Unstructured{m}, Read: func(string) bool { return true }, Complete: true, State: "full"},
		KindAgent:         {Read: func(ns string) bool { return ns == "kagent" }, State: "partial"},
		KindAgentTemplate: {Read: func(string) bool { return true }, Complete: true, State: "full"},
		KindHarness:       {Read: func(string) bool { return true }, Complete: true, State: "full"},
	}}
	ws := Compose(inv)
	if got := ws.Models[0].UsedBy; !got.LowerBound || got.Consumers != ConsumersNoneSeen {
		t.Errorf("used by %+v, want a lower bound with none seen", got)
	}
}

// The used-by list stops at UsedByCap; the count is every Agent.
func TestImpactCapsTheList(t *testing.T) {
	objs := []*unstructured.Unstructured{model(t, "kagent", "shared")}
	for i := 0; i < UsedByCap+5; i++ {
		a := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha3", "kind": KindAgent,
			"metadata": map[string]any{"name": "a" + strings.Repeat("x", i%3) + "-" + itoa(int64(i)), "namespace": "kagent"},
			"spec":     map[string]any{"template": map[string]any{"inline": map[string]any{"modelConfig": map[string]any{"name": "shared"}}}}}}
		objs = append(objs, a)
	}
	all := Objects{Read: func(string) bool { return true }, Complete: true, State: "full"}
	inv := Inventory{Now: time.Now(), Kinds: map[string]Objects{}}
	for _, k := range Kinds {
		inv.Kinds[k] = all
	}
	agents, models := all, all
	for _, o := range objs {
		if o.GetKind() == KindAgent {
			agents.Items = append(agents.Items, o)
		} else {
			models.Items = append(models.Items, o)
		}
	}
	inv.Kinds[KindAgent], inv.Kinds[KindModelConfig] = agents, models
	models.Items[0].Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "ModelConfigMissing", "message": "model is unavailable"}}}
	ws := Compose(inv)
	got := ws.Models[0].UsedBy
	if got.Count != UsedByCap+5 || len(got.Agents) != UsedByCap || got.LowerBound {
		t.Errorf("used by count %d listed %d lowerBound %v", got.Count, len(got.Agents), got.LowerBound)
	}
	counts := CountsOf(ws, Fold(ws.Findings), func(string) bool { return true })
	if counts.AgentsAffected.N != UsedByCap+5 || counts.AgentsAffected.LowerBound {
		t.Fatalf("affected count=%+v", counts.AgentsAffected)
	}

}

func model(t *testing.T, ns, name string) *unstructured.Unstructured {
	t.Helper()
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha3", "kind": KindModelConfig,
		"metadata": map[string]any{"name": name, "namespace": ns}, "spec": map[string]any{"model": "m", "provider": "OpenAI"}}}
}

// Nothing a credential can sit in reaches the workspace: header values,
// URL user info and credential queries, also when kagent quotes them in a
// condition message.
func TestWorkspaceCarriesNoCredential(t *testing.T) {
	server := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha3", "kind": KindRemoteMCPServer,
		"metadata": map[string]any{"name": "spike-literal-header", "namespace": "kagent", "generation": int64(1)},
		"spec": map[string]any{"description": "fixture with a fake literal credential", "url": "http://user:FAKEPASS@no-such-mcp.radar-kagent-spike.svc.cluster.local:9999/mcp?token=FAKEQUERY",
			"headersFrom": []any{map[string]any{"name": "Authorization", "value": "Bearer FAKEHEADER"}}},
		"status": map[string]any{"observedGeneration": int64(1), "conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "observedGeneration": int64(1),
			"message": `Failed to connect to MCP server: failed to connect MCP client: calling "initialize": sending "initialize": rejected by transport: Post "http://user:***@no-such-mcp.radar-kagent-spike.svc.cluster.local:9999/mcp?token=FAKEQUERY": dial tcp: lookup no-such-mcp.radar-kagent-spike.svc.cluster.local on 172.20.0.10:53: no such host`}}}}}
	all := Objects{Read: func(string) bool { return true }, Complete: true, State: "full"}
	inv := Inventory{Now: time.Now(), Kinds: map[string]Objects{}}
	for _, k := range Kinds {
		inv.Kinds[k] = all
	}
	servers := all
	servers.Items = []*unstructured.Unstructured{server}
	inv.Kinds[KindRemoteMCPServer] = servers
	ws := Compose(inv)
	out, err := json.Marshal(struct {
		*Workspace
		Groups []AttentionGroup
	}{ws, Fold(ws.Findings)})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"FAKEPASS", "FAKEQUERY", "FAKEHEADER", "fake literal credential"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("workspace carries %q: %s", secret, out)
		}
	}
	if !ws.MCPServers[0].Headers[0].Inline || ws.MCPServers[0].Headers[0].Name != "Authorization" {
		t.Errorf("headers %+v", ws.MCPServers[0].Headers)
	}
}

func TestReferenceUnreadDoesNotClaimFullCoverage(t *testing.T) {
	target := kagentRef(KindModelConfig, "outside", "model")
	inv := Inventory{Kinds: map[string]Objects{KindModelConfig: {State: "full", Read: func(ns string) bool { return ns == "inside" }}}}
	_, state, why := newIndex(inv).resolve(target)
	if state != RefUnread || why != "notRead" {
		t.Fatalf("scoped read: %s %s", state, why)
	}
	objs := inv.Kinds[KindModelConfig]
	objs.UnreadReason = func(string) string { return "outside_namespace_pin" }
	inv.Kinds[KindModelConfig] = objs
	_, state, why = newIndex(inv).resolve(target)
	if state != RefUnread || why != "outside_namespace_pin" {
		t.Fatalf("pinned read: %s %s", state, why)
	}
}

func TestFoldManyAgentsUnderLastDependency(t *testing.T) {
	target := kagentRef(KindModelConfig, "models", "missing")
	dep := ObjectFinding{ID: findingID(target, ReasonDependencyMissing), Subject: target, Severity: SeverityWarning, Reason: ReasonDependencyMissing}
	findings := make([]ObjectFinding, 10000, 10001)
	for i := range findings {
		agent := kagentRef(KindAgent, "agents", itoa(int64(i)))
		findings[i] = ObjectFinding{ID: findingID(agent, ReasonNoPreparedRevision), Subject: agent, Severity: SeverityCritical,
			Cause: &Cause{Target: target, Form: FormNotFound}}
	}
	groups := Fold(append(findings, dep))
	if len(groups) != 1 || len(groups[0].Folded) != len(findings) || groups[0].Findings[0] != dep.ID || groups[0].Severity != SeverityCritical {
		t.Fatalf("groups=%d folded=%d", len(groups), len(groups[0].Folded))
	}
}

func TestUnknownModelFailureDoesNotFold(t *testing.T) {
	target := kagentRef(KindModelConfig, "models", "m")
	refs := []Reference{{Role: RoleModel, Target: target}, {Role: RoleEmbedding, Target: target}}
	for _, message := range []string{
		`resolve ModelConfig "models/m": observe source namespace "agents" for allowedNamespaces.from=Selector: forbidden`,
		`resolve memory ModelConfig "m": resolve ModelConfig "models/m": observe source namespace "agents" for allowedNamespaces.from=Selector: forbidden`,
		`resolve ModelConfig "models/m": an unrecognized error`,
	} {
		agent := ObjectFinding{ID: "agent", Subject: kagentRef(KindAgent, "agents", "a"), Cause: matchCause("ResolvedRefs", message, refs)}
		dep := ObjectFinding{ID: "model", Subject: target, Reason: ReasonAPIKeySecretMissing}
		if agent.Cause != nil || len(Fold([]ObjectFinding{agent, dep})) != 2 {
			t.Fatalf("unrecognized error folded: %s, %+v", message, agent.Cause)
		}
	}
}

func TestCountsIncludeBroaderCoordinateEvidence(t *testing.T) {
	for _, kind := range []string{KindAgent, KindAgentTemplate, KindHarness} {
		t.Run(kind, func(t *testing.T) {
			inv := Inventory{Kinds: map[string]Objects{}}
			for _, k := range Kinds {
				inv.Kinds[k] = Objects{Complete: k != kind, Read: func(string) bool { return true }}
			}
			ws := Compose(inv).In([]string{"models"})
			counts := CountsOf(ws, Fold(ws.Findings), func(string) bool { return true })
			if !counts.Findings.LowerBound || !counts.Critical.LowerBound || !counts.Groups.Incomplete || !counts.AgentsAffected.LowerBound {
				t.Fatalf("partial broader %s read certified exact counts: %+v", kind, counts)
			}
			if counts.Agents.LowerBound || counts.Models.LowerBound {
				t.Fatalf("selected inventory counts should remain complete: %+v", counts)
			}
		})
	}
}

func TestAgentDescriptionComesFromReadableRootTemplate(t *testing.T) {
	inline := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha3", "kind": KindAgent,
		"metadata": map[string]any{"name": "inline", "namespace": "agents"},
		"spec":     map[string]any{"template": map[string]any{"inline": map[string]any{"description": "See https://user:DESCRIPTION_SECRET@example.com/?token=DESCRIPTION_TOKEN"}}}}}
	referenced := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha3", "kind": KindAgent,
		"metadata": map[string]any{"name": "referenced", "namespace": "agents"},
		"spec":     map[string]any{"template": map[string]any{"ref": map[string]any{"name": "base"}}}}}
	template := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1alpha3", "kind": KindAgentTemplate,
		"metadata": map[string]any{"name": "base", "namespace": "agents"}, "spec": map[string]any{"description": "Referenced description"}}}
	inv := Inventory{Kinds: map[string]Objects{
		KindAgent:         {Items: []*unstructured.Unstructured{inline, referenced}},
		KindAgentTemplate: {Items: []*unstructured.Unstructured{template}},
	}}
	w := Compose(inv)
	if len(w.Agents) != 2 || w.Agents[0].Description == "" || strings.Contains(w.Agents[0].Description, "DESCRIPTION_") || w.Agents[1].Description != "Referenced description" {
		t.Fatalf("descriptions = %+v", w.Agents)
	}
	inv.Kinds[KindAgentTemplate] = Objects{State: "denied"}
	if got := Compose(inv).Agents[1]; got.Description != "" || !got.Incomplete {
		t.Fatalf("unread template supplied a description: %+v", got)
	}
}
