package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// The delta contract clients depend on: header names (a typo silently degrades
// every client to full refetches — CORS exposes these exact names), the epoch
// stamp, the max-seq frontier, ascending since_seq paging, and 400 on a
// malformed cursor.
func TestHandleChanges_DeltaContract(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	// The smoke suite's TestMain initializes the package-global store once;
	// leave it as we found it, not nil.
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
			// The handler scopes its query to the active cluster context.
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
		s.handleChanges(rr, req)
		return rr
	}

	// Full fetch: both headers present, maxSeq = the highest arrival number.
	rr := get("/api/changes?namespaces=default")
	if rr.Code != http.StatusOK {
		t.Fatalf("full fetch status = %d, body %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Radar-Timeline-Epoch") == "" {
		t.Fatalf("missing X-Radar-Timeline-Epoch header")
	}
	maxSeqHeader := rr.Header().Get("X-Radar-Timeline-Max-Seq")
	if maxSeqHeader == "" {
		t.Fatalf("missing X-Radar-Timeline-Max-Seq header")
	}
	maxSeq, err := strconv.ParseInt(maxSeqHeader, 10, 64)
	if err != nil || maxSeq <= 0 {
		t.Fatalf("bad max-seq header %q", maxSeqHeader)
	}
	var full []timeline.TimelineEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &full); err != nil {
		t.Fatalf("unmarshal full: %v", err)
	}
	if len(full) != 3 {
		t.Fatalf("full fetch returned %d events, want 3", len(full))
	}

	// Delta from the middle: ascending seq order, only events above the cursor.
	middle := full[1].Seq
	rr = get("/api/changes?namespaces=default&since_seq=" + strconv.FormatInt(middle, 10))
	if rr.Code != http.StatusOK {
		t.Fatalf("delta status = %d", rr.Code)
	}
	var delta []timeline.TimelineEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	for _, e := range delta {
		if e.Seq <= middle {
			t.Fatalf("delta returned seq %d <= cursor %d", e.Seq, middle)
		}
	}
	for i := 1; i < len(delta); i++ {
		if delta[i].Seq < delta[i-1].Seq {
			t.Fatalf("delta page not ascending by seq: %d before %d", delta[i-1].Seq, delta[i].Seq)
		}
	}

	// Empty delta (cursor at frontier): 200, epoch still present.
	rr = get("/api/changes?namespaces=default&since_seq=" + maxSeqHeader)
	if rr.Code != http.StatusOK {
		t.Fatalf("frontier delta status = %d", rr.Code)
	}
	if rr.Header().Get("X-Radar-Timeline-Epoch") == "" {
		t.Fatalf("frontier delta missing epoch header")
	}

	// Malformed and negative cursors are input errors, not silent full fetches.
	for _, bad := range []string{"abc", "-5", "1.5"} {
		rr = get("/api/changes?namespaces=default&since_seq=" + bad)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("since_seq=%s status = %d, want 400", bad, rr.Code)
		}
	}
}
