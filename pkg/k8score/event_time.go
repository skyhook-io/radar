package k8score

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// EventLastObservedTime handles both core Events and events.k8s.io series.
// EventTime is the first occurrence, not the latest series heartbeat.
func EventLastObservedTime(event *corev1.Event) time.Time {
	last := event.LastTimestamp.Time
	if event.Series != nil && event.Series.LastObservedTime.After(last) {
		last = event.Series.LastObservedTime.Time
	}
	if last.IsZero() {
		last = event.EventTime.Time
	}
	return last
}

// EventOccurrences preserves the count on modern aggregated Event series.
func EventOccurrences(event *corev1.Event) int32 {
	if event.Series != nil && event.Series.Count > 0 {
		return event.Series.Count
	}
	return max(event.Count, 1)
}
