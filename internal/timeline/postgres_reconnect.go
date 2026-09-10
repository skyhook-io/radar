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

// openPostgresStore installs a PostgreSQL store, reporting whether it succeeded.
// Both the initial attempt and every reconnect attempt go through here so the
// retention loop and the log line cannot drift between the two paths.
func openPostgresStore(cfg StoreConfig) bool {
	store, err := NewPostgresStore(cfg.DSN)
	if err != nil {
		postgresUnavailableReason.Store(err.Error())
		log.Printf("[timeline] PostgreSQL timeline unavailable, cluster views are unaffected: %v", err)
		return false
	}
	setGlobalStore(store)
	postgresUnavailableReason.Store("")
	if cfg.RetentionAge > 0 {
		store.StartCleanupLoop(cfg.RetentionAge, time.Hour, 0)
		log.Printf("Initialized PostgreSQL timeline store (retention: %s)", cfg.RetentionAge)
	} else {
		log.Printf("Initialized PostgreSQL timeline store (retention: disabled - events table will grow unbounded)")
	}
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
			if openPostgresStore(cfg) {
				postgresReconnectMu.Lock()
				if postgresReconnectQuit == quit {
					postgresReconnectQuit = nil
				}
				postgresReconnectMu.Unlock()
				return
			}
			interval = min(interval*2, postgresReconnectMaxInterval)
		}
	}()
}

// stopPostgresReconnect halts the worker and waits for nothing: the worker only
// ever installs a store after checking it still owns the quit channel.
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
