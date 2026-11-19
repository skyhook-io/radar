package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

func startTest(t *testing.T) (*State, string) {
	t.Helper()
	st, err := LoadOrCreateState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv, lis, err := Start(st, "127.0.0.1:0", nil, Faults{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return st, lis.Addr().String()
}

func client(t *testing.T, st *State, addr, tokenFile string) (ateapi.ControlClient, context.Context) {
	t.Helper()
	cf := clientFlags{addr: addr, ca: st.CAFile, serverName: ServerName, tokenFile: tokenFile}
	c, closeFn, ctx, err := cf.dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	t.Cleanup(cancel)
	return c, ctx
}

func TestGlobalTokenPagesEveryActor(t *testing.T) {
	st, addr := startTest(t)
	c, ctx := client(t, st, addr, st.TokenFile)

	actors, pages, err := ListAllActors(ctx, c, "", 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(actors) < 1200 || pages != (len(actors)+249)/250 {
		t.Fatalf("got %d actors in %d pages", len(actors), pages)
	}
	seen := map[string]bool{}
	for _, a := range actors {
		k := a.Metadata.Atespace + "/" + a.Metadata.Name
		if seen[k] {
			t.Fatalf("duplicate %s across pages", k)
		}
		seen[k] = true
	}

	big, _, err := ListAllActors(ctx, c, "", 5000)
	if err != nil || len(big) != len(actors) {
		t.Fatalf("page_size above 1000 should be coerced, got %d, %v", len(big), err)
	}

	resp, err := c.ListActors(ctx, &ateapi.ListActorsRequest{Options: &ateapi.ListOptions{PageSize: 10}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListActors(ctx, &ateapi.ListActorsRequest{Atespace: "research", Options: &ateapi.ListOptions{PageToken: resp.NextPageToken}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("token replayed on another list: want InvalidArgument, got %v", err)
	}

	if _, err := c.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{Actor: &ateapi.ObjectRef{Atespace: "research", Name: "lit-review-0193"}, Name: "default"}); status.Code(err) != codes.NotFound {
		t.Fatalf("research actor has no egress policy: want NotFound, got %v", err)
	}
	if _, err := c.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{Actor: &ateapi.ObjectRef{Atespace: "support-bots", Name: "ticket-41302"}, Name: "default"}); err != nil {
		t.Fatal(err)
	}
}

// The atespace-only persona is what model.fga lets a person hold short of
// global viewer: reads in its atespaces, nothing global.
func TestAtespaceViewerReadsOnlyItsAtespaces(t *testing.T) {
	st, addr := startTest(t)
	c, ctx := client(t, st, addr, st.LimitedFile)

	denied := func(what string, err error) {
		t.Helper()
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: want PermissionDenied, got %v", what, err)
		}
	}
	_, err := c.ListActors(ctx, &ateapi.ListActorsRequest{})
	denied("cross-atespace ListActors", err)
	_, err = c.ListActorTemplates(ctx, &ateapi.ListActorTemplatesRequest{})
	denied("cross-atespace ListActorTemplates", err)
	_, err = c.ListAtespaces(ctx, &ateapi.ListAtespacesRequest{})
	denied("ListAtespaces", err)
	_, err = c.ListWorkers(ctx, &ateapi.ListWorkersRequest{})
	denied("ListWorkers", err)
	_, err = c.ListWorkerActorAssignments(ctx, &ateapi.ListWorkerActorAssignmentsRequest{Worker: &ateapi.ObjectRef{Name: "any"}})
	denied("ListWorkerActorAssignments", err)
	_, err = c.GetActor(ctx, &ateapi.GetActorRequest{Actor: &ateapi.ObjectRef{Atespace: "research", Name: "lit-review-0193"}})
	denied("GetActor outside its atespaces", err)
	_, err = c.GetAtespace(ctx, &ateapi.GetAtespaceRequest{Atespace: &ateapi.ObjectRef{Name: "research"}})
	denied("GetAtespace outside its atespaces", err)

	total := 0
	for _, a := range ScopedAtespaces {
		got, _, err := ListAllActors(ctx, c, a, 100)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		total += len(got)
		if _, err := c.ListActorTemplates(ctx, &ateapi.ListActorTemplatesRequest{Atespace: a}); err != nil {
			t.Errorf("templates in %s: %v", a, err)
		}
		if _, err := c.GetAtespace(ctx, &ateapi.GetAtespaceRequest{Atespace: &ateapi.ObjectRef{Name: a}}); err != nil {
			t.Errorf("GetAtespace %s: %v", a, err)
		}
	}
	if total < 1000 {
		t.Fatalf("its atespaces hold %d actors", total)
	}
	if _, err := c.GetActorEgressPolicy(ctx, &ateapi.GetActorEgressPolicyRequest{Actor: &ateapi.ObjectRef{Atespace: "support-bots", Name: "ticket-41302"}, Name: "default"}); err != nil {
		t.Errorf("egress policy in its atespace: %v", err)
	}
}

func TestBadTokenIsUnauthenticated(t *testing.T) {
	st, addr := startTest(t)
	c, ctx := client(t, st, addr, st.TokenFile)
	bad := t.TempDir() + "/bad"
	if err := os.WriteFile(bad, []byte("not-a-known-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, cctx := client(t, st, addr, bad)
	if _, err := cb.ListWorkers(cctx, &ateapi.ListWorkersRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
	if _, err := c.CreateActor(ctx, &ateapi.CreateActorRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("writes are not served: want Unimplemented, got %v", err)
	}
}

func TestDatasetIsConsistent(t *testing.T) {
	d := BuildDataset(time.Now(), nil)
	workers := map[string]*ateapi.Worker{}
	for _, w := range d.Workers {
		workers[w.Metadata.Name] = w
		s := w.Status
		if s.Allocated.CpuMillicores > s.Capacity.CpuMillicores || s.Allocated.MemoryBytes > s.Capacity.MemoryBytes || s.Allocated.Actors > s.Capacity.Actors {
			t.Errorf("worker %s allocated %v over capacity %v", w.WorkerPod, s.Allocated, s.Capacity)
		}
		if int(s.Allocated.Actors) != len(d.Assignments[w.Metadata.Name]) {
			t.Errorf("worker %s: allocated.actors %d, %d assignments", w.WorkerPod, s.Allocated.Actors, len(d.Assignments[w.Metadata.Name]))
		}
	}
	assigned := 0
	for _, a := range d.Actors {
		st := a.Status
		holds := st.State == ateapi.ActorState_ACTOR_STATE_RUNNING || st.State == ateapi.ActorState_ACTOR_STATE_RESUMING || st.State == ateapi.ActorState_ACTOR_STATE_SUSPENDING
		if holds != (st.WorkerAssignment != nil) {
			t.Errorf("%s/%s %v: worker_assignment set=%v", a.Metadata.Atespace, a.Metadata.Name, st.State, st.WorkerAssignment != nil)
		}
		if holds {
			assigned++
			if workers[st.WorkerAssignment.Worker.Name] == nil {
				t.Errorf("%s points at unknown worker", a.Metadata.Name)
			}
		}
		if (st.State == ateapi.ActorState_ACTOR_STATE_CRASHED) != (st.Crash != nil && st.State != ateapi.ActorState_ACTOR_STATE_REVERTING) {
			t.Errorf("%s: crash recorded %v in state %v", a.Metadata.Name, st.Crash != nil, st.State)
		}
		if a.Metadata.UpdateTime.AsTime().Before(a.Metadata.CreateTime.AsTime()) {
			t.Errorf("%s: update_time before create_time", a.Metadata.Name)
		}
	}
	n := 0
	for _, as := range d.Assignments {
		n += len(as)
	}
	if n != assigned {
		t.Errorf("%d assignments, %d actors holding a worker", n, assigned)
	}
}

// A crash leaves lines only in a worker Pod that was running when it
// happened, and the API states the crash time those lines carry.
func TestCrashLinesOnlyOnPodsThatRanTheActor(t *testing.T) {
	anchor := time.Now()
	pods := map[string]PodInfo{}
	for _, p := range defaultPlans() {
		pods[p.pod] = PodInfo{Name: p.pod, UID: hashUUID("pod", p.pod), Node: "node-" + p.pod, IP: "10.0.0.1", Found: true, Created: anchor.Add(-2 * time.Hour)}
	}
	lines := buildDataset(anchor, pods, anchor).CrashLines(anchor)
	all := strings.Join(lines["agent-pool-worker-1"], "\n")
	for _, want := range []string{`"ate.actor.name":"lit-review-0193"`, "Killing a sandbox over its memory limit", "Sandbox died", `"message":"Actor restored"`} {
		if !strings.Contains(all, want) {
			t.Errorf("agent-pool-worker-1 lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(lines["healthy-pool-worker-1"], "\n"), `"ate.actor.name":"pr-8812-reviewer"`) {
		t.Error("the worker-restart crash has its last lines on healthy-pool-worker-1")
	}

	d := buildDataset(anchor.Add(time.Minute), pods, anchor)
	for _, a := range d.Actors {
		if a.Metadata.Name == "lit-review-0193" {
			crash := anchor.Add(-specials()["research/lit-review-0193"].crashAge)
			if got := a.Status.Crash.CrashTime.AsTime(); !got.Equal(crash.Truncate(time.Millisecond)) {
				t.Errorf("crash time %v does not match the lines", got)
			}
			died := false
			for _, l := range lines["agent-pool-worker-1"] {
				if strings.Contains(l, `"msg":"Sandbox died"`) && strings.Contains(l, a.Metadata.Uid) {
					died = strings.Contains(l, `"time":"`+crash.UTC().Format(time.RFC3339Nano)+`"`)
				}
			}
			if !died {
				t.Errorf("no Sandbox died line at the crash time %v", crash)
			}
			if a.Status.AssignedNode != "node-agent-pool-worker-1" || a.Status.Snapshots[0].Locality != "node-agent-pool-worker-1" {
				t.Errorf("the crash's node is the Pod's: %+v", a.Status)
			}
		}
	}

	for name, p := range pods {
		p.Created = anchor.Add(-time.Minute)
		pods[name] = p
	}
	if got := buildDataset(anchor, pods, anchor).CrashLines(anchor); len(got) != 0 {
		t.Errorf("a Pod younger than the crash holds none of it: %v", got)
	}
}

// Ages read the same however long the API has run: only the crashes, whose
// lines are in the Pods' logs, keep the time they happened.
func TestTimelineHoldsWhileTheAPIRuns(t *testing.T) {
	anchor := time.Now().Truncate(time.Second)
	pods := map[string]PodInfo{}
	for _, p := range defaultPlans() {
		pi := PodInfo{Name: p.pod, UID: hashUUID("pod", p.pod), Node: "node-" + p.pod, IP: "10.0.0.1", Found: true, Created: anchor.Add(-6 * time.Hour)}
		if p.state == ateapi.WorkerState_WORKER_STATE_DRAINING {
			pi.Terminating, pi.DrainStart = true, pi.Created.Add(30*time.Second)
		}
		pods[p.pod] = pi
	}
	byName := func(d *Dataset) map[string]*ateapi.Actor {
		m := map[string]*ateapi.Actor{}
		for _, a := range d.Actors {
			m[a.Metadata.Atespace+"/"+a.Metadata.Name] = a
		}
		return m
	}
	transitional := map[ateapi.ActorState]bool{
		ateapi.ActorState_ACTOR_STATE_RESUMING: true, ateapi.ActorState_ACTOR_STATE_SUSPENDING: true,
		ateapi.ActorState_ACTOR_STATE_REVERTING: true, ateapi.ActorState_ACTOR_STATE_DELETING: true,
	}
	type reading struct{ stuck, fresh int }
	read := func(now time.Time) (reading, map[string]*ateapi.Actor) {
		d := buildDataset(now, pods, anchor)
		var r reading
		for _, a := range d.Actors {
			if !transitional[a.Status.State] {
				continue
			}
			switch held := now.Sub(a.Metadata.UpdateTime.AsTime()); {
			case held > 5*time.Minute:
				r.stuck++
			case held < 2*time.Minute:
				r.fresh++
			}
		}
		return r, byName(d)
	}

	early, a1 := read(anchor)
	late, a2 := read(anchor.Add(3 * time.Hour))
	if early != late || early.stuck != 3 || early.fresh == 0 {
		t.Errorf("stuck/fresh transitions at the anchor %+v, three hours on %+v; want 3 stuck and some fresh both times", early, late)
	}
	for _, k := range []string{"support-bots/ticket-41302", "code-review/codex-task-1050", "support-bots/ticket-40801"} {
		age1 := anchor.Sub(a1[k].Metadata.UpdateTime.AsTime())
		age2 := anchor.Add(3 * time.Hour).Sub(a2[k].Metadata.UpdateTime.AsTime())
		if age1 != age2 {
			t.Errorf("%s reads %v at the anchor, %v three hours on", k, age1, age2)
		}
	}

	// The incident: eleven crashes of one template, 20 to 40 minutes before
	// the anchor, within a quarter of an hour; the crash clock does not move.
	var lo, hi time.Duration
	n := 0
	for k, a := range a1 {
		if a.Status.State != ateapi.ActorState_ACTOR_STATE_CRASHED || a.ActorTemplate.Name != string(tLang) {
			continue
		}
		if !a2[k].Status.Crash.CrashTime.AsTime().Equal(a.Status.Crash.CrashTime.AsTime()) {
			t.Errorf("%s: crash time moved with the API's clock", k)
		}
		age := anchor.Sub(a.Status.Crash.CrashTime.AsTime())
		if n == 0 || age < lo {
			lo = age
		}
		hi = max(hi, age)
		n++
	}
	if n != 11 || lo < 20*time.Minute || hi > 40*time.Minute || hi-lo > 15*time.Minute {
		t.Errorf("incident: %d crashes from %v to %v ago", n, lo, hi)
	}

	// The worker restart: its actors crashed the same second, after it
	// registered its new process.
	d := buildDataset(anchor, pods, anchor)
	var restarted *ateapi.Worker
	for _, w := range d.Workers {
		if w.WorkerPod == "healthy-pool-worker-1" {
			restarted = w
		}
	}
	c1, c2 := a1["code-review/pr-8812-reviewer"].Status.Crash.CrashTime.AsTime(), a1["code-review/pr-8790-reviewer"].Status.Crash.CrashTime.AsTime()
	if !c1.Equal(c2) || restarted.Metadata.UpdateTime.AsTime().After(c1) {
		t.Errorf("worker restart at %v, its actors crashed at %v and %v", restarted.Metadata.UpdateTime.AsTime(), c1, c2)
	}

	// A draining worker took no placement after its drain began.
	drainStart := pods["agent-pool-worker-3"].DrainStart
	for _, p := range d.plans {
		if p.pod != "agent-pool-worker-3" {
			continue
		}
		if !p.worker.Metadata.UpdateTime.AsTime().Equal(drainStart) {
			t.Errorf("draining since %v, want the Pod's drain start %v", p.worker.Metadata.UpdateTime.AsTime(), drainStart)
		}
		for _, as := range d.Assignments[p.name] {
			if as.Metadata.CreateTime.AsTime().After(drainStart) {
				t.Errorf("%s placed at %v, after the drain began", as.Actor.Name, as.Metadata.CreateTime.AsTime())
			}
		}
	}
}
