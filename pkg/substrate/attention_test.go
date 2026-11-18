package substrate

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func rules(rows []Attention) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

func find(t *testing.T, rows []Attention, id string) Attention {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %s in %v", id, rules(rows))
	return Attention{}
}

func TestNeedsAttentionCrashedActors(t *testing.T) {
	tpl := func(name string) Ref { return Ref{"research", name} }
	actors := []Actor{
		{Atespace: "research", Name: "a1", Template: tpl("lang"), State: StateCrashed, Crash: &Crash{Message: "OOMKilled", Time: ago(4 * time.Minute)}},
		{Atespace: "research", Name: "a2", Template: tpl("lang"), State: StateCrashed, Crash: &Crash{Message: "OOMKilled", Time: ago(9 * time.Minute)}},
		{Atespace: "research", Name: "a3", Template: tpl("lang"), State: StateCrashed, Crash: &Crash{Message: "OOMKilled", Time: ago(20 * time.Minute)}},
		{Atespace: "code", Name: "b1", Template: Ref{"code", "review"}, State: StateCrashed, Crash: &Crash{Message: "exit 1"}, UpdateTime: ago(time.Hour)},
		{Atespace: "code", Name: "ok", Template: Ref{"code", "review"}, State: StateRunning},
	}
	row := find(t, NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), RuleCrashedActors)
	if row.Severity != SeverityCritical || row.Count != 4 {
		t.Fatalf("row = %+v", row)
	}
	if row.Title != "4 actors are crashed" {
		t.Errorf("title = %q", row.Title)
	}
	if row.Groups[0].Count != 3 || row.Groups[0].Subject.Name != "lang" || row.Messages[0].Text != "OOMKilled" || row.Messages[0].Count != 3 {
		t.Errorf("groups %+v messages %+v", row.Groups, row.Messages)
	}
	if row.Sample[0].Name != "a1" || !row.Since.Equal(ago(4*time.Minute)) {
		t.Errorf("latest crash must lead: sample %v since %v", row.Sample, row.Since)
	}
	if row.Subject.Kind != "Actors" || row.Subject.Filter["state"] != StateCrashed {
		t.Errorf("subject = %+v", row.Subject)
	}
}

func TestNeedsAttentionCrashedTieIsStable(t *testing.T) {
	at := ago(3 * time.Minute)
	a := Actor{Atespace: "research", Name: "b", State: StateCrashed, Crash: &Crash{Message: "OOMKilled", Time: at}}
	b := Actor{Atespace: "research", Name: "a", State: StateCrashed, Crash: &Crash{Message: "OOMKilled", Time: at}}
	for _, order := range [][]Actor{{a, b}, {b, a}} {
		row := find(t, NeedsAttention(Inputs{Actors: order, ActorsRead: true, Now: now}), RuleCrashedActors)
		if row.Sample[0].Name != "a" {
			t.Fatalf("same crash time must order by atespace/name whatever the read order; sample %v", row.Sample)
		}
	}
}

func TestNeedsAttentionStuckTransitions(t *testing.T) {
	actors := []Actor{
		{Atespace: "a", Name: "long", State: StateResuming, UpdateTime: ago(20 * time.Minute)},
		{Atespace: "a", Name: "recent", State: StateResuming, UpdateTime: ago(2 * time.Minute)},
		{Atespace: "a", Name: "rev", State: StateReverting, UpdateTime: ago(6 * time.Minute)},
		{Atespace: "a", Name: "untimed", State: StateSuspending},
		{Atespace: "a", Name: "fine", State: StateRunning, UpdateTime: ago(time.Hour)},
	}
	rows := NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now})
	if got := rules(rows); !slices.Equal(got, []string{"stuckTransition:reverting", "stuckTransition:resuming"}) {
		t.Fatalf("rows = %v: only transitions held past five minutes count", got)
	}
	r := find(t, rows, "stuckTransition:resuming")
	if r.Count != 1 || r.Sample[0].Name != "long" || r.Severity != SeverityWarning {
		t.Errorf("row = %+v", r)
	}
}

