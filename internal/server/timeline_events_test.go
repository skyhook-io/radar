package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// The wire contract the web client's single timeline path depends on (the
// same one the hub serves): NDJSON event lines closed by a terminal record
// carrying an opaque epoch:seq cursor; window loads report truncation;
// deltas page ascending by arrival; a foreign-epoch cursor is 400.
func TestHandleTimelineEvents_Contract(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	store := timeline.GetStore()
	base := time.Now().Add(-time.Minute)
	for i, name := range []string{"a", "b", "c"} {
		if err := store.Append(t.Context(), timeline.TimelineEvent{
			ID: "ev-" + name, Timestamp: base.Add(time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, Kind: "Deployment", Namespace: "default",
			Name: name, EventType: timeline.EventTypeUpdate,
			ClusterContext: k8s.ActiveClusterContext(),
		}); err != nil {
			t.Fatalf("Append %s: %v", name, err)
		}
	}

	s := &Server{}
	get := func(url string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rr := httptest.NewRecorder()
		s.handleTimelineEvents(rr, req)
		return rr
	}
	// parseStream splits an NDJSON body into events and the terminal record.
	parseStream := func(t *testing.T, body string) ([]timeline.TimelineEvent, timelineEndRecord) {
		t.Helper()
		var events []timeline.TimelineEvent
		var end timelineEndRecord
		sawEnd := false
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			if sawEnd {
				t.Fatalf("line after terminal record: %s", line)
			}
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &probe); err != nil {
				t.Fatalf("bad NDJSON line %q: %v", line, err)
			}
			if probe.Type == "end" || probe.Type == "error" {
				if err := json.Unmarshal([]byte(line), &end); err != nil {
					t.Fatalf("bad terminal record %q: %v", line, err)
				}
				sawEnd = true
				continue
			}
			var e timeline.TimelineEvent
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("bad event line %q: %v", line, err)
			}
			events = append(events, e)
		}
		if !sawEnd {
			t.Fatalf("stream missing terminal record: %s", body)
		}
		return events, end
	}

	epoch := strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10)
	fromMs := strconv.FormatInt(base.Add(-time.Second).UnixMilli(), 10)
	toMs := strconv.FormatInt(time.Now().UnixMilli(), 10)

	// Window load: all three events, cursor = epoch:maxSeq, not truncated.
	rr := get("/api/timeline/events?from=" + fromMs + "&to=" + toMs + "&namespaces=default")
	if rr.Code != http.StatusOK {
		t.Fatalf("window status = %d, body %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("window Content-Type = %q", ct)
	}
	events, end := parseStream(t, rr.Body.String())
	if len(events) != 3 {
		t.Fatalf("window returned %d events, want 3", len(events))
	}
	if end.Truncated || end.More {
		t.Fatalf("uncapped window end = %+v", end)
	}
	if !strings.HasPrefix(end.Cursor, epoch+":") {
		t.Fatalf("cursor %q not stamped with current epoch %s", end.Cursor, epoch)
	}
	var maxSeq int64
	for _, e := range events {
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	if end.Cursor != epoch+":"+strconv.FormatInt(maxSeq, 10) {
		t.Fatalf("window cursor = %q, want frontier %s:%d", end.Cursor, epoch, maxSeq)
	}
	frontier := end.Cursor

	// Capped window: newest-first cut, truncated set.
	rr = get("/api/timeline/events?from=" + fromMs + "&to=" + toMs + "&limit=2&namespaces=default")
	events, end = parseStream(t, rr.Body.String())
	if len(events) != 2 || !end.Truncated {
		t.Fatalf("capped window: %d events, end %+v; want 2 events truncated", len(events), end)
	}
	for _, e := range events {
		if e.Name == "a" {
			t.Fatalf("capped window kept the oldest event instead of the newest")
		}
	}

	// The `to` bound is honored: a window ending before event c excludes it.
	beforeC := strconv.FormatInt(base.Add(1500*time.Millisecond).UnixMilli(), 10)
	rr = get("/api/timeline/events?from=" + fromMs + "&to=" + beforeC + "&namespaces=default")
	events, _ = parseStream(t, rr.Body.String())
	for _, e := range events {
		if e.Name == "c" {
			t.Fatalf("window [from,to] returned an event past to")
		}
	}

	// Delta from the middle: only later arrivals, ascending seq, cursor advances.
	middle := events[0].Seq // events here are from the bounded window (a,b); newest first ⇒ b
	rr = get("/api/timeline/events?since=" + epoch + ":" + strconv.FormatInt(middle, 10) + "&namespaces=default")
	if rr.Code != http.StatusOK {
		t.Fatalf("delta status = %d, body %s", rr.Code, rr.Body.String())
	}
	events, end = parseStream(t, rr.Body.String())
	for i, e := range events {
		if e.Seq <= middle {
			t.Fatalf("delta returned seq %d <= cursor %d", e.Seq, middle)
		}
		if i > 0 && e.Seq < events[i-1].Seq {
			t.Fatalf("delta page not ascending by seq")
		}
	}
	if end.Cursor != frontier {
		t.Fatalf("delta cursor = %q, want frontier %q", end.Cursor, frontier)
	}

	// Delta at the frontier: no events, cursor echoed unchanged.
	rr = get("/api/timeline/events?since=" + frontier + "&namespaces=default")
	events, end = parseStream(t, rr.Body.String())
	if len(events) != 0 || end.Cursor != frontier {
		t.Fatalf("frontier delta: %d events, cursor %q; want 0 events, cursor %q", len(events), end.Cursor, frontier)
	}

	// A cursor from another epoch (store re-created) or malformed input is a
	// 400 — the client must full-resync, never trust an empty delta.
	for _, bad := range []string{"1:5", "abc", epoch, epoch + ":x", epoch + ":-2"} {
		rr = get("/api/timeline/events?since=" + bad + "&namespaces=default")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("since=%q status = %d, want 400", bad, rr.Code)
		}
	}

	// Window parameter validation.
	for _, u := range []string{
		"/api/timeline/events",
		"/api/timeline/events?from=10&to=5",
		"/api/timeline/events?from=x&to=5",
		"/api/timeline/events?from=1&to=2&limit=0",
	} {
		if rr = get(u); rr.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", u, rr.Code)
		}
	}
}
