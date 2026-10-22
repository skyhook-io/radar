package timeline

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

var (
	postgresReconnectInitialInterval = 5 * time.Second
	postgresReconnectMaxInterval     = time.Minute
)

// maxDeletedWhileUnavailable bounds how many deletes an outage remembers. Past
// it, a resource deleted during the outage and later recreated under the same
// name may not have its creation recorded.
const maxDeletedWhileUnavailable = 100_000

var (
	deletedWhileUnavailableMu       sync.Mutex
	deletedWhileUnavailable         = map[string]struct{}{}
	deletedWhileUnavailableOverflow bool
)

var (
	// Guards the worker's lifetime. ResetStore retires the current worker before
	// a new InitStore can start one, so a context switch cannot have the
	// previous context's connection installed over the new one.
	postgresReconnectMu   sync.Mutex
	postgresReconnectQuit chan struct{}

	// Why no store is installed, for diagnostics. Empty once one is.
	postgresUnavailableReason atomic.Value // string

	// Test hook: replaced to drive the worker without a real database.
	postgresDialForTest func(StoreConfig) (*PostgresStore, error)
)

// dialPostgres opens a connection without publishing it, so a caller that has
// lost its claim can close the result instead of installing it.
func dialPostgres(cfg StoreConfig) (*PostgresStore, error) {
	if postgresDialForTest != nil {
		return postgresDialForTest(cfg)
	}
	return NewPostgresStore(cfg.DSN)
}

// recordPostgresUnavailable notes why the timeline has no store. The log line
// is what the timeline page points operators to, so it names the outcome, the
// database, the next attempt and the error. The database is named separately
// because a migration or permission error does not name it.
func recordPostgresUnavailable(err error, target string, retryIn time.Duration) {
	postgresUnavailableReason.Store(err.Error())
	log.Printf("[timeline] can't connect to PostgreSQL (%s); the timeline is not recording changes and the rest of Radar is unaffected. Retrying in %s: %v", target, retryIn, err)
}

// recordWorkerUnavailable records a worker's failed attempt. A retired worker
// must not overwrite the reason: its failure describes a connection nobody is
// waiting on any more. The ownership check and the write share the lock that
// stopPostgresReconnect takes, so a reset cannot land between them.
func recordWorkerUnavailable(err error, target string, retryIn time.Duration, quit chan struct{}) {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != quit {
		return
	}
	recordPostgresUnavailable(err, target, retryIn)
}

// installPostgresStore publishes an opened store. The first attempt and every
// retry go through here, so the retention loop, the observation start and the
// log line cannot drift between the two paths.
func installPostgresStore(store *PostgresStore, cfg StoreConfig) {
	// Clear the marks of resources deleted during the outage before the store
	// takes events, then once more after: a delete that lands between the two
	// went to the list, not to the store.
	forgetDeletedWhileUnavailable(store)
	// Observation starts when events begin landing. Marking it at process start
	// would claim coverage for the window the store was down. It is set before
	// the store is published so no reader sees a store without its epoch.
	observationStartNanos.Store(time.Now().UnixNano())
	wasUnavailable := TimelineUnavailableReason() != ""
	setGlobalStore(store)
	postgresUnavailableReason.Store("")
	forgetDeletedWhileUnavailable(store)
	if wasUnavailable {
		log.Printf("[timeline] connected to PostgreSQL (%s); the timeline is recording changes. Changes made while it was unavailable were not recorded", store.target)
	}
	if cfg.RetentionAge > 0 {
		store.StartCleanupLoop(cfg.RetentionAge, time.Hour, 0)
		log.Printf("Initialized PostgreSQL timeline store (retention: %s)", cfg.RetentionAge)
	} else {
		log.Printf("Initialized PostgreSQL timeline store (retention: disabled - events table will grow unbounded)")
	}
}

// openPostgresStore dials and installs in one step, for the first attempt where
// no other worker is competing for the claim.
func openPostgresStore(cfg StoreConfig) bool {
	store, err := dialPostgres(cfg)
	if err != nil {
		recordPostgresUnavailable(err, postgresTarget(cfg.DSN), postgresReconnectInitialInterval)
		return false
	}
	installPostgresStore(store, cfg)
	return true
}

