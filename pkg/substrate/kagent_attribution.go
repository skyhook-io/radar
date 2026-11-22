package substrate

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// KagentGroup is the API group of kagent's Agents.
const KagentGroup = "api.kagent.dev"

// AgentRef names a kagent Agent.
type AgentRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (a AgentRef) Key() string { return a.Namespace + "/" + a.Name }

// KagentAgent is one kagent Agent as the caller read it from Kubernetes.
// The revisions are status.desiredRevision and
// status.latestSuccessfulRevision, each a revision's full SHA-256 digest;
// empty when the Agent has none (it never compiled, or none succeeded).
type KagentAgent struct {
	Namespace                string `json:"namespace"`
	Name                     string `json:"name"`
	UID                      string `json:"uid,omitempty"`
	DesiredRevision          string `json:"desiredRevision,omitempty"`
	LatestSuccessfulRevision string `json:"latestSuccessfulRevision,omitempty"`
	// Created is the Agent's metadata.creationTimestamp; zero when unknown.
	Created time.Time `json:"created,omitzero"`
}

func (a KagentAgent) Ref() AgentRef { return AgentRef{Namespace: a.Namespace, Name: a.Name} }

// Kinds of kagent ActorTemplate names.
const (
	// KagentTemplateRevision is an Agent revision's template:
	// <agent name, normalized and cut to 50>-<first 12 hex of the revision>.
	KagentTemplateRevision = "agentRevision"
	// KagentTemplateSandbox is a kagent sandbox's template: sandbox-<40 hex>.
	KagentTemplateSandbox = "sandbox"
)

// KagentTemplateName is what a template's name says under kagent's naming.
type KagentTemplateName struct {
	Kind string
	// Prefix is the Agent name as kagent shortens it (agentRevision only).
	Prefix string
	// Revision is the hex the name carries: a revision's first 12 digits
	// (agentRevision), or a sandbox revision's first 40.
	Revision string
}

// kagent's template names (go/core/internal/substrate/actor_template.go
// revisionActorTemplateName, translator.RevisionID.Short: 6 bytes;
// go/core/internal/substrate/sandbox.go: "sandbox-" + revision[:40]).
const (
	kagentPrefixMax     = 50
	kagentShortRevision = 12
)

