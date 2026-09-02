package ai

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		ID: "run-1", Kind: "Rollout", Group: "argoproj.io", Namespace: "ns", Name: "p", Context: "ctx-a",
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
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].Status != "done" || runs[0].SessionID != "sess-1" || runs[0].Group != "argoproj.io" {
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

func TestStoreEvidenceProvenanceSurvivesReopen(t *testing.T) {
	st, dbPath := testStore(t)
	firstRef := testEvidenceRef('a', 'b')
	secondRef := testEvidenceRef('c', 'd')
	success := false
	want := []RunEvent{
		{Seq: 1, Event: StreamEvent{Type: "turn"}},
		{Seq: 2, Event: StreamEvent{Type: "step", Step: &StepInfo{
			ID: "logs", Tool: "get_pod_logs", Status: "done",
			Result: `{"logs":["authentication failed"]}`, EvidenceRef: firstRef, IsError: &success,
		}}},
		{Seq: 3, Event: StreamEvent{Type: "step", Step: &StepInfo{
			ID: "secret", Tool: "get_resource", Status: "done",
			Result: `{"kind":"Secret"}`, EvidenceRef: secondRef, IsError: &success,
		}}},
		{Seq: 4, Event: StreamEvent{Type: "done", Diag: &Diagnosis{
			RootCause: "The workload uses a stale database credential.",
			Report:    "The pod logs and Secret state agree.",
			RootCauseEvidence: &RootCauseEvidence{
				Status: EvidenceLinked,
				Refs:   []string{firstRef, secondRef},
			},
		}}},
	}
	summary := RunSummary{
		ID: "run-evidence", Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "api",
		Context: "ctx-a", Agent: "codex", Profile: ExecutionProfileSafeguarded, Status: "done",
		CreatedAt: nowUTC(), UpdatedAt: nowUTC(),
	}
	st.AppendEvents(summary.ID, want, &summary)
	st.Close() // prove the fields survive disk, not merely the live DB handle

	reloaded, err := OpenRunStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(reloaded.Close)
	got, err := reloaded.LoadEvents(summary.ID)
	if err != nil {
		t.Fatalf("LoadEvents after reopen: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evidence provenance changed across SQLite reopen:\n got: %#v\nwant: %#v", got, want)
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

func TestStoreLoadEventsRejectsCorruptTranscript(t *testing.T) {
	tests := []struct {
		name       string
		insertRows func(*testing.T, *sqliteRunStore)
		wantError  string
	}{
		{
			name: "malformed event JSON",
			insertRows: func(t *testing.T, store *sqliteRunStore) {
				t.Helper()
				if _, err := store.db.Exec(
					`INSERT INTO run_events (run_id, seq, event_json) VALUES (?, ?, ?)`,
					"run-corrupt", 1, `{"type":`,
				); err != nil {
					t.Fatalf("insert malformed event: %v", err)
				}
			},
			wantError: "sequence 1: invalid event JSON",
		},
		{
			name: "sequence gap",
			insertRows: func(t *testing.T, store *sqliteRunStore) {
				t.Helper()
				for _, row := range []struct {
					seq int
					raw string
				}{
					{seq: 1, raw: `{"type":"turn"}`},
					{seq: 3, raw: `{"type":"done"}`},
				} {
					if _, err := store.db.Exec(
						`INSERT INTO run_events (run_id, seq, event_json) VALUES (?, ?, ?)`,
						"run-corrupt", row.seq, row.raw,
					); err != nil {
						t.Fatalf("insert sequence %d: %v", row.seq, err)
					}
				}
			},
			wantError: "non-contiguous sequence: got 3, want 2",
		},
		{
			name: "invalid sequence value",
			insertRows: func(t *testing.T, store *sqliteRunStore) {
				t.Helper()
				if _, err := store.db.Exec(
					`INSERT INTO run_events (run_id, seq, event_json) VALUES (?, ?, ?)`,
					"run-corrupt", "not-a-sequence", `{"type":"turn"}`,
				); err != nil {
					t.Fatalf("insert invalid sequence: %v", err)
				}
			},
			wantError: "invalid stored event row",
		},
		{
			name: "missing event type",
			insertRows: func(t *testing.T, store *sqliteRunStore) {
				t.Helper()
				if _, err := store.db.Exec(
					`INSERT INTO run_events (run_id, seq, event_json) VALUES (?, ?, ?)`,
					"run-corrupt", 1, `{}`,
				); err != nil {
					t.Fatalf("insert typeless event: %v", err)
				}
			},
			wantError: "event type is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			sqliteStore := store.(*sqliteRunStore)
			now := time.Now().UTC()
			store.SaveRun(RunSummary{
				ID: "run-corrupt", Kind: "Pod", Namespace: "default", Name: "web",
				Context: "ctx-a", Agent: "claude", Profile: ExecutionProfileSafeguarded, Status: "done",
				CreatedAt: now, UpdatedAt: now,
			})
			sqliteStore.barrier()
			tt.insertRows(t, sqliteStore)

			events, err := store.LoadEvents("run-corrupt")
			if err == nil || !errors.Is(err, errCorruptRunHistory) || !strings.Contains(err.Error(), `"run-corrupt"`) || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("LoadEvents() = events %+v, err %v; want contextual %q error", events, err, tt.wantError)
			}
			if events != nil {
				t.Fatalf("LoadEvents returned partial corrupt transcript: %+v", events)
			}

			manager := persistedManager(t, store, "ctx-a")
			run := manager.Get("run-corrupt")
			if run == nil {
				t.Fatal("persisted corrupt run summary was not loaded")
			}
			backlog, ch, _, cancel, subscribeErr := run.Subscribe(0)
			if !errors.Is(subscribeErr, ErrHistoryCorrupt) {
				t.Fatalf("Subscribe() error = %v, want ErrHistoryCorrupt", subscribeErr)
			}
			if backlog != nil || ch != nil || cancel != nil {
				t.Fatalf("corrupt hydration returned partial success: backlog=%+v channel=%t cancel=%t", backlog, ch != nil, cancel != nil)
			}
			if err := manager.AddTurn("run-corrupt", "continue?", false, "", false); !errors.Is(err, ErrHistoryCorrupt) {
				t.Fatalf("AddTurn() error = %v, want ErrHistoryCorrupt", err)
			}
		})
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
	if err := st.ClearTerminal(); err != nil {
		t.Fatal(err)
	}
	runs, _ = st.LoadRuns()
	events, _ := st.LoadEvents("run-2")
	if len(runs) != 0 || len(events) != 0 {
		t.Fatalf("Clear left runs=%d events=%d", len(runs), len(events))
	}
}

func TestStoreClearTerminalPreservesAllRunningRowsAndEvents(t *testing.T) {
	st, _ := testStore(t)
	now := time.Now().UTC()
	for _, run := range []RunSummary{
		{ID: "run-local-live", Status: "running", OwnerPID: os.Getpid(), CreatedAt: now, UpdatedAt: now},
		{ID: "run-foreign-live", Status: "running", OwnerPID: os.Getpid() + 1, CreatedAt: now, UpdatedAt: now},
		{ID: "run-foreign-done", Status: "done", OwnerPID: os.Getpid() + 1, CreatedAt: now, UpdatedAt: now},
	} {
		st.SaveRun(run)
		st.AppendEvent(run.ID, RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	}
	// An orphan has no running summary and is terminal garbage for clear-history
	// purposes; it must not survive merely because it is absent from runs.
	st.AppendEvent("run-orphan", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)

	if err := st.ClearTerminal(); err != nil {
		t.Fatal(err)
	}
	runs, err := st.LoadRuns()
	if err != nil {
		t.Fatal(err)
	}
	gotRuns := make(map[string]string, len(runs))
	for _, run := range runs {
		gotRuns[run.ID] = run.Status
	}
	if len(gotRuns) != 2 || gotRuns["run-local-live"] != "running" || gotRuns["run-foreign-live"] != "running" {
		t.Fatalf("ClearTerminal left summaries %+v, want both running rows only", runs)
	}
	for _, id := range []string{"run-local-live", "run-foreign-live"} {
		events, err := st.LoadEvents(id)
		if err != nil {
			t.Fatalf("LoadEvents(%q): %v", id, err)
		}
		if len(events) != 1 || events[0].Event.Type != "turn" {
			t.Fatalf("ClearTerminal damaged %q events: %+v", id, events)
		}
	}
	for _, id := range []string{"run-foreign-done", "run-orphan"} {
		events, err := st.LoadEvents(id)
		if err != nil {
			t.Fatalf("LoadEvents(%q): %v", id, err)
		}
		if len(events) != 0 {
			t.Fatalf("ClearTerminal retained %q events: %+v", id, events)
		}
	}
}

func TestStoreClearTerminalRollsBackEventsWhenSummaryDeleteFails(t *testing.T) {
	st, _ := testStore(t)
	sqliteStore := st.(*sqliteRunStore)
	now := time.Now().UTC()
	st.SaveRun(RunSummary{ID: "run-done", Status: "done", CreatedAt: now, UpdatedAt: now})
	st.AppendEvent("run-done", RunEvent{Seq: 1, Event: StreamEvent{Type: "turn"}}, nil)
	sqliteStore.barrier()
	if _, err := sqliteStore.db.Exec(`CREATE TRIGGER reject_run_delete
		BEFORE DELETE ON runs BEGIN SELECT RAISE(ABORT, 'delete rejected'); END`); err != nil {
		t.Fatalf("create delete-failure trigger: %v", err)
	}

	if err := st.ClearTerminal(); err == nil {
		t.Fatal("ClearTerminal succeeded despite rejected summary delete")
	}
	runs, err := st.LoadRuns()
	if err != nil {
		t.Fatal(err)
	}
	events, err := st.LoadEvents("run-done")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != "run-done" || len(events) != 1 || events[0].Event.Type != "turn" {
		t.Fatalf("failed clear was not atomic: runs=%+v events=%+v", runs, events)
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
