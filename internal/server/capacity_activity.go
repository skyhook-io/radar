package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	internaltimeline "github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/timeline"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	capacityActivityCursorVersion = 2
	capacityActivityQueryLimit    = 10000
)

type capacityActivityCursor struct {
	Version           int    `json:"version"`
	Epoch             string `json:"epoch"`
	FilterFingerprint string `json:"filterFingerprint"`
	Seq               int64  `json:"seq"`
	Direction         string `json:"direction"`
	ClusterContext    string `json:"clusterContext"`
}

type capacityActivityRequest struct {
	limit             int
	cursor            *capacityActivityCursor
	filterFingerprint string
	filters           url.Values
	typeFilter        capacityapi.ActivityType
	since             time.Time
	sinceProvided     bool
}

func (s *Server) handleCapacityActivity(w http.ResponseWriter, r *http.Request) {
	// Literally the first statement — ahead of the connection gate, cursor
	// parsing, and the node-visibility check. Everything the handler does is
	// downstream of this snapshot, so nothing can bind a cursor or an
	// authorization to a cluster the response never describes. Activity builds
	// its own result rather than using the shared loader, but it takes the
	// same snapshot and the same gate.
	identity := currentCapacityClusterIdentity()
	if !s.requireConnected(w) {
		return
	}
	result := capacityLoadResult{identity: identity}
	request, err := parseCapacityActivityRequest(r.URL.Query(), identity.activeClusterContext)
	if err != nil {
		s.writeCapacityPageError(w, err)
		return
	}
	if !s.requireNodeVisibility(w, r) {
		return
	}
	now := time.Now().UTC()
	response := capacityapi.NewActivityResponse(now)
	response.ResponseMeta = newCapacityResponseMeta(now, identity)
	response.Observation.StartedAt = now
	capability := s.karpenterCapability(r)
	response.State = capability.State
	if capability.State == capacityapi.IntegrationDenied {
		s.writeError(w, http.StatusForbidden, capacityNodePoolDenialMessage(capability.ReasonCode))
		return
	}
	if capability.State == capacityapi.IntegrationNotDetected || capability.State == capacityapi.IntegrationSyncing {
		response.ResponseMeta.Coverage[capacityapi.CoverageNodePools] = sourceCoverageForState(capability.State, capability.ReasonCode)
		s.writeCapacityResponse(w, result, response)
		return
	}
	response.State = capacityapi.IntegrationAvailable
	nodePoolCoverage := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
	nodePoolCoverage.ImpactFields = []string{"activity"}
	response.ResponseMeta.Coverage[capacityapi.CoverageNodePools] = nodePoolCoverage

	store := identity.timeline
	processObservationStart := internaltimeline.ObservationStart()
	response.Observation.EndedAt = now
	response.Observation.Retention.Mode = "memory_bounded"
	if store == nil || processObservationStart.IsZero() {
		response.ResponseMeta.Coverage[capacityapi.CoverageTimeline] = unavailableCoverage("timeline_unavailable", []string{"activity"})
		s.writeCapacityResponse(w, result, response)
		return
	}
	stats := store.Stats()
	clusterContext := identity.activeClusterContext
	visibility := s.newCapacityActivityVisibility(r)
	if stats.MaxEvents > 0 {
		maxEvents := stats.MaxEvents
		response.Observation.Retention.MaxEvents = &maxEvents
	}
	if stats.StorageBytes > 0 || stats.MaxStorageBytes > 0 || stats.RetentionAge > 0 {
		response.Observation.Retention.Mode = "persistent_unbounded"
	}
	if stats.MaxStorageBytes > 0 || stats.RetentionAge > 0 {
		response.Observation.Retention.Mode = "persistent_bounded"
		if stats.RetentionAge > 0 {
			seconds := int64(stats.RetentionAge.Seconds())
			response.Observation.Retention.MaxAgeSeconds = &seconds
		}
	}
	storeBounds, err := queryCapacityActivityStoreBounds(r.Context(), store, clusterContext, visibility.allows)
	if err != nil {
		response.ResponseMeta.Coverage[capacityapi.CoverageTimeline] = errorCoverage("timeline_query_failed", []string{"activity"})
		s.writeCapacityResponse(w, result, response)
		return
	}
	activityQuery, err := queryCapacityActivityWindow(r.Context(), store, request, clusterContext, visibility.allows)
	if err != nil {
		response.ResponseMeta.Coverage[capacityapi.CoverageTimeline] = errorCoverage("timeline_query_failed", []string{"activity"})
		s.writeCapacityResponse(w, result, response)
		return
	}
	events := activityQuery.events
	_, queryMinSeq := activitySequenceBounds(events)
	retainedVisibleStart := storeBounds.oldestEvent
	if stats.MaxEvents > 0 && stats.EventsEvicted {
		retainedVisibleStart, err = queryOldestVisibleCapacityActivityTimestamp(r.Context(), store, clusterContext, visibility.allows)
		if err != nil {
			response.ResponseMeta.Coverage[capacityapi.CoverageTimeline] = errorCoverage("timeline_query_failed", []string{"activity"})
			s.writeCapacityResponse(w, result, response)
			return
		}
	}
	observationStart := capacityActivityObservationStart(now, processObservationStart, stats, retainedVisibleStart, events)
	response.Observation.StartedAt = observationStart
	epoch := strconv.FormatInt(processObservationStart.UnixNano(), 10)

	if request.cursor != nil && request.cursor.Epoch != epoch {
		gap := capacityActivityGap(now, "timeline_epoch_changed", request.cursor, epoch)
		response.CursorStatus = capacityapi.CursorEpochChanged
		response.CursorGap = &gap
		response.Observation.Gaps = append(response.Observation.Gaps, gap)
		s.writeCapacityResponse(w, result, response)
		return
	}
	// An older-direction cursor that points at (or below) the oldest retained
	// record after any eviction has lost its history — an empty page here
	// would silently read as "end of history" instead of a gap. oldestSeq==0
	// (every visible record evicted) is the same gap, not a clean end.
	if request.cursor != nil && stats.EventsEvicted && request.cursor.Seq > 1 && (storeBounds.oldestSeq == 0 || request.cursor.Seq <= storeBounds.oldestSeq) {
		gap := capacityActivityGap(now, "timeline_cursor_evicted", request.cursor, epoch)
		response.CursorStatus = capacityapi.CursorEvicted
		response.CursorGap = &gap
		response.Observation.Gaps = append(response.Observation.Gaps, gap)
		s.writeCapacityResponse(w, result, response)
		return
	}

	eventCoverage, sources := visibility.describe(events, now)
	response.ResponseMeta.Coverage[capacityapi.CoverageKarpenterObjectEvents] = eventCoverage
	if visibility.claimAllowed {
		claimCoverage := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
		claimCoverage.ImpactFields = []string{"activity.claim"}
		response.ResponseMeta.Coverage[capacityapi.CoverageNodeClaims] = claimCoverage
	} else {
		response.ResponseMeta.Coverage[capacityapi.CoverageNodeClaims] = deniedCoverage("nodeclaims_list_denied", []string{"activity.claim"})
	}
	if visibility.nodeAllowed {
		nodeCoverage := capacityapi.NewSourceCoverage(capacityapi.CoverageAvailable, capacityapi.CoverageScopeCluster)
		nodeCoverage.ImpactFields = []string{"activity.node"}
		response.ResponseMeta.Coverage[capacityapi.CoverageNodes] = nodeCoverage
	} else {
		response.ResponseMeta.Coverage[capacityapi.CoverageNodes] = deniedCoverage("nodes_list_denied", []string{"activity.node"})
	}
	response.Observation.Sources = sources
	timelineCoverage := availableClusterCoverage(now, len(events), []string{"activity"})
	if activityQuery.bounded {
		timelineCoverage.Status = capacityapi.CoveragePartial
		timelineCoverage.ReasonCode = "timeline_query_bounded"
	}
	timelineCoverage.ObservationStart = &observationStart
	response.ResponseMeta.Coverage[capacityapi.CoverageTimeline] = timelineCoverage

	records := capacitymodel.BuildActivityRecords(events)
	records = filterCapacityActivityRecords(records, request.filters)
	// The aggregate summarizes the whole filtered window, so it is computed
	// before the type filter (type pills stay stable while the list narrows,
	// mirroring the demand-state rollup) and only on first-page requests —
	// cursor pages see a window bounded near the cursor, and an aggregate
	// over that slice would misread as the whole window.
	if request.cursor == nil {
		aggregate := capacitymodel.AggregateActivityRecords(records)
		response.Aggregate = &aggregate
	}
	records = filterCapacityActivityRecordsByType(records, request.typeFilter)
	if request.cursor == nil {
		reverseActivityRecords(records)
	} else {
		cursorSeq := request.cursor.Seq
		kept := records[:0]
		for _, record := range records {
			if record.MaxSeq < cursorSeq {
				kept = append(kept, record)
			}
		}
		records = kept
		reverseActivityRecords(records)
	}

	recordsTruncated := len(records) > request.limit
	if recordsTruncated {
		records = records[:request.limit]
	}
	response.Items = make([]capacityapi.ActivityEpisode, 0, len(records))
	lastSeq := int64(0)
	oldestSeq := int64(0)
	for _, record := range records {
		response.Items = append(response.Items, record.Episode)
		if record.MaxSeq > lastSeq {
			lastSeq = record.MaxSeq
		}
		if oldestSeq == 0 || record.MaxSeq < oldestSeq {
			oldestSeq = record.MaxSeq
		}
	}
	queryTruncated := activityQuery.bounded
	response.Page.HasMore = recordsTruncated || queryTruncated
	switch {
	case request.cursor == nil && recordsTruncated && oldestSeq > 0:
		response.Page.NextCursor, _ = encodeCapacityActivityCursor(epoch, request.filterFingerprint, clusterContext, oldestSeq, "older")
	case request.cursor == nil && queryTruncated && queryMinSeq > 0:
		response.Page.NextCursor, _ = encodeCapacityActivityCursor(epoch, request.filterFingerprint, clusterContext, queryMinSeq, "older")
	case request.cursor != nil && request.cursor.Direction == "older" && recordsTruncated && oldestSeq > 0:
		response.Page.NextCursor, _ = encodeCapacityActivityCursor(epoch, request.filterFingerprint, clusterContext, oldestSeq, "older")
	case request.cursor != nil && request.cursor.Direction == "older" && queryTruncated && queryMinSeq > 0:
		response.Page.NextCursor, _ = encodeCapacityActivityCursor(epoch, request.filterFingerprint, clusterContext, queryMinSeq, "older")
	}
	s.writeCapacityResponse(w, result, response)
}

