package timeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// dbProxy forwards TCP to the test database and can fail it two ways: down()
// cuts open connections and refuses new ones; stall() keeps every socket open
// and forwards nothing, like a database that stops answering.
type dbProxy struct {
	t      *testing.T
	target string
	addr   string

	mu      sync.Mutex
	ln      net.Listener
	conns   []net.Conn
	servers []net.Conn // the database side of each link; stall() cuts only these
	stalled bool
}

func startDBProxy(t *testing.T, target string) *dbProxy {
	t.Helper()
	p := &dbProxy{t: t, target: target}
	p.up("127.0.0.1:0")
	t.Cleanup(p.down)
	return p
}

func (p *dbProxy) up(addr string) {
	p.t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p.t.Fatalf("proxy listen: %v", err)
	}
	p.mu.Lock()
	p.ln, p.addr = ln, ln.Addr().String()
	p.mu.Unlock()
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			stalled := p.stalled
			p.conns = append(p.conns, client)
			p.mu.Unlock()
			if stalled {
				continue
			}
			server, err := net.Dial("tcp", p.target)
			if err != nil {
				client.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, server)
			p.servers = append(p.servers, server)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(server, client); server.Close() }()
			go func() {
				_, _ = io.Copy(client, server)
				p.mu.Lock()
				defer p.mu.Unlock()
				if !p.stalled {
					client.Close()
				}
			}()
		}
	}()
}

// stall cuts the link to the database but leaves the client side of every
// connection open, so reads block instead of failing.
func (p *dbProxy) stall() {
	p.mu.Lock()
	p.stalled = true
	servers := p.servers
	p.servers = nil
	p.mu.Unlock()
	for _, c := range servers {
		c.Close()
	}
}

func (p *dbProxy) down() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		p.ln.Close()
		p.ln = nil
	}
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}

// A database that goes away after a good start must read as unreachable, both
// to queries and to the reachability check diagnostics uses, and the same store
// must work again once the database is back, without being reopened.
func TestPostgresStoreReportsAnOutageAndRecovers(t *testing.T) {
	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr
	q := dsn.Query()
	q.Set("connect_timeout", "2")
	dsn.RawQuery = q.Encode()

	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := t.Context()
	event := TimelineEvent{ID: "before", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeAdd}
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append before the outage: %v", err)
	}

	proxy.down()

	_, err = store.Query(ctx, QueryOptions{Limit: 10})
	if err == nil {
		t.Fatal("Query succeeded with the database gone")
	}
	if !DatabaseUnreachable(err) {
		t.Fatalf("Query error %q not classed as unreachable; the page would show a generic failure", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.Ping(pingCtx)
	cancel()
	if !DatabaseUnreachable(err) {
		t.Fatalf("Ping error %v not classed as unreachable; diagnostics would report a healthy, empty timeline", err)
	}

	proxy.up(proxy.addr)

	deadline := time.Now().Add(10 * time.Second)
	for {
		events, err := store.Query(ctx, QueryOptions{Limit: 10})
		if err == nil {
			if len(events) != 1 || events[0].ID != "before" {
				t.Fatalf("after recovery got %d events, want the one written before the outage", len(events))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("store did not recover after the database came back: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := store.Append(ctx, TimelineEvent{ID: "after", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeUpdate}); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
}

// A database that stops answering, rather than refusing, must still fail the
// reachability check within its budget. Diagnostics relies on that to report the
// outage instead of waiting out every stats query and showing an empty store.
func TestPostgresStorePingFailsWhenTheDatabaseStopsAnswering(t *testing.T) {
	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr

	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	proxy.stall()

	const budget = 2 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	start := time.Now()
	err = store.Ping(ctx)
	if err == nil {
		t.Fatal("Ping succeeded against a database that is not answering")
	}
	if elapsed := time.Since(start); elapsed > budget+2*time.Second {
		t.Fatalf("Ping took %s, past its %s budget; diagnostics would stall", elapsed, budget)
	}
}

func TestDatabaseUnreachableIgnoresOtherErrors(t *testing.T) {
	if DatabaseUnreachable(errors.New("query events: syntax error")) {
		t.Fatal("a rejected query was classed as an unreachable database")
	}
	if DatabaseUnreachable(nil) {
		t.Fatal("nil classed as unreachable")
	}
	if DatabaseUnreachable(&pgconn.PgError{Code: "42601"}) {
		t.Fatal("a syntax error from the server was classed as an unreachable database")
	}
	if !DatabaseUnreachable(fmt.Errorf("query events: %w", &pgconn.PgError{Code: "57P01"})) {
		t.Fatal("the server terminating the connection was not classed as unreachable")
	}
}

// A resource deleted while no store is installed keeps its seen mark in the
// database. Once the store reconnects that mark would make a new resource with
// the same name look already seen, and its creation would be dropped. The
// reconnect has to clear it, in memory and in the database.
func TestReconnectClearsMarksOfResourcesDeletedDuringTheOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	dsn := testPostgresDSN(t)

	before, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	before.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "web")
	before.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "api")
	if err := before.Close(); err != nil { // Close drains the queued marks.
		t.Fatalf("Close: %v", err)
	}

	NoteDeletedWhileUnavailable("ctx", "apps", "Deployment", "shop", "web")

	reconnected, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if !reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "web") {
		t.Fatal("setup: the stale mark was not loaded from the database")
	}
	installPostgresStore(reconnected, StoreConfig{})

	if reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "web") {
		t.Fatal("the deleted resource is still marked seen; recreating it would not be recorded")
	}
	if !reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "api") {
		t.Fatal("a resource that was not deleted lost its mark")
	}

	ResetStore()
	fresh, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if fresh.IsResourceSeen("ctx", "apps", "Deployment", "shop", "web") {
		t.Fatal("the mark is gone in memory but still in the database; the next restart would bring it back")
	}
}

