package timeline

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

const (
	postgresMigrationTimeout = 30 * time.Second
	postgresUnlockTimeout    = 5 * time.Second
	postgresOperationTimeout = 30 * time.Second

	postgresMigrationLockNamespace int32 = 0x52414452
	postgresMigrationLockID        int32 = 1

	postgresAppendLockNamespace int64 = 0x52414452
	postgresAppendLockID        int64 = 2
)

var postgresPingTimeout = 10 * time.Second

func withPostgresOperationTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, postgresOperationTimeout)
}

//go:embed migrations/postgres/*.sql
var postgresMigrations embed.FS

type PostgresStore struct {
	db *sql.DB

	seenResources map[string]bool
	seenMu        sync.RWMutex

	filterCache map[string]*CompiledFilter
	cacheMu     sync.RWMutex

	quit      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	cleanupMu     sync.RWMutex
	retentionAge  time.Duration
	lastCleanupAt time.Time
	lastCleanupN  int64
	lastCleanupEr string
	// Sticky: once retention has deleted anything, a consumer paging forward
	// from an old cursor can no longer assume an empty page means "end of
	// history" — it may have fallen below the retained floor. Mirrors
	// SQLiteStore.evictedRows.
	evictedRows bool
}

// NewPostgresStore opens a PostgreSQL-backed timeline store and applies migrations.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, postgresConnectionError("open PostgreSQL timeline database", err, dsn)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	closeOnError := func(operation string, operationErr error) (*PostgresStore, error) {
		_ = db.Close()
		return nil, postgresConnectionError(operation, operationErr, dsn)
	}

	pingCtx, cancelPing := context.WithTimeout(context.Background(), postgresPingTimeout)
	err = db.PingContext(pingCtx)
	cancelPing()
	if err != nil {
		return closeOnError("connect to PostgreSQL timeline database", err)
	}

	migrationCtx, cancelMigrations := context.WithTimeout(
		context.Background(),
		postgresMigrationTimeout,
	)
	err = runPostgresMigrations(migrationCtx, db)
	cancelMigrations()
	if err != nil {
		return closeOnError("migrate PostgreSQL timeline database", err)
	}
	validationCtx, cancelValidation := context.WithTimeout(
		context.Background(),
		postgresMigrationTimeout,
	)
	err = validatePostgresSchema(validationCtx, db)
	cancelValidation()
	if err != nil {
		return closeOnError("validate PostgreSQL timeline schema", err)
	}

	store := &PostgresStore{
		db:            db,
		seenResources: make(map[string]bool),
		filterCache:   make(map[string]*CompiledFilter),
		quit:          make(chan struct{}),
	}
	hydrateCtx, cancelHydrate := context.WithTimeout(
		context.Background(),
		postgresPingTimeout,
	)
	err = store.hydrateSeenResources(hydrateCtx)
	cancelHydrate()
	if err != nil {
		return closeOnError("hydrate PostgreSQL timeline seen resources", err)
	}

	return store, nil
}

func validatePostgresSchema(ctx context.Context, db *sql.DB) error {
	for _, relation := range []string{
		"radar_timeline_events",
		"radar_timeline_event_seq",
		"radar_timeline_seen_resources",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", relation).Scan(&exists); err != nil {
			return fmt.Errorf("check required relation %q: %w", relation, err)
		}
		if !exists {
			return fmt.Errorf("required relation %q is missing", relation)
		}
	}
	if _, err := db.ExecContext(ctx, `
		SELECT id, timestamp, source, kind, api_version, namespace, name, uid,
		       event_type, reason, message, diff_json, health_state, owner_kind,
		       owner_name, labels_json, count, correlation_id, created_at,
		       cluster_context, resource_created_at, seq
		FROM radar_timeline_events
		LIMIT 0
	`); err != nil {
		return fmt.Errorf("check timeline events schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SELECT resource_key, seen_at FROM radar_timeline_seen_resources LIMIT 0"); err != nil {
		return fmt.Errorf("check seen resources schema: %w", err)
	}
	return nil
}

