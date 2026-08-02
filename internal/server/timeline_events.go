package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// timelineEventsPageLimit caps both response shapes at the store's own page
// ceiling. The client sends larger limits (the hub serves up to 50k); a limit
// above the cap clamps rather than erroring — it is a maximum, not a demand.
const timelineEventsPageLimit = 10000

// timelineEndRecord is the NDJSON terminal record closing an events stream.
// Its shape is the wire contract the web client's retained source parses —
// the same record the hub emits.
type timelineEndRecord struct {
	Type      string `json:"type"`
	Cursor    string `json:"cursor,omitempty"`
	More      bool   `json:"more,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// handleTimelineEvents serves GET /api/timeline/events — the shared timeline
// wire contract (the same one the hub serves): NDJSON event lines closed by a
// terminal record carrying the delta cursor.
//
//	?from=<ms>&to=<ms>[&limit=N]  window load: newest-N events in the event-time
//	                              range, `truncated` set when the range held more
//	?since=<cursor>               delta: every event that ARRIVED after the
//	                              cursor, ascending arrival order
//
// The cursor is opaque to clients: "<epoch>:<seq>", where seq is the store's
// arrival counter and epoch is the observation start. Arrival order is the
// load-bearing choice — a late-arriving or revised event has an old event
// time but a fresh seq, so it rides the next poll like any new event. A
// cursor minted by another epoch (the store was re-created: restart, context
// switch) gets 400 and the client full-resyncs; an empty delta from a reset
// store must never read as "nothing new".
func (s *Server) handleTimelineEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	// Store and epoch are captured once, together, and threaded through: a
	// kubeconfig context switch resets the global store mid-request, and
	// re-resolving it later could pair one store generation's rows with
	// another's epoch (or dereference nil).
	store := timeline.GetStore()
	if store == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Timeline store not available")
		return
	}
	epoch := timeline.ObservationStart().UnixNano()
	if since := r.URL.Query().Get("since"); since != "" {
		s.serveTimelineEventsDelta(w, r, store, epoch, since)
		return
	}
	s.serveTimelineEventsWindow(w, r, store, epoch)
}

func timelineCursor(epoch, seq int64) string {
	return strconv.FormatInt(epoch, 10) + ":" + strconv.FormatInt(seq, 10)
}

// parseTimelineCursor validates a client cursor against the current store
// epoch. ok=false means the cursor is malformed or from another epoch —
// either way the client's position is meaningless and it must full-resync.
func parseTimelineCursor(cursor string, epoch int64) (seq int64, ok bool) {
	epochStr, seqStr, found := strings.Cut(cursor, ":")
	if !found {
		return 0, false
	}
	cursorEpoch, err := strconv.ParseInt(epochStr, 10, 64)
	if err != nil || cursorEpoch != epoch {
		return 0, false
	}
	seq, err = strconv.ParseInt(seqStr, 10, 64)
	if err != nil || seq < 0 {
		return 0, false
	}
	return seq, true
}

// timelineEventsQuery is the everything-visible read both response shapes
// share: content filtering is the client's job (applyClientFilters runs on
// every mode), so the server applies only per-user namespace RBAC and the
// active-cluster scope.
func (s *Server) timelineEventsQuery(r *http.Request, namespaces []string) timeline.QueryOptions {
	return timeline.QueryOptions{
		Namespaces:       namespaces,
		FilterPreset:     "all",
		IncludeManaged:   true,
		IncludeK8sEvents: true,
		Limit:            timelineEventsPageLimit,
		// The persistent store retains events from previously-connected
		// clusters; this endpoint answers for the current one only.
		ClusterContext: k8s.ActiveClusterContext(),
	}
}

func (s *Server) serveTimelineEventsDelta(w http.ResponseWriter, r *http.Request, store timeline.EventStore, epoch int64, cursor string) {
	seq, ok := parseTimelineCursor(cursor, epoch)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "stale or invalid cursor")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		writeTimelineStream(w, nil, timelineEndRecord{Type: "end", Cursor: cursor})
		return
	}

	opts := s.timelineEventsQuery(r, namespaces)
	opts.SinceSeq = seq
	opts.SeqPaging = true

	events, err := store.Query(r.Context(), opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Cursor progress is derived BEFORE the RBAC filter: rows the user can't
	// read still advance the frontier, or a run of unreadable rows would pin
	// the cursor while the client re-fetches the same page forever (same
	// discipline as handleChanges).
	maxSeq := seq
	for _, e := range events {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	full := len(events) == timelineEventsPageLimit
	events = s.filterEventsByRBAC(r, events)

	writeTimelineStream(w, events, timelineEndRecord{
		Type:   "end",
		Cursor: timelineCursor(epoch, maxSeq),
		More:   full,
	})
}

func (s *Server) serveTimelineEventsWindow(w http.ResponseWriter, r *http.Request, store timeline.EventStore, epoch int64) {
	q := r.URL.Query()
	fromMs, errFrom := strconv.ParseInt(q.Get("from"), 10, 64)
	toMs, errTo := strconv.ParseInt(q.Get("to"), 10, 64)
	if errFrom != nil || errTo != nil || fromMs < 0 || toMs <= fromMs {
		s.writeError(w, http.StatusBadRequest, "invalid or missing from/to (epoch milliseconds, from < to)")
		return
	}
	limit := timelineEventsPageLimit
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n < limit {
			limit = n
		}
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		writeTimelineStream(w, nil, timelineEndRecord{Type: "end", Cursor: timelineCursor(epoch, 0)})
		return
	}

	opts := s.timelineEventsQuery(r, namespaces)
	opts.Since = time.UnixMilli(fromMs)
	opts.Until = time.UnixMilli(toMs)
	opts.Limit = limit

	events, err := store.Query(r.Context(), opts)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	maxSeq := int64(0)
	for _, e := range events {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	// The default query order is newest-first, so hitting the limit means the
	// oldest part of the requested range was cut — exactly what the client's
	// `truncated` banner reports. An exactly-limit-sized range over-reports;
	// harmless, the same heuristic every consumer of this flag uses.
	truncated := len(events) == limit
	events = s.filterEventsByRBAC(r, events)

	writeTimelineStream(w, events, timelineEndRecord{
		Type:      "end",
		Cursor:    timelineCursor(epoch, maxSeq),
		Truncated: truncated,
	})
}

// writeTimelineStream emits the NDJSON body: one event per line, then the
// terminal record. Everything is queried before the first byte, so failures
// surface as real HTTP errors, never a half stream.
func writeTimelineStream(w http.ResponseWriter, events []timeline.TimelineEvent, end timelineEndRecord) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for i := range events {
		if err := enc.Encode(&events[i]); err != nil {
			// The connection is gone mid-body; the missing terminal record
			// tells the client the stream was cut.
			return
		}
	}
	_ = enc.Encode(end)
}
