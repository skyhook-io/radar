package timeline

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const (
	postgresReconnectInitialInterval = 5 * time.Second
	postgresReconnectMaxInterval     = time.Minute
)

var (
	// Guards the reconnect worker's lifetime. ResetStore stops the worker before
	// a new InitStore can start one, so a context switch cannot leave the
	// previous context's worker installing its store over the new one.
	postgresReconnectMu   sync.Mutex
	postgresReconnectQuit chan struct{}

	// Why the timeline is unavailable, for diagnostics. Empty once a store is
	// installed.
	postgresUnavailableReason atomic.Value // string
)

// dialPostgres opens a connection without publishing it, so a caller that may
// have lost its claim can close the result instead of installing it.
func dialPostgres(cfg StoreConfig) (*PostgresStore, error) {
	store, err := NewPostgresStore(cfg.DSN)
	if err != nil {
		postgresUnavailableReason.Store(err.Error())
		log.Printf("[timeline] PostgreSQL timeline unavailable, cluster views are unaffected: %v", err)
		return nil, err
	}
	return store, nil
}

// installPostgresStore publishes an opened store. Both the initial attempt and
// every reconnect go through here so the retention loop, the observation start
// and the log line cannot drift between the two paths.
func installPostgresStore(store *PostgresStore, cfg StoreConfig) {
	setGlobalStore(store)
	postgresUnavailableReason.Store("")
	// Observation starts when events actually begin landing. Marking it at
	// process start would claim coverage for the window the store was down.
	observationStartNanos.Store(time.Now().UnixNano())
	if cfg.RetentionAge > 0 {
		store.StartCleanupLoop(cfg.RetentionAge, time.Hour, 0)
		log.Printf("Initialized PostgreSQL timeline store (retention: %s)", cfg.RetentionAge)
	} else {
		log.Printf("Initialized PostgreSQL timeline store (retention: disabled - events table will grow unbounded)")
	}
}

// openPostgresStore dials and installs in one step, for the initial attempt
// where no other worker can be competing for the claim.
func openPostgresStore(cfg StoreConfig) bool {
	store, err := dialPostgres(cfg)
	if err != nil {
		return false
	}
	installPostgresStore(store, cfg)
	return true
}

// startPostgresReconnect retries the connection until it succeeds or the store
// is reset. Only one worker runs at a time.
func startPostgresReconnect(cfg StoreConfig) {
	postgresReconnectMu.Lock()
	defer postgresReconnectMu.Unlock()
	if postgresReconnectQuit != nil {
		return
	}
	quit := make(chan struct{})
	postgresReconnectQuit = quit

	go func() {
		interval := postgresReconnectInitialInterval
		for {
			select {
			case <-quit:
				return
			case <-time.After(interval):
			}
			store, err := dialPostgres(cfg)
			if err != nil {
				interval = min(interval*2, postgresReconnectMaxInterval)
				continue
			}
			// Claim and install under one lock. A ResetStore that lands between
			// the dial and the install has already retired this worker, and
			// publishing here would put the previous context's connection back
			// after the new one was set up.
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

// stopPostgresReconnect retires the worker. The worker claims and installs
// under the same lock this takes, so once this returns no store can appear from
// the retired worker.
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
// empty string when one is. Diagnostics surface it so an operator is not left
// guessing why history is missing.
func TimelineUnavailableReason() string {
	reason, _ := postgresUnavailableReason.Load().(string)
	return reason
}
