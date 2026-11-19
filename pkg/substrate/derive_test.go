package substrate

import (
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

func i64(v int64) *int64 { return &v }

func TestActorReading(t *testing.T) {
	snaps := []Snapshot{{Generation: 6, Fidelity: FidelityMemory, Durable: DurableCompleted}, {Generation: 7, Fidelity: FidelityRootFS, Durable: DurablePending, Locality: "node-a"}}
	tests := []struct {
		name        string
		actor       Actor
		reason      string
		resumesWith string
		newest      int32
		node        string
	}{
		{"running has no reason", Actor{State: StateRunning, Assignment: &Assignment{Node: "node-w"}}, "", ResumeCold, 0, "node-w"},
		{"crash message", Actor{State: StateCrashed, Crash: &Crash{Message: "OOMKilled (limit 2Gi)"}}, "OOMKilled (limit 2Gi)", ResumeCold, 0, ""},
		{"crash without message", Actor{State: StateCrashed}, "Substrate recorded no crash message", ResumeCold, 0, ""},
		{"resuming for 6m", Actor{State: StateResuming, UpdateTime: ago(6 * time.Minute)}, "Resuming for 6m", ResumeCold, 0, ""},
		{"deleting without time", Actor{State: StateDeleting}, "Deleting", ResumeCold, 0, ""},
		{"suspended pending upload, local files", Actor{State: StateSuspended, UpdateTime: ago(12 * time.Minute), Snapshots: snaps}, "Upload pending; last updated 12m ago", ResumeFilesystem, 7, "node-a"},
		{"suspended stored", Actor{State: StateSuspended, Snapshots: snaps[:1], AssignedNode: "node-b"}, "", ResumeMemory, 6, "node-b"},
		{"volumes only", Actor{State: StateSuspended, Snapshots: []Snapshot{{Generation: 1, Fidelity: FidelityVolumes, Durable: DurableCompleted}}}, "", ResumeVolumes, 1, ""},
		{"unspecified fidelity", Actor{State: StateSuspended, Snapshots: []Snapshot{{Generation: 1, Durable: DurableCompleted}}}, "", ResumeUnknown, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActorReason(tc.actor, now); got != tc.reason {
				t.Errorf("reason = %q, want %q", got, tc.reason)
			}
			if got := ResumeOf(tc.actor, ResumeContext{}).With; got != tc.resumesWith {
				t.Errorf("resumesWith = %q, want %q", got, tc.resumesWith)
			}
			var gen int32
			if s := NewestSnapshot(tc.actor); s != nil {
				gen = s.Generation
			}
			if gen != tc.newest {
				t.Errorf("newest = %d, want %d", gen, tc.newest)
			}
			if got := ActorNode(tc.actor); got != tc.node {
				t.Errorf("node = %q, want %q", got, tc.node)
			}
		})
	}
}

