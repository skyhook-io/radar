package substrate

import (
	"maps"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
	model "github.com/skyhook-io/radar/pkg/substrate"
)

func ts(t *timestamppb.Timestamp) time.Time {
	if t == nil || !t.IsValid() {
		return time.Time{}
	}
	return t.AsTime().UTC()
}

func actorState(s ateapi.ActorState) string {
	switch s {
	case ateapi.ActorState_ACTOR_STATE_RESUMING:
		return model.StateResuming
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
		return model.StateRunning
	case ateapi.ActorState_ACTOR_STATE_SUSPENDING:
		return model.StateSuspending
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		return model.StateSuspended
	case ateapi.ActorState_ACTOR_STATE_CRASHED:
		return model.StateCrashed
	case ateapi.ActorState_ACTOR_STATE_DELETING:
		return model.StateDeleting
	case ateapi.ActorState_ACTOR_STATE_REVERTING:
		return model.StateReverting
	}
	return model.StateUnknown
}

// ParseActorState maps a query value to a state; ok is false for anything
// that is not one of model.ActorStates.
func ParseActorState(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, known := range model.ActorStates {
		if s == known {
			return s, true
		}
	}
	return "", false
}

func fidelity(f ateapi.SnapshotFidelity) string {
	switch f {
	case ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_VOLUMES:
		return model.FidelityVolumes
	case ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_ROOTFS:
		return model.FidelityRootFS
	case ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY:
		return model.FidelityMemory
	}
	return ""
}

func ref(r *ateapi.ObjectRef) model.Ref {
	return model.Ref{Atespace: r.GetAtespace(), Name: r.GetName()}
}

func snapshotOf(s *ateapi.Snapshot) model.Snapshot {
	out := model.Snapshot{
		Generation:       s.GetGeneration(),
		Fidelity:         fidelity(s.GetFidelity()),
		Locality:         s.GetLocality(),
		Durable:          model.DurableNone,
		SandboxClass:     s.GetSandboxRuntime().GetSandboxClass(),
		Runtime:          runtimeOf(s.GetSandboxRuntime().GetSandboxClass(), s.GetSandboxRuntime().GetVersion()),
		ActorTemplateUID: s.GetActorTemplateUid(),
	}
	switch s.GetOwner() {
	case ateapi.SnapshotOwner_SNAPSHOT_OWNER_ACTOR:
		out.Owner = "actor"
	case ateapi.SnapshotOwner_SNAPSHOT_OWNER_TAG:
		out.Owner = "tag"
	}
	if d := s.GetDurableSnapshot(); d != nil {
		switch d.GetState() {
		case ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING:
			out.Durable = model.DurablePending
		case ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED:
			out.Durable = model.DurableCompleted
		}
	}
	return out
}

// ActorOf converts Substrate's Actor to Radar's read model.
func ActorOf(a *ateapi.Actor) model.Actor {
	st := a.GetStatus()
	out := model.Actor{
		Atespace:       a.GetMetadata().GetAtespace(),
		Name:           a.GetMetadata().GetName(),
		UID:            a.GetMetadata().GetUid(),
		Template:       ref(a.GetActorTemplate()),
		State:          actorState(st.GetState()),
		AssignedNode:   st.GetAssignedNode(),
		WorkerSelector: maps.Clone(a.GetWorkerSelector().GetMatchLabels()),
		CreateTime:     ts(a.GetMetadata().GetCreateTime()),
		UpdateTime:     ts(a.GetMetadata().GetUpdateTime()),
	}
	if t := a.GetSourceTag(); t != nil && t.GetName() != "" {
		r := ref(t)
		out.SourceTag = &r
	}
	if wa := st.GetWorkerAssignment(); wa != nil {
		out.Assignment = &model.Assignment{
			Worker:       wa.GetWorker().GetName(),
			Namespace:    wa.GetWorkerNamespace(),
			Pool:         wa.GetWorkerPool(),
			Pod:          wa.GetWorkerPod(),
			Node:         wa.GetNodeName(),
			Epoch:        wa.GetWorkerEpoch(),
			SandboxClass: wa.GetSandboxRuntime().GetSandboxClass(),
			Runtime:      runtimeOf(wa.GetSandboxRuntime().GetSandboxClass(), wa.GetSandboxRuntime().GetVersion()),
		}
	}
	if c := st.GetCrash(); c != nil {
		out.Crash = &model.Crash{Message: c.GetMessage(), Time: ts(c.GetCrashTime())}
	}
	for _, s := range st.GetSnapshots() {
		out.Snapshots = append(out.Snapshots, snapshotOf(s))
	}
	return out
}

