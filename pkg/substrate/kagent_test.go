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
		got := KagentSessionOf(name)
		if (want == "") != (got == nil) || (got != nil && got.SessionID != want) {
			t.Errorf("%s: %+v, want %q", name, got, want)
		}
	}
}

// A session-<uuid> name means kagent only on a cluster that serves kagent's API.
func TestMarkKagentSessionsNeedsKagentServed(t *testing.T) {
	actors := []Actor{{Name: kagentSession}, {Name: "worker-job-1"}}
	MarkKagentSessions(actors, false)
	if actors[0].Kagent != nil {
		t.Fatalf("marked without kagent: %+v", actors[0])
	}
	MarkKagentSessions(actors, true)
	if actors[0].Kagent == nil || actors[1].Kagent != nil {
		t.Errorf("marked = %+v, %+v", actors[0].Kagent, actors[1].Kagent)
	}
}

func kagentCrashed(name, template, message string) Actor {
	return Actor{Atespace: "kagent", Name: name, Template: Ref{"kagent", template}, State: StateCrashed,
		Crash: &Crash{Message: message, Time: ago(time.Minute)}, Kagent: KagentSessionOf(name)}
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
	on := Actor{Atespace: "kagent", Name: kagentSession, State: StateRunning, Assignment: &Assignment{Worker: "w1", Epoch: 3}, Kagent: KagentSessionOf(kagentSession)}
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
