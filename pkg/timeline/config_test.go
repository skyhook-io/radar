package timeline

import "testing"

func TestPostgresStoreConfig(t *testing.T) {
	if StoreTypePostgres != StoreType("postgres") {
		t.Fatalf("StoreTypePostgres = %q, want postgres", StoreTypePostgres)
	}

	const dsn = "postgres://radar@example.test/radar"
	cfg := StoreConfig{Type: StoreTypePostgres, DSN: dsn}
	if cfg.DSN != dsn {
		t.Fatalf("StoreConfig.DSN = %q, want %q", cfg.DSN, dsn)
	}
}