func TestNeedsAttentionGoldenFailed(t *testing.T) {
	templates := []Template{
		{Atespace: "dev", Name: "browser", GoldenError: "WarmupFailed: container browser exited 137", UpdateTime: ago(25 * time.Minute)},
		{Atespace: "dev", Name: "fine", GoldenTag: &Ref{"dev", "g"}},
	}
	rows := NeedsAttention(Inputs{Templates: templates, TemplatesRead: true, Now: now})
	r := find(t, rows, "goldenSnapshotFailed:dev/browser")
	if r.Severity != SeverityWarning || len(rows) != 1 {
		t.Errorf("rows = %v", rules(rows))
	}
	if want := "Substrate's message: container browser exited 137. Substrate does not retry a failed build."; r.Reading != want {
		t.Errorf("reading = %q", r.Reading)
	}
	if !strings.Contains(r.NextStep, "recreate the template") || strings.Contains(r.NextStep, "new version") {
		t.Errorf("next step must say to recreate: Substrate has no template update: %q", r.NextStep)
	}
}

func TestNeedsAttentionNoMatchingPool(t *testing.T) {
	templates := []Template{
		{Atespace: "a", Name: "gpu", WorkerSelector: map[string]string{"class": "gpu"}},
		{Atespace: "a", Name: "gpu-idle", WorkerSelector: map[string]string{"class": "gpu"}},
		{Atespace: "a", Name: "ok", WorkerSelector: map[string]string{"class": "gvisor"}},
	}
	pools := []Pool{{Namespace: "ns", Name: "p", Labels: map[string]string{"class": "gvisor"}}}
	actors := []Actor{{Atespace: "a", Name: "waiting", Template: Ref{"a", "gpu"}, State: StateSuspended}}
	rows := NeedsAttention(Inputs{Templates: templates, TemplatesRead: true, Pools: pools, PoolsUsable: true, PoolsComplete: true, Actors: actors, ActorsRead: true, Now: now})
	if r := find(t, rows, "noMatchingPool:a/gpu"); strings.Contains(r.NextStep, "fix the selector") || !strings.Contains(r.NextStep, "Label a pool to match") || !strings.Contains(r.NextStep, "recreate the template") {
		t.Errorf("templates cannot be edited: label a pool, or recreate the template: %q", r.NextStep)
	}
	if r := find(t, rows, "noMatchingPool:a/gpu"); r.Severity != SeverityCritical {
		t.Errorf("a template with actors not running is critical: %+v", r)
	}
	if r := find(t, rows, "noMatchingPool:a/gpu-idle"); r.Severity != SeverityWarning {
		t.Errorf("a template with no waiting actors is a warning: %+v", r)
	}
	if slices.Contains(rules(rows), "noMatchingPool:a/ok") {
		t.Error("a matching template is not flagged")
	}
	if got := NeedsAttention(Inputs{Templates: templates, TemplatesRead: true, Now: now}); len(got) != 0 {
		t.Errorf("with no pool list read, the rule is skipped, not fired: %v", rules(got))
	}
	// A partial or still-syncing pool list cannot prove no pool matches.
	partial := NeedsAttention(Inputs{Templates: templates, TemplatesRead: true, Pools: pools, PoolsUsable: true, Workers: []Worker{{Pool: "p", Namespace: "ns"}}, WorkersRead: true, Now: now})
	for _, r := range partial {
		if r.Rule == RuleNoMatchingPool {
			t.Errorf("incomplete pool evidence fired %s", r.ID)
		}
	}
}

