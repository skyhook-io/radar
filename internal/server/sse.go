package server

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/topology"
)

// MaxSSEClients limits the number of concurrent SSE connections to prevent resource exhaustion
const MaxSSEClients = 100

// SSEBroadcaster manages Server-Sent Events connections
type SSEBroadcaster struct {
	clients    map[chan SSEEvent]ClientInfo
	register   chan clientRegistration
	unregister chan chan SSEEvent
	mu         sync.RWMutex
	stopCh     chan struct{}

	// watchStopCh is closed to stop the current watchResourceChanges goroutine.
	// On context switch, it is replaced with a fresh channel to restart the watcher.
	watchStopCh chan struct{}
	watchMu     sync.Mutex

	// Cached topology for relationship lookups (updated on each topology rebuild)
	cachedTopology   *topology.Topology
	cachedTopologyMu sync.RWMutex
}

// ClientInfo stores information about a connected client
type ClientInfo struct {
	Namespaces []string // Filter to specific namespaces (empty = all)
	ViewMode   string   // "full" or "traffic"
}

type clientRegistration struct {
	ch         chan SSEEvent
	namespaces []string
	viewMode   string
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
		clients:     make(map[chan SSEEvent]ClientInfo),
		register:    make(chan clientRegistration),
		unregister:  make(chan chan SSEEvent),
		stopCh:      make(chan struct{}),
		watchStopCh: make(chan struct{}),
	}
}

// Start begins the broadcaster's main loop
func (b *SSEBroadcaster) Start() {
	// Build initial topology cache (only if connected)
	if k8s.IsConnected() {
		b.initCachedTopology()
	}

	// Register for context switch notifications
	b.registerContextSwitchCallback()

	// Register for connection state changes (for graceful startup)
	b.registerConnectionStateCallback()

	// Register for CRD discovery completion
	b.registerCRDDiscoveryCallback()

	go b.run()
	go b.watchResourceChanges()
	go b.watchDeferredSync()
	go b.heartbeat()
}

// registerCRDDiscoveryCallback registers for CRD discovery completion
// When discovery completes, broadcast topology to update the discovery status in UI
func (b *SSEBroadcaster) registerCRDDiscoveryCallback() {
	k8s.OnCRDDiscoveryComplete(func() {
		log.Printf("SSE broadcaster: CRD discovery complete, broadcasting topology update")
		b.broadcastTopologyUpdate()
	})
}

// watchDeferredSync waits for deferred informers (secrets, events, etc.) to
// finish syncing and then broadcasts a topology update + deferred_ready event
// so the UI can fill in the missing data (config edges, event counts, etc.).
// It captures a local copy of watchStopCh so it exits on context switch,
// and is restarted alongside the resource watcher via restartResourceWatcher.
func (b *SSEBroadcaster) watchDeferredSync() {
	b.watchMu.Lock()
	watchStop := b.watchStopCh
	b.watchMu.Unlock()

	// Wait for cache to exist first
	for {
		cache := k8s.GetResourceCache()
		if cache != nil {
			ch := cache.DeferredDone()
			if ch == nil {
				return // no deferred informers
			}
			select {
			case <-ch:
				// Verify cache is still current (not torn down by context switch)
				if k8s.GetResourceCache() == nil {
					return
				}
				log.Printf("SSE broadcaster: deferred informers synced, broadcasting topology update")
				b.Broadcast(SSEEvent{
					Event: "deferred_ready",
					Data:  map[string]any{},
				})
				b.broadcastTopologyUpdate()
				return
			case <-b.stopCh:
				return
			case <-watchStop:
				return
			}
		}
		// Cache not ready yet — wait a bit and retry
		select {
		case <-time.After(100 * time.Millisecond):
		case <-b.stopCh:
			return
		case <-watchStop:
			return
		}
	}
}

