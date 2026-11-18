package substrate

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/timeutil"
)

// transitional states are the ones an actor should pass through, not stay in.
var transitional = map[string]bool{StateResuming: true, StateSuspending: true, StateReverting: true, StateDeleting: true}

// HeldFor is how long the actor's state has held at least: Substrate records
// no transition time, but every state change updates the actor, so its last
// update is a lower bound. Zero when Substrate reported no update time.
func HeldFor(updated, now time.Time) time.Duration {
	if updated.IsZero() || now.Before(updated) {
		return 0
	}
	return now.Sub(updated)
}

// ActorReason explains a state other than running in a few words: the crash
// message, how long a transition has lasted, or a suspended actor's pending
// upload. Empty for a running actor or one with nothing to add. The upload
// reading is literal: the API records no upload start, only the actor's last
// change, which is not Substrate's durability clock.
func ActorReason(a Actor, now time.Time) string {
	switch {
	case a.State == StateCrashed:
		if a.Crash != nil && a.Crash.Message != "" {
			return a.Crash.Message
		}
		return "Substrate recorded no crash message"
	case transitional[a.State]:
		label := strings.ToUpper(a.State[:1]) + a.State[1:]
		if d := HeldFor(a.UpdateTime, now); d > 0 {
			return label + " for " + timeutil.FormatAgeShort(d)
		}
		return label
	case a.State == StateSuspended:
		if s := NewestSnapshot(a); s != nil && s.Durable == DurablePending {
			if d := HeldFor(a.UpdateTime, now); d > 0 {
				return "Upload pending; last updated " + timeutil.FormatAgeShort(d) + " ago"
			}
			return "Upload pending"
		}
	}
	return ""
}

// ActorNode is the node an actor is on or was last on: its worker's node,
// the node holding its local snapshot, or its newest snapshot's locality.
func ActorNode(a Actor) string {
	if a.Assignment != nil && a.Assignment.Node != "" {
		return a.Assignment.Node
	}
	if a.AssignedNode != "" {
		return a.AssignedNode
	}
	if s := NewestSnapshot(a); s != nil {
		return s.Locality
	}
	return ""
}

// ActorRow is one actor as the actor tables show it.
type ActorRow struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
	State    string `json:"state"`
	// Reason is ActorReason; absent when there is nothing to add.
	Reason   string `json:"reason,omitempty"`
	Template Ref    `json:"template"`
	// Worker is the hosting worker Pod; absent when the actor has no worker.
	Worker *WorkerPodRef `json:"worker,omitempty"`
	// Node is ActorNode; absent when Substrate names none.
	Node string `json:"node,omitempty"`
	// NewestSnapshot is the highest generation, finished or not.
	NewestSnapshot *Snapshot `json:"newestSnapshot,omitempty"`
	// ResumeGeneration is the generation a resume restores from
	// (ResumeSource); absent when the actor boots cold.
	ResumeGeneration int32 `json:"resumeGeneration,omitempty"`
	// Resume is ResumeOf: resumesWith, and the condition it holds on.
	Resume
	// WaitingOn is where a transitional actor waits (WaitingOn); absent in
	// any other state.
	WaitingOn *Waiting `json:"waitingOn,omitempty"`
	// WorkerImpact is what its worker's state means for it (WorkerImpactOf);
	// absent when the worker means nothing for it or was not read.
	WorkerImpact *WorkerImpact `json:"workerImpact,omitempty"`
	CreateTime   time.Time     `json:"createTime,omitzero"`
	UpdateTime   time.Time     `json:"updateTime,omitzero"`
}

// WorkerPodRef locates a worker by its Pod and pool.
type WorkerPodRef struct {
	Worker    string `json:"worker"`
	Namespace string `json:"namespace"`
	Pool      string `json:"pool"`
	Pod       string `json:"pod"`
}

// RowOf reads one actor into its table row.
func RowOf(a Actor, now time.Time, rc ResumeContext) ActorRow {
	row := ActorRow{
		Atespace: a.Atespace, Name: a.Name, State: a.State, Template: a.Template,
		Reason: ActorReason(a, now), Node: ActorNode(a), NewestSnapshot: NewestSnapshot(a),
		Resume: ResumeOf(a, rc), CreateTime: a.CreateTime, UpdateTime: a.UpdateTime,
	}
	if src := ResumeSource(a); src != nil {
		row.ResumeGeneration = src.Generation
	}
	if as := a.Assignment; as != nil {
		row.Worker = &WorkerPodRef{Worker: as.Worker, Namespace: as.Namespace, Pool: as.Pool, Pod: as.Pod}
	}
	row.WaitingOn = WaitingOn(a)
	return row
}

