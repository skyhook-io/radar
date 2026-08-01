package app

import (
	"flag"
	"testing"
)

func TestRegisterTimelinePostgresDSNFlag(t *testing.T) {
	const (
		envDSN = "postgres://from-env.example/radar"
		cliDSN = "postgres://from-cli.example/radar"
		usage  = "PostgreSQL timeline connection string (default: RADAR_TIMELINE_POSTGRES_DSN; never persisted)"
	)

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	gotEnvKey := ""
	value := RegisterTimelinePostgresDSNFlag(flags, func(key string) string {
		gotEnvKey = key
		return envDSN
	})

	if gotEnvKey != "RADAR_TIMELINE_POSTGRES_DSN" {
		t.Fatalf("getenv key = %q, want RADAR_TIMELINE_POSTGRES_DSN", gotEnvKey)
	}
	registered := flags.Lookup("timeline-postgres-dsn")
	if registered == nil {
		t.Fatal("--timeline-postgres-dsn was not registered")
	}
	if registered.Usage != usage {
		t.Fatalf("--timeline-postgres-dsn usage = %q, want %q", registered.Usage, usage)
	}
	if *value != envDSN {
		t.Fatalf("default DSN = %q, want environment DSN %q", *value, envDSN)
	}

	if err := flags.Parse([]string{"--timeline-postgres-dsn", cliDSN}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *value != cliDSN {
		t.Fatalf("CLI DSN = %q, want override %q", *value, cliDSN)
	}
}
