package timeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver

	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

// sqliteTimeLayout is the storage format for timestamp columns compared
// lexically by ORDER BY and range filters. RFC3339Nano is unsuitable: it
// strips trailing fraction zeros, and '.' sorts before 'Z', so a
// second-aligned stamp ("...T00:00:00Z") sorts lexically AFTER a sub-second
// one in the same second ("...T00:00:00.5Z"). A fixed-width 9-digit fraction
// on UTC ('Z') makes lexical order match chronological order. Reads still
// parse with RFC3339Nano, which accepts any fraction width — required for
// rows written before normalizeTimestamps ran.
const sqliteTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// SQLiteStore is a persistent implementation of EventStore using SQLite.
// Suitable for local development with persistence and in-cluster use with PVC.
type SQLiteStore struct {
	db            *sql.DB
	seenResources map[string]bool
	seenMu        sync.RWMutex
	filterCache   map[string]*CompiledFilter
	cacheMu       sync.RWMutex
	path          string
	quit          chan struct{}
	wg            sync.WaitGroup
	closeOnce     sync.Once

	cleanupMu     sync.RWMutex
	retentionAge  time.Duration
	maxStorage    int64
	lastCleanupAt time.Time
	lastCleanupN  int64
	lastCleanupEr string

	// nextSeq is a process-lifetime monotonic arrival counter, seeded from
	// MAX(seq) at open. It survives retention emptying the table, so a new
	// insert's seq always exceeds any previously issued seq while the store
	// lives — an in-flight delta cursor can't be silently overtaken by a
	// rewound seq.
	nextSeq atomic.Int64
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
		quit:          make(chan struct{}),
	}

	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	// Hydrate the in-memory seenResources map from the persisted table so
	// that on restart, informer Add events for previously-seen resources
	// short-circuit in IsResourceSeen instead of re-running historical-event
	// extraction for every resource. Best-effort: a query failure means we
	// behave like a fresh store, not a fatal error.
	store.hydrateSeenResources()

	// Seed the monotonic seq counter from the persisted high-water mark so
	// arrival numbers continue above the last issued value across restarts.
	if err := store.seedSeq(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to seed seq counter: %w", err)
	}

	return store, nil
}

