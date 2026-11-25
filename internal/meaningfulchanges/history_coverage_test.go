package meaningfulchanges

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// An eviction boundary dates activity anywhere in the store, so it is kept
// only for callers who read every namespace; a late start alone is kept for
// every caller.
func TestHistoryCoverageForBoundaryByReason(t *testing.T) {
	since := time.Date(2026, 11, 25, 19, 10, 41, 0, time.UTC)
	late := issuesapi.HistoryObservationStartedLate
	evicted := issuesapi.HistoryEventsEvicted
	for _, tc := range []struct {
		name         string
		reasons      []issuesapi.HistoryGapReason
		allNamespace bool
		wantSince    bool
	}{
		{"late start, all namespaces", []issuesapi.HistoryGapReason{late}, true, true},
		{"late start, some namespaces", []issuesapi.HistoryGapReason{late}, false, true},
		{"evicted, all namespaces", []issuesapi.HistoryGapReason{evicted}, true, true},
		{"evicted, some namespaces", []issuesapi.HistoryGapReason{evicted}, false, false},
		{"both, some namespaces", []issuesapi.HistoryGapReason{late, evicted}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := HistoryCoverageFor(issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: since, Reasons: tc.reasons}, tc.allNamespace)
			if got == nil || !got.Incomplete || len(got.Reasons) != len(tc.reasons) {
				t.Fatalf("coverage = %+v, want incomplete with reasons %v", got, tc.reasons)
			}
			if sent := !got.CompleteSince.IsZero(); sent != tc.wantSince {
				t.Fatalf("complete_since sent = %v, want %v", sent, tc.wantSince)
			}
		})
	}
	if got := HistoryCoverageFor(issuesapi.HistoryCoverage{}, false); got != nil {
		t.Fatalf("complete history = %+v, want nil", got)
	}
}
