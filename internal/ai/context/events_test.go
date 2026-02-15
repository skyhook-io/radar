package context

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeEvent(reason, message, eventType string, count int32, lastTime time.Time) corev1.Event {
	return corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("event-%s-%d", reason, count),
			Namespace: "default",
		},
		Reason:        reason,
		Message:       message,
		Type:          eventType,
		Count:         count,
		LastTimestamp: metav1.Time{Time: lastTime},
	}
}

func TestDeduplicateEvents_CollapseIdentical(t *testing.T) {
	now := time.Now()
	events := make([]corev1.Event, 50)
	for i := range events {
		events[i] = makeEvent("BackOff", "Back-off restarting failed container", "Warning", 1, now.Add(-time.Duration(50-i)*time.Second))
	}

	result := DeduplicateEvents(events)

	if len(result) != 1 {
		t.Errorf("Expected 1 deduplicated event, got %d", len(result))
	}
	if result[0].Count != 50 {
		t.Errorf("Expected count=50, got %d", result[0].Count)
	}
	if result[0].Reason != "BackOff" {
		t.Errorf("Expected reason=BackOff, got %s", result[0].Reason)
	}
}

func TestDeduplicateEvents_PreserveDifferentReasons(t *testing.T) {
	now := time.Now()
	events := []corev1.Event{
		makeEvent("BackOff", "Back-off restarting", "Warning", 1, now),
		makeEvent("Pulled", "Successfully pulled image", "Normal", 1, now.Add(-time.Second)),
		makeEvent("Created", "Created container", "Normal", 1, now.Add(-2*time.Second)),
	}

	result := DeduplicateEvents(events)

	if len(result) != 3 {
		t.Errorf("Expected 3 events, got %d", len(result))
	}
}

func TestDeduplicateEvents_SortsByLastTimestamp(t *testing.T) {
	now := time.Now()
	events := []corev1.Event{
		makeEvent("Old", "old event", "Warning", 1, now.Add(-10*time.Minute)),
		makeEvent("New", "new event", "Warning", 1, now),
		makeEvent("Mid", "mid event", "Warning", 1, now.Add(-5*time.Minute)),
	}

	result := DeduplicateEvents(events)

	if result[0].Reason != "New" {
		t.Errorf("Expected most recent first, got: %s", result[0].Reason)
	}
	if result[2].Reason != "Old" {
		t.Errorf("Expected oldest last, got: %s", result[2].Reason)
	}
}

func TestDeduplicateEvents_CapsAt20(t *testing.T) {
	now := time.Now()
	events := make([]corev1.Event, 30)
	for i := range events {
		events[i] = makeEvent(
			fmt.Sprintf("Reason%d", i),
			fmt.Sprintf("message %d", i),
			"Warning", 1,
			now.Add(-time.Duration(i)*time.Minute),
		)
	}

	result := DeduplicateEvents(events)

	if len(result) != 20 {
		t.Errorf("Expected max 20 events, got %d", len(result))
	}
}

func TestDeduplicateEvents_NormalizesMessages(t *testing.T) {
	now := time.Now()
	events := []corev1.Event{
		makeEvent("Failed", "Error on pod my-app-abc12-xyz45", "Warning", 1, now),
		makeEvent("Failed", "Error on pod my-app-def67-uvw89", "Warning", 1, now.Add(-time.Second)),
	}

	result := DeduplicateEvents(events)

	// These should be grouped because the normalized message is the same
	if len(result) != 1 {
		t.Errorf("Expected 1 grouped event (normalized messages), got %d", len(result))
	}
	if result[0].Count != 2 {
		t.Errorf("Expected count=2, got %d", result[0].Count)
	}
}

func TestDeduplicateEvents_UsesEventCount(t *testing.T) {
	now := time.Now()
	events := []corev1.Event{
		makeEvent("BackOff", "Back-off restarting", "Warning", 10, now),
		makeEvent("BackOff", "Back-off restarting", "Warning", 5, now.Add(-time.Second)),
	}

	result := DeduplicateEvents(events)

	if len(result) != 1 {
		t.Errorf("Expected 1 event, got %d", len(result))
	}
	if result[0].Count != 15 {
		t.Errorf("Expected count=15 (10+5), got %d", result[0].Count)
	}
}

func TestDeduplicateEvents_Empty(t *testing.T) {
	result := DeduplicateEvents(nil)
	if result != nil {
		t.Errorf("Expected nil, got %v", result)
	}
}

func TestFormatEvents_Output(t *testing.T) {
	events := []DeduplicatedEvent{
		{
			Reason:        "BackOff",
			Message:       "Back-off restarting failed container",
			Type:          "Warning",
			Count:         50,
			LastTimestamp: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		},
	}

	output := FormatEvents(events)

	if !contains(output, "BackOff") || !contains(output, "x50") {
		t.Errorf("Expected formatted event with count, got: %s", output)
	}
}

func TestFormatEvents_Empty(t *testing.T) {
	output := FormatEvents(nil)
	if output != "No events found." {
		t.Errorf("Expected 'No events found.', got: %s", output)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