func TestNeedsAttentionWorkers(t *testing.T) {
	unavailable := worker("u", "p", WorkerUnavailable, 8, 3)
	unavailable.UpdateTime = ago(12 * time.Minute)
	recentUnavailable := worker("ru", "p", WorkerUnavailable, 8, 0)
	recentUnavailable.UpdateTime = ago(2 * time.Minute)
	draining := worker("d", "p", WorkerDraining, 8, 1)
	draining.UpdateTime = ago(20 * time.Minute)
	drainedEmpty := worker("de", "p", WorkerDraining, 8, 0)
	drainedEmpty.UpdateTime = ago(20 * time.Minute)
	restarting := worker("r", "p", WorkerActive, 8, 2)
	restarting.RegisteredEpoch, restarting.ObservedEpoch = 5, 4
	actors := []Actor{
		{Atespace: "a", Name: "on-u", State: StateCrashed, Assignment: &Assignment{Worker: "u"}},
		{Atespace: "a", Name: "on-d", State: StateSuspending, Assignment: &Assignment{Worker: "d"}},
		{Atespace: "a", Name: "old-epoch", State: StateRunning, Assignment: &Assignment{Worker: "r", Epoch: 4}},
		{Atespace: "a", Name: "new-epoch", State: StateRunning, Assignment: &Assignment{Worker: "r", Epoch: 5}},
	}
	rows := NeedsAttention(Inputs{
		Workers: []Worker{unavailable, recentUnavailable, draining, drainedEmpty, restarting}, WorkersRead: true,
		Actors: actors, ActorsRead: true, Now: now,
	})
	u := find(t, rows, "workerUnavailable:u")
	if u.Severity != SeverityCritical || u.Count != 1 || u.Title != "1 actor is still assigned to u-pod, unavailable for at least 12m" || !strings.HasPrefix(u.Reading, "1 actor is still assigned to it. ") {
		t.Errorf("unavailable with an actor assigned = %+v", u)
	}
	if slices.Contains(rules(rows), "workerUnavailable:ru") {
		t.Error("unavailable under five minutes is not flagged")
	}
	find(t, rows, "workerDraining:d")
	if slices.Contains(rules(rows), "workerDraining:de") {
		t.Error("a draining worker with no actors left is not flagged")
	}
	// Actors that ran under the earlier process lost their sandboxes: the
	// row leads with that, is critical and links to exactly those actors.
	r := find(t, rows, "workerRestarting:r")
	if r.Count != 1 || r.Sample[0].Name != "old-epoch" || r.Severity != SeverityCritical || r.Title != "1 actor lost its sandbox when r-pod restarted" {
		t.Errorf("restarting row must lead with the actors that lost their sandboxes: %+v", r)
	}
	if r.Subject.Kind != "Actors" || r.Subject.Filter["worker"] != "r-pod" || r.Subject.Filter["impact"] != ImpactRestarted || r.Command != revertCommand || strings.Contains(r.NextStep, "Wait") {
		t.Errorf("restarting row links the lost actors and says what to do: %+v", r)
	}
	if !rankedBefore(rows, "workerRestarting:r", RuleUploadOverdue) {
		t.Errorf("lost sandboxes rank above pending uploads: %v", rules(rows))
	}
	if len(u.Related) != 4 || u.Related[0].Kind != "Actors" || u.Related[0].Filter["impact"] != ImpactUnavailable || u.Related[1].Kind != "Pod" {
		t.Errorf("related = %+v", u.Related)
	}
	// No actor from before the restart: a registration lag, a warning.
	lag := find(t, NeedsAttention(Inputs{Workers: []Worker{restarting}, WorkersRead: true, Actors: actors[3:], ActorsRead: true, Now: now}), "workerRestarting:r")
	if lag.Severity != SeverityWarning || lag.Subject.Kind != "Worker" {
		t.Errorf("a registration lag = %+v", lag)
	}
}

