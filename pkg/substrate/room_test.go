package substrate

import (
	"slices"
	"strings"
	"testing"
)

// liveWorker is the worker kagent 1.0.1 ran on under Substrate 1.0.0: 1,000
// actor slots on about 2 CPU cores, one 1-CPU actor already placed.
func liveWorker() Worker {
	return Worker{
		Name: "w1", Namespace: "agents", Pool: "pool", Pod: "pool-0", State: WorkerActive,
		Capacity:  &Resources{CPUMillicores: 1930, MemoryBytes: 7 << 30, Actors: 1000},
		Allocated: &Resources{CPUMillicores: 1000, MemoryBytes: 1<<30 + 64<<20, Actors: 1},
	}
}

var agentTemplate = Template{Atespace: "kagent", Name: "agent", CPUMillicores: 1000, MemoryBytes: 1 << 30}

func TestHasRoomFor(t *testing.T) {
	exact := liveWorker()
	exact.Capacity.CPUMillicores = 2000
	memoryShort := liveWorker()
	memoryShort.Capacity = &Resources{CPUMillicores: 8000, MemoryBytes: 2 << 30, Actors: 1000}
	memoryShort.Allocated.MemoryBytes = 1<<30 + 300<<20
	overbooked := liveWorker()
	overbooked.Capacity.CPUMillicores = 500
	slotsFull := liveWorker()
	slotsFull.Allocated.Actors = 1000
	tests := []struct {
		name string
		w    Worker
		t    Template
		want bool
	}{
		{"the live case: 930m unbooked, the template asks 1 CPU", liveWorker(), agentTemplate, false},
		{"an exact fit is room", exact, agentTemplate, true},
		{"memory short", memoryShort, agentTemplate, false},
		{"a template with no limits needs only a slot", liveWorker(), Template{}, true},
		{"a sized booking does not fit overbooked CPU headroom", overbooked, Template{MemoryBytes: 1 << 30}, false},
		{"a zero-sized booking only needs a slot even if CPU is overbooked", overbooked, Template{}, true},
		{"no free slot is no room", slotsFull, Template{}, false},
		{"no reported capacity is no room", Worker{State: WorkerActive}, Template{}, false},
	}
	for _, tc := range tests {
		if got := HasRoomFor(tc.w, tc.t); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJoinPoolsNoRoom(t *testing.T) {
	pools := []Pool{{Namespace: "agents", Name: "pool", Desired: i64(1), ReportedReady: i64(1), Labels: map[string]string{"class": "gvisor"}}}
	live := Inputs{Pools: pools, Workers: []Worker{liveWorker()}, WorkersRead: true, WorkersComplete: true, Templates: []Template{agentTemplate}, TemplatesRead: true, TemplatesComplete: true}
	only := func(in Inputs) PoolRow {
		t.Helper()
		rows := JoinPools(in)
		if len(rows) != 1 {
			t.Fatalf("rows = %+v", rows)
		}
		return rows[0]
	}
	if r := only(live); r.Capacity != CapacityNoRoom || r.Lacking != LackingCPU {
		t.Errorf("the live case: capacity %q lacking %q, want noRoom on cpu", r.Capacity, r.Lacking)
	}

	memory := live
	memory.Workers = []Worker{liveWorker()}
	memory.Workers[0].Capacity.CPUMillicores = 8000
	memory.Workers[0].Capacity.MemoryBytes = 2 << 30
	if r := only(memory); r.Capacity != CapacityNoRoom || r.Lacking != LackingMemory {
		t.Errorf("memory-bound: capacity %q lacking %q", r.Capacity, r.Lacking)
	}
	both := live
	both.Workers = []Worker{liveWorker()}
	both.Workers[0].Capacity.MemoryBytes = 2 << 30
	if r := only(both); r.Capacity != CapacityNoRoom || r.Lacking != LackingCPUAndMemory {
		t.Errorf("both: capacity %q lacking %q", r.Capacity, r.Lacking)
	}

	free := func(name string, in Inputs) {
		t.Helper()
		if r := only(in); r.Capacity != CapacityFree || r.Lacking != "" {
			t.Errorf("%s: capacity %q lacking %q, want free with nothing lacking", name, r.Capacity, r.Lacking)
		}
	}
	unlimited := live
	unlimited.Templates = []Template{{Atespace: "kagent", Name: "small"}}
	free("a template with no limits", unlimited)

	partialWorkers := live
	partialWorkers.WorkersComplete = false
	free("an incomplete worker read: a worker not read may have room", partialWorkers)

	unreadTemplates := live
	unreadTemplates.Templates, unreadTemplates.TemplatesRead, unreadTemplates.TemplatesComplete = nil, false, false
	free("templates not read", unreadTemplates)
	partialTemplates := live
	partialTemplates.TemplatesComplete = false
	free("a partial template list: a template not read may fit", partialTemplates)

	mixed := live
	mixed.Templates = []Template{agentTemplate, {Atespace: "kagent", Name: "half", CPUMillicores: 500, MemoryBytes: 512 << 20}}
	free("one template still fits", mixed)

	none := live
	none.Templates = []Template{{Atespace: "a", Name: "gpu", WorkerSelector: map[string]string{"class": "gpu"}}}
	free("no template can run on the pool: nothing to judge", none)

	// A template that fits but selects another pool does not make room here.
	elsewhere := live
	elsewhere.Templates = []Template{agentTemplate, {Atespace: "a", Name: "tiny-gpu", WorkerSelector: map[string]string{"class": "gpu"}}}
	if r := only(elsewhere); r.Capacity != CapacityNoRoom {
		t.Errorf("a template selecting another pool counted: %q", r.Capacity)
	}

	// No free slot stays full, and a shortfall stays short.
	full := live
	full.Workers = []Worker{liveWorker()}
	full.Workers[0].Allocated.Actors = 1000
	if r := only(full); r.Capacity != CapacityFull {
		t.Errorf("no free slot: %q, want full", r.Capacity)
	}
	short := live
	short.Pools = []Pool{{Namespace: "agents", Name: "pool", Desired: i64(2), ReportedReady: i64(1), Labels: pools[0].Labels}}
	if r := only(short); r.Capacity != CapacityShort {
		t.Errorf("short of workers: %q, want short", r.Capacity)
	}
}

func TestNeedsAttentionPoolNoRoom(t *testing.T) {
	pools := []Pool{{Namespace: "agents", Name: "pool", Desired: i64(1), ReportedReady: i64(1)}}
	in := Inputs{
		Pools: pools, PoolsUsable: true, PoolsComplete: true, Workers: []Worker{liveWorker()}, WorkersRead: true, WorkersComplete: true,
		Templates: []Template{agentTemplate}, TemplatesRead: true, TemplatesComplete: true, Now: now,
	}
	rows := NeedsAttention(in)
	r := find(t, rows, "poolNoRoom:agents/pool")
	if r.Severity != SeverityWarning || r.Count != 1 || r.Subject.Kind != "WorkerPool" || r.Subject.Name != "pool" {
		t.Errorf("row = %+v", r)
	}
	if r.Title != "agents / pool has no CPU left for another actor" {
		t.Errorf("title = %q", r.Title)
	}
	want := "None of its active workers with a free actor slot has the CPU left to start a fresh actor of the template that can run on it. The most CPU unbooked on such a worker is 930m CPU; the worker would book at least 1 CPU."
	if r.Reading != want {
		t.Errorf("reading = %q\nwant      %q", r.Reading, want)
	}
	if n := len(strings.Fields(r.NextStep)); n > 25 || !strings.Contains(r.NextStep, "lower CPU limits") {
		t.Errorf("next step (%d words) = %q", n, r.NextStep)
	}
	if slices.ContainsFunc(rows, func(a Attention) bool { return a.Rule == RulePoolFull }) {
		t.Error("a pool with free slots is not full")
	}

	// Where a kagent session runs on the pool's templates, kagent makes them
	// from its defaults: the step lowers those, not the templates.
	in.Actors, in.ActorsRead = []Actor{{Atespace: "kagent", Name: "session-1", Template: agentTemplate.Ref(), State: StateSuspended, Kagent: &KagentSession{SessionID: "1"}}}, true
	r = find(t, NeedsAttention(in), "poolNoRoom:agents/pool")
	if r.NextStep != "Add workers, or for actors that look like kagent sessions, check kagent's config.runtimeDefaults.cpu (and .memory) before lowering limits." {
		t.Errorf("kagent step = %q", r.NextStep)
	}
	in.Workers[0].Capacity = &Resources{CPUMillicores: 8000, MemoryBytes: 2 << 30, Actors: 1000}
	r = find(t, NeedsAttention(in), "poolNoRoom:agents/pool")
	if !strings.Contains(r.Reading, "The most memory unbooked on such a worker is 960Mi; the worker would book at least 1Gi.") || !strings.Contains(r.NextStep, "config.runtimeDefaults.memory (and .cpu)") || len(strings.Fields(r.NextStep)) > 25 {
		t.Errorf("memory row = %q / %q", r.Reading, r.NextStep)
	}
	// A standalone sandbox's template is not an Agent's: the generic step.
	in.Actors[0].Kagent.Sandbox = true
	if r = find(t, NeedsAttention(in), "poolNoRoom:agents/pool"); !strings.Contains(r.NextStep, "recreate them") {
		t.Errorf("sandbox step = %q", r.NextStep)
	}
	in.Actors[0].Kagent.Sandbox = false

	check := func(in Inputs) Check {
		for _, c := range Checks(in) {
			if c.Rule == RulePoolNoRoom {
				return c
			}
		}
		t.Fatal("no poolNoRoom check")
		return Check{}
	}
	if c := check(in); !c.Ran {
		t.Errorf("check = %+v", c)
	}
	in.WorkersComplete = false
	if c := check(in); c.Ran || c.Why != "the worker list is incomplete" {
		t.Errorf("incomplete workers: check = %+v", c)
	}
	if slices.ContainsFunc(NeedsAttention(in), func(a Attention) bool { return a.Rule == RulePoolNoRoom }) {
		t.Error("an incomplete worker read raised noRoom")
	}
	in.WorkersComplete, in.TemplatesComplete = true, false
	if c := check(in); c.Ran || c.Why != "the template list is incomplete" {
		t.Errorf("incomplete templates: check = %+v", c)
	}
}

func TestPoolRoomUsesLiveWorkerLabelsAndRuntimeOverhead(t *testing.T) {
	w := Worker{Namespace: "agents", Pool: "pool", State: WorkerActive, Labels: map[string]string{"class": "live"},
		Capacity: &Resources{Actors: 5, MemoryBytes: 1<<30 + 20<<20}, DefaultMemoryOverhead: 10 << 20, DefaultMemoryOverheadPerGiB: 20 << 20}
	in := Inputs{Pools: []Pool{{Namespace: "agents", Name: "pool", Labels: map[string]string{"class": "stale"}}},
		Workers: []Worker{w}, WorkersRead: true, WorkersComplete: true, TemplatesComplete: true,
		Templates: []Template{
			{Name: "agent", MemoryBytes: 1 << 30, WorkerSelector: map[string]string{"class": "live"}},
			{Name: "tiny-elsewhere", WorkerSelector: map[string]string{"class": "stale"}},
		}}
	if p := JoinPools(in)[0]; p.Capacity != CapacityNoRoom || p.Lacking != LackingMemory || p.room.ask.MemoryBytes != 1<<30+30<<20 {
		t.Errorf("only the live selector matches, and the worker books fixed plus proportional overhead: %+v", p)
	}
	in.Workers[0].Capacity.MemoryBytes = 1
	in.Templates[0].MemoryBytes = 0
	if p := JoinPools(in)[0]; p.Capacity != CapacityNoRoom || p.room.ask.MemoryBytes != 10<<20 {
		t.Errorf("a zero-limit template still books fixed overhead: %+v", p)
	}
}
