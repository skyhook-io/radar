package substrate

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Names and revisions read from a live kagent 1.0 cluster.
const (
	helperRevision = "aa5e9426eb2ead1c0f65f5c97cf535a9f41038ecd7f8f2b9a7ecc6d925987f40"
	spikeRevision  = "8885899a4609043c33180db7c7e4e1141780af639de9c19842681e70eb8de1db"
	// brokenRevision is an edit of k8s-helper that kagent cannot prepare.
	brokenRevision  = "0b7f3c9e5d21a64f8e09b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2"
	sandboxTemplate = "sandbox-3f2c1b0a9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b"
	kagentSandboxed = "session-9b2d6c1e-4f3a-4e8b-9c7d-1a2b3c4d5e6f"
)

var (
	helper = KagentAgent{Namespace: "kagent", Name: "k8s-helper", UID: "47548047-6e64-491a-8475-7c7522620286",
		DesiredRevision: helperRevision, LatestSuccessfulRevision: helperRevision}
	spike  = KagentAgent{Namespace: "kagent", Name: "spike-agent", DesiredRevision: spikeRevision, LatestSuccessfulRevision: spikeRevision}
	broken = KagentAgent{Namespace: "kagent", Name: "broken-model"}
)

func everyNamespace(string) bool { return true }

func session(name, template string, state string) Actor {
	return Actor{Atespace: "kagent", Name: name, Template: Ref{"kagent", template}, State: state, UpdateTime: ago(time.Minute), Kagent: KagentSessionOf(name)}
}

func TestKagentTemplateNames(t *testing.T) {
	for _, tc := range []struct{ agent, revision, want string }{
		{"k8s-helper", helperRevision, "k8s-helper-aa5e9426eb2e"},
		{"spike-agent", spikeRevision, "spike-agent-8885899a4609"},
		// kagent lowercases, turns underscores into hyphens, then cuts to 50
		// and trims the hyphens a cut leaves.
		{"My_Agent", helperRevision, "my-agent-aa5e9426eb2e"},
		{strings.Repeat("a", 49) + "-tail", helperRevision, strings.Repeat("a", 49) + "-aa5e9426eb2e"},
		{strings.Repeat("b", 70), helperRevision, strings.Repeat("b", 50) + "-aa5e9426eb2e"},
		{"k8s-helper", "", ""},
		{"k8s-helper", "aa5e9426eb2e", ""},
		{"k8s-helper", strings.ToUpper(helperRevision), ""},
	} {
		if got := KagentRevisionTemplate(tc.agent, tc.revision); got != tc.want {
			t.Errorf("KagentRevisionTemplate(%q, %q) = %q, want %q", tc.agent, tc.revision, got, tc.want)
		}
	}
	for name, want := range map[string]KagentTemplateName{
		"k8s-helper-aa5e9426eb2e": {Kind: KagentTemplateRevision, Prefix: "k8s-helper", Revision: "aa5e9426eb2e"},
		sandboxTemplate:           {Kind: KagentTemplateSandbox, Revision: strings.TrimPrefix(sandboxTemplate, "sandbox-")},
		// A prefix kagent's cut could not leave.
		strings.Repeat("c", 51) + "-aa5e9426eb2e": {},
		"k8s-helper--aa5e9426eb2e":                {},
		"K8s-helper-aa5e9426eb2e":                 {},
		"k8s-helper-aa5e9426eb2":                  {},
		"lang":                                    {},
	} {
		got, ok := ParseKagentTemplateName(name)
		if ok != (want.Kind != "") || got != want {
			t.Errorf("ParseKagentTemplateName(%q) = %+v %v, want %+v", name, got, ok, want)
		}
	}
}

