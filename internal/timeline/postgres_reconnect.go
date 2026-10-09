package timeline

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

var (
	postgresReconnectInitialInterval = 5 * time.Second
	postgresReconnectMaxInterval     = time.Minute
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

// recordPostgresUnavailable notes why the timeline has no store.
func recordPostgresUnavailable(err error) {
	postgresUnavailableReason.Store(err.Error())
	log.Printf("[timeline] PostgreSQL timeline unavailable, cluster views are unaffected: %v", err)
}

// recordWorkerUnavailable records a worker's failed attempt. A retired worker
// must not overwrite the reason: its failure describes a connection nobody is
// waiting on any more. The ownership check and the write share the lock that
// stopPostgresReconnect takes, so a reset cannot land between them.
func recordWorkerUnavailable(err error, quit chan struct{}) {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != quit {
		return
	}
	recordPostgresUnavailable(err)
}

// installPostgresStore publishes an opened store. The first attempt and every
// retry go through here, so the retention loop, the observation start and the
// log line cannot drift between the two paths.
func installPostgresStore(store *PostgresStore, cfg StoreConfig) {
	// Observation starts when events begin landing. Marking it at process start
	// would claim coverage for the window the store was down. It is set before
	// the store is published so no reader sees a store without its epoch.
	observationStartNanos.Store(time.Now().UnixNano())
	setGlobalStore(store)
	postgresUnavailableReason.Store("")
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
		recordPostgresUnavailable(err)
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
				recordWorkerUnavailable(err, quit)
				interval = min(interval*2, maxInterval)
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
}

// TimelineUnavailableReason reports why no timeline store is installed, or an
// empty string when one is.
func TimelineUnavailableReason() string {
	reason, _ := postgresUnavailableReason.Load().(string)
	return reason
}
