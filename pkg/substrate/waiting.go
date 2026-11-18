package substrate

import (
	"fmt"
	"time"
)

// What a transition waits on, as Substrate's workflows run it. A resume is
// placed in the same write that sets RESUMING, so a resuming actor always has
// a worker; an actor no worker can take stays SUSPENDED. Nothing drives an
// unfinished resume or suspend in the background: it continues only on the
// next ResumeActor (a routed request, kubectl ate resume) or SuspendActor.
const (
	// WaitingRestore: placed on its worker, attaching volumes and restoring.
	WaitingRestore = "restore"
	// WaitingCheckpoint: checkpointing on its worker; a synchronous suspend
	// also waits for the upload.
	WaitingCheckpoint = "checkpoint"
	// WaitingRevert and WaitingDelete run in the API server, on no worker.
	WaitingRevert = "revert"
	WaitingDelete = "delete"
	// WaitingUnrecorded: a resuming or suspending actor with no worker
	// recorded, which Substrate's workflows never leave behind.
	WaitingUnrecorded = "unrecorded"
)

// Waiting is where a transitional actor waits.
type Waiting struct {
	On string `json:"on"`
	// Worker is the worker the step runs on; absent for revert and delete.
	Worker *WorkerPodRef `json:"worker,omitempty"`
	Node   string        `json:"node,omitempty"`
	// Reading says it in a sentence.
	Reading string `json:"reading"`
}

// WaitingOn reads where a resuming, suspending, reverting or deleting actor
// waits; nil in any other state.
func WaitingOn(a Actor) *Waiting {
	var w *WorkerPodRef
	node := ""
	if as := a.Assignment; as != nil {
		w = &WorkerPodRef{Worker: as.Worker, Namespace: as.Namespace, Pool: as.Pool, Pod: as.Pod}
		node = as.Node
	}
	where := ""
	if w != nil {
		where = fmt.Sprintf(" on worker %s/%s", w.Namespace, w.Pod)
	}
	switch a.State {
	case StateResuming:
		if w == nil {
			return &Waiting{On: WaitingUnrecorded, Reading: "Resuming with no worker recorded, which Substrate's resume never leaves behind; read the API server's log."}
		}
		return &Waiting{On: WaitingRestore, Worker: w, Node: node, Reading: "Placed" + where + ", restoring there. An unfinished resume continues only on the next resume call: a routed request or kubectl ate resume."}
	case StateSuspending:
		if w == nil {
			return &Waiting{On: WaitingUnrecorded, Reading: "Suspending with no worker recorded, which Substrate's suspend never leaves behind; read the API server's log."}
		}
		return &Waiting{On: WaitingCheckpoint, Worker: w, Node: node, Reading: "Checkpointing" + where + "; a synchronous suspend also waits for the upload to object storage. An unfinished suspend continues only on the next suspend call."}
	case StateReverting:
		return &Waiting{On: WaitingRevert, Reading: "Reverting in Substrate's API server, which drops the generations newer than the one it reverts to."}
	case StateDeleting:
		return &Waiting{On: WaitingDelete, Reading: "Deleting in Substrate's API server, which removes the actor's snapshots and volumes."}
	}
	return nil
}

// Worker impacts: what a worker's state means for an actor assigned to it.
const (
	// ImpactUnavailable: Substrate marks the worker unavailable, which its
	// controller does while the worker Pod is not Ready or has no IP. It
	// places nothing new there and keeps the actor assigned and running in
	// its records. If the Pod is deleted Substrate crashes the actor.
	ImpactUnavailable = "unavailable"
	// ImpactDraining: the worker Pod is terminating. When it is gone Substrate
	// crashes every actor still assigned to it.
	ImpactDraining = "draining"
	// ImpactRestarted: the worker process restarted after the actor started
	// under it, so the sandbox it ran in is gone; Substrate crashes it when it
	// reconciles the worker.
	ImpactRestarted = "restarted"
)