// The live cluster's names: a conversation on k8s-helper's revision, a
// kagent sandbox beside it, an unrelated actor, and a session on a template
// kagent did not make.
func TestKagentAttributionLiveNames(t *testing.T) {
	ix := NewKagentIndex([]KagentAgent{helper, spike, broken}, everyNamespace, nil)
	chat := session(kagentSession, "k8s-helper-aa5e9426eb2e", StateSuspended)
	got := ix.Actor(chat)
	if got == nil || got.SessionID != "01a1faa2-b3e3-7dd3-ae2d-3114122d4291" || got.Kind != KagentAgentSession ||
		got.Agent == nil || *got.Agent != helper.Ref() || got.Revision != helperRevision || !got.NewSessions || !got.Desired {
		t.Fatalf("the conversation = %+v", got)
	}
	if got := ix.Actor(session(kagentSandboxed, sandboxTemplate, StateRunning)); got == nil || got.Kind != KagentSandbox || got.Agent != nil {
		t.Errorf("the sandbox = %+v", got)
	}
	if got := ix.Actor(Actor{Atespace: "research", Name: "lit-0201", Template: Ref{"research", "lang"}}); got != nil {
		t.Errorf("a non-kagent actor = %+v", got)
	}
	if got := ix.Actor(session(kagentOther, "lang", StateRunning)); got == nil || got.Kind != KagentUnattributed || got.Reason != KagentTemplateUnknown {
		t.Errorf("a session on a template kagent didn't name = %+v", got)
	}
	elsewhere := chat
	elsewhere.Atespace = "research"
	if got := ix.Actor(elsewhere); got.Reason != KagentTemplateUnknown {
		t.Errorf("an actor outside its template's atespace = %+v", got)
	}
	if got := ix.Actor(session(kagentOther, "spike-agent-8885899a4609", StateRunning)); got.Agent == nil || *got.Agent != spike.Ref() {
		t.Errorf("spike-agent's session = %+v", got)
	}
	if got := ix.Actor(session(kagentOther, "other-agent-0123456789ab", StateRunning)); got.Kind != KagentUnattributed || got.Reason != KagentNoAgent {
		t.Errorf("a template no Agent names = %+v", got)
	}
	if tpl := ix.KagentTemplate(Ref{"kagent", "k8s-helper-aa5e9426eb2e"}); tpl == nil || tpl.Agent == nil || *tpl.Agent != helper.Ref() {
		t.Errorf("k8s-helper's template = %+v", tpl)
	}
	if tpl := ix.KagentTemplate(Ref{"research", "lang"}); tpl != nil {
		t.Errorf("an unrelated template = %+v", tpl)
	}
}

// A working revision, then an edit kagent can't prepare: desired moves on,
// latestSuccessful stays, and a session started now runs on latestSuccessful.
func TestKagentBlockedEditKeepsNewSessionsOnTheLatestSuccessful(t *testing.T) {
	edited := helper
	edited.DesiredRevision = brokenRevision
	ix := NewKagentIndex([]KagentAgent{edited}, everyNamespace, nil)
	fresh := session(kagentOther, "k8s-helper-aa5e9426eb2e", StateRunning)
	got := ix.Actor(fresh)
	if got.Kind != KagentAgentSession || !got.NewSessions || got.Desired || got.Revision != helperRevision {
		t.Fatalf("a new session during the blocked edit = %+v", got)
	}
	actors := []Actor{fresh, session(kagentSession, "k8s-helper-aa5e9426eb2e", StateCrashed)}
	byTemplate := GroupByTemplate(actors)
	sum := ix.AgentSessions(edited, func(t Ref) []Actor { return byTemplate[t] }, nil, true, now, ResumeContext{})
	if len(sum.Revisions) != 2 || sum.Revisions[0].Template.Name != "k8s-helper-aa5e9426eb2e" || !sum.Revisions[0].NewSessions || sum.Revisions[0].Desired ||
		sum.Revisions[0].Sessions != 2 || sum.Revisions[1].Template.Name != "k8s-helper-0b7f3c9e5d21" || !sum.Revisions[1].Desired || sum.Revisions[1].Sessions != 0 {
		t.Errorf("revisions = %+v", sum.Revisions)
	}
	if sum.Sessions.Total != 2 || sum.Sessions.ByState[StateCrashed] != 1 || sum.NeedAttention != 1 || len(sum.Attention) != 1 || sum.Attention[0].Name != kagentSession || !sum.Complete {
		t.Errorf("summary = %+v", sum)
	}
}

// A session on an earlier revision is named for the Agent but not counted:
// a deleted Agent of the same name reads the same way.
func TestKagentEarlierRevisionIsAHintNeverACount(t *testing.T) {
	ix := NewKagentIndex([]KagentAgent{helper}, everyNamespace, nil)
	old := session(kagentOther, "k8s-helper-0123456789ab", StateSuspended)
	got := ix.Actor(old)
	if got.Kind != KagentUnattributed || got.Reason != KagentEarlierRevision || got.Agent != nil || !slices.Equal(got.Candidates, []AgentRef{helper.Ref()}) {
		t.Fatalf("an earlier revision's session = %+v", got)
	}
	actors := []Actor{old, session(kagentSession, "k8s-helper-aa5e9426eb2e", StateSuspended)}
	byTemplate := GroupByTemplate(actors)
	prefixes := KagentTemplatesByPrefix([]Ref{old.Template, actors[1].Template})
	sum := ix.AgentSessions(helper, func(t Ref) []Actor { return byTemplate[t] }, prefixes[KagentPrefixKey("kagent", "k8s-helper")], false, now, ResumeContext{})
	if sum.Sessions.Total != 1 || sum.HintedEarlierRevisions != 1 || sum.HintShared || sum.Complete {
		t.Errorf("summary = %+v", sum)
	}
}

