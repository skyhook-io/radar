// Package wiretest exports the timeline events WIRE conformance suite: the
// HTTP-level invariants every server speaking the timeline contract must hold,
// driven as real requests through the server's own http.Handler. It mirrors
// the storetest idiom (pkg/timeline/storetest) — the properties live once,
// here, and each server (radar OSS from internal/server, radar-hub from its
// own handler) supplies its live handler plus a seed hook and runs the same
// suite.
//
// The two servers diverge deliberately (OSS cursor is "<epoch>:<seq>" and
// rejects a foreign cursor with 400; hub cursor is a single nanosecond
// frontier and replays a stale one; hub emits coverage/error records and caps
// at a higher row count; hub defaults an absent window while OSS requires it).
// Every such divergence is a DECLARED capability on Config, never a hard-coded
// expectation — the suite asserts the shared invariants unconditionally and
// gates the divergent cases on the capability flags.
package wiretest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	timeline "github.com/skyhook-io/radar/pkg/timeline"
	"github.com/skyhook-io/radar/pkg/timelineapi"
)

// CursorKind declares how a server mints its opaque delta cursor. The suite
// treats the cursor as opaque for round-tripping, but uses this to assert the
// cursor's structural shape and to synthesize a guaranteed-foreign cursor.
type CursorKind int

const (
	// CursorEpochSeq is radar OSS's "<epoch>:<seq>" cursor — an observation
	// epoch and a monotonic arrival counter.
	CursorEpochSeq CursorKind = iota
	// CursorNanoFrontier is radar-hub's single nanosecond event-time frontier.
	CursorNanoFrontier
)

// ModeEvents is the timeline events endpoint mode passed to Config.PathFor.
const ModeEvents = "events"

// Config wires one server's live handler into the suite and declares its
// capabilities. The shared invariants hold regardless; the capability flags
// gate the deliberately-divergent cases.
type Config struct {
	// Handler is the server's live http.Handler for the timeline endpoint.
	Handler http.Handler
	// PathFor returns the request path for a mode (currently only ModeEvents).
	// OSS returns "/api/timeline/events"; hub returns "/c/{id}/api/timeline/events".
	PathFor func(mode string) string
	// Seed loads fixture events into the server's live store. It is called more
	// than once; each call adds to what is already stored. The server-specific
	// stamping (cluster context, etc.) belongs here, keeping the suite k8s-free.
	Seed func(t *testing.T, events []timeline.TimelineEvent)
	// Query is applied to every request (optional). OSS uses it to carry the
	// namespace filter ("namespaces=default") its RBAC layer expects.
	Query url.Values

	// Capabilities — the declared points of divergence.

	// CursorKind is the server's cursor shape.
	CursorKind CursorKind
	// EmitsCoverage is true when the server emits coverage records for
	// unanswerable window sub-ranges (hub yes, OSS no).
	EmitsCoverage bool
	// MaxRows is the server's per-response row cap; a larger requested limit
	// clamps to it rather than erroring.
	MaxRows int
	// RejectsForeignCursor is true when a cursor from another store generation
	// is a 400 (OSS); false when the server replays it instead (hub).
	RejectsForeignCursor bool
	// DefaultsWindow is true when an absent from/to defaults a window (hub, 24h);
	// false when from/to are required (OSS).
	DefaultsWindow bool
}