func TestNeedsAttentionPools(t *testing.T) {
	pools := []Pool{
		{Namespace: "ns", Name: "short", Desired: i64(2), Ready: 2},
		{Namespace: "ns", Name: "empty", Desired: i64(2), Ready: 0},
		{Namespace: "ns", Name: "full", Desired: i64(1), Ready: 1},
		{Namespace: "ns", Name: "free", Desired: i64(1), Ready: 1},
	}
	workers := []Worker{
		worker("s1", "short", WorkerActive, 8, 1), worker("s2", "short", WorkerUnavailable, 8, 0),
		worker("f1", "full", WorkerActive, 2, 2),
		worker("e1", "free", WorkerActive, 8, 1),
	}
	for i := range workers {
		workers[i].Namespace = "ns"
	}
	rows := NeedsAttention(Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Workers: workers, WorkersRead: true, WorkersComplete: true, Now: now})
	short := find(t, rows, "poolShort:ns/short")
	if short.Severity != SeverityWarning || short.Reading != "1 of 2 desired workers are active. New and resuming actors may wait or fail to start." {
		t.Errorf("short = %+v", short)
	}
	if r := find(t, rows, "poolShort:ns/empty"); r.Severity != SeverityCritical {
		t.Errorf("a pool with no worker taking actors is critical: %+v", r)
	}
	find(t, rows, "poolFull:ns/full")
	if slices.Contains(rules(rows), "poolFull:ns/free") || slices.Contains(rules(rows), "poolShort:ns/free") {
		t.Error("a pool with room is not flagged")
	}
	// Without Substrate's workers, only Kubernetes' ready count can say short.
	k8sOnly := NeedsAttention(Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Now: now})
	if got := rules(k8sOnly); !slices.Equal(got, []string{"poolShort:ns/empty"}) {
		t.Errorf("Kubernetes-only rows = %v", got)
	}
	// A partial worker list (a capped read) proves a free slot it saw, never
	// a shortfall or a full pool: a worker it did not see may have room.
	// Kubernetes' own ready count still says short.
	partial := NeedsAttention(Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Workers: workers, WorkersRead: true, Now: now})
	if got := rules(partial); !slices.Equal(got, []string{"poolShort:ns/empty"}) {
		t.Errorf("partial-worker rows = %v", got)
	}
	if r := find(t, partial, "poolShort:ns/empty"); !strings.HasPrefix(r.Reading, "0 of 2 desired workers are ready.") {
		t.Errorf("a partial worker list leaves the count to Kubernetes: %q", r.Reading)
	}
	for _, r := range JoinPools(pools, workers, true, false, nil, false) {
		want := map[string]string{"short": CapacityFree, "empty": CapacityShort, "full": "", "free": CapacityFree}[r.Name]
		if r.Capacity != want || r.Placement.Complete {
			t.Errorf("%s: capacity %q complete %v, want %q", r.Name, r.Capacity, r.Placement.Complete, want)
		}
	}
}