func (s *PostgresStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.quit)
		s.wg.Wait()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// Append adds a single event to the store.
func (s *PostgresStore) Append(ctx context.Context, event TimelineEvent) error {
	return s.AppendBatch(ctx, []TimelineEvent{event})
}

// AppendBatch adds multiple events atomically.
//
// All appends serialize through a transaction-scoped advisory lock so that a
// reader cannot observe and advance past a higher committed sequence while a
// lower sequence remains uncommitted. K8s Event rows upsert mutable fields
// (count, message, timestamp, seq) when the incoming revision is not older,
// mirroring the SQLite and memory stores.
func (s *PostgresStore) AppendBatch(ctx context.Context, events []TimelineEvent) error {
	if len(events) == 0 {
		return nil
	}
	ctx, cancel := withPostgresOperationTimeout(ctx)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append transaction: %w", err)
	}
	defer tx.Rollback()

	// Serialize all append transactions before sequence allocation. A PostgreSQL
	// sequence is concurrent-safe but not commit-ordered; without this lock a
	// reader could advance past a higher committed seq while a lower one was still
	// in flight, permanently skipping the later event.
	if _, err := tx.ExecContext(
		ctx,
		"SELECT pg_advisory_xact_lock($1, $2)",
		postgresAppendLockNamespace,
		postgresAppendLockID,
	); err != nil {
		return fmt.Errorf("acquire append lock: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO radar_timeline_events (
			id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
			reason, message, diff_json, health_state, owner_kind, owner_name,
			labels_json, count, correlation_id, cluster_context, resource_created_at, seq
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, nextval('radar_timeline_event_seq')
		)
		ON CONFLICT(id) DO UPDATE SET
			timestamp = excluded.timestamp,
			event_type = excluded.event_type,
			reason = excluded.reason,
			message = excluded.message,
			health_state = excluded.health_state,
			count = excluded.count,
			seq = excluded.seq,
			resource_created_at = COALESCE(excluded.resource_created_at, radar_timeline_events.resource_created_at),
			owner_kind = CASE WHEN excluded.owner_kind != '' THEN excluded.owner_kind ELSE radar_timeline_events.owner_kind END,
			owner_name = CASE WHEN excluded.owner_name != '' THEN excluded.owner_name ELSE radar_timeline_events.owner_name END,
			labels_json = CASE WHEN excluded.labels_json IS NOT NULL THEN excluded.labels_json ELSE radar_timeline_events.labels_json END
		WHERE radar_timeline_events.source = 'k8s_event'
			AND excluded.source = 'k8s_event'
			AND excluded.timestamp >= radar_timeline_events.timestamp
	`)
	if err != nil {
		return fmt.Errorf("prepare append statement: %w", err)
	}
	defer stmt.Close()

	for _, event := range events {
		var diffJSON, labelsJSON any
		var ownerKind, ownerName string

		if event.Diff != nil {
			b, err := json.Marshal(event.Diff)
			if err != nil {
				log.Printf("Warning: failed to marshal diff for event %s: %v", event.ID, err)
			} else {
				diffJSON = string(b)
			}
		}
		if event.Labels != nil {
			b, err := json.Marshal(event.Labels)
			if err != nil {
				log.Printf("Warning: failed to marshal labels for event %s: %v", event.ID, err)
			} else {
				labelsJSON = string(b)
			}
		}
		if event.Owner != nil {
			ownerKind = event.Owner.Kind
			ownerName = event.Owner.Name
		}

		var resourceCreatedAt any
		if event.CreatedAt != nil {
			resourceCreatedAt = event.CreatedAt.UTC().UnixNano()
		}

		_, err = stmt.ExecContext(ctx,
			event.ID,
			event.Timestamp.UTC().UnixNano(),
			string(event.Source),
			event.Kind,
			event.APIVersion,
			event.Namespace,
			event.Name,
			event.UID,
			string(event.EventType),
			event.Reason,
			event.Message,
			diffJSON,
			string(event.HealthState),
			ownerKind,
			ownerName,
			labelsJSON,
			event.Count,
			event.CorrelationID,
			event.ClusterContext,
			resourceCreatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert event %s: %w", event.ID, err)
		}
	}

	return tx.Commit()
}

// Query retrieves events matching the given options.
func (s *PostgresStore) Query(ctx context.Context, opts QueryOptions) ([]TimelineEvent, error) {
	ctx, cancel := withPostgresOperationTimeout(ctx)
	defer cancel()
	q, args, err := s.buildQuery(opts)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var cf *CompiledFilter
	if opts.FilterPreset != "" {
		var filterErr error
		cf, filterErr = s.getOrCompileFilter(opts.FilterPreset)
		if filterErr != nil {
			log.Printf("Warning: failed to compile filter preset %q: %v", opts.FilterPreset, filterErr)
		}
	}

	events := make([]TimelineEvent, 0)
	for rows.Next() {
		event, err := s.scanEvent(rows)
		if err != nil {
			return nil, err
		}

		if cf != nil && !cf.Matches(&event) {
			continue
		}

		if event.IsManaged() && !opts.IncludeManaged && !cf.IncludesManaged() {
			continue
		}

		if !opts.IncludeK8sEvents && event.Source == SourceK8sEvent {
			continue
		}

		events = append(events, event)
	}

	return events, rows.Err()
}

// QueryGrouped retrieves events grouped according to the specified mode.
func (s *PostgresStore) QueryGrouped(ctx context.Context, opts QueryOptions) (*TimelineResponse, error) {
	startTime := time.Now()

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

// GetEvent retrieves a single event by ID.
func (s *PostgresStore) GetEvent(ctx context.Context, id string) (*TimelineEvent, error) {
	ctx, cancel := withPostgresOperationTimeout(ctx)
	defer cancel()
	query := `SELECT id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id, cluster_context, resource_created_at, seq
		FROM radar_timeline_events WHERE id = $1`

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

// GetChangesForOwner retrieves changes for resources owned by the given owner.
func (s *PostgresStore) GetChangesForOwner(ctx context.Context, ownerKind, ownerNamespace, ownerName, clusterContext string, since time.Time, limit int) ([]TimelineEvent, error) {
	ctx, cancel := withPostgresOperationTimeout(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id, cluster_context, resource_created_at, seq
		FROM radar_timeline_events
		WHERE owner_kind = $1 AND owner_name = $2 AND namespace = $3`
	args := []any{ownerKind, ownerName, ownerNamespace}

	argN := 4
	if clusterContext != "" {
		query += fmt.Sprintf(" AND cluster_context = $%d", argN)
		args = append(args, clusterContext)
		argN++
	}
	if !since.IsZero() {
		query += fmt.Sprintf(" AND timestamp >= $%d", argN)
		args = append(args, since.UTC().UnixNano())
		argN++
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

// MarkResourceSeen records that a resource has been seen.
func (s *PostgresStore) MarkResourceSeen(clusterContext, kind, namespace, name string) {
	key := SeenResourceKey(clusterContext, kind, namespace, name)

	s.seenMu.Lock()
	s.seenResources[key] = true
	s.seenMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(
		ctx,
		"INSERT INTO radar_timeline_seen_resources (resource_key) VALUES ($1) ON CONFLICT(resource_key) DO NOTHING",
		[]byte(key),
	); err != nil {
		log.Printf("[timeline] failed to persist seen resource: %v", err)
	}
}

// IsResourceSeen checks if a resource has been seen before in the given cluster
// context.
func (s *PostgresStore) IsResourceSeen(clusterContext, kind, namespace, name string) bool {
	s.seenMu.RLock()
	defer s.seenMu.RUnlock()
	return s.seenResources[SeenResourceKey(clusterContext, kind, namespace, name)]
}

// ClearResourceSeen removes a resource from the seen set.
func (s *PostgresStore) ClearResourceSeen(clusterContext, kind, namespace, name string) {
	key := SeenResourceKey(clusterContext, kind, namespace, name)

	s.seenMu.Lock()
	delete(s.seenResources, key)
	s.seenMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(
		ctx,
		"DELETE FROM radar_timeline_seen_resources WHERE resource_key = $1",
		[]byte(key),
	); err != nil {
		log.Printf("[timeline] failed to clear seen resource: %v", err)
	}
}

// Stats returns storage statistics.
func (s *PostgresStore) Stats() StoreStats {
	var stats StoreStats
	ctx, cancel := context.WithTimeout(context.Background(), postgresOperationTimeout)
	defer cancel()

	row := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM radar_timeline_events")
	row.Scan(&stats.TotalEvents)

	row = s.db.QueryRowContext(ctx, "SELECT MIN(timestamp), MAX(timestamp) FROM radar_timeline_events")
	var oldest, newest sql.NullInt64
	row.Scan(&oldest, &newest)
	if oldest.Valid {
		stats.OldestEvent = time.Unix(0, oldest.Int64).UTC()
	}
	if newest.Valid {
		stats.NewestEvent = time.Unix(0, newest.Int64).UTC()
	}

	row = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MIN(seq), 0), COALESCE(MAX(seq), 0)
		FROM radar_timeline_events
	`)
	row.Scan(&stats.OldestSeq, &stats.NewestSeq)

	stats.StorageBytes = s.storageBytes(ctx)

	s.seenMu.RLock()
	stats.SeenResources = len(s.seenResources)
	s.seenMu.RUnlock()

	s.cleanupMu.RLock()
	stats.RetentionAge = s.retentionAge
	stats.LastCleanupAt = s.lastCleanupAt
	stats.LastCleanupDeletedRows = s.lastCleanupN
	stats.LastCleanupError = s.lastCleanupEr
	stats.EventsEvicted = s.evictedRows
	s.cleanupMu.RUnlock()

	return stats
}

// Cleanup removes events older than the given duration.
func (s *PostgresStore) Cleanup(ctx context.Context, maxAge time.Duration) (int64, error) {
	ctx, cancel := withPostgresOperationTimeout(ctx)
	defer cancel()
	cutoff := time.Now().Add(-maxAge).UTC().UnixNano()
	var totalDeleted int64
	const batchSize = 1000

	for {
		result, err := s.db.ExecContext(ctx, `
			WITH doomed AS (
				SELECT id
				FROM radar_timeline_events
				WHERE timestamp < $1
				ORDER BY timestamp ASC
				LIMIT $2
			)
			DELETE FROM radar_timeline_events
			USING doomed
			WHERE radar_timeline_events.id = doomed.id
		`, cutoff, batchSize)
		if err != nil {
			return totalDeleted, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return totalDeleted, err
		}
		totalDeleted += deleted
		if deleted == 0 || deleted < batchSize {
			break
		}
		if err := ctx.Err(); err != nil {
			return totalDeleted, err
		}
	}
	return totalDeleted, nil
}

// StartCleanupLoop spawns a goroutine that periodically deletes events older
// than retention. The loop exits when Close is called.
//
// maxStorageBytes is accepted for interface symmetry and deliberately ignored:
// PostgreSQL relation size reflects MVCC bloat and autovacuum timing rather
// than live data, so a SQLite-style file-size target would prune real history
// chasing space the database will reclaim on its own. Retention is the only
// bound here.
func (s *PostgresStore) StartCleanupLoop(retention, interval time.Duration, maxStorageBytes int64) {
	if interval <= 0 || retention <= 0 {
		return
	}
	s.cleanupMu.Lock()
	s.retentionAge = retention
	s.cleanupMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		s.runCleanup(retention)
		for {
			select {
			case <-s.quit:
				return
			case <-ticker.C:
				s.runCleanup(retention)
			}
		}
	}()
}

func (s *PostgresStore) runCleanup(retention time.Duration) {
	ctx := context.Background()
	n, err := s.Cleanup(ctx, retention)
	now := time.Now()
	s.cleanupMu.Lock()
	s.lastCleanupAt = now
	s.lastCleanupN = n
	if n > 0 {
		s.evictedRows = true
	}
	if err != nil {
		s.lastCleanupEr = err.Error()
	} else {
		s.lastCleanupEr = ""
	}
	s.cleanupMu.Unlock()

	if err != nil {
		log.Printf("[timeline] PostgreSQL cleanup failed: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[timeline] PostgreSQL cleanup: deleted %d events", n)
	}
}

// storageBytes returns the total relation size for the timeline tables and
// indexes. Errors are ignored so a size-query failure does not break Stats.
func (s *PostgresStore) storageBytes(ctx context.Context) int64 {
	var total sql.NullInt64
	row := s.db.QueryRowContext(ctx, `
		SELECT pg_total_relation_size('radar_timeline_events') +
		       pg_total_relation_size('radar_timeline_seen_resources')
	`)
	if err := row.Scan(&total); err != nil {
		log.Printf("[timeline] failed to query PostgreSQL relation size: %v", err)
		return 0
	}
	return total.Int64
}

func (s *PostgresStore) buildQuery(opts QueryOptions) (string, []any, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT id, timestamp, source, kind, api_version, namespace, name, uid, event_type,
		reason, message, diff_json, health_state, owner_kind, owner_name,
		labels_json, count, correlation_id, cluster_context, resource_created_at, seq
		FROM radar_timeline_events WHERE 1=1`)

	var args []any
	argN := 1

	addFilter := func(format string, value any) {
		query.WriteString(fmt.Sprintf(format, argN))
		args = append(args, value)
		argN++
	}
	addInFilter := func(column string, values []any) {
		if len(values) == 0 {
			return
		}
		query.WriteString(fmt.Sprintf(" AND %s IN (", column))
		for i, v := range values {
			if i > 0 {
				query.WriteString(",")
			}
			query.WriteString(fmt.Sprintf("$%d", argN))
			args = append(args, v)
			argN++
		}
		query.WriteString(")")
	}

	if len(opts.Namespaces) > 0 {
		vals := make([]any, len(opts.Namespaces))
		for i, ns := range opts.Namespaces {
			vals[i] = ns
		}
		addInFilter("namespace", vals)
	}
	if len(opts.Kinds) > 0 {
		vals := make([]any, len(opts.Kinds))
		for i, k := range opts.Kinds {
			vals[i] = k
		}
		addInFilter("kind", vals)
	}
	if len(opts.Names) > 0 {
		vals := make([]any, len(opts.Names))
		for i, n := range opts.Names {
			vals[i] = n
		}
		addInFilter("name", vals)
	}
	if !opts.Since.IsZero() {
		addFilter(" AND timestamp >= $%d", opts.Since.UTC().UnixNano())
	}
	if !opts.Until.IsZero() {
		addFilter(" AND timestamp <= $%d", opts.Until.UTC().UnixNano())
	}
	if len(opts.Sources) > 0 {
		vals := make([]any, len(opts.Sources))
		for i, src := range opts.Sources {
			vals[i] = string(src)
		}
		addInFilter("source", vals)
	}
	if len(opts.EventTypes) > 0 {
		vals := make([]any, len(opts.EventTypes))
		for i, et := range opts.EventTypes {
			vals[i] = string(et)
		}
		addInFilter("event_type", vals)
	}
	if opts.ExcludeDeleted {
		addFilter(" AND event_type != $%d", string(EventTypeDelete))
	}
	if opts.ClusterContext != "" {
		addFilter(" AND cluster_context = $%d", opts.ClusterContext)
	}

	seqPaging := opts.SeqPaging || opts.SinceSeq > 0
	if seqPaging {
		addFilter(" AND seq > $%d", opts.SinceSeq)
	}
	if opts.UntilSeq > 0 {
		addFilter(" AND seq < $%d", opts.UntilSeq)
	}

	// Ordering mirrors the SQLite store exactly (sqlite_store.go). Delta reads
	// page by ascending arrival so a burst larger than the page resumes from the
	// lowest unseen seq; bounded snapshots and backwards pages are
	// newest-arrival-first; everything else is newest-event-time-first.
	if seqPaging || opts.SequenceOrder == pkgtimeline.SequenceOrderAscending {
		query.WriteString(" ORDER BY seq ASC")
	} else if opts.UntilSeq > 0 || opts.SequenceOrder == pkgtimeline.SequenceOrderDescending {
		query.WriteString(" ORDER BY seq DESC")
	} else {
		query.WriteString(" ORDER BY timestamp DESC")
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 10000 {
		limit = 10000
	}
	query.WriteString(fmt.Sprintf(" LIMIT %d", limit))

	if opts.Offset > 0 && !seqPaging {
		query.WriteString(fmt.Sprintf(" OFFSET %d", opts.Offset))
	}

	return query.String(), args, nil
}

type eventScanner interface {
	Scan(dest ...any) error
}

func (s *PostgresStore) scanEvent(scanner eventScanner) (TimelineEvent, error) {
	var event TimelineEvent
	var source, eventType, healthState string
	var apiVersion, uid, reason, message, correlationID, clusterContext sql.NullString
	var ownerKind, ownerName sql.NullString
	var diffJSON, labelsJSON []byte
	var resourceCreatedAt sql.NullInt64
	var timestampNanos int64

	err := scanner.Scan(
		&event.ID,
		&timestampNanos,
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

	event.Source = EventSource(source)
	event.EventType = EventType(eventType)
	event.HealthState = HealthState(healthState)
	event.ClusterContext = clusterContext.String

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
	event.Timestamp = time.Unix(0, timestampNanos).UTC()
	if resourceCreatedAt.Valid {
		t := time.Unix(0, resourceCreatedAt.Int64).UTC()
		event.CreatedAt = &t
	}
	if ownerKind.Valid && ownerKind.String != "" {
		event.Owner = &OwnerInfo{
			Kind: ownerKind.String,
			Name: ownerName.String,
		}
	}
	if len(diffJSON) > 0 {
		var diff DiffInfo
		if json.Unmarshal(diffJSON, &diff) == nil {
			event.Diff = &diff
		}
	}
	if len(labelsJSON) > 0 {
		json.Unmarshal(labelsJSON, &event.Labels)
	}

	return event, nil
}

func (s *PostgresStore) scanEventRow(row *sql.Row) (TimelineEvent, error) {
	return s.scanEvent(row)
}

func (s *PostgresStore) getOrCompileFilter(presetName string) (*CompiledFilter, error) {
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

func (s *PostgresStore) hydrateSeenResources(ctx context.Context) error {
	rows, err := s.db.QueryContext(
		ctx,
		"SELECT resource_key FROM radar_timeline_seen_resources",
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var key []byte
		if err := rows.Scan(&key); err != nil {
			return err
		}
		s.seenResources[string(key)] = true
	}
	return rows.Err()
}

type postgresMigration struct {
	version int64
	name    string
	sql     string
}

func runPostgresMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadPostgresMigrations()
	if err != nil {
		return err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(
		ctx,
		"SELECT pg_advisory_lock($1, $2)",
		postgresMigrationLockNamespace,
		postgresMigrationLockID,
	); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}

	migrationErr := applyPostgresMigrations(ctx, conn, migrations)

	unlockCtx, cancelUnlock := context.WithTimeout(
		context.Background(),
		postgresUnlockTimeout,
	)
	var unlocked bool
	unlockErr := conn.QueryRowContext(
		unlockCtx,
		"SELECT pg_advisory_unlock($1, $2)",
		postgresMigrationLockNamespace,
		postgresMigrationLockID,
	).Scan(&unlocked)
	cancelUnlock()
	if unlockErr == nil && !unlocked {
		unlockErr = fmt.Errorf("migration lock was not held")
	}

	if migrationErr != nil {
		if unlockErr != nil {
			return fmt.Errorf("%w; release migration lock: %v", migrationErr, unlockErr)
		}
		return migrationErr
	}
	if unlockErr != nil {
		return fmt.Errorf("release migration lock: %w", unlockErr)
	}
	return nil
}

func applyPostgresMigrations(
	ctx context.Context,
	conn *sql.Conn,
	migrations []postgresMigration,
) error {
	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS radar_timeline_schema_migrations (
			version    bigint PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	applied, err := appliedPostgresMigrationVersions(ctx, conn)
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if applied[migration.version] {
			continue
		}

		// New migrations must remain additive while rolling upgrades can run old
		// and new Radar pods against the same database.
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO radar_timeline_schema_migrations (version) VALUES ($1)",
			migration.version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", migration.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", migration.name, err)
		}
	}
	return nil
}

