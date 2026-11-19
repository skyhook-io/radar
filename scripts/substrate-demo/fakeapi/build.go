package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

// Crashed actors whose newest generation a revert discards.
const (
	uploadStillPending = "research/lit-review-0175"
	uploadAbandoned    = "research/lit-review-0188"
)

func workerSPIFFEID() string { return "spiffe://cluster.local/ns/" + fixtureNamespace + "/sa/default" }

func sortActors(as []*ateapi.Actor) {
	slices.SortFunc(as, func(a, b *ateapi.Actor) int {
		if c := strings.Compare(a.Metadata.Atespace, b.Metadata.Atespace); c != 0 {
			return c
		}
		return strings.Compare(a.Metadata.Name, b.Metadata.Name)
	})
}

var maxGen = map[tmplKey]int{missingTemplate: 8, tADK: 40, tClaude: 18, tLang: 30, tCodex: 12, tBrowser: 4, tNotebook: 25, tGPU: 1}

func (g *gen) snapshot(r *actorRec, generation int32, fid ateapi.SnapshotFidelity, locality string, durable ateapi.SnapshotStorageState, runtimeKey string) *ateapi.Snapshot {
	a := r.actor
	id := hashUUID("snapshot", a.Metadata.Uid, fmt.Sprint(generation))
	s := &ateapi.Snapshot{
		Uuid:             id,
		Generation:       generation,
		Owner:            ateapi.SnapshotOwner_SNAPSHOT_OWNER_ACTOR,
		Fidelity:         fid,
		ActorTemplateUid: r.tmplUID,
		Locality:         locality,
		SandboxRuntime:   runtimeStamp(r.tmpl.class, runtimeKey),
	}
	if durable != ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_UNSPECIFIED {
		s.DurableSnapshot = &ateapi.SnapshotStorage{
			State:  durable,
			Object: &ateapi.ObjectSnapshot{SnapshotUri: fmt.Sprintf("%s/atespaces/%s/actors/%s/%s", snapshotBucket, a.Metadata.Atespace, a.Metadata.Uid, id)},
		}
	}
	return s
}

func (g *gen) assignment(r *actorRec, epoch int64) *ateapi.WorkerAssignment {
	p := r.plan
	var ips []string
	if p.podInfo.IP != "" {
		ips = []string{p.podInfo.IP}
	}
	return &ateapi.WorkerAssignment{
		Worker:          &ateapi.ObjectRef{Name: p.name},
		WorkerNamespace: fixtureNamespace,
		WorkerPool:      p.pool,
		WorkerPod:       p.pod,
		WorkerPodUid:    p.podInfo.UID,
		WorkerPodIps:    ips,
		NodeName:        p.podInfo.Node,
		WorkerEpoch:     epoch,
		SandboxRuntime:  runtimeStamp(r.tmpl.class, "current"),
		WorkerSpiffeId:  workerSPIFFEID(),
	}
}

const (
	completed = ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED
	pending   = ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING
	noDurable = ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_UNSPECIFIED
)

