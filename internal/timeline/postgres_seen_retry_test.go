package timeline

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// seenMarkFixture is a store installed behind a proxy that can take the
// database away, with a direct connection to read what the database holds.
type seenMarkFixture struct {
	t      *testing.T
	proxy  *dbProxy
	store  *PostgresStore
	direct *sql.DB
}

// newSeenMarkFixture seeds marks for names through a store of its own, then
// installs a second store behind the proxy, the way a running Radar holds them.
func newSeenMarkFixture(t *testing.T, names ...string) *seenMarkFixture {
	t.Helper()
	ResetStore()
	t.Cleanup(ResetStore)
	prevInterval := postgresOutageCheckInterval
	postgresOutageCheckInterval = 100 * time.Millisecond
	t.Cleanup(func() { postgresOutageCheckInterval = prevInterval })

	directDSN := testPostgresDSN(t)
	seed, err := NewPostgresStore(directDSN)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	for _, name := range names {
		seed.MarkResourceSeen("ctx", "apps", "Deployment", "shop", name)
	}
	if err := seed.Close(); err != nil { // Close drains the queued marks.
		t.Fatalf("Close: %v", err)
	}

	direct, err := sql.Open("pgx", directDSN)
	if err != nil {
		t.Fatalf("open direct connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })

	proxied, err := url.Parse(directDSN)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, proxied.Host)
	proxied.Host = proxy.addr
	q := proxied.Query()
	q.Set("connect_timeout", "2")
	proxied.RawQuery = q.Encode()
	store, err := NewPostgresStore(proxied.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	return &seenMarkFixture{t: t, proxy: proxy, store: store, direct: direct}
}

func (f *seenMarkFixture) install() {
	installPostgresStore(f.store, StoreConfig{})
	// Registered after the test database's own cleanup, so it runs first: the
	// schema can't be dropped while this store is still building indexes.
	f.t.Cleanup(ResetStore)
}

// unclearedCount reports how many failed clears the store holds for a retry.
func (f *seenMarkFixture) unclearedCount() int {
	f.store.seenMu.RLock()
	defer f.store.seenMu.RUnlock()
	return len(f.store.unclearedSeen)
}

// markedInDatabase reads the mark the way the next restart would load it.
func (f *seenMarkFixture) markedInDatabase(name string) bool {
	f.t.Helper()
	var n int
	key := []byte(SeenResourceKey("ctx", "apps", "Deployment", "shop", name))
	if err := f.direct.QueryRow("SELECT COUNT(*) FROM radar_timeline_seen_resources WHERE resource_key = $1", key).Scan(&n); err != nil {
		f.t.Fatalf("read seen mark: %v", err)
	}
	return n > 0
}

// A delete while the database is gone leaves the resource's mark in the
// database. After the next restart that mark makes a new resource with the same
// name look already seen, so its creation is dropped. The clear has to be
// applied once the database answers again.
func TestClearThatFailedDuringAnOutageIsAppliedWhenTheDatabaseAnswers(t *testing.T) {
	f := newSeenMarkFixture(t, "web", "kept")
	f.install()

	f.proxy.down()
	f.store.ClearResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	if !f.markedInDatabase("web") {
		t.Fatal("setup: the clear reached the database with the proxy down")
	}

	f.proxy.up(f.proxy.addr)
	waitFor(t, "the failed clear to be applied", func() bool { return !f.markedInDatabase("web") })
	if !f.markedInDatabase("kept") {
		t.Fatal("a resource that was not deleted lost its mark")
	}
}

// A resource deleted during the outage and marked again before the database
// answers exists again, so the retried clear must leave its mark alone.
func TestMarkAfterAFailedClearKeepsTheResourceMarked(t *testing.T) {
	f := newSeenMarkFixture(t, "recreated", "gone")
	f.install()

	f.proxy.down()
	f.store.ClearResourceSeen("ctx", "apps", "Deployment", "shop", "recreated")
	f.store.ClearResourceSeen("ctx", "apps", "Deployment", "shop", "gone")
	f.store.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "recreated")

	f.proxy.up(f.proxy.addr)
	waitFor(t, "the failed clear to be applied", func() bool { return !f.markedInDatabase("gone") })
	ResetStore() // closes the store, which drains its writer and stops its retries
	if !f.markedInDatabase("recreated") {
		t.Fatal("the retried clear removed the mark of a resource that exists again")
	}
}

// The reconnect clears the marks of resources deleted while no store was
// installed. When the database fails again during that clear, the clear has to
// be applied once it answers.
func TestReconnectClearThatFailsIsAppliedWhenTheDatabaseAnswers(t *testing.T) {
	f := newSeenMarkFixture(t, "web")
	NoteDeletedWhileUnavailable("ctx", "apps", "Deployment", "shop", "web")

	f.proxy.down()
	f.install()
	// The install only queues the clear; wait for the writer to try it and
	// keep it for a retry, or the test passes on a first attempt that worked.
	waitFor(t, "the clear to fail and be kept", func() bool { return f.unclearedCount() == 1 })
	if !f.markedInDatabase("web") {
		t.Fatal("setup: the clear reached the database with the proxy down")
	}

	f.proxy.up(f.proxy.addr)
	waitFor(t, "the failed clear to be applied", func() bool { return !f.markedInDatabase("web") })
}

// Any read that meets a lost database starts the outage log, not only the
// timeline page's: an AI tool or the Pod environment section may be the only
// thing touching the timeline on a quiet cluster.
func TestOutageFoundByAnyReadIsLogged(t *testing.T) {
	f := newSeenMarkFixture(t)
	f.install()
	out := captureLog(t)

	f.proxy.down()
	if _, err := f.store.Query(t.Context(), QueryOptions{Limit: 1}); err == nil {
		t.Fatal("Query succeeded with the database gone")
	}
	waitForLog(t, out, "[timeline] lost the connection to PostgreSQL")
}

// closeCallsBack is a store whose Close reads the global store, as the
// PostgreSQL store's writer does when a write fails while it drains.
type closeCallsBack struct {
	EventStore
	closed chan struct{}
}

func (s closeCallsBack) Close() error {
	GetStore()
	close(s.closed)
	return nil
}

// ResetStore must not hold the store lock while the store closes, or a close
// that calls back into GetStore never returns and a context switch hangs.
func TestResetStoreLetsTheClosingStoreReadTheGlobalStore(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	store := closeCallsBack{EventStore: NewMemoryStore(10), closed: make(chan struct{})}
	setGlobalStore(store)

	done := make(chan struct{})
	go func() {
		ResetStore()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ResetStore deadlocked: the closing store could not read the global store")
	}
	select {
	case <-store.closed:
	default:
		t.Fatal("ResetStore did not close the store")
	}
}

// A mark queued before the outage and still waiting in the writer's queue
// must not flush after the retried clear and bring the deleted resource's row
// back: the retry goes through the same queue, behind it.
func TestRetriedClearIsAppliedAfterAMarkAlreadyQueued(t *testing.T) {
	f := newSeenMarkFixture(t)
	// Never installed, so closed here, before the test schema is dropped.
	t.Cleanup(func() { _ = f.store.Close() })
	key := SeenResourceKey("ctx", "apps", "Deployment", "shop", "web")

	// A store whose writer is not running yet, so the queue holds what is put
	// in it until the writer starts.
	held := &PostgresStore{
		db:            f.store.db,
		target:        f.store.target,
		seenResources: map[string]bool{},
		unclearedSeen: map[string]struct{}{key: {}},
		seenWrites:    make(chan seenResourceWrite, seenWriteQueueDepth),
		quit:          make(chan struct{}),
	}
	held.seenWrites <- seenResourceWrite{key: key}

	if err := held.applyUnclearedSeen(); err != nil {
		t.Fatalf("applyUnclearedSeen: %v", err)
	}
	held.wg.Add(1)
	go func() {
		defer held.wg.Done()
		held.runSeenWriter()
	}()
	close(held.quit) // drains the queue in order, then stops
	held.wg.Wait()

	if f.markedInDatabase("web") {
		t.Fatal("the queued mark flushed after the retried clear; the deleted resource still reads as seen")
	}
}

// A clear the database rejects while it is still answering (here a lock
// timeout) starts no outage watch. The store's own retry has to apply it once
// the lock is gone.
func TestClearRejectedWhileTheDatabaseAnswersIsRetried(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	prevInitial, prevMax := seenClearRetryInitialInterval, seenClearRetryMaxInterval
	seenClearRetryInitialInterval, seenClearRetryMaxInterval = 50*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { seenClearRetryInitialInterval, seenClearRetryMaxInterval = prevInitial, prevMax })

	dsn := testPostgresDSN(t)
	seed, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	seed.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	if err := seed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	direct, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open direct connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })

	impatient, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	q := impatient.Query()
	q.Set("lock_timeout", "200")
	impatient.RawQuery = q.Encode()
	store, err := NewPostgresStore(impatient.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})
	t.Cleanup(ResetStore) // before the test schema is dropped

	tx, err := direct.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec("LOCK TABLE radar_timeline_seen_resources IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock: %v", err)
	}
	store.ClearResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	// Read inside the locking transaction: retries keep failing until it ends,
	// so the row is still there however many have run.
	key := []byte(SeenResourceKey("ctx", "apps", "Deployment", "shop", "web"))
	var rows int
	if err := tx.QueryRow("SELECT COUNT(*) FROM radar_timeline_seen_resources WHERE resource_key = $1", key).Scan(&rows); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read seen mark: %v", err)
	}
	if rows != 1 {
		_ = tx.Rollback()
		t.Fatal("setup: the clear went through despite the lock")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	waitFor(t, "the rejected clear to be retried", func() bool {
		var n int
		if err := direct.QueryRow("SELECT COUNT(*) FROM radar_timeline_seen_resources WHERE resource_key = $1", key).Scan(&n); err != nil {
			t.Fatalf("read seen mark: %v", err)
		}
		return n == 0
	})
}

