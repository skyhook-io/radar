package timeline

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// setPostgresReconnectForTest drives the worker without a real outage: dial
// decides what each attempt sees, and the intervals shrink so the test does not
// wait out the production backoff.
func setPostgresReconnectForTest(t *testing.T, dial func(StoreConfig) (*PostgresStore, error)) {
	t.Helper()
	postgresReconnectMu.Lock()
	prevDial, prevInitial, prevMax := postgresDialForTest, postgresReconnectInitialInterval, postgresReconnectMaxInterval
	postgresDialForTest = dial
	postgresReconnectInitialInterval = time.Millisecond
	postgresReconnectMaxInterval = 2 * time.Millisecond
	postgresReconnectMu.Unlock()
	t.Cleanup(func() {
		// Retire the worker before restoring: a live one holds captured copies,
		// but a worker started after this point must see the real values.
		stopPostgresReconnect()
		postgresReconnectMu.Lock()
		postgresDialForTest, postgresReconnectInitialInterval, postgresReconnectMaxInterval = prevDial, prevInitial, prevMax
		postgresReconnectMu.Unlock()
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// An unreachable database must not fail InitStore. Returning an error here
// fails subsystem bring-up and takes every cluster view down with it.
func TestInitStoreDegradesWhenPostgresIsUnreachable(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	setPostgresReconnectForTest(t, func(StoreConfig) (*PostgresStore, error) {
		return nil, errors.New("dial tcp: connection refused")
	})

	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: "postgres://x/y"}); err != nil {
		t.Fatalf("InitStore returned %v; subsystem bring-up would fail", err)
	}
	if GetStore() != nil {
		t.Fatal("a store was installed despite an unreachable database")
	}
	if TimelineUnavailableReason() == "" {
		t.Fatal("no reason recorded; diagnostics would show a missing timeline with no explanation")
	}
	if !ObservationStart().IsZero() {
		t.Fatal("observation coverage claimed while nothing is recording")
	}
}

// The worker is the whole point: without it the timeline stays down for the life
// of the process. This fails if the worker never runs.
func TestReconnectWorkerInstallsTheStoreOnceTheDatabaseAnswers(t *testing.T) {
	dsn := testPostgresDSN(t)
	ResetStore()
	t.Cleanup(ResetStore)

	var attempts atomic.Int32
	setPostgresReconnectForTest(t, func(cfg StoreConfig) (*PostgresStore, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("dial tcp: connection refused")
		}
		return NewPostgresStore(dsn)
	})

	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: dsn}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if GetStore() != nil {
		t.Fatal("first attempt should have failed")
	}

	waitFor(t, "the worker to install a store", func() bool { return GetStore() != nil })
	if attempts.Load() < 3 {
		t.Fatalf("store appeared after %d attempts; the worker did not retry", attempts.Load())
	}
	if TimelineUnavailableReason() != "" {
		t.Fatalf("reason lingered after recovery: %s", TimelineUnavailableReason())
	}
	if ObservationStart().IsZero() {
		t.Fatal("observation window never opened after recovery")
	}
}

// ResetStore must retire the worker. A context switch reinitializes the store,
// and a worker from the previous context would install its connection over it.
func TestResetStoreRetiresTheWorker(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	setPostgresReconnectForTest(t, func(StoreConfig) (*PostgresStore, error) {
		return nil, errors.New("dial tcp: connection refused")
	})

	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: "postgres://x/y"}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	postgresReconnectMu.Lock()
	running := postgresReconnectQuit != nil
	postgresReconnectMu.Unlock()
	if !running {
		t.Fatal("no worker started; the timeline would stay down forever")
	}

	ResetStore()
	postgresReconnectMu.Lock()
	stopped := postgresReconnectQuit == nil
	postgresReconnectMu.Unlock()
	if !stopped {
		t.Fatal("worker survived ResetStore")
	}
	if TimelineUnavailableReason() != "" {
		t.Fatal("stale unavailable reason survived ResetStore")
	}
}

// The reason reaches diagnostics, so it must not carry the DSN password.
func TestUnavailableReasonRedactsCredentials(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	if err := InitStore(StoreConfig{
		Type: StoreTypePostgres,
		DSN:  "postgres://radar:hunter2@127.0.0.1:1/radar?sslmode=disable",
	}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	reason := TimelineUnavailableReason()
	if reason == "" {
		t.Fatal("no reason recorded")
	}
	if strings.Contains(reason, "hunter2") {
		t.Fatalf("reason leaked the DSN password: %s", reason)
	}
}

// A worker whose attempt fails after ResetStore retired it must not write its
// error: the reason would describe a database the new context never used.
func TestRetiredWorkerDoesNotRecordItsFailure(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	dialing := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	setPostgresReconnectForTest(t, func(StoreConfig) (*PostgresStore, error) {
		if calls.Add(1) == 2 {
			close(dialing)
			<-release
		}
		return nil, errors.New("dial tcp: connection refused")
	})

	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: "postgres://x/y"}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	<-dialing
	ResetStore()
	close(release)

	// The worker's failed attempt returns after the reset. Give it time to try
	// to record, then check nothing was written.
	time.Sleep(50 * time.Millisecond)
	if reason := TimelineUnavailableReason(); reason != "" {
		t.Fatalf("a retired worker recorded its failure after the reset: %s", reason)
	}
}
