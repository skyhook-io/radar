package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	capacityCursorVersion          = 2
	capacityCursorInvalidErrorCode = "capacity_cursor_invalid"
	maxCapacityCursorLength        = 4096
)

type capacityCursorInvalidError struct {
	message string
}

func (e *capacityCursorInvalidError) Error() string {
	return e.message
}

func newCapacityCursorInvalidError(message string) error {
	return &capacityCursorInvalidError{message: message}
}

func isCapacityCursorInvalidError(err error) bool {
	var cursorErr *capacityCursorInvalidError
	return errors.As(err, &cursorErr)
}

func (s *Server) writeCapacityPageError(w http.ResponseWriter, err error) {
	if isCapacityCursorInvalidError(err) {
		s.writeErrorCode(w, http.StatusBadRequest, capacityCursorInvalidErrorCode, err.Error())
		return
	}
	s.writeError(w, http.StatusBadRequest, err.Error())
}

type capacityPageOptions struct {
	Scope          string
	Filters        url.Values
	ClusterContext string
	DefaultLimit   int
	MaxLimit       int
}

type capacityPageRequest struct {
	limit               int
	after               string
	scope               string
	filterFingerprint   string
	clusterContext      string
	snapshotFingerprint string
}

type capacityPageResult[T any] struct {
	items      []T
	hasMore    bool
	nextCursor string
}

type capacityKeyedItem[T any] struct {
	key  string
	item T
}

type capacityPageCursor struct {
	Version             int    `json:"version"`
	Scope               string `json:"scope"`
	FilterFingerprint   string `json:"filterFingerprint"`
	After               string `json:"after"`
	ClusterContext      string `json:"clusterContext"`
	SnapshotFingerprint string `json:"snapshotFingerprint"`
}

func parseCapacityPage(query url.Values, opts capacityPageOptions) (capacityPageRequest, error) {
	if opts.Scope == "" {
		return capacityPageRequest{}, fmt.Errorf("capacity page scope is required")
	}
	if opts.DefaultLimit <= 0 || opts.MaxLimit < opts.DefaultLimit {
		return capacityPageRequest{}, fmt.Errorf("invalid capacity page limit configuration")
	}

	limit, err := parseCapacityLimit(query, opts.DefaultLimit, opts.MaxLimit)
	if err != nil {
		return capacityPageRequest{}, err
	}

	clusterContext := opts.ClusterContext
	if clusterContext == "" {
		clusterContext = k8s.ActiveClusterContext()
	}
	request := capacityPageRequest{
		limit:             limit,
		scope:             opts.Scope,
		filterFingerprint: capacityFilterFingerprint(opts.Filters),
		clusterContext:    clusterContext,
	}
	cursors := query["cursor"]
	if len(cursors) > 1 {
		return capacityPageRequest{}, newCapacityCursorInvalidError("cursor must be specified at most once")
	}
	if len(cursors) == 0 || cursors[0] == "" {
		return request, nil
	}

	cursor, err := decodeCapacityPageCursor(cursors[0])
	if err != nil {
		return capacityPageRequest{}, err
	}
	if cursor.Scope != request.scope {
		return capacityPageRequest{}, newCapacityCursorInvalidError("cursor does not belong to this capacity endpoint")
	}
	if cursor.FilterFingerprint != request.filterFingerprint {
		return capacityPageRequest{}, newCapacityCursorInvalidError("cursor does not match the current filters")
	}
	if cursor.ClusterContext != request.clusterContext {
		return capacityPageRequest{}, newCapacityCursorInvalidError("cursor belongs to a different cluster context; restart pagination")
	}
	request.after = cursor.After
	request.snapshotFingerprint = cursor.SnapshotFingerprint
	return request, nil
}

func parseCapacityLimit(query url.Values, defaultLimit, maxLimit int) (int, error) {
	values := query["limit"]
	if len(values) > 1 {
		return 0, fmt.Errorf("limit must be specified at most once")
	}
	if len(values) == 0 || values[0] == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit %q (expected a positive integer)", values[0])
	}
	if limit > maxLimit {
		return 0, fmt.Errorf("limit %d exceeds maximum %d", limit, maxLimit)
	}
	return limit, nil
}

func capacityFilterFingerprint(filters url.Values) string {
	keys := make([]string, 0, len(filters))
	for key := range filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	canonical := url.Values{}
	for _, key := range keys {
		values := append([]string{}, filters[key]...)
		sort.Strings(values)
		values = compactSortedStrings(values)
		canonical[key] = values
	}
	sum := sha256.Sum256([]byte(canonical.Encode()))
	return hex.EncodeToString(sum[:])
}

func compactSortedStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func encodeCapacityPageCursor(request capacityPageRequest, after, snapshotFingerprint string) (string, error) {
	if after == "" {
		return "", fmt.Errorf("capacity cursor key is required")
	}
	payload, err := json.Marshal(capacityPageCursor{
		Version:             capacityCursorVersion,
		Scope:               request.scope,
		FilterFingerprint:   request.filterFingerprint,
		After:               after,
		ClusterContext:      request.clusterContext,
		SnapshotFingerprint: snapshotFingerprint,
	})
	if err != nil {
		return "", fmt.Errorf("encode capacity cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCapacityPageCursor(encoded string) (capacityPageCursor, error) {
	if len(encoded) > maxCapacityCursorLength {
		return capacityPageCursor{}, newCapacityCursorInvalidError("invalid cursor: exceeds maximum length")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return capacityPageCursor{}, newCapacityCursorInvalidError("invalid cursor encoding")
	}
	if len(payload) > maxCapacityCursorLength {
		return capacityPageCursor{}, newCapacityCursorInvalidError("invalid cursor: exceeds maximum length")
	}

	var cursor capacityPageCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return capacityPageCursor{}, newCapacityCursorInvalidError("invalid cursor payload")
	}
	if err := ensureCapacityJSONEOF(decoder); err != nil {
		return capacityPageCursor{}, newCapacityCursorInvalidError("invalid cursor payload")
	}
	if cursor.Version != capacityCursorVersion {
		return capacityPageCursor{}, newCapacityCursorInvalidError(fmt.Sprintf("unsupported cursor version %d", cursor.Version))
	}
	if cursor.Scope == "" || cursor.FilterFingerprint == "" || cursor.After == "" || cursor.SnapshotFingerprint == "" {
		return capacityPageCursor{}, newCapacityCursorInvalidError("invalid cursor payload")
	}
	return cursor, nil
}

func ensureCapacityJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON")
		}
		return err
	}
	return nil
}

func paginateCapacityKeyset[T any](items []T, request capacityPageRequest, keyFor func(T) string) (capacityPageResult[T], error) {
	keyed, err := capacityKeyedItems(items, keyFor)
	if err != nil {
		return capacityPageResult[T]{}, err
	}
	snapshotFingerprint, err := capacitySnapshotFingerprint(keyed)
	if err != nil {
		return capacityPageResult[T]{}, err
	}
	return paginateCapacityKeyed(keyed, request, snapshotFingerprint)
}

func paginateCapacityKeysetWithSnapshot[T any](items []T, request capacityPageRequest, keyFor func(T) string, snapshotFingerprint string) (capacityPageResult[T], error) {
	if snapshotFingerprint == "" {
		return capacityPageResult[T]{}, fmt.Errorf("capacity snapshot fingerprint is required")
	}
	keyed, err := capacityKeyedItems(items, keyFor)
	if err != nil {
		return capacityPageResult[T]{}, err
	}
	return paginateCapacityKeyed(keyed, request, snapshotFingerprint)
}

func capacityKeyedItems[T any](items []T, keyFor func(T) string) ([]capacityKeyedItem[T], error) {
	keyed := make([]capacityKeyedItem[T], 0, len(items))
	for _, item := range items {
		key := keyFor(item)
		if key == "" {
			return nil, fmt.Errorf("capacity page item has an empty key")
		}
		keyed = append(keyed, capacityKeyedItem[T]{key: key, item: item})
	}
	sort.Slice(keyed, func(i, j int) bool { return keyed[i].key < keyed[j].key })
	for i := 1; i < len(keyed); i++ {
		if keyed[i-1].key == keyed[i].key {
			return nil, fmt.Errorf("capacity page item key %q is not unique", keyed[i].key)
		}
	}
	return keyed, nil
}

func paginateCapacityKeyed[T any](keyed []capacityKeyedItem[T], request capacityPageRequest, snapshotFingerprint string) (capacityPageResult[T], error) {
	if request.snapshotFingerprint != "" && request.snapshotFingerprint != snapshotFingerprint {
		return capacityPageResult[T]{}, newCapacityCursorInvalidError("capacity data changed while paginating; restart pagination")
	}

	start := 0
	if request.after != "" {
		start = sort.Search(len(keyed), func(i int) bool { return keyed[i].key > request.after })
	}
	end := start + request.limit
	if end > len(keyed) {
		end = len(keyed)
	}

	pageItems := make([]T, 0, end-start)
	for _, item := range keyed[start:end] {
		pageItems = append(pageItems, item.item)
	}
	result := capacityPageResult[T]{items: pageItems, hasMore: end < len(keyed)}
	if result.hasMore && len(pageItems) > 0 {
		nextCursor, err := encodeCapacityPageCursor(request, keyed[end-1].key, snapshotFingerprint)
		if err != nil {
			return capacityPageResult[T]{}, err
		}
		result.nextCursor = nextCursor
	}
	return result, nil
}

// capacitySnapshotFingerprint hashes MEMBERSHIP ONLY (the sorted item keys),
// mirroring the demand endpoint. Keyset pagination on stable unique keys
// cannot skip or duplicate items when values churn, so hashing item payloads
// would only make cursors self-invalidate every metrics resample (~30s) and
// bounce users off page ≥2 on any live cluster.
func capacitySnapshotFingerprint[T any](items []capacityKeyedItem[T]) (string, error) {
	hash := sha256.New()
	for _, keyed := range items {
		hash.Write([]byte(keyed.key))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