func TestNeedsAttentionUploadsOverdue(t *testing.T) {
	pending := func(node string) []Snapshot {
		return []Snapshot{{Generation: 3, Durable: DurablePending, Locality: node}}
	}
	actors := []Actor{
		{Atespace: "a", Name: "old", State: StateSuspended, Snapshots: pending("node-a"), UpdateTime: ago(30 * time.Minute)},
		{Atespace: "a", Name: "older", State: StateSuspended, Snapshots: pending("node-a"), UpdateTime: ago(45 * time.Minute)},
		{Atespace: "a", Name: "on-b", State: StateSuspended, Snapshots: pending("node-b"), UpdateTime: ago(20 * time.Minute)},
		{Atespace: "a", Name: "recent", State: StateSuspended, Snapshots: pending("node-a"), UpdateTime: ago(3 * time.Minute)},
		{Atespace: "a", Name: "stored", State: StateSuspended, Snapshots: []Snapshot{{Generation: 1, Durable: DurableCompleted}}, UpdateTime: ago(time.Hour)},
		{Atespace: "a", Name: "older-gen-pending", State: StateSuspended, UpdateTime: ago(time.Hour),
			Snapshots: []Snapshot{{Generation: 1, Durable: DurablePending}, {Generation: 2, Durable: DurableCompleted}}},
		{Atespace: "a", Name: "running", State: StateRunning, Snapshots: pending("node-a"), UpdateTime: ago(time.Hour)},
	}
	ready := map[string]bool{"node-a": true, "node-b": true}
	rows := NeedsAttention(Inputs{Actors: actors, ActorsRead: true, NodeReady: ready, Now: now})
	if len(rows) != 1 {
		t.Fatalf("one row for every node, not one per node: %v", rules(rows))
	}
	r := rows[0]
	if r.Severity != SeverityWarning || r.Count != 3 || r.Title != "3 suspended actors' newest snapshots are pending upload" {
		t.Errorf("row = %+v", r)
	}
	if len(r.Nodes) != 2 || r.Nodes[0] != (UploadNode{Node: "node-a", State: NodeReady, Count: 2, Oldest: ago(45 * time.Minute)}) || r.Nodes[1].Node != "node-b" {
		t.Errorf("nodes = %+v", r.Nodes)
	}
	one := NeedsAttention(Inputs{Actors: actors[:1], ActorsRead: true, Now: now})[0]
	if one.Title != "1 suspended actor's newest snapshot is pending upload" || one.Nodes[0].State != NodeUnknown {
		t.Errorf("singular row = %+v", one)
	}

	for _, tc := range []struct {
		name  string
		ready map[string]bool
		now   time.Time
		says  string
	}{
		{"node NotReady", map[string]bool{"node-a": false, "node-b": true}, now, "Node node-a is NotReady"},
		{"node gone", map[string]bool{"node-b": true}, now, "Node node-a is gone"},
		{"over an hour", ready, now.Add(20 * time.Minute), "The oldest last update is over an hour ago."},
	} {
		r := NeedsAttention(Inputs{Actors: actors, ActorsRead: true, NodeReady: tc.ready, Now: tc.now})[0]
		if r.Severity != SeverityCritical || !strings.Contains(r.Reading, tc.says) {
			t.Errorf("%s: %s %q", tc.name, r.Severity, r.Reading)
		}
	}
}

func TestNeedsAttentionRankWithinSeverity(t *testing.T) {
	pending := []Snapshot{{Generation: 3, Durable: DurablePending, Locality: "gone-node"}}
	actors := []Actor{
		{Atespace: "a", Name: "crashed-long-ago", State: StateCrashed, UpdateTime: ago(3 * time.Hour)},
		{Atespace: "a", Name: "upload", State: StateSuspended, Snapshots: pending, UpdateTime: ago(20 * time.Minute)},
		{Atespace: "a", Name: "stuck", State: StateResuming, UpdateTime: ago(10 * time.Minute)},
	}
	templates := []Template{
		{Atespace: "a", Name: "nopool", WorkerSelector: map[string]string{"class": "gpu"}, UpdateTime: ago(time.Minute)},
		{Atespace: "a", Name: "golden", GoldenError: "x", WorkerSelector: map[string]string{"class": "gvisor"}, UpdateTime: ago(time.Minute)},
	}
	unavailable := worker("u", "p", WorkerUnavailable, 8, 0)
	unavailable.UpdateTime = ago(time.Hour)
	pools := []Pool{{Namespace: "agents", Name: "p", Labels: map[string]string{"class": "gvisor"}, Desired: i64(2), Ready: 2}}
	rows := NeedsAttention(Inputs{
		Actors: actors, ActorsRead: true, Templates: templates, TemplatesRead: true,
		Workers: []Worker{unavailable}, WorkersRead: true, WorkersComplete: true, Pools: pools, PoolsUsable: true, PoolsComplete: true,
		NodeReady: map[string]bool{}, Now: now,
	})
	want := []string{
		RuleCrashedActors, "poolShort:agents/p", RuleUploadOverdue, // critical
		"noMatchingPool:a/nopool", "workerUnavailable:u", "stuckTransition:resuming", "goldenSnapshotFailed:a/golden", // warning
	}
	if got := rules(rows); !slices.Equal(got, want) {
		t.Errorf("order =\n%v\nwant\n%v", got, want)
	}
}