// What Radar's Pod cache says about an unavailable worker's Pod, the evidence
// for which side holds the worker back.
const (
	// PodNotReady and PodNoIP: Kubernetes is the cause.
	PodNotReady = "notReady"
	PodNoIP     = "noIP"
	// PodReady: Ready with an IP, so Kubernetes is not the cause: Substrate's
	// controller has not marked the worker available again.
	PodReady = "ready"
	// PodUnread: Radar's cache does not hold the worker's Pod.
	PodUnread = "unread"
)

// WorkerPod is a worker's Pod as Radar's cache holds it.
type WorkerPod struct {
	Ready bool
	HasIP bool
	// Deadline is a terminating Pod's deletionTimestamp: the deletion request
	// plus the grace period, when Kubernetes kills it. Zero when the Pod is
	// not terminating.
	Deadline time.Time
}

// WorkerPods maps "namespace/pod" to a worker's cached Pod. A missing entry
// is a Pod Radar could not read; nil when Pods were not read at all.
type WorkerPods map[string]WorkerPod

// Of returns w's Pod and whether Radar read it.
func (p WorkerPods) Of(w Worker) (WorkerPod, bool) {
	pod, ok := p[w.Namespace+"/"+w.Pod]
	return pod, ok
}

// PodStateOf names what the cache says about w's Pod.
func (p WorkerPods) PodStateOf(w Worker) string {
	pod, ok := p.Of(w)
	switch {
	case !ok:
		return PodUnread
	case !pod.Ready:
		return PodNotReady
	case !pod.HasIP:
		return PodNoIP
	}
	return PodReady
}

// unavailableCause says why Substrate marks a worker unavailable, from its
// Pod: Substrate's controller marks a worker unavailable when its Pod stops
// being Ready with an IP, and available again once it is.
func unavailableCause(podState string) string {
	switch podState {
	case PodNotReady:
		return "Substrate marks its worker unavailable because the worker Pod is not Ready."
	case PodNoIP:
		return "Substrate marks its worker unavailable because the worker Pod has no IP."
	case PodReady:
		return "Substrate marks its worker unavailable although the worker Pod is Ready, so Kubernetes is not the cause: Substrate's controller has not marked the worker available again."
	}
	return "Substrate marks its worker unavailable: either the worker Pod is not Ready, or Substrate's controller has not marked it available again. Radar could not read the Pod to tell which."
}

// WorkerImpact is what the worker's state means for one actor on it.
type WorkerImpact struct {
	Kind    string `json:"kind"`
	Reading string `json:"reading"`
	// PodState is the unavailable worker's Pod as Radar's cache holds it;
	// absent for other kinds.
	PodState string `json:"podState,omitempty"`
	// Deadline is when a draining worker's Pod is due to be gone; absent when
	// unknown.
	Deadline time.Time `json:"deadline,omitzero"`
	// NextStep is one action for this actor, and Command runs it, with the
	// actor's and the worker Pod's names filled in.
	NextStep string `json:"nextStep"`
	Command  string `json:"command"`
}

// WorkerImpactKind is WorkerImpactOf's kind alone, cheap enough to sort a
// whole actor list by; "" when the worker means nothing for a.
func WorkerImpactKind(a Actor, w Worker) string {
	if a.Assignment == nil || a.Assignment.Worker != w.Name {
		return ""
	}
	// A resuming actor is restored again by its resume when the process it
	// was placed under is gone, so only a started actor has lost its sandbox.
	if a.State != StateResuming && w.RegisteredEpoch > a.Assignment.Epoch {
		return ImpactRestarted
	}
	switch w.State {
	case WorkerDraining:
		return ImpactDraining
	case WorkerUnavailable:
		return ImpactUnavailable
	}
	return ""
}