type capacityActivityWindow struct {
	events  []timeline.TimelineEvent
	bounded bool
}

type capacityActivityStoreBounds struct {
	oldestSeq   int64
	newestSeq   int64
	oldestEvent time.Time
}

func queryCapacityActivityStoreBounds(ctx context.Context, store timeline.EventStore, clusterContext string, allows func(timeline.TimelineEvent) bool) (capacityActivityStoreBounds, error) {
	oldestQuery := newCapacityActivityStoreQuery(clusterContext, timeline.SequenceOrderAscending)
	oldest, err := queryFirstVisibleCapacityActivityEvent(ctx, store, oldestQuery, allows)
	if err != nil {
		return capacityActivityStoreBounds{}, err
	}
	newestQuery := newCapacityActivityStoreQuery(clusterContext, timeline.SequenceOrderDescending)
	newest, err := queryFirstVisibleCapacityActivityEvent(ctx, store, newestQuery, allows)
	if err != nil {
		return capacityActivityStoreBounds{}, err
	}

	bounds := capacityActivityStoreBounds{}
	if oldest != nil {
		bounds.oldestSeq = oldest.Seq
		bounds.oldestEvent = oldest.Timestamp
	}
	if newest != nil {
		bounds.newestSeq = newest.Seq
	}
	return bounds, nil
}

