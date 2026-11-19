package substrate

import (
	"cmp"
	"math/big"
	"slices"
	"strings"
)

// Resume kinds: what a resume of the actor starts from.
const (
	ResumeMemory     = "memory"
	ResumeFilesystem = "filesystem"
	ResumeVolumes    = "volumes"
	// ResumeCold: no snapshot, so the actor boots from its image.
	ResumeCold = "cold"
	// ResumeUnknown: the resume source names no fidelity, or it was captured
	// under a template whose current UID Radar could not read.
	ResumeUnknown = "unknown"
)

// Resume conditions qualify a memory reading. Substrate restores memory only
// on a worker whose runtime can restore the snapshot; otherwise it relaxes
// the runtime, places the actor on another runtime of the same class, and
// restores the filesystem (workflow_resume.go).
const (
	// ConditionCompatibleRuntime: memory if the worker the actor lands on
	// runs a compatible runtime, else the filesystem.
	ConditionCompatibleRuntime = "compatibleRuntime"
	// ConditionRuntimeMismatch: a known downgrade. The worker the actor is
	// resuming on, or every active worker of its class, runs a runtime that
	// cannot restore the snapshot's memory.
	ConditionRuntimeMismatch = "runtimeMismatch"
)

// NewestSnapshot returns the snapshot with the highest generation, or nil.
func NewestSnapshot(a Actor) *Snapshot {
	var newest *Snapshot
	for i := range a.Snapshots {
		if newest == nil || a.Snapshots[i].Generation > newest.Generation {
			newest = &a.Snapshots[i]
		}
	}
	return newest
}

// ResumeSource is the snapshot a resume restores from, as Substrate picks it:
// the newest generation that has local files or a completed durable copy. A
// generation still being written is neither, so it is not yet the source.
// Nil means the actor boots cold.
func ResumeSource(a Actor) *Snapshot {
	var src *Snapshot
	for i := range a.Snapshots {
		s := &a.Snapshots[i]
		if s.Locality == "" && s.Durable != DurableCompleted {
			continue
		}
		if src == nil || s.Generation >= src.Generation {
			src = s
		}
	}
	return src
}

// RecoverySource is the snapshot the actor holds after a revert: the newest
// generation with a completed durable copy. A revert deletes pending uploads
// and every local copy, then keeps only that generation (pruneSnapshots with
// no local snapshot). Nil means the actor boots cold after a revert.
func RecoverySource(a Actor) *Snapshot {
	var src *Snapshot
	for i := range a.Snapshots {
		s := &a.Snapshots[i]
		if s.Durable == DurableCompleted && (src == nil || s.Generation > src.Generation) {
			src = s
		}
	}
	return src
}

// TemplateUIDs maps each template to its current UID. A nil map, or a
// missing entry, means the template was not read.
type TemplateUIDs map[Ref]string

// ResumeContext is what a resume reading compares a snapshot with: the
// templates' current UIDs and, when workers were read, the runtimes the
// active workers of each class can restore under.
type ResumeContext struct {
	UIDs TemplateUIDs
	// runtimes is nil when workers were not read.
	runtimes map[string][]Runtime
}

// NewResumeContext indexes the active workers' runtimes by class.
// workersComplete false means Radar does not know every runtime the fleet
// runs, so it cannot say none is compatible.
func NewResumeContext(uids TemplateUIDs, workers []Worker, workersComplete bool) ResumeContext {
	rc := ResumeContext{UIDs: uids}
	if !workersComplete {
		return rc
	}
	rc.runtimes = map[string][]Runtime{}
	for _, w := range workers {
		if w.State != WorkerActive {
			continue
		}
		for _, rt := range w.Runtimes {
			rc.runtimes[rt.Class] = append(rc.runtimes[rt.Class], rt)
		}
	}
	return rc
}

// Resume is what a resume restores, and on what condition.
type Resume struct {
	With string `json:"resumesWith"`
	// Condition is empty when With holds unconditionally.
	Condition string `json:"resumeCondition,omitempty"`
}

// resumeOf reads what restoring from s gives actor a. A snapshot captured
// under a template the actor no longer runs restores volumes only, whatever
// its fidelity; when that cannot be checked the answer is unknown.
func resumeOf(a Actor, s *Snapshot, rc ResumeContext, assignment *Assignment) Resume {
	if s == nil {
		return Resume{With: ResumeCold}
	}
	if s.ActorTemplateUID != "" {
		uid, ok := rc.UIDs[a.Template]
		if !ok {
			return Resume{With: ResumeUnknown}
		}
		if uid != s.ActorTemplateUID {
			return Resume{With: ResumeVolumes}
		}
	}
	switch s.Fidelity {
	case FidelityRootFS:
		return Resume{With: ResumeFilesystem}
	case FidelityVolumes:
		return Resume{With: ResumeVolumes}
	case FidelityMemory:
	default:
		return Resume{With: ResumeUnknown}
	}
	if s.Runtime == nil {
		return Resume{With: ResumeMemory, Condition: ConditionCompatibleRuntime}
	}
	// The actor is being placed: its assignment records the runtime it
	// restores under.
	if assignment != nil && assignment.Runtime != nil {
		if RuntimeCompatible(*s.Runtime, *assignment.Runtime) {
			return Resume{With: ResumeMemory}
		}
		return Resume{With: ResumeFilesystem, Condition: ConditionRuntimeMismatch}
	}
	if rc.runtimes != nil {
		class := rc.runtimes[s.Runtime.Class]
		for _, rt := range class {
			if RuntimeCompatible(*s.Runtime, rt) {
				return Resume{With: ResumeMemory, Condition: ConditionCompatibleRuntime}
			}
		}
		if len(class) > 0 {
			return Resume{With: ResumeFilesystem, Condition: ConditionRuntimeMismatch}
		}
	}
	return Resume{With: ResumeMemory, Condition: ConditionCompatibleRuntime}
}