// PoolLabels is a pool and the labels selectors match against.
type PoolLabels struct {
	Ref    PoolRef
	Labels map[string]string
}

// KnownPools lists every pool Radar knows of: the WorkerPools in its cache,
// then pools only Substrate's workers name. A WorkerPool's own labels win;
// Substrate copies them onto its workers, so a worker's labels stand in for a
// pool Radar cannot read. Sorted by namespace and name.
func KnownPools(pools []Pool, workers []Worker) []PoolLabels {
	byKey := map[string]PoolLabels{}
	for _, p := range pools {
		byKey[p.Ref().Key()] = PoolLabels{Ref: p.Ref(), Labels: p.Labels}
	}
	for _, w := range workers {
		if _, ok := byKey[w.PoolRef().Key()]; !ok {
			byKey[w.PoolRef().Key()] = PoolLabels{Ref: w.PoolRef(), Labels: w.Labels}
		}
	}
	out := slices.Collect(maps.Values(byKey))
	slices.SortFunc(out, func(a, b PoolLabels) int { return strings.Compare(a.Ref.Key(), b.Ref.Key()) })
	return out
}

// SelectorMatches is Substrate's equality-only selector: every pair must be
// present. An empty selector matches every pool.
func SelectorMatches(selector, labels map[string]string) bool {
	for k, v := range selector {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// MatchingPools are the pools whose labels a template's selector matches.
// A match is not a placement claim: Substrate also checks the actor's own
// selector, its snapshot's class and node, the runtime, registration and the
// actor's booking against free resources.
func MatchingPools(t Template, pools []PoolLabels) []PoolRef {
	return matching(pools, t.WorkerSelector, nil)
}

// ActorMatchingPools are the pools matching both the template's selector and
// the actor's own: Substrate requires both.
func ActorMatchingPools(a Actor, t Template, pools []PoolLabels) []PoolRef {
	return matching(pools, t.WorkerSelector, a.WorkerSelector)
}

func matching(pools []PoolLabels, selectors ...map[string]string) []PoolRef {
	out := []PoolRef{}
	for _, p := range pools {
		ok := true
		for _, sel := range selectors {
			ok = ok && SelectorMatches(sel, p.Labels)
		}
		if ok {
			out = append(out, p.Ref)
		}
	}
	return out
}

// HasFreeSlot reports whether a worker has an actor slot left: fewer actors
// allocated than its actor capacity. Substrate treats an unreported dimension
// as zero, so no capacity, or a capacity of 0 actors, is no slot. It says
// nothing about whether a given actor fits: that also needs its CPU and
// memory booking, runtime overhead included.
func HasFreeSlot(w Worker) bool {
	if w.Capacity == nil {
		return false
	}
	var used int32
	if w.Allocated != nil {
		used = w.Allocated.Actors
	}
	return used < w.Capacity.Actors
}

// WorkerCounts counts workers by state.
type WorkerCounts struct {
	Total       int `json:"total"`
	Active      int `json:"active"`
	Draining    int `json:"draining"`
	Unavailable int `json:"unavailable"`
}

func (c *WorkerCounts) add(w Worker) {
	c.Total++
	switch w.State {
	case WorkerActive:
		c.Active++
	case WorkerDraining:
		c.Draining++
	case WorkerUnavailable:
		c.Unavailable++
	}
}

// Usage is an allocated amount out of a capacity, summed over active workers:
// the only ones Substrate places on.
type Usage struct {
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
}

// Placement totals over a set of workers. Slots counts the actor slots of
// active workers.
type Placement struct {
	Workers WorkerCounts `json:"workers"`
	Slots   Usage        `json:"slots"`
	CPU     Usage        `json:"cpuMillicores"`
	Memory  Usage        `json:"memoryBytes"`
	// Complete: every Substrate worker was read. Otherwise the totals are
	// lower bounds over the workers read, and a worker or slot not counted
	// may exist.
	Complete bool `json:"complete"`
	// WithFreeSlots is how many active workers have a free actor slot
	// (HasFreeSlot), for the pool's capacity verdict; not on the wire.
	WithFreeSlots int `json:"-"`
}

func (p *Placement) add(w Worker) {
	p.Workers.add(w)
	if w.State != WorkerActive {
		return
	}
	if HasFreeSlot(w) {
		p.WithFreeSlots++
	}
	if w.Capacity != nil {
		p.Slots.Total += int64(w.Capacity.Actors)
		p.CPU.Total += w.Capacity.CPUMillicores
		p.Memory.Total += w.Capacity.MemoryBytes
	}
	if w.Allocated != nil {
		p.Slots.Used += int64(w.Allocated.Actors)
		p.CPU.Used += w.Allocated.CPUMillicores
		p.Memory.Used += w.Allocated.MemoryBytes
	}
}

// PlacementOf totals the given workers.
func PlacementOf(workers []Worker) Placement {
	var p Placement
	for _, w := range workers {
		p.add(w)
	}
	return p
}

// Pool capacity verdicts.
const (
	// CapacityShort: fewer workers take actors than the pool wants.
	CapacityShort = "short"
	// CapacityFull: no active worker has a free actor slot.
	CapacityFull = "full"
	// CapacityFree: some active worker has a free actor slot; a given actor
	// may still not fit.
	CapacityFree = "free"
)

// PoolRow is one pool, joining Radar's WorkerPool with Substrate's workers.
type PoolRow struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Kubernetes is the WorkerPool from Radar's cache; absent when only
	// Substrate's workers name the pool.
	Kubernetes *Pool `json:"kubernetes,omitempty"`
	// Placement is absent when Substrate's workers were not read.
	Placement *Placement `json:"placement,omitempty"`
	// Actors assigned to the pool's workers; absent when actors were not read.
	Actors         *int     `json:"actors,omitempty"`
	SandboxClasses []string `json:"sandboxClasses,omitempty"`
	// Capacity is short, full or free; absent when neither side was read
	// well enough to say.
	Capacity string `json:"capacity,omitempty"`
	// Service is Substrate's own verdict and facts for the pool, from
	// Prometheus; absent when Prometheus was not read for it.
	Service *PoolService `json:"service,omitempty"`
}

func (r PoolRow) Ref() PoolRef { return PoolRef{Namespace: r.Namespace, Name: r.Name} }

// JoinPools joins WorkerPools with Substrate's workers by namespace and name.
// workersRead and actorsRead say whether those lists were read well enough to
// count; a nil slice with read=true means Substrate has none. workersComplete
// says the worker list holds every worker, without which a pool's placement
// proves a free slot but never a shortfall or that no slot is free.
func JoinPools(pools []Pool, workers []Worker, workersRead, workersComplete bool, actors []Actor, actorsRead bool) []PoolRow {
	rows := map[string]*PoolRow{}
	row := func(ref PoolRef) *PoolRow {
		r := rows[ref.Key()]
		if r == nil {
			r = &PoolRow{Namespace: ref.Namespace, Name: ref.Name}
			if workersRead {
				r.Placement = &Placement{Complete: workersComplete}
			}
			if actorsRead {
				zero := 0
				r.Actors = &zero
			}
			rows[ref.Key()] = r
		}
		return r
	}
	for i := range pools {
		p := pools[i]
		row(p.Ref()).Kubernetes = &p
	}
	if workersRead {
		for _, w := range workers {
			row(w.PoolRef()).Placement.add(w)
		}
	}
	if actorsRead {
		for _, a := range actors {
			if as := a.Assignment; as != nil {
				*row(PoolRef{Namespace: as.Namespace, Name: as.Pool}).Actors++
			}
		}
	}
	classes := map[string]map[string]bool{}
	for _, p := range pools {
		for _, c := range p.SandboxClasses {
			addClass(classes, p.Ref().Key(), c)
		}
	}
	for _, w := range workers {
		for _, c := range w.SandboxClasses {
			addClass(classes, w.PoolRef().Key(), c)
		}
	}
	out := make([]PoolRow, 0, len(rows))
	for key, r := range rows {
		r.SandboxClasses = slices.Sorted(maps.Keys(classes[key]))
		r.Capacity = poolCapacity(*r)
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b PoolRow) int { return strings.Compare(a.Ref().Key(), b.Ref().Key()) })
	return out
}