// seedSeq initializes nextSeq to the current MAX(seq). A failure must fail the
// open: seeding at 0 would issue seqs below rows already in the table, and a
// delta client's cursor (the old high-water mark) would then read every new
// event as already-seen — a live-looking timeline that only refreshes on the
// periodic full resync.
func (s *SQLiteStore) seedSeq() error {
	var maxSeq int64
	if err := s.db.QueryRow("SELECT COALESCE(MAX(seq), 0) FROM events").Scan(&maxSeq); err != nil {
		return err
	}
	s.nextSeq.Store(maxSeq)
	return nil
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
		created_at TEXT DEFAULT (datetime('now')),
		cluster_context TEXT NOT NULL DEFAULT '',
		resource_created_at TEXT,
		seq INTEGER NOT NULL DEFAULT 0
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

	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// SQLite's ALTER TABLE ADD COLUMN errors with "duplicate column" when the
	// schema is already current; PRAGMA-detect before adding.
	rows, err := s.db.Query("PRAGMA table_info(events)")
	if err != nil {
		return err
	}
	defer rows.Close()
	hasAPIVersion := false
	hasClusterContext := false
	hasResourceCreatedAt := false
	hasSeq := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		switch name {
		case "api_version":
			hasAPIVersion = true
		case "cluster_context":
			hasClusterContext = true
		case "resource_created_at":
			hasResourceCreatedAt = true
		case "seq":
			hasSeq = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasAPIVersion {
		if _, err := s.db.Exec("ALTER TABLE events ADD COLUMN api_version TEXT"); err != nil {
			return err
		}
	}
	if !hasClusterContext {
		// Pre-existing rows keep '' — their cluster is unknowable (recorded
		// before provenance was tracked), and context-scoped queries
		// deliberately exclude them rather than guess.
		if _, err := s.db.Exec("ALTER TABLE events ADD COLUMN cluster_context TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if !hasResourceCreatedAt {
		if _, err := s.db.Exec("ALTER TABLE events ADD COLUMN resource_created_at TEXT"); err != nil {
			return err
		}
	}
	if !hasSeq {
		if _, err := s.db.Exec("ALTER TABLE events ADD COLUMN seq INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		// Backfill arrival numbers from rowid — insertion order, which is what
		// seq means. New appends continue above MAX(seq).
		if _, err := s.db.Exec("UPDATE events SET seq = rowid WHERE seq = 0"); err != nil {
			return err
		}
	}
	if err := s.normalizeTimestamps(); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_events_cluster_ts ON events(cluster_context, timestamp DESC)"); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_events_seq ON events(seq)"); err != nil {
		return err
	}

	return nil
}

// normalizeTimestamps rewrites rows whose timestamp doesn't match
// sqliteTimeLayout. Older builds stored RFC3339Nano in the host's local zone;
// both the zone offset and the variable fraction width break the
// lexical-order-is-chronological invariant the layout exists for. One pass per
// legacy row: rewritten rows match the GLOB and are skipped on later opens.
func (s *SQLiteStore) normalizeTimestamps() error {
	rows, err := s.db.Query(`SELECT id, timestamp FROM events WHERE timestamp NOT GLOB '????-??-??T??:??:??.?????????Z'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type fixup struct{ id, ts string }
	var fixups []fixup
	unparseable := 0
	for rows.Next() {
		var id, ts string
		if err := rows.Scan(&id, &ts); err != nil {
			return err
		}
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			unparseable++
			continue
		}
		fixups = append(fixups, fixup{id: id, ts: t.UTC().Format(sqliteTimeLayout)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if unparseable > 0 {
		log.Printf("[timeline] %d timeline rows have unparseable timestamps; left as-is", unparseable)
	}
	if len(fixups) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("UPDATE events SET timestamp = ? WHERE id = ?")
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, f := range fixups {
		if _, err := stmt.Exec(f.ts, f.id); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[timeline] normalized %d legacy timeline timestamps to fixed-width UTC", len(fixups))
	return nil
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

	// A K8s Event mutates count/message/lastTimestamp in place on the same uid;
	// the id is the uid, so a bump re-arrives as a conflict. Upsert those mutable
	// fields for k8s_event rows — mirroring MemoryStore.appendLocked — so the row
	// reflects the latest revision instead of dropping the bump. The WHERE gates
	// the update to k8s_event conflicts whose incoming revision is not older, so
	// an out-of-order relay can't clobber a newer row. For informer/historical
	// ids the WHERE is false, leaving the original row untouched — the same
	// no-op an INSERT OR IGNORE gives for a relist dupe.
	//
	// Enrichment (owner/labels/createdAt) upserts asymmetrically: a bump that
	// carries it wins (fresher truth), a bump that lost it (tombstone expired,
	// object gone from the live cache) keeps what the row already knows.
	// seq is the arrival number, taken from the process-lifetime nextSeq counter
	// rather than MAX(seq)+1 per insert: retention emptying the table can't then
	// rewind seq and strand a live client's delta cursor. The upsert takes
	// excluded.seq — the freshly issued value — so a count bump re-arrives at the
	// cursor frontier instead of staying buried at its original arrival position,
	// mirroring MemoryStore's vacate-and-re-append-at-head.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (
			id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
			reason, message, diff_json, health_state, owner_kind, owner_name,
			labels_json, count, correlation_id, cluster_context, resource_created_at, seq
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			timestamp = excluded.timestamp,
			event_type = excluded.event_type,
			reason = excluded.reason,
			message = excluded.message,
			health_state = excluded.health_state,
			count = excluded.count,
			seq = excluded.seq,
			resource_created_at = COALESCE(excluded.resource_created_at, events.resource_created_at),
			owner_kind = CASE WHEN excluded.owner_kind != '' THEN excluded.owner_kind ELSE events.owner_kind END,
			owner_name = CASE WHEN excluded.owner_name != '' THEN excluded.owner_name ELSE events.owner_name END,
			labels_json = CASE WHEN excluded.labels_json != '' THEN excluded.labels_json ELSE events.labels_json END
		WHERE events.source = 'k8s_event'
			AND excluded.source = 'k8s_event'
			AND excluded.timestamp >= events.timestamp
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
		var resourceCreatedAt any
		if event.CreatedAt != nil {
			resourceCreatedAt = event.CreatedAt.UTC().Format(sqliteTimeLayout)
		}

		// Timestamps hit a TEXT column compared lexically by ORDER BY / range
		// filters; sqliteTimeLayout (fixed-width UTC) keeps lexical order
		// chronological.
		_, err = stmt.ExecContext(ctx,
			event.ID,
			event.Timestamp.UTC().Format(sqliteTimeLayout),
			string(event.Source),
			event.Kind,
			event.APIVersion,
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
			event.ClusterContext,
			resourceCreatedAt,
			s.nextSeq.Add(1),
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
	query.WriteString("SELECT id, timestamp, source, kind, api_version, namespace, name, uid, event_type, ")
	query.WriteString("reason, message, diff_json, health_state, owner_kind, owner_name, ")
	query.WriteString("labels_json, count, correlation_id, cluster_context, resource_created_at, seq FROM events WHERE 1=1")

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

	if len(opts.Names) > 0 {
		query.WriteString(" AND name IN (")
		for i, name := range opts.Names {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString("?")
			args = append(args, name)
		}
		query.WriteString(")")
	}

	if !opts.Since.IsZero() {
		query.WriteString(" AND timestamp >= ?")
		args = append(args, opts.Since.UTC().Format(sqliteTimeLayout))
	}

	if !opts.Until.IsZero() {
		query.WriteString(" AND timestamp <= ?")
		args = append(args, opts.Until.UTC().Format(sqliteTimeLayout))
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

	if len(opts.EventTypes) > 0 {
		query.WriteString(" AND event_type IN (")
		for i, et := range opts.EventTypes {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString("?")
			args = append(args, string(et))
		}
		query.WriteString(")")
	}

	// Filter deletes in SQL (before ORDER/LIMIT) so hiding them can't under-fill
	// the page when the newest rows happen to be deletes.
	if opts.ExcludeDeleted {
		query.WriteString(" AND event_type != ?")
		args = append(args, string(EventTypeDelete))
	}

	if opts.ClusterContext != "" {
		query.WriteString(" AND cluster_context = ?")
		args = append(args, opts.ClusterContext)
	}

	if opts.SinceSeq > 0 {
		query.WriteString(" AND seq > ?")
		args = append(args, opts.SinceSeq)
	}

	// Delta reads (SinceSeq>0) page by ascending arrival order: the server
	// advances the client cursor by the max seq in the page, so a burst larger
	// than the limit must resume from the lowest unseen seq — timestamp DESC
	// would return the newest matches and silently drop the mid-seq ones. The
	// client merges by id, so ascending is fine. Non-delta reads page newest-first.
	if opts.SinceSeq > 0 {
		query.WriteString(" ORDER BY seq ASC")
	} else {
		query.WriteString(" ORDER BY timestamp DESC")
	}

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
	query := `SELECT id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id, cluster_context, resource_created_at, seq FROM events WHERE id = ?`

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
func (s *SQLiteStore) GetChangesForOwner(ctx context.Context, ownerKind, ownerNamespace, ownerName, clusterContext string, since time.Time, limit int) ([]TimelineEvent, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id, cluster_context, resource_created_at, seq FROM events
		WHERE owner_kind = ? AND owner_name = ? AND namespace = ?`

	args := []any{ownerKind, ownerName, ownerNamespace}

	if clusterContext != "" {
		query += " AND cluster_context = ?"
		args = append(args, clusterContext)
	}

	if !since.IsZero() {
		query += " AND timestamp >= ?"
		args = append(args, since.UTC().Format(sqliteTimeLayout))
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

// MarkResourceSeen records that a resource has been seen. The key is
// cluster-qualified: this store outlives kubeconfig context switches, so a bare
// kind/namespace/name would let a same-named resource in another cluster read
// as already-seen and drop its add.
func (s *SQLiteStore) MarkResourceSeen(clusterContext, kind, namespace, name string) {
	key := SeenResourceKey(clusterContext, kind, namespace, name)

	s.seenMu.Lock()
	s.seenResources[key] = true
	s.seenMu.Unlock()

	// Persist to database (best effort)
	_, _ = s.db.Exec("INSERT OR REPLACE INTO seen_resources (resource_key) VALUES (?)", key)
}

// IsResourceSeen checks if a resource has been seen before in the given cluster
// context.
func (s *SQLiteStore) IsResourceSeen(clusterContext, kind, namespace, name string) bool {
	s.seenMu.RLock()
	defer s.seenMu.RUnlock()
	return s.seenResources[SeenResourceKey(clusterContext, kind, namespace, name)]
}

// ClearResourceSeen removes a resource from the seen set
func (s *SQLiteStore) ClearResourceSeen(clusterContext, kind, namespace, name string) {
	key := SeenResourceKey(clusterContext, kind, namespace, name)

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
	stats.StorageBytes = s.storageBytes()

	s.seenMu.RLock()
	stats.SeenResources = len(s.seenResources)
	s.seenMu.RUnlock()

	s.cleanupMu.RLock()
	stats.RetentionAge = s.retentionAge
	stats.MaxStorageBytes = s.maxStorage
	stats.LastCleanupAt = s.lastCleanupAt
	stats.LastCleanupDeletedRows = s.lastCleanupN
	stats.LastCleanupError = s.lastCleanupEr
	s.cleanupMu.RUnlock()

	return stats
}

// Close releases any resources held by the store. Safe to call multiple times.
func (s *SQLiteStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.quit)
		s.wg.Wait()
	})
	return s.db.Close()
}

// Cleanup removes events older than the given duration
func (s *SQLiteStore) Cleanup(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(sqliteTimeLayout)
	result, err := s.db.ExecContext(ctx, "DELETE FROM events WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *SQLiteStore) PruneToMaxSize(ctx context.Context, maxBytes int64) (int64, error) {
	if maxBytes <= 0 {
		return 0, nil
	}
	if current := s.storageBytes(); current <= maxBytes {
		return 0, nil
	}
	if err := s.checkpointWAL(ctx); err != nil {
		return 0, err
	}
	if current := s.storageBytes(); current <= maxBytes {
		return 0, nil
	}

	target := maxBytes * 85 / 100
	if target <= 0 {
		target = maxBytes
	}

	var totalDeleted int64
	for attempts := 0; attempts < 5; attempts++ {
		current := s.storageBytes()
		if current <= maxBytes {
			return totalDeleted, nil
		}

		count, err := s.countEvents(ctx)
		if err != nil {
			return totalDeleted, err
		}
		if count == 0 {
			if err := s.reclaimStorage(ctx); err != nil {
				return totalDeleted, err
			}
			break
		}
		if count == 1 {
			deleted, err := s.deleteOldestEvents(ctx, 1)
			totalDeleted += deleted
			if err != nil {
				return totalDeleted, err
			}
			if err := s.reclaimStorage(ctx); err != nil {
				return totalDeleted, err
			}
			continue
		}

		avgBytes := current / count
		if avgBytes < 1 {
			avgBytes = 1
		}
		toDelete := ((current - target) / avgBytes) + 1
		if toDelete < 1000 && count > 1000 {
			toDelete = 1000
		}
		if toDelete < 1 {
			toDelete = 1
		}
		if toDelete >= count {
			toDelete = count - 1
		}

		deleted, err := s.deleteOldestEvents(ctx, toDelete)
		totalDeleted += deleted
		if err != nil {
			return totalDeleted, err
		}
		if deleted == 0 {
			break
		}
		if err := s.reclaimStorage(ctx); err != nil {
			return totalDeleted, err
		}
	}

	if current := s.storageBytes(); current > maxBytes {
		return totalDeleted, fmt.Errorf("timeline storage still above max size after pruning (%d > %d bytes)", current, maxBytes)
	}
	return totalDeleted, nil
}

// StartCleanupLoop spawns a goroutine that periodically deletes events older
// than retention and prunes oldest events when the DB exceeds maxStorageBytes.
// Runs once immediately so post-upgrade users with bloated DBs don't wait an
// hour for the first cleanup. The loop exits when Close is called. Both
// retention <= 0 and maxStorageBytes <= 0 together disable cleanup entirely.
func (s *SQLiteStore) StartCleanupLoop(retention, interval time.Duration, maxStorageBytes int64) {
	if interval <= 0 || (retention <= 0 && maxStorageBytes <= 0) {
		return
	}
	s.cleanupMu.Lock()
	s.retentionAge = retention
	s.maxStorage = maxStorageBytes
	s.cleanupMu.Unlock()
	s.wg.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.runCleanup(retention, maxStorageBytes)
		for {
			select {
			case <-s.quit:
				return
			case <-ticker.C:
				s.runCleanup(retention, maxStorageBytes)
			}
		}
	})
}

// hydrateSeenResources populates the in-memory seenResources map from the
// persisted table. Best-effort: any error leaves the map in whatever state it
// reached, which is no worse than a fresh store. Safe to call only from the
// constructor — no locking, since no other goroutine has the store yet.
func (s *SQLiteStore) hydrateSeenResources() {
	rows, err := s.db.Query("SELECT resource_key FROM seen_resources")
	if err != nil {
		log.Printf("[timeline] failed to load seen resources from %s: %v", s.path, err)
		return
	}
	defer rows.Close()

	var loaded, skipped int
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			skipped++
			continue
		}
		s.seenResources[key] = true
		loaded++
	}
	if err := rows.Err(); err != nil {
		log.Printf("[timeline] seen_resources iteration ended with error after %d rows: %v", loaded, err)
	}
	if skipped > 0 {
		log.Printf("[timeline] skipped %d unreadable seen_resources rows in %s (loaded %d)", skipped, s.path, loaded)
	}
	if loaded > 0 {
		log.Printf("[timeline] loaded %d seen resources from %s", loaded, s.path)
	}
}

