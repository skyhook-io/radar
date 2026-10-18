package meaningfulchanges

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func initFetchBoundStore(t *testing.T, maxSize int) timeline.EventStore {
	t.Helper()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: maxSize}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	return timeline.GetStore()
}

func appendEvent(t *testing.T, store timeline.EventStore, e timeline.TimelineEvent) {
	t.Helper()
	e.Source = timeline.SourceInformer
	e.ClusterContext = k8s.ActiveClusterContext()
	if e.Namespace == "" {
		e.Namespace = "shop"
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("append %s: %v", e.ID, err)
	}
}

// An install's creates must not share a bounded query with edits, or an older
// edit falls out of the fetch: creates come only from the lifecycle query.
func TestRecentInstallBurstDoesNotHideOlderEdit(t *testing.T) {
	store := initFetchBoundStore(t, 2000)
	now := time.Now()
	appendEvent(t, store, timeline.TimelineEvent{
		ID: "env-edit", Timestamp: now.Add(-40 * time.Minute),
		APIVersion: "apps/v1", Kind: "Deployment", Name: "checkout", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.template.spec.containers[checkout].env[PRODUCT_CATALOG_ADDR]"}}},
	})
	// More creates than the fetch bound: they must not share a query with
	// the edit.
	for i := range FetchBound + 100 {
		appendEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("create-%d", i), Timestamp: now.Add(-20*time.Minute + time.Duration(i)*time.Second),
			APIVersion: "v1", Kind: "Service", Name: fmt.Sprintf("svc-%d", i), EventType: timeline.EventTypeAdd,
		})
	}
	for i := range 300 {
		appendEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("status-%d", i), Timestamp: now.Add(-5*time.Minute + time.Duration(i)*time.Second),
			APIVersion: "apps/v1", Kind: "Deployment", Name: fmt.Sprintf("app-%d", i%40), EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(0), NewValue: int32(1)}}},
		})
	}

	result, err := Recent(context.Background(), Query{Namespaces: []string{"shop"}})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if !result.FetchSaturated {
		t.Fatal("more creates than the fetch bound must saturate the lifecycle query")
	}
	if len(result.Changes) == 0 || result.Changes[0].Name != "checkout" {
		t.Fatalf("the older env edit must rank first, got %+v", result.Changes)
	}
	if !result.OutputCapped {
		t.Fatal("500 qualifying rows against limit 20 must report output capping")
	}
}

// Rows the caller may not read must be dropped before the internal cut, or
// higher-ranked hidden rows push readable ones out and the caller sees fewer.
func TestRecentAllowRunsBeforeTheOutputCut(t *testing.T) {
	store := initFetchBoundStore(t, 2000)
	now := time.Now()
	appendEvent(t, store, timeline.TimelineEvent{
		ID: "readable", Timestamp: now.Add(-30 * time.Minute),
		APIVersion: "apps/v1", Kind: "Deployment", Name: "web", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: int32(1), NewValue: int32(2)}}},
	})
	for i := range 60 {
		appendEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("hidden-%d", i), Timestamp: now.Add(-time.Duration(60-i) * time.Second),
			APIVersion: "v1", Kind: "ConfigMap", Name: fmt.Sprintf("cfg-%d", i), EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)"}}},
		})
	}

	result, err := Recent(context.Background(), Query{
		Namespaces: []string{"shop"},
		Limit:      50,
		Allow:      func(c issuesapi.RecentChange) bool { return c.Kind != "ConfigMap" },
	})
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Name != "web" {
		t.Fatalf("readable change must survive 60 newer hidden rows, got %+v", result.Changes)
	}
	if result.OutputCapped {
		t.Fatal("hidden rows must not count toward output capping")
	}
}

