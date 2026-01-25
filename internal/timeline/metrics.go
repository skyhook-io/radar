package timeline

import (
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
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Reason    string    `json:"reason"`
	Operation string    `json:"operation"`
	Time      time.Time `json:"time"`
}

// DropReason constants for categorizing why events are dropped
const (
	DropReasonNoisyFilter    = "noisy_filter"
	DropReasonChannelFull    = "channel_full"
	DropReasonAlreadySeen    = "already_seen"
	DropReasonHistoryNil     = "history_nil"
	DropReasonStoreFailed    = "store_failed"
	DropReasonSubscriberFull = "subscriber_full"
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
func RecordDrop(kind, namespace, name, reason, operation string) {
	m := GetMetrics()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.EventsDropped[reason]++

	record := DropRecord{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Reason:    reason,
		Operation: operation,
		Time:      time.Now(),
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
	for k, v := range m.EventsReceived {
		received[k] = v
	}

	dropped := make(map[string]int64, len(m.EventsDropped))
	for k, v := range m.EventsDropped {
		dropped[k] = v
	}

	recorded := make(map[string]int64, len(m.EventsRecorded))
	for k, v := range m.EventsRecorded {
		recorded[k] = v
	}

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
	Resource        ResourceInfo   `json:"resource"`
	TimelineEvents  []TimelineEvent `json:"timeline_events"`
	DropHistory     []DropRecord   `json:"drop_history"`
	StorePresent    bool           `json:"store_present"`
	Recommendations []string       `json:"recommendations"`
}

// ResourceInfo identifies a resource being diagnosed
type ResourceInfo struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// GetDiagnosis diagnoses why events for a specific resource might be missing
func GetDiagnosis(kind, namespace, name string) DiagnoseResponse {
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
		// Query for events matching this resource
		events, err := store.Query(nil, QueryOptions{
			Namespace:        namespace,
			Kinds:            []string{kind},
			Limit:            50,
			IncludeManaged:   true,
			IncludeK8sEvents: true,
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

	// Check for drops related to this resource
	metrics := GetMetrics()
	metrics.mu.RLock()
	for _, drop := range metrics.RecentDrops {
		if drop.Kind == kind && drop.Namespace == namespace && drop.Name == name {
			resp.DropHistory = append(resp.DropHistory, drop)
		}
	}
	metrics.mu.RUnlock()

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
					"Resource was filtered "+string(rune(count))+" times by noisy_filter - check isNoisyResource() patterns in cache.go")
			case DropReasonChannelFull:
				resp.Recommendations = append(resp.Recommendations,
					"Resource events dropped due to channel full - the system may be under heavy load")
			case DropReasonAlreadySeen:
				resp.Recommendations = append(resp.Recommendations,
					"Resource marked as already seen - this is normal for informer restarts")
			case DropReasonSubscriberFull:
				resp.Recommendations = append(resp.Recommendations,
					"SSE subscriber channel full - clients may not be keeping up with event stream")
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