func (g *gen) fillStatus(r *actorRec) {
	a, st, t := r.actor, r.actor.Status, r.tmpl
	state := ateapi.ActorState_ACTOR_STATE_SUSPENDED
	switch {
	case r.sp != nil:
		state = r.sp.state
	case r.plan != nil:
		state = ateapi.ActorState_ACTOR_STATE_RUNNING
	}
	st.State = state

	var generation int32
	if !r.golden && t.key != tGPU {
		generation = int32(1 + g.rng.IntN(maxGen[t.key]))
		if r.sp == nil && r.plan == nil && g.rng.IntN(50) == 0 {
			generation = 0 // created, never resumed
		}
	}
	fid := t.fidelity
	if fid == ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY && g.rng.IntN(12) == 0 {
		// best-effort ceiling: memory was skipped, e.g. under host pressure
		fid = ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_ROOTFS
	}
	st.LastAssignedGeneration = generation

	var age time.Duration
	if r.sp != nil {
		age = r.sp.age
	}
	node := ""
	if r.plan != nil {
		node = r.plan.podInfo.Node
	} else if r.lastPlan != nil {
		node = r.lastPlan.podInfo.Node
	}

	switch state {
	case ateapi.ActorState_ACTOR_STATE_RUNNING:
		if age == 0 {
			age = g.between(2*time.Minute, 6*time.Hour)
		}
		r.placedAge = age + 3*time.Second
		epoch := r.plan.registered
		if r.sp != nil && r.sp.staleEpoch {
			epoch = r.plan.registered - 1
		}
		st.WorkerAssignment = g.assignment(r, epoch)
		st.AssignedNode = node
		if generation > 0 {
			st.Snapshots = []*ateapi.Snapshot{g.snapshot(r, generation, fid, node, completed, "current")}
		}
	case ateapi.ActorState_ACTOR_STATE_RESUMING:
		r.placedAge = age
		st.WorkerAssignment = g.assignment(r, 0)
		st.AssignedNode = node
		if generation > 0 {
			st.Snapshots = []*ateapi.Snapshot{g.snapshot(r, generation, fid, "", completed, "current")}
		}
	case ateapi.ActorState_ACTOR_STATE_SUSPENDING:
		r.placedAge = age + g.between(30*time.Minute, 5*time.Hour)
		st.WorkerAssignment = g.assignment(r, r.plan.registered)
		st.AssignedNode = node
		st.Snapshots = []*ateapi.Snapshot{
			g.snapshot(r, generation, fid, node, completed, "current"),
			g.snapshot(r, generation+1, fid, node, noDurable, "current"),
		}
		st.LastAssignedGeneration = generation + 1
	case ateapi.ActorState_ACTOR_STATE_CRASHED, ateapi.ActorState_ACTOR_STATE_REVERTING:
		crashAge := g.onCrashClock(r.sp.crashAge)
		st.Crash = &ateapi.ActorCrash{Message: r.sp.crash, CrashTime: g.ts(crashAge)}
		if state == ateapi.ActorState_ACTOR_STATE_CRASHED {
			age = crashAge
		}
		if generation > 0 {
			st.Snapshots = []*ateapi.Snapshot{g.snapshot(r, generation, fid, node, completed, "current")}
			st.AssignedNode = node
			// Two crashes left a newer generation a revert would discard: a
			// finalized async suspend still uploading from its node, and a
			// synchronous suspend whose upload failed, which Substrate never
			// records.
			switch a.Metadata.Atespace + "/" + a.Metadata.Name {
			case uploadStillPending:
				st.Snapshots = append(st.Snapshots, g.snapshot(r, generation+1, fid, node, pending, "current"))
				st.LastAssignedGeneration = generation + 1
			case uploadAbandoned:
				st.Snapshots = append(st.Snapshots, g.snapshot(r, generation+1, fid, "", pending, "current"))
				st.LastAssignedGeneration = generation + 1
			}
		}
	case ateapi.ActorState_ACTOR_STATE_DELETING:
		a.Metadata.DeleteTime = g.ts(age)
		if generation > 0 {
			st.Snapshots = []*ateapi.Snapshot{g.snapshot(r, generation, fid, "", completed, "current")}
		}
	case ateapi.ActorState_ACTOR_STATE_SUSPENDED:
		switch {
		case generation == 0:
			age = g.start.Sub(a.Metadata.CreateTime.AsTime())
		case t.atespace+"/"+a.Metadata.Name == stuckUpload:
			age = stuckUploadAge
			st.Snapshots = []*ateapi.Snapshot{g.snapshot(r, generation, fid, g.nodes[0], pending, "current")}
			st.AssignedNode = g.nodes[0]
		default:
			roll := g.ages.IntN(100)
			switch {
			case roll < 2:
				age = g.between(20*time.Second, 4*time.Minute)
			case roll < 60:
				age = g.between(4*time.Minute, 6*time.Hour)
			case roll < 90:
				age = g.between(6*time.Hour, 3*24*time.Hour)
			default:
				age = g.between(3*24*time.Hour, 20*24*time.Hour)
			}
			rt := "current"
			if age > 3*24*time.Hour {
				rt = "retired"
			} else if age > 24*time.Hour {
				rt = "previous"
			}
			n := g.nodes[g.rng.IntN(len(g.nodes))]
			loc, durable := "", completed
			if age < 4*time.Minute {
				// async suspend: on the node, upload in progress
				loc, durable = n, pending
			} else if age < 2*time.Hour {
				loc = n
			}
			st.Snapshots = []*ateapi.Snapshot{g.snapshot(r, generation, fid, loc, durable, rt)}
			st.AssignedNode = loc
		}
	}
	r.stateAge = age
	a.Metadata.UpdateTime = g.ts(age)
	if created := g.start.Sub(a.Metadata.CreateTime.AsTime()); created < age+time.Minute {
		a.Metadata.CreateTime = g.ts(age + g.between(time.Hour, 3*24*time.Hour))
	}

	name := a.Metadata.Name
	var n int
	if _, err := fmt.Sscanf(name, "pr-%d-reviewer", &n); err == nil && n >= 8401 && n <= 8406 {
		a.SourceTag = &ateapi.ObjectRef{Atespace: "code-review", Name: "repo-warm-baseline"}
	}
	if _, err := fmt.Sscanf(name, "ticket-%d", &n); err == nil && n >= 41300 && n <= 41305 {
		a.SourceTag = &ateapi.ObjectRef{Atespace: "support-bots", Name: "kb-loaded-2026-11"}
	}
	if _, err := fmt.Sscanf(name, "pr-%d-reviewer", &n); err == nil && n >= 8410 && n <= 8412 && (r.plan == nil || r.plan.pool == "batch-pool") {
		a.WorkerSelector = &ateapi.Selector{MatchLabels: map[string]string{"tier": "batch"}}
	}
}

