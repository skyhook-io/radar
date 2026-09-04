package timeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/skyhook-io/radar/pkg/timeline/storetest"
)

var postgresTestSchemaCounter atomic.Uint64

func testPostgresDSN(t *testing.T) string {
	t.Helper()

	baseDSN := os.Getenv("RADAR_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("RADAR_TEST_POSTGRES_DSN is not set")
	}

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	if err := admin.PingContext(t.Context()); err != nil {
		admin.Close()
		t.Fatalf("ping PostgreSQL test database: %v", err)
	}

	schema := fmt.Sprintf(
		"radar_test_%d_%d",
		time.Now().UnixNano(),
		postgresTestSchemaCounter.Add(1),
	)
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create PostgreSQL test schema: %v", err)
	}

	t.Cleanup(func() {
		if _, err := admin.ExecContext(
			context.Background(),
			"DROP SCHEMA IF EXISTS "+schema+" CASCADE",
		); err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL test admin connection: %v", err)
		}
	})

	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse RADAR_TEST_POSTGRES_DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestNewPostgresStoreRunsMigrations(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var versions int
	if err := store.db.QueryRowContext(
		t.Context(),
		"SELECT COUNT(*) FROM radar_timeline_schema_migrations",
	).Scan(&versions); err != nil {
		t.Fatalf("query migration versions: %v", err)
	}
	if versions != 1 {
		t.Fatalf("migration versions = %d, want 1", versions)
	}

	for _, relation := range []string{
		"radar_timeline_events",
		"radar_timeline_event_seq",
		"radar_timeline_seen_resources",
	} {
		var exists bool
		if err := store.db.QueryRowContext(
			t.Context(),
			"SELECT to_regclass($1) IS NOT NULL",
			relation,
		).Scan(&exists); err != nil {
			t.Fatalf("query relation %s: %v", relation, err)
		}
		if !exists {
			t.Errorf("relation %s was not created", relation)
		}
	}
}

func TestNewPostgresStoreMigrationsAreIdempotent(t *testing.T) {
	dsn := testPostgresDSN(t)

	first, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("first NewPostgresStore: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("second NewPostgresStore: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})

	var versions int
	if err := second.db.QueryRowContext(
		t.Context(),
		"SELECT COUNT(*) FROM radar_timeline_schema_migrations",
	).Scan(&versions); err != nil {
		t.Fatalf("query migration versions: %v", err)
	}
	if versions != 1 {
		t.Fatalf("migration versions = %d, want 1", versions)
	}
}

func TestNewPostgresStoreFailsWhenRequiredTableIsMissing(t *testing.T) {
	dsn := testPostgresDSN(t)

	store, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), "DROP TABLE radar_timeline_events"); err != nil {
		_ = store.Close()
		t.Fatalf("drop timeline events table: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = NewPostgresStore(dsn)
	if err == nil {
		t.Fatal("NewPostgresStore succeeded with a missing required table")
	}
	if !strings.Contains(err.Error(), "validate PostgreSQL timeline schema") {
		t.Fatalf("NewPostgresStore error = %q, want schema validation error", err)
	}
}

func TestNewPostgresStoreConcurrentMigrations(t *testing.T) {
	dsn := testPostgresDSN(t)

	start := make(chan struct{})
	results := make(chan error, 2)
	var storesMu sync.Mutex
	var stores []*PostgresStore
	for range 2 {
		go func() {
			<-start
			store, err := NewPostgresStore(dsn)
			if err == nil {
				storesMu.Lock()
				stores = append(stores, store)
				storesMu.Unlock()
			}
			results <- err
		}()
	}
	close(start)

	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("concurrent NewPostgresStore: %v", err)
		}
	}
	t.Cleanup(func() {
		storesMu.Lock()
		defer storesMu.Unlock()
		for _, store := range stores {
			if err := store.Close(); err != nil {
				t.Errorf("Close concurrent store: %v", err)
			}
		}
	})
	if len(stores) != 2 {
		t.Fatalf("successful concurrent stores = %d, want 2", len(stores))
	}

	var versions int
	if err := stores[0].db.QueryRowContext(
		t.Context(),
		"SELECT COUNT(*) FROM radar_timeline_schema_migrations",
	).Scan(&versions); err != nil {
		t.Fatalf("query migration versions: %v", err)
	}
	if versions != 1 {
		t.Fatalf("migration versions = %d, want 1", versions)
	}
}