func TestHistoryCoverageSince(t *testing.T) {
	t.Run("complete when observation and the store cover the window", func(t *testing.T) {
		initFetchBoundStore(t, 100)
		if got := HistoryCoverageSince(time.Now().Add(-time.Hour)); got.Incomplete || len(got.Reasons) != 0 || !got.CompleteSince.IsZero() {
			t.Fatalf("coverage = %+v, want complete", got)
		}
	})

	t.Run("observation started after the window began", func(t *testing.T) {
		initFetchBoundStore(t, 100)
		started := time.Now().Add(-10*time.Minute - 300*time.Millisecond)
		timeline.SetObservationStartForTest(started)
		got := HistoryCoverageSince(time.Now().Add(-time.Hour))
		if !got.Incomplete || !slices.Equal(got.Reasons, []issuesapi.HistoryGapReason{issuesapi.HistoryObservationStartedLate}) {
			t.Fatalf("coverage = %+v, want observation_started_late", got)
		}
		// Rounded up to the second: never earlier than the true boundary.
		if want := started.Truncate(time.Second).Add(time.Second).UTC(); !got.CompleteSince.Equal(want) || got.CompleteSince.Location() != time.UTC {
			t.Fatalf("complete_since = %v, want %v in UTC", got.CompleteSince, want)
		}
		if got := HistoryCoverageSince(time.Now().Add(-5 * time.Minute)); got.Incomplete {
			t.Fatalf("a window inside the observed span must be complete, got %+v", got)
		}
	})

	t.Run("events evicted from inside the window", func(t *testing.T) {
		store := initFetchBoundStore(t, 3)
		now := time.Now()
		for i := range 5 {
			appendEvent(t, store, timeline.TimelineEvent{
				ID: fmt.Sprintf("e-%d", i), Timestamp: now.Add(-time.Duration(50-i) * time.Minute),
				Kind: "Deployment", Name: "web", EventType: timeline.EventTypeUpdate,
			})
		}
		evictedThrough := now.Add(-49 * time.Minute)
		got := HistoryCoverageSince(now.Add(-time.Hour))
		if !got.Incomplete || !slices.Equal(got.Reasons, []issuesapi.HistoryGapReason{issuesapi.HistoryEventsEvicted}) {
			t.Fatalf("coverage = %+v, want events_evicted", got)
		}
		if got.CompleteSince.Before(evictedThrough) || got.CompleteSince.After(evictedThrough.Add(time.Second)) {
			t.Fatalf("complete_since = %v, want the newest evicted event's time %v rounded up", got.CompleteSince, evictedThrough)
		}
		if got := HistoryCoverageSince(now.Add(-30 * time.Minute)); got.Incomplete {
			t.Fatalf("a window after the evicted events must be complete, got %+v", got)
		}
	})
}

func TestHistoryCoverageForWithholdsTheBoundaryFromRestrictedCallers(t *testing.T) {
	coverage := issuesapi.HistoryCoverage{
		Incomplete:    true,
		CompleteSince: time.Date(2026, 10, 17, 12, 20, 30, 0, time.UTC),
		Reasons:       []issuesapi.HistoryGapReason{issuesapi.HistoryEventsEvicted},
	}
	if got := HistoryCoverageFor(issuesapi.HistoryCoverage{}, true); got != nil {
		t.Fatalf("complete coverage must be omitted, got %+v", got)
	}
	full := HistoryCoverageFor(coverage, true)
	if full == nil || !full.CompleteSince.Equal(coverage.CompleteSince) {
		t.Fatalf("cluster-wide reader coverage = %+v, want complete_since kept", full)
	}
	restricted := HistoryCoverageFor(coverage, false)
	if restricted == nil || !restricted.Incomplete || !restricted.CompleteSince.IsZero() || len(restricted.Reasons) != 1 {
		t.Fatalf("restricted coverage = %+v, want incomplete with reasons and no complete_since", restricted)
	}
	if HistoryCoverageGuidance(*restricted) == HistoryCoverageGuidance(*full) {
		t.Fatal("restricted guidance must not carry the timestamp")
	}
}

// Nothing in a namespace predates the namespace, so an answer scoped to
// namespaces created after the boundary is complete. This is the common case
// of an app installed into a new namespace after Radar started.
func TestRecentNamespaceCreatedAfterTheBoundaryIsComplete(t *testing.T) {
	now := time.Now()
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fresh", CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute))}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "old", CreationTimestamp: metav1.NewTime(now.Add(-30 * 24 * time.Hour))}},
	)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
	initFetchBoundStore(t, 100)
	timeline.SetObservationStartForTest(now.Add(-10 * time.Minute))

	for _, tt := range []struct {
		namespaces     []string
		wantIncomplete bool
	}{
		{namespaces: []string{"fresh"}},
		{namespaces: []string{"old"}, wantIncomplete: true},
		{namespaces: []string{"fresh", "old"}, wantIncomplete: true},
		{namespaces: []string{"missing"}, wantIncomplete: true},
		{namespaces: nil, wantIncomplete: true},
	} {
		result, err := Recent(context.Background(), Query{Namespaces: tt.namespaces})
		if err != nil {
			t.Fatalf("Recent(%v): %v", tt.namespaces, err)
		}
		if result.History.Incomplete != tt.wantIncomplete {
			t.Errorf("namespaces %v: history incomplete = %v, want %v", tt.namespaces, result.History.Incomplete, tt.wantIncomplete)
		}
	}
}