func (g *gen) buildWorker(p *workerPlan) *ateapi.Worker {
	pool := poolByName(p.pool)
	// A Worker is named after its Pod's UID, so it cannot predate it.
	createdAge := min(p.createdAge, p.podAge(g.start))
	stateAge := p.stateAge
	if p.incident {
		stateAge = g.onCrashClock(stateAge)
	}
	if drain := p.drainAge(g.start); drain > 0 {
		stateAge = drain
	}
	stateAge = min(stateAge, createdAge)
	var labels map[string]string
	if len(pool.labels) > 0 {
		labels = maps.Clone(pool.labels)
	}
	var ips []string
	if p.podInfo.IP != "" {
		ips = []string{p.podInfo.IP}
	}
	w := &ateapi.Worker{
		Metadata: &ateapi.ResourceMetadata{
			Name: p.name, Uid: hashUUID("worker", p.pod), Version: 6 + p.registered*3,
			CreateTime: g.ts(createdAge), UpdateTime: g.ts(stateAge),
		},
		WorkerNamespace: fixtureNamespace,
		WorkerPool:      p.pool,
		WorkerPod:       p.pod,
		WorkerPodUid:    p.podInfo.UID,
		NodeName:        p.podInfo.Node,
		WorkerSpiffeId:  workerSPIFFEID(),
		Labels:          labels,
		Status: &ateapi.WorkerStatus{
			State:         p.state,
			Capacity:      &ateapi.WorkerResources{CpuMillicores: p.cpu, MemoryBytes: p.mem, Actors: p.slots},
			ObservedEpoch: p.observed,
			SandboxClasses: []*ateapi.WorkerSandboxClass{{
				SandboxClass:       pool.class,
				DefaultRuntime:     &ateapi.WorkerSandboxRuntime{Version: compat(pool.class, "current"), ActorOverhead: overheadFor(pool.class)},
				RestorableRuntimes: []*ateapi.WorkerSandboxRuntime{{Version: compat(pool.class, "previous"), ActorOverhead: overheadFor(pool.class)}},
			}},
			Ips:             ips,
			IssuedEpoch:     p.issued,
			RegisteredEpoch: p.registered,
		},
	}
	p.worker = w
	return w
}

