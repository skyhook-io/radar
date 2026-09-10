package timeline

import (
	"strings"
	"testing"
)

// An unreachable database must not fail InitStore. Every caller already treats a
// missing store as "timeline unavailable", so leaving it unset degrades the
// timeline alone; returning an error here instead fails subsystem bring-up and
// takes every cluster view down with it.
func TestInitStorePostgresUnreachableDegradesInsteadOfFailing(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	err := InitStore(StoreConfig{
		Type: StoreTypePostgres,
		DSN:  "postgres://radar:radar@127.0.0.1:1/radar?sslmode=disable",
	})
	if err != nil {
		t.Fatalf("InitStore returned an error, which fails subsystem bring-up: %v", err)
	}
	if GetStore() != nil {
		t.Fatal("a store was installed despite an unreachable database")
	}
	if TimelineUnavailableReason() == "" {
		t.Fatal("no reason recorded; diagnostics would show a missing timeline with no explanation")
	}
}

// The reason must never leak the DSN password, since it reaches diagnostics.
func TestUnavailableReasonRedactsCredentials(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	if err := InitStore(StoreConfig{
		Type: StoreTypePostgres,
		DSN:  "postgres://radar:hunter2@127.0.0.1:1/radar?sslmode=disable",
	}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if reason := TimelineUnavailableReason(); reason == "" {
		t.Fatal("no reason recorded")
	} else if strings.Contains(reason, "hunter2") {
		t.Fatalf("reason leaked the DSN password: %s", reason)
	}
}

// ResetStore must stop the worker. A context switch reinitializes the store, and
// a worker left running from the previous context would install its connection
// over the new one.
func TestResetStoreStopsTheReconnectWorker(t *testing.T) {
	ResetStore()
	if err := InitStore(StoreConfig{
		Type: StoreTypePostgres,
		DSN:  "postgres://radar:radar@127.0.0.1:1/radar?sslmode=disable",
	}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	postgresReconnectMu.Lock()
	running := postgresReconnectQuit != nil
	postgresReconnectMu.Unlock()
	if !running {
		t.Fatal("no reconnect worker started; the timeline would stay down forever")
	}

	ResetStore()
	postgresReconnectMu.Lock()
	stopped := postgresReconnectQuit == nil
	postgresReconnectMu.Unlock()
	if !stopped {
		t.Fatal("reconnect worker survived ResetStore")
	}
	if TimelineUnavailableReason() != "" {
		t.Fatal("stale unavailable reason survived ResetStore")
	}
}

// The worker installs the store once the database becomes reachable, without
// anything else prompting it.
func TestReconnectWorkerInstallsTheStoreWhenTheDatabaseReturns(t *testing.T) {
	dsn := testPostgresDSN(t)
	ResetStore()
	t.Cleanup(ResetStore)

	// Start against a dead address so the first attempt fails, then point the
	// worker at the live database by restarting it with the real DSN - the same
	// path a database coming back takes.
	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: "postgres://radar:radar@127.0.0.1:1/radar?sslmode=disable"}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if GetStore() != nil {
		t.Fatal("store installed despite a dead address")
	}
	ResetStore()
	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: dsn}); err != nil {
		t.Fatalf("InitStore with a live database: %v", err)
	}
	if GetStore() == nil {
		t.Fatal("no store installed against a live database")
	}
	if TimelineUnavailableReason() != "" {
		t.Fatalf("unavailable reason lingered after a successful open: %s", TimelineUnavailableReason())
	}
}
