package substrate

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRecoveryIsTheLatestStoredGeneration(t *testing.T) {
	stored := Snapshot{Generation: 5, Fidelity: FidelityMemory, Durable: DurableCompleted}
	tests := []struct {
		name      string
		actor     Actor
		gen       int32
		with      string
		discarded []Discarded
	}{
		{"newer local generation with an upload that may still finish",
			Actor{State: StateCrashed, Snapshots: []Snapshot{stored, {Generation: 6, Fidelity: FidelityMemory, Durable: DurablePending, Locality: "n"}}},
			5, ResumeMemory, []Discarded{{Generation: 6, Fidelity: FidelityMemory, Why: DiscardUploadPending}}},
		{"crashed suspend without locality: the upload is never recorded",
			Actor{State: StateCrashed, Snapshots: []Snapshot{stored, {Generation: 6, Durable: DurablePending}}},
			5, ResumeMemory, []Discarded{{Generation: 6, Why: DiscardUploadAbandoned}}},
		{"nothing stored boots cold after a revert",
			Actor{State: StateCrashed, Snapshots: []Snapshot{{Generation: 2, Fidelity: FidelityMemory, Locality: "n", Durable: DurableNone}}},
			0, ResumeCold, []Discarded{{Generation: 2, Fidelity: FidelityMemory, Why: DiscardLocalOnly}}},
		{"in-progress record a crash kept for cleanup",
			Actor{State: StateRunning, Snapshots: []Snapshot{stored, {Generation: 7, Durable: DurableNone}}},
			5, ResumeMemory, []Discarded{{Generation: 7, Why: DiscardIncomplete}}},
	}
	for _, tc := range tests {
		r := RecoveryOf(tc.actor, ResumeContext{})
		if r == nil {
			t.Fatalf("%s: not revertable", tc.name)
		}
		if r.Generation != tc.gen || r.With != tc.with || !slices.Equal(r.Discarded, tc.discarded) {
			t.Errorf("%s: %+v", tc.name, r)
		}
	}
	// The ordinary resume source stays the newer local generation.
	a := tests[0].actor
	if ResumeSource(a).Generation != 6 || RecoverySource(a).Generation != 5 {
		t.Error("resume source and recovery source are different facts")
	}
	if RecoveryOf(Actor{State: StateSuspended, Snapshots: []Snapshot{stored}}, ResumeContext{}) != nil {
		t.Error("a suspended actor without a local snapshot is not revertable")
	}
}