// Run drives the shared timeline wire contract against cfg's live handler.
func Run(t *testing.T, cfg Config) {
	t.Helper()

	now := time.Now()
	base := now.Add(-time.Minute)
	mk := func(id string, off time.Duration) timeline.TimelineEvent {
		return timeline.TimelineEvent{
			ID: id, Timestamp: base.Add(off), Source: timeline.SourceInformer,
			Kind: "Deployment", Namespace: "default", Name: id,
			EventType: timeline.EventTypeUpdate,
		}
	}
	cfg.Seed(t, []timeline.TimelineEvent{
		mk("wc-0", 0), mk("wc-1", time.Second), mk("wc-2", 2*time.Second),
	})

	fromMs := strconv.FormatInt(base.Add(-time.Second).UnixMilli(), 10)
	toMs := strconv.FormatInt(now.UnixMilli(), 10)

	// coverage accumulates every coverage record seen across the suite, backing
	// the EmitsCoverage=false negative assertion.
	var coverageSeen []timelineapi.CoverageRecord
	decode := func(t *testing.T, rr *httptest.ResponseRecorder) ([]json.RawMessage, *timelineapi.EndRecord, *timelineapi.ErrorRecord) {
		t.Helper()
		rows, end, errRec, cov, err := timelineapi.DecodeStream(rr.Body)
		if err != nil {
			t.Fatalf("DecodeStream: %v", err)
		}
		coverageSeen = append(coverageSeen, cov...)
		return rows, end, errRec
	}

	// --- shared invariant: window load is framed NDJSON closed by a terminal ---
	var frontier string
	t.Run("window load frames NDJSON closed by a terminal record", func(t *testing.T) {
		rr := cfg.request(t, map[string]string{"from": fromMs, "to": toMs})
		if rr.Code != http.StatusOK {
			t.Fatalf("window status = %d, body %s", rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/x-ndjson" {
			t.Fatalf("window Content-Type = %q, want application/x-ndjson", ct)
		}
		rows, end, errRec := decode(t, rr)
		if errRec != nil {
			t.Fatalf("window emitted an error record on success: %+v", errRec)
		}
		if end == nil {
			t.Fatalf("window missing terminal end record")
		}
		if len(rows) != 3 {
			t.Fatalf("window returned %d rows, want 3", len(rows))
		}
		if end.Cursor == "" {
			t.Fatalf("terminal record carried no cursor")
		}
		assertCursorShape(t, cfg.CursorKind, end.Cursor)
		if end.Truncated || end.More {
			t.Fatalf("uncapped window terminal = %+v, want neither truncated nor more", end)
		}
		frontier = end.Cursor
	})

	// --- shared invariant: rows decode into the timeline row schema ---
	t.Run("rows decode into timeline.TimelineEvent", func(t *testing.T) {
		rr := cfg.request(t, map[string]string{"from": fromMs, "to": toMs})
		rows, _, _ := decode(t, rr)
		if len(rows) == 0 {
			t.Fatalf("no rows to decode")
		}
		for _, raw := range rows {
			var e timeline.TimelineEvent
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatalf("row does not decode into timeline.TimelineEvent: %v (%s)", err, raw)
			}
			if e.ID == "" || e.Kind == "" {
				t.Fatalf("row decoded but lost identity: %+v", e)
			}
		}
	})

	// --- shared invariant: an empty window still terminates with an end record ---
	t.Run("empty window still terminates", func(t *testing.T) {
		fromFuture := strconv.FormatInt(now.Add(time.Hour).UnixMilli(), 10)
		toFuture := strconv.FormatInt(now.Add(2*time.Hour).UnixMilli(), 10)
		rr := cfg.request(t, map[string]string{"from": fromFuture, "to": toFuture})
		if rr.Code != http.StatusOK {
			t.Fatalf("empty window status = %d, body %s", rr.Code, rr.Body.String())
		}
		rows, end, _ := decode(t, rr)
		if end == nil {
			t.Fatalf("empty window missing terminal end record")
		}
		if len(rows) != 0 {
			t.Fatalf("empty window returned %d rows, want 0", len(rows))
		}
	})

	// --- shared invariant: a capped window sets truncated and cuts to the cap ---
	t.Run("capped window is truncated to the row cap", func(t *testing.T) {
		rr := cfg.request(t, map[string]string{"from": fromMs, "to": toMs, "limit": "2"})
		rows, end, _ := decode(t, rr)
		if end == nil {
			t.Fatalf("capped window missing terminal end record")
		}
		if len(rows) != 2 || !end.Truncated {
			t.Fatalf("capped window: %d rows, truncated=%v; want 2 rows, truncated", len(rows), end.Truncated)
		}
	})

	// --- shared invariant: an over-cap limit clamps, never errors (gated on MaxRows) ---
	t.Run("over-limit request clamps rather than errors", func(t *testing.T) {
		over := strconv.Itoa(cfg.MaxRows + 5000)
		rr := cfg.request(t, map[string]string{"from": fromMs, "to": toMs, "limit": over})
		if rr.Code != http.StatusOK {
			t.Fatalf("over-limit status = %d, want 200 (clamp, not error); body %s", rr.Code, rr.Body.String())
		}
		rows, end, _ := decode(t, rr)
		if end == nil {
			t.Fatalf("over-limit window missing terminal end record")
		}
		if len(rows) > cfg.MaxRows {
			t.Fatalf("over-limit returned %d rows, exceeds cap %d", len(rows), cfg.MaxRows)
		}
	})

	// --- shared invariant: cursor round-trips — feed the end cursor back and the delta continues ---
	t.Run("cursor round-trip continues the stream", func(t *testing.T) {
		if frontier == "" {
			t.Fatalf("no frontier cursor captured from the window load")
		}
		// At the frontier the delta is empty and terminates.
		rr := cfg.request(t, map[string]string{"since": frontier})
		if rr.Code != http.StatusOK {
			t.Fatalf("frontier delta status = %d, body %s", rr.Code, rr.Body.String())
		}
		rows, end, _ := decode(t, rr)
		if end == nil {
			t.Fatalf("frontier delta missing terminal end record")
		}
		if len(rows) != 0 {
			t.Fatalf("frontier delta returned %d rows, want 0", len(rows))
		}

		// A new arrival after the cursor rides the next delta. Timestamp is set
		// after the window's `to` so it advances a nanosecond-frontier cursor as
		// well as an arrival-seq one.
		cfg.Seed(t, []timeline.TimelineEvent{
			mk("wc-new", time.Minute+time.Second), // base + 61s == now + 1s
		})
		rr = cfg.request(t, map[string]string{"since": frontier})
		if rr.Code != http.StatusOK {
			t.Fatalf("continuation delta status = %d, body %s", rr.Code, rr.Body.String())
		}
		rows, end, _ = decode(t, rr)
		if end == nil {
			t.Fatalf("continuation delta missing terminal end record")
		}
		found := false
		for _, raw := range rows {
			var e timeline.TimelineEvent
			if err := json.Unmarshal(raw, &e); err != nil {
				t.Fatalf("continuation row decode: %v", err)
			}
			if e.ID == "wc-new" {
				found = true
			}
		}
		if !found {
			t.Fatalf("continuation delta did not deliver the new arrival: %d rows", len(rows))
		}
	})

	// --- capability-gated: foreign cursor is 400 or replayed ---
	t.Run("foreign cursor is rejected or replayed per capability", func(t *testing.T) {
		rr := cfg.request(t, map[string]string{"since": foreignCursor(cfg.CursorKind)})
		if cfg.RejectsForeignCursor {
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("foreign cursor status = %d, want 400", rr.Code)
			}
			return
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("foreign cursor status = %d, want 200 (replay), body %s", rr.Code, rr.Body.String())
		}
		_, end, _ := decode(t, rr)
		if end == nil {
			t.Fatalf("replayed foreign cursor missing terminal end record")
		}
	})

	// --- capability-gated: absent window defaults or is required ---
	t.Run("absent window defaults or is required per capability", func(t *testing.T) {
		rr := cfg.request(t, nil)
		if cfg.DefaultsWindow {
			if rr.Code != http.StatusOK {
				t.Fatalf("absent window status = %d, want 200 (default), body %s", rr.Code, rr.Body.String())
			}
			_, end, _ := decode(t, rr)
			if end == nil {
				t.Fatalf("defaulted window missing terminal end record")
			}
			return
		}
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("absent window status = %d, want 400 (required)", rr.Code)
		}
	})

	// --- capability-gated: coverage records appear only when declared ---
	t.Run("coverage records absent unless declared", func(t *testing.T) {
		// Positive coverage requires a retention/observation gap the shared suite
		// cannot synthesize without server-specific internals, so the assertion
		// is one-sided: a server that does NOT declare coverage must never emit
		// it across any request the suite made.
		if !cfg.EmitsCoverage && len(coverageSeen) != 0 {
			t.Fatalf("server does not declare coverage yet emitted %d coverage record(s): %+v",
				len(coverageSeen), coverageSeen)
		}
	})
}