func capacityActivityObservationStart(now, processStart time.Time, stats timeline.StoreStats, oldestEvent time.Time, events []timeline.TimelineEvent) time.Time {
	if stats.MaxEvents > 0 && !stats.EventsEvicted && !processStart.IsZero() {
		return processStart
	}
	startedAt := now
	if !oldestEvent.IsZero() && oldestEvent.Before(startedAt) {
		startedAt = oldestEvent
	}
	for _, event := range events {
		if !event.Timestamp.IsZero() && event.Timestamp.Before(startedAt) {
			startedAt = event.Timestamp
		}
	}
	if stats.MaxEvents > 0 && !processStart.IsZero() && startedAt.Before(processStart) {
		return processStart
	}
	return startedAt
}

func queryOldestVisibleCapacityActivityTimestamp(ctx context.Context, store timeline.EventStore, clusterContext string, allows func(timeline.TimelineEvent) bool) (time.Time, error) {
	query := newCapacityActivityStoreQuery(clusterContext, timeline.SequenceOrderAscending)
	oldest := time.Time{}
	for {
		batch, err := store.Query(ctx, query)
		if err != nil {
			return time.Time{}, err
		}
		for _, event := range batch {
			if allows(event) && !event.Timestamp.IsZero() && (oldest.IsZero() || event.Timestamp.Before(oldest)) {
				oldest = event.Timestamp
			}
		}
		if len(batch) < capacityActivityQueryLimit || !advanceCapacityActivityStoreQuery(&query, batch) {
			return oldest, nil
		}
	}
}

