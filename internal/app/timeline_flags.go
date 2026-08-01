package app

import "flag"

// RegisterTimelinePostgresDSNFlag registers the runtime-only PostgreSQL DSN flag.
func RegisterTimelinePostgresDSNFlag(flags *flag.FlagSet, getenv func(string) string) *string {
	return flags.String(
		"timeline-postgres-dsn",
		getenv("RADAR_TIMELINE_POSTGRES_DSN"),
		"PostgreSQL timeline connection string (default: RADAR_TIMELINE_POSTGRES_DSN; never persisted)",
	)
}
