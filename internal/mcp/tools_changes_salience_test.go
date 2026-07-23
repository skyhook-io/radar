package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestGetChangesEmitsTierAWithoutIssueAwarePromotion(t *testing.T) {
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{
		ID: "frontend-env", Timestamp: time.Now().Add(-time.Minute),
		Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Deployment", Namespace: "shop", Name: "frontend",
		EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
			Path: "spec.template.spec.containers[frontend].env[CART_ADDR]",
		}}},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	result, _, err := handleGetChanges(context.Background(), nil, getChangesInput{
		Namespace: "shop",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("handleGetChanges: %v", err)
	}
	var response getChangesResponseMCP
	if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Changes) != 1 {
		t.Fatalf("changes = %+v, want one entry", response.Changes)
	}
	if got := response.Changes[0].Salience; got != issuesapi.ChangeSalienceConfigEdit {
		t.Fatalf("get_changes salience = %q, want config_edit only", got)
	}
}
