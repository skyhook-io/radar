package substrate

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func pagedActor(atespace, name, uid string, state ateapi.ActorState, created, updated time.Duration, worker string) *ateapi.Actor {
	a := &ateapi.Actor{
		Metadata:      &ateapi.ResourceMetadata{Atespace: atespace, Name: name, Uid: uid, CreateTime: timestamppb.New(t0.Add(created)), UpdateTime: timestamppb.New(t0.Add(updated))},
		ActorTemplate: &ateapi.ObjectRef{Atespace: atespace, Name: "t"},
		Status:        &ateapi.ActorStatus{State: state},
	}
	if worker != "" {
		a.Status.WorkerAssignment = &ateapi.WorkerAssignment{Worker: &ateapi.ObjectRef{Name: worker}, WorkerNamespace: "agents", WorkerPool: "p", WorkerPod: worker + "-pod"}
	}
	return a
}

// pagedControl serves scripted actor pages, as Substrate's soft pagination
// may: in order, one per token, with every other list empty.
type pagedControl struct {
	ateapi.ControlClient
	pages   [][]*ateapi.Actor
	workers []*ateapi.Worker
}

func (c *pagedControl) ListAtespaces(context.Context, *ateapi.ListAtespacesRequest, ...grpc.CallOption) (*ateapi.ListAtespacesResponse, error) {
	return &ateapi.ListAtespacesResponse{}, nil
}

func (c *pagedControl) ListWorkers(context.Context, *ateapi.ListWorkersRequest, ...grpc.CallOption) (*ateapi.ListWorkersResponse, error) {
	return &ateapi.ListWorkersResponse{Workers: c.workers}, nil
}

func (c *pagedControl) ListActorTemplates(context.Context, *ateapi.ListActorTemplatesRequest, ...grpc.CallOption) (*ateapi.ListActorTemplatesResponse, error) {
	return &ateapi.ListActorTemplatesResponse{}, nil
}

func (c *pagedControl) ListActors(_ context.Context, r *ateapi.ListActorsRequest, _ ...grpc.CallOption) (*ateapi.ListActorsResponse, error) {
	i, _ := strconv.Atoi(r.GetOptions().GetPageToken())
	next := ""
	if i+1 < len(c.pages) {
		next = strconv.Itoa(i + 1)
	}
	return &ateapi.ListActorsResponse{Actors: c.pages[i], NextPageToken: next}, nil
}

func readPages(t *testing.T, c *pagedControl) *Snapshot {
	t.Helper()
	snap, tierErr := readSnapshot(context.Background(), &conn{client: c}, t0, nil)
	if tierErr != nil {
		t.Fatal(tierErr)
	}
	return snap
}

func TestReaderUnderPaginationChurn(t *testing.T) {
	running, suspended := ateapi.ActorState_ACTOR_STATE_RUNNING, ateapi.ActorState_ACTOR_STATE_SUSPENDED
	tests := []struct {
		name  string
		pages [][]*ateapi.Actor
		want  map[string]string // name -> "uid state"
	}{
		{"an empty middle page is followed", [][]*ateapi.Actor{
			{pagedActor("a", "x", "u1", running, 0, 0, "")}, {}, {pagedActor("a", "y", "u2", running, 0, 0, "")},
		}, map[string]string{"x": "u1 running", "y": "u2 running"}},
		{"the same UID on two pages counts once", [][]*ateapi.Actor{
			{pagedActor("a", "x", "u1", running, 0, time.Minute, "")}, {pagedActor("a", "x", "u1", running, 0, time.Minute, "")},
		}, map[string]string{"x": "u1 running"}},
		{"a later version of one UID wins", [][]*ateapi.Actor{
			{pagedActor("a", "x", "u1", running, 0, time.Minute, "")}, {pagedActor("a", "x", "u1", suspended, 0, 2*time.Minute, "")},
		}, map[string]string{"x": "u1 suspended"}},
		{"an earlier version on a later page loses", [][]*ateapi.Actor{
			{pagedActor("a", "x", "u1", suspended, 0, 2*time.Minute, "")}, {pagedActor("a", "x", "u1", running, 0, time.Minute, "")},
		}, map[string]string{"x": "u1 suspended"}},
		{"a deleted and recreated name keeps the newer actor", [][]*ateapi.Actor{
			{pagedActor("a", "x", "old", running, 0, time.Hour, "")}, {pagedActor("a", "x", "new", suspended, 30*time.Minute, 31*time.Minute, "")},
		}, map[string]string{"x": "new suspended"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap := readPages(t, &pagedControl{pages: tc.pages})
			got := map[string]string{}
			for _, a := range snap.Actors {
				got[a.Name] = a.UID + " " + a.State
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("actors = %v, want %v", got, tc.want)
			}
			if snap.Coverage.Actors.State != CoverageFull || *snap.Coverage.Actors.Count != len(tc.want) {
				t.Errorf("coverage = %+v", snap.Coverage.Actors)
			}
			if idx := snap.Index(); idx.States.Total != len(tc.want) {
				t.Errorf("index counts %d actors, want %d", idx.States.Total, len(tc.want))
			}
		})
	}
}

