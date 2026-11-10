package search

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestNamedUnwatchedKindReportsCollectionGap(t *testing.T) {
	named := newCoverage(Query{KindFilter: []string{"Secret"}})
	named.kind("Secret", "", readreason.RadarKindNotCollected)
	if got := named.result(); len(got) != 1 || got[0].Code != readreason.RadarKindNotCollected {
		t.Fatalf("named unavailable kind disappeared: %+v", got)
	}
	broad := newCoverage(Query{})
	broad.kind("Secret", "", readreason.RadarKindNotCollected)
	if got := broad.result(); len(got) != 0 {
		t.Fatalf("broad query was flooded with unwatched kinds: %+v", got)
	}
}
