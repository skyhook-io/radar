package timeline

import (
	"context"
	"maps"
	"strconv"
	"sync"
	"time"
)

// EventMetrics tracks event pipeline metrics for debugging
type EventMetrics struct {
	mu sync.RWMutex

	// Ingestion counters
	EventsReceived map[string]int64 // by kind
	EventsDropped  map[string]int64 // by reason
	EventsRecorded map[string]int64 // by kind

	// Recent drops for debugging
	RecentDrops    []DropRecord
	maxRecentDrops int

	// Query counters
	EventsQueried  int64
	EventsFiltered int64

	// Start time for uptime
	startTime time.Time
}

// DropRecord records a single dropped event for debugging
type DropRecord struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	Operation string `json:"operation"`
	// ClusterContext is the kubeconfig context the dropped event belonged to,
	// captured at informer wiring time (NOT read live at drop time). Informer
	// shutdown on a context switch is asynchronous, so a straggler callback can
	// record a drop after the switch — stamping the wiring-time context keeps it
	// attributed to the cluster it came from, and read paths filter to the active
	// context so a previous cluster's resource names never surface on the new one.
	ClusterContext string    `json:"clusterContext,omitempty"`
	Time           time.Time `json:"time"`
}

// DropReason constants for categorizing why events are dropped
const (
	DropReasonNoisyFilter = "noisy_filter"
	DropReasonChannelFull = "channel_full"
	DropReasonAlreadySeen = "already_seen"
	DropReasonHistoryNil  = "history_nil"
	DropReasonStoreFailed = "store_failed"
	// DropReasonNoDiff: update event for a kind whose diff function found no
	// observable change. Heartbeats, managed-fields-only updates, reconcile
	// counters — see KindHasDiffer for the audited set.
	DropReasonNoDiff = "no_diff"
	// DropReasonTypeMismatch: a diff function received an object that wasn't
	// the expected type (e.g. dynamic informer wired with the wrong factory).
	// Distinguished from no_diff so a sudden spike is visible — these would
	// otherwise look like healthy heartbeat suppression.
	DropReasonTypeMismatch = "type_mismatch"
)

var (
	globalMetrics *EventMetrics
	metricsOnce   sync.Once
)

// initMetrics initializes the global metrics instance
func initMetrics() {
	metricsOnce.Do(func() {
		globalMetrics = &EventMetrics{
			EventsReceived: make(map[string]int64),
			EventsDropped:  make(map[string]int64),
			EventsRecorded: make(map[string]int64),
			RecentDrops:    make([]DropRecord, 0, 100),
			maxRecentDrops: 100,
			startTime:      time.Now(),
		}
	})
}

// GetMetrics returns the global metrics instance
func GetMetrics() *EventMetrics {
	initMetrics()
	return globalMetrics
}

// IncrementReceived increments the counter for received events by kind
func IncrementReceived(kind string) {
	m := GetMetrics()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EventsReceived[kind]++
}

// IncrementDropped increments the counter for dropped events by reason
func IncrementDropped(reason string) {
	m := GetMetrics()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EventsDropped[reason]++
}

// IncrementRecorded increments the counter for recorded events by kind
func IncrementRecorded(kind string) {
	m := GetMetrics()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.EventsRecorded[kind]++
}

// GetStoreErrorCount returns the number of store write failures
func GetStoreErrorCount() int64 {
	m := GetMetrics()
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.EventsDropped[DropReasonStoreFailed]
}

// GetTotalDropCount returns the total number of dropped events across all reasons
func GetTotalDropCount() int64 {
	m := GetMetrics()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total int64
	for _, count := range m.EventsDropped {
		total += count
	}
	return total
}

// RecordDrop records a dropped event with details for debugging
func RecordDrop(kind, namespace, name, reason, operation, clusterContext string) {
	m := GetMetrics()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.EventsDropped[reason]++

	record := DropRecord{
		Kind:           kind,
		Namespace:      namespace,
		Name:           name,
		Reason:         reason,
		Operation:      operation,
		ClusterContext: clusterContext,
		Time:           time.Now(),
	}

	// Keep only the most recent drops (ring buffer style)
	if len(m.RecentDrops) >= m.maxRecentDrops {
		// Shift everything left by one
		copy(m.RecentDrops, m.RecentDrops[1:])
		m.RecentDrops[len(m.RecentDrops)-1] = record
	} else {
		m.RecentDrops = append(m.RecentDrops, record)
	}
}

