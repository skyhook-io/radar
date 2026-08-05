package server

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
	"github.com/skyhook-io/radar/pkg/timelineapi/wiretest"
)

// TestHandleTimelineEvents_WireContract runs the shared timeline wire
// conformance suite (exported from the radar OSS pkg module) against OSS's own
// live handler. The same suite backs radar-hub against its handler, so both
// servers prove they speak the one contract; each declares its capabilities and
// the suite gates the deliberate divergences on them. OSS's capabilities:
// "<epoch>:<seq>" cursors, foreign cursor → 400, no coverage records, a 10k row
// cap, and a required (non-defaulted) window.
func TestHandleTimelineEvents_WireContract(t *testing.T) {
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

	s := &Server{}
	wiretest.Run(t, wiretest.Config{
		Handler: http.HandlerFunc(s.handleTimelineEvents),
		PathFor: func(string) string { return "/api/timeline/events" },
		Seed: func(t *testing.T, events []pkgtimeline.TimelineEvent) {
			t.Helper()
			store := timeline.GetStore()
			for _, e := range events {
				e.ClusterContext = k8s.ActiveClusterContext()
				if err := store.Append(t.Context(), e); err != nil {
					t.Fatalf("seed append %s: %v", e.ID, err)
				}
			}
		},
		Query:                url.Values{"namespaces": {"default"}},
		CursorKind:           wiretest.CursorEpochSeq,
		EmitsCoverage:        false,
		MaxRows:              10000,
		RejectsForeignCursor: true,
		DefaultsWindow:       false,
	})
}