// kagent digests the Agent's UID into each revision, so a recreated Agent of
// the same name has new revisions and the old sessions stop matching.
func TestKagentRecreatedAgentLeavesOldSessionsUnattributed(t *testing.T) {
	recreated := helper
	recreated.UID = "9f0e1d2c-3b4a-5968-7f8e-9d0c1b2a3f4e"
	recreated.DesiredRevision, recreated.LatestSuccessfulRevision = spikeRevision, spikeRevision
	ix := NewKagentIndex([]KagentAgent{recreated}, everyNamespace, nil)
	got := ix.Actor(session(kagentSession, "k8s-helper-aa5e9426eb2e", StateSuspended))
	if got.Kind != KagentUnattributed || got.Reason != KagentEarlierRevision {
		t.Errorf("a session of the deleted Agent = %+v", got)
	}
}

// Agent names longer than 50 characters that share their first 50 share
// a template prefix: distinct revisions still attribute exactly, the same
// short revision is ambiguous, and an earlier revision hints at both.
func TestKagentLongNamesSharingAPrefix(t *testing.T) {
	base := strings.Repeat("research-assistant-", 3)[:50]
	one := KagentAgent{Namespace: "kagent", Name: base + "-one", DesiredRevision: helperRevision, LatestSuccessfulRevision: helperRevision}
	two := KagentAgent{Namespace: "kagent", Name: base + "-two", DesiredRevision: spikeRevision, LatestSuccessfulRevision: spikeRevision}
	prefix := KagentTemplatePrefix(one.Name)
	if prefix != KagentTemplatePrefix(two.Name) {
		t.Fatalf("fixture: prefixes %q and %q differ", prefix, KagentTemplatePrefix(two.Name))
	}
	ix := NewKagentIndex([]KagentAgent{one, two}, everyNamespace, nil)
	if got := ix.Actor(session(kagentSession, prefix+"-aa5e9426eb2e", StateRunning)); got.Agent == nil || *got.Agent != one.Ref() {
		t.Errorf("one's session = %+v", got)
	}
	if got := ix.Actor(session(kagentSession, prefix+"-8885899a4609", StateRunning)); got.Agent == nil || *got.Agent != two.Ref() {
		t.Errorf("two's session = %+v", got)
	}
	earlier := session(kagentOther, prefix+"-0123456789ab", StateSuspended)
	if got := ix.Actor(earlier); got.Reason != KagentEarlierRevision || len(got.Candidates) != 2 {
		t.Errorf("an earlier revision under the shared prefix = %+v", got)
	}

	same := two
	same.DesiredRevision, same.LatestSuccessfulRevision = helperRevision, helperRevision
	ix = NewKagentIndex([]KagentAgent{one, same}, everyNamespace, nil)
	clash := session(kagentSession, prefix+"-aa5e9426eb2e", StateRunning)
	got := ix.Actor(clash)
	if got.Kind != KagentUnattributed || got.Reason != KagentAmbiguous || got.Agent != nil || !slices.Equal(got.Candidates, []AgentRef{one.Ref(), same.Ref()}) {
		t.Fatalf("one template two Agents produce = %+v", got)
	}
	byTemplate := GroupByTemplate([]Actor{clash, earlier})
	prefixes := KagentTemplatesByPrefix([]Ref{clash.Template, earlier.Template})[KagentPrefixKey("kagent", prefix)]
	sum := ix.AgentSessions(one, func(t Ref) []Actor { return byTemplate[t] }, prefixes, true, now, ResumeContext{})
	if sum.Sessions.Total != 0 || len(sum.Revisions) != 1 || !sum.Revisions[0].Ambiguous || sum.Revisions[0].Sessions != 0 || sum.HintedEarlierRevisions != 1 || !sum.HintShared {
		t.Errorf("summary = %+v", sum)
	}
}

