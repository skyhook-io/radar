package server

import (
	"encoding/base64"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestCapacityPageDefaultsAndValidLimit(t *testing.T) {
	opts := capacityPageOptions{Scope: "pools", DefaultLimit: 25, MaxLimit: 100}

	request, err := parseCapacityPage(url.Values{}, opts)
	if err != nil {
		t.Fatalf("parse default page: %v", err)
	}
	if request.limit != 25 || request.after != "" || request.scope != "pools" {
		t.Fatalf("default request = %#v", request)
	}

	request, err = parseCapacityPage(url.Values{"limit": {"60"}}, opts)
	if err != nil {
		t.Fatalf("parse explicit limit: %v", err)
	}
	if request.limit != 60 {
		t.Fatalf("limit = %d, want 60", request.limit)
	}
}

func TestCapacityPageRejectsInvalidLimits(t *testing.T) {
	opts := capacityPageOptions{Scope: "pools", DefaultLimit: 25, MaxLimit: 100}
	tests := map[string]url.Values{
		"zero":        {"limit": {"0"}},
		"negative":    {"limit": {"-2"}},
		"non integer": {"limit": {"2.5"}},
		"over max":    {"limit": {"101"}},
		"repeated":    {"limit": {"10", "20"}},
	}
	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCapacityPage(query, opts); err == nil {
				t.Fatalf("parseCapacityPage(%v) succeeded", query)
			}
		})
	}
}

func TestCapacityKeysetPaginationIsDeterministic(t *testing.T) {
	opts := capacityPageOptions{
		Scope:        "members/general-purpose/node",
		Filters:      url.Values{"type": {"node"}},
		DefaultLimit: 2,
		MaxLimit:     100,
	}
	firstRequest, err := parseCapacityPage(url.Values{"limit": {"2"}}, opts)
	if err != nil {
		t.Fatalf("parse first page: %v", err)
	}
	items := []string{"delta", "alpha", "charlie", "bravo"}
	first, err := paginateCapacityKeyset(items, firstRequest, func(item string) string { return item })
	if err != nil {
		t.Fatalf("paginate first page: %v", err)
	}
	if !reflect.DeepEqual(first.items, []string{"alpha", "bravo"}) || !first.hasMore || first.nextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}

	secondRequest, err := parseCapacityPage(url.Values{
		"limit":  {"2"},
		"cursor": {first.nextCursor},
	}, opts)
	if err != nil {
		t.Fatalf("parse second page: %v", err)
	}
	second, err := paginateCapacityKeyset(items, secondRequest, func(item string) string { return item })
	if err != nil {
		t.Fatalf("paginate second page: %v", err)
	}
	if !reflect.DeepEqual(second.items, []string{"charlie", "delta"}) || second.hasMore || second.nextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestCapacityCursorNormalizesFilterOrderAndDuplicates(t *testing.T) {
	firstOptions := capacityPageOptions{
		Scope: "demand",
		Filters: url.Values{
			"namespace": {"zeta", "alpha", "alpha"},
			"state":     {"blocked", "awaiting_capacity"},
		},
		DefaultLimit: 25,
		MaxLimit:     100,
	}
	request, err := parseCapacityPage(url.Values{}, firstOptions)
	if err != nil {
		t.Fatalf("parse first request: %v", err)
	}
	cursor, err := encodeCapacityPageCursor(request, "group-002", "snapshot-a")
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}

	secondOptions := capacityPageOptions{
		Scope: "demand",
		Filters: url.Values{
			"state":     {"awaiting_capacity", "blocked"},
			"namespace": {"alpha", "zeta"},
		},
		DefaultLimit: 25,
		MaxLimit:     100,
	}
	continued, err := parseCapacityPage(url.Values{"cursor": {cursor}}, secondOptions)
	if err != nil {
		t.Fatalf("parse normalized continuation: %v", err)
	}
	if continued.after != "group-002" {
		t.Fatalf("after = %q, want group-002", continued.after)
	}
}