// startPostgresReconnect retries until the database answers or the store is
// reset. Without it the timeline stays down for the life of the process, since
// nothing else reopens it.
func startPostgresReconnect(cfg StoreConfig) {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != nil {
		return
	}
	quit := make(chan struct{})
	postgresReconnectQuit = quit
	// Captured under the lock: the worker must not read these again once it is
	// running, or a caller changing them races the loop.
	interval, maxInterval := postgresReconnectInitialInterval, postgresReconnectMaxInterval
	dial := dialPostgres

	go func() {
		for {
			select {
			case <-quit:
				return
			case <-time.After(interval):
			}

			store, err := dial(cfg)
			if err != nil {
				interval = min(interval*2, maxInterval)
				recordWorkerUnavailable(err, postgresTarget(cfg.DSN), interval, quit)
				continue
			}
			// Claim and install under one lock. A ResetStore landing between the
			// dial and the install has already retired this worker, and
			// publishing here would undo the reset.
			postgresReconnectMu.Lock()
			if postgresReconnectQuit != quit {
				postgresReconnectMu.Unlock()
				if closeErr := store.Close(); closeErr != nil {
					log.Printf("[timeline] closing a superseded PostgreSQL connection: %v", closeErr)
				}
				return
			}
			installPostgresStore(store, cfg)
			postgresReconnectQuit = nil
			postgresReconnectMu.Unlock()
			return
		}
	}()
}

// stopPostgresReconnect retires the worker. It claims and installs under the
// lock this takes, so once this returns no store can appear from it.
func stopPostgresReconnect() {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != nil {
		close(postgresReconnectQuit)
		postgresReconnectQuit = nil
	}
	postgresUnavailableReason.Store("")
	takeDeletedWhileUnavailable()
	stopPostgresOutageWatch()
}

// TimelineUnavailableReason reports why no timeline store is installed, or an
// empty string when one is.
func TimelineUnavailableReason() string {
	reason, _ := postgresUnavailableReason.Load().(string)
	return reason
}

// StoreMissingError says why no timeline store is installed, for a caller
// that has to tell "nothing changed" from "history could not be checked".
func StoreMissingError() error {
	if reason := TimelineUnavailableReason(); reason != "" {
		return fmt.Errorf("timeline history is unavailable because Radar can't connect to its PostgreSQL database: %s", reason)
	}
	return errors.New("timeline store not initialized")
}

// Unavailable reports why the timeline cannot record or serve history right
// now, or nil when it can. A PostgreSQL store is pinged: a database that goes
// away after a good start leaves the store in place, and only a round trip
// shows the outage. Any ping failure counts, a refused connection or a
// database that stops answering alike.
func Unavailable(ctx context.Context) error {
	store := GetStore()
	if store == nil {
		if reason := TimelineUnavailableReason(); reason != "" {
			return errors.New(reason)
		}
		return nil
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		return nil
	}
	err := pg.Ping(ctx)
	// The alerting metric and diagnostics check through here, often with no
	// timeline traffic at all, so a failure here starts the outage log too.
	NoteDatabaseError(pg, err)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("PostgreSQL did not answer in time: %w", err)
	}
	return err
}

// NoteDeletedWhileUnavailable remembers a resource deleted while no store was
// installed. Its seen mark is still in the database, and once the store comes
// back the mark would make a new resource with the same name look already seen,
// so its creation would be dropped. The reconnected store clears these marks.
func NoteDeletedWhileUnavailable(clusterContext, group, kind, namespace, name string) {
	deletedWhileUnavailableMu.Lock()
	if len(deletedWhileUnavailable) >= maxDeletedWhileUnavailable {
		deletedWhileUnavailableOverflow = true
	} else {
		deletedWhileUnavailable[SeenResourceKey(clusterContext, group, kind, namespace, name)] = struct{}{}
	}
	deletedWhileUnavailableMu.Unlock()

	// The caller saw no store, but one may have been installed since, and its
	// last pass over this list may already have run. installPostgresStore
	// publishes the store before that pass, so either the pass sees this entry
	// or this check sees the store.
	if pg, ok := GetStore().(*PostgresStore); ok {
		forgetDeletedWhileUnavailable(pg)
	}
}

// DeletedWhileUnavailableCountForTest reports how many deletes are waiting to
// have their seen marks cleared.
func DeletedWhileUnavailableCountForTest() int {
	deletedWhileUnavailableMu.Lock()
	defer deletedWhileUnavailableMu.Unlock()
	return len(deletedWhileUnavailable)
}

// takeDeletedWhileUnavailable empties the list and reports whether it overflowed.
func takeDeletedWhileUnavailable() ([]string, bool) {
	deletedWhileUnavailableMu.Lock()
	defer deletedWhileUnavailableMu.Unlock()
	keys := make([]string, 0, len(deletedWhileUnavailable))
	for k := range deletedWhileUnavailable {
		keys = append(keys, k)
	}
	overflow := deletedWhileUnavailableOverflow
	deletedWhileUnavailable = map[string]struct{}{}
	deletedWhileUnavailableOverflow = false
	return keys, overflow
}

func forgetDeletedWhileUnavailable(store *PostgresStore) {
	keys, overflow := takeDeletedWhileUnavailable()
	if overflow {
		log.Printf("[timeline] more than %d resources were deleted while PostgreSQL was unreachable; "+
			"one of the others recreated under the same name may not have its creation recorded", maxDeletedWhileUnavailable)
	}
	// Runs with no new keys too: it also queues the clears an earlier attempt
	// failed to apply. A clear that fails in the writer is kept and retried.
	if err := store.forgetSeen(keys); err != nil {
		log.Printf("[timeline] seen marks of resources deleted while PostgreSQL was unreachable are not cleared yet: %v", err)
	}
}