func TestNewPostgresStoreHydratesNULSeenResourceKey(t *testing.T) {
	dsn := testPostgresDSN(t)
	first, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("first NewPostgresStore: %v", err)
	}

	key := SeenResourceKey("cluster-a", "Deployment", "default", "api")
	if !strings.ContainsRune(key, '\x00') {
		t.Fatalf("SeenResourceKey %q does not contain NUL", key)
	}
	if _, err := first.db.ExecContext(
		t.Context(),
		`INSERT INTO radar_timeline_seen_resources (resource_key) VALUES ($1)`,
		[]byte(key),
	); err != nil {
		t.Fatalf("insert seen-resource key: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("second NewPostgresStore: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})

	second.seenMu.RLock()
	seen := second.seenResources[key]
	second.seenMu.RUnlock()
	if !seen {
		t.Fatalf("NUL-containing seen-resource key was not hydrated")
	}
}

func TestNewPostgresStoreConnectionErrorRedactsPassword(t *testing.T) {
	const (
		password = "radar-super-secret"
		dsn      = "postgres://radar:" + password + "@127.0.0.1:1/radar?sslmode=disable&connect_timeout=1"
	)

	_, err := NewPostgresStore(dsn)
	if err == nil {
		t.Fatal("NewPostgresStore returned nil error for unreachable database")
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), dsn) {
		t.Fatalf("connection error leaked credentials: %v", err)
	}
}

func TestPostgresConnectionErrorRedactsKeywordPassword(t *testing.T) {
	const password = "radar-keyword-secret"
	dsn := "host=127.0.0.1 port=1 user=radar password=" + password + " dbname=radar"
	driverErr := errors.New("driver exposed password " + password)

	err := postgresConnectionError("connect", driverErr, dsn)
	if strings.Contains(err.Error(), password) {
		t.Fatalf("connection error leaked keyword DSN password: %v", err)
	}
}

func TestNewPostgresStorePingIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	releaseConnection := make(chan struct{})
	t.Cleanup(func() {
		close(releaseConnection)
	})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		<-releaseConnection
	}()

	originalTimeout := postgresPingTimeout
	postgresPingTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		postgresPingTimeout = originalTimeout
	})

	dsn := fmt.Sprintf(
		"postgres://radar:secret@%s/radar?sslmode=disable",
		listener.Addr().String(),
	)
	started := time.Now()
	_, err = NewPostgresStore(dsn)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("NewPostgresStore returned nil error for unresponsive server")
	}
	if elapsed > time.Second {
		t.Fatalf("NewPostgresStore took %s, want bounded ping under 1s", elapsed)
	}
}

