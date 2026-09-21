// SQLite persistence for AI investigation runs — the durable backing behind
// RunManager's in-memory state, so history, transcripts, and the agent-session
// hand-off survive a Radar restart. Single local user (the feature is gated to
// no-auth standalone radar), so one DB file with a single writer is plenty.
package ai

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

// RunStore persists runs and their event logs. Write methods are asynchronous
// and must never block the caller — RunManager enqueues them while holding a
// run's mutex on the live event path.
type RunStore interface {
	// SaveRun upserts a run's summary row.
	SaveRun(s RunSummary)
	// AppendEvent appends one event row. e.Seq > 0 inserts that exact sequence;
	// e.Seq == 0 lets the store assign MAX(seq)+1 (used for terminal markers on
	// runs whose log was never hydrated into memory). When summary is non-nil it
	// is upserted in the SAME transaction — terminal events ride with their
	// status so crash recovery can trust the status column.
	AppendEvent(runID string, e RunEvent, summary *RunSummary)
	// AppendEvents is the ordered batch form of AppendEvent. Every event and the
	// optional summary commit in ONE transaction. Compound boundaries such as
	// apply→verify and stale-error→closed use this so a hard process exit can
	// leave either the whole transition or none of it, never a partial boundary.
	AppendEvents(runID string, events []RunEvent, summary *RunSummary)
	// LoadRuns returns every persisted summary, oldest first.
	LoadRuns() ([]RunSummary, error)
	// LoadEvents returns a run's events ordered by seq.
	LoadEvents(runID string) ([]RunEvent, error)
	// DeleteRun removes a run and its events.
	DeleteRun(id string)
	// ClearTerminal synchronously removes every non-running run and its events in
	// ONE transaction. The store derives the live set from the same database
	// snapshot used by the delete: a manager's in-memory run list is deliberately
	// incomplete when another Radar process owns a live run in this shared DB.
	ClearTerminal() error
	// Degraded reports that persistence has stopped working (disk error or a
	// saturated write queue) — history will not survive a restart.
	Degraded() bool
	// Path returns the DB file path (so a detached/broken store's files can
	// still be removed when the user clears history).
	Path() string
	// Close drains pending writes and closes the DB.
	Close()
}

// errCorruptRunHistory classifies structural transcript damage separately from
// transient database/read failures. Callers must not retry this condition in a
// tight EventSource loop: the same retained rows will fail on every load.
var errCorruptRunHistory = errors.New("corrupt investigation transcript")

const storeSchema = `
CREATE TABLE IF NOT EXISTS runs (
	id           TEXT PRIMARY KEY,
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL,
	status       TEXT NOT NULL,
	summary_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_events (
	run_id     TEXT NOT NULL,
	seq        INTEGER NOT NULL,
	event_json TEXT NOT NULL,
	PRIMARY KEY (run_id, seq)
);
`

// sqliteRunStore implements RunStore over one SQLite file with a single writer
// goroutine. Writes are enqueued (non-blocking) and applied in order; reads use
// the same connection and may briefly wait on the writer (busy_timeout).
type sqliteRunStore struct {
	path string
	db   *sql.DB
	ops  chan func(db *sql.DB) error
	done chan struct{}
	// closeMu makes enqueue-vs-Close safe: senders hold RLock while checking the
	// closed flag AND sending, Close holds Lock while flipping it and closing the
	// channel — so a send can never race the close and panic.
	closeMu  sync.RWMutex
	closed   bool
	degraded atomic.Bool
}

// OpenRunStore opens (or creates) the run history DB. The file is created 0600
// before SQLite touches it so the WAL/SHM siblings inherit private permissions —
// transcripts contain cluster data.
func OpenRunStore(dbPath string) (RunStore, error) {
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("ai history dir: %w", err)
		}
	}
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ai history file: %w", err)
	}
	_ = f.Close()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("ai history open: %w", err)
	}
	// One writer at a time (SQLite's model); reads share the connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=10000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA user_version=1",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("ai history pragma: %w", err)
		}
	}
	if _, err := db.Exec(storeSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ai history schema: %w", err)
	}

	s := &sqliteRunStore{
		path: dbPath,
		db:   db,
		ops:  make(chan func(db *sql.DB) error, 4096),
		done: make(chan struct{}),
	}
	go s.writer()
	return s, nil
}

func (s *sqliteRunStore) writer() {
	defer close(s.done)
	for op := range s.ops {
		if op == nil {
			continue
		}
		if err := op(s.db); err != nil && !s.degraded.Load() {
			s.degraded.Store(true)
			log.Printf("[ai] history write failed — investigations will NOT survive a restart: %v", err)
		}
	}
}

// enqueue hands an op to the writer without ever blocking the caller (it runs
// under a run's mutex on the hot path). A full queue flips degraded and drops.
func (s *sqliteRunStore) enqueue(op func(db *sql.DB) error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.ops <- op:
	default:
		if !s.degraded.Load() {
			s.degraded.Store(true)
			log.Printf("[ai] history write queue full — dropping writes; investigations will NOT survive a restart")
		}
	}
}

// enqueueWait hands an op to the writer and blocks until it ran (user-initiated
// operations like Clear, where dropping would be wrong). Returns false if the
// store is closed.
func (s *sqliteRunStore) enqueueWait(op func(db *sql.DB) error) bool {
	done := make(chan struct{})
	s.closeMu.RLock()
	if s.closed {
		s.closeMu.RUnlock()
		return false
	}
	s.ops <- func(db *sql.DB) error {
		defer close(done)
		return op(db)
	}
	s.closeMu.RUnlock()
	<-done
	return true
}

// barrier waits until every op enqueued before it has been applied.
func (s *sqliteRunStore) barrier() {
	s.enqueueWait(func(*sql.DB) error { return nil })
}