func TestNeedsAttentionOrderAndUnknownInputs(t *testing.T) {
	if got := NeedsAttention(Inputs{Now: now}); len(got) != 0 {
		t.Errorf("nothing read means no rows, not all-clear rows: %v", rules(got))
	}
	actors := []Actor{{Atespace: "a", Name: "c", State: StateCrashed, UpdateTime: ago(time.Hour)}}
	templates := []Template{{Atespace: "a", Name: "t", GoldenError: "x", UpdateTime: ago(time.Minute)}}
	rows := NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Templates: templates, TemplatesRead: true, Now: now})
	if got := rules(rows); !slices.Equal(got, []string{RuleCrashedActors, "goldenSnapshotFailed:a/t"}) {
		t.Errorf("critical rows lead, whatever their age: %v", got)
	}
}

func TestNewPoolShortIsNotCritical(t *testing.T) {
	desired := int64(1)
	for _, tc := range []struct {
		name string
		pool Pool
		want string
	}{
		{"pending pool", Pool{Namespace: "agents", Name: "p", Desired: &desired, Health: "degraded", HealthReason: "Pending"}, SeverityWarning},
		{"scaling pool", Pool{Namespace: "agents", Name: "p", Desired: &desired, HealthReason: "Scaling", CreateTime: ago(time.Hour)}, SeverityWarning},
		{"young pool", Pool{Namespace: "agents", Name: "p", Desired: &desired, HealthReason: "Provisioning stalled", CreateTime: ago(time.Minute)}, SeverityWarning},
		{"settled pool with none", Pool{Namespace: "agents", Name: "p", Desired: &desired, HealthReason: "Provisioning stalled", CreateTime: ago(time.Hour)}, SeverityCritical},
	} {
		r := find(t, NeedsAttention(Inputs{Pools: []Pool{tc.pool}, PoolsUsable: true, PoolsComplete: true, Now: now}), "poolShort:agents/p")
		if r.Severity != tc.want || r.Count != 1 {
			t.Errorf("%s: severity %s count %d, want %s with the shortfall kept", tc.name, r.Severity, r.Count, tc.want)
		}
	}
}

func TestActorAttentionUsesRulePredicates(t *testing.T) {
	var actors []Actor
	for i := range 25 {
		actors = append(actors, Actor{Atespace: "a", Name: fmt.Sprintf("c%02d", i), State: StateCrashed, UpdateTime: ago(time.Duration(i) * time.Minute)})
	}
	restarting := worker("w", "p", WorkerActive, 8, 2)
	restarting.RegisteredEpoch, restarting.ObservedEpoch = 5, 4
	unavailable := worker("u", "p", WorkerUnavailable, 8, 1)
	unavailable.UpdateTime = ago(time.Hour)
	in := Inputs{Actors: actors, ActorsRead: true, Workers: []Worker{restarting, unavailable}, WorkersRead: true, Now: now}
	rows := NeedsAttention(in)
	oldest := actors[24]
	if slices.Contains(find(t, rows, RuleCrashedActors).Sample, oldest.Ref()) {
		t.Fatal("fixture: the oldest crash should fall outside the sample")
	}
	if got := rules(ActorAttention(rows, oldest, now)); !slices.Equal(got, []string{RuleCrashedActors}) {
		t.Errorf("a crashed actor beyond the sample = %v", got)
	}
	fresh := Actor{Atespace: "a", Name: "c24", State: StateRunning}
	if got := ActorAttention(rows, fresh, now); len(got) != 0 {
		t.Errorf("the fresh read wins: an actor running now is not crashed: %v", rules(got))
	}
	old := Actor{Atespace: "a", Name: "x", State: StateRunning, Assignment: &Assignment{Worker: "w", Epoch: 4}}
	if got := rules(ActorAttention(rows, old, now)); !slices.Equal(got, []string{"workerRestarting:w"}) {
		t.Errorf("an actor from before the restart = %v", got)
	}
	current := Actor{Atespace: "a", Name: "y", State: StateRunning, Assignment: &Assignment{Worker: "w", Epoch: 5}}
	if got := ActorAttention(rows, current, now); len(got) != 0 {
		t.Errorf("an actor of the current epoch is not covered: %v", rules(got))
	}
	onU := Actor{Atespace: "a", Name: "z", State: StateCrashed, Assignment: &Assignment{Worker: "u"}}
	if got := rules(ActorAttention(rows, onU, now)); !slices.Equal(got, []string{RuleCrashedActors, "workerUnavailable:u"}) {
		t.Errorf("an actor on an unavailable worker = %v", got)
	}
}

