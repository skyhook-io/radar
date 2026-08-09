package server

import (
	"encoding/base64"
	"net/url"
	"testing"
	"time"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/timeline"
)

func TestCapacityActivityObservationStartUsesRetainedMemoryEvidenceWithoutPredatingProcess(t *testing.T) {
	processStart := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	now := processStart.Add(time.Hour)
	evicted := timeline.StoreStats{MaxEvents: 3, EventsEvicted: true}

	for _, test := range []struct {
		name        string
		stats       timeline.StoreStats
		oldestEvent time.Time
		want        time.Time
	}{
		{name: "unevicted memory", stats: timeline.StoreStats{MaxEvents: 3}, oldestEvent: processStart.Add(10 * time.Minute), want: processStart},
		{name: "evicted event before process", stats: evicted, oldestEvent: processStart.Add(-10 * time.Minute), want: processStart},
		{name: "evicted event after process", stats: evicted, oldestEvent: processStart.Add(10 * time.Minute), want: processStart.Add(10 * time.Minute)},
		{name: "evicted without visible events", stats: evicted, want: now},
		{name: "persistent history may predate process", oldestEvent: processStart.Add(-10 * time.Minute), want: processStart.Add(-10 * time.Minute)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := capacityActivityObservationStart(now, processStart, test.stats, test.oldestEvent, nil)
			if !got.Equal(test.want) {
				t.Fatalf("observation start = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCapacityActivityCursorRoundTripAndFilterBinding(t *testing.T) {
	filters := url.Values{"pool": {"general"}, "claim": {"general-abc12"}}
	fingerprint := capacityFilterFingerprint(filters)
	encoded, err := encodeCapacityActivityCursor("epoch-a", fingerprint, "cluster-a", 42, "older")
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	decoded, err := decodeCapacityActivityCursor(encoded)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if decoded.Epoch != "epoch-a" || decoded.Seq != 42 || decoded.Direction != "older" || decoded.FilterFingerprint != fingerprint || decoded.ClusterContext != "cluster-a" {
		t.Fatalf("decoded cursor = %#v", decoded)
	}

	request, err := parseCapacityActivityRequest(url.Values{
		"pool": {"general"}, "claim": {"general-abc12"}, "cursor": {encoded}, "limit": {"75"},
	}, "cluster-a")
	if err != nil || request.cursor == nil || request.limit != 75 {
		t.Fatalf("parse bound cursor = %#v, %v", request, err)
	}
	if _, err := parseCapacityActivityRequest(url.Values{"pool": {"other"}, "claim": {"general-abc12"}, "cursor": {encoded}}, "cluster-a"); err == nil || !isCapacityCursorInvalidError(err) {
		t.Fatalf("changed filters error = %v, want classified cursor error", err)
	}
	// The cluster the cursor is checked against is the caller's CAPTURED stamp,
	// not a fresh global read — that is what keeps a mid-request switch from
	// admitting the previous cluster's cursor.
	if _, err := parseCapacityActivityRequest(url.Values{"pool": {"general"}, "claim": {"general-abc12"}, "cursor": {encoded}}, "cluster-b"); err == nil || !isCapacityCursorInvalidError(err) {
		t.Fatalf("changed cluster error = %v, want classified cursor error", err)
	}
}

func TestCapacityActivityRequestTracksExplicitSince(t *testing.T) {
	request, err := parseCapacityActivityRequest(url.Values{"since": {"2026-07-13T10:00:00Z"}}, k8s.ActiveClusterContext())
	if err != nil {
		t.Fatalf("parse since: %v", err)
	}
	if !request.sinceProvided || request.since.IsZero() {
		t.Fatalf("explicit since was not retained: %#v", request)
	}
	request, err = parseCapacityActivityRequest(url.Values{}, k8s.ActiveClusterContext())
	if err != nil || request.sinceProvided || !request.since.IsZero() {
		t.Fatalf("implicit since = %#v, %v", request, err)
	}
}

func TestCapacityActivityCursorRejectsMalformedPayload(t *testing.T) {
	malformed := map[string]string{
		"base64":        "%%%",
		"unknown field": base64.RawURLEncoding.EncodeToString([]byte(`{"version":2,"epoch":"e","filterFingerprint":"f","seq":1,"direction":"older","extra":true}`)),
		"version":       base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"epoch":"e","filterFingerprint":"f","seq":1,"direction":"older"}`)),
		"negative seq":  base64.RawURLEncoding.EncodeToString([]byte(`{"version":2,"epoch":"e","filterFingerprint":"f","seq":-1,"direction":"older"}`)),
		"direction":     base64.RawURLEncoding.EncodeToString([]byte(`{"version":2,"epoch":"e","filterFingerprint":"f","seq":1,"direction":"sideways"}`)),
	}
	for name, cursor := range malformed {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCapacityActivityCursor(cursor); err == nil || !isCapacityCursorInvalidError(err) {
				t.Fatalf("malformed cursor error = %v, want classified cursor error", err)
			}
		})
	}
	if _, err := parseCapacityActivityRequest(url.Values{"cursor": {"one", "two"}}, k8s.ActiveClusterContext()); err == nil || !isCapacityCursorInvalidError(err) {
		t.Fatalf("repeated cursor error = %v, want classified cursor error", err)
	}
}

func TestCapacityActivityTypeFilterBindsFingerprintAndNarrowsRecords(t *testing.T) {
	base, err := parseCapacityActivityRequest(url.Values{}, k8s.ActiveClusterContext())
	if err != nil {
		t.Fatalf("parse base request: %v", err)
	}
	typed, err := parseCapacityActivityRequest(url.Values{"type": {"provision"}}, k8s.ActiveClusterContext())
	if err != nil {
		t.Fatalf("parse typed request: %v", err)
	}
	if typed.typeFilter != capacityapi.ActivityProvision {
		t.Fatalf("typeFilter = %q, want provision", typed.typeFilter)
	}
	if typed.filterFingerprint == base.filterFingerprint {
		t.Fatal("type filter must participate in the cursor filter fingerprint")
	}

	records := []capacitymodel.ActivityRecord{
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityProvision}},
		{Episode: capacityapi.ActivityEpisode{Type: capacityapi.ActivityDisruption}},
	}
	all := filterCapacityActivityRecordsByType(append([]capacitymodel.ActivityRecord{}, records...), "")
	if len(all) != 2 {
		t.Fatalf("unfiltered records = %d, want 2", len(all))
	}
	kept := filterCapacityActivityRecordsByType(records, capacityapi.ActivityDisruption)
	if len(kept) != 1 || kept[0].Episode.Type != capacityapi.ActivityDisruption {
		t.Fatalf("type-filtered records = %#v", kept)
	}
}

func TestCapacityActivityRequestValidatesFiltersAndSince(t *testing.T) {
	for name, query := range map[string]url.Values{
		"repeated filter": {"pool": {"a", "b"}},
		"invalid since":   {"since": {"yesterday"}},
		"repeated since":  {"since": {"2026-07-13T10:00:00Z", "2026-07-13T11:00:00Z"}},
		"workload filter": {"workload": {"api"}},
		"invalid type":    {"type": {"resize"}},
		"repeated type":   {"type": {"provision", "termination"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCapacityActivityRequest(query, k8s.ActiveClusterContext()); err == nil {
				t.Fatal("invalid activity request was accepted")
			}
		})
	}
}
