package substrate

import (
	"strings"
	"testing"
	"time"
)

func TestWaitingOn(t *testing.T) {
	placed := &Assignment{Worker: "w1", Namespace: "agents", Pool: "p", Pod: "p-worker-1", Node: "node-a"}
	tests := []struct {
		name string
		a    Actor
		on   string
		says string
	}{
		{"resuming is placed and restoring on its worker", Actor{State: StateResuming, Assignment: placed}, WaitingRestore, "agents/p-worker-1"},
		{"resuming continues only on the next resume call", Actor{State: StateResuming, Assignment: placed}, WaitingRestore, "next resume call"},
		{"suspending checkpoints on its worker", Actor{State: StateSuspending, Assignment: placed}, WaitingCheckpoint, "agents/p-worker-1"},
		{"a resume with no worker is never left by Substrate", Actor{State: StateResuming}, WaitingUnrecorded, "never leaves"},
		{"revert runs in the API server", Actor{State: StateReverting}, WaitingRevert, "API server"},
		{"delete runs in the API server", Actor{State: StateDeleting}, WaitingDelete, "API server"},
	}
	for _, tc := range tests {
		w := WaitingOn(tc.a)
		if w == nil || w.On != tc.on || !strings.Contains(w.Reading, tc.says) {
			t.Errorf("%s: %+v", tc.name, w)
		}
	}
	for _, state := range []string{StateRunning, StateSuspended, StateCrashed} {
		if w := WaitingOn(Actor{State: state, Assignment: placed}); w != nil {
			t.Errorf("%s waits on nothing: %+v", state, w)
		}
	}
	if row := RowOf(Actor{State: StateResuming, Assignment: placed}, now, ResumeContext{}); row.WaitingOn == nil || row.WaitingOn.Node != "node-a" {
		t.Errorf("row = %+v", row.WaitingOn)
	}
}