// GetSnapshot returns a snapshot of the current metrics
func (m *EventMetrics) GetSnapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Deep copy maps
	received := make(map[string]int64, len(m.EventsReceived))
	maps.Copy(received, m.EventsReceived)

	dropped := make(map[string]int64, len(m.EventsDropped))
	maps.Copy(dropped, m.EventsDropped)

	recorded := make(map[string]int64, len(m.EventsRecorded))
	maps.Copy(recorded, m.EventsRecorded)

	// Copy recent drops
	recentDrops := make([]DropRecord, len(m.RecentDrops))
	copy(recentDrops, m.RecentDrops)

	return MetricsSnapshot{
		Counters: MetricsCounters{
			Received: received,
			Dropped:  dropped,
			Recorded: recorded,
		},
		RecentDrops: recentDrops,
		Uptime:      time.Since(m.startTime).String(),
		UptimeSec:   int64(time.Since(m.startTime).Seconds()),
	}
}

// Reset clears all metrics (useful for testing)
func (m *EventMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.EventsReceived = make(map[string]int64)
	m.EventsDropped = make(map[string]int64)
	m.EventsRecorded = make(map[string]int64)
	m.RecentDrops = make([]DropRecord, 0, m.maxRecentDrops)
	m.EventsQueried = 0
	m.EventsFiltered = 0
	m.startTime = time.Now()
}

// DropsForCluster returns only the drop records attributed to clusterContext.
// Read paths pass the active context so a straggler drop stamped with a
// previously-connected cluster (recorded in the async informer-shutdown window
// after a switch) is never shown on the new cluster. Both the stamp and the read
// filter come from ActiveClusterContext(), so the in-cluster case matches too
// (both sides use the "in-cluster" sentinel).
func DropsForCluster(drops []DropRecord, clusterContext string) []DropRecord {
	out := make([]DropRecord, 0, len(drops))
	for _, d := range drops {
		if d.ClusterContext == clusterContext {
			out = append(out, d)
		}
	}
	return out
}

// ResetMetricsForContextSwitch clears per-cluster event-pipeline state when the
// connected cluster changes. RecentDrops name individual resources (kind /
// namespace / name), and the counters are per-cluster totals, so both must be
// dropped on a context switch — otherwise the debug/diagnostics drop surfaces
// expose a previously-connected cluster's resource names to whoever can read
// the same kind on the NEW cluster. Process uptime (startTime) is preserved:
// it reports how long the event pipeline has been running, not a per-cluster
// session, so it must survive the switch.
//
// Best-effort on the counters: unlike RecentDrops (stamped + filtered on read),
// the counters have no cluster guard, so a straggler informer draining after the
// switch (async shutdown, up to ~5s) can re-increment them with a few old-cluster
// ticks after this reset. Accepted: counters carry only kind names, not resource
// identity, so the exposure is a type label ("Secret") on a debug surface, never
// a resource name.
func ResetMetricsForContextSwitch() {
	m := GetMetrics()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.EventsReceived = make(map[string]int64)
	m.EventsDropped = make(map[string]int64)
	m.EventsRecorded = make(map[string]int64)
	m.RecentDrops = make([]DropRecord, 0, m.maxRecentDrops)
	m.EventsQueried = 0
	m.EventsFiltered = 0
}

// MetricsSnapshot is a JSON-serializable snapshot of metrics
type MetricsSnapshot struct {
	Counters    MetricsCounters `json:"counters"`
	RecentDrops []DropRecord    `json:"recent_drops"`
	Uptime      string          `json:"uptime"`
	UptimeSec   int64           `json:"uptime_sec"`
}

// MetricsCounters contains the counter maps
type MetricsCounters struct {
	Received map[string]int64 `json:"received"`
	Dropped  map[string]int64 `json:"dropped"`
	Recorded map[string]int64 `json:"recorded"`
}

// DebugEventsResponse is the full response for /api/debug/events
type DebugEventsResponse struct {
	Counters    MetricsCounters `json:"counters"`
	StoreStats  StoreStats      `json:"store_stats"`
	RecentDrops []DropRecord    `json:"recent_drops"`
	Uptime      string          `json:"uptime"`
	UptimeSec   int64           `json:"uptime_sec"`
}

// GetDebugEventsResponse builds the full debug response
func GetDebugEventsResponse() DebugEventsResponse {
	snapshot := GetMetrics().GetSnapshot()

	var stats StoreStats
	if store := GetStore(); store != nil {
		stats = store.Stats()
	}

	return DebugEventsResponse{
		Counters:    snapshot.Counters,
		StoreStats:  stats,
		RecentDrops: snapshot.RecentDrops,
		Uptime:      snapshot.Uptime,
		UptimeSec:   snapshot.UptimeSec,
	}
}

// DiagnoseRequest is the request for diagnosing a specific resource
type DiagnoseRequest struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// DiagnoseResponse is the response from the diagnose endpoint
type DiagnoseResponse struct {
	Resource        ResourceInfo    `json:"resource"`
	TimelineEvents  []TimelineEvent `json:"timeline_events"`
	DropHistory     []DropRecord    `json:"drop_history"`
	StorePresent    bool            `json:"store_present"`
	Recommendations []string        `json:"recommendations"`
}

