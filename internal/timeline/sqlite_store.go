package timeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver

	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

// SQLiteStore is a persistent implementation of EventStore using SQLite.
// Suitable for local development with persistence and in-cluster use with PVC.
type SQLiteStore struct {
	db            *sql.DB
	seenResources map[string]bool
	seenMu        sync.RWMutex
	filterCache   map[string]*CompiledFilter
	cacheMu       sync.RWMutex
	path          string
}

// NewSQLiteStore creates a new SQLite-backed event store.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// SQLite only supports one writer at a time - limit connections
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Configure SQLite for performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",  // 64MB cache
		"PRAGMA busy_timeout=10000", // 10 second timeout
		"PRAGMA temp_store=MEMORY",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: failed to set %s: %v", pragma, err)
		}
	}

	store := &SQLiteStore{
		db:            db,
		seenResources: make(map[string]bool),
		filterCache:   make(map[string]*CompiledFilter),
		path:          dbPath,
	}

	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Clear seen_resources on startup so historical events get re-extracted
	// The events table will handle deduplication via INSERT OR IGNORE
	if _, err := db.Exec("DELETE FROM seen_resources"); err != nil {
		log.Printf("Warning: failed to clear seen resources: %v", err)
	}

	return store, nil
}

// initSchema creates the database tables if they don't exist
func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		source TEXT NOT NULL,
		kind TEXT NOT NULL,
		namespace TEXT,
		name TEXT NOT NULL,
		uid TEXT,
		event_type TEXT NOT NULL,
		reason TEXT,
		message TEXT,
		diff_json TEXT,
		health_state TEXT,
		owner_kind TEXT,
		owner_name TEXT,
		labels_json TEXT,
		count INTEGER DEFAULT 0,
		correlation_id TEXT,
		created_at TEXT DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind);
	CREATE INDEX IF NOT EXISTS idx_events_namespace ON events(namespace);
	CREATE INDEX IF NOT EXISTS idx_events_name ON events(name);
	CREATE INDEX IF NOT EXISTS idx_events_source ON events(source);
	CREATE INDEX IF NOT EXISTS idx_events_owner ON events(owner_kind, owner_name, namespace);
	CREATE INDEX IF NOT EXISTS idx_events_kind_ns_name ON events(kind, namespace, name);

	CREATE TABLE IF NOT EXISTS seen_resources (
		resource_key TEXT PRIMARY KEY,
		seen_at TEXT DEFAULT (datetime('now'))
	);
	`

	_, err := s.db.Exec(schema)
	return err
}

// Append adds a single event to the store
func (s *SQLiteStore) Append(ctx context.Context, event TimelineEvent) error {
	return s.AppendBatch(ctx, []TimelineEvent{event})
}

// AppendBatch adds multiple events atomically
func (s *SQLiteStore) AppendBatch(ctx context.Context, events []TimelineEvent) error {
	if len(events) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO events (
			id, timestamp, source, kind, namespace, name, uid, event_type,
			reason, message, diff_json, health_state, owner_kind, owner_name,
			labels_json, count, correlation_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, event := range events {
		var diffJSON, labelsJSON []byte
		var ownerKind, ownerName string
		var err error

		if event.Diff != nil {
			diffJSON, err = json.Marshal(event.Diff)
			if err != nil {
				log.Printf("Warning: failed to marshal diff for event %s: %v", event.ID, err)
				// Continue without diff - it's not critical
				diffJSON = nil
			}
		}
		if event.Labels != nil {
			labelsJSON, err = json.Marshal(event.Labels)
			if err != nil {
				log.Printf("Warning: failed to marshal labels for event %s: %v", event.ID, err)
				// Continue without labels - it's not critical
				labelsJSON = nil
			}
		}
		if event.Owner != nil {
			ownerKind = event.Owner.Kind
			ownerName = event.Owner.Name
		}

		_, err = stmt.ExecContext(ctx,
			event.ID,
			event.Timestamp.Format(time.RFC3339Nano),
			string(event.Source),
			event.Kind,
			event.Namespace,
			event.Name,
			event.UID,
			string(event.EventType),
			event.Reason,
			event.Message,
			string(diffJSON),
			string(event.HealthState),
			ownerKind,
			ownerName,
			string(labelsJSON),
			event.Count,
			event.CorrelationID,
		)
		if err != nil {
			return fmt.Errorf("failed to insert event: %w", err)
		}
	}

	return tx.Commit()
}

// Query retrieves events matching the given options
func (s *SQLiteStore) Query(ctx context.Context, opts QueryOptions) ([]TimelineEvent, error) {
	// Build query
	query := strings.Builder{}
	query.WriteString("SELECT id, timestamp, source, kind, namespace, name, uid, event_type, ")
	query.WriteString("reason, message, diff_json, health_state, owner_kind, owner_name, ")
	query.WriteString("labels_json, count, correlation_id FROM events WHERE 1=1")

	var args []any

	// Apply filters
	if len(opts.Namespaces) > 0 {
		query.WriteString(" AND namespace IN (")
		for i, ns := range opts.Namespaces {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString("?")
			args = append(args, ns)
		}
		query.WriteString(")")
	}

	if len(opts.Kinds) > 0 {
		query.WriteString(" AND kind IN (")
		for i, k := range opts.Kinds {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString("?")
			args = append(args, k)
		}
		query.WriteString(")")
	}

	if !opts.Since.IsZero() {
		query.WriteString(" AND timestamp >= ?")
		args = append(args, opts.Since.Format(time.RFC3339Nano))
	}

	if !opts.Until.IsZero() {
		query.WriteString(" AND timestamp <= ?")
		args = append(args, opts.Until.Format(time.RFC3339Nano))
	}

	if len(opts.Sources) > 0 {
		query.WriteString(" AND source IN (")
		for i, src := range opts.Sources {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString("?")
			args = append(args, string(src))
		}
		query.WriteString(")")
	}

	// Order by timestamp descending
	query.WriteString(" ORDER BY timestamp DESC")

	// Apply limit
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 10000 {
		limit = 10000
	}
	query.WriteString(fmt.Sprintf(" LIMIT %d", limit))

	if opts.Offset > 0 {
		query.WriteString(fmt.Sprintf(" OFFSET %d", opts.Offset))
	}

	// Execute query
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// Get compiled filter for post-filtering
	var cf *CompiledFilter
	if opts.FilterPreset != "" {
		var filterErr error
		cf, filterErr = s.getOrCompileFilter(opts.FilterPreset)
		if filterErr != nil {
			log.Printf("Warning: failed to compile filter preset %q: %v", opts.FilterPreset, filterErr)
			// Continue without filtering - caller asked for this preset but we can't apply it
		}
	}

	events := make([]TimelineEvent, 0)
	for rows.Next() {
		event, err := s.scanEvent(rows)
		if err != nil {
			return nil, err
		}

		// Apply post-filters (for complex filters not handled in SQL)
		if cf != nil && !cf.Matches(&event) {
			continue
		}

		// Handle IncludeManaged
		// opts.IncludeManaged takes precedence; the preset can also allow managed resources.
		if event.IsManaged() && !opts.IncludeManaged && !cf.IncludesManaged() {
			continue
		}

		// Handle IncludeK8sEvents
		if !opts.IncludeK8sEvents && event.Source == SourceK8sEvent {
			continue
		}

		events = append(events, event)
	}

	return events, rows.Err()
}

// QueryGrouped retrieves events grouped according to the specified mode
func (s *SQLiteStore) QueryGrouped(ctx context.Context, opts QueryOptions) (*TimelineResponse, error) {
	startTime := time.Now()

	// Get events (with higher limit for grouping)
	queryOpts := opts
	queryOpts.Limit = min(opts.Limit*10, 5000)

	events, err := s.Query(ctx, queryOpts)
	if err != nil {
		return nil, err
	}

	if opts.GroupBy == GroupByNone {
		if len(events) > opts.Limit {
			events = events[:opts.Limit]
		}
		return &TimelineResponse{
			Ungrouped: events,
			Meta: TimelineMeta{
				TotalEvents: len(events),
				QueryTimeMs: time.Since(startTime).Milliseconds(),
				HasMore:     len(events) == opts.Limit,
			},
		}, nil
	}

	// Group events using shared implementation from pkg/timeline
	groups := pkgtimeline.GroupEvents(events, opts.GroupBy)

	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	hasMore := len(groups) > limit
	if hasMore {
		groups = groups[:limit]
	}

	return &TimelineResponse{
		Groups: groups,
		Meta: TimelineMeta{
			TotalEvents: len(events),
			GroupCount:  len(groups),
			QueryTimeMs: time.Since(startTime).Milliseconds(),
			HasMore:     hasMore,
		},
	}, nil
}

// GetEvent retrieves a single event by ID
func (s *SQLiteStore) GetEvent(ctx context.Context, id string) (*TimelineEvent, error) {
	query := `SELECT id, timestamp, source, kind, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id FROM events WHERE id = ?`

	row := s.db.QueryRowContext(ctx, query, id)
	event, err := s.scanEventRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// GetChangesForOwner retrieves changes for resources owned by the given owner
func (s *SQLiteStore) GetChangesForOwner(ctx context.Context, ownerKind, ownerNamespace, ownerName string, since time.Time, limit int) ([]TimelineEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, timestamp, source, kind, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id FROM events
		WHERE owner_kind = ? AND owner_name = ? AND namespace = ?`

	args := []any{ownerKind, ownerName, ownerNamespace}

	if !since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, since.Format(time.RFC3339Nano))
	}

	query += fmt.Sprintf(" ORDER BY timestamp DESC LIMIT %d", limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]TimelineEvent, 0)
	for rows.Next() {
		event, err := s.scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}

	return events, rows.Err()
}