func TestRowCause(t *testing.T) {
	cases := []struct {
		name  string
		actor Actor
		cause string
	}{
		{"memory-limit kill", Actor{State: StateCrashed, Crash: &Crash{Message: "killed for exceeding its memory limit"}}, CauseMemoryLimit},
		{"memory-limit kill wrapping the exit error", Actor{State: StateCrashed, Crash: &Crash{Message: "killed for exceeding its memory limit: signal: killed"}}, CauseMemoryLimit},
		{"another crash", Actor{State: StateCrashed, Crash: &Crash{Message: "container agent: exit code 1"}}, ""},
		{"no message", Actor{State: StateCrashed}, ""},
		{"not crashed", Actor{State: StateSuspended, Crash: &Crash{Message: "killed for exceeding its memory limit"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := RowOf(tc.actor, now, ResumeContext{})
			if row.Cause != tc.cause {
				t.Errorf("cause = %q, want %q", row.Cause, tc.cause)
			}
			// The cause is a label beside the message, never in its place.
			if tc.actor.Crash != nil && tc.actor.State == StateCrashed && row.Reason != tc.actor.Crash.Message {
				t.Errorf("reason = %q, want Substrate's message", row.Reason)
			}
		})
	}
}

func TestGoldenOf(t *testing.T) {
	tag := &Ref{Atespace: "a", Name: "golden-v1"}
	tests := []struct {
		name string
		t    Template
		want string
	}{
		{"disabled wins", Template{GoldenDisabled: true, GoldenError: "x"}, GoldenDisabled},
		{"error is terminal", Template{GoldenError: "WarmupFailed: exited 137", GoldenTag: tag}, GoldenFailed},
		{"tag means ready", Template{GoldenTag: tag}, GoldenReady},
		{"nothing yet", Template{}, GoldenBuilding},
	}
	for _, tc := range tests {
		if got := GoldenOf(tc.t).State; got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	// Only an error naming a memory-limit kill names the cause.
	if g := GoldenOf(Template{GoldenError: "WarmupFailed: exited 137"}); g.Cause != "" {
		t.Errorf("another failure names no cause: %+v", g)
	}
	if g := GoldenOf(Template{GoldenError: "WarmupFailed: golden actor crashed: " + memoryLimitMessage}); g.Cause != CauseMemoryLimit {
		t.Errorf("a memory-limit kill is named: %+v", g)
	}
}

func TestMatchingPools(t *testing.T) {
	pools := KnownPools(
		[]Pool{{Namespace: "agents", Name: "gvisor-pool", Labels: map[string]string{"class": "gvisor"}}},
		[]Worker{
			{Namespace: "agents", Pool: "gvisor-pool", Labels: map[string]string{"class": "ignored"}},
			{Namespace: "batch", Pool: "microvm-pool", Labels: map[string]string{"class": "microvm"}},
		},
	)
	tests := []struct {
		name     string
		selector map[string]string
		want     []string
	}{
		{"empty selector matches every pool", nil, []string{"agents/gvisor-pool", "batch/microvm-pool"}},
		{"Kubernetes labels win over worker labels", map[string]string{"class": "gvisor"}, []string{"agents/gvisor-pool"}},
		{"worker labels stand in for a pool Radar cannot read", map[string]string{"class": "microvm"}, []string{"batch/microvm-pool"}},
		{"no match", map[string]string{"class": "gpu"}, []string{}},
		{"every pair must match", map[string]string{"class": "gvisor", "zone": "a"}, []string{}},
	}
	for _, tc := range tests {
		got := []string{}
		for _, p := range MatchingPools(Template{WorkerSelector: tc.selector}, pools) {
			got = append(got, p.Key())
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func worker(name, pool, state string, capActors, allocActors int32) Worker {
	return Worker{
		Name: name, Namespace: "agents", Pool: pool, Pod: name + "-pod", Node: "node-" + name, State: state,
		Capacity:  &Resources{CPUMillicores: 8000, MemoryBytes: 16 << 30, Actors: capActors},
		Allocated: &Resources{CPUMillicores: 1000 * int64(allocActors), MemoryBytes: int64(allocActors) << 30, Actors: allocActors},
	}
}

func TestHasFreeSlot(t *testing.T) {
	full := worker("w", "p", WorkerActive, 4, 4)
	cpuFull := worker("w", "p", WorkerActive, 8, 2)
	cpuFull.Allocated.CPUMillicores = 8000
	tests := []struct {
		name string
		w    Worker
		want bool
	}{
		{"free slot", worker("w", "p", WorkerActive, 8, 3), true},
		{"slots full", full, false},
		// A free slot is not a fit claim: the CPU shortfall is the actor's
		// booking to check, not the slot's.
		{"cpu used up still has a free slot", cpuFull, true},
		{"no capacity reported", Worker{State: WorkerActive}, false},
		{"zero actor capacity is zero slots", worker("w", "p", WorkerActive, 0, 0), false},
	}
	for _, tc := range tests {
		if got := HasFreeSlot(tc.w); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJoinPools(t *testing.T) {
	pools := []Pool{
		{Namespace: "agents", Name: "short", Desired: i64(3), Ready: 3},
		{Namespace: "agents", Name: "full", Desired: i64(1), Ready: 1, SandboxClasses: []string{"gvisor"}},
		{Namespace: "agents", Name: "free", Desired: i64(1), Ready: 1},
		{Namespace: "agents", Name: "k8s-short", Desired: i64(2), Ready: 1},
	}
	workers := []Worker{
		worker("s1", "short", WorkerActive, 8, 1), worker("s2", "short", WorkerUnavailable, 8, 0),
		worker("f1", "full", WorkerActive, 4, 4),
		worker("e1", "free", WorkerActive, 8, 2), worker("e2", "free", WorkerDraining, 8, 5),
		worker("o1", "only-in-substrate", WorkerActive, 2, 0),
	}
	workers[2].SandboxClasses = []string{"gvisor"}
	actors := []Actor{
		{Name: "a", State: StateRunning, Assignment: &Assignment{Worker: "e1", Namespace: "agents", Pool: "free"}},
		{Name: "b", State: StateSuspended},
	}

	withAPI := map[string]PoolRow{}
	for _, r := range JoinPools(pools, workers, true, true, actors, true) {
		withAPI[r.Name] = r
	}
	want := map[string]string{"short": CapacityShort, "full": CapacityFull, "free": CapacityFree, "k8s-short": CapacityShort, "only-in-substrate": CapacityFree}
	for name, capacity := range want {
		if got := withAPI[name].Capacity; got != capacity {
			t.Errorf("%s capacity = %q, want %q", name, got, capacity)
		}
	}
	free := withAPI["free"]
	if free.Placement.Slots != (Usage{Used: 2, Total: 8}) || free.Placement.Workers.Draining != 1 || *free.Actors != 1 {
		t.Errorf("free pool placement = %+v actors %d; draining workers must not count toward slots", free.Placement, *free.Actors)
	}
	if withAPI["only-in-substrate"].Kubernetes != nil {
		t.Error("a pool only Substrate names has no Kubernetes side")
	}
	if !slices.Equal(withAPI["full"].SandboxClasses, []string{"gvisor"}) {
		t.Errorf("classes = %v", withAPI["full"].SandboxClasses)
	}

	for _, r := range JoinPools(pools, nil, false, false, nil, false) {
		if r.Placement != nil || r.Actors != nil {
			t.Errorf("%s: unread workers or actors must be absent, not zero", r.Name)
		}
		wantCap := map[string]string{"k8s-short": CapacityShort}[r.Name]
		if r.Capacity != wantCap {
			t.Errorf("%s without the API: capacity %q, want %q (Kubernetes alone shows only ready < desired)", r.Name, r.Capacity, wantCap)
		}
	}
}

func TestTemplateRows(t *testing.T) {
	templates := []Template{
		{Atespace: "a", Name: "ok", WorkerSelector: map[string]string{"class": "gvisor"}},
		{Atespace: "a", Name: "nopool", WorkerSelector: map[string]string{"class": "gpu"}},
	}
	actors := []Actor{
		{Atespace: "a", Name: "x", Template: Ref{"a", "ok"}, State: StateRunning},
		{Atespace: "a", Name: "y", Template: Ref{"a", "ok"}, State: StateCrashed},
	}
	pools := []Pool{{Namespace: "ns", Name: "p", Labels: map[string]string{"class": "gvisor"}, SandboxClasses: []string{"gvisor"}}}
	rows := TemplateRows(templates, actors, true, KnownPools(pools, nil), true, true, JoinPools(pools, nil, false, false, nil, false))
	byName := map[string]TemplateRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if r := byName["ok"]; len(r.MatchingPools) != 1 || r.Actors.Total != 2 || r.Actors.ByState[StateCrashed] != 1 || !slices.Equal(r.SandboxClasses, []string{"gvisor"}) {
		t.Errorf("ok = %+v", r)
	}
	if r := byName["nopool"]; r.MatchingPools == nil || len(r.MatchingPools) != 0 || r.Actors.Total != 0 {
		t.Errorf("nopool must have an empty, non-nil pool list and zero actors: %+v", r)
	}
	if byName["nopool"].Golden.Blocked != GoldenBlockedNoMatchingPool || byName["ok"].Golden.Blocked != "" {
		t.Errorf("a building golden snapshot with no pool is blocked: %+v / %+v", byName["nopool"].Golden, byName["ok"].Golden)
	}
	unknown := TemplateRows(templates, nil, false, nil, false, false, nil)
	if unknown[0].MatchingPools != nil || unknown[0].Actors != nil {
		t.Error("unknown pools and actors must be absent")
	}
	partial := map[string]TemplateRow{}
	for _, r := range TemplateRows(templates, nil, false, KnownPools(pools, nil), true, false, nil) {
		partial[r.Name] = r
	}
	if r := partial["ok"]; len(r.MatchingPools) != 1 || r.MatchingPoolsComplete {
		t.Errorf("a match in a partial list is shown, marked incomplete: %+v", r)
	}
	if r := partial["nopool"]; r.MatchingPools != nil {
		t.Errorf("no match in a partial list is unknown, not empty: %+v", r)
	}
	if partial["nopool"].Golden.Blocked != "" {
		t.Error("an incomplete pool list cannot say the build is blocked")
	}
}

func TestResumeSource(t *testing.T) {
	tpl := Ref{"a", "t"}
	tests := []struct {
		name  string
		snaps []Snapshot
		uids  TemplateUIDs
		want  string
		gen   int32
	}{
		{"in-progress generation is not the source", []Snapshot{
			{Generation: 1, Fidelity: FidelityVolumes, Durable: DurableCompleted},
			{Generation: 2, Fidelity: FidelityMemory, Durable: DurablePending},
		}, nil, ResumeVolumes, 1},
		{"local files make a source before upload", []Snapshot{{Generation: 3, Fidelity: FidelityMemory, Durable: DurablePending, Locality: "n"}}, nil, ResumeMemory, 3},
		{"nothing usable boots cold", []Snapshot{{Generation: 2, Fidelity: FidelityMemory, Durable: DurablePending}}, nil, ResumeCold, 0},
		{"same template keeps memory", []Snapshot{{Generation: 4, Fidelity: FidelityMemory, Durable: DurableCompleted, ActorTemplateUID: "uid-1"}}, TemplateUIDs{tpl: "uid-1"}, ResumeMemory, 4},
		{"replaced template restores volumes", []Snapshot{{Generation: 4, Fidelity: FidelityMemory, Durable: DurableCompleted, ActorTemplateUID: "uid-1"}}, TemplateUIDs{tpl: "uid-2"}, ResumeVolumes, 4},
		{"template not read is unknown", []Snapshot{{Generation: 4, Fidelity: FidelityMemory, Durable: DurableCompleted, ActorTemplateUID: "uid-1"}}, nil, ResumeUnknown, 4},
	}
	for _, tc := range tests {
		a := Actor{Template: tpl, State: StateSuspending, Snapshots: tc.snaps}
		row := RowOf(a, now, NewResumeContext(tc.uids, nil, false))
		if row.With != tc.want || row.ResumeGeneration != tc.gen {
			t.Errorf("%s: resumesWith %s from gen %d, want %s from gen %d", tc.name, row.With, row.ResumeGeneration, tc.want, tc.gen)
		}
		if newest := NewestSnapshot(a); newest == nil || row.NewestSnapshot.Generation != newest.Generation {
			t.Errorf("%s: newest snapshot must stay the highest generation", tc.name)
		}
	}
}
