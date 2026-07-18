package context

import (
	"encoding/json"
	"fmt"
	"strings"
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

func makeEventForObject(reason, message, eventType string, count int32, lastTime time.Time, kind, namespace, name string) corev1.Event {
	ev := makeEvent(reason, message, eventType, count, lastTime)
	ev.InvolvedObject = corev1.ObjectReference{Kind: kind, Namespace: namespace, Name: name}
	return ev
}

// The systemic grouping key spans objects: two pods emitting the same
// normalized warning collapse into ONE group whose count aggregates both —
// Objects must name each contributor instead of leaving the count subjectless.
func TestDeduplicateEventsWithObjects_CollectsDistinctObjects(t *testing.T) {
	now := time.Now()
	events := []corev1.Event{
		makeEventForObject("BackOff", "Back-off restarting failed container in pod cart-5d4f7c9b8-aaaaa", "Warning", 3, now.Add(-10*time.Minute), "Pod", "shop", "cart-5d4f7c9b8-aaaaa"),
		makeEventForObject("BackOff", "Back-off restarting failed container in pod cart-5d4f7c9b8-bbbbb", "Warning", 2, now.Add(-1*time.Minute), "Pod", "shop", "cart-5d4f7c9b8-bbbbb"),
	}

	result := DeduplicateEventsWithObjects(events, 3)

	if len(result) != 1 {
		t.Fatalf("expected 1 group (pod-hash normalization), got %d: %+v", len(result), result)
	}
	g := result[0]
	if g.Count != 5 {
		t.Errorf("count = %d, want 5 (aggregated across objects)", g.Count)
	}
	if g.ObjectCount != 2 || g.ObjectsTruncated {
		t.Errorf("objectCount = %d truncated=%v, want 2/false", g.ObjectCount, g.ObjectsTruncated)
	}
	if len(g.Objects) != 2 {
		t.Fatalf("objects = %+v, want both pods", g.Objects)
	}
	// Most recent contributor first.
	if g.Objects[0].Name != "cart-5d4f7c9b8-bbbbb" || g.Objects[1].Name != "cart-5d4f7c9b8-aaaaa" {
		t.Errorf("objects order = %+v, want most-recent first", g.Objects)
	}
}

// Distinct identities are counted before the cap, and equal timestamps fall
// back to name order so the emitted subset is deterministic.
func TestDeduplicateEventsWithObjects_CapsDeterministically(t *testing.T) {
	now := time.Now()
	var events []corev1.Event
	for _, name := range []string{"pod-d", "pod-b", "pod-c", "pod-a", "pod-e"} {
		events = append(events, makeEventForObject("BackOff", "Back-off restarting failed container", "Warning", 1, now, "Pod", "shop", name))
	}

	result := DeduplicateEventsWithObjects(events, 3)

	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	g := result[0]
	if g.ObjectCount != 5 || !g.ObjectsTruncated {
		t.Errorf("objectCount = %d truncated=%v, want 5/true (distinct counted before cap)", g.ObjectCount, g.ObjectsTruncated)
	}
	if len(g.Objects) != 3 {
		t.Fatalf("objects = %+v, want capped at 3", g.Objects)
	}
	for i, want := range []string{"pod-a", "pod-b", "pod-c"} {
		if g.Objects[i].Name != want {
			t.Errorf("objects[%d] = %s, want %s (name order on equal timestamps)", i, g.Objects[i].Name, want)
		}
	}
}

// Series-style events (events.k8s.io mirrored into core/v1) carry recency
// and count in Series, not the legacy fields — an actively repeating warning
// must not read as a stale count-1 one-off.
func TestDeduplicateEvents_HonorsEventSeries(t *testing.T) {
	now := time.Now()
	ev := corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "series-ev", Namespace: "default"},
		Reason:     "Unhealthy", Message: "Readiness probe failed", Type: "Warning",
		// Legacy fields as a series emitter leaves them: Count zero,
		// LastTimestamp zero, EventTime = FIRST occurrence (an hour ago).
		EventTime: metav1.MicroTime{Time: now.Add(-1 * time.Hour)},
		Series: &corev1.EventSeries{
			Count:            42,
			LastObservedTime: metav1.MicroTime{Time: now.Add(-30 * time.Second)},
		},
	}

	result := DeduplicateEvents([]corev1.Event{ev})
	if len(result) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result))
	}
	if result[0].Count != 42 {
		t.Errorf("count = %d, want 42 (Series.Count)", result[0].Count)
	}
	if got := result[0].LastTimestamp; now.Sub(got) > time.Minute {
		t.Errorf("lastTimestamp = %v, want Series.LastObservedTime (~30s ago), not first-occurrence EventTime", got)
	}
}