func TestMemoryResumeNeedsACompatibleRuntime(t *testing.T) {
	rt := func(bundle, cpu string) Runtime {
		return Runtime{Class: "gvisor", Schema: "v1", Attributes: map[string]string{"sandbox_bundle_hash": bundle, "cpu_features": cpu}}
	}
	snapRT := rt("A", "5")
	memory := []Snapshot{{Generation: 3, Fidelity: FidelityMemory, Durable: DurableCompleted, Runtime: &snapRT}}
	active := func(rts ...Runtime) Worker { return Worker{Name: "w", State: WorkerActive, Runtimes: rts} }
	rtB := rt("B", "5")
	rtSuper := rt("A", "f")
	tests := []struct {
		name    string
		actor   Actor
		workers []Worker
		read    bool
		want    Resume
	}{
		{"workers not read: conditional", Actor{State: StateSuspended, Snapshots: memory}, nil, false, Resume{ResumeMemory, ConditionCompatibleRuntime}},
		{"a compatible worker exists: still conditional on landing there", Actor{State: StateSuspended, Snapshots: memory}, []Worker{active(rtB), active(rtSuper)}, true, Resume{ResumeMemory, ConditionCompatibleRuntime}},
		{"every active worker of the class is incompatible: known downgrade", Actor{State: StateSuspended, Snapshots: memory}, []Worker{active(rtB)}, true, Resume{ResumeFilesystem, ConditionRuntimeMismatch}},
		{"resuming on an incompatible runtime", Actor{State: StateResuming, Snapshots: memory, Assignment: &Assignment{Runtime: &rtB}}, nil, false, Resume{ResumeFilesystem, ConditionRuntimeMismatch}},
		{"resuming on a compatible runtime is unconditional", Actor{State: StateResuming, Snapshots: memory, Assignment: &Assignment{Runtime: &rtSuper}}, nil, false, Resume{ResumeMemory, ""}},
	}
	for _, tc := range tests {
		if got := ResumeOf(tc.actor, NewResumeContext(nil, tc.workers, tc.read)); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if RuntimeCompatible(rt("A", "f"), rt("A", "5")) {
		t.Error("a snapshot's cpu features must be a subset of the worker's")
	}
	if RuntimeCompatible(snapRT, rt("A", "0x5")) {
		t.Error("cpu_features is lowercase hex with no prefix; anything else matches nothing")
	}
}

func TestActorMatchingPoolsNeedsBothSelectors(t *testing.T) {
	pools := []PoolLabels{
		{Ref: PoolRef{"ns", "a"}, Labels: map[string]string{"sandbox": "gvisor", "tier": "batch"}},
		{Ref: PoolRef{"ns", "b"}, Labels: map[string]string{"sandbox": "gvisor"}},
	}
	tpl := Template{WorkerSelector: map[string]string{"sandbox": "gvisor"}}
	a := Actor{WorkerSelector: map[string]string{"tier": "batch"}}
	if got := ActorMatchingPools(a, tpl, pools); len(got) != 1 || got[0].Name != "a" {
		t.Errorf("got %v", got)
	}
	if got := MatchingPools(tpl, pools); len(got) != 2 {
		t.Errorf("the template alone matches both: %v", got)
	}
}

func TestMissingTemplateNeedsACompleteTemplateRead(t *testing.T) {
	actors := []Actor{{Atespace: "a", Name: "orphan", Template: Ref{"a", "gone"}, State: StateSuspended}, {Atespace: "a", Name: "ok", Template: Ref{"a", "t"}, State: StateSuspended}}
	templates := []Template{{Atespace: "a", Name: "t"}}
	in := Inputs{Actors: actors, ActorsRead: true, Templates: templates, TemplatesRead: true, TemplatesComplete: true, Now: now}
	r := find(t, NeedsAttention(in), "missingTemplate:a/gone")
	if r.Count != 1 || len(ActorAttention([]Attention{r}, actors[0], now)) != 1 || len(ActorAttention([]Attention{r}, actors[1], now)) != 0 {
		t.Errorf("row = %+v", r)
	}
	in.TemplatesComplete = false
	if slices.Contains(rules(NeedsAttention(in)), "missingTemplate:a/gone") {
		t.Error("a partial template read cannot say a template is gone")
	}
	for _, c := range Checks(in) {
		if c.Rule == RuleMissingTemplate && (c.Ran || c.Why == "") {
			t.Errorf("the check must say it did not run, and why: %+v", c)
		}
	}
}

func TestDrainingIsInformationalUntilThePodDeadline(t *testing.T) {
	w := worker("d", "p", WorkerDraining, 8, 1)
	w.UpdateTime = ago(20 * time.Minute)
	actors := []Actor{{Atespace: "a", Name: "x", State: StateRunning, Assignment: &Assignment{Worker: "d"}}}
	base := Inputs{Actors: actors, ActorsRead: true, Workers: []Worker{w}, WorkersRead: true, Now: now}

	in := base
	in.WorkerPods = WorkerPods{"agents/d-pod": {Ready: true, HasIP: true, Deadline: now.Add(40 * time.Minute)}}
	r := find(t, NeedsAttention(in), "workerDraining:d")
	if r.Severity != SeverityInfo || !strings.Contains(r.Reading, "does not suspend or move") || strings.Contains(r.NextStep, "why") {
		t.Errorf("within the deadline: %+v", r)
	}
	in.WorkerPods = WorkerPods{"agents/d-pod": {Ready: true, HasIP: true, Deadline: now.Add(-time.Minute)}}
	if r := find(t, NeedsAttention(in), "workerDraining:d"); r.Severity != SeverityWarning {
		t.Errorf("past the deadline is stalled: %+v", r)
	}
	// Without the Pod's deadline, Substrate's 60-minute worker grace stands in.
	if r := find(t, NeedsAttention(base), "workerDraining:d"); r.Severity != SeverityInfo {
		t.Errorf("20m into a 60m grace: %+v", r)
	}
	base.Now = now.Add(45 * time.Minute)
	if r := find(t, NeedsAttention(base), "workerDraining:d"); r.Severity != SeverityWarning {
		t.Errorf("65m into a 60m grace: %+v", r)
	}
}

func TestEpochGapIsReconciliationNotARestart(t *testing.T) {
	w := worker("r", "p", WorkerActive, 8, 0)
	w.RegisteredEpoch, w.ObservedEpoch = 1, 0
	r := find(t, NeedsAttention(Inputs{Workers: []Worker{w}, WorkersRead: true, ActorsRead: true, Now: now}), "workerRestarting:r")
	if strings.Contains(r.Title+r.Reading, "restart") {
		t.Errorf("a first registration is not a restart: %+v", r)
	}
}

func TestServiceVerdictsAddRowsWhereDefinite(t *testing.T) {
	two := 2.0
	five := 5.0
	overdue := 3.0
	pools := []Pool{
		{Namespace: "ns", Name: "short", Desired: i64(5), Ready: 2},
		{Namespace: "ns", Name: "quiet", Desired: i64(1), Ready: 1},
		{Namespace: "ns", Name: "fine", Desired: i64(1), Ready: 1},
	}
	svc := &Service{Read: true, Window: "1h",
		Pools: map[string]PoolService{
			"ns/short": {Verdict: ServiceWorkerShortfall, SchedulableWorkers: &two, DesiredWorkers: &five},
			"ns/quiet": {Verdict: ServiceNoFreeActorSlots},
			"ns/fine":  {Verdict: ServiceUnknown, Reason: "no series"},
		},
		Nodes: map[string]NodeService{"node-a": {Verdict: ServiceUploadsOverdue, OverdueUploads: &overdue, Bands: map[string]float64{"over_15m": 1, "1m_to_5m": 2}}},
	}
	in := Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Service: svc, NodeReady: map[string]bool{"node-a": true}, Now: now}
	rows := NeedsAttention(in)
	if r := find(t, rows, "poolShort:ns/short"); !strings.Contains(r.Reading, "capacity alert condition") || strings.Count(strings.Join(rules(rows), ","), "ns/short") != 1 {
		t.Errorf("the verdict confirms the inventory row, one row per pool: %v %+v", rules(rows), r)
	}
	find(t, rows, "poolFull:ns/quiet")
	if slices.Contains(rules(rows), "poolFull:ns/fine") || slices.Contains(rules(rows), "poolShort:ns/fine") {
		t.Error("an unknown verdict adds nothing")
	}
	if r := find(t, rows, "nodeUploadsOverdue:node-a"); r.Count != 3 || !strings.Contains(r.Reading, "over 15m") {
		t.Errorf("node row = %+v", r)
	}
	for _, c := range Checks(in) {
		if c.Rule == "poolService" && !c.Ran {
			t.Errorf("Prometheus was read: %+v", c)
		}
	}
	in.Service = &Service{Why: "Prometheus is not connected"}
	for _, c := range Checks(in) {
		if c.Rule == RuleNodeUploadsOverdue && (c.Ran || c.Why != "Prometheus is not connected") {
			t.Errorf("unknown without Prometheus: %+v", c)
		}
	}
}