func addClass(classes map[string]map[string]bool, key, class string) {
	if classes[key] == nil {
		classes[key] = map[string]bool{}
	}
	classes[key][class] = true
}

func poolCapacity(r PoolRow) string {
	var desired int64 = -1
	if r.Kubernetes != nil && r.Kubernetes.Desired != nil {
		desired = *r.Kubernetes.Desired
	}
	if r.Placement != nil && !r.Placement.Complete && r.Placement.WithFreeSlots > 0 {
		return CapacityFree
	}
	if r.Placement != nil && r.Placement.Complete {
		if desired >= 0 && int64(r.Placement.Workers.Active) < desired {
			return CapacityShort
		}
		if r.Placement.WithFreeSlots == 0 {
			if r.Placement.Workers.Active == 0 {
				return ""
			}
			return CapacityFull
		}
		return CapacityFree
	}
	if r.Kubernetes != nil && desired >= 0 && r.Kubernetes.ReadyWorkers() < desired {
		return CapacityShort
	}
	return ""
}

// StateCounts counts actors by state.
type StateCounts struct {
	Total   int            `json:"total"`
	ByState map[string]int `json:"byState"`
}

// NewStateCounts returns counts with every state present at zero, so a
// state with no actors reads 0 rather than missing.
func NewStateCounts() StateCounts {
	c := StateCounts{ByState: map[string]int{}}
	for _, s := range ActorStates {
		c.ByState[s] = 0
	}
	return c
}