// Without the Agents of a namespace, Radar says it could not read them; a
// sandbox's template needs no Agent.
func TestKagentAgentNotReadable(t *testing.T) {
	ix := NewKagentIndex(nil, func(ns string) bool { return ns != "kagent" }, nil)
	if got := ix.Actor(session(kagentSession, "k8s-helper-aa5e9426eb2e", StateRunning)); got.Kind != KagentUnattributed || got.Reason != KagentAgentNotReadable {
		t.Errorf("a session in an unreadable namespace = %+v", got)
	}
	if got := ix.Actor(session(kagentSandboxed, sandboxTemplate, StateRunning)); got.Kind != KagentSandbox {
		t.Errorf("a sandbox = %+v", got)
	}
	if got := NewKagentIndex(nil, nil, nil).Actor(session(kagentSession, "k8s-helper-aa5e9426eb2e", StateRunning)); got.Reason != KagentAgentNotReadable {
		t.Errorf("no Agents read at all = %+v", got)
	}
}

// The steps name the Agent once the session or template is attributed, and a
// failed golden snapshot of an Agent's template is never "recreate it".
func TestKagentStepsNameTheAgent(t *testing.T) {
	ix := NewKagentIndex([]KagentAgent{helper}, everyNamespace, nil)
	k := ix.Actor(session(kagentSession, "k8s-helper-aa5e9426eb2e", StateCrashed))
	if got := KagentStepForAgent(kagentActorStep, k); !strings.HasPrefix(got, "This actor is a session of kagent Agent kagent/k8s-helper.") {
		t.Errorf("actor step = %q", got)
	}
	unattributed := ix.Actor(session(kagentSession, "lang", StateCrashed))
	if got := KagentStepForAgent(kagentActorStep, unattributed); got != kagentActorStep {
		t.Errorf("an unattributed session's step changed: %q", got)
	}

	tpl := Template{Atespace: "kagent", Name: "k8s-helper-aa5e9426eb2e", GoldenError: "GoldenActorCrashed: exit 1"}
	oom := Template{Atespace: "kagent", Name: "spike-agent-8885899a4609", GoldenError: "GoldenActorCrashed: sandbox " + memoryLimitMessage}
	other := Template{Atespace: "research", Name: "lang", GoldenError: "GoldenActorCrashed: exit 1"}
	templates := []Template{tpl, oom, other}
	actors := []Actor{kagentCrashed(kagentSession, "k8s-helper-aa5e9426eb2e", "exit 1")}
	rows := NeedsAttention(Inputs{Templates: templates, TemplatesRead: true, Actors: actors, ActorsRead: true, Now: now})
	KagentAttention(rows, templates, NewKagentIndex([]KagentAgent{helper, spike}, everyNamespace, nil))
	for _, r := range rows {
		switch {
		case r.Rule == RuleGoldenFailed && r.Subject.Name == tpl.Name:
			if strings.Contains(strings.ToLower(r.NextStep), "recreate the template") || !strings.Contains(r.NextStep, "kagent/k8s-helper") || !strings.Contains(r.NextStep, "Ready condition") ||
				!strings.Contains(r.Reading, "kagent deletes the template and creates it again") {
				t.Errorf("k8s-helper's golden row = %q / %q", r.Reading, r.NextStep)
			}
		case r.Rule == RuleGoldenFailed && r.Subject.Name == oom.Name:
			if !strings.Contains(r.NextStep, "config.runtimeDefaults.memory") || !strings.Contains(r.NextStep, "kagent/spike-agent") {
				t.Errorf("spike-agent's memory golden row = %q", r.NextStep)
			}
		case r.Rule == RuleGoldenFailed:
			if !strings.Contains(r.NextStep, "recreate the template") {
				t.Errorf("a template kagent doesn't own lost its step: %q", r.NextStep)
			}
		case r.Rule == RuleCrashedActors:
			if g := r.Groups[0]; !strings.HasPrefix(g.NextStep, "These actors are sessions of kagent Agent kagent/k8s-helper.") {
				t.Errorf("crash group = %q", g.NextStep)
			}
		}
	}
}