// A crashed or reverting actor has no worker assignment, so no next step may
// send the operator to its worker or its log lines in Radar.
func TestNextStepsOfferOnlyWhatCanBeRead(t *testing.T) {
	actors := []Actor{
		{Atespace: "a", Name: "c", State: StateCrashed, Crash: &Crash{Message: "killed for exceeding its memory limit"}, UpdateTime: ago(time.Hour)},
		{Atespace: "a", Name: "r", State: StateReverting, UpdateTime: ago(time.Hour)},
		{Atespace: "a", Name: "d", State: StateDeleting, UpdateTime: ago(time.Hour)},
		{Atespace: "a", Name: "s", State: StateResuming, UpdateTime: ago(time.Hour)},
	}
	actors = append(actors, Actor{Atespace: "a", Name: "c2", State: StateCrashed, Crash: &Crash{Message: "gVisor sandbox exited"}, UpdateTime: ago(2 * time.Hour)})
	rows := NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now})
	crashed := find(t, rows, RuleCrashedActors)
	if crashed.NextStep != crashedNextStep || crashed.Command != "kubectl ate revert actor <name> -a <atespace>" {
		t.Errorf("mixed causes get the general step: %+v", crashed)
	}
	for _, step := range []string{crashedNextStep, CrashedMemoryNextStep} {
		if strings.Contains(step, "last log lines") {
			t.Errorf("Radar cannot show a crashed actor's log lines: %s", step)
		}
	}
	for _, id := range []string{"stuckTransition:reverting", "stuckTransition:deleting"} {
		if r := find(t, rows, id); !strings.Contains(r.NextStep, "has no worker") || r.Command != apiServerLogs {
			t.Errorf("%s holds no worker: %+v", id, r)
		}
	}
	if r := find(t, rows, "stuckTransition:resuming"); !strings.Contains(r.NextStep, "Resume each again") || r.Command != resumeCommand {
		t.Errorf("an unfinished resume continues only on the next resume call: %+v", r)
	}
	// A group row speaks of the actors; one actor's page, of that actor.
	if page := ActorAttention(rows, actors[3], now); len(page) != 1 || !strings.HasPrefix(page[0].NextStep, "Resume it again") || page[0].Command != "kubectl ate resume actor s -a a" {
		t.Errorf("a stuck actor's own page: %+v", page)
	}
	for _, step := range []string{crashedNextStep, CrashedMemoryNextStep, stuckNextStep[StateResuming]} {
		if strings.Contains(step, "this actor") || strings.Contains(step, " it to ") {
			t.Errorf("a group step is plural: %s", step)
		}
	}
	for _, step := range []string{crashedActorNextStep, CrashedMemoryActorStep(actors[0]), stuckActorNextStep[StateResuming], stuckActorNextStep[StateSuspending]} {
		if strings.Contains(step, "each") || strings.Contains(step, "the actors") || strings.Contains(step, "them") {
			t.Errorf("an actor page's step is singular: %s", step)
		}
	}
	// On the actor's own page, the step follows its own crash and names it.
	onPage := ActorAttention(rows, actors[0], now)
	if other := ActorAttention(rows, actors[4], now); other[0].NextStep != crashedActorNextStep || other[0].Command != "kubectl ate revert actor c2 -a a" {
		t.Errorf("another cause gets the general step: %+v", other[0])
	}
	if onPage[0].NextStep != CrashedMemoryActorStep(actors[0]) || onPage[0].Command != "kubectl ate update actor c -a a --template <new-template>" {
		t.Errorf("a memory-limit kill names its remedy: %+v", onPage[0])
	}
	if r := find(t, NeedsAttention(Inputs{Actors: actors[:1], ActorsRead: true, Now: now}), RuleCrashedActors); r.NextStep != CrashedMemoryNextStep || r.Command != retemplateCommand || r.Groups[0].Cause != CauseMemoryLimit {
		t.Errorf("crashes that are all memory-limit kills name the remedy: %+v", r)
	}
	if crashed.Groups[0].Cause != "" {
		t.Errorf("a template group with another crash names no cause: %+v", crashed.Groups)
	}
	for _, r := range rows {
		if n := len(strings.Fields(r.NextStep)); n > 25 || strings.Contains(r.NextStep, "`") {
			t.Errorf("%s: a next step is one short action, commands apart (%d words): %s", r.ID, n, r.NextStep)
		}
	}
}

