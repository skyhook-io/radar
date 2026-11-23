package kagent

import (
	"regexp"
)

// How an Agent's own message says a dependency stopped its latest change
// (Cause.Form).
const (
	// FormNotFound: kagent found no object by that name.
	FormNotFound = "notFound"
	// FormNotPermitted: the target's allowedNamespaces refused the reference.
	FormNotPermitted = "notPermitted"
	// FormFailed: the target exists and is itself broken (a ModelConfig whose
	// Secret is missing or whose spec kagent rejects).
	FormFailed = "failed"
)

// Cause is the dependency an Agent's ResolvedRefs or Compatible message names.
// kagent's compiler returns its first error, so this is the first cause it
// reports: fixing it can reveal another.
type Cause struct {
	Target ObjectRef `json:"target"`
	Role   string    `json:"role"`
	Form   string    `json:"form"`
}

// The messages kagent 1.0.1 writes when it can't resolve an Agent's reference.
// Each is anchored, so a message that only quotes one of them is not read as
// it; anything else leaves the Agent's finding unmatched.
var (
	// resolve ModelConfig "ns/name": not found | reference from namespace … |
	// <the ModelConfig's own failure> (translator/references.go:52,55,61).
	causeModel = regexp.MustCompile(`^resolve ModelConfig "([^"/]+)/([^"]+)": (.+)$`)
	// ModelConfig "ns/name": <semantic failure>, on Compatible
	// (translator/references.go:58).
	causeModelInvalid = regexp.MustCompile(`^ModelConfig "([^"/]+)/([^"]+)": (.+)$`)
	// resolve summarizer|memory ModelConfig "name": <either form above>
	// (translator/adkconfig/runtime.go:70,141).
	causeHarnessModel = regexp.MustCompile(`^resolve (summarizer|memory) ModelConfig "[^"]+": (?:resolve )?ModelConfig "([^"/]+)/([^"]+)": (.+)$`)
	// resolve AgentTemplate "ns/name": not found, the Agent's template
	// (translator/compiler.go:119).
	causeRootTemplate = regexp.MustCompile(`^resolve AgentTemplate "([^"/]+)/([^"]+)": not found$`)
	// resolve AgentTemplate "name": not found, a subagent's (compiler.go:237).
	causeSubagentTemplate = regexp.MustCompile(`^resolve AgentTemplate "([^"/]+)": not found$`)
	// resolve Harness "ns/name": not found (compiler.go:126).
	causeHarness = regexp.MustCompile(`^resolve Harness "([^"/]+)/([^"]+)": not found$`)
	// resolve RemoteMCPServer "name": not found (compiler.go:284).
	causeMCPMissing = regexp.MustCompile(`^resolve RemoteMCPServer "([^"/]+)": not found$`)
	// resolve RemoteMCPServer "ns/name": reference from namespace … (compiler.go:287).
	causeMCPRefused       = regexp.MustCompile(`^resolve RemoteMCPServer "([^"/]+)/([^"]+)": (.+)$`)
	modelReferenceFailure = regexp.MustCompile(`^(?:secret \"?[^\"\s]+\"? (?:not found|does not contain key \"[^\"]+\")|config map [^\s]+ (?:not found|does not contain key \"[^\"]+\"|has an empty endpoint at key \"[^\"]+\"))$`)
	modelSemanticFailure  = regexp.MustCompile(`^(?:(?:AzureOpenAI|GeminiVertexAI|AnthropicVertexAI|ollama|bedrock|sapAICore|foundry) model config is required|unsupported model provider: .+|foundry endpoint could not be resolved: set foundry.endpoint or a foundry.endpointFrom whose ConfigMap key exists)$`)
	// reference from namespace "ns" denied by allowedNamespaces (translator/references.go:41).
	refusedByPolicy = regexp.MustCompile(`^reference from namespace "[^"]+" denied by allowedNamespaces$`)
)

func formOf(rest string) string {
	switch {
	case rest == "not found":
		return FormNotFound
	case refusedByPolicy.MatchString(rest):
		return FormNotPermitted
	case modelReferenceFailure.MatchString(rest) || modelSemanticFailure.MatchString(rest):
		return FormFailed
	}
	return ""
}

// matchCause reads which declared reference an Agent's message names. The
// subagent and MCP server forms carry a bare name, so they match only when
// exactly one declared reference of that role has it. A cause that names no
// declared reference is unmatched: the status may describe an earlier spec.
func matchCause(condition, message string, refs []Reference) *Cause {
	var want []Reference
	pick := func(form string, ok func(Reference) bool) *Cause {
		if form == "" {
			return nil
		}
		want = want[:0]
		for _, r := range refs {
			if ok(r) && !containsTarget(want, r) {
				want = append(want, r)
			}
		}
		if len(want) != 1 {
			return nil
		}
		return &Cause{Target: want[0].Target, Role: want[0].Role, Form: form}
	}
	named := func(kind, ns, name string, roles ...string) func(Reference) bool {
		return func(r Reference) bool {
			return r.Target.Kind == kind && r.Target.Namespace == ns && r.Target.Name == name && hasRole(roles, r.Role)
		}
	}
	if condition == "Compatible" {
		if m := causeHarnessModel.FindStringSubmatch(message); m != nil && formOf(m[4]) == FormFailed {
			return pick(FormFailed, named(KindModelConfig, m[2], m[3], harnessRole(m[1])))
		}
		if m := causeModelInvalid.FindStringSubmatch(message); m != nil && modelSemanticFailure.MatchString(m[3]) {
			return pick(FormFailed, named(KindModelConfig, m[1], m[2], RoleModel, RoleSubagentModel))
		}
		return nil
	}
	if condition != "ResolvedRefs" {
		return nil
	}
	if m := causeHarnessModel.FindStringSubmatch(message); m != nil {
		return pick(formOf(m[4]), named(KindModelConfig, m[2], m[3], harnessRole(m[1])))
	}
	if m := causeModel.FindStringSubmatch(message); m != nil {
		return pick(formOf(m[3]), named(KindModelConfig, m[1], m[2], RoleModel, RoleSubagentModel))
	}
	if m := causeRootTemplate.FindStringSubmatch(message); m != nil {
		return pick(FormNotFound, named(KindAgentTemplate, m[1], m[2], RoleTemplate))
	}
	if m := causeSubagentTemplate.FindStringSubmatch(message); m != nil {
		return pick(FormNotFound, func(r Reference) bool {
			return r.Target.Kind == KindAgentTemplate && r.Target.Name == m[1] && r.Role == RoleSubagentTemplate
		})
	}
	if m := causeHarness.FindStringSubmatch(message); m != nil {
		return pick(FormNotFound, named(KindHarness, m[1], m[2], RoleHarness))
	}
	if m := causeMCPMissing.FindStringSubmatch(message); m != nil {
		return pick(FormNotFound, func(r Reference) bool {
			return r.Target.Kind == KindRemoteMCPServer && r.Target.Name == m[1] && (r.Role == RoleMCPServer || r.Role == RoleSubagentMCPServer)
		})
	}
	if m := causeMCPRefused.FindStringSubmatch(message); m != nil && formOf(m[3]) == FormNotPermitted {
		return pick(FormNotPermitted, named(KindRemoteMCPServer, m[1], m[2], RoleMCPServer, RoleSubagentMCPServer))
	}
	return nil
}

func harnessRole(wrapper string) string {
	if wrapper == "memory" {
		return RoleEmbedding
	}
	return RoleSummarizer
}

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

func containsTarget(refs []Reference, r Reference) bool {
	for _, existing := range refs {
		if existing.Target == r.Target {
			return true
		}
	}
	return false
}
