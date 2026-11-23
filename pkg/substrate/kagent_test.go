package substrate

import (
	"strings"
	"testing"
	"time"
)

// Names as kagent 1.0 makes them: session actors session-<uuid>, an Agent's
// templates <agent>-<12 hex of its revision>, sandbox templates sandbox-<40 hex>.
const (
	kagentSession = "session-01a1faa2-b3e3-7dd3-ae2d-3114122d4291"
	kagentOther   = "session-7c9e6679-7425-40de-944b-e07fc1f90ae7"
	// sandboxTemplateName is the template kagent makes for a standalone sandbox.
	sandboxTemplateName = "sandbox-0123456789abcdef0123456789abcdef01234567"
)

func TestKagentSessionOf(t *testing.T) {
	for name, want := range map[string]string{
		kagentSession: "01a1faa2-b3e3-7dd3-ae2d-3114122d4291",
		"session-01A1FAA2-B3E3-7DD3-AE2D-3114122D4291": "",
		"session-01a1faa2":                                 "",
		"k8s-helper-aa5e9426eb2e":                          "",
		"sandbox-0123456789abcdef0123456789abcdef01234567": "",
		"my-session-01a1faa2-b3e3-7dd3-ae2d-3114122d4291":  "",
	} {
		got := KagentSessionOf(name, "k8s-helper-aa5e9426eb2e")
		if (want == "") != (got == nil) || (got != nil && (got.SessionID != want || got.Sandbox)) {
			t.Errorf("%s: %+v, want %q", name, got, want)
		}
	}
	// A standalone sandbox's actor is named like a session's; its template
	// says which it is.
	for template, sandbox := range map[string]bool{
		sandboxTemplateName: true,
		"sandbox-0123456789ABCDEF0123456789abcdef01234567": false,
		"sandbox-0123456789abcdef":                         false,
		"k8s-helper-aa5e9426eb2e":                          false,
	} {
		if got := KagentSessionOf(kagentSession, template); got == nil || got.Sandbox != sandbox {
			t.Errorf("template %s: %+v, want sandbox %v", template, got, sandbox)
		}
	}
}

// A session-<uuid> name means kagent only on a cluster that serves kagent's API.
func TestMarkKagentSessionsNeedsKagentServed(t *testing.T) {
	actors := []Actor{{Name: kagentSession}, {Name: "worker-job-1"}, {Name: kagentOther, Template: Ref{"kagent", sandboxTemplateName}}}
	MarkKagentSessions(actors, false)
	if actors[0].Kagent != nil {
		t.Fatalf("marked without kagent: %+v", actors[0])
	}
	MarkKagentSessions(actors, true)
	if actors[0].Kagent == nil || actors[0].Kagent.Sandbox || actors[1].Kagent != nil || actors[2].Kagent == nil || !actors[2].Kagent.Sandbox {
		t.Errorf("marked = %+v, %+v, %+v", actors[0].Kagent, actors[1].Kagent, actors[2].Kagent)
	}
}

func kagentCrashed(name, template, message string) Actor {
	return Actor{Atespace: "kagent", Name: name, Template: Ref{"kagent", template}, State: StateCrashed,
		Crash: &Crash{Message: message, Time: ago(time.Minute)}, Kagent: KagentSessionOf(name, template)}
}

func noKubectlAte(t *testing.T, where, step, command, then string) {
	t.Helper()
	if command != "" || then != "" || !strings.Contains(step, "RevertSession") || !strings.Contains(step, "delete the chat") {
		t.Errorf("%s: step %q command %q then %q", where, step, command, then)
	}
}

