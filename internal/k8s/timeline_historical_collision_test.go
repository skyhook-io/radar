package k8s

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestExtractTimelineHistoricalEvents_UnstructuredKindCollisionsKeepCreatedEvent(t *testing.T) {
	createdAt := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		apiVersion string
		kind       string
	}{
		{name: "Volcano Job", apiVersion: "batch.volcano.sh/v1alpha1", kind: "Job"},
		{name: "Knative Service", apiVersion: "serving.knative.dev/v1", kind: "Service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := historicalUnstructured(tt.apiVersion, tt.kind, createdAt)
			events := extractTimelineHistoricalEvents("cluster-a", tt.kind, tt.apiVersion, "gpu-demo", "collision", obj, nil, nil)

			assertSingleCreatedHistoricalEvent(t, events, tt.apiVersion, createdAt)
		})
	}
}

func TestExtractTimelineHistoricalEvents_GenericUnstructuredKeepsCreatedEvent(t *testing.T) {
	createdAt := time.Date(2026, time.August, 31, 12, 30, 0, 0, time.UTC)
	obj := historicalUnstructured("example.io/v1", "Widget", createdAt)

	events := extractTimelineHistoricalEvents("cluster-a", "Widget", "example.io/v1", "gpu-demo", "generic", obj, nil, nil)

	assertSingleCreatedHistoricalEvent(t, events, "example.io/v1", createdAt)
}

func TestExtractTimelineHistoricalEvents_UnstructuredWithoutCreationTimestampEmitsNothing(t *testing.T) {
	obj := historicalUnstructured("batch.volcano.sh/v1alpha1", "Job", time.Time{})

	events := extractTimelineHistoricalEvents("cluster-a", "Job", "batch.volcano.sh/v1alpha1", "gpu-demo", "collision", obj, nil, nil)

	if len(events) != 0 {
		t.Fatalf("historical events = %+v, want none", events)
	}
}

func TestExtractTimelineHistoricalEvents_TypedCoreJobDoesNotDuplicateCreatedEvent(t *testing.T) {
	createdAt := time.Date(2026, time.August, 31, 13, 0, 0, 0, time.UTC)
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:              "core-job",
		Namespace:         "gpu-demo",
		CreationTimestamp: metav1.NewTime(createdAt),
	}}

	events := extractTimelineHistoricalEvents("cluster-a", "Job", "", "gpu-demo", "core-job", job, nil, nil)

	assertSingleCreatedHistoricalEvent(t, events, "", createdAt)
}

func historicalUnstructured(apiVersion, kind string, createdAt time.Time) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetNamespace("gpu-demo")
	obj.SetName("collision")
	obj.SetCreationTimestamp(metav1.NewTime(createdAt))
	return obj
}

func assertSingleCreatedHistoricalEvent(t *testing.T, events []timeline.TimelineEvent, apiVersion string, createdAt time.Time) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("historical events = %d, want exactly one: %+v", len(events), events)
	}
	event := events[0]
	if event.Source != timeline.SourceHistorical || event.Reason != "created" {
		t.Fatalf("historical event = %+v, want one created event", event)
	}
	if event.APIVersion != apiVersion {
		t.Fatalf("apiVersion = %q, want %q", event.APIVersion, apiVersion)
	}
	if !event.Timestamp.Equal(createdAt) {
		t.Fatalf("timestamp = %s, want %s", event.Timestamp, createdAt)
	}
}
