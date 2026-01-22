package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
	"github.com/skyhook-io/skyhook-explorer/internal/topology"
)

// SSEBroadcaster manages Server-Sent Events connections
type SSEBroadcaster struct {
	clients    map[chan SSEEvent]ClientInfo
	register   chan clientRegistration
	unregister chan chan SSEEvent
	mu         sync.RWMutex
	stopCh     chan struct{}

	// Cached topology for relationship lookups (updated on each topology rebuild)
	cachedTopology   *topology.Topology
	cachedTopologyMu sync.RWMutex
}

// ClientInfo stores information about a connected client
type ClientInfo struct {
	Namespace string
	ViewMode  string // "full" or "traffic"
}

type clientRegistration struct {
	ch        chan SSEEvent
	namespace string
	viewMode  string
}

// SSEEvent represents an event to send to clients
type SSEEvent struct {
	Event string `json:"event"` // "topology", "k8s_event", "heartbeat"
	Data  any    `json:"data"`
}

// safeSend sends an event to a channel, recovering from panic if the channel is closed
func safeSend(ch chan SSEEvent, event SSEEvent) {
	defer func() {
		recover() // Ignore panic from send on closed channel
	}()
	select {
	case ch <- event:
	default:
		// Channel full, skip
	}
}

// NewSSEBroadcaster creates a new SSE broadcaster
func NewSSEBroadcaster() *SSEBroadcaster {
	return &SSEBroadcaster{
		clients:    make(map[chan SSEEvent]ClientInfo),
		register:   make(chan clientRegistration),
		unregister: make(chan chan SSEEvent),
		stopCh:     make(chan struct{}),
	}
}

// Start begins the broadcaster's main loop
func (b *SSEBroadcaster) Start() {
	// Build initial topology cache
	b.initCachedTopology()

	// Register for context switch notifications
	b.registerContextSwitchCallback()

	go b.run()
	go b.watchResourceChanges()
	go b.heartbeat()
}

// registerContextSwitchCallback registers for context switch notifications
// When context switches, we clear the cached topology and notify clients
func (b *SSEBroadcaster) registerContextSwitchCallback() {
	// Register for progress updates during context switch
	k8s.OnContextSwitchProgress(func(message string) {
		b.Broadcast(SSEEvent{
			Event: "context_switch_progress",
			Data: map[string]any{
				"message": message,
			},
		})
	})

	// Register for context switch completion
	k8s.OnContextSwitch(func(newContext string) {
		log.Printf("SSE broadcaster: context switched to %q, clearing cached topology", newContext)

		// Clear cached topology
		b.cachedTopologyMu.Lock()
		b.cachedTopology = nil
		b.cachedTopologyMu.Unlock()

		// Broadcast context_changed event to all clients
		b.Broadcast(SSEEvent{
			Event: "context_changed",
			Data: map[string]any{
				"context": newContext,
			},
		})

		// Broadcast the new topology so clients can complete the switch
		// Run in goroutine to not block the context switch
		go b.broadcastTopologyUpdate()
	})
}

// initCachedTopology builds the initial topology cache
func (b *SSEBroadcaster) initCachedTopology() {
	builder := topology.NewBuilder()
	opts := topology.DefaultBuildOptions()
	opts.ViewMode = topology.ViewModeResources
	// Include ReplicaSets in the cache so relationship lookups work for them
	opts.IncludeReplicaSets = true

	if topo, err := builder.Build(opts); err == nil {
		b.updateCachedTopology(topo)
		log.Printf("Initialized topology cache with %d nodes and %d edges", len(topo.Nodes), len(topo.Edges))
	} else {
		log.Printf("Warning: Failed to initialize topology cache: %v", err)
	}
}

// Stop gracefully shuts down the broadcaster
func (b *SSEBroadcaster) Stop() {
	close(b.stopCh)
}

func (b *SSEBroadcaster) run() {
	for {
		select {
		case <-b.stopCh:
			// Close all client channels
			b.mu.Lock()
			for ch := range b.clients {
				close(ch)
			}
			b.clients = make(map[chan SSEEvent]ClientInfo)
			b.mu.Unlock()
			return

		case reg := <-b.register:
			b.mu.Lock()
			b.clients[reg.ch] = ClientInfo{Namespace: reg.namespace, ViewMode: reg.viewMode}
			b.mu.Unlock()
			log.Printf("SSE client connected (namespace=%s, view=%s), total clients: %d", reg.namespace, reg.viewMode, len(b.clients))

		case ch := <-b.unregister:
			b.mu.Lock()
			if _, ok := b.clients[ch]; ok {
				delete(b.clients, ch)
				close(ch)
			}
			b.mu.Unlock()
			log.Printf("SSE client disconnected, total clients: %d", len(b.clients))
		}
	}
}

