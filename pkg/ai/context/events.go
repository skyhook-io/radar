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

// EventObjectRef identifies one involved object that contributed to a
// deduplicated event group.
type EventObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// DeduplicatedEventGroup is a DeduplicatedEvent plus the distinct involved
// objects behind it. Produced only by DeduplicateEventsWithObjects — the
// systemic grouping key stays (Reason, normalizedMessage, Type), so one
// group can span several objects; Objects makes that visible instead of
// leaving an aggregated Count with no subject.
type DeduplicatedEventGroup struct {
	DeduplicatedEvent
	// Objects lists distinct involved objects, most recent contribution
	// first (ties broken by kind/namespace/name), capped by the caller;
	// ObjectCount is the uncapped distinct total.
	Objects          []EventObjectRef `json:"objects,omitempty"`
	ObjectCount      int              `json:"objectCount,omitempty"`
	ObjectsTruncated bool             `json:"objectsTruncated,omitempty"`
}

// DeduplicateEvents groups similar K8s events by (Reason, normalizedMessage),
// collapses repeats with counts, sorts by last timestamp descending, and caps at 20.
func DeduplicateEvents(events []corev1.Event) []DeduplicatedEvent {
	groups := deduplicateEventGroups(events, 0)
	if len(groups) == 0 {
		return nil
	}
	result := make([]DeduplicatedEvent, len(groups))
	for i := range groups {
		result[i] = groups[i].DeduplicatedEvent
	}
	return result
}

// DeduplicateEventsWithObjects is DeduplicateEvents plus per-group involved
// objects, for surfaces (like the dashboard) where an aggregated count
// without a subject would be misleading. objectCap bounds Objects per group;
// ObjectCount always carries the uncapped distinct total.
func DeduplicateEventsWithObjects(events []corev1.Event, objectCap int) []DeduplicatedEventGroup {
	if objectCap <= 0 {
		objectCap = 1
	}
	return deduplicateEventGroups(events, objectCap)
}

func deduplicateEventGroups(events []corev1.Event, objectCap int) []DeduplicatedEventGroup {
	if len(events) == 0 {
		return nil
	}

	groups := make(map[eventKey]*DeduplicatedEventGroup)
	objects := make(map[eventKey]map[EventObjectRef]time.Time)
	order := make([]eventKey, 0)

	for i := range events {
		ev := &events[i]
		key := eventKey{
			Reason:            ev.Reason,
			NormalizedMessage: normalizeMessage(ev.Message),
			Type:              ev.Type,
		}

		last := eventLastTimestamp(ev)
		evCount := eventOccurrenceCount(ev)

		if existing, ok := groups[key]; ok {
			existing.Count += evCount
			if last.After(existing.LastTimestamp) {
				existing.LastTimestamp = last
				existing.Message = ev.Message // keep the most recent actual message
			} else if last.Equal(existing.LastTimestamp) && ev.Message < existing.Message {
				existing.Message = ev.Message // deterministic representative on exact ties
			}
		} else {
			groups[key] = &DeduplicatedEventGroup{DeduplicatedEvent: DeduplicatedEvent{
				Reason:        ev.Reason,
				Message:       ev.Message,
				Type:          ev.Type,
				Count:         evCount,
				LastTimestamp: last,
			}}
			order = append(order, key)
		}

		if objectCap > 0 && (ev.InvolvedObject.Kind != "" || ev.InvolvedObject.Name != "") {
			ref := EventObjectRef{
				Kind:      ev.InvolvedObject.Kind,
				Namespace: ev.InvolvedObject.Namespace,
				Name:      ev.InvolvedObject.Name,
			}
			seen := objects[key]
			if seen == nil {
				seen = make(map[EventObjectRef]time.Time)
				objects[key] = seen
			}
			if prev, ok := seen[ref]; !ok || last.After(prev) {
				seen[ref] = last
			}
		}
	}

	result := make([]DeduplicatedEventGroup, 0, len(groups))
	for _, key := range order {
		g := *groups[key]
		if objectCap > 0 {
			g.Objects, g.ObjectCount, g.ObjectsTruncated = selectGroupObjects(objects[key], objectCap)
		}
		result = append(result, g)
	}

	// Most recent first, with full deterministic tie-breakers BEFORE the
	// cap — otherwise which equal-timestamp groups survive the cut depends
	// on informer map iteration order.
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if !a.LastTimestamp.Equal(b.LastTimestamp) {
			return a.LastTimestamp.After(b.LastTimestamp)
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Message < b.Message
	})

	if len(result) > maxDeduplicatedEvents {
		result = result[:maxDeduplicatedEvents]
	}

	return result
}

// selectGroupObjects orders a group's distinct involved objects by most
// recent contribution (ties broken by kind/namespace/name for determinism)
// and caps the emitted list, counting distinct identities before the cap.
func selectGroupObjects(seen map[EventObjectRef]time.Time, limit int) ([]EventObjectRef, int, bool) {
	if len(seen) == 0 {
		return nil, 0, false
	}
	refs := make([]EventObjectRef, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		at, bt := seen[a], seen[b]
		if !at.Equal(bt) {
			return at.After(bt)
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	total := len(refs)
	if total > limit {
		return refs[:limit], total, true
	}
	return refs, total, false
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
	// Series-style events (events.k8s.io emitters mirrored into core/v1)
	// carry their latest occurrence in Series.LastObservedTime — legacy
	// LastTimestamp stays zero and EventTime is the FIRST occurrence, so
	// without this an actively repeating warning reads as stale.
	if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
		return ev.Series.LastObservedTime.Time
	}
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if ev.EventTime.Time.IsZero() {
		return ev.CreationTimestamp.Time
	}
	return ev.EventTime.Time
}

// eventOccurrenceCount reads the aggregate occurrence count: series-style
// events carry it in Series.Count (legacy Count stays zero for them).
func eventOccurrenceCount(ev *corev1.Event) int {
	if ev.Series != nil && ev.Series.Count > 0 {
		return int(ev.Series.Count)
	}
	return max(int(ev.Count), 1)
}