func appliedPostgresMigrationVersions(
	ctx context.Context,
	conn *sql.Conn,
) (map[int64]bool, error) {
	rows, err := conn.QueryContext(
		ctx,
		"SELECT version FROM radar_timeline_schema_migrations",
	)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]bool)
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func loadPostgresMigrations() ([]postgresMigration, error) {
	entries, err := fs.ReadDir(postgresMigrations, "migrations/postgres")
	if err != nil {
		return nil, fmt.Errorf("read embedded PostgreSQL migrations: %w", err)
	}

	migrations := make([]postgresMigration, 0, len(entries))
	versions := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}

		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("invalid PostgreSQL migration name %q", entry.Name())
		}
		version, err := strconv.ParseInt(versionText, 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid PostgreSQL migration version in %q", entry.Name())
		}
		if previous, exists := versions[version]; exists {
			return nil, fmt.Errorf(
				"duplicate PostgreSQL migration version %d in %q and %q",
				version,
				previous,
				entry.Name(),
			)
		}

		migrationSQL, err := fs.ReadFile(
			postgresMigrations,
			"migrations/postgres/"+entry.Name(),
		)
		if err != nil {
			return nil, fmt.Errorf("read PostgreSQL migration %q: %w", entry.Name(), err)
		}
		versions[version] = entry.Name()
		migrations = append(migrations, postgresMigration{
			version: version,
			name:    entry.Name(),
			sql:     string(migrationSQL),
		})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}

