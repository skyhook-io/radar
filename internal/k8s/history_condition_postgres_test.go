package k8s

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

func TestComputeDiff_ConditionPostgresRoundTrip(t *testing.T) {
	baseDSN := os.Getenv("RADAR_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("RADAR_TEST_POSTGRES_DSN is not set")
	}
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL test database: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL test database: %v", err)
		}
	})
	schema := fmt.Sprintf("radar_condition_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create PostgreSQL test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop PostgreSQL test schema: %v", err)
		}
	})
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse RADAR_TEST_POSTGRES_DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	store, err := timeline.NewPostgresStore(parsed.String())
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var events []timeline.TimelineEvent
	for i, reasons := range [][2]string{
		{"Pending", "Ready"},
		{`Waiting: "update"`, `Ready: "updated"`},
		{"\x00", `\x00`},
	} {
		diff := ComputeDiffFromUnstructured("Widget",
			conditionEncodingResource("False", reasons[0]),
			conditionEncodingResource("True", reasons[1]))
		if diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != "status.conditions[Ready]" {
			t.Fatalf("condition diff = %+v, want one condition field", diff)
		}
		events = append(events, timeline.TimelineEvent{
			ID: fmt.Sprintf("condition-%d", i), Timestamp: time.Now().UTC(),
			Source: timeline.SourceInformer, EventType: timeline.EventTypeUpdate,
			Kind: "Widget", APIVersion: "example.com/v1", Namespace: "default", Name: "widget",
			Diff: diff,
		})
	}
	if err := store.AppendBatch(t.Context(), events); err != nil {
		t.Fatalf("AppendBatch condition diffs: %v", err)
	}
	for _, want := range events {
		got, err := store.GetEvent(t.Context(), want.ID)
		if err != nil || got == nil {
			t.Fatalf("GetEvent(%q): %v %+v", want.ID, err, got)
		}
		if !reflect.DeepEqual(got.Diff, want.Diff) {
			t.Errorf("GetEvent(%q) diff = %+v, want %+v", want.ID, got.Diff, want.Diff)
		}
	}
}