// The clears kept for a retry are bounded, and running past the bound is
// logged once rather than growing without limit through a long outage.
func TestClearsKeptForARetryAreBounded(t *testing.T) {
	store := &PostgresStore{seenResources: map[string]bool{}, unclearedSeen: map[string]struct{}{}}
	out := captureLog(t)
	keys := make([]string, maxUnclearedSeen+10)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	store.noteUncleared(keys[:maxUnclearedSeen+5])
	store.noteUncleared(keys[maxUnclearedSeen+5:])
	if n := len(store.unclearedSeen); n != maxUnclearedSeen {
		t.Fatalf("%d clears kept, want the bound %d", n, maxUnclearedSeen)
	}
	if n := strings.Count(out.String(), "seen-resource clears are waiting for PostgreSQL"); n != 1 {
		t.Fatalf("overflow logged %d times, want 1:\n%s", n, out.String())
	}
}

// A retried clear racing a new mark of the same resource must never be queued
// after that mark, or the writer deletes the mark the recreated resource needs.
// Either the retry sees the mark and drops the clear, or the mark queues behind
// it.
func TestRetriedClearNeverQueuesAfterANewerMark(t *testing.T) {
	key := SeenResourceKey("ctx", "apps", "Deployment", "shop", "web")
	for round := range 2000 {
		store := &PostgresStore{
			seenResources: map[string]bool{},
			unclearedSeen: map[string]struct{}{key: {}},
			seenWrites:    make(chan seenResourceWrite, 4),
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = store.applyUnclearedSeen() }()
		go func() { defer wg.Done(); store.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "web") }()
		wg.Wait()
		close(store.seenWrites)

		clearAt, markAt := -1, -1
		i := 0
		for w := range store.seenWrites {
			if w.delete {
				clearAt = i
			} else {
				markAt = i
			}
			i++
		}
		if clearAt >= 0 && clearAt > markAt {
			t.Fatalf("round %d: the retried clear was queued after the newer mark (clear at %d, mark at %d)", round, clearAt, markAt)
		}
	}
}