// watchResourceChanges listens for K8s resource changes and broadcasts topology updates
func (b *SSEBroadcaster) watchResourceChanges() {
	cache := k8s.GetResourceCache()
	if cache == nil {
		log.Println("Warning: Resource cache not available for SSE broadcasts")
		return
	}

	changes := cache.Changes()
	if changes == nil {
		return
	}

	// Debounce changes - wait for 100ms of quiet before sending topology update
	debounceTimer := time.NewTimer(0)
	<-debounceTimer.C // drain initial timer
	pendingUpdate := false

	for {
		select {
		case <-b.stopCh:
			return

		case change, ok := <-changes:
			if !ok {
				return
			}

			// Broadcast K8s event immediately for important events
			if change.Kind == "Event" || change.Operation == "delete" ||
				(change.Kind == "Pod" && change.Operation != "update") ||
				change.Diff != nil { // Also broadcast updates with meaningful diffs
				eventData := map[string]any{
					"kind":      change.Kind,
					"namespace": change.Namespace,
					"name":      change.Name,
					"operation": change.Operation,
				}
				// Include diff info if available
				if change.Diff != nil {
					eventData["diff"] = map[string]any{
						"fields":  change.Diff.Fields,
						"summary": change.Diff.Summary,
					}
				}
				b.Broadcast(SSEEvent{
					Event: "k8s_event",
					Data:  eventData,
				})
			}

			// Schedule debounced topology update (500ms to reduce UI thrashing)
			if !pendingUpdate {
				debounceTimer.Reset(500 * time.Millisecond)
				pendingUpdate = true
			}

		case <-debounceTimer.C:
			if pendingUpdate {
				pendingUpdate = false
				b.broadcastTopologyUpdate()
			}
		}
	}
}

// broadcastTopologyUpdate sends the current topology to all clients
func (b *SSEBroadcaster) broadcastTopologyUpdate() {
	b.mu.RLock()
	clients := make(map[chan SSEEvent]ClientInfo, len(b.clients))
	for ch, info := range b.clients {
		clients[ch] = info
	}
	b.mu.RUnlock()

	builder := topology.NewBuilder()

	// Always build and cache a full topology (all namespaces, resources view)
	// for relationship lookups, even if no clients are connected
	fullOpts := topology.DefaultBuildOptions()
	fullOpts.ViewMode = topology.ViewModeResources
	fullOpts.IncludeReplicaSets = true // Include for relationship lookups
	if fullTopo, err := builder.Build(fullOpts); err == nil {
		b.updateCachedTopology(fullTopo)
	} else {
		log.Printf("Error building full topology for cache: %v", err)
	}

	if len(clients) == 0 {
		return
	}

	// Group clients by namespace + viewMode filter
	type clientKey struct {
		namespace string
		viewMode  string
	}
	clientGroups := make(map[clientKey][]chan SSEEvent)
	for ch, info := range clients {
		key := clientKey{namespace: info.Namespace, viewMode: info.ViewMode}
		clientGroups[key] = append(clientGroups[key], ch)
	}

	// Build topology for each group and send
	for key, channels := range clientGroups {
		opts := topology.DefaultBuildOptions()
		opts.Namespace = key.namespace
		if key.viewMode == "traffic" {
			opts.ViewMode = topology.ViewModeTraffic
		}

		topo, err := builder.Build(opts)
		if err != nil {
			log.Printf("Error building topology for broadcast: %v", err)
			continue
		}

		event := SSEEvent{
			Event: "topology",
			Data:  topo,
		}

		for _, ch := range channels {
			safeSend(ch, event)
		}
	}
}

// heartbeat sends periodic heartbeats to keep connections alive
func (b *SSEBroadcaster) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopCh:
			return
		case <-ticker.C:
			b.Broadcast(SSEEvent{
				Event: "heartbeat",
				Data: map[string]any{
					"time": time.Now().Unix(),
				},
			})
		}
	}
}

// Broadcast sends an event to all connected clients
func (b *SSEBroadcaster) Broadcast(event SSEEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.clients {
		safeSend(ch, event)
	}
}

// Subscribe adds a new SSE client
func (b *SSEBroadcaster) Subscribe(namespace, viewMode string) chan SSEEvent {
	ch := make(chan SSEEvent, 10)
	b.register <- clientRegistration{ch: ch, namespace: namespace, viewMode: viewMode}
	return ch
}

// Unsubscribe removes an SSE client
func (b *SSEBroadcaster) Unsubscribe(ch chan SSEEvent) {
	b.unregister <- ch
}

// GetCachedTopology returns the most recently built full topology.
// This is used for relationship lookups without rebuilding the topology.
func (b *SSEBroadcaster) GetCachedTopology() *topology.Topology {
	b.cachedTopologyMu.RLock()
	defer b.cachedTopologyMu.RUnlock()
	return b.cachedTopology
}

// updateCachedTopology stores a full topology for relationship lookups
func (b *SSEBroadcaster) updateCachedTopology(topo *topology.Topology) {
	b.cachedTopologyMu.Lock()
	defer b.cachedTopologyMu.Unlock()
	b.cachedTopology = topo
}

// HandleSSE is the HTTP handler for the SSE endpoint
func (b *SSEBroadcaster) HandleSSE(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Get filters from query
	namespace := r.URL.Query().Get("namespace")
	viewMode := r.URL.Query().Get("view")
	if viewMode == "" {
		viewMode = "full"
	}

	// Ensure we can flush
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to events
	eventCh := b.Subscribe(namespace, viewMode)
	defer b.Unsubscribe(eventCh)

	// Send initial topology immediately
	builder := topology.NewBuilder()
	opts := topology.DefaultBuildOptions()
	opts.Namespace = namespace
	if viewMode == "traffic" {
		opts.ViewMode = topology.ViewModeTraffic
	}
	if topo, err := builder.Build(opts); err == nil {
		data, _ := json.Marshal(topo)
		fmt.Fprintf(w, "event: topology\ndata: %s\n\n", data)
		flusher.Flush()
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
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Event, data)
			flusher.Flush()
		}
	}
}