// Every rule's next step stays one short action, with any command in its own
// field.
// What a revert keeps decides how an actor moved to a new template resumes:
// Substrate restores only the volumes of a snapshot stamped with another
// template (templateReplaced), and treats an unstamped one as matching.
func TestMemoryStepFollowsTheRecoverySnapshot(t *testing.T) {
	snap := func(f, uid string, d string) Snapshot {
		return Snapshot{Generation: 3, Fidelity: f, Durable: d, ActorTemplateUID: uid}
	}
	tests := []struct {
		snaps []Snapshot
		says  string
	}{
		{nil, "it has no stored snapshot, so it boots fresh."},
		{[]Snapshot{snap(FidelityMemory, "old", DurablePending)}, "it has no stored snapshot, so it boots fresh."},
		{[]Snapshot{snap(FidelityMemory, "old", DurableCompleted)}, "there it resumes from volumes only; its memory and files belong to the old template."},
		{[]Snapshot{snap(FidelityRootFS, "old", DurableCompleted)}, "there it resumes from volumes only; its saved files belong to the old template."},
		{[]Snapshot{snap(FidelityRootFS, "", DurableCompleted)}, "there it resumes from its saved files."},
		{[]Snapshot{snap(FidelityVolumes, "old", DurableCompleted)}, "there it resumes from its volumes."},
	}
	for _, tc := range tests {
		a := Actor{State: StateCrashed, Crash: &Crash{Message: memoryLimitMessage}, Snapshots: tc.snaps}
		if got := CrashedMemoryActorStep(a); !strings.HasSuffix(got, tc.says) || !strings.HasPrefix(got, "Move this actor to a higher-memory template, then revert it; ") {
			t.Errorf("%+v: %s", tc.snaps, got)
		}
	}
}

func TestNextStepsAreShort(t *testing.T) {
	steps := append(slices.Collect(maps.Values(stuckNextStep)), slices.Collect(maps.Values(stuckActorNextStep))...)
	steps = append(steps, crashedNextStep, CrashedMemoryNextStep, crashedActorNextStep)
	stamped := func(f string) Actor {
		return Actor{State: StateCrashed, Snapshots: []Snapshot{{Generation: 1, Fidelity: f, Durable: DurableCompleted, ActorTemplateUID: "old"}}}
	}
	for _, a := range []Actor{{State: StateCrashed}, stamped(FidelityMemory), stamped(FidelityRootFS), stamped(FidelityVolumes)} {
		steps = append(steps, CrashedMemoryActorStep(a))
	}
	for _, step := range steps {
		if n := len(strings.Fields(step)); n > 25 {
			t.Errorf("%d words: %s", n, step)
		}
	}
	for state, step := range workerUnavailableStep {
		if n := len(strings.Fields(step.text)); n > 25 {
			t.Errorf("%s: %d words: %s", state, n, step.text)
		}
	}
}
