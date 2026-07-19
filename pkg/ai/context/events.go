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
	// Runs of >=6 digits attached to a NAME (\b requires a word character
	// before the hyphen without consuming it, so adjacent segments like
	// name-{epoch}-{childID} both match) are generated name segments: epoch
	// suffixes in Argo cron workflow and CronJob pod names, controller
	// child-node IDs, cert-manager Order/Challenge FNV suffixes.
	// podHashPattern's {5,10} bound only partially consumes them, leaving
	// distinct digit tails that split one chronic failure into a new group
	// per incarnation. The word-boundary anchor restricts this to identifier
	// context: freestanding quantities (eviction thresholds, byte counts —
	// "using 123456789") and negative values ("value: -123456789") stay
	// distinct, and the >=6 floor additionally preserves ports, HTTP status
	// codes, and exit codes.
	longNumPattern = regexp.MustCompile(`\b-\d{6,}`)
	// CronJob MissSchedule emits RFC1123Z ("Sun, 19 Jul 2026 12:30:00
	// +0000"), repeatedly for a chronically missing schedule — the ISO
	// tsPattern doesn't touch it, so each emission formed a new group.
	rfc1123ZPattern = regexp.MustCompile(`\b(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun), \d{2} (?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{4} \d{2}:\d{2}:\d{2} [+-]\d{4}`)
	// Kubelet SystemOOM ("victim process: X, pid: 1234567") embeds the
	// ephemeral PID — identity, never magnitude — so repeated OOMs split
	// per kill. All widths normalized: a 5-digit PID is no more meaningful
	// than a 7-digit one.
	pidPattern = regexp.MustCompile(`\bpid: \d+\b`)
)

func normalizeMessage(msg string) string {
	// longNumPattern runs AFTER the specific patterns — a digit-heavy UUID
	// segment or IP would otherwise be eaten before uuidPattern/ipPattern
	// can recognize it — and BEFORE podHashPattern, whose {5,10} bound is
	// what leaves the digit tails.
	//
	// Its placeholder must stay inside [a-z0-9]: CronJob pod names are
	// {name}-{unixMinutes}-{rand5}, and an out-of-class token (e.g. "<n>")
	// in the middle would stop podHashPattern from consuming the whole
	// name, splitting same-shaped events on the rand5 tail — the exact
	// per-incarnation splitting this pattern exists to fix.
	s := uuidPattern.ReplaceAllString(msg, "<uuid>")
	s = tsPattern.ReplaceAllString(s, "<timestamp>")
	s = rfc1123ZPattern.ReplaceAllString(s, "<timestamp>")
	s = ipPattern.ReplaceAllString(s, "<ip>")
	s = pidPattern.ReplaceAllString(s, "pid: <pid>")
	s = longNumPattern.ReplaceAllString(s, "-nnnnnn")
	s = podHashPattern.ReplaceAllString(s, "<pod>")
	return s
}

type eventKey struct {
	Reason            string
	NormalizedMessage string
	Type              string
}