// runCleanup deletes events older than retention and truncates the WAL so the
// sidecar file stays bounded. WAL truncation is best-effort. Records outcome
// (timestamp, deleted count, last error) so it's surfaceable via Stats() and
// /api/diagnostics — operators shouldn't need to tail logs to know retention
// is working.
func (s *SQLiteStore) runCleanup(retention time.Duration, maxStorageBytes int64) {
	var n int64
	var cleanupErr error
	var checkpointErr error
	var pruneErr error
	ctx := context.Background()
	if retention > 0 {
		n, cleanupErr = s.Cleanup(ctx, retention)
		if cleanupErr == nil {
			checkpointErr = s.checkpointWAL(ctx)
		}
	}
	if maxStorageBytes > 0 {
		var pruned int64
		pruned, pruneErr = s.PruneToMaxSize(ctx, maxStorageBytes)
		n += pruned
	}
	err := errors.Join(cleanupErr, checkpointErr, pruneErr)
	now := time.Now()
	s.cleanupMu.Lock()
	s.lastCleanupAt = now
	s.lastCleanupN = n
	if err != nil {
		s.lastCleanupEr = err.Error()
	} else {
		s.lastCleanupEr = ""
	}
	s.cleanupMu.Unlock()

	if err != nil {
		log.Printf("[timeline] cleanup failed for %s: %v", s.path, err)
		return
	}
	if n > 0 {
		log.Printf("[timeline] cleanup: deleted %d events from %s", n, s.path)
	}
}