func image(name string) string {
	return "registry.example.com/agents/" + name + "@sha256:" + hexOf(64, "image", name)
}

func buildTemplate(g *gen, t tmplSpec, uid string) *ateapi.ActorTemplate {
	mounts := []*ateapi.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}
	var containers []*ateapi.Container
	for i, c := range t.containers {
		ct := &ateapi.Container{
			Name:         c,
			Image:        image(string(t.key) + "-" + c),
			Env:          []*ateapi.EnvVar{{Name: "LOG_FORMAT", Value: "json"}, {Name: "AGENT_TEMPLATE", Value: string(t.key)}},
			VolumeMounts: mounts,
		}
		if i == 0 {
			ct.WakeupProbe = &ateapi.WakeupProbe{HttpGet: &ateapi.HttpGetAction{Path: "/healthz", Port: 8080}, TimeoutSeconds: 30}
		}
		containers = append(containers, ct)
	}
	if t.key == tBrowser {
		containers[0].Resources = &ateapi.ResourceRequirements{Limits: &ateapi.Resources{MemoryBytes: 2560 * mib}}
		containers[1].Resources = &ateapi.ResourceRequirements{Limits: &ateapi.Resources{MemoryBytes: 512 * mib}}
		containers[1].Command = []string{"/usr/local/bin/mcp-server", "--transport=http", "--port=8080"}
	}
	tmpl := &ateapi.ActorTemplate{
		Metadata: &ateapi.ResourceMetadata{
			Atespace: t.atespace, Name: string(t.key), Uid: uid, Version: 4,
			CreateTime: g.ts(t.createdAge), UpdateTime: g.ts(t.createdAge - time.Minute),
		},
		Containers: containers,
		Volumes:    []*ateapi.Volume{{Name: "workspace", DurableDir: &ateapi.DurableDirVolumeSource{}}},
		Resources:  &ateapi.ResourceRequirements{Limits: &ateapi.Resources{CpuMillicores: t.cpu, MemoryBytes: t.mem}},
		SnapshotConfig: &ateapi.SnapshotConfig{
			PreferredFidelity: t.fidelity,
		},
		Status: &ateapi.ActorTemplateStatus{GoldenSnapshotStatus: &ateapi.GoldenSnapshotStatus{}},
	}
	if len(t.selector) > 0 {
		tmpl.WorkerSelector = &ateapi.Selector{MatchLabels: maps.Clone(t.selector)}
	}
	gs := tmpl.Status.GoldenSnapshotStatus
	switch t.golden {
	case goldenReady:
		gs.GoldenTag = &ateapi.ObjectRef{Atespace: t.atespace, Name: uid}
		gs.SnapshotTime = g.ts(t.goldenAge + 2*time.Minute)
		tmpl.Metadata.UpdateTime = g.ts(t.goldenAge)
		tmpl.Metadata.Version = 7
	case goldenFailed:
		// Its golden actor crashed, so the failure is on the crash clock.
		failed := g.onCrashClock(t.goldenAge)
		gs.SnapshotTime = g.ts(failed - 3*time.Minute)
		gs.ErrorMessage = "GoldenActorCrashed: golden actor crashed before its snapshot was taken: " + msgOOM
		tmpl.Metadata.UpdateTime = g.ts(failed)
		tmpl.Metadata.Version = 6
	case goldenDisabled:
		tmpl.SnapshotConfig.GoldenSnapshotConfig = &ateapi.GoldenSnapshotConfig{Mode: ateapi.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED}
		tmpl.Status.GoldenSnapshotStatus = nil
	case goldenPending:
		// The golden actor cannot be placed, so warm-up never starts.
	}
	if t.key == tADK {
		tmpl.GoldenEgressPolicy = &ateapi.EgressPolicyTemplate{Rules: []*ateapi.EgressRule{
			{Https: &ateapi.HttpsRule{Hostnames: []string{"pypi.org", "files.pythonhosted.org"}}},
		}}
	}
	return tmpl
}