// registerConnectionStateCallback registers for connection state changes
// This broadcasts connection_state events to all clients for graceful startup UI
func (b *SSEBroadcaster) registerConnectionStateCallback() {
	k8s.OnConnectionChange(func(status k8s.ConnectionStatus) {
		log.Printf("SSE broadcaster: connection state changed to %q (context=%s, progress=%q)",
			status.State, status.Context, status.ProgressMsg)

		b.Broadcast(SSEEvent{
			Event: "connection_state",
			Data: map[string]any{
				"state":           status.State,
				"context":         status.Context,
				"clusterName":     status.ClusterName,
				"error":           status.Error,
				"errorType":       status.ErrorType,
				"progressMessage": status.ProgressMsg,
			},
		})

		// When we become connected, build and broadcast topology to all clients
		if status.State == k8s.StateConnected {
			log.Printf("SSE broadcaster: connection became connected, scheduling topology broadcast")
			go b.broadcastTopologyUpdate()
		}
	})
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

		// Restart the resource change watcher for the new cache
		b.restartResourceWatcher()

		// Broadcast context_changed event to all clients
		b.mu.RLock()
		clientCount := len(b.clients)
		b.mu.RUnlock()
		log.Printf("SSE broadcaster: broadcasting context_changed to %d clients", clientCount)

		b.Broadcast(SSEEvent{
			Event: "context_changed",
			Data: map[string]any{
				"context": newContext,
			},
		})

		// Broadcast the new topology so clients can complete the switch
		// Run in goroutine to not block the context switch
		log.Printf("SSE broadcaster: scheduling topology broadcast")
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
			if len(b.clients) >= MaxSSEClients {
				b.mu.Unlock()
				log.Printf("SSE client rejected: max clients (%d) reached", MaxSSEClients)
				close(reg.ch) // Signal rejection by closing the channel
				continue
			}
			b.clients[reg.ch] = ClientInfo{Namespaces: reg.namespaces, ViewMode: reg.viewMode}
			b.mu.Unlock()
			log.Printf("SSE client connected (namespaces=%v, view=%s), total clients: %d", reg.namespaces, reg.viewMode, len(b.clients))

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

// restartResourceWatcher stops the current watchResourceChanges and
// watchDeferredSync goroutines and spawns new ones for the current
// resource cache. Called on context switch since the old cache's
// changes channel is abandoned (never closed — see cache.go Stop()).
func (b *SSEBroadcaster) restartResourceWatcher() {
	b.watchMu.Lock()
	defer b.watchMu.Unlock()

	close(b.watchStopCh)
	b.watchStopCh = make(chan struct{})

	go b.watchResourceChanges()
	go b.watchDeferredSync()
}

// watchResourceChanges listens for K8s resource changes and broadcasts topology updates.
// If the cache isn't ready yet (server starts before cluster init), it waits for
// the connection state to become connected before starting the watch loop.
// It captures a local copy of watchStopCh so that restartResourceWatcher() can
// stop this goroutine by closing the old channel without a data race.
func (b *SSEBroadcaster) watchResourceChanges() {
	// Capture local stop channel — restartResourceWatcher() will close it
	// when a context switch happens, causing this goroutine to exit.
	b.watchMu.Lock()
	watchStop := b.watchStopCh
	b.watchMu.Unlock()

	cache := k8s.GetResourceCache()
	if cache == nil {
		// Cache not ready yet — wait for connection to be established
		log.Println("SSE broadcaster: cache not ready, waiting for connection...")
		ch := make(chan struct{}, 1)
		k8s.OnConnectionChange(func(status k8s.ConnectionStatus) {
			// Check if this watcher was replaced by a context switch.
			// Without this, callbacks accumulate in the connectionCallbacks
			// slice on each restart and fire uselessly on future state changes.
			select {
			case <-watchStop:
				return
			default:
			}
			if status.State == k8s.StateConnected {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		})
		// If already connected by the time we register, check again
		if k8s.IsConnected() {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		select {
		case <-ch:
			cache = k8s.GetResourceCache()
			if cache == nil {
				log.Println("Warning: Resource cache still nil after connection")
				return
			}
			log.Println("SSE broadcaster: cache ready, starting resource change watcher")
		case <-b.stopCh:
			return
		case <-watchStop:
			return
		}
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

		case <-watchStop:
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
	// Skip if resource cache is torn down (e.g. during context switch).
	// The next successful connection will trigger a fresh build.
	if k8s.GetResourceCache() == nil {
		return
	}

	b.mu.RLock()
	clients := make(map[chan SSEEvent]ClientInfo, len(b.clients))
	maps.Copy(clients, b.clients)
	b.mu.RUnlock()

	log.Printf("Broadcasting topology update to %d clients", len(clients))

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
		log.Printf("No SSE clients to broadcast topology to")
		return
	}

	// Group clients by namespace filter + viewMode
	// Use comma-separated namespaces string as map key since slices aren't comparable
	// Note: namespaces are pre-sorted at subscription time for consistent grouping
	type clientKey struct {
		namespacesKey string // comma-separated sorted namespaces
		viewMode      string
	}
	type clientGroup struct {
		namespaces []string
		channels   []chan SSEEvent
	}
	clientGroups := make(map[clientKey]*clientGroup)
	for ch, info := range clients {
		nsKey := strings.Join(info.Namespaces, ",") // namespaces already sorted at subscribe time
		key := clientKey{namespacesKey: nsKey, viewMode: info.ViewMode}
		if clientGroups[key] == nil {
			clientGroups[key] = &clientGroup{namespaces: info.Namespaces}
		}
		clientGroups[key].channels = append(clientGroups[key].channels, ch)
	}

	// Build topology for each group and send
	for key, group := range clientGroups {
		opts := topology.DefaultBuildOptions()
		opts.Namespaces = group.namespaces
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

		for _, ch := range group.channels {
			safeSend(ch, event)
		}
		log.Printf("Sent topology (%d nodes, %d edges) to %d clients (ns=%v, view=%s)",
			len(topo.Nodes), len(topo.Edges), len(group.channels), group.namespaces, key.viewMode)
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

// Subscribe adds a new SSE client. Returns nil if max clients reached.
func (b *SSEBroadcaster) Subscribe(namespaces []string, viewMode string) chan SSEEvent {
	// Check client count before creating the channel to fail fast
	b.mu.RLock()
	clientCount := len(b.clients)
	b.mu.RUnlock()

	if clientCount >= MaxSSEClients {
		log.Printf("SSE subscription rejected: max clients (%d) reached", MaxSSEClients)
		return nil
	}

	// Sort namespaces once at subscription time for consistent grouping during broadcasts
	sortedNs := make([]string, len(namespaces))
	copy(sortedNs, namespaces)
	sort.Strings(sortedNs)

	ch := make(chan SSEEvent, 10)
	b.register <- clientRegistration{ch: ch, namespaces: sortedNs, viewMode: viewMode}
	return ch
}

// Unsubscribe removes an SSE client
func (b *SSEBroadcaster) Unsubscribe(ch chan SSEEvent) {
	b.unregister <- ch
}

// ClientCount returns the number of connected SSE clients.
func (b *SSEBroadcaster) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
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
	namespaces := parseNamespaces(r.URL.Query())
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
	eventCh := b.Subscribe(namespaces, viewMode)
	if eventCh == nil {
		http.Error(w, "Too many SSE connections", http.StatusServiceUnavailable)
		return
	}
	defer b.Unsubscribe(eventCh)

	// Send current connection state immediately so client knows current status
	status := k8s.GetConnectionStatus()
	connData, err := json.Marshal(map[string]any{
		"state":           status.State,
		"context":         status.Context,
		"clusterName":     status.ClusterName,
		"error":           status.Error,
		"errorType":       status.ErrorType,
		"progressMessage": status.ProgressMsg,
	})
	if err == nil {
		fmt.Fprintf(w, "event: connection_state\ndata: %s\n\n", connData)
		flusher.Flush()
	}

	// Send initial topology immediately (only if connected)
	if status.State == k8s.StateConnected {
		builder := topology.NewBuilder()
		opts := topology.DefaultBuildOptions()
		opts.Namespaces = namespaces
		if viewMode == "traffic" {
			opts.ViewMode = topology.ViewModeTraffic
		}
		if topo, err := builder.Build(opts); err == nil {
			data, marshalErr := json.Marshal(topo)
			if marshalErr != nil {
				log.Printf("SSE: failed to marshal initial topology: %v", marshalErr)
			} else {
				fmt.Fprintf(w, "event: topology\ndata: %s\n\n", data)
				flusher.Flush()
			}
		}
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
				log.Printf("SSE: failed to marshal event %q: %v", event.Event, err)
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
