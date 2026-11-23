package kagent

import (
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Radar's reasons for what the workspace finds beyond each object's own
// reading. They name a coordinate, not an object, so they are the workspace's
// alone: Issues are raised on objects, and each Agent that names the
// coordinate has its own Issue.
const (
	// ReasonDependencyMissing: Agents name an AgentTemplate, Harness,
	// ModelConfig or RemoteMCPServer that the caller's read proves absent.
	ReasonDependencyMissing = "KagentDependencyMissing"
	// ReasonDependencyNotPermitted: the target's allowedNamespaces refuses
	// the namespace of Agents that name it.
	ReasonDependencyNotPermitted = "KagentDependencyNotPermitted"
)

// What a dependency's finding means for one Agent (AffectedAgent.Effect).
const (
	// EffectBlocked: the Agent's own message names this dependency as the
	// first cause kagent reports for not preparing its latest change.
	EffectBlocked = "blocked"
	// EffectMayRunWithoutTools: kagent's controller can't discover the MCP
	// server's tools, and the Agent's latest change is prepared, so its
	// sessions may run without them: each runtime discovers tools again on
	// every invocation and leaves a server out when that fails.
	EffectMayRunWithoutTools = "mayRunWithoutTools"
	// EffectUsesIt: the Agent declares it; kagent reports no effect on it.
	EffectUsesIt = "usesIt"
)

// Whether any Agent uses a dependency (Impact.Consumers).
const (
	ConsumersSeen = "seen"
	// ConsumersNone: every Agent, AgentTemplate and Harness in scope was read
	// and none uses it. It may be staged for an Agent not yet created.
	ConsumersNone = "none"
	// ConsumersNoneSeen: none that was read uses it, but the read was partial.
	ConsumersNoneSeen = "noneSeen"
)

// UsedByCap bounds the Agents listed under one dependency; Count is all of them.
const UsedByCap = 500

// ReadingView is a Reading on the wire.
type ReadingView struct {
	State     State     `json:"state"`
	Tone      Tone      `json:"tone"`
	Text      string    `json:"text"`
	Summary   string    `json:"summary"`
	Condition string    `json:"condition,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Message   string    `json:"message,omitempty"`
	Since     time.Time `json:"since,omitzero"`
	// Egress is kagent's EgressPoliciesApplied=False summary, apart from
	// readiness.
	Egress string `json:"egress,omitempty"`
}

// View is the reading on the wire.
func (r Reading) View() ReadingView {
	v := ReadingView{State: r.State, Tone: r.Tone, Text: r.Text, Summary: r.Summary, Condition: r.Condition, Reason: r.Reason, Message: r.Message}
	if r.SinceKnown {
		v.Since = r.Since
	}
	if r.Egress != nil {
		v.Egress = r.Egress.Summary
	}
	return v
}

// The latest change's state, kagent's preparation of the Agent's current spec
// (LatestChange.State).
const (
	ChangePrepared    = "prepared"
	ChangePreparing   = "preparing"
	ChangeBlocked     = "blocked"
	ChangeFailed      = "failed"
	ChangeNotReady    = "notReady"
	ChangeUnprocessed = "notProcessed"
	ChangeNotReported = "notReported"
	ChangeDeleting    = "deleting"
	ChangeUnknown     = "unknown"
)

// LatestChange is what kagent says of the Agent's current spec, apart from
// the revision new sessions start on.
type LatestChange struct {
	State string `json:"state"`
	// Message is kagent's own, for a blocked, failed or not-ready change.
	Message string `json:"message,omitempty"`
}

func latestChangeOf(r Reading) LatestChange {
	switch r.State {
	case StateReady:
		return LatestChange{State: ChangePrepared}
	case StatePreparing:
		return LatestChange{State: ChangePreparing, Message: r.Message}
	case StateBlocked:
		return LatestChange{State: ChangeBlocked, Message: r.Message}
	case StateFailed:
		return LatestChange{State: ChangeFailed, Message: r.Message}
	case StateNotReady:
		return LatestChange{State: ChangeNotReady, Message: r.Message}
	case StateStale:
		return LatestChange{State: ChangeUnprocessed}
	case StateNotReported:
		return LatestChange{State: ChangeNotReported}
	case StateDeleting:
		return LatestChange{State: ChangeDeleting}
	}
	return LatestChange{State: ChangeUnknown}
}

// DependencyState is a referenced object as one Agent row shows it.
type DependencyState struct {
	Ref   ObjectRef `json:"ref"`
	State string    `json:"state"`
	// Unread is the coverage state or read reason for an unread target.
	Unread  string       `json:"unread,omitempty"`
	Reading *ReadingView `json:"reading,omitempty"`
}

// MCPSummary counts the RemoteMCPServers an Agent's latest change binds.
type MCPSummary struct {
	Declared int `json:"declared"`
	// Failing: kagent's controller can't discover their tools, or didn't
	// accept them.
	Failing int `json:"failing"`
	Absent  int `json:"absent"`
	Unread  int `json:"unread"`
	// NotPermitted: their allowedNamespaces refuses this Agent's namespace.
	NotPermitted int `json:"notPermitted"`
}

// AgentView is one Agent: kagent's preparation of its latest change, the
// revision new sessions start on, and what the latest change declares. The
// references are the latest change's: while it is blocked or not yet
// prepared, new sessions still run an earlier revision whose configuration
// Radar does not reconstruct.
type AgentView struct {
	Ref          ObjectRef    `json:"ref"`
	Reading      ReadingView  `json:"reading"`
	LatestChange LatestChange `json:"latestChange"`
	// NewSessionsRevision is status.latestSuccessfulRevision, the revision
	// kagent starts new sessions on; empty when none was prepared.
	NewSessionsRevision string `json:"newSessionsRevision,omitempty"`
	// DesiredRevision is status.desiredRevision; kagent clears it when it
	// can't compile the latest change.
	DesiredRevision string    `json:"desiredRevision,omitempty"`
	Created         time.Time `json:"created,omitzero"`
	InlineTemplate  bool      `json:"inlineTemplate"`
	HarnessType     string    `json:"harnessType,omitempty"`
	// Model is the root template's ModelConfig.
	Model      *DependencyState `json:"model,omitempty"`
	MCPServers MCPSummary       `json:"mcpServers"`
	References []Reference      `json:"references"`
	// ToolsNotOffered are tools an MCP binding asks for that the server's
	// discovery, which succeeded, did not publish.
	ToolsNotOffered []ToolAsk `json:"toolsNotOffered,omitempty"`
	// Incomplete: an intermediate object (the template, a subagent template,
	// the Harness) was not read, or the tree was cut at MaxTemplates or
	// MaxDepth, so References may not be all of them.
	Incomplete bool `json:"incomplete,omitempty"`
	// Cycle names subagent templates that form a cycle, which kagent rejects.
	Cycle []string `json:"cycle,omitempty"`
}

// ToolAsk is a tool asked of one MCP server that it doesn't offer.
type ToolAsk struct {
	Server ObjectRef   `json:"server"`
	Tool   string      `json:"tool"`
	Agents []ObjectRef `json:"agents,omitempty"`
}

// AffectedAgent is one Agent a dependency's finding concerns.
type AffectedAgent struct {
	Agent  ObjectRef `json:"agent"`
	Effect string    `json:"effect"`
	// Roles are how the Agent's latest change uses the dependency.
	Roles []string `json:"roles"`
	// Through are the intermediate kinds the Agent's use passes through
	// (AgentTemplate, Harness), on the path through fewest: reading them is
	// part of the evidence that the Agent uses it.
	Through []string `json:"through,omitempty"`
}

// Impact is the Agents a dependency is used by, de-duplicated across paths.
type Impact struct {
	// Agents lists up to UsedByCap of them; Count is every one observed.
	Agents []AffectedAgent `json:"agents"`
	Count  int             `json:"count"`
	// LowerBound: some Agents, templates or Harnesses were not read, so more
	// may use it.
	LowerBound bool   `json:"lowerBound"`
	Consumers  string `json:"consumers"`
	// ByRole counts the Agents by how they use it.
	ByRole map[string]int `json:"byRole,omitempty"`
}

// ObjectFinding is one finding with the object it is about: a reading's own
// Finding (the Issue Radar raises on the object), or one the workspace finds
// on a coordinate Agents name.
type ObjectFinding struct {
	ID           string    `json:"id"`
	Subject      ObjectRef `json:"subject"`
	Severity     Severity  `json:"severity"`
	Reason       string    `json:"reason"`
	KagentReason string    `json:"kagentReason,omitempty"`
	Message      string    `json:"message"`
	Action       string    `json:"action"`
	Since        time.Time `json:"since,omitzero"`
	// Secret is the API key Secret a ModelConfig's finding is about;
	// findings naming one Secret group under it.
	Secret *ObjectRef `json:"secret,omitempty"`
	// Cause is set on an Agent's finding whose message names the dependency
	// that blocked its latest change.
	Cause *Cause `json:"cause,omitempty"`
	// Affects is set on a dependency's finding.
	Affects *Impact `json:"affects,omitempty"`
}

func findingID(subject ObjectRef, reason string) string { return subject.Key() + "/" + reason }

// HeaderSource is one RemoteMCPServer header: its name and where its value
// comes from. A literal value is never carried.
type HeaderSource struct {
	Name string `json:"name"`
	// From is the Secret or ConfigMap the value comes from; Inline when the
	// spec holds it literally.
	From   *ObjectRef `json:"from,omitempty"`
	Key    string     `json:"key,omitempty"`
	Inline bool       `json:"inline,omitempty"`
}

// MCPServerView is one RemoteMCPServer.
type MCPServerView struct {
	Ref     ObjectRef   `json:"ref"`
	Reading ReadingView `json:"reading"`
	// URL without user info, query or fragment.
	URL      string `json:"url,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	// ToolsOffered are the tools kagent's controller discovered; known only
	// when it discovered successfully.
	ToolsOffered      []string `json:"toolsOffered"`
	ToolsOfferedKnown bool     `json:"toolsOfferedKnown"`
	// Failing: kagent's controller can't discover its tools or didn't accept
	// it, as last reported.
	Failing bool           `json:"failing"`
	Headers []HeaderSource `json:"headers,omitempty"`
	UsedBy  Impact         `json:"usedBy"`
	// AskedNotOffered are tools Agents ask of it that its discovery didn't
	// publish.
	AskedNotOffered []ToolAsk `json:"askedNotOffered,omitempty"`
}

// Credential is how a ModelConfig authenticates, by reference only.
type Credential struct {
	// Mode: apiKeySecret, actorJwt, tokenPassthrough or none.
	Mode   string     `json:"mode"`
	Secret *ObjectRef `json:"secret,omitempty"`
	Key    string     `json:"key,omitempty"`
}

// ModelView is one ModelConfig.
type ModelView struct {
	Ref        ObjectRef   `json:"ref"`
	Reading    ReadingView `json:"reading"`
	Provider   string      `json:"provider,omitempty"`
	Model      string      `json:"model,omitempty"`
	Credential Credential  `json:"credential"`
	UsedBy     Impact      `json:"usedBy"`
}

// ProviderView is one ModelProviderConfig, which kagent 1.0.1 reports no
// status for.
type ProviderView struct {
	Ref      ObjectRef   `json:"ref"`
	Reading  ReadingView `json:"reading"`
	Provider string      `json:"provider,omitempty"`
}

// Workspace is the kagent workspace composed over one caller's inventory.
type Workspace struct {
	Agents         []AgentView     `json:"agents"`
	MCPServers     []MCPServerView `json:"mcpServers"`
	Models         []ModelView     `json:"models"`
	ModelProviders []ProviderView  `json:"modelProviders"`
	// Findings are atomic: one per object and reason, before any folding.
	Findings []ObjectFinding `json:"findings"`

	ix     *index
	uses   map[string][]use
	walks  map[string]agentWalk
	agents map[string]*agentState
}

type use struct {
	agent   ObjectRef
	role    string
	through []string
	state   string
}

type agentState struct {
	reading Reading
	cause   *Cause
	// prepared: the latest change is what new sessions run.
	prepared bool
}

// Compose reads every object in inv, walks each Agent's declared references
// and joins them: each dependency's finding lists the Agents that use it,
// each Agent's blocked finding names its cause where kagent's message is one
// Radar recognizes, and a coordinate Agents name that the read proves absent,
// or whose policy refuses them, is a finding of its own.
func Compose(inv Inventory) *Workspace {
	ix := newIndex(inv)
	w := &Workspace{ix: ix, uses: map[string][]use{}, walks: map[string]agentWalk{}, agents: map[string]*agentState{},
		Agents: []AgentView{}, MCPServers: []MCPServerView{}, Models: []ModelView{}, ModelProviders: []ProviderView{}, Findings: []ObjectFinding{}}
	readings := map[string]Reading{}
	read := func(u *unstructured.Unstructured) Reading {
		key := refOf(u).Key()
		if r, ok := readings[key]; ok {
			return r
		}
		r, _ := Read(u, inv.Now)
		readings[key] = r
		return r
	}

	for _, a := range ix.items(KindAgent) {
		ref := refOf(a)
		walk := ix.walkAgent(a)
		for i := range walk.refs {
			ref := &walk.refs[i]
			for j := range ref.Tools {
				ref.Tools[j] = RedactURLsInText(ref.Tools[j])
			}
			if ReportsStatus(ref.Target.Kind) {
				if u, _, _ := ix.resolve(ref.Target); u != nil {
					v := read(u).View()
					ref.Reading = &v
				}
			}
		}
		w.walks[ref.Key()] = walk
		reading := read(a)
		state := &agentState{reading: reading, prepared: reading.State == StateReady}
		if reading.State == StateBlocked || reading.State == StateFailed {
			state.cause = matchCause(reading.Condition, reading.Message, walk.refs)
		}
		w.agents[ref.Key()] = state
		for _, r := range walk.refs {
			w.uses[r.Target.Key()] = append(w.uses[r.Target.Key()], use{agent: ref, role: r.Role, through: through(r), state: r.State})
		}
	}

	for _, a := range ix.items(KindAgent) {
		w.Agents = append(w.Agents, w.agentView(a, read))
		for _, f := range read(a).Findings() {
			of := objectFinding(refOf(a), f)
			if isVerdict(f.Reason) {
				of.Cause = w.agents[refOf(a).Key()].cause
			}
			w.Findings = append(w.Findings, of)
		}
	}
	for _, m := range ix.items(KindModelConfig) {
		reading := read(m)
		view := ModelView{Ref: refOf(m), Reading: reading.View(), Credential: credentialOf(m), UsedBy: w.Impact(refOf(m), "")}
		view.Provider, _, _ = unstructured.NestedString(m.Object, "spec", "provider")
		view.Provider = RedactURLsInText(view.Provider)
		view.Model, _, _ = unstructured.NestedString(m.Object, "spec", "model")
		view.Model = RedactURLsInText(view.Model)
		w.Models = append(w.Models, view)
		for _, f := range reading.Findings() {
			of := objectFinding(view.Ref, f)
			impact := w.Impact(view.Ref, f.Reason)
			of.Affects = &impact
			if f.Reason == ReasonAPIKeySecretMissing && view.Credential.Secret != nil {
				of.Secret = view.Credential.Secret
			}
			w.Findings = append(w.Findings, of)
		}
	}
	for _, s := range ix.items(KindRemoteMCPServer) {
		reading := read(s)
		view := w.serverView(s, reading)
		w.MCPServers = append(w.MCPServers, view)
		for _, f := range reading.Findings() {
			of := objectFinding(view.Ref, f)
			impact := w.Impact(view.Ref, f.Reason)
			of.Affects = &impact
			w.Findings = append(w.Findings, of)
		}
	}
	for _, p := range ix.items(KindModelProviderConfig) {
		view := ProviderView{Ref: refOf(p), Reading: read(p).View()}
		view.Provider, _, _ = unstructured.NestedString(p.Object, "spec", "type")
		view.Provider = RedactURLsInText(view.Provider)
		w.ModelProviders = append(w.ModelProviders, view)
		for _, f := range read(p).Findings() {
			w.Findings = append(w.Findings, objectFinding(view.Ref, f))
		}
	}
	for _, t := range ix.items(KindSandboxTemplate) {
		for _, f := range read(t).Findings() {
			w.Findings = append(w.Findings, objectFinding(refOf(t), f))
		}
	}
	w.Findings = append(w.Findings, w.coordinateFindings()...)
	w.addToolAsks()
	return w
}

func isVerdict(reason string) bool {
	return reason == ReasonNoPreparedRevision || reason == ReasonChangeBlocked || reason == ReasonChangeFailed
}

func through(r Reference) []string {
	var out []string
	if r.From.Kind == KindAgentTemplate {
		out = append(out, KindAgentTemplate)
	}
	if r.From.Kind == KindHarness || r.Role == RoleHarness {
		out = append(out, KindHarness)
	}
	if r.Role == RoleTemplate || r.Role == RoleSubagentTemplate {
		out = append(out, KindAgentTemplate)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func objectFinding(subject ObjectRef, f Finding) ObjectFinding {
	of := ObjectFinding{ID: findingID(subject, f.Reason), Subject: subject, Severity: f.Severity, Reason: f.Reason, KagentReason: f.KagentReason, Message: f.Message, Action: f.Action}
	if f.SinceKnown {
		of.Since = f.Since
	}
	return of
}

func (w *Workspace) agentView(a *unstructured.Unstructured, read func(*unstructured.Unstructured) Reading) AgentView {
	ref := refOf(a)
	walk := w.walks[ref.Key()]
	reading := read(a)
	rev := ReadAgentRevisions(a)
	view := AgentView{Ref: ref, Reading: reading.View(), LatestChange: latestChangeOf(reading),
		NewSessionsRevision: rev.LatestSuccessful, DesiredRevision: rev.Desired, Created: a.GetCreationTimestamp().UTC(),
		InlineTemplate: walk.inline, HarnessType: walk.harnessType, References: walk.refs, Incomplete: walk.incomplete, Cycle: walk.cycle}
	if view.References == nil {
		view.References = []Reference{}
	}
	servers := map[string]bool{}
	for _, r := range walk.refs {
		switch {
		case r.Role == RoleModel && view.Model == nil:
			view.Model = &DependencyState{Ref: r.Target, State: r.State, Unread: r.Unread}
			if u, _, _ := w.ix.resolve(r.Target); u != nil {
				v := read(u).View()
				view.Model.Reading = &v
			}
		case (r.Role == RoleMCPServer || r.Role == RoleSubagentMCPServer) && !servers[r.Target.Key()]:
			servers[r.Target.Key()] = true
			view.MCPServers.Declared++
			switch r.State {
			case RefAbsent:
				view.MCPServers.Absent++
			case RefUnread:
				view.MCPServers.Unread++
			case RefNotPermitted:
				view.MCPServers.NotPermitted++
			}
			if u, _, _ := w.ix.resolve(r.Target); u != nil && failing(read(u)) {
				view.MCPServers.Failing++
			}
		}
	}
	return view
}

// failing: a RemoteMCPServer whose tools kagent's controller can't discover,
// or that it didn't accept, including as last reported for an earlier
// generation.
func failing(r Reading) bool {
	if r.State == StateStale && r.Previous != nil {
		r = *r.Previous
	}
	return r.State == StateFailed || r.State == StateNotReady
}

// Impact is the Agents that use target, each with what reason's finding on it
// means for that Agent: a coordinate's reason (missing, not permitted), or ""
// and any other reason for the object's own failure.
func (w *Workspace) Impact(target ObjectRef, reason string) Impact {
	return w.impact(target, reason, UsedByCap)
}

// AffectedAgents is every Agent Impact counts, uncapped.
func (w *Workspace) AffectedAgents(target ObjectRef, reason string) []AffectedAgent {
	return w.impact(target, reason, -1).Agents
}

func (w *Workspace) impact(target ObjectRef, reason string, limit int) Impact {
	out := Impact{Agents: []AffectedAgent{}}
	byAgent := map[string]*AffectedAgent{}
	first := map[string]bool{}
	var order []string
	for _, u := range w.uses[target.Key()] {
		key := u.agent.Key()
		a := byAgent[key]
		if a == nil {
			a = &AffectedAgent{Agent: u.agent}
			byAgent[key] = a
			first[key] = true
			order = append(order, key)
		}
		if !slices.Contains(a.Roles, u.role) {
			a.Roles = append(a.Roles, u.role)
		}
		// One readable path is evidence enough: keep the one through fewest
		// objects.
		if first[key] || len(u.through) < len(a.Through) {
			a.Through = u.through
		}
		first[key] = false
	}
	sort.Strings(order)
	server := target.Kind == KindRemoteMCPServer
	var serverFailing bool
	if server {
		if u, _, _ := w.ix.resolve(target); u != nil {
			r, _ := Read(u, w.ix.inv.Now)
			serverFailing = failing(r)
		}
	}
	for _, key := range order {
		a := byAgent[key]
		slices.Sort(a.Roles)
		a.Through = slices.Clone(a.Through)
		state := w.agents[key]
		switch {
		case state != nil && state.cause != nil && state.cause.Target == target && state.cause.Form == formFor(reason):
			a.Effect = EffectBlocked
		case server && serverFailing && state != nil && state.prepared:
			a.Effect = EffectMayRunWithoutTools
		default:
			a.Effect = EffectUsesIt
		}
		out.Count++
		if out.ByRole == nil {
			out.ByRole = map[string]int{}
		}
		for _, role := range a.Roles {
			out.ByRole[role]++
		}
		if limit < 0 || len(out.Agents) < limit {
			out.Agents = append(out.Agents, *a)
		}
	}
	out.LowerBound = !w.consumersComplete(target.Kind)
	switch {
	case out.Count > 0:
		out.Consumers = ConsumersSeen
	case out.LowerBound:
		out.Consumers = ConsumersNoneSeen
	default:
		out.Consumers = ConsumersNone
	}
	return out
}

func formFor(reason string) string {
	switch reason {
	case ReasonDependencyMissing:
		return FormNotFound
	case ReasonDependencyNotPermitted:
		return FormNotPermitted
	}
	return FormFailed
}

// consumersComplete: every object that could name a target of kind was read
// in scope, and every Agent's tree was walked to its end.
func (w *Workspace) consumersComplete(kind string) bool {
	if !w.ix.complete(KindAgent) || !w.ix.complete(KindAgentTemplate) {
		return false
	}
	if kind == KindModelConfig && !w.ix.complete(KindHarness) {
		return false
	}
	for _, walk := range w.walks {
		if walk.incomplete {
			return false
		}
	}
	return true
}

// coordinateFindings are the targets Agents name that the read proves absent,
// and the present ones whose sharing policy refuses some of them.
func (w *Workspace) coordinateFindings() []ObjectFinding {
	type refused struct {
		target     ObjectRef
		namespaces []string
	}
	missing := map[string]ObjectRef{}
	notPermitted := map[string]*refused{}
	for _, walk := range w.walks {
		for _, r := range walk.refs {
			switch {
			case r.State == RefAbsent && r.Target.Group == Group && r.Target.Kind != KindEgressPolicy:
				missing[r.Target.Key()] = r.Target
			case r.State == RefNotPermitted:
				n := notPermitted[r.Target.Key()]
				if n == nil {
					n = &refused{target: r.Target}
					notPermitted[r.Target.Key()] = n
				}
				if !slices.Contains(n.namespaces, r.From.Namespace) {
					n.namespaces = append(n.namespaces, r.From.Namespace)
				}
			}
		}
	}
	var out []ObjectFinding
	for _, key := range sortedKeys(missing) {
		t := missing[key]
		f := ObjectFinding{ID: findingID(t, ReasonDependencyMissing), Subject: t, Severity: SeverityWarning, Reason: ReasonDependencyMissing,
			Message: t.Kind + " " + t.NamespacedName() + " doesn't exist, and Agents name it.",
			Action:  "Create " + t.Kind + " " + t.NamespacedName() + ", or point the Agents that name it at an existing one."}
		impact := w.Impact(t, ReasonDependencyMissing)
		f.Affects = &impact
		out = append(out, f)
	}
	for _, key := range sortedKeys(notPermitted) {
		n := notPermitted[key]
		sort.Strings(n.namespaces)
		list := strings.Join(n.namespaces, ", ")
		noun := "namespace "
		if len(n.namespaces) > 1 {
			noun = "namespaces "
		}
		f := ObjectFinding{ID: findingID(n.target, ReasonDependencyNotPermitted), Subject: n.target, Severity: SeverityWarning, Reason: ReasonDependencyNotPermitted,
			Message: n.target.Kind + " " + n.target.NamespacedName() + "'s allowedNamespaces refuses references from " + noun + list + ", where Agents name it.",
			Action:  "Allow " + noun + list + " in " + n.target.Kind + " " + n.target.NamespacedName() + "'s allowedNamespaces, or point those Agents at one they may use."}
		impact := w.Impact(n.target, ReasonDependencyNotPermitted)
		f.Affects = &impact
		out = append(out, f)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (w *Workspace) serverView(s *unstructured.Unstructured, reading Reading) MCPServerView {
	ref := refOf(s)
	view := MCPServerView{Ref: ref, Reading: reading.View(), ToolsOffered: []string{}, Failing: failing(reading), UsedBy: w.Impact(ref, "")}
	if url, _, _ := unstructured.NestedString(s.Object, "spec", "url"); url != "" {
		view.URL = StripURLCredentials(url)
	}
	view.Protocol, _, _ = unstructured.NestedString(s.Object, "spec", "protocol")
	if reading.State == StateReady {
		view.ToolsOffered, view.ToolsOfferedKnown = DiscoveredTools(s), true
		for i := range view.ToolsOffered {
			view.ToolsOffered[i] = RedactURLsInText(view.ToolsOffered[i])
		}
	}
	headers, _, _ := unstructured.NestedSlice(s.Object, "spec", "headersFrom")
	for _, raw := range headers {
		h, _ := raw.(map[string]any)
		name, _ := h["name"].(string)
		src := HeaderSource{Name: name}
		if from, ok := h["valueFrom"].(map[string]any); ok {
			kind, _ := from["type"].(string)
			fromName, _ := from["name"].(string)
			src.Key, _ = from["key"].(string)
			if kind == KindSecret || kind == KindConfigMap {
				src.From = &ObjectRef{Kind: kind, Namespace: ref.Namespace, Name: fromName}
			}
		} else if _, literal := h["value"]; literal {
			src.Inline = true
		}
		view.Headers = append(view.Headers, src)
	}
	return view
}

func credentialOf(m *unstructured.Unstructured) Credential {
	spec, _, _ := unstructured.NestedMap(m.Object, "spec")
	if secret, _ := spec["apiKeySecret"].(string); secret != "" {
		key, _ := spec["apiKeySecretKey"].(string)
		return Credential{Mode: "apiKeySecret", Secret: &ObjectRef{Kind: KindSecret, Namespace: m.GetNamespace(), Name: secret}, Key: key}
	}
	if _, ok := spec["actorJwt"]; ok {
		return Credential{Mode: "actorJwt"}
	}
	if pass, _ := spec["tokenPassthrough"].(bool); pass {
		return Credential{Mode: "tokenPassthrough"}
	}
	return Credential{Mode: "none"}
}

// addToolAsks records the tools Agents ask of MCP servers whose discovery
// succeeded but did not publish them. kagent still prepares such an Agent.
func (w *Workspace) addToolAsks() {
	offered := map[string][]string{}
	for i := range w.MCPServers {
		if w.MCPServers[i].ToolsOfferedKnown {
			offered[w.MCPServers[i].Ref.Key()] = w.MCPServers[i].ToolsOffered
		}
	}
	asks := map[string]map[string][]ObjectRef{}
	for i := range w.Agents {
		a := &w.Agents[i]
		seen := map[string]bool{}
		for _, r := range a.References {
			tools, ok := offered[r.Target.Key()]
			if !ok || (r.Role != RoleMCPServer && r.Role != RoleSubagentMCPServer) {
				continue
			}
			for _, tool := range r.Tools {
				if slices.Contains(tools, tool) || seen[r.Target.Key()+"/"+tool] {
					continue
				}
				seen[r.Target.Key()+"/"+tool] = true
				a.ToolsNotOffered = append(a.ToolsNotOffered, ToolAsk{Server: r.Target, Tool: tool})
				if asks[r.Target.Key()] == nil {
					asks[r.Target.Key()] = map[string][]ObjectRef{}
				}
				asks[r.Target.Key()][tool] = append(asks[r.Target.Key()][tool], a.Ref)
			}
		}
	}
	for i := range w.MCPServers {
		s := &w.MCPServers[i]
		byTool := asks[s.Ref.Key()]
		for _, tool := range sortedKeys(byTool) {
			s.AskedNotOffered = append(s.AskedNotOffered, ToolAsk{Server: s.Ref, Tool: tool, Agents: byTool[tool]})
		}
	}
}

// In is the workspace narrowed to namespaces (nil: all): the lists and the
// findings whose subject is there. References and impact keep what was read
// outside them.
func (w *Workspace) In(namespaces []string) *Workspace {
	if namespaces == nil {
		return w
	}
	in := func(ns string) bool { return slices.Contains(namespaces, ns) }
	out := *w
	out.Agents = keep(w.Agents, func(a AgentView) bool { return in(a.Ref.Namespace) })
	out.MCPServers = keep(w.MCPServers, func(s MCPServerView) bool { return in(s.Ref.Namespace) })
	out.Models = keep(w.Models, func(m ModelView) bool { return in(m.Ref.Namespace) })
	out.ModelProviders = keep(w.ModelProviders, func(p ProviderView) bool { return in(p.Ref.Namespace) })
	out.Findings = keep(w.Findings, func(f ObjectFinding) bool { return in(f.Subject.Namespace) })
	return &out
}

func keep[T any](items []T, ok func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		if ok(it) {
			out = append(out, it)
		}
	}
	return out
}