// request drives one GET through the handler, merging cfg.Query with params.
func (c Config) request(t *testing.T, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{}
	for k, vs := range c.Query {
		q[k] = append([]string(nil), vs...)
	}
	for k, v := range params {
		q.Set(k, v)
	}
	target := c.PathFor(ModeEvents)
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	c.Handler.ServeHTTP(rr, req)
	return rr
}

// foreignCursor synthesizes a syntactically valid cursor that cannot belong to
// the live store generation: an epoch of 1 never matches OSS's real
// nanosecond epoch, and a frontier of 1ns is a stale position hub replays.
func foreignCursor(kind CursorKind) string {
	switch kind {
	case CursorNanoFrontier:
		return "1"
	default:
		return "1:5"
	}
}

// assertCursorShape checks the terminal cursor has the structure the declared
// CursorKind promises — a guard against a server silently changing cursor form.
func assertCursorShape(t *testing.T, kind CursorKind, cursor string) {
	t.Helper()
	switch kind {
	case CursorEpochSeq:
		epoch, seq, ok := strings.Cut(cursor, ":")
		if !ok || !isDigits(epoch) || !isDigits(seq) {
			t.Fatalf("cursor %q is not <epoch>:<seq>", cursor)
		}
	case CursorNanoFrontier:
		if !isDigits(cursor) {
			t.Fatalf("cursor %q is not a bare nanosecond frontier", cursor)
		}
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