// ResourceInfo identifies a resource being diagnosed
type ResourceInfo struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// GetDiagnosis diagnoses why events for a resource might be missing. `allow`, when
// non-nil, authorizes each timeline row / drop by (kind, apiVersion, namespace)
// BEFORE the recommendations are derived from them — so a caller who can't read
// some of the matched rows gets neither those rows nor tips computed from them.
// nil `allow` (auth off) returns everything. Rows are also scoped to the active
// clusterContext (store query + stamped drop provenance).
func GetDiagnosis(kind, namespace, name, clusterContext string, allow func(kind, apiVersion, namespace string) bool) DiagnoseResponse {
	resp := DiagnoseResponse{
		Resource: ResourceInfo{
			Kind:      kind,
			Namespace: namespace,
			Name:      name,
		},
		TimelineEvents:  []TimelineEvent{},
		DropHistory:     []DropRecord{},
		Recommendations: []string{},
	}

	store := GetStore()
	resp.StorePresent = store != nil

	if store != nil {
		// Query for events matching this resource. Scope to the active cluster
		// context — the persistent store outlives context switches, so an
		// unscoped query would return a previously-connected cluster's rows.
		events, err := store.Query(context.Background(), QueryOptions{
			Namespaces:       []string{namespace},
			Kinds:            []string{kind},
			Limit:            50,
			IncludeManaged:   true,
			IncludeK8sEvents: true,
			ClusterContext:   clusterContext,
		})
		if err == nil {
			// Filter to just this resource
			for _, e := range events {
				if e.Name == name {
					resp.TimelineEvents = append(resp.TimelineEvents, e)
				}
			}
		}
	}

	// Check for drops related to this resource. Scope to the active cluster so a
	// straggler drop from a previously-connected cluster (recorded in the async
	// informer-shutdown window after a switch) neither appears in DropHistory nor
	// influences the recommendations derived from it.
	metrics := GetMetrics()
	metrics.mu.RLock()
	for _, drop := range metrics.RecentDrops {
		if drop.ClusterContext == clusterContext && drop.Kind == kind && drop.Namespace == namespace && drop.Name == name {
			resp.DropHistory = append(resp.DropHistory, drop)
		}
	}
	metrics.mu.RUnlock()

	// Per-kind RBAC: drop rows the caller can't read BEFORE deriving any
	// recommendation, so tips can't describe a filtered-out event/drop. Drops
	// carry no apiVersion, so they resolve group-less (a Kind colliding with a
	// cluster-scoped builtin is the one documented residue).
	if allow != nil {
		events := resp.TimelineEvents[:0]
		for _, e := range resp.TimelineEvents {
			if allow(e.Kind, e.APIVersion, e.Namespace) {
				events = append(events, e)
			}
		}
		resp.TimelineEvents = events

		drops := resp.DropHistory[:0]
		for _, d := range resp.DropHistory {
			if allow(d.Kind, "", d.Namespace) {
				drops = append(drops, d)
			}
		}
		resp.DropHistory = drops
	}

	// Generate recommendations
	if len(resp.TimelineEvents) == 0 && len(resp.DropHistory) == 0 {
		resp.Recommendations = append(resp.Recommendations,
			"No events found in timeline store and no recent drops recorded.")
		resp.Recommendations = append(resp.Recommendations,
			"The resource may not have been created/updated since Explorer started.")
		resp.Recommendations = append(resp.Recommendations,
			"Check if the resource exists with: kubectl get "+kind+" -n "+namespace+" "+name)
	}

	if len(resp.DropHistory) > 0 {
		reasons := make(map[string]int)
		for _, drop := range resp.DropHistory {
			reasons[drop.Reason]++
		}
		for reason, count := range reasons {
			switch reason {
			case DropReasonNoisyFilter:
				resp.Recommendations = append(resp.Recommendations,
					"Resource was filtered "+strconv.Itoa(count)+" times by noisy_filter - check isNoisyResource() patterns in cache.go")
			case DropReasonChannelFull:
				resp.Recommendations = append(resp.Recommendations,
					"Resource events dropped due to channel full - the system may be under heavy load")
			case DropReasonAlreadySeen:
				resp.Recommendations = append(resp.Recommendations,
					"Resource marked as already seen - this is normal for informer restarts")
			}
		}
	}

	if len(resp.TimelineEvents) > 0 && resp.TimelineEvents[0].IsManaged() {
		resp.Recommendations = append(resp.Recommendations,
			"This is a managed resource (has owner). By default, managed resources are hidden from the timeline.")
		resp.Recommendations = append(resp.Recommendations,
			"Use include_managed=true query parameter to see managed resources.")
	}

	return resp
}