// kagent loses a session whose actor moved to another template, so neither
// the fleet row nor its template group offers the move, even for a
// memory-limit kill, where other actors get it.
func TestCrashedKagentSessionsRecoverInKagent(t *testing.T) {
	oom := "sandbox " + memoryLimitMessage
	actors := []Actor{kagentCrashed(kagentSession, "k8s-helper-aa5e9426eb2e", oom), kagentCrashed(kagentOther, "k8s-helper-aa5e9426eb2e", oom)}
	row := find(t, NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), RuleCrashedActors)
	noKubectlAte(t, "fleet row", row.NextStep, row.Command, row.ThenCommand)
	g := row.Groups[0]
	if !g.Kagent || g.Cause != CauseMemoryLimit {
		t.Errorf("group = %+v", g)
	}
	noKubectlAte(t, "group", g.NextStep, g.Command, g.ThenCommand)

	for _, a := range actors {
		r := find(t, ActorAttention(NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), a, nil, now), RuleCrashedActors)
		noKubectlAte(t, "actor page", r.NextStep, r.Command, r.ThenCommand)
		if !strings.Contains(r.NextStep, "memory") {
			t.Errorf("a memory kill's step names memory: %q", r.NextStep)
		}
	}
}

// Beside other actors, the fleet row keeps its command for them and names
// kagent for its sessions; each template group follows its own actors.
func TestCrashedKagentAmongOtherActors(t *testing.T) {
	actors := []Actor{
		kagentCrashed(kagentSession, "k8s-helper-aa5e9426eb2e", "exit 1"),
		{Atespace: "research", Name: "a1", Template: Ref{"research", "lang"}, State: StateCrashed, Crash: &Crash{Message: "exit 1"}},
	}
	row := find(t, NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), RuleCrashedActors)
	if row.Command != revertCommand || !strings.HasSuffix(row.NextStep, kagentAside) {
		t.Errorf("fleet row = %q / %q", row.NextStep, row.Command)
	}
	for _, g := range row.Groups {
		if g.Kagent != (g.Subject.Atespace == "kagent") {
			t.Errorf("group %s kagent = %v", g.Subject.Name, g.Kagent)
		}
	}
	if r := find(t, ActorAttention(NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), actors[1], nil, now), RuleCrashedActors); r.Command != "kubectl ate revert actor a1 -a research" {
		t.Errorf("the other actor keeps its revert: %+v", r)
	}
}

// A worker restart loses a kagent session's sandbox like any other; kagent,
// not a revert, recovers it.
func TestLostKagentSessionRecoversInKagent(t *testing.T) {
	on := Actor{Atespace: "kagent", Name: kagentSession, State: StateRunning, Assignment: &Assignment{Worker: "w1", Epoch: 3}, Kagent: KagentSessionOf(kagentSession, "k8s-helper-aa5e9426eb2e")}
	impact := WorkerImpactOf(on, Worker{Name: "w1", State: WorkerActive, RegisteredEpoch: 4}, nil)
	if impact == nil || impact.Kind != ImpactRestarted {
		t.Fatalf("impact = %+v", impact)
	}
	noKubectlAte(t, "worker impact", impact.NextStep, impact.Command, "")

	restarting := worker("r", "p", WorkerActive, 8, 1)
	restarting.RegisteredEpoch, restarting.ObservedEpoch = 5, 4
	on.Assignment = &Assignment{Worker: "r", Epoch: 4}
	rows := NeedsAttention(Inputs{Workers: []Worker{restarting}, WorkersRead: true, Actors: []Actor{on}, ActorsRead: true, Now: now})
	r := find(t, rows, "workerRestarting:r")
	noKubectlAte(t, "worker row", r.NextStep, r.Command, "")
}

// sandboxStep: a standalone sandbox has no chat and no RevertSession, so its
// step deletes it through kagent's SandboxService and offers no kubectl ate.
func sandboxStep(t *testing.T, where, step, command, then string) {
	t.Helper()
	if command != "" || then != "" || !strings.Contains(step, "SandboxService") || strings.Contains(step, "RevertSession") || strings.Contains(step, "chat") || strings.Contains(step, "session") {
		t.Errorf("%s: step %q command %q then %q", where, step, command, then)
	}
}