// A delete whose caller saw no store, but that is noted only after the
// reconnect's last pass over the list, must still clear its mark.
func TestDeleteNotedAfterTheReconnectStillClearsItsMark(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	dsn := testPostgresDSN(t)

	before, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	before.MarkResourceSeen("ctx", "apps", "Deployment", "shop", "late")
	if err := before.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reconnected, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(reconnected, StoreConfig{})
	t.Cleanup(ResetStore) // before the test schema is dropped
	NoteDeletedWhileUnavailable("ctx", "apps", "Deployment", "shop", "late")

	if reconnected.IsResourceSeen("ctx", "apps", "Deployment", "shop", "late") {
		t.Fatal("a delete noted after the reconnect left its mark; recreating it would not be recorded")
	}
	if n := DeletedWhileUnavailableCountForTest(); n != 0 {
		t.Fatalf("%d delete(s) left waiting for a reconnect that already happened", n)
	}
}

// Deletes from a cluster context that has been switched away from must not be
// applied to the next one's store.
func TestResetStoreDropsDeletesNotedDuringTheOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	NoteDeletedWhileUnavailable("old", "apps", "Deployment", "shop", "web")
	ResetStore()
	if keys, _ := takeDeletedWhileUnavailable(); len(keys) != 0 {
		t.Fatalf("ResetStore kept %d delete(s) from the previous context", len(keys))
	}
}

// A database that stops answering times a query out rather than failing its
// connection, so the query error alone does not say "down". The availability
// check that follows a timeout has to.
func TestStalledQueryTimesOutAndTheCheckReportsTheOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr
	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})
	// Registered after the test database's own cleanup, so it runs first: the
	// schema can't be dropped while this store is still building indexes.
	t.Cleanup(ResetStore)

	proxy.stall()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	_, err = store.Query(ctx, QueryOptions{Limit: 10})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled query returned %v, want a timeout", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if Unavailable(ctx) == nil {
		t.Fatal("availability check passed with the database not answering")
	}
}

// syncBuffer is a log sink the outage watcher's goroutine can write to while
// the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	out := &syncBuffer{}
	log.SetOutput(out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return out
}

