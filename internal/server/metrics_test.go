package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

func scrapeMetrics(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	(&Server{broadcaster: NewSSEBroadcaster()}).newMetricsHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("read scrape: %v", err)
	}
	return string(body)
}

// The timeline can be down while every other view works, so the scrape is the
// one place an alert can see it. It must report 0 with the database down, count
// what was lost, and never carry the database address.
func TestMetricsReportTimelineOutage(t *testing.T) {
	timeline.ResetStore()
	timeline.ResetMetricsForContextSwitch()
	t.Cleanup(func() {
		timeline.ResetStore()
		timeline.ResetMetricsForContextSwitch()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	// Port 1 refuses at once, so the first attempt fails without a timeout.
	if err := timeline.InitStore(timeline.StoreConfig{
		Type: timeline.StoreTypePostgres,
		DSN:  "postgres://radar:pw@127.0.0.1:1/radar?connect_timeout=2",
	}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	timeline.RecordDrop("Deployment", "shop", "web", timeline.DropReasonStoreDown, "update", "ctx")

	out := scrapeMetrics(t)
	if !strings.Contains(out, "radar_timeline_available 0") {
		t.Fatalf("scrape does not report the timeline down:\n%s", out)
	}
	if !strings.Contains(out, `radar_timeline_dropped_events_total{reason="store_unavailable"} 1`) {
		t.Fatalf("scrape does not count the lost change:\n%s", out)
	}
	if strings.Contains(out, "127.0.0.1") {
		t.Fatalf("scrape leaks the database address:\n%s", out)
	}

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore memory: %v", err)
	}
	if out := scrapeMetrics(t); !strings.Contains(out, "radar_timeline_available 1") {
		t.Fatalf("scrape reports a working timeline as down:\n%s", out)
	}
}

// /metrics can be reachable without signing in, so scrapes must not each make
// a database round trip: within the TTL they reuse the last check.
func TestMetricsTimelineCheckIsReusedWithinTheTTL(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	if err := timeline.InitStore(timeline.StoreConfig{
		Type: timeline.StoreTypePostgres,
		DSN:  "postgres://radar:pw@127.0.0.1:1/radar?connect_timeout=2",
	}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	check := &timelineAvailability{}
	if got := check.value(); got != 0 {
		t.Fatalf("value=%v with the database down, want 0", got)
	}

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore memory: %v", err)
	}
	if got := check.value(); got != 0 {
		t.Fatalf("value=%v within the TTL, want the cached 0", got)
	}

	check.checkedAt = time.Now().Add(-metricsTimelineCheckTTL)
	if got := check.value(); got != 1 {
		t.Fatalf("value=%v after the TTL with a working store, want 1", got)
	}
}