func TestKagentSandboxDeletesThroughSandboxService(t *testing.T) {
	box := kagentCrashed(kagentSession, sandboxTemplateName, "sandbox "+memoryLimitMessage)
	rows := NeedsAttention(Inputs{Actors: []Actor{box}, ActorsRead: true, Now: now})
	row := find(t, rows, RuleCrashedActors)
	sandboxStep(t, "fleet row", row.NextStep, row.Command, row.ThenCommand)
	if g := row.Groups[0]; !g.Kagent {
		t.Errorf("group = %+v", g)
	} else {
		sandboxStep(t, "group", g.NextStep, g.Command, g.ThenCommand)
	}
	r := find(t, ActorAttention(rows, box, nil, now), RuleCrashedActors)
	sandboxStep(t, "actor page", r.NextStep, r.Command, r.ThenCommand)
	sandboxStep(t, "memory kill", KagentActorStep(box, true), "", "")

	box.State, box.Crash, box.Assignment = StateRunning, nil, &Assignment{Worker: "w1", Epoch: 3}
	impact := WorkerImpactOf(box, Worker{Name: "w1", State: WorkerActive, RegisteredEpoch: 4}, nil)
	if impact == nil {
		t.Fatal("no impact")
	}
	sandboxStep(t, "worker impact", impact.NextStep, impact.Command, "")

	// Sessions and sandboxes together: the fleet step names both.
	both := find(t, NeedsAttention(Inputs{Actors: []Actor{kagentCrashed(kagentSession, sandboxTemplateName, "exit 1"), kagentCrashed(kagentOther, "k8s-helper-aa5e9426eb2e", "exit 1")}, ActorsRead: true, Now: now}), RuleCrashedActors)
	if both.Command != "" || !strings.Contains(both.NextStep, "SandboxService") || !strings.Contains(both.NextStep, "RevertSession") {
		t.Errorf("sessions and sandboxes = %q / %q", both.NextStep, both.Command)
	}
}

// A template group's command runs for each of its actors, so a kagent
// session sharing a template with another actor takes the group's commands
// away; the other actor keeps its own on its page.
func TestCrashedKagentSharingATemplate(t *testing.T) {
	oom := "sandbox " + memoryLimitMessage
	other := Actor{Atespace: "kagent", Name: "plain", Template: Ref{"kagent", "k8s-helper-aa5e9426eb2e"}, State: StateCrashed, Crash: &Crash{Message: oom, Time: ago(time.Minute)}}
	actors := []Actor{kagentCrashed(kagentSession, "k8s-helper-aa5e9426eb2e", oom), other}
	rows := NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now})
	row := find(t, rows, RuleCrashedActors)
	g := row.Groups[0]
	if len(row.Groups) != 1 || !g.Kagent || g.Command != "" || g.ThenCommand != "" || g.NextStep != CrashedMemoryNextStep+kagentAside {
		t.Errorf("group = %+v", g)
	}
	if row.Command != "" && row.Command == retemplateCommand {
		t.Errorf("fleet row offers the move: %+v", row)
	}
	r := find(t, ActorAttention(rows, other, nil, now), RuleCrashedActors)
	if r.Command != "kubectl ate update actor plain -a kagent --template <new-template>" {
		t.Errorf("the other actor keeps its own move: %+v", r)
	}

	plainGroup := find(t, NeedsAttention(Inputs{Actors: []Actor{kagentCrashed(kagentSession, "k8s-helper-aa5e9426eb2e", "exit 1"), {Atespace: "kagent", Name: "plain", Template: Ref{"kagent", "k8s-helper-aa5e9426eb2e"}, State: StateCrashed, Crash: &Crash{Message: "exit 1"}}}, ActorsRead: true, Now: now}), RuleCrashedActors).Groups[0]
	if !plainGroup.Kagent || plainGroup.Command != "" || plainGroup.NextStep != crashedNextStep+kagentAside {
		t.Errorf("non-memory group = %+v", plainGroup)
	}
}
