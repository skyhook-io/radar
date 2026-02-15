package context

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const maxDeduplicatedEvents = 20

// DeduplicatedEvent represents a group of similar K8s events collapsed into one.
type DeduplicatedEvent struct {
	Reason        string    `json:"reason"`
	Message       string    `json:"message"`
	Type          string    `json:"type"` // Normal or Warning
	Count         int       `json:"count"`
	LastTimestamp time.Time `json:"lastTimestamp"`
}

// String returns a human-readable representation for LLM context.
func (e DeduplicatedEvent) String() string {
	if e.Count > 1 {
		return fmt.Sprintf("[%s] %s (x%d, last=%s): %s",
			e.Type, e.Reason, e.Count,
			e.LastTimestamp.Format(time.RFC3339), e.Message)
	}
	return fmt.Sprintf("[%s] %s (%s): %s",
		e.Type, e.Reason,
		e.LastTimestamp.Format(time.RFC3339), e.Message)
}

// normalizing patterns: replace pod hashes, UUIDs, timestamps with placeholders
var (
	podHashPattern = regexp.MustCompile(`[a-z0-9]+-[a-z0-9]{5,10}(-[a-z0-9]{5})?`)
	uuidPattern    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	tsPattern      = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`)
	ipPattern      = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?`)
)

func normalizeMessage(msg string) string {
	s := uuidPattern.ReplaceAllString(msg, "<uuid>")
	s = tsPattern.ReplaceAllString(s, "<timestamp>")
	s = ipPattern.ReplaceAllString(s, "<ip>")
	s = podHashPattern.ReplaceAllString(s, "<pod>")
	return s
}

type eventKey struct {
	Reason            string
	NormalizedMessage string
	Type              string
}

// DeduplicateEvents groups similar K8s events by (Reason, normalizedMessage),
// collapses repeats with counts, sorts by last timestamp descending, and caps at 20.
func DeduplicateEvents(events []corev1.Event) []DeduplicatedEvent {
	if len(events) == 0 {
		return nil
	}

	groups := make(map[eventKey]*DeduplicatedEvent)
	order := make([]eventKey, 0)

	for i := range events {
		ev := &events[i]
		key := eventKey{
			Reason:            ev.Reason,
			NormalizedMessage: normalizeMessage(ev.Message),
			Type:              ev.Type,
		}

		last := eventLastTimestamp(ev)
		evCount := int(ev.Count)
		if evCount < 1 {
			evCount = 1
		}

		if existing, ok := groups[key]; ok {
			existing.Count += evCount
			if last.After(existing.LastTimestamp) {
				existing.LastTimestamp = last
				existing.Message = ev.Message // keep the most recent actual message
			}
		} else {
			groups[key] = &DeduplicatedEvent{
				Reason:        ev.Reason,
				Message:       ev.Message,
				Type:          ev.Type,
				Count:         evCount,
				LastTimestamp: last,
			}
			order = append(order, key)
		}
	}

	result := make([]DeduplicatedEvent, 0, len(groups))
	for _, key := range order {
		result = append(result, *groups[key])
	}

	// Sort by last timestamp descending (most recent first)
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastTimestamp.After(result[j].LastTimestamp)
	})

	if len(result) > maxDeduplicatedEvents {
		result = result[:maxDeduplicatedEvents]
	}

	return result
}

// FormatEvents renders deduplicated events as a string for LLM context.
func FormatEvents(events []DeduplicatedEvent) string {
	if len(events) == 0 {
		return "No events found."
	}
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func eventLastTimestamp(ev *corev1.Event) time.Time {
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if ev.EventTime.Time.IsZero() {
		return ev.CreationTimestamp.Time
	}
	return ev.EventTime.Time
}
