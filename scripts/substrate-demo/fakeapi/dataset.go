package main

import (
	"crypto/sha1"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

// fixtureNamespace is where the WorkerPools and worker Pods live (--namespace).
var fixtureNamespace = "radar-substrate-demo"

const (
	fixtureLabel   = "radar.skyhook.io/fixture"
	fixtureValue   = "substrate-demo"
	snapshotBucket = "s3://substrate-snapshots-demo"

	mib = int64(1) << 20
	gib = int64(1) << 30
)

// PodInfo is what the dataset needs from a worker Pod. When the Pod is
// missing, the Worker still exists in the API (as it would for a Worker whose
// Pod went away) with a stable made-up UID.
type PodInfo struct {
	Name        string
	UID         string
	Node        string
	IP          string
	Terminating bool
	Found       bool
	Created     time.Time
}

type poolSpec struct {
	name    string
	labels  map[string]string
	class   string
	config  string
	desired int
}

// Workers mirror their pool's labels. Only batch-pool and microvm-pool carry
// labels that template selectors match on.
var poolSpecs = []poolSpec{
	{name: "agent-pool", class: "gvisor", config: "gvisor-default", desired: 5, labels: map[string]string{fixtureLabel: fixtureValue}},
	{name: "healthy-pool", class: "gvisor", config: "gvisor-default", desired: 2, labels: map[string]string{fixtureLabel: fixtureValue}},
	{name: "batch-pool", class: "gvisor", config: "gvisor-default", desired: 1, labels: map[string]string{"sandbox": "gvisor", "tier": "batch", fixtureLabel: fixtureValue}},
	{name: "microvm-pool", class: "microvm", config: "microvm", desired: 1, labels: map[string]string{"sandbox": "microvm", fixtureLabel: fixtureValue}},
}

func poolByName(name string) poolSpec {
	for _, p := range poolSpecs {
		if p.name == name {
			return p
		}
	}
	panic("unknown pool " + name)
}

type tmplKey string

const (
	tADK      tmplKey = "adk-support-agent"
	tClaude   tmplKey = "claude-code-gvisor"
	tLang     tmplKey = "langgraph-research"
	tCodex    tmplKey = "codex-microvm"
	tBrowser  tmplKey = "mcp-browser"
	tNotebook tmplKey = "notebook-runner"
	tGPU      tmplKey = "gpu-eval"
	// missingTemplate names actors still, but Substrate has no such template.
	missingTemplate tmplKey = "triage-v1"
)

type goldenKind int

const (
	goldenReady goldenKind = iota
	goldenFailed
	goldenDisabled
	goldenPending
)

type tmplSpec struct {
	key        tmplKey
	atespace   string
	cpu        int64
	mem        int64
	selector   map[string]string
	fidelity   ateapi.SnapshotFidelity
	golden     goldenKind
	goldenAge  time.Duration
	createdAge time.Duration
	count      int
	first      int
	nameFmt    string
	class      string
	containers []string
}

var tmplSpecs = []tmplSpec{
	{key: tADK, atespace: "support-bots", cpu: 500, mem: 1 * gib, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, golden: goldenReady, goldenAge: 5 * 24 * time.Hour, createdAge: 34 * 24 * time.Hour, count: 611, first: 40700, nameFmt: "ticket-%05d", class: "gvisor", containers: []string{"agent"}},
	{key: tClaude, atespace: "code-review", cpu: 1000, mem: 1536 * mib, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, golden: goldenReady, goldenAge: 2 * time.Hour, createdAge: 21 * 24 * time.Hour, count: 430, first: 8400, nameFmt: "pr-%04d-reviewer", class: "gvisor", containers: []string{"agent"}},
	{key: tLang, atespace: "research", cpu: 2000, mem: 2 * gib, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, golden: goldenReady, goldenAge: 2 * 24 * time.Hour, createdAge: 18 * 24 * time.Hour, count: 160, first: 40, nameFmt: "lit-review-%04d", class: "gvisor", containers: []string{"agent"}},
	{key: tCodex, atespace: "code-review", cpu: 4000, mem: 8 * gib, selector: map[string]string{"sandbox": "microvm"}, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, golden: goldenReady, goldenAge: 24 * time.Hour, createdAge: 9 * 24 * time.Hour, count: 56, first: 1000, nameFmt: "codex-task-%04d", class: "microvm", containers: []string{"codex"}},
	{key: tBrowser, atespace: "sandbox-dev", cpu: 1000, mem: 3 * gib, selector: map[string]string{"sandbox": "gvisor"}, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_ROOTFS, golden: goldenFailed, goldenAge: 25 * time.Minute, createdAge: 30 * time.Minute, count: 15, first: 1, nameFmt: "browser-sbx-%03d", class: "gvisor", containers: []string{"browser", "mcp-server"}},
	{key: tNotebook, atespace: "research", cpu: 2000, mem: 4 * gib, selector: map[string]string{"tier": "batch"}, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_VOLUMES, golden: goldenDisabled, createdAge: 12 * 24 * time.Hour, count: 12, first: 1, nameFmt: "notebook-%03d", class: "gvisor", containers: []string{"kernel"}},
	{key: tGPU, atespace: "research", cpu: 4000, mem: 16 * gib, selector: map[string]string{"accelerator": "nvidia-l4"}, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, golden: goldenPending, createdAge: 3 * time.Hour, count: 3, first: 1, nameFmt: "gpu-eval-%02d", class: "gvisor", containers: []string{"eval"}},
}

func tmplSpecFor(k tmplKey) tmplSpec {
	for _, t := range tmplSpecs {
		if t.key == k {
			return t
		}
	}
	panic("unknown template " + k)
}

var atespaceSpecs = []struct {
	name string
	age  time.Duration
}{
	{"code-review", 40 * 24 * time.Hour},
	{"research", 38 * 24 * time.Hour},
	{"sandbox-dev", 6 * 24 * time.Hour},
	{"support-bots", 41 * 24 * time.Hour},
}

type mix struct {
	t tmplKey
	n int
}

// workerPlan is one Worker. mix counts every actor assigned to it, including
// the named special cases placed on it below.
type workerPlan struct {
	pool       string
	pod        string
	owned      bool
	state      ateapi.WorkerState
	stateAge   time.Duration
	createdAge time.Duration
	issued     int64
	registered int64
	observed   int64
	slots      int32
	cpu        int64
	mem        int64
	mix        []mix

	podInfo PodInfo
	name    string
	actors  []*actorRec
	worker  *ateapi.Worker
}

func defaultPlans() []*workerPlan {
	gv := func(p *workerPlan) *workerPlan { p.slots, p.cpu, p.mem = 24, 16000, 64*gib; return p }
	return []*workerPlan{
		// Its Pod is Running and Ready, so Kubernetes looks fine while
		// Substrate has stopped placing on it.
		gv(&workerPlan{pool: "agent-pool", pod: "agent-pool-worker-0", owned: true, state: ateapi.WorkerState_WORKER_STATE_UNAVAILABLE, stateAge: 12 * time.Minute, createdAge: 40 * time.Hour, issued: 1, registered: 1, observed: 1, mix: []mix{{tADK, 2}, {tClaude, 1}}}),
		gv(&workerPlan{pool: "agent-pool", pod: "agent-pool-worker-1", owned: true, state: ateapi.WorkerState_WORKER_STATE_ACTIVE, stateAge: 39 * time.Hour, createdAge: 39 * time.Hour, issued: 1, registered: 1, observed: 1, mix: []mix{{tADK, 12}, {tClaude, 4}, {tLang, 2}}}),
		// Restarted 9 minutes ago; the control plane has not yet crashed the
		// actors the previous process ran, so three still read RUNNING.
		gv(&workerPlan{pool: "agent-pool", pod: "agent-pool-worker-2", owned: true, state: ateapi.WorkerState_WORKER_STATE_ACTIVE, stateAge: 9 * time.Minute, createdAge: 39 * time.Hour, issued: 4, registered: 4, observed: 3, mix: []mix{{tADK, 8}, {tClaude, 3}, {tLang, 2}}}),
		gv(&workerPlan{pool: "agent-pool", pod: "agent-pool-worker-3", owned: true, state: ateapi.WorkerState_WORKER_STATE_DRAINING, stateAge: 0, createdAge: 39 * time.Hour, issued: 1, registered: 1, observed: 1, mix: []mix{{tADK, 2}, {tClaude, 1}, {tLang, 1}}}),
		gv(&workerPlan{pool: "healthy-pool", pod: "healthy-pool-worker-0", owned: true, state: ateapi.WorkerState_WORKER_STATE_ACTIVE, stateAge: 39 * time.Hour, createdAge: 39 * time.Hour, issued: 1, registered: 1, observed: 1, mix: []mix{{tADK, 20}, {tClaude, 4}}}),
		// Restarted 6 minutes ago and already reconciled: the two actors it
		// lost are CRASHED with "worker restarted while hosting the actor".
		gv(&workerPlan{pool: "healthy-pool", pod: "healthy-pool-worker-1", owned: true, state: ateapi.WorkerState_WORKER_STATE_ACTIVE, stateAge: 6 * time.Minute, createdAge: 39 * time.Hour, issued: 2, registered: 2, observed: 2, mix: []mix{{tADK, 10}, {tClaude, 3}, {tLang, 1}}}),
		gv(&workerPlan{pool: "batch-pool", pod: "batch-pool-worker-0", owned: true, state: ateapi.WorkerState_WORKER_STATE_ACTIVE, stateAge: 20 * time.Hour, createdAge: 20 * time.Hour, issued: 1, registered: 1, observed: 1, mix: []mix{{tBrowser, 3}, {tNotebook, 2}, {tClaude, 2}, {tLang, 1}}}),
		{pool: "microvm-pool", pod: "microvm-pool-worker-0", owned: true, state: ateapi.WorkerState_WORKER_STATE_ACTIVE, stateAge: 20 * time.Hour, createdAge: 20 * time.Hour, issued: 1, registered: 1, observed: 1, slots: 8, cpu: 32000, mem: 96 * gib, mix: []mix{{tCodex, 6}}},
	}
}

// special pins a named actor to a scenario. worker is a pod name for actors
// that hold a worker; lastWorker names where a crashed actor last ran.
type special struct {
	state      ateapi.ActorState
	worker     string
	lastWorker string
	age        time.Duration
	crash      string
	crashAge   time.Duration
	staleEpoch bool
	stuck      bool
}

const (
	msgOOM            = "killed for exceeding its memory limit"
	msgWorkerRestart  = "worker restarted while hosting the actor"
	msgRestoreFailure = `atelet Restore: while restoring "agent" application container: container "agent" exited with status 1 during restore`
)

func specials() map[string]special {
	run := ateapi.ActorState_ACTOR_STATE_RUNNING
	res := ateapi.ActorState_ACTOR_STATE_RESUMING
	sus := ateapi.ActorState_ACTOR_STATE_SUSPENDING
	crashed := ateapi.ActorState_ACTOR_STATE_CRASHED
	m := map[string]special{
		"support-bots/ticket-41001":    {state: run, worker: "agent-pool-worker-0", age: 3 * time.Hour},
		"support-bots/ticket-41002":    {state: run, worker: "agent-pool-worker-0", age: 2 * time.Hour},
		"code-review/pr-8801-reviewer": {state: run, worker: "agent-pool-worker-0", age: 70 * time.Minute},

		"support-bots/ticket-41302": {state: res, worker: "agent-pool-worker-1", age: 38 * time.Minute, stuck: true},
		"support-bots/ticket-40977": {state: sus, worker: "agent-pool-worker-1", age: 47 * time.Minute, stuck: true},
		"support-bots/ticket-41290": {state: res, worker: "agent-pool-worker-1", age: 8 * time.Second},

		"support-bots/ticket-41120":    {state: run, worker: "agent-pool-worker-2", age: 2 * time.Hour, staleEpoch: true},
		"support-bots/ticket-41121":    {state: run, worker: "agent-pool-worker-2", age: 95 * time.Minute, staleEpoch: true},
		"code-review/pr-8822-reviewer": {state: run, worker: "agent-pool-worker-2", age: 40 * time.Minute, staleEpoch: true},

		"support-bots/ticket-41210":    {state: run, worker: "agent-pool-worker-3", age: 4 * time.Hour},
		"research/lit-review-0197":     {state: run, worker: "agent-pool-worker-3", age: 25 * time.Minute},
		"support-bots/ticket-41211":    {state: sus, worker: "agent-pool-worker-3", age: 20 * time.Second},
		"code-review/pr-8829-reviewer": {state: sus, worker: "agent-pool-worker-3", age: 15 * time.Second},

		"code-review/pr-8818-reviewer": {state: res, worker: "healthy-pool-worker-0", age: 41 * time.Minute, stuck: true},
		"support-bots/ticket-41250":    {state: sus, worker: "healthy-pool-worker-1", age: 6 * time.Second},
		"sandbox-dev/browser-sbx-014":  {state: res, worker: "batch-pool-worker-0", age: 12 * time.Second},
		"code-review/codex-task-1050":  {state: res, worker: "microvm-pool-worker-0", age: 5 * time.Second},

		"code-review/pr-8812-reviewer": {state: crashed, lastWorker: "healthy-pool-worker-1", crash: msgWorkerRestart, crashAge: 6 * time.Minute},
		"code-review/pr-8790-reviewer": {state: crashed, lastWorker: "healthy-pool-worker-1", crash: msgWorkerRestart, crashAge: 6 * time.Minute},
		"support-bots/ticket-41077":    {state: crashed, lastWorker: "healthy-pool-worker-0", crash: msgRestoreFailure, crashAge: 31 * time.Minute},

		"research/lit-review-0151":     {state: ateapi.ActorState_ACTOR_STATE_REVERTING, lastWorker: "agent-pool-worker-1", crash: msgOOM, crashAge: 70 * time.Minute, age: 40 * time.Second},
		"code-review/pr-8701-reviewer": {state: ateapi.ActorState_ACTOR_STATE_REVERTING, lastWorker: "healthy-pool-worker-0", crash: msgWorkerRestart, crashAge: 3 * time.Hour, age: 75 * time.Second},
		"sandbox-dev/browser-sbx-007":  {state: ateapi.ActorState_ACTOR_STATE_DELETING, age: 30 * time.Second},
	}
	// Eleven OOM crashes of one template inside the last hour, spread over
	// the workers that ran them.
	oom := []struct {
		n    int
		age  time.Duration
		last string
	}{
		{193, 4 * time.Minute, "agent-pool-worker-1"}, {188, 9 * time.Minute, "agent-pool-worker-2"},
		{175, 14 * time.Minute, "agent-pool-worker-1"}, {170, 19 * time.Minute, "healthy-pool-worker-1"},
		{166, 23 * time.Minute, "agent-pool-worker-2"}, {159, 28 * time.Minute, "agent-pool-worker-1"},
		{147, 33 * time.Minute, "healthy-pool-worker-1"}, {139, 38 * time.Minute, "agent-pool-worker-1"},
		{122, 44 * time.Minute, "agent-pool-worker-2"}, {108, 49 * time.Minute, "batch-pool-worker-0"},
		{96, 55 * time.Minute, "agent-pool-worker-1"},
	}
	for _, o := range oom {
		m[fmt.Sprintf("research/lit-review-%04d", o.n)] = special{state: crashed, lastWorker: o.last, crash: msgOOM, crashAge: o.age}
	}
	return m
}

// stuckUpload is suspended with its snapshot still only on the node: the
// async upload has been PENDING for 52 minutes.
const stuckUpload = "support-bots/ticket-40801"

type actorRec struct {
	actor     *ateapi.Actor
	tmpl      tmplSpec
	tmplUID   string
	sp        *special
	plan      *workerPlan
	lastPlan  *workerPlan
	golden    bool
	placedAge time.Duration
	// stateAge is how long ago the actor entered its current state.
	stateAge time.Duration
}

// Dataset is everything the fake API serves, built once at start.
type Dataset struct {
	Start       time.Time
	Atespaces   []*ateapi.Atespace
	Templates   []*ateapi.ActorTemplate
	Actors      []*ateapi.Actor
	Workers     []*ateapi.Worker
	Assignments map[string][]*ateapi.ActorAssignment
	Tags        []*ateapi.Tag
	Egress      map[string]*ateapi.EgressPolicy

	plans []*workerPlan
	recs  []*actorRec
}

func hashUUID(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func hexOf(n int, parts ...string) string {
	var sb strings.Builder
	for i := 0; sb.Len() < n; i++ {
		h := sha1.Sum([]byte(strings.Join(append(parts, fmt.Sprint(i)), "\x00")))
		fmt.Fprintf(&sb, "%x", h)
	}
	return sb.String()[:n]
}

type gen struct {
	rng   *rand.Rand
	start time.Time
	nodes []string
}

func (g *gen) ts(age time.Duration) *timestamppb.Timestamp {
	return timestamppb.New(g.start.Add(-age).Truncate(time.Millisecond))
}

func (g *gen) between(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(g.rng.Int64N(int64(hi-lo)))
}

// Runtime compatibility stamps. "current" is every worker's default runtime,
// "previous" is still restorable, and "retired" (snapshots older than three
// days) matches no worker, so those actors would cold-boot on resume.
func runtimeStamp(class, key string) *ateapi.ActorSandboxRuntime {
	return &ateapi.ActorSandboxRuntime{SandboxClass: class, Version: compat(class, key)}
}

func compat(class, key string) *ateapi.VersionedSandboxCompat {
	return &ateapi.VersionedSandboxCompat{
		SchemaVersion: "v1",
		Attributes: []*ateapi.AttributeEntry{
			{Key: "architecture", Value: "amd64"},
			{Key: "cpu_features", Value: cpuFeatures(class)},
			{Key: "sandbox_bundle_hash", Value: "sha256:" + hexOf(64, "bundle", class, key)},
		},
	}
}

func overheadFor(class string) *ateapi.RuntimeOverhead {
	if class == "microvm" {
		return &ateapi.RuntimeOverhead{MemoryBytes: 96 * mib, MemoryBytesPerGib: 8 * mib}
	}
	return &ateapi.RuntimeOverhead{MemoryBytes: 48 * mib}
}

func overheadBytes(class string, limit int64) int64 {
	o := overheadFor(class)
	return o.MemoryBytes + o.MemoryBytesPerGib*limit/gib
}

// fallbackNodes stand in for the node of a Pod that is missing or unscheduled.
var fallbackNodes = []string{"unscheduled-node-a", "unscheduled-node-b", "unscheduled-node-c"}

// BuildDataset is deterministic for a given start time and pod set: random
// draws never depend on the pods, which only fill in identity fields.
func BuildDataset(start time.Time, pods map[string]PodInfo) *Dataset {
	return buildDataset(start, pods, time.Time{})
}

// buildDataset with a nonzero crashAnchor pins the crashes already written to
// the Pods' logs to it (crashstory.go).
func buildDataset(start time.Time, pods map[string]PodInfo, crashAnchor time.Time) *Dataset {
	g := &gen{rng: rand.New(rand.NewPCG(20261118, 7)), start: start}
	d := &Dataset{Start: start, Assignments: map[string][]*ateapi.ActorAssignment{}, Egress: map[string]*ateapi.EgressPolicy{}}
	d.plans = defaultPlans()

	nodeSet := map[string]bool{}
	for _, p := range d.plans {
		pi, ok := pods[p.pod]
		if !ok || !pi.Found {
			pi = PodInfo{Name: p.pod, UID: hashUUID("pod", p.pod)}
		}
		// A Pod that is missing or not yet scheduled still gets a node and IP,
		// so the Worker record stays complete.
		if pi.Node == "" {
			pi.Node = fallbackNodes[len(p.pod)%len(fallbackNodes)]
		}
		if pi.IP == "" {
			pi.IP = fmt.Sprintf("10.0.%d.%d", 4+len(p.pod)%2, 100+len(p.pod))
		}
		p.podInfo = pi
		p.name = pi.UID
		nodeSet[pi.Node] = true
	}
	for n := range nodeSet {
		g.nodes = append(g.nodes, n)
	}
	sort.Strings(g.nodes)

	for _, a := range atespaceSpecs {
		d.Atespaces = append(d.Atespaces, &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{
			Name: a.name, Uid: hashUUID("atespace", a.name), Version: 1, CreateTime: g.ts(a.age), UpdateTime: g.ts(a.age),
		}})
	}

	tmplUID := map[tmplKey]string{}
	for _, t := range tmplSpecs {
		tmplUID[t.key] = hashUUID("template", t.atespace, string(t.key))
	}
	for _, t := range tmplSpecs {
		d.Templates = append(d.Templates, buildTemplate(g, t, tmplUID[t.key]))
	}

	sp := specials()
	for _, t := range tmplSpecs {
		for i := 0; i < t.count; i++ {
			name := fmt.Sprintf(t.nameFmt, t.first+i)
			r := &actorRec{tmpl: t, tmplUID: tmplUID[t.key]}
			r.actor = &ateapi.Actor{
				Metadata: &ateapi.ResourceMetadata{
					Atespace: t.atespace, Name: name, Uid: hashUUID("actor", t.atespace, name),
					Version:    int64(2 + g.rng.IntN(180)),
					CreateTime: g.ts(g.between(time.Hour, t.createdAge)),
				},
				ActorTemplate: &ateapi.ObjectRef{Atespace: t.atespace, Name: string(t.key)},
				Status:        &ateapi.ActorStatus{},
			}
			if s, ok := sp[t.atespace+"/"+name]; ok {
				s := s
				r.sp = &s
			}
			d.recs = append(d.recs, r)
		}
		// The golden actor is named after the template's UID.
		if t.golden == goldenFailed || t.golden == goldenPending {
			uid := tmplUID[t.key]
			r := &actorRec{tmpl: t, tmplUID: uid, golden: true}
			r.actor = &ateapi.Actor{
				Metadata: &ateapi.ResourceMetadata{
					Atespace: t.atespace, Name: uid, Uid: hashUUID("actor", t.atespace, uid), Version: 3, CreateTime: g.ts(t.createdAge),
				},
				ActorTemplate: &ateapi.ObjectRef{Atespace: t.atespace, Name: string(t.key)},
				Status:        &ateapi.ActorStatus{},
			}
			if t.golden == goldenFailed {
				r.sp = &special{state: ateapi.ActorState_ACTOR_STATE_CRASHED, lastWorker: "batch-pool-worker-0", crash: msgOOM, crashAge: t.goldenAge}
			}
			d.recs = append(d.recs, r)
		}
	}

	// Actors of a template that was deleted: Substrate deletes a template's
	// golden actor and tag, not the actors created from it.
	gone := tmplSpec{key: missingTemplate, atespace: "support-bots", cpu: 500, mem: gib, fidelity: ateapi.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY, createdAge: 60 * 24 * time.Hour, count: 4, first: 1, nameFmt: "triage-legacy-%02d", class: "gvisor"}
	for i := 0; i < gone.count; i++ {
		name := fmt.Sprintf(gone.nameFmt, gone.first+i)
		d.recs = append(d.recs, &actorRec{tmpl: gone, tmplUID: hashUUID("template", gone.atespace, string(gone.key)), actor: &ateapi.Actor{
			Metadata: &ateapi.ResourceMetadata{
				Atespace: gone.atespace, Name: name, Uid: hashUUID("actor", gone.atespace, name), Version: 9,
				CreateTime: g.ts(g.between(30*24*time.Hour, gone.createdAge)),
			},
			ActorTemplate: &ateapi.ObjectRef{Atespace: gone.atespace, Name: string(gone.key)},
			Status:        &ateapi.ActorStatus{},
		}})
	}

	planByPod := map[string]*workerPlan{}
	for _, p := range d.plans {
		planByPod[p.pod] = p
	}
	for _, r := range d.recs {
		if r.sp == nil {
			continue
		}
		if r.sp.worker != "" {
			r.plan = planByPod[r.sp.worker]
		}
		if r.sp.lastWorker != "" {
			r.lastPlan = planByPod[r.sp.lastWorker]
		}
	}

	// Fill each worker's mix with ordinary actors of that template.
	pool := map[tmplKey][]*actorRec{}
	for _, r := range d.recs {
		if r.sp == nil && !r.golden && r.actor.Metadata.Name != strings.TrimPrefix(stuckUpload, "support-bots/") {
			pool[r.tmpl.key] = append(pool[r.tmpl.key], r)
		}
	}
	for _, t := range tmplSpecs {
		k, rs := t.key, pool[t.key]
		perm := g.rng.Perm(len(rs))
		shuffled := make([]*actorRec, len(rs))
		for i, j := range perm {
			shuffled[i] = rs[j]
		}
		pool[k] = shuffled
	}
	for _, p := range d.plans {
		for _, m := range p.mix {
			have := 0
			for _, r := range d.recs {
				if r.plan == p && r.tmpl.key == m.t {
					have++
				}
			}
			for ; have < m.n; have++ {
				r := pool[m.t][0]
				pool[m.t] = pool[m.t][1:]
				r.plan = p
			}
		}
	}

	for _, r := range d.recs {
		g.fillStatus(r)
		// Nothing has run since the template went, 12 days ago: a resume
		// fails before it changes the actor.
		if r.tmpl.key == missingTemplate {
			age := 12*24*time.Hour + time.Duration(len(r.actor.Metadata.Name))*time.Hour
			r.stateAge = age
			r.actor.Metadata.UpdateTime = g.ts(age)
			for _, sn := range r.actor.Status.Snapshots {
				sn.Locality = ""
				sn.DurableSnapshot.State = completed
				sn.SandboxRuntime = runtimeStamp(r.tmpl.class, "retired")
			}
			r.actor.Status.AssignedNode = ""
		}
		// What an actor did on its worker happened on that Pod.
		if r.plan != nil {
			if limit := r.plan.podAge(start); r.stateAge > limit {
				r.stateAge = limit
				r.actor.Metadata.UpdateTime = g.ts(limit)
			}
			r.placedAge = min(r.placedAge, r.plan.podAge(start))
		}
	}

	if !crashAnchor.IsZero() {
		d.anchorCrashes(g, crashAnchor)
	}

	for _, p := range d.plans {
		d.Workers = append(d.Workers, g.buildWorker(p))
	}
	for _, r := range d.recs {
		if r.plan != nil {
			p := r.plan
			p.actors = append(p.actors, r)
			res := &ateapi.Resources{CpuMillicores: r.tmpl.cpu, MemoryBytes: r.tmpl.mem + overheadBytes(r.tmpl.class, r.tmpl.mem)}
			placed := g.ts(r.placedAge)
			d.Assignments[p.name] = append(d.Assignments[p.name], &ateapi.ActorAssignment{
				Metadata:  &ateapi.ResourceMetadata{Name: r.actor.Metadata.Uid, Uid: hashUUID("assignment", r.actor.Metadata.Uid), Version: 1, CreateTime: placed, UpdateTime: placed},
				Actor:     &ateapi.ObjectRef{Atespace: r.actor.Metadata.Atespace, Name: r.actor.Metadata.Name},
				ActorUid:  r.actor.Metadata.Uid,
				Resources: res,
			})
		} else if r.lastPlan != nil {
			r.lastPlan.actors = append(r.lastPlan.actors, r)
		}
	}
	for _, p := range d.plans {
		var cpu, mem int64
		for _, a := range d.Assignments[p.name] {
			cpu += a.Resources.CpuMillicores
			mem += a.Resources.MemoryBytes
		}
		p.worker.Status.Allocated = &ateapi.WorkerResources{CpuMillicores: cpu, MemoryBytes: mem, Actors: int32(len(d.Assignments[p.name]))}
	}

	for _, r := range d.recs {
		d.Actors = append(d.Actors, r.actor)
	}
	sortActors(d.Actors)
	slices.SortFunc(d.Workers, func(a, b *ateapi.Worker) int { return strings.Compare(a.Metadata.Name, b.Metadata.Name) })

	d.Tags = g.buildTags(d)
	for _, r := range d.recs {
		if pol := egressFor(r); pol != nil {
			d.Egress[r.actor.Metadata.Atespace+"/"+r.actor.Metadata.Name] = pol
		}
	}
	return d
}

// cpuFeatures is a mask in Substrate's canonical encoding: lowercase hex, no
// prefix, no leading zeros.
func cpuFeatures(class string) string {
	return "f" + hexOf(15, "cpu", class)
}