// TemplateOf converts Substrate's ActorTemplate to Radar's read model.
func TemplateOf(t *ateapi.ActorTemplate) model.Template {
	out := model.Template{
		Atespace:          t.GetMetadata().GetAtespace(),
		Name:              t.GetMetadata().GetName(),
		UID:               t.GetMetadata().GetUid(),
		WorkerSelector:    maps.Clone(t.GetWorkerSelector().GetMatchLabels()),
		CPUMillicores:     t.GetResources().GetLimits().GetCpuMillicores(),
		MemoryBytes:       t.GetResources().GetLimits().GetMemoryBytes(),
		PreferredFidelity: fidelity(t.GetSnapshotConfig().GetPreferredFidelity()),
		StorageLocation:   t.GetSnapshotConfig().GetStorageLocation(),
		GoldenDisabled:    t.GetSnapshotConfig().GetGoldenSnapshotConfig().GetMode() == ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED,
		CreateTime:        ts(t.GetMetadata().GetCreateTime()),
		UpdateTime:        ts(t.GetMetadata().GetUpdateTime()),
	}
	if out.PreferredFidelity == "" {
		out.PreferredFidelity = model.FidelityMemory
	}
	for _, c := range t.GetContainers() {
		out.Containers = append(out.Containers, c.GetName())
	}
	gs := t.GetStatus().GetGoldenSnapshotStatus()
	if tag := gs.GetGoldenTag(); tag != nil && tag.GetName() != "" {
		r := ref(tag)
		out.GoldenTag = &r
	}
	out.GoldenTime = ts(gs.GetSnapshotTime())
	out.GoldenError = gs.GetErrorMessage()
	return out
}

func workerState(s ateapi.WorkerState) string {
	switch s {
	case ateapi.WorkerState_WORKER_STATE_ACTIVE:
		return model.WorkerActive
	case ateapi.WorkerState_WORKER_STATE_DRAINING:
		return model.WorkerDraining
	case ateapi.WorkerState_WORKER_STATE_UNAVAILABLE:
		return model.WorkerUnavailable
	}
	return model.StateUnknown
}

func resources(r *ateapi.WorkerResources) *model.Resources {
	if r == nil {
		return nil
	}
	return &model.Resources{CPUMillicores: r.GetCpuMillicores(), MemoryBytes: r.GetMemoryBytes(), Actors: r.GetActors()}
}

func workerOf(w *ateapi.Worker) model.Worker {
	st := w.GetStatus()
	out := model.Worker{
		Name:            w.GetMetadata().GetName(),
		Namespace:       w.GetWorkerNamespace(),
		Pool:            w.GetWorkerPool(),
		Pod:             w.GetWorkerPod(),
		PodUID:          w.GetWorkerPodUid(),
		Node:            w.GetNodeName(),
		Labels:          maps.Clone(w.GetLabels()),
		State:           workerState(st.GetState()),
		Capacity:        resources(st.GetCapacity()),
		Allocated:       resources(st.GetAllocated()),
		RegisteredEpoch: st.GetRegisteredEpoch(),
		ObservedEpoch:   st.GetObservedEpoch(),
		UpdateTime:      ts(w.GetMetadata().GetUpdateTime()),
	}
	for _, c := range st.GetSandboxClasses() {
		out.SandboxClasses = append(out.SandboxClasses, c.GetSandboxClass())
		for _, rt := range append([]*ateapi.WorkerSandboxRuntime{c.GetDefaultRuntime()}, c.GetRestorableRuntimes()...) {
			if r := runtimeOf(c.GetSandboxClass(), rt.GetVersion()); r != nil {
				out.Runtimes = append(out.Runtimes, *r)
			}
		}
	}
	return out
}

// runtimeOf reads a compatibility stamp; nil when Substrate recorded none.
func runtimeOf(class string, v *ateapi.VersionedSandboxCompat) *model.Runtime {
	if v == nil {
		return nil
	}
	attrs := make(map[string]string, len(v.GetAttributes()))
	for _, a := range v.GetAttributes() {
		attrs[a.GetKey()] = a.GetValue()
	}
	return &model.Runtime{Class: class, Schema: v.GetSchemaVersion(), Attributes: attrs}
}

// EgressSummary is what an actor's egress policy allows, without its
// effects (headers, credentials).
type EgressSummary struct {
	// State is set (a policy exists), none (no policy: Substrate's egress
	// gateway denies all outbound traffic), denied or failed.
	State string `json:"state"`
	Rules int    `json:"rules"`
	// Destinations are the hostnames the rules name, deduplicated, at most 20.
	Destinations []string `json:"destinations,omitempty"`
	// More counts destinations beyond the first 20.
	More    int    `json:"more,omitempty"`
	Message string `json:"message,omitempty"`
}

// EgressOf summarizes an egress policy.
func EgressOf(p *ateapi.EgressPolicy) EgressSummary {
	out := EgressSummary{State: "set", Rules: len(p.GetRules())}
	seen := map[string]bool{}
	var all []string
	add := func(hosts []string) {
		for _, h := range hosts {
			if !seen[h] {
				seen[h] = true
				all = append(all, h)
			}
		}
	}
	for _, r := range p.GetRules() {
		add(r.GetHttp().GetHostnames())
		add(r.GetHttps().GetHostnames())
		add(r.GetTlsPassthrough().GetHostnames())
	}
	if len(all) > 20 {
		out.More = len(all) - 20
		all = all[:20]
	}
	out.Destinations = all
	return out
}