func (c *StateCounts) Add(state string) {
	c.Total++
	c.ByState[state]++
}

// CountStates counts actors by state.
func CountStates(actors []Actor) StateCounts {
	c := NewStateCounts()
	for _, a := range actors {
		c.Add(a.State)
	}
	return c
}

// TemplateRow is one template as the templates screen shows it.
type TemplateRow struct {
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
	Golden   Golden `json:"golden"`
	// Actors is absent when actors were not read.
	Actors *StateCounts `json:"actors,omitempty"`
	// MatchingPools are the pools whose labels the template's selector
	// matches. Null when Radar cannot say: no pool list was read, or none of a
	// partial list matches. Empty only when the pool list is complete and
	// nothing matches.
	MatchingPools []PoolRef `json:"matchingPools"`
	// MatchingPoolsComplete: the pool list matched against is complete, so
	// MatchingPools is every pool the selector matches.
	MatchingPoolsComplete bool              `json:"matchingPoolsComplete"`
	SandboxClasses        []string          `json:"sandboxClasses,omitempty"`
	WorkerSelector        map[string]string `json:"workerSelector,omitempty"`
	CPUMillicores         int64             `json:"cpuMillicores,omitempty"`
	MemoryBytes           int64             `json:"memoryBytes,omitempty"`
	PreferredFidelity     string            `json:"preferredFidelity"`
	StorageLocation       string            `json:"storageLocation,omitempty"`
	Containers            []string          `json:"containers,omitempty"`
	UpdateTime            time.Time         `json:"updateTime,omitzero"`
}

// TemplateRows reads templates with their actors and matching pools.
// poolsUsable: some pool list (WorkerPools or workers) was read. poolsComplete:
// it holds every pool, so a selector that matches none matches nothing.
func TemplateRows(templates []Template, actors []Actor, actorsRead bool, pools []PoolLabels, poolsUsable, poolsComplete bool, poolRows []PoolRow) []TemplateRow {
	counts := map[Ref]*StateCounts{}
	if actorsRead {
		for _, t := range templates {
			c := NewStateCounts()
			counts[t.Ref()] = &c
		}
		for _, a := range actors {
			if c := counts[a.Template]; c != nil {
				c.Add(a.State)
			}
		}
	}
	classes := map[string][]string{}
	for _, r := range poolRows {
		classes[r.Ref().Key()] = r.SandboxClasses
	}
	out := make([]TemplateRow, 0, len(templates))
	for _, t := range templates {
		row := TemplateRow{
			Atespace: t.Atespace, Name: t.Name, Golden: GoldenOf(t), Actors: counts[t.Ref()],
			WorkerSelector: t.WorkerSelector, CPUMillicores: t.CPUMillicores, MemoryBytes: t.MemoryBytes,
			PreferredFidelity: t.PreferredFidelity, StorageLocation: t.StorageLocation,
			Containers: t.Containers, UpdateTime: t.UpdateTime,
		}
		if poolsUsable {
			row.MatchingPoolsComplete = poolsComplete
			if matches := MatchingPools(t, pools); len(matches) > 0 || poolsComplete {
				row.MatchingPools = matches
			}
			set := map[string]bool{}
			for _, p := range row.MatchingPools {
				for _, c := range classes[p.Key()] {
					set[c] = true
				}
			}
			row.SandboxClasses = slices.Sorted(maps.Keys(set))
			if row.Golden.State == GoldenBuilding && poolsComplete && len(row.MatchingPools) == 0 {
				row.Golden.Blocked = GoldenBlockedNoMatchingPool
			}
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b TemplateRow) int {
		return strings.Compare(a.Atespace+"/"+a.Name, b.Atespace+"/"+b.Name)
	})
	return out
}