func queryCapacityActivityWindow(ctx context.Context, store timeline.EventStore, request capacityActivityRequest, clusterContext string, allows func(timeline.TimelineEvent) bool) (capacityActivityWindow, error) {
	query := newCapacityActivityStoreQuery(clusterContext, timeline.SequenceOrderDescending)
	if request.sinceProvided {
		query.Since = request.since
	}
	if request.cursor == nil {
		return queryCapacityActivityVisiblePage(ctx, store, query, allows)
	}

	window := capacityActivityWindow{}
	for {
		batch, err := store.Query(ctx, query)
		if err != nil {
			return capacityActivityWindow{}, err
		}
		crossedCursor := false
		for _, event := range batch {
			if !allows(event) {
				continue
			}
			window.events = append(window.events, event)
			crossedCursor = crossedCursor || event.Seq < request.cursor.Seq
		}
		if len(batch) < capacityActivityQueryLimit {
			return window, nil
		}
		if !advanceCapacityActivityStoreQuery(&query, batch) {
			return window, nil
		}
		if crossedCursor {
			more, err := queryFirstVisibleCapacityActivityEvent(ctx, store, query, allows)
			window.bounded = more != nil
			return window, err
		}
	}
}

func newCapacityActivityStoreQuery(clusterContext string, order timeline.SequenceOrder) timeline.QueryOptions {
	return timeline.QueryOptions{
		Limit:            capacityActivityQueryLimit,
		Kinds:            []string{karpenter.NodePoolKind, karpenter.NodeClaimKind, "Node"},
		IncludeManaged:   true,
		IncludeK8sEvents: true,
		ClusterContext:   clusterContext,
		SequenceOrder:    order,
	}
}