// Workers and actors are separate reads, so an actor may name a worker the
// worker read did not show it on. Placement follows each actor's own record.
func TestAssignmentChangedBetweenWorkerAndActorReads(t *testing.T) {
	c := &pagedControl{
		workers: []*ateapi.Worker{
			{Metadata: &ateapi.ResourceMetadata{Name: "w1"}, WorkerNamespace: "agents", WorkerPool: "p", WorkerPod: "w1-pod",
				Status: &ateapi.WorkerStatus{State: ateapi.WorkerState_WORKER_STATE_ACTIVE, Allocated: &ateapi.WorkerResources{Actors: 1}}},
		},
		pages: [][]*ateapi.Actor{{pagedActor("a", "moved", "u1", ateapi.ActorState_ACTOR_STATE_RUNNING, 0, 0, "w2")}},
	}
	snap := readPages(t, c)
	idx := snap.Index()
	if len(idx.ByWorker["w1"]) != 0 || len(idx.ByWorker["w2"]) != 1 {
		t.Errorf("by worker = %v", idx.ByWorker)
	}
	if snap.Workers[0].Allocated.Actors != 1 {
		t.Error("the worker's own allocation stays as Substrate reported it")
	}
}

// scopedPages is an identity that may list only atespace by atespace, each
// holding perScope actors.
type scopedPages struct {
	ateapi.ControlClient
	atespaces int
	perScope  int
	calls     atomic.Int32
}

func (c *scopedPages) ListAtespaces(context.Context, *ateapi.ListAtespacesRequest, ...grpc.CallOption) (*ateapi.ListAtespacesResponse, error) {
	out := &ateapi.ListAtespacesResponse{}
	for i := range c.atespaces {
		out.Atespaces = append(out.Atespaces, &ateapi.Atespace{Metadata: &ateapi.ResourceMetadata{Name: fmt.Sprintf("s%02d", i)}})
	}
	return out, nil
}

func (c *scopedPages) ListWorkers(context.Context, *ateapi.ListWorkersRequest, ...grpc.CallOption) (*ateapi.ListWorkersResponse, error) {
	return &ateapi.ListWorkersResponse{}, nil
}

func (c *scopedPages) ListActorTemplates(_ context.Context, r *ateapi.ListActorTemplatesRequest, _ ...grpc.CallOption) (*ateapi.ListActorTemplatesResponse, error) {
	return &ateapi.ListActorTemplatesResponse{}, nil
}

func (c *scopedPages) ListActors(_ context.Context, r *ateapi.ListActorsRequest, _ ...grpc.CallOption) (*ateapi.ListActorsResponse, error) {
	if r.GetAtespace() == "" {
		return nil, status.Error(codes.PermissionDenied, "no list across atespaces")
	}
	c.calls.Add(1)
	out := &ateapi.ListActorsResponse{}
	for i := range c.perScope {
		out.Actors = append(out.Actors, pagedActor(r.GetAtespace(), fmt.Sprintf("a%d", i), r.GetAtespace()+strconv.Itoa(i), ateapi.ActorState_ACTOR_STATE_RUNNING, 0, 0, ""))
	}
	return out, nil
}