func upsertRun(db *sql.DB, s RunSummary) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO runs (id, created_at, updated_at, status, summary_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at,
			status=excluded.status, summary_json=excluded.summary_json`,
		s.ID, s.CreatedAt.UnixMilli(), s.UpdatedAt.UnixMilli(), s.Status, string(b))
	return err
}

func (s *sqliteRunStore) SaveRun(sum RunSummary) {
	s.enqueue(func(db *sql.DB) error { return upsertRun(db, sum) })
}

func (s *sqliteRunStore) AppendEvent(runID string, e RunEvent, summary *RunSummary) {
	s.AppendEvents(runID, []RunEvent{e}, summary)
}

func (s *sqliteRunStore) AppendEvents(runID string, events []RunEvent, summary *RunSummary) {
	if len(events) == 0 {
		return
	}
	s.enqueue(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, event := range events {
			b, marshalErr := json.Marshal(event.Event)
			if marshalErr != nil {
				return marshalErr
			}
			if event.Seq > 0 {
				_, err = tx.Exec(`INSERT OR REPLACE INTO run_events (run_id, seq, event_json) VALUES (?, ?, ?)`,
					runID, event.Seq, string(b))
			} else {
				// Store-assigned sequence: terminal markers appended to a run whose
				// log was never loaded into memory this process. Repeating MAX inside
				// this transaction advances through an unsequenced batch.
				_, err = tx.Exec(`INSERT INTO run_events (run_id, seq, event_json)
				SELECT ?, COALESCE(MAX(seq), 0) + 1, ? FROM run_events WHERE run_id = ?`,
					runID, string(b), runID)
			}
			if err != nil {
				return err
			}
		}
		if summary != nil {
			b, err := json.Marshal(*summary)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO runs (id, created_at, updated_at, status, summary_json)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at,
					status=excluded.status, summary_json=excluded.summary_json`,
				summary.ID, summary.CreatedAt.UnixMilli(), summary.UpdatedAt.UnixMilli(),
				summary.Status, string(b)); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func (s *sqliteRunStore) LoadRuns() ([]RunSummary, error) {
	s.barrier() // read-your-writes: drain queued ops so loads see them
	rows, err := s.db.Query(`SELECT summary_json FROM runs ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunSummary
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var sum RunSummary
		if err := json.Unmarshal([]byte(raw), &sum); err != nil {
			continue // one corrupt row shouldn't lose the rest of history
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

func (s *sqliteRunStore) LoadEvents(runID string) ([]RunEvent, error) {
	s.barrier() // read-your-writes: a hydration right after markStale/startup must see those markers
	rows, err := s.db.Query(`SELECT seq, event_json FROM run_events WHERE run_id = ? ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("load investigation events for %q: %w", runID, err)
	}
	defer rows.Close()
	var out []RunEvent
	expectedSeq := 1
	for rows.Next() {
		var seq int
		var raw string
		if err := rows.Scan(&seq, &raw); err != nil {
			// With this fixed schema, Scan failures are stored-value conversion
			// failures (for example a non-integer sequence), not a retryable read
			// outage. Driver I/O failures arrive through rows.Err below.
			return nil, fmt.Errorf("%w for %q: invalid stored event row: %v", errCorruptRunHistory, runID, err)
		}
		if seq != expectedSeq {
			return nil, fmt.Errorf(
				"%w for %q: non-contiguous sequence: got %d, want %d",
				errCorruptRunHistory, runID, seq, expectedSeq,
			)
		}
		var ev StreamEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil, fmt.Errorf("%w for %q at sequence %d: invalid event JSON: %v", errCorruptRunHistory, runID, seq, err)
		}
		if ev.Type == "" {
			return nil, fmt.Errorf("%w for %q at sequence %d: event type is empty", errCorruptRunHistory, runID, seq)
		}
		out = append(out, RunEvent{Seq: seq, Event: ev})
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load investigation events for %q: %w", runID, err)
	}
	return out, nil
}

func (s *sqliteRunStore) DeleteRun(id string) {
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.Exec(`DELETE FROM run_events WHERE run_id = ?`, id); err != nil {
			return err
		}
		_, err := db.Exec(`DELETE FROM runs WHERE id = ?`, id)
		return err
	})
}

func (s *sqliteRunStore) ClearTerminal() error {
	var out error
	ran := s.enqueueWait(func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			out = err
			return err
		}
		defer func() { _ = tx.Rollback() }()
		// Keep events only when their summary is running at this transaction's
		// snapshot. This also clears orphaned event rows, which have no live owner
		// to protect. SQLite's write transaction prevents a foreign terminal update
		// from interleaving between the event and summary deletes.
		if _, err := tx.Exec(`DELETE FROM run_events
			WHERE NOT EXISTS (
				SELECT 1 FROM runs
				WHERE runs.id = run_events.run_id AND runs.status = 'running'
			)`); err != nil {
			out = err
			return err
		}
		if _, err := tx.Exec(`DELETE FROM runs WHERE status <> 'running'`); err != nil {
			out = err
			return err
		}
		out = tx.Commit()
		return out
	})
	if !ran {
		// A closed store didn't run the delete at all — reporting success here
		// would let callers drop state the DB still holds.
		return errors.New("history store is closed")
	}
	return out
}

func (s *sqliteRunStore) Degraded() bool { return s.degraded.Load() }

func (s *sqliteRunStore) Path() string { return s.path }

// Close drains pending writes and closes the DB. Late writers (agent goroutines
// finishing after shutdown began) become no-ops via the closed flag; the flag
// flip and the channel close happen under the write lock, so no sender can race
// them.
func (s *sqliteRunStore) Close() {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closed = true
	close(s.ops)
	s.closeMu.Unlock()
	<-s.done
	_ = s.db.Close()
}