func queryCapacityActivityVisiblePage(ctx context.Context, store timeline.EventStore, query timeline.QueryOptions, allows func(timeline.TimelineEvent) bool) (capacityActivityWindow, error) {
	window := capacityActivityWindow{events: make([]timeline.TimelineEvent, 0, capacityActivityQueryLimit)}
	for {
		batch, err := store.Query(ctx, query)
		if err != nil {
			return capacityActivityWindow{}, err
		}
		for _, event := range batch {
			if !allows(event) {
				continue
			}
			if len(window.events) == capacityActivityQueryLimit {
				window.bounded = true
				return window, nil
			}
			window.events = append(window.events, event)
		}
		if len(batch) < capacityActivityQueryLimit || !advanceCapacityActivityStoreQuery(&query, batch) {
			return window, nil
		}
	}
}

func queryFirstVisibleCapacityActivityEvent(ctx context.Context, store timeline.EventStore, query timeline.QueryOptions, allows func(timeline.TimelineEvent) bool) (*timeline.TimelineEvent, error) {
	for {
		batch, err := store.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		for index := range batch {
			if allows(batch[index]) {
				event := batch[index]
				return &event, nil
			}
		}
		if len(batch) < capacityActivityQueryLimit || !advanceCapacityActivityStoreQuery(&query, batch) {
			return nil, nil
		}
	}
}

func advanceCapacityActivityStoreQuery(query *timeline.QueryOptions, batch []timeline.TimelineEvent) bool {
	maxSeq, minSeq := activitySequenceBounds(batch)
	if query.SequenceOrder == timeline.SequenceOrderAscending {
		if maxSeq == 0 || maxSeq <= query.SinceSeq {
			return false
		}
		query.SinceSeq = maxSeq
		return true
	}
	if minSeq == 0 || (query.UntilSeq > 0 && minSeq >= query.UntilSeq) {
		return false
	}
	query.UntilSeq = minSeq
	return true
}

// clusterContext is the caller's captured cluster stamp; the cursor's cluster
// binding is checked against it rather than a fresh read, so a switch between
// the snapshot and this parse cannot admit another cluster's cursor.
func parseCapacityActivityRequest(query url.Values, clusterContext string) (capacityActivityRequest, error) {
	limit, err := parseCapacityLimit(query, capacityDefaultPageLimit, capacityMaxPageLimit)
	if err != nil {
		return capacityActivityRequest{}, err
	}
	if _, found := query["workload"]; found {
		return capacityActivityRequest{}, fmt.Errorf("workload is not a supported activity filter")
	}
	filters := url.Values{}
	for _, key := range []string{"pool", "claim", "node"} {
		values := query[key]
		if len(values) > 1 {
			return capacityActivityRequest{}, fmt.Errorf("%s must be specified at most once", key)
		}
		if len(values) == 1 && strings.TrimSpace(values[0]) != "" {
			filters[key] = []string{strings.TrimSpace(values[0])}
		}
	}
	request := capacityActivityRequest{limit: limit, filters: filters}
	if values := query["since"]; len(values) > 1 {
		return capacityActivityRequest{}, fmt.Errorf("since must be specified at most once")
	} else if len(values) == 1 && values[0] != "" {
		request.since, err = time.Parse(time.RFC3339, values[0])
		if err != nil {
			return capacityActivityRequest{}, fmt.Errorf("invalid since %q (expected RFC3339)", values[0])
		}
		request.sinceProvided = true
		filters.Set("since", request.since.UTC().Format(time.RFC3339Nano))
	}
	if values := query["type"]; len(values) > 1 {
		return capacityActivityRequest{}, fmt.Errorf("type must be specified at most once")
	} else if len(values) == 1 && strings.TrimSpace(values[0]) != "" {
		activityType := capacityapi.ActivityType(strings.TrimSpace(values[0]))
		valid := map[capacityapi.ActivityType]bool{
			capacityapi.ActivityProvision:             true,
			capacityapi.ActivityLaunchFailure:         true,
			capacityapi.ActivityRegistrationFailure:   true,
			capacityapi.ActivityInitializationFailure: true,
			capacityapi.ActivityDisruption:            true,
			capacityapi.ActivityInterruption:          true,
			capacityapi.ActivityTermination:           true,
			capacityapi.ActivityConfigChange:          true,
		}
		if !valid[activityType] {
			return capacityActivityRequest{}, fmt.Errorf("invalid activity type %q", activityType)
		}
		request.typeFilter = activityType
		filters.Set("type", string(activityType))
	}
	request.filterFingerprint = capacityFilterFingerprint(filters)
	if values := query["cursor"]; len(values) > 1 {
		return capacityActivityRequest{}, newCapacityCursorInvalidError("cursor must be specified at most once")
	} else if len(values) == 1 && values[0] != "" {
		cursor, err := decodeCapacityActivityCursor(values[0])
		if err != nil {
			return capacityActivityRequest{}, err
		}
		if cursor.FilterFingerprint != request.filterFingerprint {
			return capacityActivityRequest{}, newCapacityCursorInvalidError("cursor does not match the current filters")
		}
		if cursor.ClusterContext != clusterContext {
			return capacityActivityRequest{}, newCapacityCursorInvalidError("cursor belongs to a different cluster context")
		}
		request.cursor = &cursor
	}
	return request, nil
}