// Which equal-timestamp groups survive the 20-group cap must not depend on
// input (informer map) order: same events, two orders, same output.
func TestDeduplicateEvents_DeterministicUnderCapTies(t *testing.T) {
	now := time.Now()
	var events []corev1.Event
	for i := 0; i < 30; i++ {
		events = append(events, makeEvent(fmt.Sprintf("Reason%02d", i), fmt.Sprintf("message %02d", i), "Warning", 1, now))
	}
	reversed := make([]corev1.Event, len(events))
	for i := range events {
		reversed[len(events)-1-i] = events[i]
	}

	a := DeduplicateEvents(events)
	b := DeduplicateEvents(reversed)
	if len(a) != maxDeduplicatedEvents || len(b) != maxDeduplicatedEvents {
		t.Fatalf("lens = %d/%d, want both capped at %d", len(a), len(b), maxDeduplicatedEvents)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("order diverged at %d under equal timestamps: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// Type is a grouping key, so it must also be a comparator key: a Normal and
// a Warning group identical on every other field must not swap which one
// survives the 20-group cap based on input order.
func TestDeduplicateEvents_TypeTieDeterministicAtCap(t *testing.T) {
	now := time.Now()
	events := make([]corev1.Event, 0, 21)
	for i := 0; i < 19; i++ {
		events = append(events, makeEvent(fmt.Sprintf("A%02d", i), "m", "Warning", 1, now))
	}
	events = append(events,
		makeEvent("Z", "same", "Normal", 1, now),
		makeEvent("Z", "same", "Warning", 1, now),
	)
	reversed := make([]corev1.Event, len(events))
	for i := range events {
		reversed[len(events)-1-i] = events[i]
	}
	a, b := DeduplicateEvents(events), DeduplicateEvents(reversed)
	if len(a) != maxDeduplicatedEvents || len(b) != maxDeduplicatedEvents {
		t.Fatalf("lens = %d/%d, want both capped", len(a), len(b))
	}
	if a[len(a)-1] != b[len(b)-1] {
		t.Fatalf("cap survivor depends on input order: %+v vs %+v", a[len(a)-1], b[len(b)-1])
	}
}

// Partial involved-object refs (kind without name, or name without kind) are
// not a usable identity and must be dropped, not emitted malformed. APIVersion
// is carried through for kind disambiguation.
func TestDeduplicateEventsWithObjects_RefValidityAndAPIVersion(t *testing.T) {
	now := time.Now()
	full := makeEventForObject("BackOff", "restarting", "Warning", 1, now, "Pod", "shop", "pod-a")
	full.InvolvedObject.APIVersion = "v1"
	kindless := makeEventForObject("BackOff", "restarting", "Warning", 1, now, "", "shop", "orphan")
	nameless := makeEventForObject("BackOff", "restarting", "Warning", 1, now, "Pod", "shop", "")

	result := DeduplicateEventsWithObjects([]corev1.Event{full, kindless, nameless}, 3)
	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	g := result[0]
	if g.ObjectCount != 1 || len(g.Objects) != 1 {
		t.Fatalf("objects = %+v (count %d), want only the fully-identified ref", g.Objects, g.ObjectCount)
	}
	want := EventObjectRef{Kind: "Pod", APIVersion: "v1", Namespace: "shop", Name: "pod-a"}
	if g.Objects[0] != want {
		t.Errorf("objects[0] = %+v, want %+v", g.Objects[0], want)
	}
}

// One object seen through emitters that populate apiVersion inconsistently
// ("" vs "v1") is ONE object — identity keys on the API group, never the raw
// apiVersion string.
func TestDeduplicateEventsWithObjects_APIVersionVariantsAreOneIdentity(t *testing.T) {
	now := time.Now()
	older := makeEventForObject("BackOff", "restarting", "Warning", 1, now.Add(-time.Minute), "Pod", "shop", "pod-a")
	newer := makeEventForObject("BackOff", "restarting", "Warning", 1, now, "Pod", "shop", "pod-a")
	newer.InvolvedObject.APIVersion = "v1"

	result := DeduplicateEventsWithObjects([]corev1.Event{older, newer}, 3)
	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	g := result[0]
	if g.ObjectCount != 1 || len(g.Objects) != 1 {
		t.Fatalf("objects = %+v (count %d), want ONE identity across apiVersion variants", g.Objects, g.ObjectCount)
	}
	if g.Objects[0].APIVersion != "v1" {
		t.Errorf("kept ref apiVersion = %q, want the most recent sighting's (\"v1\")", g.Objects[0].APIVersion)
	}
}

// A NON-core object seen with and without apiVersion is still one identity
// when the emitter set UID (the group fallback alone cannot merge "" with
// "apps/v1"), and the known apiVersion is carried onto the kept ref even
// when the most recent sighting lacked it.
func TestDeduplicateEventsWithObjects_UIDMergesAcrossMissingAPIVersion(t *testing.T) {
	now := time.Now()
	withVersion := makeEventForObject("ScalingReplicaSet", "scaled down", "Warning", 1, now.Add(-time.Minute), "Deployment", "shop", "web")
	withVersion.InvolvedObject.APIVersion = "apps/v1"
	withVersion.InvolvedObject.UID = "uid-web-1"
	versionless := makeEventForObject("ScalingReplicaSet", "scaled down", "Warning", 1, now, "Deployment", "shop", "web")
	versionless.InvolvedObject.UID = "uid-web-1"

	result := DeduplicateEventsWithObjects([]corev1.Event{withVersion, versionless}, 3)
	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	g := result[0]
	if g.ObjectCount != 1 || len(g.Objects) != 1 {
		t.Fatalf("objects = %+v (count %d), want ONE identity via UID", g.Objects, g.ObjectCount)
	}
	if g.Objects[0].APIVersion != "apps/v1" {
		t.Errorf("kept ref apiVersion = %q, want \"apps/v1\" carried from the earlier sighting", g.Objects[0].APIVersion)
	}
}

// Two UIDs behind one name are successive incarnations (a StatefulSet pod
// deleted and recreated while its old events linger), not two objects: the
// emitted list must not repeat an identical kind/group/namespace/name ref,
// and objectCount must not inflate past what a consumer can act on.
func TestDeduplicateEventsWithObjects_RecreatedObjectIsOneRef(t *testing.T) {
	now := time.Now()
	oldIncarnation := makeEventForObject("BackOff", "restarting", "Warning", 3, now.Add(-time.Minute), "Pod", "shop", "db-0")
	oldIncarnation.InvolvedObject.UID = "uid-old"
	newIncarnation := makeEventForObject("BackOff", "restarting", "Warning", 1, now, "Pod", "shop", "db-0")
	newIncarnation.InvolvedObject.UID = "uid-new"
	newIncarnation.InvolvedObject.APIVersion = "v1"

	result := DeduplicateEventsWithObjects([]corev1.Event{oldIncarnation, newIncarnation}, 3)
	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	g := result[0]
	if g.ObjectCount != 1 || len(g.Objects) != 1 || g.ObjectsTruncated {
		t.Fatalf("objects = %+v (count %d, truncated %v), want ONE ref across incarnations", g.Objects, g.ObjectCount, g.ObjectsTruncated)
	}
	want := EventObjectRef{Kind: "Pod", APIVersion: "v1", Namespace: "shop", Name: "db-0"}
	if g.Objects[0] != want {
		t.Errorf("objects[0] = %+v, want most-recent incarnation's ref %+v", g.Objects[0], want)
	}
}

// The plain DeduplicateEvents wire shape is unchanged by the objects path —
// its consumers (get_events, diagnose, resource includes) group across pods
// on purpose and must not grow new fields.
func TestDeduplicateEvents_ShapeUnchanged(t *testing.T) {
	now := time.Now()
	events := []corev1.Event{
		makeEventForObject("BackOff", "Back-off restarting", "Warning", 1, now, "Pod", "shop", "pod-a"),
	}
	result := DeduplicateEvents(events)
	if len(result) != 1 {
		t.Fatalf("expected 1 event, got %d", len(result))
	}
	data, err := json.Marshal(result[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"objects", "objectCount", "objectsTruncated"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("DeduplicatedEvent JSON grew %q: %s", forbidden, data)
		}
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
