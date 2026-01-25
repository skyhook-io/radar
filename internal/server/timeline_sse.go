package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/skyhook-io/skyhook-explorer/internal/timeline"
)

// TimelineSSEBroadcaster manages Server-Sent Events connections for timeline streams
type TimelineSSEBroadcaster struct {
	clients    map[chan TimelineSSEEvent]TimelineClientInfo
	register   chan timelineClientRegistration
	unregister chan chan TimelineSSEEvent
	mu         sync.RWMutex
	stopCh     chan struct{}
}

// TimelineClientInfo stores information about a connected timeline client
type TimelineClientInfo struct {
	Namespace string
	GroupBy   timeline.GroupingMode
	Filter    string // Filter preset name
}

type timelineClientRegistration struct {
	ch        chan TimelineSSEEvent
	namespace string
	groupBy   timeline.GroupingMode
	filter    string
}

// TimelineSSEEvent represents an event to send to timeline clients
type TimelineSSEEvent struct {
	Event string `json:"event"` // "initial", "event", "group_update", "heartbeat"
	Data  any    `json:"data"`
}

// NewTimelineSSEBroadcaster creates a new timeline SSE broadcaster
func NewTimelineSSEBroadcaster() *TimelineSSEBroadcaster {
	return &TimelineSSEBroadcaster{
		clients:    make(map[chan TimelineSSEEvent]TimelineClientInfo),
		register:   make(chan timelineClientRegistration),
		unregister: make(chan chan TimelineSSEEvent),
		stopCh:     make(chan struct{}),
	}
}

// Start begins the timeline broadcaster's main loop
func (b *TimelineSSEBroadcaster) Start() {
	go b.run()
	go b.watchTimelineEvents()
	go b.heartbeat()
}

// Stop gracefully shuts down the broadcaster
func (b *TimelineSSEBroadcaster) Stop() {
	close(b.stopCh)
}

func (b *TimelineSSEBroadcaster) run() {
	for {
		select {
		case <-b.stopCh:
			// Close all client channels
			b.mu.Lock()
			for ch := range b.clients {
				close(ch)
			}
			b.clients = make(map[chan TimelineSSEEvent]TimelineClientInfo)
			b.mu.Unlock()
			return

		case reg := <-b.register:
			b.mu.Lock()
			b.clients[reg.ch] = TimelineClientInfo{
				Namespace: reg.namespace,
				GroupBy:   reg.groupBy,
				Filter:    reg.filter,
			}
			b.mu.Unlock()
			log.Printf("Timeline SSE client connected (namespace=%s, groupBy=%s), total clients: %d",
				reg.namespace, reg.groupBy, len(b.clients))

		case ch := <-b.unregister:
			b.mu.Lock()
			if _, ok := b.clients[ch]; ok {
				delete(b.clients, ch)
				close(ch)
			}
			b.mu.Unlock()
			log.Printf("Timeline SSE client disconnected, total clients: %d", len(b.clients))
		}
	}
}

// watchTimelineEvents subscribes to timeline events and broadcasts to clients
func (b *TimelineSSEBroadcaster) watchTimelineEvents() {
	eventCh, unsubscribe := timeline.Subscribe()
	defer unsubscribe()

	for {
		select {
		case <-b.stopCh:
			return

		case event, ok := <-eventCh:
			if !ok {
				return
			}

			// Broadcast to clients that match this event's namespace
			b.broadcastEvent(event)
		}
	}
}

// broadcastEvent sends a timeline event to matching clients
func (b *TimelineSSEBroadcaster) broadcastEvent(event timeline.TimelineEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch, info := range b.clients {
		// Check namespace filter
		if info.Namespace != "" && event.Namespace != info.Namespace {
			continue
		}

		// Apply filter preset if specified
		if info.Filter != "" {
			presets := timeline.DefaultFilterPresets()
			if preset, ok := presets[info.Filter]; ok {
				compiled, err := timeline.CompileFilter(&preset)
				if err == nil && compiled != nil && !compiled.Matches(&event) {
					continue
				}
			}
		}

		// Compute group ID for the event
		groupID := computeGroupID(&event, info.GroupBy)

		sseEvent := TimelineSSEEvent{
			Event: "event",
			Data: map[string]any{
				"event":   event,
				"groupId": groupID,
			},
		}

		// Non-blocking send
		select {
		case ch <- sseEvent:
		default:
			// Channel full, skip
		}
	}
}

