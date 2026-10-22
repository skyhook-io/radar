package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// appendInstall records count Deployment and count Service creates in shop,
// one second apart, plus, with edit, one env edit five minutes later.
func appendInstall(t *testing.T, count int, edit bool) {
	t.Helper()
	store := initCorrelationStore(t)
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	add := func(e timeline.TimelineEvent) {
		t.Helper()
		e.Source, e.ClusterContext, e.Namespace = timeline.SourceInformer, k8s.ActiveClusterContext(), "shop"
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatalf("append %s: %v", e.ID, err)
		}
	}
	for i := 0; i < count; i++ {
		at := start.Add(time.Duration(i) * time.Second)
		name := fmt.Sprintf("svc-%02d", i)
		add(timeline.TimelineEvent{ID: "deploy-" + name, Timestamp: at, Kind: "Deployment", APIVersion: "apps/v1", Name: name, EventType: timeline.EventTypeAdd})
		add(timeline.TimelineEvent{ID: "service-" + name, Timestamp: at, Kind: "Service", APIVersion: "v1", Name: name, EventType: timeline.EventTypeAdd})
	}
	if !edit {
		return
	}
	add(timeline.TimelineEvent{
		ID: "edit", Timestamp: start.Add(5 * time.Minute), Kind: "Deployment", APIVersion: "apps/v1", Name: "svc-00",
		EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{
			Path: "spec.template.spec.containers[app].env[CART_ADDR]", OldValue: "cart:7070", NewValue: "localhost:7070",
		}}},
	})
}

func getChangesForTest(t *testing.T, ctx context.Context, input getChangesInput) (getChangesResponseMCP, string) {
	t.Helper()
	result, _, err := handleGetChanges(ctx, nil, input)
	if err != nil {
		t.Fatalf("handleGetChanges: %v", err)
	}
	text := extractText(t, result)
	var response getChangesResponseMCP
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response, text
}

func TestGetChangesListsInstallCreatesCompactly(t *testing.T) {
	appendInstall(t, 6, true)
	response, text := getChangesForTest(t, context.Background(), getChangesInput{Namespace: "shop", Limit: 50})

	if len(response.Changes) != 1 || response.Changes[0].ChangeType != "update" {
		t.Fatalf("changes = %+v, want only the edit as a full row", response.Changes)
	}
	if len(response.Created) != 1 || response.Created[0].Count != 12 || response.Created[0].Namespace != "shop" {
		t.Fatalf("created = %+v, want one run of 12 creates", response.Created)
	}
	if got := response.Created[0].APIVersions; got["Deployment"] != "apps/v1" || got["Service"] != "v1" {
		t.Fatalf("apiVersions = %+v", got)
	}
	// Moving rows into created must not read as "lower-ranked changes were
	// omitted": every matched change was returned.
	if response.NarrowHint != "" {
		t.Fatalf("narrowHint = %q, want none for a complete, uncapped window", response.NarrowHint)
	}
	if strings.Index(text, `"created"`) < strings.Index(text, `"changes"`) {
		t.Fatalf("created must follow changes in the response: %s", text)
	}
}

func TestGetChangesCreateOnlyWindowKeepsChangesArray(t *testing.T) {
	appendInstall(t, 6, false)
	response, text := getChangesForTest(t, context.Background(), getChangesInput{Namespace: "shop", Limit: 50})
	if response.Changes == nil || len(response.Changes) != 0 || !strings.Contains(text, `"changes":[]`) {
		t.Fatalf("changes = %v (%s), want an empty array", response.Changes, text)
	}
	if len(response.Created) != 1 || response.Created[0].Count != 12 {
		t.Fatalf("created = %+v", response.Created)
	}
}

func TestGetChangesLimitCountsCreatedObjects(t *testing.T) {
	appendInstall(t, 6, true)
	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Namespace: "shop", Limit: 8})
	if total := len(response.Changes) + response.Created[0].Count; total != 8 {
		t.Fatalf("rows + created objects = %d, want limit 8", total)
	}
	if !strings.Contains(response.NarrowHint, "lower-ranked changes were omitted") {
		t.Fatalf("narrowHint = %q, want the capped-output hint", response.NarrowHint)
	}
}

func TestGetChangesNameFilterKeepsFullRows(t *testing.T) {
	appendInstall(t, 6, true)
	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Namespace: "shop", Name: "svc-00"})
	if response.Created != nil {
		t.Fatalf("created = %+v, want none for name=", response.Created)
	}
	if len(response.Changes) != 3 {
		t.Fatalf("changes = %+v, want the edit and both creates named svc-00", response.Changes)
	}
}

func TestGetChangesCompactsOnlyRowsTheCallerMayRead(t *testing.T) {
	appendInstall(t, 6, true)
	ctx := withClusterAdmin(t, "deploy-reader")
	perms := getPermCache().Get("deploy-reader", nil)
	perms.SetCanI("list", "apps", "deployments", "shop", true)
	t.Cleanup(getPermCache().Invalidate)

	response, text := getChangesForTest(t, ctx, getChangesInput{Namespace: "shop", Limit: 50})
	if strings.Contains(text, "Service/") || strings.Contains(text, `"Service"`) {
		t.Fatalf("a Service the caller cannot read leaked into the response: %s", text)
	}
	if len(response.Created) != 1 || response.Created[0].Count != 6 {
		t.Fatalf("created = %+v, want the 6 readable Deployments", response.Created)
	}
}
