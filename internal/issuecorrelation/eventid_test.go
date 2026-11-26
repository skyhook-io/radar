package issuecorrelation

import (
	"context"
	"testing"
	"time"
)

// Change rows carry their timeline event's ID only when asked: the MCP tool
// doesn't ask, so its output is unchanged.
func TestCorrelate_EventIDsOnlyWhenAsked(t *testing.T) {
	initCache(t, webDeployment())
	store := initStore(t)
	firstSeen := time.Now().Add(-10 * time.Minute)
	e := specUpdate("Deployment", "web", firstSeen.Add(-4*time.Minute).Truncate(time.Second), "web:bad")
	appendChange(t, store, e)
	iss := webIssue(firstSeen)
	opts := anchoredOptions(iss)
	got := Correlate(context.Background(), iss, opts, Access{})
	if len(got.Changes) != 1 || got.Changes[0].EventID != e.ID {
		t.Fatalf("asked: %+v, want event %s", got.Changes, e.ID)
	}
	opts.EventIDs = false
	got = Correlate(context.Background(), iss, opts, Access{})
	if len(got.Changes) != 1 || got.Changes[0].EventID != "" {
		t.Fatalf("not asked: %+v", got.Changes)
	}
}