func postgresConnectionError(operation string, err error, dsn string) error {
	message := err.Error()
	if dsn != "" {
		message = strings.ReplaceAll(message, dsn, "[REDACTED]")
	}
	if config, parseErr := pgx.ParseConfig(dsn); parseErr == nil && config.Password != "" {
		message = strings.ReplaceAll(message, config.Password, "[REDACTED]")
		message = strings.ReplaceAll(message, url.QueryEscape(config.Password), "[REDACTED]")
		message = strings.ReplaceAll(message, url.PathEscape(config.Password), "[REDACTED]")
	}
	// Keep the chain intact so callers can still errors.Is/errors.As the
	// driver's error, but never let its own Error() text reach a log or an API
	// response: that string is where the DSN password appears. fmt.Errorf with
	// %w would print the wrapped message verbatim and undo the redaction, so
	// the wrapper below overrides Error() and exposes Unwrap() only.
	return &postgresConnError{msg: fmt.Sprintf("%s: %s", operation, message), err: err}
}

// postgresConnError carries a redacted message while preserving the underlying
// driver error for errors.Is/errors.As.
type postgresConnError struct {
	msg string
	err error
}

func (e *postgresConnError) Error() string { return e.msg }
func (e *postgresConnError) Unwrap() error { return e.err }