func TestWorkerImpactOf(t *testing.T) {
	on := func(state string, epoch int64) Actor {
		return Actor{State: state, Assignment: &Assignment{Worker: "w1", Epoch: epoch}}
	}
	worker := func(state string, registered int64) Worker {
		return Worker{Name: "w1", State: state, RegisteredEpoch: registered}
	}
	deadline := now.Add(20 * time.Minute)
	terminating := WorkerPods{"/": {Ready: true, HasIP: true, Deadline: deadline}}
	tests := []struct {
		name string
		a    Actor
		w    Worker
		pods WorkerPods
		kind string
		says string
	}{
		{"active worker means nothing", on(StateRunning, 3), worker(WorkerActive, 3), nil, "", ""},
		{"unavailable: still recorded running", on(StateRunning, 3), worker(WorkerUnavailable, 3), nil, ImpactUnavailable, "still records this actor as running."},
		{"draining: crashed when the Pod goes", on(StateRunning, 3), worker(WorkerDraining, 3), nil, ImpactDraining, "marks every actor still assigned to it crashed"},
		{"restarted: the sandbox is gone", on(StateRunning, 3), worker(WorkerActive, 4), nil, ImpactRestarted, "its sandbox is gone"},
		{"a resuming actor is restored again, not crashed", on(StateResuming, 0), worker(WorkerActive, 4), nil, "", ""},
		{"restart wins over unavailable", on(StateRunning, 3), worker(WorkerUnavailable, 4), nil, ImpactRestarted, ""},
		{"another worker's actor", Actor{State: StateRunning, Assignment: &Assignment{Worker: "w2"}}, worker(WorkerDraining, 3), nil, "", ""},
	}
	for _, tc := range tests {
		got := WorkerImpactOf(tc.a, tc.w, tc.pods)
		kind := ""
		if got != nil {
			kind = got.Kind
		}
		if kind != tc.kind || (got != nil && !strings.Contains(got.Reading, tc.says)) || kind != WorkerImpactKind(tc.a, tc.w) {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	named := Actor{Atespace: "a", Name: "x", State: StateRunning, Assignment: &Assignment{Worker: "w1", Epoch: 3}}
	if got := WorkerImpactOf(named, worker(WorkerDraining, 3), nil); got.Command != "kubectl ate suspend actor x -a a" || got.NextStep == "" {
		t.Errorf("draining step = %+v", got)
	}
	if got := WorkerImpactOf(named, worker(WorkerActive, 4), nil); got.Command != "kubectl ate revert actor x -a a" || got.NextStep == "" {
		t.Errorf("restarted step = %+v", got)
	}
	// The deadline is a field, never a time written into the sentence.
	got := WorkerImpactOf(on(StateRunning, 3), worker(WorkerDraining, 3), terminating)
	if !got.Deadline.Equal(deadline) || strings.Contains(got.Reading, "2026") {
		t.Errorf("draining with its Pod's deadline: %+v", got)
	}
}

// Substrate's controller marks a worker unavailable when its Pod stops being
// Ready with an IP, so the Pod says which side holds the worker back, and
// the actor page and the worker row must say the same.
func TestUnavailableWorkerReadsItsPod(t *testing.T) {
	w := Worker{Name: "w1", Namespace: "agents", Pod: "w1-pod", State: WorkerUnavailable, UpdateTime: ago(10 * time.Minute)}
	a := Actor{Atespace: "a", Name: "x", State: StateRunning, Assignment: &Assignment{Worker: "w1"}}
	tests := []struct {
		pods            WorkerPods
		state, says     string
		denies, command string
	}{
		{WorkerPods{"agents/w1-pod": {Ready: false, HasIP: true}}, PodNotReady, "Pod is not Ready", "Kubernetes is not the cause", "kubectl describe pod w1-pod -n agents"},
		{WorkerPods{"agents/w1-pod": {Ready: true}}, PodNoIP, "no IP", "Kubernetes is not the cause", "kubectl describe pod w1-pod -n agents"},
		{WorkerPods{"agents/w1-pod": {Ready: true, HasIP: true}}, PodReady, "Kubernetes is not the cause", "is not Ready.", "kubectl logs -n ate-system deploy/ate-controller"},
		{WorkerPods{}, PodUnread, "could not read", "Kubernetes is the cause", "kubectl get pod w1-pod -n agents"},
	}
	for _, tc := range tests {
		got := WorkerImpactOf(a, w, tc.pods)
		if got.PodState != tc.state || !strings.Contains(got.Reading, tc.says) || strings.Contains(got.Reading, tc.denies) {
			t.Errorf("%s: actor impact %+v", tc.state, got)
		}
		if got.Command != tc.command || got.NextStep == "" {
			t.Errorf("%s: the actor's impact carries the worker row's step: %+v", tc.state, got)
		}
		row := find(t, NeedsAttention(Inputs{Workers: []Worker{w}, WorkersRead: true, WorkerPods: tc.pods, Now: now}), "workerUnavailable:w1")
		if !strings.Contains(row.Reading, tc.says) || strings.Contains(row.Reading, tc.denies) || row.Command != tc.command {
			t.Errorf("%s: worker row %+v", tc.state, row)
		}
	}
}

func TestStuckRowsSayWhereTheyWait(t *testing.T) {
	at := func(name, pod string) Actor {
		return Actor{Atespace: "a", Name: name, State: StateResuming, UpdateTime: ago(20 * time.Minute),
			Assignment: &Assignment{Worker: pod, Namespace: "agents", Pod: pod}}
	}
	actors := []Actor{at("r1", "worker-1"), at("r2", "worker-1"), at("r3", "worker-3")}
	r := find(t, NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), "stuckTransition:resuming")
	if !strings.HasPrefix(r.Reading, "2 placed and restoring on worker-1; 1 on worker-3 (namespace agents). ") {
		t.Errorf("reading = %q", r.Reading)
	}
	if len(r.Groups) != 2 || r.Groups[0].Count != 2 || r.Groups[0].Subject.Kind != "Worker" {
		t.Errorf("groups = %+v", r.Groups)
	}
}

// A worker's own row says what its state means for its actors: an active
// worker whose process restarted under them is not healthy.
func TestWorkerReadingOf(t *testing.T) {
	w := Worker{Name: "w2", Namespace: "agents", Pod: "w2-pod", State: WorkerActive, RegisteredEpoch: 4, ObservedEpoch: 3}
	on := func(epoch int64, state string) Actor {
		return Actor{State: state, Assignment: &Assignment{Worker: "w2", Epoch: epoch}}
	}
	assigned := []Actor{on(3, StateRunning), on(3, StateRunning), on(3, StateRunning), on(0, StateResuming), on(4, StateRunning)}
	r := WorkerReadingOf(w, assigned, true, nil)
	if r == nil || r.Kind != WorkerReconciling || *r.Lost != 3 || *r.Assigned != 5 || !strings.HasPrefix(r.Reading, "3 actors lost their sandboxes when its process restarted") {
		t.Errorf("reconciling with lost actors = %+v", r)
	}
	if r := WorkerReadingOf(w, assigned[4:], true, nil); r.Lost == nil || *r.Lost != 0 || !strings.Contains(r.Reading, "No actor still assigned ran under the earlier process") {
		t.Errorf("a registration lag = %+v", r)
	}
	if r := WorkerReadingOf(w, nil, false, nil); r.Lost != nil || r.Assigned != nil {
		t.Errorf("actors not read: counts unknown, not 0: %+v", r)
	}
	w.ObservedEpoch = 4
	if r := WorkerReadingOf(w, assigned[4:], true, nil); r != nil {
		t.Errorf("an active, reconciled worker has no reading: %+v", r)
	}
	w.State = WorkerUnavailable
	r = WorkerReadingOf(w, assigned[4:], true, WorkerPods{"agents/w2-pod": {Ready: false}})
	if r.Kind != WorkerUnavailable || !strings.HasPrefix(r.Reading, "1 actor is still assigned. Its Pod is not Ready") {
		t.Errorf("unavailable = %+v", r)
	}
}