var (
	kagentSandboxTemplate  = regexp.MustCompile(`^sandbox-[0-9a-f]{40}$`)
	kagentRevisionTemplate = regexp.MustCompile(`^(.+)-([0-9a-f]{12})$`)
	kagentRevisionDigest   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ParseKagentTemplateName reads a template name under kagent's naming; false
// when kagent could not have made it. An agentRevision name's prefix must be
// one KagentTemplatePrefix returns for some Agent name.
func ParseKagentTemplateName(name string) (KagentTemplateName, bool) {
	if kagentSandboxTemplate.MatchString(name) {
		return KagentTemplateName{Kind: KagentTemplateSandbox, Revision: strings.TrimPrefix(name, "sandbox-")}, true
	}
	m := kagentRevisionTemplate.FindStringSubmatch(name)
	// Agent names end in a letter or digit, and a cut trims hyphens, so a
	// prefix never ends in one.
	if m == nil || KagentTemplatePrefix(m[1]) != m[1] || strings.HasSuffix(m[1], "-") {
		return KagentTemplateName{}, false
	}
	return KagentTemplateName{Kind: KagentTemplateRevision, Prefix: m[1], Revision: m[2]}, true
}

// KagentTemplatePrefix is the part of an Agent's name kagent puts in its
// revision templates' names: lowercased, underscores as hyphens, cut to 63
// and then to 50 characters, trailing hyphens trimmed after each cut
// (truncateDNS1123, truncateDNS1123To).
func KagentTemplatePrefix(agentName string) string {
	return cutDNS1123(cutDNS1123(agentName, 63), kagentPrefixMax)
}

func cutDNS1123(value string, limit int) string {
	value = strings.ToLower(strings.ReplaceAll(value, "_", "-"))
	if len(value) > limit {
		value = strings.TrimRight(value[:limit], "-")
	}
	return value
}

// KagentRevisionTemplate is the template kagent creates in the Agent's
// namespace for one of its revisions; "" when revision is not a revision
// digest.
func KagentRevisionTemplate(agentName, revision string) string {
	if !kagentRevisionDigest.MatchString(revision) {
		return ""
	}
	return KagentTemplatePrefix(agentName) + "-" + revision[:kagentShortRevision]
}

// What Radar can say about a kagent session's actor (KagentAttribution.Kind).
const (
	// KagentAgentSession: its template is exactly the template of a current
	// revision of one Agent the caller can read, or provably of an earlier
	// one (KagentAttribution.EarlierRevision).
	KagentAgentSession = "agentSession"
	// KagentSandbox: its template is a kagent sandbox's.
	KagentSandbox = "sandbox"
	// KagentUnattributed: neither; Reason says why.
	KagentUnattributed = "unattributed"
)

// Why a kagent session's actor is not attributed (KagentAttribution.Reason).
const (
	// KagentEarlierRevision: the template is named for an Agent the caller
	// can read, but for neither of its current revisions, and Radar can't
	// prove it is that Agent's: the template predates the Agent, a time is
	// missing, or the name was cut so another Agent could share it. A deleted
	// Agent of the same name leaves templates like this, whose revisions
	// differ because they digest its UID. Candidates name the Agents as a
	// hint, never as a match.
	KagentEarlierRevision = "earlierRevision"
	// KagentAgentNotReadable: the caller can't read Agents in the template's
	// namespace, or Radar has not read them there.
	KagentAgentNotReadable = "agentNotReadable"
	// KagentNoAgent: no Agent the caller read in that namespace has a name
	// that produces the template.
	KagentNoAgent = "noAgent"
	// KagentAmbiguous: current revisions of more than one Agent produce the
	// template's name; Candidates name them.
	KagentAmbiguous = "ambiguous"
	// KagentTemplateUnknown: kagent's naming could not have produced the
	// template, or it is in another atespace than the actor.
	KagentTemplateUnknown = "templateUnknown"
)

// KagentAttribution says which Agent, if any, an actor or template belongs
// to, judged against the Agents one caller can read. It is computed per
// request and never kept in Radar's shared Substrate read.
type KagentAttribution struct {
	// Kind is agentSession, sandbox or unattributed.
	Kind  string    `json:"kind"`
	Agent *AgentRef `json:"agent,omitempty"`
	// Revision is the full digest of the Agent revision the template is for
	// (agentSession only); for an earlier revision, only the 12 hex digits
	// its template's name carries, since the Agent no longer names the digest.
	Revision string `json:"revision,omitempty"`
	// EarlierRevision: the template is the Agent's but for neither current
	// revision. Sessions keep the revision they started on, and any change to
	// kagent's configuration gives every Agent a new one, so this is where
	// most sessions run.
	EarlierRevision bool `json:"earlierRevision,omitempty"`
	// NewSessions: Revision is the Agent's latestSuccessfulRevision, the one
	// kagent starts new sessions on.
	NewSessions bool `json:"newSessions,omitempty"`
	// Desired: Revision is the Agent's desiredRevision.
	Desired bool `json:"desired,omitempty"`
	// Reason says why it is unattributed.
	Reason string `json:"reason,omitempty"`
	// Candidates are the Agents an earlierRevision template is named for, or
	// those an ambiguous one matches. A hint: never counted as their sessions.
	Candidates []AgentRef `json:"candidates,omitempty"`
}

// KagentActor is a kagent session's actor with its attribution for the
// caller: on actor rows and a worker's assigned actors.
type KagentActor struct {
	SessionID string `json:"sessionId"`
	KagentAttribution
}

type kagentMatch struct {
	agent       KagentAgent
	revision    string
	newSessions bool
	desired     bool
}

// KagentIndex matches actors and templates against the Agents one caller
// read. Build one per request; it memoizes by template and is not safe for
// concurrent use.
type KagentIndex struct {
	// byTemplate: the templates the Agents' current revisions produce.
	byTemplate map[Ref][]kagentMatch
	// byPrefix: Agents by namespace and template prefix.
	byPrefix map[string][]AgentRef
	agents   map[string]KagentAgent
	readable func(namespace string) bool
	memo     map[Ref]KagentAttribution
	// created is each template's Substrate createTime, where known.
	created map[Ref]time.Time
}

// NewKagentIndex indexes agents. readable reports whether every Agent in a
// namespace was read for this caller; only there can Radar say none
// produces a template. templates are the templates read, whose creation
// times prove an earlier revision's template is an Agent's.
func NewKagentIndex(agents []KagentAgent, readable func(namespace string) bool, templates []Template) *KagentIndex {
	ix := &KagentIndex{
		byTemplate: map[Ref][]kagentMatch{}, byPrefix: map[string][]AgentRef{}, agents: make(map[string]KagentAgent, len(agents)),
		readable: readable, memo: map[Ref]KagentAttribution{}, created: map[Ref]time.Time{},
	}
	for _, t := range templates {
		if !t.CreateTime.IsZero() {
			ix.created[t.Ref()] = t.CreateTime
		}
	}
	for _, a := range agents {
		ix.agents[a.Ref().Key()] = a
		prefix := a.Namespace + "/" + KagentTemplatePrefix(a.Name)
		ix.byPrefix[prefix] = append(ix.byPrefix[prefix], a.Ref())
		for _, rev := range []string{a.LatestSuccessfulRevision, a.DesiredRevision} {
			name := KagentRevisionTemplate(a.Name, rev)
			if name == "" {
				continue
			}
			ref := Ref{Atespace: a.Namespace, Name: name}
			matches := ix.byTemplate[ref]
			if slices.ContainsFunc(matches, func(m kagentMatch) bool { return m.agent.Ref() == a.Ref() && m.revision == rev }) {
				continue
			}
			ix.byTemplate[ref] = append(matches, kagentMatch{agent: a, revision: rev, newSessions: rev == a.LatestSuccessfulRevision, desired: rev == a.DesiredRevision})
		}
	}
	for k := range ix.byPrefix {
		slices.SortFunc(ix.byPrefix[k], compareAgentRefs)
	}
	return ix
}

func compareAgentRefs(a, b AgentRef) int { return strings.Compare(a.Key(), b.Key()) }

func (ix *KagentIndex) canRead(namespace string) bool {
	return ix.readable != nil && ix.readable(namespace)
}

// Agent is a readable Agent by reference.
func (ix *KagentIndex) Agent(ref AgentRef) (KagentAgent, bool) {
	a, ok := ix.agents[ref.Key()]
	return a, ok
}

// Template attributes a template, as an actor running on it would be.
func (ix *KagentIndex) Template(t Ref) KagentAttribution {
	if got, ok := ix.memo[t]; ok {
		return got
	}
	got := ix.template(t)
	ix.memo[t] = got
	return got
}

func (ix *KagentIndex) template(t Ref) KagentAttribution {
	parsed, ok := ParseKagentTemplateName(t.Name)
	if ok && parsed.Kind == KagentTemplateSandbox {
		return KagentAttribution{Kind: KagentSandbox}
	}
	if !ix.canRead(t.Atespace) {
		return KagentAttribution{Kind: KagentUnattributed, Reason: KagentAgentNotReadable}
	}
	if matches := ix.byTemplate[t]; len(matches) > 0 {
		var agents []AgentRef
		for _, m := range matches {
			if !slices.Contains(agents, m.agent.Ref()) {
				agents = append(agents, m.agent.Ref())
			}
		}
		if len(agents) > 1 {
			slices.SortFunc(agents, compareAgentRefs)
			return KagentAttribution{Kind: KagentUnattributed, Reason: KagentAmbiguous, Candidates: agents}
		}
		// One Agent: its two revisions name the template only when they
		// share their first 12 digits, and then both flags hold.
		got := KagentAttribution{Kind: KagentAgentSession, Agent: &agents[0], Revision: matches[0].revision}
		for _, m := range matches {
			got.NewSessions = got.NewSessions || m.newSessions
			got.Desired = got.Desired || m.desired
			if m.newSessions {
				got.Revision = m.revision
			}
		}
		return got
	}
	if !ok {
		return KagentAttribution{Kind: KagentUnattributed, Reason: KagentTemplateUnknown}
	}
	if named := ix.byPrefix[t.Atespace+"/"+parsed.Prefix]; len(named) > 0 {
		return ix.earlier(t, parsed, named)
	}
	return KagentAttribution{Kind: KagentUnattributed, Reason: KagentNoAgent}
}

// kagentCreationMargin is how much later than an Agent's creationTimestamp
// Substrate must have created a template for it to be that Agent's. The
// timestamp is truncated to the second, so an Agent deleted and recreated
// within one second can carry a time earlier than its predecessor's last
// template; and the two times come from different clocks (Substrate's API
// server, the Kubernetes API server). One second for the precision, one for
// skew.
const kagentCreationMargin = 2 * time.Second

// earlier attributes a template named for readable Agents but for none of
// their current revisions. It is exactly an Agent's when Substrate created it
// at least kagentCreationMargin after that Agent was created and no other
// Agent could have named it: a deleted Agent's templates were created before
// its deletion, so before any Agent that took its name later.
//
// The prefix must also be shorter than kagent's 50-character cut. At 50, a
// deleted Agent whose name only began with this one's (A's name plus "-old")
// was cut to the same prefix, whether or not this Agent's own name was cut.
// One case remains: a deleted Agent whose name continued this one's with
// hyphens alone up to the 50th character, which the cut trims away. Radar
// does not try to exclude it.
func (ix *KagentIndex) earlier(t Ref, parsed KagentTemplateName, named []AgentRef) KagentAttribution {
	hint := KagentAttribution{Kind: KagentUnattributed, Reason: KagentEarlierRevision, Candidates: slices.Clone(named)}
	created, ok := ix.created[t]
	if !ok {
		return hint
	}
	var after []KagentAgent
	for _, ref := range named {
		if a := ix.agents[ref.Key()]; !a.Created.IsZero() && !created.Before(a.Created.Add(kagentCreationMargin)) {
			after = append(after, a)
		}
	}
	switch {
	case len(after) > 1:
		refs := make([]AgentRef, 0, len(after))
		for _, a := range after {
			refs = append(refs, a.Ref())
		}
		return KagentAttribution{Kind: KagentUnattributed, Reason: KagentAmbiguous, Candidates: refs}
	case len(after) == 1 && len(named) == 1 && len(parsed.Prefix) < kagentPrefixMax:
		agent := after[0].Ref()
		return KagentAttribution{Kind: KagentAgentSession, Agent: &agent, Revision: parsed.Revision, EarlierRevision: true}
	}
	return hint
}

// Actor attributes a kagent session's actor; nil for any other actor. kagent
// creates a session's actor in its template's atespace, the Agent's
// namespace, so an actor elsewhere is not one of its Agents'.
func (ix *KagentIndex) Actor(a Actor) *KagentActor {
	if a.Kagent == nil {
		return nil
	}
	out := &KagentActor{SessionID: a.Kagent.SessionID}
	if a.Template.Atespace != a.Atespace {
		out.KagentAttribution = KagentAttribution{Kind: KagentUnattributed, Reason: KagentTemplateUnknown}
		return out
	}
	out.KagentAttribution = ix.Template(a.Template)
	return out
}

// KagentTemplate is a template row's attribution: set only when the template
// is an Agent revision's or a sandbox's, so other templates on a kagent
// cluster carry nothing.
func (ix *KagentIndex) KagentTemplate(t Ref) *KagentAttribution {
	got := ix.Template(t)
	if got.Kind == KagentUnattributed {
		return nil
	}
	return &got
}

// GroupByTemplate is actors by the template they run, each list in the order
// given.
func GroupByTemplate(actors []Actor) map[Ref][]Actor {
	out := map[Ref][]Actor{}
	for _, a := range actors {
		out[a.Template] = append(out[a.Template], a)
	}
	return out
}

// KagentPrefixKey keys KagentTemplatesByPrefix: atespace and template prefix.
func KagentPrefixKey(atespace, prefix string) string { return atespace + "/" + prefix }

// KagentTemplatesByPrefix lists the agentRevision templates among refs by
// atespace and prefix (KagentPrefixKey), each list sorted by name.
func KagentTemplatesByPrefix(refs []Ref) map[string][]Ref {
	out := map[string][]Ref{}
	seen := map[Ref]bool{}
	for _, t := range refs {
		if seen[t] {
			continue
		}
		seen[t] = true
		if p, ok := ParseKagentTemplateName(t.Name); ok && p.Kind == KagentTemplateRevision {
			key := KagentPrefixKey(t.Atespace, p.Prefix)
			out[key] = append(out[key], t)
		}
	}
	for k := range out {
		slices.SortFunc(out[k], func(a, b Ref) int { return strings.Compare(a.Name, b.Name) })
	}
	return out
}

// KagentAgentRevision is one current revision of an Agent and its template.
type KagentAgentRevision struct {
	Revision string `json:"revision"`
	Template Ref    `json:"template"`
	// NewSessions: the latestSuccessfulRevision, which new sessions start on.
	NewSessions bool `json:"newSessions,omitempty"`
	// Desired: the desiredRevision. While it differs from the latest
	// successful one, kagent has not prepared it, and new sessions still
	// start on the latest successful revision.
	Desired bool `json:"desired,omitempty"`
	// Ambiguous: another readable Agent's revision produces the same
	// template name, so its actors are counted for neither.
	Ambiguous bool `json:"ambiguous,omitempty"`
	// Sessions counts the session actors on the template, attributed to this
	// Agent; 0 when Ambiguous.
	Sessions int `json:"sessions"`
}

// KagentAgentSessions is what Substrate's actors say about one Agent's
// sessions.
type KagentAgentSessions struct {
	Agent     KagentAgent           `json:"agent"`
	Revisions []KagentAgentRevision `json:"revisions"`
	// Sessions counts the attributed session actors by state, on current and
	// earlier revisions alike.
	Sessions StateCounts `json:"sessions"`
	// OnEarlierRevisions is how many of Sessions run on an earlier revision's
	// template; the rest are on the Revisions.
	OnEarlierRevisions int `json:"onEarlierRevisions"`
	// Complete: every actor in the Agent's atespace was read, so the counts
	// are exact; otherwise they are lower bounds.
	Complete bool `json:"complete"`
	// NeedAttention counts the attributed sessions crashed or stuck in a
	// transition past StuckAfter; Attention holds the first few of them,
	// crashed first, then stuck, newest change first.
	NeedAttention int        `json:"needAttention"`
	Attention     []ActorRow `json:"attention"`
	// HintedEarlierRevisions counts session actors on templates named for this
	// Agent, for no current revision, that Radar can't prove are its: a hint,
	// not part of Sessions (KagentEarlierRevision).
	HintedEarlierRevisions int `json:"hintedEarlierRevisions"`
	// HintShared: some of those templates' names also fit another readable
	// Agent, which the same actors are hinted to as well.
	HintShared bool `json:"hintShared,omitempty"`
}

// KagentAttentionSample is how many sessions needing attention
// KagentAgentSessions lists.
const KagentAttentionSample = 5

// AgentSessions summarizes one readable Agent's sessions. actorsOn returns
// the actors on a template in the Agent's atespace; prefixTemplates are
// that atespace's agentRevision templates with the Agent's prefix
// (KagentTemplatesByPrefix). complete: every actor in the atespace was read.
func (ix *KagentIndex) AgentSessions(agent KagentAgent, actorsOn func(Ref) []Actor, prefixTemplates []Ref, complete bool, now time.Time, rc ResumeContext) KagentAgentSessions {
	out := KagentAgentSessions{Agent: agent, Revisions: []KagentAgentRevision{}, Sessions: NewStateCounts(), Complete: complete, Attention: []ActorRow{}}
	current := map[Ref]bool{}
	var attention []Actor
	for _, rev := range []string{agent.LatestSuccessfulRevision, agent.DesiredRevision} {
		name := KagentRevisionTemplate(agent.Name, rev)
		if name == "" {
			continue
		}
		t := Ref{Atespace: agent.Namespace, Name: name}
		if current[t] {
			continue
		}
		current[t] = true
		attr := ix.Template(t)
		// Both revisions name one template when they share their first 12
		// digits; it then carries both flags.
		row := KagentAgentRevision{Revision: rev, Template: t,
			NewSessions: name == KagentRevisionTemplate(agent.Name, agent.LatestSuccessfulRevision),
			Desired:     name == KagentRevisionTemplate(agent.Name, agent.DesiredRevision),
			Ambiguous:   attr.Reason == KagentAmbiguous}
		if attr.Kind == KagentAgentSession && attr.Agent != nil && *attr.Agent == agent.Ref() {
			for _, a := range actorsOn(t) {
				if a.Kagent == nil || a.Atespace != t.Atespace {
					continue
				}
				row.Sessions++
				out.Sessions.Add(a.State)
				if a.State == StateCrashed || IsStuck(a, now) {
					attention = append(attention, a)
				}
			}
		}
		out.Revisions = append(out.Revisions, row)
	}
	for _, t := range prefixTemplates {
		if current[t] {
			continue
		}
		attr := ix.Template(t)
		mine := attr.Kind == KagentAgentSession && attr.EarlierRevision && attr.Agent != nil && *attr.Agent == agent.Ref()
		hinted := attr.Reason == KagentEarlierRevision && slices.Contains(attr.Candidates, agent.Ref())
		if !mine && !hinted {
			continue
		}
		if hinted && len(attr.Candidates) > 1 {
			out.HintShared = true
		}
		for _, a := range actorsOn(t) {
			if a.Kagent == nil || a.Atespace != t.Atespace {
				continue
			}
			if hinted {
				out.HintedEarlierRevisions++
				continue
			}
			out.OnEarlierRevisions++
			out.Sessions.Add(a.State)
			if a.State == StateCrashed || IsStuck(a, now) {
				attention = append(attention, a)
			}
		}
	}
	out.NeedAttention = len(attention)
	slices.SortStableFunc(attention, func(a, b Actor) int {
		if ca, cb := a.State == StateCrashed, b.State == StateCrashed; ca != cb {
			if ca {
				return -1
			}
			return 1
		}
		return b.UpdateTime.Compare(a.UpdateTime)
	})
	for _, a := range attention[:min(len(attention), KagentAttentionSample)] {
		row := RowOf(a, now, rc)
		row.Kagent = ix.Actor(a)
		out.Attention = append(out.Attention, row)
	}
	return out
}

// KagentAttention rewrites the steps of rows about templates kagent owns for
// an Agent the caller can read: kagent creates one template per revision and
// rebuilds a failed golden snapshot itself (go/core/internal/controller/
// golden_retry.go: it deletes the failed template on a backoff and creates it
// again), so the step is never to recreate the template. rows is changed in
// place; templates are the read the rows came from.
func KagentAttention(rows []Attention, templates []Template, ix *KagentIndex) {
	causes := map[Ref]string{}
	for _, t := range templates {
		causes[t.Ref()] = GoldenOf(t).Cause
	}
	for i := range rows {
		r := &rows[i]
		if r.Rule == RuleCrashedActors {
			for j := range r.Groups {
				g := &r.Groups[j]
				if g.Kagent && g.Subject != nil {
					if attr := ix.Template(Ref{Atespace: g.Subject.Atespace, Name: g.Subject.Name}); attr.Kind == KagentAgentSession && attr.Agent != nil {
						g.NextStep = strings.Replace(g.NextStep, "These actors are kagent sessions", "These actors are sessions of kagent Agent "+attr.Agent.Key(), 1)
					}
				}
			}
			continue
		}
		if r.Rule != RuleGoldenFailed || r.Subject.Kind != "ActorTemplate" {
			continue
		}
		attr := ix.Template(Ref{Atespace: r.Subject.Atespace, Name: r.Subject.Name})
		if attr.Kind != KagentAgentSession || attr.Agent == nil {
			continue
		}
		r.Reading = strings.Replace(r.Reading, "Substrate does not retry a failed build.", "Substrate does not retry a failed build; kagent deletes the template and creates it again on a backoff.", 1)
		r.NextStep = KagentGoldenStep(*attr.Agent, causes[Ref{Atespace: r.Subject.Atespace, Name: r.Subject.Name}] == CauseMemoryLimit)
		r.Command, r.ThenCommand = "", ""
	}
}

// KagentGoldenStep is the step for a failed golden snapshot of an Agent's
// template. kagent sets every Agent's memory from its runtime defaults
// (go/core/internal/translator/compiler.go: result.Memory =
// settings.GetMemory()), which its Helm chart sets as
// config.runtimeDefaults.memory.
func KagentGoldenStep(agent AgentRef, memory bool) string {
	if memory {
		return "Raise config.runtimeDefaults.memory in kagent's Helm values; Agent " + agent.Key() + " then gets a new revision and template. Don't recreate this one."
	}
	return "Fix the cause on Agent " + agent.Key() + ": kagent owns this template and rebuilds it itself. The Agent's Ready condition says when it retries."
}

// KagentStepForAgent names the Agent in a step written for a kagent session's
// actor, when the session is attributed to one.
func KagentStepForAgent(step string, k *KagentActor) string {
	if k == nil || k.Kind != KagentAgentSession || k.Agent == nil {
		return step
	}
	return strings.Replace(step, "This actor is a kagent session", "This actor is a session of kagent Agent "+k.Agent.Key(), 1)
}