func waitForLog(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log never contained %q:\n%s", want, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The timeline page sends operators to the logs. A database that goes away
// after a good start has to show there once when it is lost, naming the
// database, and once when it answers again, even if nothing else writes in
// between.
func TestRuntimeOutageIsLoggedWithTheDatabaseAndItsRecovery(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	prevInterval := postgresOutageCheckInterval
	postgresOutageCheckInterval = 100 * time.Millisecond
	t.Cleanup(func() { postgresOutageCheckInterval = prevInterval })

	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr
	q := dsn.Query()
	q.Set("connect_timeout", "2")
	dsn.RawQuery = q.Encode()
	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})
	// Registered after the test database's own cleanup, so it runs first: the
	// schema can't be dropped while this store is still building indexes.
	t.Cleanup(ResetStore)
	out := captureLog(t)

	proxy.down()
	event := TimelineEvent{ID: "lost", Timestamp: time.Now().UTC(), Source: SourceInformer, Kind: "Deployment", Namespace: "default", Name: "web", EventType: EventTypeUpdate}
	for range 3 {
		if err := RecordEvent(t.Context(), event); err == nil {
			t.Fatal("RecordEvent succeeded with the database gone")
		}
	}
	waitForLog(t, out, "[timeline] lost the connection to PostgreSQL (host=127.0.0.1 port=")
	for _, want := range []string{"database=radar user=radar", "Checking again in 100ms", "connection refused"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("outage line lacks %q:\n%s", want, out.String())
		}
	}
	if n := strings.Count(out.String(), "lost the connection"); n != 1 {
		t.Fatalf("outage logged %d times, want 1:\n%s", n, out.String())
	}
	// The test password matches the user name, so look for it the way a DSN
	// carries it rather than on its own.
	if pw, ok := dsn.User.Password(); ok && (strings.Contains(out.String(), ":"+pw+"@") || strings.Contains(out.String(), "password=")) {
		t.Fatalf("log carries the database password:\n%s", out.String())
	}

	proxy.up(proxy.addr)
	waitForLog(t, out, "[timeline] PostgreSQL (host=127.0.0.1 port=")
	if !strings.Contains(out.String(), "is answering again after") {
		t.Fatalf("recovery line missing:\n%s", out.String())
	}
}

// The alerting metric and diagnostics check the database with no timeline
// traffic at all. When one of those checks finds it gone, the logs have to say
// so too, or an alert sends the operator to logs with nothing in them.
func TestOutageFoundByTheHealthCheckIsLogged(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	prevInterval := postgresOutageCheckInterval
	postgresOutageCheckInterval = 100 * time.Millisecond
	t.Cleanup(func() { postgresOutageCheckInterval = prevInterval })

	dsn, err := url.Parse(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	proxy := startDBProxy(t, dsn.Host)
	dsn.Host = proxy.addr
	q := dsn.Query()
	q.Set("connect_timeout", "2")
	dsn.RawQuery = q.Encode()
	store, err := NewPostgresStore(dsn.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})
	// Registered after the test database's own cleanup, so it runs first: the
	// schema can't be dropped while this store is still building indexes.
	t.Cleanup(ResetStore)
	out := captureLog(t)

	proxy.down()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	err = Unavailable(ctx)
	cancel()
	if err == nil {
		t.Fatal("Unavailable reported a working timeline with the database gone")
	}
	waitForLog(t, out, "[timeline] lost the connection to PostgreSQL")

	proxy.up(proxy.addr)
	waitForLog(t, out, "is answering again after")
}

// A query the server rejected is not an outage, and neither is a timeout or a
// dropped pooled connection when the database still answers a ping.
func TestRejectedQueriesAndOneOffFailuresAreNotLoggedAsAnOutage(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	store, err := NewPostgresStore(testPostgresDSN(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	installPostgresStore(store, StoreConfig{})
	// Registered after the test database's own cleanup, so it runs first: the
	// schema can't be dropped while this store is still building indexes.
	t.Cleanup(ResetStore)
	out := captureLog(t)

	for _, failure := range []error{
		&pgconn.PgError{Code: "23505"},
		fmt.Errorf("query: %w", context.DeadlineExceeded),
		fmt.Errorf("query: %w", io.ErrUnexpectedEOF),
	} {
		NoteDatabaseError(store, failure)
		deadline := time.Now().Add(15 * time.Second)
		for {
			postgresOutageMu.Lock()
			watching := postgresOutageStore != nil
			postgresOutageMu.Unlock()
			if !watching {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the check after %v never finished", failure)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if strings.Contains(out.String(), "lost the connection") {
			t.Fatalf("%v with a healthy database was logged as an outage:\n%s", failure, out.String())
		}
	}
}

// At startup the log line is the operator's lead: it says what is off, when the
// next attempt is, and which database failed, without the password.
func TestStartupOutageLogNamesTheDatabaseAndTheNextAttempt(t *testing.T) {
	ResetStore()
	t.Cleanup(ResetStore)
	out := captureLog(t)
	if err := InitStore(StoreConfig{Type: StoreTypePostgres, DSN: "postgres://radar:s3cret-pw@127.0.0.1:1/radar?connect_timeout=2"}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	line := out.String()
	for _, want := range []string{"[timeline] can't connect to PostgreSQL (host=127.0.0.1 port=1 database=radar user=radar)", "Retrying in 5s", "connect"} {
		if !strings.Contains(line, want) {
			t.Fatalf("startup log lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "s3cret-pw") {
		t.Fatalf("startup log carries the password:\n%s", line)
	}
}
