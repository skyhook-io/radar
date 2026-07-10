package ai

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) (RunStore, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ai-runs.db")
	st, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st, dbPath
}

func TestStoreRoundtrip(t *testing.T) {
	st, _ := testStore(t)
	sum := RunSummary{
		ID: "run-1", Kind: "Pod", Namespace: "ns", Name: "p", Context: "ctx-a",
		Agent: "claude", Status: "running", SessionID: "sess-1",
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		UpdatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	st.SaveRun(sum)
	st.AppendEvent("run-1", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	st.AppendEvent("run-1", RunEvent{Seq: 2, Event: StreamEvent{Type: "thinking", Token: "hmm"}}, nil)
	// Terminal event rides with its summary in one transaction.
	sum.Status = "done"
	st.AppendEvent("run-1", RunEvent{Seq: 3, Event: StreamEvent{Type: "done"}}, &sum)
	st.(*sqliteRunStore).barrier()

	runs, err := st.LoadRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].Status != "done" || runs[0].SessionID != "sess-1" {
		t.Fatalf("LoadRuns = %+v", runs)
	}
	events, err := st.LoadEvents("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Seq != 1 || events[2].Event.Type != "done" {
		t.Fatalf("LoadEvents = %+v", events)
	}
	if events[1].Event.Token != "hmm" {
		t.Errorf("event payload lost: %+v", events[1])
	}
}

func TestStoreAutoSeq(t *testing.T) {
	st, _ := testStore(t)
	st.AppendEvent("run-9", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	// Seq 0 = store-assigned MAX+1: terminal markers on never-hydrated runs.
	st.AppendEvent("run-9", RunEvent{Event: StreamEvent{Type: "error", Error: "stale"}}, nil)
	st.AppendEvent("run-9", RunEvent{Event: StreamEvent{Type: "closed"}}, nil)
	st.(*sqliteRunStore).barrier()

	events, err := st.LoadEvents("run-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[1].Seq != 2 || events[2].Seq != 3 {
		t.Fatalf("auto-seq mis-assigned: %+v", events)
	}
	if events[2].Event.Type != "closed" {
		t.Errorf("order lost: %+v", events)
	}
}

func TestStoreDeleteAndClear(t *testing.T) {
	st, _ := testStore(t)
	for _, id := range []string{"run-1", "run-2"} {
		st.SaveRun(RunSummary{ID: id, Status: "done", CreatedAt: time.Now(), UpdatedAt: time.Now()})
		st.AppendEvent(id, RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	}
	st.DeleteRun("run-1")
	st.(*sqliteRunStore).barrier()
	runs, _ := st.LoadRuns()
	if len(runs) != 1 || runs[0].ID != "run-2" {
		t.Fatalf("DeleteRun left %+v", runs)
	}
	if err := st.Clear(nil); err != nil {
		t.Fatal(err)
	}
	runs, _ = st.LoadRuns()
	events, _ := st.LoadEvents("run-2")
	if len(runs) != 0 || len(events) != 0 {
		t.Fatalf("Clear left runs=%d events=%d", len(runs), len(events))
	}
}

func TestStoreFilePermissions(t *testing.T) {
	_, dbPath := testStore(t)
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("history DB is %v, want 0600 — transcripts hold cluster data", perm)
	}
}

func TestStoreOpenFailureIsClean(t *testing.T) {
	// A path whose parent can't be created must error (caller degrades to
	// memory-only), not panic or half-open.
	if _, err := OpenRunStore(filepath.Join(string([]byte{0}), "nope.db")); err == nil {
		t.Fatal("expected open error for impossible path")
	}
}