func (g *gen) buildTags(d *Dataset) []*ateapi.Tag {
	var tags []*ateapi.Tag
	tagSnap := func(atespace, tagUID string, generation int32, fid ateapi.SnapshotFidelity, tmplUID, class string, state ateapi.SnapshotStorageState) *ateapi.Snapshot {
		id := hashUUID("tag-snapshot", tagUID)
		return &ateapi.Snapshot{
			Uuid: id, Generation: generation, Owner: ateapi.SnapshotOwner_SNAPSHOT_OWNER_TAG, Fidelity: fid, ActorTemplateUid: tmplUID,
			DurableSnapshot: &ateapi.SnapshotStorage{State: state, Object: &ateapi.ObjectSnapshot{SnapshotUri: fmt.Sprintf("%s/atespaces/%s/tags/%s/%s", snapshotBucket, atespace, tagUID, id)}},
			SandboxRuntime:  runtimeStamp(class, "current"),
		}
	}
	for _, tm := range d.Templates {
		gs := tm.GetStatus().GetGoldenSnapshotStatus()
		if gs.GetGoldenTag() == nil {
			continue
		}
		t := tmplSpecFor(tmplKey(tm.Metadata.Name))
		uid := hashUUID("tag", tm.Metadata.Atespace, tm.Metadata.Uid)
		tags = append(tags, &ateapi.Tag{
			Metadata:    &ateapi.ResourceMetadata{Atespace: tm.Metadata.Atespace, Name: tm.Metadata.Uid, Uid: uid, Version: 2, CreateTime: g.ts(t.goldenAge + time.Minute), UpdateTime: g.ts(t.goldenAge)},
			Status:      &ateapi.TagStatus{Snapshot: tagSnap(tm.Metadata.Atespace, uid, 1, t.fidelity, tm.Metadata.Uid, t.class, completed), ActorTemplateUid: tm.Metadata.Uid},
			Scope:       ateapi.TagScope_TAG_SCOPE_PUBLISHED,
			SourceActor: &ateapi.ObjectRef{Atespace: tm.Metadata.Atespace, Name: tm.Metadata.Uid},
		})
	}
	user := []struct {
		atespace, name, source string
		t                      tmplKey
		scope                  ateapi.TagScope
		age                    time.Duration
		state                  ateapi.SnapshotStorageState
	}{
		{"code-review", "repo-warm-baseline", "pr-8400-reviewer", tClaude, ateapi.TagScope_TAG_SCOPE_ATESPACE, 6 * 24 * time.Hour, completed},
		{"support-bots", "kb-loaded-2026-11", "ticket-40700", tADK, ateapi.TagScope_TAG_SCOPE_PUBLISHED, 9 * 24 * time.Hour, completed},
		{"research", "lit-baseline-wip", "lit-review-0042", tLang, ateapi.TagScope_TAG_SCOPE_ATESPACE, 3 * time.Minute, pending},
	}
	for _, u := range user {
		t := tmplSpecFor(u.t)
		tmplUID := hashUUID("template", t.atespace, string(t.key))
		uid := hashUUID("tag", u.atespace, u.name)
		tags = append(tags, &ateapi.Tag{
			Metadata:    &ateapi.ResourceMetadata{Atespace: u.atespace, Name: u.name, Uid: uid, Version: 2, CreateTime: g.ts(u.age), UpdateTime: g.ts(u.age - time.Second)},
			Status:      &ateapi.TagStatus{Snapshot: tagSnap(u.atespace, uid, 1, t.fidelity, tmplUID, t.class, u.state), ActorTemplateUid: tmplUID},
			Scope:       u.scope,
			SourceActor: &ateapi.ObjectRef{Atespace: u.atespace, Name: u.source},
		})
	}
	slices.SortFunc(tags, func(a, b *ateapi.Tag) int {
		if c := strings.Compare(a.Metadata.Atespace, b.Metadata.Atespace); c != 0 {
			return c
		}
		return strings.Compare(a.Metadata.Name, b.Metadata.Name)
	})
	return tags
}