// MarkResourceSeen records that a resource has been seen
func (s *SQLiteStore) MarkResourceSeen(kind, namespace, name string) {
	key := ResourceKey(kind, namespace, name)

	s.seenMu.Lock()
	s.seenResources[key] = true
	s.seenMu.Unlock()

	// Persist to database (best effort)
	_, _ = s.db.Exec("INSERT OR REPLACE INTO seen_resources (resource_key) VALUES (?)", key)
}

// IsResourceSeen checks if a resource has been seen before
func (s *SQLiteStore) IsResourceSeen(kind, namespace, name string) bool {
	s.seenMu.RLock()
	defer s.seenMu.RUnlock()
	return s.seenResources[ResourceKey(kind, namespace, name)]
}

// ClearResourceSeen removes a resource from the seen set
func (s *SQLiteStore) ClearResourceSeen(kind, namespace, name string) {
	key := ResourceKey(kind, namespace, name)

	s.seenMu.Lock()
	delete(s.seenResources, key)
	s.seenMu.Unlock()

	// Remove from database (best effort)
	_, _ = s.db.Exec("DELETE FROM seen_resources WHERE resource_key = ?", key)
}

// Stats returns storage statistics
func (s *SQLiteStore) Stats() StoreStats {
	var stats StoreStats

	// Get total events
	row := s.db.QueryRow("SELECT COUNT(*) FROM events")
	row.Scan(&stats.TotalEvents)

	// Get oldest and newest timestamps
	row = s.db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM events")
	var oldest, newest sql.NullString
	row.Scan(&oldest, &newest)
	if oldest.Valid {
		stats.OldestEvent, _ = time.Parse(time.RFC3339Nano, oldest.String)
	}
	if newest.Valid {
		stats.NewestEvent, _ = time.Parse(time.RFC3339Nano, newest.String)
	}

	// Get database file size
	if info, err := os.Stat(s.path); err == nil {
		stats.StorageBytes = info.Size()
	}

	s.seenMu.RLock()
	stats.SeenResources = len(s.seenResources)
	s.seenMu.RUnlock()

	return stats
}

