// Package substrate is Radar's read model of Agent Substrate's API (actors,
// actor templates, workers, atespaces) and the readings Radar derives from it.
// It is pure: the gRPC client and its conversion live in Radar's
// internal/substrate, so Radar Hub can reuse the derivations without them.
//
// The types here are also the wire shapes of /api/substrate/*. What an
// omitted field means depends on its kind, and each field says which:
//   - a pointer (a measurement, a reference, a nested reading): unknown, not
//     reported or not read;
//   - a list or map of an object Substrate returned: empty. Snapshots
//     omitted is no snapshot; a WorkerSelector omitted matches every pool;
//   - a number Substrate defines with a zero meaning (a template's CPU or
//     memory limit): that zero;
//   - a boolean: false;
//   - a time: not reported.
//
// Lists a response builds from reads (rows, assigned actors) follow their
// response's rule: [] when read and empty, absent when not read.
package substrate

import (
	"strings"
	"time"
)

// Ref names an atespaced Substrate resource.
type Ref struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
}

// PoolRef names a Kubernetes WorkerPool.
type PoolRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (p PoolRef) Key() string { return p.Namespace + "/" + p.Name }

// Actor states, Substrate's ActorState in lowercase.
const (
	StateResuming   = "resuming"
	StateRunning    = "running"
	StateSuspending = "suspending"
	StateSuspended  = "suspended"
	StateCrashed    = "crashed"
	StateDeleting   = "deleting"
	StateReverting  = "reverting"
	StateUnknown    = "unknown"
)

// ActorStates lists every state in display order.
var ActorStates = []string{StateRunning, StateSuspended, StateResuming, StateSuspending, StateReverting, StateCrashed, StateDeleting, StateUnknown}

// Snapshot fidelities, lowest to highest. Each includes the ones below it.
const (
	FidelityVolumes = "volumes"
	FidelityRootFS  = "rootfs"
	FidelityMemory  = "memory"
)

// Durable storage states of a snapshot.
const (
	DurablePending   = "pending"
	DurableCompleted = "completed"
	// DurableNone: Substrate recorded no durable copy; the files exist only on
	// the node named by locality, if any.
	DurableNone = "none"
)

// Snapshot is one generation of an actor's saved state.
type Snapshot struct {
	Generation int32 `json:"generation"`
	// Fidelity is memory, rootfs or volumes; absent when unspecified.
	Fidelity string `json:"fidelity,omitempty"`
	// Owner is actor (captured from this actor) or tag (a tag's copy,
	// including the golden snapshot).
	Owner string `json:"owner,omitempty"`
	// Locality is the node holding the snapshot's files; absent once they
	// are detached.
	Locality string `json:"locality,omitempty"`
	// Durable is pending, completed or none.
	Durable string `json:"durable"`
	// SandboxClass is the class the actor ran under when it was taken.
	SandboxClass string `json:"sandboxClass,omitempty"`
	// Runtime is the runtime the snapshot was taken under; a memory restore
	// needs a worker runtime compatible with it (RuntimeCompatible).
	Runtime *Runtime `json:"-"`
	// ActorTemplateUID is the template the snapshot's guest state was
	// captured under; a resume under another template restores volumes only.
	ActorTemplateUID string `json:"-"`
}

// Assignment is the worker hosting an actor. Substrate clears it whenever the
// actor has no worker (suspended, crashed).
type Assignment struct {
	// Worker is Substrate's opaque Worker name.
	Worker    string `json:"worker"`
	Namespace string `json:"namespace"`
	Pool      string `json:"pool"`
	Pod       string `json:"pod"`
	Node      string `json:"node"`
	// Epoch is the worker process the actor runs under, 0 until it becomes
	// running. Below its worker's RegisteredEpoch, the actor's sandbox went
	// with an earlier process (WorkerImpactOf).
	Epoch        int64  `json:"epoch,omitempty"`
	SandboxClass string `json:"sandboxClass,omitempty"`
	// Runtime is what the actor was started or restored under on this worker.
	Runtime *Runtime `json:"-"`
}

// Runtime is a sandbox runtime's compatibility stamp: Substrate's
// ActorSandboxRuntime, or one runtime a worker reports for its class.
type Runtime struct {
	Class      string
	Schema     string
	Attributes map[string]string
}