func egressFor(r *actorRec) *ateapi.EgressPolicy {
	a := r.actor
	var rules []*ateapi.EgressRule
	bearer := func(uri string) *ateapi.HttpRuleEffects {
		return &ateapi.HttpRuleEffects{ReplaceHeaders: []*ateapi.CredentialHeader{{Header: "Authorization", Prefix: "Bearer ", CredentialUri: uri}}}
	}
	switch a.Metadata.Atespace {
	case "support-bots":
		rules = []*ateapi.EgressRule{
			{Https: &ateapi.HttpsRule{Hostnames: []string{"api.openai.com"}, Effects: bearer("ate-secret://vault/support-bots/openai-api-key")}},
			{Https: &ateapi.HttpsRule{Hostnames: []string{"*.zendesk.com"}, Effects: bearer("ate-secret://vault/support-bots/zendesk-token")}},
		}
	case "code-review":
		rules = []*ateapi.EgressRule{
			{Https: &ateapi.HttpsRule{Hostnames: []string{"github.com", "api.github.com"}, Effects: &ateapi.HttpRuleEffects{ReplaceHeaders: []*ateapi.CredentialHeader{
				{Header: "Authorization", Prefix: "Bearer ", ActorJwt: &ateapi.ActorJwtSource{Audiences: []string{"https://sts.example.com"}, ExpirationSeconds: 900}},
			}}}},
			{TlsPassthrough: &ateapi.TlsPassthroughRule{Hostnames: []string{"*.githubusercontent.com"}, Ports: &ateapi.Ports{Numbers: []int32{443}}}},
			{Http: &ateapi.HttpRule{Hostnames: []string{"proxy.golang.org"}}},
		}
	case "sandbox-dev":
		// Deliberately wide: every name, every port.
		rules = []*ateapi.EgressRule{{Https: &ateapi.HttpsRule{Hostnames: []string{"*"}, Ports: &ateapi.Ports{All: &ateapi.AllPorts{}}}}}
	default:
		return nil
	}
	return &ateapi.EgressPolicy{
		Metadata: &ateapi.ResourceMetadata{
			Atespace: a.Metadata.Atespace, Name: "default", Uid: hashUUID("egress", a.Metadata.Uid), Version: 1,
			CreateTime: a.Metadata.CreateTime, UpdateTime: a.Metadata.CreateTime,
		},
		Rules: rules,
	}
}

// goldenWarmup is how long before its golden actor crashed the template
// update that started the warm-up came.
const goldenWarmup = 4 * time.Minute

// drainAge is how long ago the worker's Pod began terminating, the drain's
// start; zero when the Pod is not Terminating or was not read.
func (p *workerPlan) drainAge(start time.Time) time.Duration {
	if p.podInfo.DrainStart.IsZero() {
		return 0
	}
	return max(start.Sub(p.podInfo.DrainStart), 0)
}

// podAge caps scenario ages tied to this worker: nothing on a Pod is older
// than the Pod. Unbounded when the Pod was not read.
func (p *workerPlan) podAge(start time.Time) time.Duration {
	if !p.podInfo.Found || p.podInfo.Created.IsZero() {
		return time.Duration(1<<63 - 1)
	}
	return max(start.Sub(p.podInfo.Created), 0)
}