// postgresOutageCheckInterval is how often a store that lost its database is
// pinged until it answers. A variable so tests need not wait.
var postgresOutageCheckInterval = 5 * time.Second

var (
	// The store being watched after it lost its database, and the channel that
	// stops the watch. Both nil when there is no outage.
	postgresOutageMu    sync.Mutex
	postgresOutageStore *PostgresStore
	postgresOutageQuit  chan struct{}
)

// NoteDatabaseError reports a failed read or write against the installed
// store. When the failure means PostgreSQL went away after a good start, it
// logs the outage once and watches the database until it answers, then logs
// the recovery. The timeline page sends operators to these lines, so they have
// to appear even on a quiet cluster where nothing else touches the database.
// A query the server rejected is not an outage and is ignored. Anything else
// is confirmed with a ping first: one dropped pooled connection, or one slow
// query, is not an outage either.
func NoteDatabaseError(store EventStore, err error) {
	pg, ok := store.(*PostgresStore)
	if !ok || err == nil {
		return
	}
	if !DatabaseUnreachable(err) && !errors.Is(err, context.DeadlineExceeded) {
		return
	}

	postgresOutageMu.Lock()
	if postgresOutageStore != nil || GetStore() != store {
		postgresOutageMu.Unlock()
		return
	}
	quit := make(chan struct{})
	postgresOutageStore, postgresOutageQuit = pg, quit
	interval := postgresOutageCheckInterval
	postgresOutageMu.Unlock()

	go watchPostgresOutage(pg, err, interval, quit)
}

func watchPostgresOutage(pg *PostgresStore, cause error, interval time.Duration, quit chan struct{}) {
	ping := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), postgresPingTimeout)
		defer cancel()
		return pg.Ping(ctx)
	}
	if ping() == nil {
		endPostgresOutage(quit, "")
		applyUnclearedSeen(pg)
		return
	}
	since := time.Now()
	if !logIfStillWatching(pg, quit, fmt.Sprintf("[timeline] lost the connection to PostgreSQL (%s); the timeline is not recording changes, and the rest of Radar is unaffected. Checking again in %s, and until it answers: %v",
		pg.target, interval, cause)) {
		return
	}
	for {
		select {
		case <-quit:
			return
		case <-time.After(interval):
		}
		// A store closed by a context switch is not coming back.
		if GetStore() != EventStore(pg) {
			endPostgresOutage(quit, "")
			return
		}
		if ping() == nil {
			endPostgresOutage(quit, fmt.Sprintf("[timeline] PostgreSQL (%s) is answering again after %s; the timeline is recording changes again. Changes made in between were not recorded",
				pg.target, time.Since(since).Round(time.Second)))
			applyUnclearedSeen(pg)
			return
		}
	}
}

// applyUnclearedSeen retries the store's own failed seen-mark clears. It does
// not drain deletedWhileUnavailable: that list fills only while no store is
// installed and belongs to whichever context is current, which after a context
// switch is no longer this store's.
func applyUnclearedSeen(pg *PostgresStore) {
	if GetStore() != EventStore(pg) {
		return
	}
	if err := pg.applyUnclearedSeen(); err != nil {
		log.Printf("[timeline] seen marks of deleted resources are not cleared yet: %v", err)
	}
}

// logIfStillWatching logs line only if this watch has not been stopped and its
// store is still the installed one, checked under the lock that
// stopPostgresOutageWatch takes, so a context switch cannot slip between the
// check and the line. It reports whether the watch should go on.
func logIfStillWatching(pg *PostgresStore, quit chan struct{}, line string) bool {
	postgresOutageMu.Lock()
	defer postgresOutageMu.Unlock()
	if postgresOutageQuit != quit {
		return false
	}
	if GetStore() != EventStore(pg) {
		postgresOutageStore, postgresOutageQuit = nil, nil
		return false
	}
	log.Print(line)
	return true
}

// endPostgresOutage clears the watch if it is still this one's, and logs line
// when it is not empty.
func endPostgresOutage(quit chan struct{}, line string) {
	postgresOutageMu.Lock()
	defer postgresOutageMu.Unlock()
	if postgresOutageQuit != quit {
		return
	}
	postgresOutageStore, postgresOutageQuit = nil, nil
	if line != "" {
		log.Print(line)
	}
}

// stopPostgresOutageWatch ends any watch without logging a recovery: the
// store it watched is being replaced.
func stopPostgresOutageWatch() {
	postgresOutageMu.Lock()
	defer postgresOutageMu.Unlock()
	if postgresOutageQuit != nil {
		close(postgresOutageQuit)
	}
	postgresOutageStore, postgresOutageQuit = nil, nil
}