// ResumeOf reads what the actor's next resume restores. For a resuming actor
// that is decided by the worker it was placed on.
func ResumeOf(a Actor, rc ResumeContext) Resume {
	var placed *Assignment
	if a.State == StateResuming {
		placed = a.Assignment
	}
	return resumeOf(a, ResumeSource(a), rc, placed)
}

// RuntimeCompatible is Substrate's matchesCompat: the worker runtime can
// restore a snapshot stamped with snap when both are the same class, share a
// non-empty schema version, and every snapshot attribute is on the worker
// with an equal value, except cpu_features, where the snapshot's bitmask must
// be a subset of the worker's. Keys only the worker has are ignored.
func RuntimeCompatible(snap, worker Runtime) bool {
	if snap.Class != worker.Class || worker.Schema == "" || worker.Schema != snap.Schema {
		return false
	}
	for k, sv := range snap.Attributes {
		wv, ok := worker.Attributes[k]
		if !ok {
			return false
		}
		if k == "cpu_features" {
			if !featureSubset(sv, wv) {
				return false
			}
		} else if wv != sv {
			return false
		}
	}
	return true
}

// featureSubset compares cpu_features masks, lowercase hex with no prefix or
// leading zeros; a value that does not parse matches nothing.
func featureSubset(snap, worker string) bool {
	s, ok1 := parseMask(snap)
	w, ok2 := parseMask(worker)
	if !ok1 || !ok2 {
		return false
	}
	return new(big.Int).AndNot(s, w).Sign() == 0
}

func parseMask(v string) (*big.Int, bool) {
	if v == "" || (len(v) > 1 && v[0] == '0') || strings.ToLower(v) != v {
		return nil, false
	}
	return new(big.Int).SetString(v, 16)
}

// Why a revert discards a generation.
const (
	// DiscardLocalOnly: only on its node; a revert deletes local copies.
	DiscardLocalOnly = "localOnly"
	// DiscardUploadPending: a finalized suspend whose upload may still
	// finish. A revert deletes pending uploads; once the upload is stored the
	// generation becomes the recovery source instead.
	DiscardUploadPending = "uploadPending"
	// DiscardUploadAbandoned: a crashed suspend's generation with no local
	// copy. Substrate never records that upload (snapshot_report.go), so
	// waiting does not make it the recovery source.
	DiscardUploadAbandoned = "uploadAbandoned"
	// DiscardIncomplete: recorded but neither stored nor on a node, as a
	// crash leaves an in-progress suspend so a revert can clean it up.
	DiscardIncomplete = "incomplete"
)

// Discarded is one generation newer than the recovery source.
type Discarded struct {
	Generation int32  `json:"generation"`
	Fidelity   string `json:"fidelity,omitempty"`
	Why        string `json:"why"`
}

// Recovery is what a revert leaves the actor with. Substrate reverts a
// crashed or running actor, or a suspended one whose newest generation is on
// its node.
type Recovery struct {
	// Generation is the recovery source; absent when nothing is stored, so
	// the actor boots cold after a revert.
	Generation int32  `json:"generation,omitempty"`
	Fidelity   string `json:"fidelity,omitempty"`
	Resume
	// Discarded are the newer generations a revert drops, newest first.
	Discarded []Discarded `json:"discarded"`
}

// Revertable is Substrate's isRevertable.
func Revertable(a Actor) bool {
	switch a.State {
	case StateCrashed, StateRunning:
		return true
	case StateSuspended:
		n := NewestSnapshot(a)
		return n != nil && n.Locality != ""
	}
	return false
}

// RecoveryOf reads what a revert would leave; nil when the actor cannot be
// reverted.
func RecoveryOf(a Actor, rc ResumeContext) *Recovery {
	if !Revertable(a) {
		return nil
	}
	src := RecoverySource(a)
	r := &Recovery{Resume: resumeOf(a, src, rc, nil), Discarded: []Discarded{}}
	var floor int32
	if src != nil {
		r.Generation, r.Fidelity, floor = src.Generation, src.Fidelity, src.Generation
	}
	for _, s := range a.Snapshots {
		if s.Generation <= floor {
			continue
		}
		d := Discarded{Generation: s.Generation, Fidelity: s.Fidelity}
		switch {
		case s.Durable == DurablePending && s.Locality == "" && a.State == StateCrashed:
			d.Why = DiscardUploadAbandoned
		case s.Durable == DurablePending:
			d.Why = DiscardUploadPending
		case s.Locality != "":
			d.Why = DiscardLocalOnly
		default:
			d.Why = DiscardIncomplete
		}
		r.Discarded = append(r.Discarded, d)
	}
	slices.SortFunc(r.Discarded, func(x, y Discarded) int { return cmp.Compare(y.Generation, x.Generation) })
	return r
}