// The live shape after a kagent Helm change: every Agent got a new revision,
// and k8s-helper's only session still runs on the template of the revision
// it started on, created after the Agent. That is exactly k8s-helper's.
func TestKagentEarlierRevisionCreatedAfterTheAgentIsTheAgents(t *testing.T) {
	agentCreated := ago(48 * time.Hour)
	moved := helper
	moved.Created = agentCreated
	moved.DesiredRevision, moved.LatestSuccessfulRevision = spikeRevision, spikeRevision
	old := Template{Atespace: "kagent", Name: "k8s-helper-aa5e9426eb2e", CreateTime: agentCreated.Add(3 * time.Second)}
	chat := session(kagentSession, old.Name, StateSuspended)
	ix := NewKagentIndex([]KagentAgent{moved}, everyNamespace, []Template{old})
	got := ix.Actor(chat)
	if got.Kind != KagentAgentSession || got.Agent == nil || *got.Agent != moved.Ref() || !got.EarlierRevision || got.NewSessions || got.Desired || got.Revision != "aa5e9426eb2e" {
		t.Fatalf("the session on the earlier revision = %+v", got)
	}
	byTemplate := GroupByTemplate([]Actor{chat})
	sum := ix.AgentSessions(moved, func(t Ref) []Actor { return byTemplate[t] }, KagentTemplatesByPrefix([]Ref{old.Ref()})[KagentPrefixKey("kagent", "k8s-helper")], true, now, ResumeContext{})
	if sum.Sessions.Total != 1 || sum.OnEarlierRevisions != 1 || sum.HintedEarlierRevisions != 0 || len(sum.Revisions) != 1 || sum.Revisions[0].Sessions != 0 {
		t.Errorf("summary = %+v", sum)
	}
	if tpl := ix.KagentTemplate(old.Ref()); tpl == nil || !tpl.EarlierRevision || tpl.Agent == nil {
		t.Errorf("the earlier template = %+v", tpl)
	}

	// The same instant counts: a template can't be created before its Agent.
	same := old
	same.CreateTime = agentCreated
	if got := NewKagentIndex([]KagentAgent{moved}, everyNamespace, []Template{same}).Actor(chat); got.Kind != KagentAgentSession {
		t.Errorf("a template created with the Agent = %+v", got)
	}
}

// What Radar can't prove stays a hint: a template older than the Agent (a
// deleted Agent of the same name made it), a time missing on either side, a
// name kagent cut, and two Agents the template postdates.
func TestKagentEarlierRevisionUnprovenStaysAHint(t *testing.T) {
	agentCreated := ago(time.Hour)
	recreated := helper
	recreated.Created = agentCreated
	recreated.DesiredRevision, recreated.LatestSuccessfulRevision = spikeRevision, spikeRevision
	chat := session(kagentSession, "k8s-helper-aa5e9426eb2e", StateSuspended)
	hint := func(name string, agents []KagentAgent, templates []Template, a Actor) {
		t.Helper()
		got := NewKagentIndex(agents, everyNamespace, templates).Actor(a)
		if got.Kind != KagentUnattributed || got.Reason != KagentEarlierRevision || got.Agent != nil {
			t.Errorf("%s = %+v", name, got)
		}
	}
	older := Template{Atespace: "kagent", Name: chat.Template.Name, CreateTime: agentCreated.Add(-time.Second)}
	hint("a template from before the Agent was recreated", []KagentAgent{recreated}, []Template{older}, chat)
	hint("a template not read", []KagentAgent{recreated}, nil, chat)
	hint("a template with no createTime", []KagentAgent{recreated}, []Template{{Atespace: "kagent", Name: chat.Template.Name}}, chat)
	noTime := recreated
	noTime.Created = time.Time{}
	hint("an Agent with no creation time", []KagentAgent{noTime}, []Template{{Atespace: "kagent", Name: chat.Template.Name, CreateTime: agentCreated.Add(time.Minute)}}, chat)

	long := KagentAgent{Namespace: "kagent", Name: strings.Repeat("r", 50) + "-one", Created: agentCreated, DesiredRevision: spikeRevision, LatestSuccessfulRevision: spikeRevision}
	cut := session(kagentOther, KagentTemplatePrefix(long.Name)+"-aa5e9426eb2e", StateSuspended)
	hint("a cut name another Agent could have shared", []KagentAgent{long}, []Template{{Atespace: "kagent", Name: cut.Template.Name, CreateTime: agentCreated.Add(time.Minute)}}, cut)

	other := long
	other.Name = strings.Repeat("r", 50) + "-two"
	got := NewKagentIndex([]KagentAgent{long, other}, everyNamespace, []Template{{Atespace: "kagent", Name: cut.Template.Name, CreateTime: agentCreated.Add(time.Minute)}}).Actor(cut)
	if got.Kind != KagentUnattributed || got.Reason != KagentAmbiguous || len(got.Candidates) != 2 {
		t.Errorf("an earlier template two Agents predate = %+v", got)
	}
}