// Close releases any resources held by the store
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Cleanup removes events older than the given duration
func (s *SQLiteStore) Cleanup(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, "DELETE FROM events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// scanEvent scans a row into a TimelineEvent
func (s *SQLiteStore) scanEvent(rows *sql.Rows) (TimelineEvent, error) {
	var event TimelineEvent
	var timestamp string
	var source, eventType, healthState string
	var uid, reason, message, diffJSON, labelsJSON sql.NullString
	var ownerKind, ownerName, correlationID sql.NullString

	err := rows.Scan(
		&event.ID,
		&timestamp,
		&source,
		&event.Kind,
		&event.Namespace,
		&event.Name,
		&uid,
		&eventType,
		&reason,
		&message,
		&diffJSON,
		&healthState,
		&ownerKind,
		&ownerName,
		&labelsJSON,
		&event.Count,
		&correlationID,
	)
	if err != nil {
		return event, err
	}

	event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
	event.Source = EventSource(source)
	event.EventType = EventType(eventType)
	event.HealthState = HealthState(healthState)

	if uid.Valid {
		event.UID = uid.String
	}
	if reason.Valid {
		event.Reason = reason.String
	}
	if message.Valid {
		event.Message = message.String
	}
	if correlationID.Valid {
		event.CorrelationID = correlationID.String
	}

	if diffJSON.Valid && diffJSON.String != "" {
		var diff DiffInfo
		if json.Unmarshal([]byte(diffJSON.String), &diff) == nil {
			event.Diff = &diff
		}
	}

	if ownerKind.Valid && ownerKind.String != "" {
		event.Owner = &OwnerInfo{
			Kind: ownerKind.String,
			Name: ownerName.String,
		}
	}

	if labelsJSON.Valid && labelsJSON.String != "" {
		json.Unmarshal([]byte(labelsJSON.String), &event.Labels)
	}

	return event, nil
}

// scanEventRow scans a single row into a TimelineEvent
func (s *SQLiteStore) scanEventRow(row *sql.Row) (TimelineEvent, error) {
	var event TimelineEvent
	var timestamp string
	var source, eventType, healthState string
	var uid, reason, message, diffJSON, labelsJSON sql.NullString
	var ownerKind, ownerName, correlationID sql.NullString

	err := row.Scan(
		&event.ID,
		&timestamp,
		&source,
		&event.Kind,
		&event.Namespace,
		&event.Name,
		&uid,
		&eventType,
		&reason,
		&message,
		&diffJSON,
		&healthState,
		&ownerKind,
		&ownerName,
		&labelsJSON,
		&event.Count,
		&correlationID,
	)
	if err != nil {
		return event, err
	}

	event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
	event.Source = EventSource(source)
	event.EventType = EventType(eventType)
	event.HealthState = HealthState(healthState)

	if uid.Valid {
		event.UID = uid.String
	}
	if reason.Valid {
		event.Reason = reason.String
	}
	if message.Valid {
		event.Message = message.String
	}
	if correlationID.Valid {
		event.CorrelationID = correlationID.String
	}

	if diffJSON.Valid && diffJSON.String != "" {
		var diff DiffInfo
		if json.Unmarshal([]byte(diffJSON.String), &diff) == nil {
			event.Diff = &diff
		}
	}

	if ownerKind.Valid && ownerKind.String != "" {
		event.Owner = &OwnerInfo{
			Kind: ownerKind.String,
			Name: ownerName.String,
		}
	}

	if labelsJSON.Valid && labelsJSON.String != "" {
		json.Unmarshal([]byte(labelsJSON.String), &event.Labels)
	}

	return event, nil
}

// getOrCompileFilter returns a cached compiled filter or compiles a new one
func (s *SQLiteStore) getOrCompileFilter(presetName string) (*CompiledFilter, error) {
	s.cacheMu.RLock()
	if cf, ok := s.filterCache[presetName]; ok {
		s.cacheMu.RUnlock()
		return cf, nil
	}
	s.cacheMu.RUnlock()

	presets := DefaultFilterPresets()
	preset, ok := presets[presetName]
	if !ok {
		return nil, nil
	}

	cf, err := CompileFilter(&preset)
	if err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	s.filterCache[presetName] = cf
	s.cacheMu.Unlock()

	return cf, nil
}