func TestPostgresStore_AppendBatch(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	events := []TimelineEvent{
		{ID: "append-a", Timestamp: base, Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
		{ID: "append-b", Timestamp: base.Add(time.Second), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeUpdate},
	}
	if err := store.AppendBatch(t.Context(), events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM radar_timeline_events").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 2 {
		t.Fatalf("event count = %d, want 2", count)
	}
}

func TestPostgresStore_AppendBatchRollsBackOnContextCancellation(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	events := []TimelineEvent{
		{ID: "rollback-a", Timestamp: time.Now(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
	}
	if err := store.AppendBatch(ctx, events); err == nil {
		t.Fatal("AppendBatch with cancelled context returned nil error")
	}

	var count int
	if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM radar_timeline_events").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 0 {
		t.Fatalf("event count = %d, want 0 after rollback", count)
	}
}

func TestPostgresStore_AppendDuplicateInformerNoOp(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	first := TimelineEvent{ID: "dup-1", Timestamp: base, Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd, Reason: "created"}
	relist := TimelineEvent{ID: "dup-1", Timestamp: base.Add(time.Minute), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeUpdate, Reason: "relist"}

	if err := store.AppendBatch(t.Context(), []TimelineEvent{first, relist}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	var reason, eventType string
	var count int
	if err := store.db.QueryRowContext(
		t.Context(),
		"SELECT reason, event_type, COUNT(*) FROM radar_timeline_events WHERE id = $1 GROUP BY reason, event_type",
		"dup-1",
	).Scan(&reason, &eventType, &count); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
	if reason != "created" || eventType != string(EventTypeAdd) {
		t.Fatalf("original row was mutated: reason=%q event_type=%q", reason, eventType)
	}
}

func TestPostgresStore_AppendK8sEventUpsert(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	born := base.Add(-time.Hour)
	first := TimelineEvent{
		ID: "evt-upsert-1", Timestamp: base, Source: SourceK8sEvent, Kind: "Pod", Namespace: "default", Name: "web-0",
		EventType: EventTypeWarning, Reason: "BackOff", Message: "back-off 20s", Count: 1,
		CreatedAt: &born,
		Owner:     &OwnerInfo{Kind: "ReplicaSet", Name: "web-rs"},
		Labels:    map[string]string{"app": "web"},
	}
	bump := TimelineEvent{
		ID: "evt-upsert-1", Timestamp: base.Add(time.Minute), Source: SourceK8sEvent, Kind: "Pod", Namespace: "default", Name: "web-0",
		EventType: EventTypeWarning, Reason: "BackOff", Message: "back-off 40s", Count: 5,
		// intentionally omit CreatedAt, Owner, Labels to verify enrichment preservation
	}

	if err := store.AppendBatch(t.Context(), []TimelineEvent{first}); err != nil {
		t.Fatalf("append first: %v", err)
	}
	var firstSeq int64
	if err := store.db.QueryRowContext(t.Context(), "SELECT seq FROM radar_timeline_events WHERE id = $1", "evt-upsert-1").Scan(&firstSeq); err != nil {
		t.Fatalf("query first seq: %v", err)
	}

	if err := store.AppendBatch(t.Context(), []TimelineEvent{bump}); err != nil {
		t.Fatalf("append bump: %v", err)
	}

	var count int32
	var message string
	var seq int64
	var createdAtNanos int64
	var ownerKind, ownerName string
	var labels []byte
	if err := store.db.QueryRowContext(
		t.Context(),
		`SELECT count, message, seq, resource_created_at, owner_kind, owner_name, labels_json
		 FROM radar_timeline_events WHERE id = $1`,
		"evt-upsert-1",
	).Scan(&count, &message, &seq, &createdAtNanos, &ownerKind, &ownerName, &labels); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}
	if message != "back-off 40s" {
		t.Fatalf("message = %q, want back-off 40s", message)
	}
	if seq <= firstSeq {
		t.Fatalf("seq did not advance: %d <= %d", seq, firstSeq)
	}
	if createdAt := time.Unix(0, createdAtNanos).UTC(); !createdAt.Equal(born) {
		t.Fatalf("created_at lost enrichment: %v want %v", createdAt, born)
	}
	if ownerKind != "ReplicaSet" || ownerName != "web-rs" {
		t.Fatalf("owner lost enrichment: %s/%s", ownerKind, ownerName)
	}
	if string(labels) == "" {
		t.Fatalf("labels lost enrichment")
	}
}

func TestPostgresStore_AppendK8sEventStalenessGuard(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	newer := TimelineEvent{ID: "evt-stale-1", Timestamp: base.Add(time.Minute), Source: SourceK8sEvent, Kind: "Pod", Namespace: "default", Name: "web-0", EventType: EventTypeWarning, Reason: "BackOff", Message: "newer", Count: 5}
	older := TimelineEvent{ID: "evt-stale-1", Timestamp: base, Source: SourceK8sEvent, Kind: "Pod", Namespace: "default", Name: "web-0", EventType: EventTypeWarning, Reason: "BackOff", Message: "older", Count: 1}

	if err := store.AppendBatch(t.Context(), []TimelineEvent{newer}); err != nil {
		t.Fatalf("append newer: %v", err)
	}
	if err := store.AppendBatch(t.Context(), []TimelineEvent{older}); err != nil {
		t.Fatalf("append older: %v", err)
	}

	var count int32
	var message string
	if err := store.db.QueryRowContext(t.Context(), "SELECT count, message FROM radar_timeline_events WHERE id = $1", "evt-stale-1").Scan(&count, &message); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if count != 5 || message != "newer" {
		t.Fatalf("stale relay clobbered newer row: count=%d message=%q", count, message)
	}
}

func TestPostgresStore_AppendRestartSequenceContinuity(t *testing.T) {
	dsn := testPostgresDSN(t)
	store1, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("first NewPostgresStore: %v", err)
	}
	if err := store1.AppendBatch(t.Context(), []TimelineEvent{{ID: "seq-a", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	var seq1 int64
	if err := store1.db.QueryRowContext(t.Context(), "SELECT seq FROM radar_timeline_events WHERE id = $1", "seq-a").Scan(&seq1); err != nil {
		t.Fatalf("query seq1: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("second NewPostgresStore: %v", err)
	}
	defer store2.Close()
	if err := store2.AppendBatch(t.Context(), []TimelineEvent{{ID: "seq-b", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	var seq2 int64
	if err := store2.db.QueryRowContext(t.Context(), "SELECT seq FROM radar_timeline_events WHERE id = $1", "seq-b").Scan(&seq2); err != nil {
		t.Fatalf("query seq2: %v", err)
	}
	if seq2 <= seq1 {
		t.Fatalf("sequence did not advance after restart: %d <= %d", seq2, seq1)
	}
}

func TestPostgresStore_AppendConcurrentStores(t *testing.T) {
	dsn := testPostgresDSN(t)
	store1, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("first NewPostgresStore: %v", err)
	}
	defer store1.Close()
	store2, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("second NewPostgresStore: %v", err)
	}
	defer store2.Close()

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := store1
			if i%2 == 1 {
				store = store2
			}
			e := TimelineEvent{
				ID: fmt.Sprintf("concurrent-%d", i), Timestamp: time.Now().UTC(), Source: SourceInformer,
				Kind: "Deployment", Namespace: "default", Name: fmt.Sprintf("web-%d", i), EventType: EventTypeAdd,
			}
			if err := store.Append(t.Context(), e); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	var count int
	if err := store1.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM radar_timeline_events").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != n {
		t.Fatalf("event count = %d, want %d", count, n)
	}
}

func TestPostgresStore_AppendSerializationBlocksConcurrentWriter(t *testing.T) {
	dsn := testPostgresDSN(t)
	store0, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := store0.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open connection: %v", err)
	}
	defer conn.Close()

	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(
		t.Context(),
		"SELECT pg_advisory_xact_lock($1, $2)",
		postgresAppendLockNamespace,
		postgresAppendLockID,
	); err != nil {
		t.Fatalf("acquire lock: %v", err)
	}

	store, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore writer: %v", err)
	}
	defer store.Close()

	done := make(chan error, 1)
	go func() {
		e := TimelineEvent{ID: "blocked", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd}
		done <- store.Append(context.Background(), e)
	}()

	select {
	case <-done:
		t.Fatal("append completed while holding the serialization lock")
	case <-time.After(200 * time.Millisecond):
		// expected
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback lock tx: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("append did not complete after lock release")
	}
}

func TestPostgresStore_QueryAllOptions(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	events := []TimelineEvent{
		{ID: "q-a", Timestamp: base, Source: SourceInformer, Kind: "Deployment", Namespace: "ns1", Name: "web", EventType: EventTypeAdd, ClusterContext: "ctx1"},
		{ID: "q-b", Timestamp: base.Add(time.Minute), Source: SourceInformer, Kind: "Deployment", Namespace: "ns2", Name: "web", EventType: EventTypeUpdate, ClusterContext: "ctx1"},
		{ID: "q-c", Timestamp: base.Add(2 * time.Minute), Source: SourceK8sEvent, Kind: "Pod", Namespace: "ns1", Name: "pod-0", EventType: EventTypeWarning, ClusterContext: "ctx2"},
		{ID: "q-d", Timestamp: base.Add(3 * time.Minute), Source: SourceInformer, Kind: "Service", Namespace: "ns1", Name: "svc", EventType: EventTypeDelete, ClusterContext: "ctx1"},
	}
	if err := store.AppendBatch(t.Context(), events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	cases := []struct {
		name     string
		opts     QueryOptions
		wantIDs  []string
		wantMore bool
	}{
		{
			name:    "namespace filter",
			opts:    QueryOptions{Namespaces: []string{"ns1"}, Limit: 10, IncludeK8sEvents: true},
			wantIDs: []string{"q-d", "q-c", "q-a"},
		},
		{
			name:    "kind filter",
			opts:    QueryOptions{Kinds: []string{"Deployment"}, Limit: 10, IncludeK8sEvents: true},
			wantIDs: []string{"q-b", "q-a"},
		},
		{
			name:    "name filter",
			opts:    QueryOptions{Names: []string{"web"}, Limit: 10, IncludeK8sEvents: true},
			wantIDs: []string{"q-b", "q-a"},
		},
		{
			name:    "source filter",
			opts:    QueryOptions{Sources: []EventSource{SourceK8sEvent}, Limit: 10, IncludeK8sEvents: true},
			wantIDs: []string{"q-c"},
		},
		{
			name:    "event type filter",
			opts:    QueryOptions{EventTypes: []EventType{EventTypeDelete}, Limit: 10, IncludeK8sEvents: true},
			wantIDs: []string{"q-d"},
		},
		{
			name:    "cluster context filter",
			opts:    QueryOptions{ClusterContext: "ctx2", Limit: 10, IncludeK8sEvents: true},
			wantIDs: []string{"q-c"},
		},
		{
			name:    "exclude deleted",
			opts:    QueryOptions{Limit: 10, ExcludeDeleted: true, IncludeK8sEvents: true},
			wantIDs: []string{"q-c", "q-b", "q-a"},
		},
		{
			name:    "exclude k8s events",
			opts:    QueryOptions{Limit: 10, IncludeK8sEvents: false},
			wantIDs: []string{"q-d", "q-b", "q-a"},
		},
		{
			name:    "time range",
			opts:    QueryOptions{Limit: 10, Since: base.Add(30 * time.Second), Until: base.Add(90 * time.Second), IncludeK8sEvents: true},
			wantIDs: []string{"q-b"},
		},
		{
			name:     "limit",
			opts:     QueryOptions{Limit: 2, IncludeK8sEvents: true},
			wantIDs:  []string{"q-d", "q-c"},
			wantMore: true,
		},
		{
			name:    "offset",
			opts:    QueryOptions{Limit: 10, Offset: 1, IncludeK8sEvents: true},
			wantIDs: []string{"q-c", "q-b", "q-a"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.opts.Limit == 0 {
				tc.opts.Limit = 200
			}
			tc.opts.IncludeManaged = true
			got, err := store.Query(t.Context(), tc.opts)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("got %d events, want %d: %+v", len(got), len(tc.wantIDs), idsOf(got))
			}
			for i, id := range tc.wantIDs {
				if got[i].ID != id {
					t.Fatalf("event[%d].ID = %q, want %q", i, got[i].ID, id)
				}
			}
		})
	}
}

func TestPostgresStore_QuerySeqPaging(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	for i := range 5 {
		e := TimelineEvent{
			ID: fmt.Sprintf("seqp-%d", i), Timestamp: base.Add(time.Duration(i) * time.Second),
			Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: fmt.Sprintf("web-%d", i), EventType: EventTypeAdd,
		}
		if err := store.Append(t.Context(), e); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	cursor := int64(0)
	var order []string
	for range 10 {
		page, err := store.Query(t.Context(), QueryOptions{Limit: 2, SinceSeq: cursor, SeqPaging: true, IncludeManaged: true})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if e.Seq <= cursor {
				t.Fatalf("seq %d not after cursor %d", e.Seq, cursor)
			}
			order = append(order, e.ID)
			cursor = e.Seq
		}
	}
	want := []string{"seqp-0", "seqp-1", "seqp-2", "seqp-3", "seqp-4"}
	if len(order) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(order), len(want), order)
	}
	for i, id := range want {
		if order[i] != id {
			t.Fatalf("order[%d] = %q, want %q", i, order[i], id)
		}
	}
}

func TestPostgresStore_GetEvent(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	e := TimelineEvent{ID: "get-1", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd, Message: "hello"}
	if err := store.Append(t.Context(), e); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.GetEvent(t.Context(), "get-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got == nil {
		t.Fatal("GetEvent returned nil for existing event")
	}
	if got.Message != "hello" {
		t.Fatalf("GetEvent message = %q, want hello", got.Message)
	}

	missing, err := store.GetEvent(t.Context(), "does-not-exist")
	if err != nil {
		t.Fatalf("GetEvent missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("GetEvent missing = %+v, want nil", missing)
	}
}

func TestPostgresStore_GetChangesForOwner(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	events := []TimelineEvent{
		{ID: "owner-a", Timestamp: base, Source: SourceInformer, Kind: "Pod", Namespace: "default", Name: "pod-0", EventType: EventTypeAdd, Owner: &OwnerInfo{Kind: "ReplicaSet", Name: "rs-1"}},
		{ID: "owner-b", Timestamp: base.Add(time.Minute), Source: SourceInformer, Kind: "Pod", Namespace: "default", Name: "pod-1", EventType: EventTypeAdd, Owner: &OwnerInfo{Kind: "ReplicaSet", Name: "rs-1"}},
		{ID: "owner-c", Timestamp: base.Add(2 * time.Minute), Source: SourceInformer, Kind: "Pod", Namespace: "default", Name: "pod-2", EventType: EventTypeAdd, Owner: &OwnerInfo{Kind: "ReplicaSet", Name: "rs-2"}},
	}
	if err := store.AppendBatch(t.Context(), events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	got, err := store.GetChangesForOwner(t.Context(), "ReplicaSet", "default", "rs-1", "", time.Time{}, 10)
	if err != nil {
		t.Fatalf("GetChangesForOwner: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	for _, e := range got {
		if e.Owner == nil || e.Owner.Name != "rs-1" {
			t.Fatalf("unexpected event: %+v", e)
		}
	}
}

func TestPostgresStore_SeenResourcesPersist(t *testing.T) {
	dsn := testPostgresDSN(t)
	store1, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("first NewPostgresStore: %v", err)
	}
	store1.MarkResourceSeen("ctx1", "Deployment", "default", "web")
	if err := store1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store2, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("second NewPostgresStore: %v", err)
	}
	defer store2.Close()

	if !store2.IsResourceSeen("ctx1", "Deployment", "default", "web") {
		t.Fatal("seen resource was not persisted")
	}

	store2.ClearResourceSeen("ctx1", "Deployment", "default", "web")
	if store2.IsResourceSeen("ctx1", "Deployment", "default", "web") {
		t.Fatal("seen resource was not cleared")
	}

	store3, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("third NewPostgresStore: %v", err)
	}
	defer store3.Close()
	if store3.IsResourceSeen("ctx1", "Deployment", "default", "web") {
		t.Fatal("cleared seen resource reappeared after restart")
	}
}

func TestPostgresStore_Stats(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	if err := store.AppendBatch(t.Context(), []TimelineEvent{
		{ID: "stats-a", Timestamp: base, Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
		{ID: "stats-b", Timestamp: base.Add(time.Hour), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeUpdate},
	}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	stats := store.Stats()
	if stats.TotalEvents != 2 {
		t.Fatalf("TotalEvents = %d, want 2", stats.TotalEvents)
	}
	if stats.SeenResources != 0 {
		t.Fatalf("SeenResources = %d, want 0", stats.SeenResources)
	}
	if stats.OldestEvent.IsZero() || stats.NewestEvent.IsZero() {
		t.Fatal("OldestEvent/NewestEvent not set")
	}
	if stats.StorageBytes <= 0 {
		t.Fatalf("StorageBytes = %d, want > 0", stats.StorageBytes)
	}
}

func TestPostgresStore_Cleanup(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	if err := store.AppendBatch(t.Context(), []TimelineEvent{
		{ID: "clean-old", Timestamp: base.Add(-2 * time.Hour), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
		{ID: "clean-new", Timestamp: base, Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
	}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	deleted, err := store.Cleanup(t.Context(), time.Hour)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	var count int
	if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM radar_timeline_events").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestPostgresStore_CleanupCancellation(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	var events []TimelineEvent
	for i := range 2500 {
		events = append(events, TimelineEvent{
			ID: fmt.Sprintf("cancel-%d", i), Timestamp: base.Add(-time.Duration(i+2) * time.Hour),
			Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: fmt.Sprintf("web-%d", i), EventType: EventTypeAdd,
		})
	}
	if err := store.AppendBatch(t.Context(), events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.Cleanup(ctx, time.Hour)
	if err == nil {
		t.Fatal("Cleanup with cancelled context returned nil error")
	}
}

func TestPostgresStore_StartCleanupLoop(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer store.Close()

	base := time.Now().UTC()
	if err := store.AppendBatch(t.Context(), []TimelineEvent{
		{ID: "loop-old", Timestamp: base.Add(-2 * time.Hour), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
		{ID: "loop-new", Timestamp: base, Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd},
	}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	store.StartCleanupLoop(time.Hour, 50*time.Millisecond, 0)

	var count int
	for range 100 {
		time.Sleep(20 * time.Millisecond)
		if err := store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM radar_timeline_events").Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count == 1 {
			break
		}
	}
	if count != 1 {
		t.Fatalf("cleanup loop did not remove old event: count = %d", count)
	}
}

func TestStoreConformance_Postgres(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) EventStore {
		t.Helper()
		store, err := NewPostgresStore(testPostgresDSN(t))
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
		return store
	})
}

func idsOf(events []TimelineEvent) []string {
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids
}

// TestPostgresSuiteEnabled fails when the suite is required but would skip.
//
// Every PostgreSQL test calls testPostgresDSN, which skips when
// RADAR_TEST_POSTGRES_DSN is unset. A skipped test reports success, so a CI job
// that loses the DSN — a renamed service, a dropped env block — would keep
// reporting green with the whole backend untested. CI sets
// RADAR_REQUIRE_POSTGRES_TESTS so that silence becomes a failure.
func TestPostgresSuiteEnabled(t *testing.T) {
	if os.Getenv("RADAR_REQUIRE_POSTGRES_TESTS") == "" {
		t.Skip("RADAR_REQUIRE_POSTGRES_TESTS is not set")
	}
	if os.Getenv("RADAR_TEST_POSTGRES_DSN") == "" {
		t.Fatal("RADAR_REQUIRE_POSTGRES_TESTS is set but RADAR_TEST_POSTGRES_DSN is empty: the PostgreSQL suite would skip silently")
	}
}

// TestPostgresStore_ColumnRoundTrip pins the encodings that differ from the
// SQLite store: diff and labels go to jsonb rather than TEXT, and both
// timestamps go to bigint epoch nanoseconds rather than a fixed-width string.
// Nothing else reads these columns back through the store surface, so a
// silent encoding change would otherwise only surface as missing detail in the
// UI.
func TestPostgresStore_ColumnRoundTrip(t *testing.T) {
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	// Deliberately not round microseconds: timestamptz would truncate these and
	// the assertions below would fail.
	stamp := time.Unix(1788506383, 819945123).UTC()
	born := time.Unix(1788500000, 456789321).UTC()
	want := TimelineEvent{
		ID: "roundtrip-1", Timestamp: stamp, Source: SourceInformer,
		Kind: "Deployment", APIVersion: "apps/v1", Namespace: "default", Name: "web",
		UID: "uid-1", EventType: EventTypeUpdate, Reason: "Scaled",
		Message: "scaled up", HealthState: HealthHealthy, CreatedAt: &born,
		CorrelationID: "corr-1", ClusterContext: "kind-test",
		Owner:  &OwnerInfo{Kind: "ReplicaSet", Name: "web-rs"},
		Labels: map[string]string{"app": "web", "tier": "front"},
		Diff: &DiffInfo{
			Summary: "replicas 1 -> 3",
			Fields:  []FieldChange{{Path: "spec.replicas", OldValue: "1", NewValue: "3"}},
		},
	}
	if err := store.Append(t.Context(), want); err != nil {
		t.Fatalf("Append: %v", err)
	}

	assert := func(where string, got *TimelineEvent) {
		t.Helper()
		if !got.Timestamp.Equal(stamp) {
			t.Errorf("%s: timestamp = %v (%d ns), want %v (%d ns)",
				where, got.Timestamp, got.Timestamp.UnixNano(), stamp, stamp.UnixNano())
		}
		if got.CreatedAt == nil || !got.CreatedAt.Equal(born) {
			t.Errorf("%s: createdAt = %v, want %v", where, got.CreatedAt, born)
		}
		if got.Diff == nil {
			t.Fatalf("%s: diff lost", where)
		}
		if got.Diff.Summary != want.Diff.Summary || len(got.Diff.Fields) != 1 {
			t.Errorf("%s: diff = %+v, want %+v", where, got.Diff, want.Diff)
		} else if f := got.Diff.Fields[0]; f.Path != "spec.replicas" || f.OldValue != "1" || f.NewValue != "3" {
			t.Errorf("%s: diff field = %+v", where, f)
		}
		if got.Labels["app"] != "web" || got.Labels["tier"] != "front" || len(got.Labels) != 2 {
			t.Errorf("%s: labels = %v, want %v", where, got.Labels, want.Labels)
		}
		if got.Owner == nil || got.Owner.Kind != "ReplicaSet" || got.Owner.Name != "web-rs" {
			t.Errorf("%s: owner = %+v", where, got.Owner)
		}
		if got.APIVersion != "apps/v1" || got.UID != "uid-1" || got.CorrelationID != "corr-1" ||
			got.ClusterContext != "kind-test" || got.HealthState != HealthHealthy {
			t.Errorf("%s: scalar column lost: %+v", where, got)
		}
	}

	got, err := store.GetEvent(t.Context(), "roundtrip-1")
	if err != nil || got == nil {
		t.Fatalf("GetEvent: %v %+v", err, got)
	}
	assert("GetEvent", got)

	events, err := store.Query(t.Context(), QueryOptions{
		Limit: 10, IncludeManaged: true, IncludeK8sEvents: true,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Query returned %d events, want 1", len(events))
	}
	assert("Query", &events[0])
}