func TestActorBudgetSpansEveryAtespace(t *testing.T) {
	prev := MaxActors
	MaxActors = 5
	t.Cleanup(func() { MaxActors = prev })
	c := &scopedPages{atespaces: 40, perScope: 3}
	snap, tierErr := readSnapshot(context.Background(), &conn{client: c}, t0, nil)
	if tierErr != nil {
		t.Fatal(tierErr)
	}
	cov := snap.Coverage.Actors
	if len(snap.Actors) != 5 || cap(snap.Actors) != 5 || *cov.Count != 5 {
		t.Errorf("kept %d actors (cap %d): one budget for the whole read, in a slice of its own size", len(snap.Actors), cap(snap.Actors))
	}
	if cov.State != CoveragePartial || cov.Code != CodeCapExceeded || len(cov.UnreadAtespaces) == 0 {
		t.Errorf("coverage = %+v: atespaces never started must be named unread", cov)
	}
	if calls := int(c.calls.Load()); calls >= 40 || calls+len(cov.UnreadAtespaces) != 40 {
		t.Errorf("%d atespaces read and %d unread of 40: dispatch must stop once the budget is spent", calls, len(cov.UnreadAtespaces))
	}
	read := slices.Concat(cov.ReadAtespaces, cov.UnreadAtespaces)
	slices.Sort(read)
	if len(read) != 40 {
		t.Errorf("every atespace is either read or unread: %v", read)
	}
}

func TestStaleWhileRefresh(t *testing.T) {
	clock := t0
	m, k, s := stubManager(t, fixedKube)
	m.Now = func() time.Time { return clock }
	ctl := &stubControl{}
	m.conn = &conn{key: connKey(k, s), client: ctl}
	first, tier := m.Snapshot(context.Background())
	if tier.State != TierConnected || first == nil || !tier.ReadAt.Equal(clock) {
		t.Fatalf("first read = %+v", tier)
	}

	// Past Freshness: served at once while a refresh runs.
	clock = clock.Add(Freshness + time.Second)
	ctl.entered, ctl.release = make(chan struct{}), make(chan struct{})
	served, tier := m.Snapshot(context.Background())
	if served != first || !tier.Refreshing || tier.State != TierConnected || tier.LastError != nil {
		t.Errorf("stale read = %+v", tier)
	}
	<-ctl.entered
	if _, peek := m.Peek(context.Background()); !peek.Refreshing {
		t.Errorf("peek during refresh = %+v", peek)
	}

	// The refresh fails: the last good snapshot is still served, with the error.
	ctl.unavailable = true
	close(ctl.release)
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.failed != nil
	})
	served, tier = m.Snapshot(context.Background())
	if served != first || tier.LastError == nil || tier.LastError.Code != CodeUnreachable || !tier.Refreshing {
		t.Errorf("after a failed refresh = %+v", tier)
	}

	// Past MaxStale the failure is the answer.
	clock = clock.Add(MaxStale)
	if snap, tier := m.Snapshot(context.Background()); snap != nil || tier.State != TierFailed {
		t.Errorf("past MaxStale = %+v", tier)
	}
}

func TestPeekNeverWaitsOnAFirstRead(t *testing.T) {
	m, k, s := stubManager(t, fixedKube)
	ctl := &stubControl{entered: make(chan struct{}), release: make(chan struct{})}
	m.conn = &conn{key: connKey(k, s), client: ctl}
	done := make(chan APITier, 1)
	go func() {
		_, tier := m.Peek(context.Background())
		done <- tier
	}()
	select {
	case tier := <-done:
		if tier.State != TierConnecting {
			t.Errorf("peek with nothing read = %+v", tier)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Peek waited on the read")
	}
	<-ctl.entered
	close(ctl.release)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