func TestCapacityCursorRejectsChangedScopeOrFilters(t *testing.T) {
	request, err := parseCapacityPage(url.Values{}, capacityPageOptions{
		Scope:        "pools",
		Filters:      url.Values{"state": {"ready"}},
		DefaultLimit: 25,
		MaxLimit:     100,
	})
	if err != nil {
		t.Fatalf("parse original request: %v", err)
	}
	cursor, err := encodeCapacityPageCursor(request, "pool-b", "snapshot-a")
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}

	for name, opts := range map[string]capacityPageOptions{
		"scope": {
			Scope: "demand", Filters: url.Values{"state": {"ready"}}, DefaultLimit: 25, MaxLimit: 100,
		},
		"filters": {
			Scope: "pools", Filters: url.Values{"state": {"not-ready"}}, DefaultLimit: 25, MaxLimit: 100,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCapacityPage(url.Values{"cursor": {cursor}}, opts); err == nil || !isCapacityCursorInvalidError(err) {
				t.Fatalf("changed cursor context error = %v, want classified cursor error", err)
			}
		})
	}
}

func TestCapacityCursorRejectsMalformedPayloads(t *testing.T) {
	validFingerprint := capacityFilterFingerprint(nil)
	tests := map[string]string{
		"invalid base64": "%%%",
		"unknown field":  capacityTestCursor(`{"version":2,"scope":"pools","filterFingerprint":"` + validFingerprint + `","after":"a","snapshotFingerprint":"s","extra":true}`),
		"version":        capacityTestCursor(`{"version":1,"scope":"pools","filterFingerprint":"` + validFingerprint + `","after":"a","snapshotFingerprint":"s"}`),
		"trailing json":  capacityTestCursor(`{"version":2,"scope":"pools","filterFingerprint":"` + validFingerprint + `","after":"a","snapshotFingerprint":"s"}{}`),
		"missing key":    capacityTestCursor(`{"version":2,"scope":"pools","filterFingerprint":"` + validFingerprint + `","after":"","snapshotFingerprint":"s"}`),
		"oversized":      strings.Repeat("a", maxCapacityCursorLength+1),
	}
	for name, cursor := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCapacityPageCursor(cursor); err == nil || !isCapacityCursorInvalidError(err) {
				t.Fatalf("malformed cursor error = %v, want classified cursor error", err)
			}
		})
	}

	if _, err := parseCapacityPage(url.Values{"cursor": {"one", "two"}}, capacityPageOptions{
		Scope: "pools", DefaultLimit: 25, MaxLimit: 100,
	}); err == nil || !isCapacityCursorInvalidError(err) {
		t.Fatalf("repeated cursor error = %v, want classified cursor error", err)
	}
}

func TestCapacityKeysetRejectsInvalidKeys(t *testing.T) {
	request := capacityPageRequest{limit: 10, scope: "pools", filterFingerprint: capacityFilterFingerprint(nil)}
	for name, items := range map[string][]string{
		"empty":     {"alpha", ""},
		"duplicate": {"alpha", "alpha"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := paginateCapacityKeyset(items, request, func(item string) string { return item }); err == nil {
				t.Fatal("invalid page item keys were accepted")
			}
		})
	}
}

func TestCapacityKeysetContinuesAfterDeletedBoundaryAndReturnsNonNilEmpty(t *testing.T) {
	request := capacityPageRequest{
		limit:             2,
		after:             "bravo",
		scope:             "pools",
		filterFingerprint: capacityFilterFingerprint(nil),
	}
	page, err := paginateCapacityKeyset([]string{"alpha", "charlie", "delta"}, request, func(item string) string { return item })
	if err != nil {
		t.Fatalf("paginate after deleted boundary: %v", err)
	}
	if !reflect.DeepEqual(page.items, []string{"charlie", "delta"}) {
		t.Fatalf("items = %#v", page.items)
	}

	empty, err := paginateCapacityKeyset([]string{}, request, func(item string) string { return item })
	if err != nil {
		t.Fatalf("paginate empty input: %v", err)
	}
	if empty.items == nil || len(empty.items) != 0 {
		t.Fatalf("empty items = %#v, want non-nil empty slice", empty.items)
	}
}