// WorkerImpactOf reads what w's state means for actor a, assigned to it; nil
// when the worker is active and its process has not restarted under the
// actor. pods holds the worker Pods Radar's cache read.
func WorkerImpactOf(a Actor, w Worker, pods WorkerPods) *WorkerImpact {
	switch WorkerImpactKind(a, w) {
	case ImpactRestarted:
		return &WorkerImpact{Kind: ImpactRestarted,
			NextStep: "Revert it to its newest stored generation now; Substrate marks it crashed when it reconciles the worker anyway.",
			Command:  forActor(revertCommand, a),
			Reading: fmt.Sprintf(
				"Its worker process restarted (epoch %d; this actor ran under %d), so its sandbox is gone. Substrate marks it crashed when it reconciles the worker; until then it may still show %s.",
				w.RegisteredEpoch, a.Assignment.Epoch, a.State)}
	case ImpactDraining:
		pod, _ := pods.Of(w)
		return &WorkerImpact{Kind: ImpactDraining, Deadline: pod.Deadline,
			NextStep: "Suspend it before its worker Pod is gone to keep its state.", Command: forActor(suspendCommand, a),
			Reading: "Its worker Pod is terminating. When the Pod is gone, Substrate marks every actor still assigned to it crashed, this one included; suspend it before then to keep its state."}
	case ImpactUnavailable:
		state := pods.PodStateOf(w)
		step := workerUnavailableStep[state]
		return &WorkerImpact{Kind: ImpactUnavailable, PodState: state, NextStep: step.text, Command: step.command(w),
			Reading: unavailableCause(state) + " It places nothing new there and still records this actor as " + a.State + ". If the Pod is deleted, Substrate marks it crashed."}
	}
	return nil
}

// WorkerReconciling is a worker reading for an active worker whose
// registration is ahead of what Substrate has observed: its process
// restarted and Substrate has not yet reconciled it.
const WorkerReconciling = "reconciling"

// WorkerReading is what a worker's state means for the actors on it, for the
// worker's own row beside Substrate's state.
type WorkerReading struct {
	// Kind is draining, unavailable or reconciling.
	Kind string `json:"kind"`
	// Assigned counts the actors still assigned to it, and Lost those that
	// ran under an earlier process of it and so lost their sandboxes
	// (LostActors). Both are absent when actors were not read.
	Assigned *int   `json:"assigned,omitempty"`
	Lost     *int   `json:"lost,omitempty"`
	Reading  string `json:"reading"`
}

// WorkerReadingOf reads w for its row; nil for an active worker whose
// registration Substrate has observed. assigned is its actors, used only
// when actorsRead.
func WorkerReadingOf(w Worker, assigned []Actor, actorsRead bool, pods WorkerPods) *WorkerReading {
	r := &WorkerReading{}
	switch {
	case w.State == WorkerDraining:
		r.Kind, r.Reading = WorkerDraining, "Its Pod is terminating; Substrate places nothing new on it and marks any actor still assigned crashed when the Pod is gone."
	case w.State == WorkerUnavailable:
		r.Kind, r.Reading = WorkerUnavailable, workerUnavailableCause[pods.PodStateOf(w)]+" Substrate places nothing new on it."
	case w.RegisteredEpoch > w.ObservedEpoch:
		r.Kind = WorkerReconciling
		r.Reading = fmt.Sprintf("Its process restarted: registered epoch %d, which Substrate has not yet reconciled (observed %d).", w.RegisteredEpoch, w.ObservedEpoch)
	default:
		return nil
	}
	if !actorsRead {
		return r
	}
	n, lost := len(assigned), len(LostActors(w, assigned))
	r.Assigned, r.Lost = &n, &lost
	switch {
	case lost > 0:
		r.Reading = fmt.Sprintf("%s when its process restarted; Substrate marks them crashed when it reconciles the worker. ", plural(lost, "actor lost its sandbox", "actors lost their sandboxes")) + r.Reading
	case r.Kind == WorkerReconciling:
		r.Reading += " No actor still assigned ran under the earlier process."
	case n > 0:
		r.Reading = fmt.Sprintf("%s still assigned. ", plural(n, "actor is", "actors are")) + r.Reading
	}
	return r
}