// The outage watch retries its own store's clears only. The list of deletes
// made while no store was installed belongs to the current context, and a
// watch on a store from before a context switch must leave it alone.
func TestOutageRecoveryLeavesTheCurrentContextsDeletesAlone(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	watched := &PostgresStore{
		seenResources: map[string]bool{},
		unclearedSeen: map[string]struct{}{},
		seenWrites:    make(chan seenResourceWrite, 4),
		quit:          make(chan struct{}),
	}
	setGlobalStore(watched)
	t.Cleanup(func() { setGlobalStore(nil) }) // never opened, so never closed
	// As if the next context had noted a delete while its store was down.
	deletedWhileUnavailableMu.Lock()
	deletedWhileUnavailable[SeenResourceKey("next", "apps", "Deployment", "shop", "web")] = struct{}{}
	deletedWhileUnavailableMu.Unlock()

	applyUnclearedSeen(watched)
	if n := DeletedWhileUnavailableCountForTest(); n != 1 {
		t.Fatalf("%d delete(s) left on the list after the outage watch, want 1: it took the current context's deletes", n)
	}
}

// A clear still waiting for its retry when Radar shuts down gets one last
// attempt, so the restart does not load its stale mark.
func TestClosingTheStoreAppliesClearsStillWaitingForARetry(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	prevInitial, prevMax := seenClearRetryInitialInterval, seenClearRetryMaxInterval
	seenClearRetryInitialInterval, seenClearRetryMaxInterval = time.Hour, time.Hour // no retry before the close
	t.Cleanup(func() { seenClearRetryInitialInterval, seenClearRetryMaxInterval = prevInitial, prevMax })

	dsn := testPostgresDSN(t)
	seed, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	seed.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	if err := seed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	direct, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open direct connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })

	impatient, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	q := impatient.Query()
	q.Set("lock_timeout", "200")
	impatient.RawQuery = q.Encode()
	store, err := NewPostgresStore(impatient.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})
	t.Cleanup(ResetStore) // before the test schema is dropped

	tx, err := direct.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec("LOCK TABLE radar_timeline_seen_resources IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock: %v", err)
	}
	store.ClearResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	ResetStore()
	var n int
	key := []byte(SeenResourceKey("ctx", "apps", "Deployment", "shop", "web"))
	if err := direct.QueryRow("SELECT COUNT(*) FROM radar_timeline_seen_resources WHERE resource_key = $1", key).Scan(&n); err != nil {
		t.Fatalf("read seen mark: %v", err)
	}
	if n != 0 {
		t.Fatal("the clear waiting for its retry was dropped at close; the restart loads its stale mark")
	}
}

// A retry that runs once the store has started closing must leave its clears
// for the close's last attempt: the writer has stopped, and a clear queued to
// it then would be dropped.
func TestRetryAfterCloseLeavesClearsForTheLastAttempt(t *testing.T) {
	key := SeenResourceKey("ctx", "apps", "Deployment", "shop", "web")
	store := &PostgresStore{
		seenResources: map[string]bool{},
		unclearedSeen: map[string]struct{}{key: {}},
		seenWrites:    make(chan seenResourceWrite, 4),
		quit:          make(chan struct{}),
	}
	close(store.quit) // as Close does, before the writer drains

	if err := store.applyUnclearedSeen(); err == nil {
		t.Fatal("applyUnclearedSeen queued clears on a closing store")
	}
	if len(store.seenWrites) != 0 {
		t.Fatal("a clear was queued to a writer that has stopped")
	}
	if _, kept := store.unclearedSeen[key]; !kept {
		t.Fatal("the clear left the set, so the close's last attempt misses it")
	}
}