func TestCapacityCursorRejectsChangedClusterOrSnapshot(t *testing.T) {
	opts := capacityPageOptions{Scope: "pools", ClusterContext: "cluster-a", DefaultLimit: 1, MaxLimit: 100}
	request, err := parseCapacityPage(url.Values{}, opts)
	if err != nil {
		t.Fatalf("parse first page: %v", err)
	}
	first, err := paginateCapacityKeyset([]string{"alpha", "bravo"}, request, func(item string) string { return item })
	if err != nil {
		t.Fatalf("paginate first page: %v", err)
	}
	if _, err := parseCapacityPage(url.Values{"cursor": {first.nextCursor}}, capacityPageOptions{
		Scope: "pools", ClusterContext: "cluster-b", DefaultLimit: 1, MaxLimit: 100,
	}); err == nil || !strings.Contains(err.Error(), "different cluster context") || !isCapacityCursorInvalidError(err) {
		t.Fatalf("changed cluster error = %v", err)
	}

	continued, err := parseCapacityPage(url.Values{"cursor": {first.nextCursor}}, opts)
	if err != nil {
		t.Fatalf("parse continuation: %v", err)
	}
	if _, err := paginateCapacityKeyset([]string{"alpha", "changed"}, continued, func(item string) string { return item }); err == nil || !strings.Contains(err.Error(), "restart pagination") || !isCapacityCursorInvalidError(err) {
		t.Fatalf("changed snapshot error = %v", err)
	}
}

func TestCapacityCursorRejectsChangedProvidedSnapshot(t *testing.T) {
	opts := capacityPageOptions{Scope: "demand", ClusterContext: "cluster-a", DefaultLimit: 1, MaxLimit: 25}
	request, err := parseCapacityPage(url.Values{}, opts)
	if err != nil {
		t.Fatalf("parse first page: %v", err)
	}
	items := []string{"alpha", "bravo"}
	first, err := paginateCapacityKeysetWithSnapshot(items, request, func(item string) string { return item }, "groups-and-pools-a")
	if err != nil {
		t.Fatalf("paginate first page: %v", err)
	}
	continued, err := parseCapacityPage(url.Values{"cursor": {first.nextCursor}}, opts)
	if err != nil {
		t.Fatalf("parse continuation: %v", err)
	}
	if _, err := paginateCapacityKeysetWithSnapshot(items, continued, func(item string) string { return item }, "groups-and-pools-b"); err == nil || !strings.Contains(err.Error(), "restart pagination") || !isCapacityCursorInvalidError(err) {
		t.Fatalf("changed provided snapshot error = %v", err)
	}
}

func TestCapacitySnapshotFingerprintsMembershipOnly(t *testing.T) {
	type observed struct {
		Name  string `json:"name"`
		AsOf  string `json:"asOf"`
		Value string `json:"value"`
	}
	opts := capacityPageOptions{Scope: "observed", ClusterContext: "cluster-a", DefaultLimit: 1, MaxLimit: 100}
	request, err := parseCapacityPage(url.Values{}, opts)
	if err != nil {
		t.Fatalf("parse first request: %v", err)
	}
	first, err := paginateCapacityKeyset([]observed{{Name: "a", AsOf: "one", Value: "v1"}, {Name: "b", AsOf: "one", Value: "v1"}}, request, func(item observed) string { return item.Name })
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	continued, err := parseCapacityPage(url.Values{"cursor": {first.nextCursor}}, opts)
	if err != nil {
		t.Fatalf("parse continuation: %v", err)
	}
	// Live-value churn (usage resamples every ~30s) must NOT invalidate the
	// cursor — keyset pagination on stable keys cannot skip or duplicate.
	if _, err := paginateCapacityKeyset([]observed{{Name: "a", AsOf: "two", Value: "v2"}, {Name: "b", AsOf: "two", Value: "v2"}}, continued, func(item observed) string { return item.Name }); err != nil {
		t.Fatalf("value churn invalidated snapshot: %v", err)
	}
	// Membership change does invalidate.
	if _, err := paginateCapacityKeyset([]observed{{Name: "a", AsOf: "two", Value: "v2"}, {Name: "c", AsOf: "two", Value: "v2"}}, continued, func(item observed) string { return item.Name }); err == nil || !isCapacityCursorInvalidError(err) {
		t.Fatalf("membership change error = %v, want cursor invalid", err)
	}
}

func capacityTestCursor(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}