// computeGroupID returns the group ID for an event based on grouping mode
func computeGroupID(event *timeline.TimelineEvent, mode timeline.GroupingMode) string {
	switch mode {
	case timeline.GroupByOwner:
		if event.Owner != nil {
			return fmt.Sprintf("%s/%s/%s", event.Owner.Kind, event.Namespace, event.Owner.Name)
		}
		return fmt.Sprintf("%s/%s/%s", event.Kind, event.Namespace, event.Name)

	case timeline.GroupByApp:
		// Try app.kubernetes.io/name first, then app label
		if event.Labels != nil {
			if appName, ok := event.Labels["app.kubernetes.io/name"]; ok {
				return fmt.Sprintf("app/%s/%s", event.Namespace, appName)
			}
			if appName, ok := event.Labels["app"]; ok {
				return fmt.Sprintf("app/%s/%s", event.Namespace, appName)
			}
		}
		return fmt.Sprintf("%s/%s/%s", event.Kind, event.Namespace, event.Name)

	case timeline.GroupByNamespace:
		return fmt.Sprintf("namespace/%s", event.Namespace)

	default:
		return ""
	}
}

// heartbeat sends periodic heartbeats to keep connections alive
func (b *TimelineSSEBroadcaster) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.broadcast(TimelineSSEEvent{
				Event: "heartbeat",
				Data: map[string]any{
					"time": time.Now().Unix(),
				},
			})
		}
	}
}

// broadcast sends an event to all connected clients
func (b *TimelineSSEBroadcaster) broadcast(event TimelineSSEEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.clients {
		select {
		case ch <- event:
		default:
			// Channel full, skip
		}
	}
}

// subscribe adds a new SSE client for timeline
func (b *TimelineSSEBroadcaster) subscribe(namespace string, groupBy timeline.GroupingMode, filter string) chan TimelineSSEEvent {
	ch := make(chan TimelineSSEEvent, 50)
	b.register <- timelineClientRegistration{
		ch:        ch,
		namespace: namespace,
		groupBy:   groupBy,
		filter:    filter,
	}
	return ch
}

// unsubscribe removes an SSE client
func (b *TimelineSSEBroadcaster) unsubscribe(ch chan TimelineSSEEvent) {
	b.unregister <- ch
}

// HandleTimelineSSE is the HTTP handler for the timeline SSE endpoint
func (b *TimelineSSEBroadcaster) HandleTimelineSSE(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Get filters from query
	namespace := r.URL.Query().Get("namespace")
	groupByStr := r.URL.Query().Get("group_by")
	filter := r.URL.Query().Get("filter")

	// Parse grouping mode
	groupBy := timeline.GroupByNone
	switch groupByStr {
	case "owner":
		groupBy = timeline.GroupByOwner
	case "app":
		groupBy = timeline.GroupByApp
	case "namespace":
		groupBy = timeline.GroupByNamespace
	}

	// Ensure we can flush
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to events
	eventCh := b.subscribe(namespace, groupBy, filter)
	defer b.unsubscribe(eventCh)

	// Send initial data immediately
	if err := b.sendInitialData(r.Context(), w, flusher, namespace, groupBy, filter); err != nil {
		log.Printf("Error sending initial timeline data: %v", err)
		return
	}

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			data, err := json.Marshal(event.Data)
			if err != nil {
				// Log the error and notify client instead of silently dropping
				log.Printf("Timeline SSE: failed to marshal event %q: %v", event.Event, err)
				errorData, _ := json.Marshal(map[string]string{
					"error":      "Failed to serialize event data",
					"event_type": event.Event,
				})
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", errorData)
				flusher.Flush()
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, data)
			flusher.Flush()
		}
	}
}

// sendInitialData sends the initial grouped timeline data to the client
func (b *TimelineSSEBroadcaster) sendInitialData(reqCtx context.Context, w http.ResponseWriter, flusher http.Flusher, namespace string, groupBy timeline.GroupingMode, filter string) error {
	store := timeline.GetStore()
	if store == nil {
		// No store initialized, send empty initial data
		data, _ := json.Marshal(map[string]any{
			"groups": []any{},
			"meta": map[string]any{
				"totalEvents": 0,
			},
		})
		fmt.Fprintf(w, "event: initial\ndata: %s\n\n", data)
		flusher.Flush()
		return nil
	}

	// Build query options
	opts := timeline.DefaultQueryOptions()
	opts.Namespace = namespace
	opts.GroupBy = groupBy
	opts.FilterPreset = filter
	opts.Limit = 500 // Initial load limit

	// Use request context with timeout - query cancels if client disconnects
	ctx, cancel := context.WithTimeout(reqCtx, 5*time.Second)
	defer cancel()

	// Query grouped data
	response, err := store.QueryGrouped(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to query timeline: %w", err)
	}

	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal timeline data: %w", err)
	}

	fmt.Fprintf(w, "event: initial\ndata: %s\n\n", data)
	flusher.Flush()
	return nil
}
