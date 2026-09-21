package timeline

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInitStoreRejectsPostgresWithoutDSN(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	err := InitStore(StoreConfig{Type: StoreTypePostgres})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL timeline store requires a DSN") {
		t.Fatalf("InitStore error = %v, want missing DSN error", err)
	}
	if GetStore() != nil {
		t.Fatal("InitStore configured a fallback store for PostgreSQL without DSN")
	}
}

func TestInitStoreRejectsPostgresOnConnectionFailure(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	err := InitStore(StoreConfig{
		Type: StoreTypePostgres,
		DSN:  "postgres://radar:secret@127.0.0.1:1/radar?connect_timeout=1",
	})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL timeline store failed to initialize") {
		t.Fatalf("InitStore error = %v, want PostgreSQL initialization failure", err)
	}
	if GetStore() != nil {
		t.Fatal("InitStore configured a fallback store for failed PostgreSQL")
	}
}

func TestInitStoreRepeatsInitializationErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  StoreConfig
		want string
	}{
		{
			name: "postgres missing dsn",
			cfg:  StoreConfig{Type: StoreTypePostgres},
			want: "PostgreSQL timeline store requires a DSN",
		},
		{
			name: "postgres connection failure",
			cfg: StoreConfig{
				Type: StoreTypePostgres,
				DSN:  "postgres://radar:secret@127.0.0.1:1/radar?connect_timeout=1",
			},
			want: "PostgreSQL timeline store failed to initialize",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ResetStore()
			t.Cleanup(ResetStore)

			for call := 1; call <= 2; call++ {
				err := InitStore(tc.cfg)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("InitStore call %d error = %v, want %q", call, err, tc.want)
				}
				if GetStore() != nil {
					t.Fatalf("InitStore call %d configured a store after initialization failure", call)
				}
			}
		})
	}
}

func TestInitStoreConcurrentWithResetPreservesPostgresError(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	cfg := StoreConfig{
		Type: StoreTypePostgres,
		DSN:  "postgres://radar:secret@127.0.0.1:1/radar?connect_timeout=1",
	}

	for iteration := 0; iteration < 200; iteration++ {
		ResetStore()

		start := make(chan struct{})
		var wg sync.WaitGroup
		var initErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			initErr = InitStore(cfg)
		}()
		go func() {
			defer wg.Done()
			<-start
			ResetStore()
		}()

		close(start)
		wg.Wait()

		if initErr == nil {
			t.Fatalf("iteration %d: concurrent InitStore reported nil for failed PostgreSQL", iteration)
		}
		if err := InitStore(cfg); err == nil {
			t.Fatalf("iteration %d: final InitStore reported nil for failed PostgreSQL", iteration)
		}
		if GetStore() != nil {
			t.Fatalf("iteration %d: failed PostgreSQL configured a store", iteration)
		}
	}
}

func TestInitStoreInitializesPostgresStore(t *testing.T) {
	baseDSN := testPostgresDSN(t)
	ResetStore()
	t.Cleanup(ResetStore)

	err := InitStore(StoreConfig{
		Type:         StoreTypePostgres,
		DSN:          baseDSN,
		RetentionAge: time.Hour,
	})
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := GetStore()
	if store == nil {
		t.Fatal("InitStore did not configure PostgreSQL store")
	}
	stats := store.Stats()
	if stats.TotalEvents != 0 {
		t.Fatalf("new store has %d events, want 0", stats.TotalEvents)
	}
}

func TestInitStorePreservesSQLiteDegradation(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)

	err := InitStore(StoreConfig{
		Type:    StoreTypeSQLite,
		Path:    t.TempDir(),
		MaxSize: 77,
	})
	if err != nil {
		t.Fatalf("InitStore returned SQLite open error instead of degrading: %v", err)
	}
	store := GetStore()
	if store == nil {
		t.Fatal("InitStore did not install degraded memory store")
	}
	if stats := store.Stats(); !stats.Degraded {
		t.Fatalf("store stats = %+v, want degraded SQLite fallback", stats)
	}
}