func encodeCapacityActivityCursor(epoch, filterFingerprint, clusterContext string, seq int64, direction string) (string, error) {
	if epoch == "" || filterFingerprint == "" || seq < 0 || direction != "older" {
		return "", fmt.Errorf("invalid activity cursor fields")
	}
	payload, err := json.Marshal(capacityActivityCursor{Version: capacityActivityCursorVersion, Epoch: epoch, FilterFingerprint: filterFingerprint, Seq: seq, Direction: direction, ClusterContext: clusterContext})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCapacityActivityCursor(encoded string) (capacityActivityCursor, error) {
	if len(encoded) > maxCapacityCursorLength {
		return capacityActivityCursor{}, newCapacityCursorInvalidError("invalid cursor: exceeds maximum length")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) > maxCapacityCursorLength {
		return capacityActivityCursor{}, newCapacityCursorInvalidError("invalid cursor encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor capacityActivityCursor
	if err := decoder.Decode(&cursor); err != nil || ensureCapacityJSONEOF(decoder) != nil {
		return capacityActivityCursor{}, newCapacityCursorInvalidError("invalid cursor payload")
	}
	if cursor.Version != capacityActivityCursorVersion || cursor.Epoch == "" || cursor.FilterFingerprint == "" || cursor.Seq < 0 || cursor.Direction != "older" {
		return capacityActivityCursor{}, newCapacityCursorInvalidError("invalid cursor payload")
	}
	return cursor, nil
}

type capacityActivityVisibility struct {
	server               *Server
	request              *http.Request
	claimAllowed         bool
	nodeAllowed          bool
	clusterEventsAllowed bool
}

func (s *Server) newCapacityActivityVisibility(r *http.Request) capacityActivityVisibility {
	return capacityActivityVisibility{
		server:               s,
		request:              r,
		claimAllowed:         s.canRead(r, karpenter.Group, "nodeclaims", "", "list"),
		nodeAllowed:          s.canRead(r, "", "nodes", "", "list"),
		clusterEventsAllowed: s.canRead(r, "", "events", "", "list"),
	}
}

func (v capacityActivityVisibility) allows(event timeline.TimelineEvent) bool {
	if strings.TrimSpace(event.Name) == "" {
		return false
	}
	group := schema.FromAPIVersionAndKind(event.APIVersion, event.Kind).Group
	switch {
	case event.Kind == karpenter.NodePoolKind && group == karpenter.Group:
	case event.Kind == karpenter.NodeClaimKind && group == karpenter.Group:
		if !v.claimAllowed {
			return false
		}
	case event.Kind == "Node" && event.Labels[karpenter.NodePoolLabelKey] != "":
		if !v.nodeAllowed {
			return false
		}
	default:
		return false
	}
	if event.Source == timeline.SourceK8sEvent && !v.clusterEventsAllowed && !v.server.canRead(v.request, "", "events", event.Namespace, "list") {
		return false
	}
	return true
}

func (v capacityActivityVisibility) describe(events []timeline.TimelineEvent, now time.Time) (capacityapi.SourceCoverage, []capacityapi.EvidenceSource) {
	sourceSet := map[capacityapi.EvidenceSource]bool{}
	includedK8sEvents := 0
	for _, event := range events {
		if event.Source == timeline.SourceK8sEvent {
			includedK8sEvents++
		}
		sourceSet[capacityActivityEvidenceSource(event.Source)] = true
	}
	coverage := availableClusterCoverage(now, includedK8sEvents, []string{"activity.evidence"})
	if !v.clusterEventsAllowed {
		coverage.Status = capacityapi.CoveragePartial
		coverage.Scope = capacityapi.CoverageScopeAllAuthorizedNamespaces
		coverage.ReasonCode = "event_namespace_visibility_partial"
	}
	sources := make([]capacityapi.EvidenceSource, 0, len(sourceSet))
	for _, source := range []capacityapi.EvidenceSource{capacityapi.EvidenceCondition, capacityapi.EvidenceKubernetesEvent, capacityapi.EvidenceResourceChange} {
		if sourceSet[source] {
			sources = append(sources, source)
		}
	}
	return coverage, sources
}

func filterCapacityActivityRecords(records []capacitymodel.ActivityRecord, filters url.Values) []capacitymodel.ActivityRecord {
	result := records[:0]
	for _, record := range records {
		episode := record.Episode
		if value := filters.Get("pool"); value != "" && (episode.Pool == nil || episode.Pool.Ref.Name != value) {
			continue
		}
		if value := filters.Get("claim"); value != "" && (episode.Claim == nil || episode.Claim.Ref.Name != value) {
			continue
		}
		if value := filters.Get("node"); value != "" && (episode.Node == nil || episode.Node.Ref.Name != value) {
			continue
		}
		result = append(result, record)
	}
	return result
}

func filterCapacityActivityRecordsByType(records []capacitymodel.ActivityRecord, activityType capacityapi.ActivityType) []capacitymodel.ActivityRecord {
	if activityType == "" {
		return records
	}
	kept := records[:0]
	for _, record := range records {
		if record.Episode.Type == activityType {
			kept = append(kept, record)
		}
	}
	return kept
}

func activitySequenceBounds(events []timeline.TimelineEvent) (maxSeq, minSeq int64) {
	for _, event := range events {
		if event.Seq > maxSeq {
			maxSeq = event.Seq
		}
		if event.Seq > 0 && (minSeq == 0 || event.Seq < minSeq) {
			minSeq = event.Seq
		}
	}
	return maxSeq, minSeq
}

func capacityActivityGap(now time.Time, reason string, cursor *capacityActivityCursor, epoch string) capacityapi.ObservationGap {
	previous := ""
	if cursor != nil {
		previous, _ = encodeCapacityActivityCursor(cursor.Epoch, cursor.FilterFingerprint, cursor.ClusterContext, cursor.Seq, cursor.Direction)
	}
	return capacityapi.ObservationGap{DetectedAt: now, Reason: reason, PreviousCursor: previous, NewEpoch: epoch}
}

func reverseActivityRecords(records []capacitymodel.ActivityRecord) {
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
}

func capacityActivityEvidenceSource(source timeline.EventSource) capacityapi.EvidenceSource {
	switch source {
	case timeline.SourceK8sEvent:
		return capacityapi.EvidenceKubernetesEvent
	case timeline.SourceHistorical:
		return capacityapi.EvidenceCondition
	default:
		return capacityapi.EvidenceResourceChange
	}
}