// EventObjectRef identifies one involved object that contributed to a
// deduplicated event group. APIVersion is carried so consumers can
// disambiguate colliding kinds (core Service vs Knative Service) when
// feeding the ref into resource lookups.
type EventObjectRef struct {
	Kind       string `json:"kind"`
	APIVersion string `json:"apiVersion,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
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
	objects := make(map[eventKey]map[eventObjectIdentity]objectSighting)
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

		// Both kind AND name required — legacy Event validation doesn't
		// reliably enforce either, and a partial ref ("Pod/shop/" or
		// "/shop/foo") is not a usable identity.
		if objectCap > 0 && ev.InvolvedObject.Kind != "" && ev.InvolvedObject.Name != "" {
			ref := EventObjectRef{
				Kind:       ev.InvolvedObject.Kind,
				APIVersion: ev.InvolvedObject.APIVersion,
				Namespace:  ev.InvolvedObject.Namespace,
				Name:       ev.InvolvedObject.Name,
			}
			id := identityForInvolvedObject(&ev.InvolvedObject)
			seen := objects[key]
			if seen == nil {
				seen = make(map[eventObjectIdentity]objectSighting)
				objects[key] = seen
			}
			prev, existed := seen[id]
			if !existed || last.After(prev.Last) {
				// Carry a known apiVersion across sightings — emitters are
				// inconsistent about populating it, and losing it would
				// mislabel the object's API group downstream.
				if existed && ref.APIVersion == "" && prev.Ref.APIVersion != "" {
					ref.APIVersion = prev.Ref.APIVersion
				}
				seen[id] = objectSighting{Ref: ref, Last: last}
			} else if existed && prev.Ref.APIVersion == "" && ref.APIVersion != "" {
				prev.Ref.APIVersion = ref.APIVersion
				seen[id] = prev
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
	// on informer map iteration order. Type is a comparator key because it
	// is a grouping key: Normal and Warning groups can tie on everything
	// else.
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
		if a.Message != b.Message {
			return a.Message < b.Message
		}
		return a.Type < b.Type
	})

	if len(result) > maxDeduplicatedEvents {
		result = result[:maxDeduplicatedEvents]
	}

	return result
}

// eventObjectIdentity is the dedup key for involved objects. UID is the
// primary identity when the emitter populated it; otherwise kind + API
// group + namespace + name. Version-within-group is deliberately excluded —
// it never distinguishes objects, and emitters populate apiVersion
// inconsistently (which also means the group fallback can split one
// non-core object seen with and without apiVersion; UID avoids that
// whenever it is available).
type eventObjectIdentity struct {
	UID       string
	Kind      string
	Group     string
	Namespace string
	Name      string
}

// identityForInvolvedObject builds the dedup key: UID alone when present,
// else the kind/group/namespace/name fallback.
func identityForInvolvedObject(obj *corev1.ObjectReference) eventObjectIdentity {
	if obj.UID != "" {
		return eventObjectIdentity{UID: string(obj.UID)}
	}
	return eventObjectIdentity{
		Kind:      obj.Kind,
		Group:     GroupOfAPIVersion(obj.APIVersion),
		Namespace: obj.Namespace,
		Name:      obj.Name,
	}
}

// objectSighting keeps the most recent full ref observed for one identity.
type objectSighting struct {
	Ref  EventObjectRef
	Last time.Time
}

// GroupOfAPIVersion returns the API group portion of an apiVersion string
// ("apps/v1" → "apps"; "v1" or "" → "" for the core group).
func GroupOfAPIVersion(apiVersion string) string {
	if idx := strings.IndexByte(apiVersion, '/'); idx > 0 {
		return apiVersion[:idx]
	}
	return ""
}

// selectGroupObjects orders a group's distinct involved objects by most
// recent contribution (ties broken by kind/namespace/name for determinism)
// and caps the emitted list, counting distinct identities before the cap.
// Distinct means distinct EMITTED identity (kind/group/namespace/name):
// UID keying during collection can hold several incarnations of one name
// (a StatefulSet pod deleted and recreated while old events linger), and
// for a lookup-oriented surface those are one subject, not duplicates.
func selectGroupObjects(seen map[eventObjectIdentity]objectSighting, limit int) ([]EventObjectRef, int, bool) {
	if len(seen) == 0 {
		return nil, 0, false
	}
	merged := make(map[eventObjectIdentity]objectSighting, len(seen))
	for _, s := range seen {
		id := eventObjectIdentity{
			Kind:      s.Ref.Kind,
			Group:     GroupOfAPIVersion(s.Ref.APIVersion),
			Namespace: s.Ref.Namespace,
			Name:      s.Ref.Name,
		}
		prev, existed := merged[id]
		if !existed || s.Last.After(prev.Last) {
			if existed && s.Ref.APIVersion == "" && prev.Ref.APIVersion != "" {
				s.Ref.APIVersion = prev.Ref.APIVersion
			}
			merged[id] = s
		} else if prev.Ref.APIVersion == "" && s.Ref.APIVersion != "" {
			prev.Ref.APIVersion = s.Ref.APIVersion
			merged[id] = prev
		}
	}
	sightings := make([]objectSighting, 0, len(merged))
	for _, s := range merged {
		sightings = append(sightings, s)
	}
	// Sort by the emitted ref, not the identity key — UID-keyed identities
	// carry no name fields.
	sort.Slice(sightings, func(i, j int) bool {
		a, b := sightings[i], sightings[j]
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		if a.Ref.Kind != b.Ref.Kind {
			return a.Ref.Kind < b.Ref.Kind
		}
		if a.Ref.Namespace != b.Ref.Namespace {
			return a.Ref.Namespace < b.Ref.Namespace
		}
		if a.Ref.Name != b.Ref.Name {
			return a.Ref.Name < b.Ref.Name
		}
		return a.Ref.APIVersion < b.Ref.APIVersion
	})
	total := len(sightings)
	truncated := total > limit
	if truncated {
		sightings = sightings[:limit]
	}
	refs := make([]EventObjectRef, len(sightings))
	for i, s := range sightings {
		refs[i] = s.Ref
	}
	return refs, total, truncated
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
