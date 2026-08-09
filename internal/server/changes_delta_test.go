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

	// Min-seq advertises the store's oldest retained seq so a consumer can tell
	// its cursor fell below the retention floor. Nothing evicted here, so it is
	// the seq of the oldest surviving event.
	minSeqHeader := rr.Header().Get("X-Radar-Timeline-Min-Seq")
	if minSeqHeader == "" {
		t.Fatalf("missing X-Radar-Timeline-Min-Seq header")
	}
	minSeq, err := strconv.ParseInt(minSeqHeader, 10, 64)
	if err != nil || minSeq <= 0 {
		t.Fatalf("bad min-seq header %q", minSeqHeader)
	}
	if want := minEventSeq(full); minSeq != want {
		t.Fatalf("min-seq header = %d, want oldest retained seq %d", minSeq, want)
	}
	if minSeq > maxSeq {
		t.Fatalf("min-seq %d above max-seq %d", minSeq, maxSeq)
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

// After the ring evicts its oldest records, the min-seq header must advance
// past the evicted floor so a consumer that pulled forward from a stale cursor
// can detect the gap instead of reading an empty page as "caught up".
func TestHandleChanges_MinSeqTracksEviction(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })

	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 3}); err != nil {
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
	// Six distinct resources into a 3-slot ring: the first three are evicted.
	for i := 0; i < 6; i++ {
		name := "r" + strconv.Itoa(i)
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
	req := httptest.NewRequest(http.MethodGet, "/api/changes?namespaces=default", nil)
	rr := httptest.NewRecorder()
	s.handleChanges(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rr.Code, rr.Body.String())
	}

	minSeqHeader := rr.Header().Get("X-Radar-Timeline-Min-Seq")
	if minSeqHeader == "" {
		t.Fatalf("missing X-Radar-Timeline-Min-Seq header")
	}
	minSeq, err := strconv.ParseInt(minSeqHeader, 10, 64)
	if err != nil {
		t.Fatalf("bad min-seq header %q", minSeqHeader)
	}
	// Seq 1 was evicted, so the retained floor must be above it.
	if minSeq <= 1 {
		t.Fatalf("min-seq header = %d, want floor above evicted seq 1", minSeq)
	}
	var events []timeline.TimelineEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected retained events after eviction")
	}
	if want := minEventSeq(events); minSeq != want {
		t.Fatalf("min-seq header = %d, want oldest retained seq %d", minSeq, want)
	}
}

// The min-seq header is derived pre-RBAC-filter (pageMinSeq) but the retained
// floor was historically re-read fresh after the slow, SAR-bound RBAC filter.
// A ring evicting during that window raises the fresh floor above seqs still in
// the response body, so the header could advertise a floor higher than a seq we
// actually delivered — making the hub puller record a false coverage gap and
// skip events it received. clampMinSeqToPage is the guard: whatever the floor
// read reports, the emitted value can never exceed the lowest delivered seq.
func TestClampMinSeqToPage(t *testing.T) {
	cases := []struct {
		name          string
		retainedFloor int64
		pageMinSeq    int64
		want          int64
	}{
		// The race: a fresh floor read (10) has risen above a seq still on the
		// delivered page (4) because the ring evicted mid-request. The emitted
		// header must be the page floor, not the inflated store floor.
		{"floor inflated above delivered page", 10, 4, 4},
		// Genuine gap: low seqs were evicted before the query, so the page
		// itself starts high (8) above the floor (5). min() keeps the true
		// floor so the consumer still sees the gap below the page.
		{"genuine gap preserved", 5, 8, 5},
		// No skew: page floor equals the store floor.
		{"floor matches page", 4, 4, 4},
		// Empty page (all rows RBAC-filtered, or none matched): nothing in the
		// body to contradict, so the raw floor stands.
		{"empty page keeps floor", 7, 0, 7},
		// Empty store: floor 0 → caller omits the header entirely.
		{"empty store", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampMinSeqToPage(tc.retainedFloor, tc.pageMinSeq); got != tc.want {
				t.Fatalf("clampMinSeqToPage(%d, %d) = %d, want %d",
					tc.retainedFloor, tc.pageMinSeq, got, tc.want)
			}
			// The load-bearing invariant across every non-empty page: the
			// emitted floor never exceeds a delivered seq.
			if tc.pageMinSeq > 0 {
				if got := clampMinSeqToPage(tc.retainedFloor, tc.pageMinSeq); got > tc.pageMinSeq {
					t.Fatalf("emitted floor %d exceeds delivered page floor %d", got, tc.pageMinSeq)
				}
			}
		})
	}
}

// minEventSeq returns the smallest Seq in a page; the changes feed returns
// events newest-first, so the oldest retained seq is the page minimum, not
// events[0].
func minEventSeq(events []timeline.TimelineEvent) int64 {
	var min int64
	for _, e := range events {
		if min == 0 || e.Seq < min {
			min = e.Seq
		}
	}
	return min
}