func (s *SQLiteStore) storageBytes() int64 {
	var total int64
	for _, path := range []string{s.path, s.path + "-wal"} {
		if info, err := os.Stat(path); err == nil {
			total += info.Size()
		}
	}
	return total
}

func (s *SQLiteStore) countEvents(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *SQLiteStore) deleteOldestEvents(ctx context.Context, limit int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM events
		WHERE id IN (
			SELECT id FROM events
			ORDER BY timestamp ASC, created_at ASC
			LIMIT ?
		)
	`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *SQLiteStore) reclaimStorage(ctx context.Context) error {
	if err := s.checkpointWAL(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum failed: %w", err)
	}
	return nil
}

func (s *SQLiteStore) checkpointWAL(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("wal checkpoint failed: %w", err)
	}
	return nil
}

// scanEvent scans a row into a TimelineEvent
func (s *SQLiteStore) scanEvent(rows *sql.Rows) (TimelineEvent, error) {
	var event TimelineEvent
	var timestamp string
	var source, eventType, healthState string
	var apiVersion, uid, reason, message, diffJSON, labelsJSON sql.NullString
	var ownerKind, ownerName, correlationID, clusterContext, resourceCreatedAt sql.NullString

	err := rows.Scan(
		&event.ID,
		&timestamp,
		&source,
		&event.Kind,
		&apiVersion,
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
		&clusterContext,
		&resourceCreatedAt,
		&event.Seq,
	)
	if err != nil {
		return event, err
	}
	event.ClusterContext = clusterContext.String
	if resourceCreatedAt.Valid && resourceCreatedAt.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, resourceCreatedAt.String); err == nil {
			event.CreatedAt = &t
		}
	}

	event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
	event.Source = EventSource(source)
	event.EventType = EventType(eventType)
	event.HealthState = HealthState(healthState)

	if apiVersion.Valid {
		event.APIVersion = apiVersion.String
	}
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
	var apiVersion, uid, reason, message, diffJSON, labelsJSON sql.NullString
	var ownerKind, ownerName, correlationID, clusterContext, resourceCreatedAt sql.NullString

	err := row.Scan(
		&event.ID,
		&timestamp,
		&source,
		&event.Kind,
		&apiVersion,
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
		&clusterContext,
		&resourceCreatedAt,
		&event.Seq,
	)
	if err != nil {
		return event, err
	}
	event.ClusterContext = clusterContext.String
	if resourceCreatedAt.Valid && resourceCreatedAt.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, resourceCreatedAt.String); err == nil {
			event.CreatedAt = &t
		}
	}

	event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
	event.Source = EventSource(source)
	event.EventType = EventType(eventType)
	event.HealthState = HealthState(healthState)

	if apiVersion.Valid {
		event.APIVersion = apiVersion.String
	}
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