func rankedBefore(rows []Attention, first, second string) bool {
	i := slices.IndexFunc(rows, func(r Attention) bool { return r.ID == first })
	j := slices.IndexFunc(rows, func(r Attention) bool { return r.ID == second })
	return i >= 0 && (j < 0 || i < j)
}

// The pool's readiness is counted from the Pods Radar holds; the
// WorkerPool's status, which can lag, is cited only where it disagrees.
func TestPoolShortCountsPods(t *testing.T) {
	pools := []Pool{{Namespace: "ns", Name: "p", Desired: i64(5), Ready: 3, Pods: &PoolPods{Created: 4, Ready: 2, Terminating: 1}}}
	r := find(t, NeedsAttention(Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Now: now}), "poolShort:ns/p")
	want := "2 of 5 desired workers are ready. Kubernetes has created 4 of 5 worker Pods. Radar holds 2 ready worker Pods; the WorkerPool's status says 3."
	if !strings.HasPrefix(r.Reading, want) {
		t.Errorf("reading = %q", r.Reading)
	}
	pools[0].Pods = &PoolPods{Created: 5, Ready: 3}
	r = find(t, NeedsAttention(Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Now: now}), "poolShort:ns/p")
	if strings.Contains(r.Reading, "status says") || strings.Contains(r.Reading, "has created") {
		t.Errorf("agreeing counts are said once: %q", r.Reading)
	}
	pools[0].Pods = &PoolPods{Created: 5, Ready: 5}
	if rows := NeedsAttention(Inputs{Pools: pools, PoolsUsable: true, PoolsComplete: true, Now: now}); slices.Contains(rules(rows), "poolShort:ns/p") {
		t.Error("five ready Pods are not short, whatever a stale status says")
	}
}

// A memory-limit cause gets its remedy wherever it shows: a golden snapshot
// that failed on it, and a crash group whose crashes all were.
func TestMemoryCauseRemedies(t *testing.T) {
	templates := []Template{{Atespace: "a", Name: "mcp", GoldenError: "GoldenActorCrashed: golden actor crashed before its snapshot was taken: killed for exceeding its memory limit"}}
	g := find(t, NeedsAttention(Inputs{Templates: templates, TemplatesRead: true, Now: now}), "goldenSnapshotFailed:a/mcp")
	if !strings.HasPrefix(g.NextStep, "Recreate the template with a higher memory limit") || strings.Contains(g.Reading, "GoldenActorCrashed") ||
		!strings.HasPrefix(g.Reading, "Its golden actor was killed for exceeding its memory limit") || strings.Contains(g.NextStep, "killed") {
		t.Errorf("golden = %+v", g)
	}
	actors := []Actor{
		{Atespace: "a", Name: "m", Template: Ref{"a", "big"}, State: StateCrashed, Crash: &Crash{Message: memoryLimitMessage}},
		{Atespace: "a", Name: "o", Template: Ref{"a", "other"}, State: StateCrashed, Crash: &Crash{Message: "gVisor sandbox exited"}},
	}
	c := find(t, NeedsAttention(Inputs{Actors: actors, ActorsRead: true, Now: now}), RuleCrashedActors)
	for _, grp := range c.Groups {
		memory := grp.Subject.Name == "big"
		if (grp.Cause == CauseMemoryLimit) != memory || (grp.NextStep == CrashedMemoryNextStep) != memory || (grp.Command == retemplateCommand) != memory {
			t.Errorf("group %s = %+v", grp.Subject.Name, grp)
		}
	}
}
