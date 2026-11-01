package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

// The calico_rr_label_drift shape: a BGPConfiguration created during the
// incident, invisible to the default feed. The answer names its kind so the
// change stays discoverable.
func TestGetChangesNamesOmittedKinds(t *testing.T) {
	store := initCorrelationStore(t)
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "bgp", Timestamp: time.Now(), Kind: "BGPConfiguration", APIVersion: "crd.projectcalico.org/v1", Name: "default",
		EventType: timeline.EventTypeAdd,
	})
	const want = "This window also has changes to kinds the default feed leaves out: BGPConfiguration. Pass kind=<Kind> to list them."

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{})
	if len(response.Changes) != 0 || !strings.Contains(response.NarrowHint, want) {
		t.Fatalf("default get_changes = %+v, want no rows and the omitted-kinds hint", response)
	}
	if len(response.OmittedKinds) != 1 || response.OmittedKinds[0].Kind != "BGPConfiguration" || response.OmittedKinds[0].Group != "crd.projectcalico.org" || response.OmittedKinds[0].LatestChangeType != "add" {
		t.Fatalf("omittedKinds = %+v", response.OmittedKinds)
	}
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{Kind: "BGPConfiguration"})
	if len(response.Changes) != 1 || strings.Contains(response.NarrowHint, "leaves out") || response.OmittedKinds != nil {
		t.Fatalf("kind=BGPConfiguration = %+v, want the row and no hint", response)
	}
	// A namespace query doesn't point at other scopes.
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{Namespace: "shop"})
	if strings.Contains(response.NarrowHint, "leaves out") {
		t.Fatalf("namespace get_changes hint = %q", response.NarrowHint)
	}
}