// Crash is why and when an actor entered crashed.
type Crash struct {
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"time,omitzero"`
}

// Actor is one Substrate actor as Radar reads it.
type Actor struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
	UID      string `json:"uid,omitempty"`
	Template Ref    `json:"template"`
	// SourceTag is the tag the actor was seeded from; absent when it started
	// from its template.
	SourceTag    *Ref        `json:"sourceTag,omitempty"`
	State        string      `json:"state"`
	Assignment   *Assignment `json:"assignment,omitempty"`
	AssignedNode string      `json:"assignedNode,omitempty"`
	Crash        *Crash      `json:"crash,omitempty"`
	// Snapshots omitted: the actor has none.
	Snapshots []Snapshot `json:"snapshots,omitempty"`
	// WorkerSelector narrows the template's; omitted adds nothing to it.
	WorkerSelector map[string]string `json:"workerSelector,omitempty"`
	CreateTime     time.Time         `json:"createTime,omitzero"`
	// UpdateTime is Substrate's last change to the actor. The current state
	// has held at least since then.
	UpdateTime time.Time `json:"updateTime,omitzero"`
}

func (a Actor) Ref() Ref { return Ref{Atespace: a.Atespace, Name: a.Name} }

// Golden snapshot states of a template.
const (
	GoldenReady    = "ready"
	GoldenFailed   = "failed"
	GoldenBuilding = "building"
	GoldenDisabled = "disabled"
)

// Template is one ActorTemplate.
type Template struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
	UID      string `json:"uid,omitempty"`
	// WorkerSelector is the pool labels the template requires; absent matches
	// every pool. An actor's own selector applies on top of it.
	WorkerSelector map[string]string `json:"workerSelector,omitempty"`
	// CPUMillicores and MemoryBytes are the actor's limits; omitted is 0,
	// none set.
	CPUMillicores int64 `json:"cpuMillicores,omitempty"`
	MemoryBytes   int64 `json:"memoryBytes,omitempty"`
	// PreferredFidelity is the ceiling snapshots are packaged at; Substrate's
	// default is memory.
	PreferredFidelity string `json:"preferredFidelity"`
	// StorageLocation is the template's snapshot location; absent means the
	// API server's default.
	StorageLocation string   `json:"storageLocation,omitempty"`
	Containers      []string `json:"containers,omitempty"`
	// The Golden* fields are read through GoldenOf.
	GoldenDisabled bool      `json:"-"`
	GoldenTag      *Ref      `json:"-"`
	GoldenTime     time.Time `json:"-"`
	GoldenError    string    `json:"-"`
	CreateTime     time.Time `json:"createTime,omitzero"`
	UpdateTime     time.Time `json:"updateTime,omitzero"`
}

func (t Template) Ref() Ref { return Ref{Atespace: t.Atespace, Name: t.Name} }

// Golden is a template's golden snapshot reading.
type Golden struct {
	// State is ready, failed (terminal), building or disabled.
	State string `json:"state"`
	Tag   *Ref   `json:"tag,omitempty"`
	// Time is when warm-up ended (ready) or the template last changed (failed).
	Time  time.Time `json:"time,omitzero"`
	Error string    `json:"error,omitempty"`
	// Cause is CauseMemoryLimit when Error says the golden actor was killed
	// over its memory limit; absent otherwise.
	Cause string `json:"cause,omitempty"`
	// Blocked says why a building snapshot cannot progress: noMatchingPool,
	// when the complete pool list holds no pool its selector matches. Substrate
	// runs the golden actor like any actor, so with nowhere to place it the
	// build retries without end and never fails.
	Blocked string `json:"blocked,omitempty"`
}

// GoldenBlockedNoMatchingPool is Golden.Blocked for a template whose
// selector matches no pool; the same word as the noMatchingPool rule.
const GoldenBlockedNoMatchingPool = "noMatchingPool"

// GoldenOf reads a template's golden snapshot state. A nonempty error is
// terminal and wins over a tag left from before.
func GoldenOf(t Template) Golden {
	switch {
	case t.GoldenDisabled:
		return Golden{State: GoldenDisabled}
	case t.GoldenError != "":
		g := Golden{State: GoldenFailed, Error: t.GoldenError, Time: t.UpdateTime}
		if strings.Contains(t.GoldenError, memoryLimitMessage) {
			g.Cause = CauseMemoryLimit
		}
		return g
	case t.GoldenTag != nil:
		return Golden{State: GoldenReady, Tag: t.GoldenTag, Time: t.GoldenTime}
	}
	return Golden{State: GoldenBuilding}
}

// Worker states, Substrate's WorkerState in lowercase.
const (
	WorkerActive      = "active"
	WorkerDraining    = "draining"
	WorkerUnavailable = "unavailable"
)

// Resources is an amount of each dimension Substrate places by. Substrate
// treats an unreported dimension as zero: a capacity of 0 actors is 0 slots.
type Resources struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
	Actors        int32 `json:"actors"`
}

// Worker is one Substrate worker (a worker Pod).
type Worker struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Pool      string `json:"pool"`
	Pod       string `json:"pod"`
	// PodUID tells the worker's own Pod from a later one of the same name.
	PodUID string            `json:"-"`
	Node   string            `json:"node"`
	Labels map[string]string `json:"-"`
	// State is active, draining, unavailable, or unknown.
	State     string     `json:"state"`
	Capacity  *Resources `json:"capacity,omitempty"`
	Allocated *Resources `json:"allocated,omitempty"`
	// RegisteredEpoch above ObservedEpoch means Substrate has not yet
	// reconciled the latest registration: actors placed before it, if any,
	// may still read running.
	RegisteredEpoch int64    `json:"registeredEpoch"`
	ObservedEpoch   int64    `json:"observedEpoch"`
	SandboxClasses  []string `json:"sandboxClasses,omitempty"`
	// Runtimes are the runtimes the worker can restore snapshots under: each
	// class's default runtime, then its restorable ones.
	Runtimes []Runtime `json:"-"`
	// UpdateTime is Substrate's last change to the worker; the current state
	// has held at least since then.
	UpdateTime time.Time `json:"updateTime,omitzero"`
}

func (w Worker) PoolRef() PoolRef { return PoolRef{Namespace: w.Namespace, Name: w.Pool} }

// Atespace is one Substrate atespace.
type Atespace struct {
	Name       string    `json:"name"`
	CreateTime time.Time `json:"createTime,omitzero"`
}

// Pool is a Kubernetes WorkerPool as Radar's cache holds it.
type Pool struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"-"`
	// Desired is spec.replicas; absent when unset.
	Desired *int64 `json:"desired,omitempty"`
	// Ready is status.readyReplicas, the worker Pods Kubernetes calls ready.
	// It can lag the Pods; Pods is what Radar's cache holds now.
	Ready int64 `json:"ready"`
	// Pods counts the pool's worker Pods in Radar's cache; absent when the
	// caller cannot list Pods in the namespace or the cache does not list
	// them.
	Pods           *PoolPods `json:"pods,omitempty"`
	SandboxClasses []string  `json:"sandboxClasses,omitempty"`
	// Health and HealthReason are Radar's WorkerPool verdict (pkg/health).
	Health       string    `json:"health"`
	HealthReason string    `json:"healthReason"`
	CreateTime   time.Time `json:"createTime,omitzero"`
}

func (p Pool) Ref() PoolRef { return PoolRef{Namespace: p.Namespace, Name: p.Name} }

// PoolPods counts a pool's worker Pods, those labelled with its name.
type PoolPods struct {
	// Created counts the Pods not being deleted.
	Created int `json:"created"`
	// Ready counts those of them whose Ready condition holds.
	Ready int `json:"ready"`
	// Terminating counts the Pods being deleted, which a Deployment's ready
	// count leaves out even while they are Ready.
	Terminating int `json:"terminating"`
}

// ReadyWorkers is how many of the pool's worker Pods are ready: counted from
// the Pods when Radar holds them, else the WorkerPool's own status.
func (p Pool) ReadyWorkers() int64 {
	if p.Pods != nil {
		return int64(p.Pods.Ready)
	}
	return p.Ready
}
