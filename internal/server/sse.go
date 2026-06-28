package server

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/perfstats"
	topology "github.com/skyhook-io/radar/pkg/topology"
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

	// lastBroadcastMaxEstimated holds the max EstimatedNodes across all
	// per-group topology builds in the most recent broadcast cycle. Drives
	// the debounce ladder (see topologyDebounceFor). It reflects only the
	// currently-active client groups: a sample window over recent builds
	// would let a brief visit to a big namespace keep the debounce
	// sticky-high long after the user filtered to a small one, whereas this
	// settles within one cycle of a namespace switch.
	lastBroadcastMaxEstimated atomic.Int64

	// watchStopCh is closed to stop the current watchResourceChanges goroutine.
	// On context switch, it is replaced with a fresh channel to restart the watcher.
	watchStopCh chan struct{}
	watchMu     sync.Mutex

	// Cached topology for relationship lookups (rebuilt on broadcast or lazily on access)
	cachedTopology      *topology.Topology
	cachedTopologyMu    sync.RWMutex
	cachedTopologyDirty bool // true when changes occurred but topology not yet rebuilt

	// warmupDone is closed when deferred informers finish syncing. During warmup,
	// topology broadcasts use longer debounce and skip the expensive full-topology
	// cache build. Protected by watchMu (same as watchStopCh).
	warmupDone chan struct{}
}

// ClientInfo stores information about a connected client
type ClientInfo struct {
	Namespaces       []string // Filter to specific namespaces (empty = all)
	ViewMode         string   // "full" or "traffic"
	ShowPolicyEffect bool     // Evaluate NetworkPolicies on edges
	// DeniedKinds are cluster-scoped topology kinds (Nodes, PV, StorageClass,
	// NodePool, …) this user can't list, stripped from every topology frame.
	// Resolved once at subscribe time (the request is available there) so the
	// broadcast loop never runs a SAR. nil/empty for users with full access.
	DeniedKinds map[topology.NodeKind]bool
}

type clientRegistration struct {
	ch               chan SSEEvent
	namespaces       []string
	viewMode         string
	showPolicyEffect bool
	deniedKinds      map[topology.NodeKind]bool
}

// SSEEvent represents an event to send to clients
type SSEEvent struct {
	Event string `json:"event"` // "topology", "k8s_event", "heartbeat"
	Data  any    `json:"data"`
}

// topologyDebounceFor returns the topology-broadcast debounce duration based
// on the max estimated topology node count across the most recent broadcast
// cycle's per-group builds. Falls back to a crude derivation from total
// resource count before the first broadcast (or when all clients have been
// disconnected for an entire cycle).
//
// Ladder: ≤500 → 1s, ≤2000 → 2s, ≤5000 → 5s, >5000 → 15s. Minimum is 1s by
// design — even at the smallest cluster scale we don't need faster topology
// refreshes than that, and SSE k8s_event frames (which fire immediately, not
// on this debounce) cover the case where the user wants to see individual
// resource state changes in real time.
//
// The lastBroadcastMaxEstimated input reflects only currently-active client
// groups, which means a namespace switch settles within one debounce cycle
// (the next broadcast updates the value, and the cycle after that uses the
// fresh value). A max taken over a sample window of recent builds would
// instead keep a brief stint on a big namespace visible for many cycles
// after the user switched away.
func topologyDebounceFor(lastBroadcastMaxEstimated int64, cache interface{ GetResourceCount() int }) time.Duration {
	estimated := lastBroadcastMaxEstimated
	if estimated == 0 && cache != nil {
		// No broadcasts recorded yet, or no clients connected during the
		// last cycle. Use raw resource count divided by 5 as a crude proxy —
		// most resources don't become topology nodes (events, secrets,
		// configmaps).
		estimated = int64(cache.GetResourceCount()) / 5
	}
	switch {
	case estimated > 5000:
		return 15 * time.Second
	case estimated > 2000:
		return 5 * time.Second
	case estimated > 500:
		return 2 * time.Second
	default:
		return 1 * time.Second
	}
}

// safeSend sends an event to a channel, recovering from panic if the channel is closed
func safeSend(ch chan SSEEvent, event SSEEvent) {
	defer func() {
		recover() // Ignore panic from send on closed channel
	}()
	select {
	case ch <- event:
	default:
		// Channel full, skip. Counted in perfstats so users can see drops
		// in /api/diagnostics without enabling any flag.
		perfstats.IncSSEDrop()
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
		warmupDone:  make(chan struct{}),
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

// isWarmingUp returns true if the initial warmup phase is still in progress.
func (b *SSEBroadcaster) isWarmingUp() bool {
	b.watchMu.Lock()
	ch := b.warmupDone
	b.watchMu.Unlock()
	select {
	case <-ch:
		return false
	default:
		return true
	}
}

// watchDeferredSync waits for deferred informers (secrets, events, etc.) to
// finish syncing and then broadcasts a topology update + deferred_ready event
// so the UI can fill in the missing data (config edges, event counts, etc.).
// It captures a local copy of watchStopCh so it exits on context switch,
// and is restarted alongside the resource watcher via restartResourceWatcher.
func (b *SSEBroadcaster) watchDeferredSync() {
	b.watchMu.Lock()
	watchStop := b.watchStopCh
	warmupCh := b.warmupDone // capture local copy — context switch may replace the field
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

				// Signal warmup complete — debounce can drop to normal.
				// Close the local copy (not b.warmupDone) so a context switch
				// that replaced the field won't have its new channel closed.
				select {
				case <-warmupCh:
					// Already closed (e.g. context switch race)
				default:
					log.Printf("SSE broadcaster: warmup phase complete, switching to normal debounce")
					close(warmupCh)
				}

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

	resetCacheView := func() {
		// Clear cached topology and dirty flag for the old context
		b.cachedTopologyMu.Lock()
		b.cachedTopology = nil
		b.cachedTopologyDirty = false
		b.cachedTopologyMu.Unlock()

		// Reset warmup phase for the new context (under watchMu to synchronize
		// with isWarmingUp and watchDeferredSync which read this field)
		b.watchMu.Lock()
		b.warmupDone = make(chan struct{})
		b.watchMu.Unlock()

		// Restart the resource change watcher for the new cache
		b.restartResourceWatcher()
	}

	// Register for context switch completion
	k8s.OnContextSwitch(func(newContext string) {
		log.Printf("SSE broadcaster: context switched to %q, clearing cached topology", newContext)
		resetCacheView()

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

	k8s.OnNamespaceRescope(func(namespace string) {
		log.Printf("SSE broadcaster: namespace cache rescoped to %q, clearing cached topology", k8s.SanitizeForLog(namespace))
		resetCacheView()
		log.Printf("SSE broadcaster: scheduling topology broadcast")
		go b.broadcastTopologyUpdate()
	})
}

// initCachedTopology builds the initial topology cache
func (b *SSEBroadcaster) initCachedTopology() {
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	opts := topology.DefaultBuildOptions()
	opts.ViewMode = topology.ViewModeResources
	// Include ReplicaSets in the cache so relationship lookups work for them
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

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
			b.clients[reg.ch] = ClientInfo{Namespaces: reg.namespaces, ViewMode: reg.viewMode, ShowPolicyEffect: reg.showPolicyEffect, DeniedKinds: reg.deniedKinds}
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

	// Debounce strategy:
	// - During warmup: critical informers that didn't make the patience
	//   window (e.g. ingresses, jobs, replicasets) plus deferred informers
	//   plus dynamic CRD informers all stream in over the next 10–60s. We
	//   want the topology graph to settle in a few coherent paints, not
	//   jump on every arrival, so we coalesce into 5s windows. The UI is
	//   already on the home view by this point with a "loading more" hint;
	//   the slight delay is preferable to a fidgety graph.
	// - After warmup: scale debounce by the estimated topology node count
	//   from the most recent builds (the same signal driving the in-builder
	//   large-cluster optimizations). Minimum 1s — even at small scale we
	//   don't need faster topology refreshes than that.
	const warmupDebounce = 5 * time.Second
	b.watchMu.Lock()
	warmupCh := b.warmupDone // local copy under lock; nil-ed after firing to avoid closed-channel spin
	b.watchMu.Unlock()
	warmupComplete := false
	log.Printf("SSE watcher: using %v warmup debounce until initial sync completes", warmupDebounce)

	debounceTimer := time.NewTimer(0)
	<-debounceTimer.C // drain initial timer
	pendingUpdate := false

	for {
		select {
		case <-b.stopCh:
			return

		case <-watchStop:
			return

		case <-warmupCh:
			warmupCh = nil // prevent closed-channel spin on next iteration
			warmupComplete = true
			log.Printf("SSE watcher: warmup complete (%d resources), debounce now dynamic by estimated node count (min 1s)", cache.GetResourceCount())

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
				b.broadcastResourceChange(SSEEvent{
					Event: "k8s_event",
					Data:  eventData,
				}, change.Namespace, change.Kind)
			}

			// Schedule debounced topology update. Re-evaluate debounce on
			// every reset so a cluster that grows past a ladder threshold
			// starts coalescing more aggressively without restart, and so
			// a namespace switch (which changes the active client groups
			// and therefore the next broadcast's max estimate) settles
			// within one debounce cycle.
			if !pendingUpdate {
				dur := warmupDebounce
				if warmupComplete {
					dur = topologyDebounceFor(b.lastBroadcastMaxEstimated.Load(), cache)
				}
				debounceTimer.Reset(dur)
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

	if len(clients) == 0 {
		// No clients — mark the relationship cache as dirty so it gets
		// rebuilt on next GetCachedTopology() call. Skip the expensive build.
		b.cachedTopologyMu.Lock()
		b.cachedTopologyDirty = true
		b.cachedTopologyMu.Unlock()
		// Forget the last cycle's estimate so a future session doesn't inherit
		// a disconnected session's debounce (a small namespace shouldn't keep a
		// big one's 15s cadence). topologyDebounceFor falls back to the resource-
		// count proxy until the next broadcast records a real estimate.
		b.lastBroadcastMaxEstimated.Store(0)
		return
	}

	log.Printf("Broadcasting topology update to %d clients", len(clients))

	// One broadcast cycle = one debounce fire that reaches clients. Counted
	// here (not in the per-group loop below) so the metric reflects cycles,
	// not the number of distinct namespace/view/policy groups.
	perfstats.IncSSEBroadcast()

	// During warmup, skip the expensive full-topology cache build. Nobody is
	// clicking into resource details while the connecting spinner is showing,
	// so the relationship cache isn't needed yet. Mark dirty for lazy rebuild.
	if b.isWarmingUp() {
		b.cachedTopologyMu.Lock()
		b.cachedTopologyDirty = true
		b.cachedTopologyMu.Unlock()
	} else {
		if fullTopo, err := buildFullTopology(); err == nil {
			b.updateCachedTopology(fullTopo)
		} else {
			log.Printf("Error building full topology for cache: %v", err)
		}
	}

	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))

	// Group clients by namespace filter + viewMode
	// Use comma-separated namespaces string as map key since slices aren't comparable
	// Note: namespaces are pre-sorted at subscription time for consistent grouping
	type clientKey struct {
		namespacesKey    string // comma-separated sorted namespaces
		deniedKindsKey   string // comma-separated sorted denied cluster-scoped kinds
		viewMode         string
		showPolicyEffect bool
	}
	type clientGroup struct {
		namespaces       []string
		showPolicyEffect bool
		deniedKinds      map[topology.NodeKind]bool
		channels         []chan SSEEvent
	}
	clientGroups := make(map[clientKey]*clientGroup)
	for ch, info := range clients {
		nsKey := strings.Join(info.Namespaces, ",") // namespaces already sorted at subscribe time
		// Users with identical namespace + cluster-scoped RBAC share one frame;
		// a user denied cluster-scoped kinds gets a distinct, stripped frame
		// rather than the unfiltered bytes of a more-privileged peer.
		key := clientKey{namespacesKey: nsKey, deniedKindsKey: deniedKindsKey(info.DeniedKinds), viewMode: info.ViewMode, showPolicyEffect: info.ShowPolicyEffect}
		if clientGroups[key] == nil {
			clientGroups[key] = &clientGroup{namespaces: info.Namespaces, showPolicyEffect: info.ShowPolicyEffect, deniedKinds: info.DeniedKinds}
		}
		clientGroups[key].channels = append(clientGroups[key].channels, ch)
	}

	// Build topology for each group and send. Pre-marshal once per group so
	// the same bytes go out to every client in the group (the per-client SSE
	// writer would otherwise re-marshal the same large topology N times).
	// Also gives us a single point to record payload bytes and the max
	// estimated node count across active groups — the latter drives the
	// next cycle's debounce ladder.
	var maxEstimated int64
	for key, group := range clientGroups {
		opts := topology.DefaultBuildOptions()
		opts.Namespaces = group.namespaces
		if key.viewMode == "traffic" {
			opts.ViewMode = topology.ViewModeTraffic
		}
		opts.ShowPolicyEffect = group.showPolicyEffect

		topo, err := builder.Build(opts)
		if err != nil {
			log.Printf("Error building topology for broadcast: %v", err)
			continue
		}
		topo.StripNodeKinds(group.deniedKinds)

		if int64(topo.EstimatedNodes) > maxEstimated {
			maxEstimated = int64(topo.EstimatedNodes)
		}

		data, marshalErr := json.Marshal(topo)
		if marshalErr != nil {
			log.Printf("Error marshaling topology for broadcast: %v", marshalErr)
			continue
		}
		perfstats.RecordTopologyPayload(len(data))

		event := SSEEvent{
			Event: "topology",
			Data:  json.RawMessage(data),
		}

		for _, ch := range group.channels {
			safeSend(ch, event)
		}
	}

	// Store the max for the next cycle's debounce decision. Stored even
	// when maxEstimated stayed 0 (eg. every build errored) — that just
	// falls through to the bootstrap proxy in topologyDebounceFor.
	b.lastBroadcastMaxEstimated.Store(maxEstimated)
}

// deniedKindsKey builds a stable grouping key from a denied-kinds set so that
// clients with the same effective cluster-scoped RBAC share a pre-marshaled
// frame. Empty (full access) collapses to "" — the common case.
func deniedKindsKey(deny map[topology.NodeKind]bool) string {
	if len(deny) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(deny))
	for k := range deny {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	return strings.Join(kinds, ",")
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

// premarshalEventData serializes event.Data to json.RawMessage once, before
// fan-out, so the per-client SSE writer emits the same bytes to every connected
// client instead of reflection-marshaling the identical payload N times (once
// per tab). Frames whose Data is already json.RawMessage (the topology path)
// pass through untouched. A marshal error leaves Data as-is — the per-client
// writer then surfaces it via its existing error path.
func premarshalEventData(event SSEEvent) SSEEvent {
	if _, ok := event.Data.(json.RawMessage); ok {
		return event
	}
	data, err := json.Marshal(event.Data)
	if err != nil {
		return event
	}
	event.Data = json.RawMessage(data)
	return event
}

// Broadcast sends an event to all connected clients
func (b *SSEBroadcaster) Broadcast(event SSEEvent) {
	event = premarshalEventData(event)
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.clients {
		safeSend(ch, event)
	}
}

// broadcastResourceChange sends a per-resource change frame (k8s_event, which
// can carry a spec/data diff) only to clients whose RBAC plausibly permits the
// resource. Namespaced changes go only to clients whose RBAC-filtered namespace
// set includes the namespace; cluster-scoped changes go only to clients not
// denied that kind (the topology denied set resolved at subscribe time).
//
// This is a PARTIAL gate, not a complete authorization boundary, and is a big
// reduction over the previous broadcast-to-all (which leaked every diff to every
// client). Two gaps remain, both needing per-(group,resource) state this path
// doesn't carry yet (ResourceChange has only Kind):
//   - namespaced kinds the user can't read WITHIN an allowed namespace (e.g.
//     Secrets/Roles for a list-pods-only viewer) still pass the namespace check;
//   - cluster-scoped kinds outside the topology set (ClusterRole, webhooks,
//     cluster-scoped CRDs) aren't in DeniedKinds, and kind-string matching misses
//     CRD variants (EC2NodeClass vs synthesized NodeClass).
// The complete fix carries the exact GVR on ResourceChange and authorizes each
// client via the cached per-user canRead — tracked separately.
func (b *SSEBroadcaster) broadcastResourceChange(event SSEEvent, namespace, kind string) {
	event = premarshalEventData(event)
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch, info := range b.clients {
		if clientCanSeeChange(info, namespace, kind) {
			safeSend(ch, event)
		}
	}
}

// clientCanSeeChange reports whether a client's RBAC allows a change frame.
func clientCanSeeChange(info ClientInfo, namespace, kind string) bool {
	if namespace != "" {
		// Namespaced change: deliver only if the client can see that namespace.
		// nil Namespaces means all-namespace access (no RBAC restriction).
		if info.Namespaces == nil {
			return true
		}
		return slices.Contains(info.Namespaces, namespace)
	}
	// Cluster-scoped change (no namespace): deliver unless the kind is one this
	// client was denied — the per-kind SAR result resolved at subscribe time.
	return !info.DeniedKinds[topology.NodeKind(kind)]
}

// Subscribe adds a new SSE client. Returns nil if max clients reached.
func (b *SSEBroadcaster) Subscribe(namespaces []string, viewMode string, deniedKinds map[topology.NodeKind]bool, showPolicyEffect ...bool) chan SSEEvent {
	// Check client count before creating the channel to fail fast
	b.mu.RLock()
	clientCount := len(b.clients)
	b.mu.RUnlock()

	if clientCount >= MaxSSEClients {
		log.Printf("SSE subscription rejected: max clients (%d) reached", MaxSSEClients)
		return nil
	}

	// Sort namespaces once at subscription time for consistent grouping during broadcasts.
	// Preserve nil (all namespaces) vs empty slice (no access) — they have different semantics.
	var sortedNs []string
	if namespaces != nil {
		sortedNs = make([]string, len(namespaces))
		copy(sortedNs, namespaces)
		sort.Strings(sortedNs)
	}

	policyEffect := len(showPolicyEffect) > 0 && showPolicyEffect[0]
	ch := make(chan SSEEvent, 10)
	b.register <- clientRegistration{ch: ch, namespaces: sortedNs, viewMode: viewMode, showPolicyEffect: policyEffect, deniedKinds: deniedKinds}
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
// If the cache is dirty (changes occurred with no SSE clients), it rebuilds on demand.
func (b *SSEBroadcaster) GetCachedTopology() *topology.Topology {
	b.cachedTopologyMu.RLock()
	dirty := b.cachedTopologyDirty
	topo := b.cachedTopology
	b.cachedTopologyMu.RUnlock()

	if dirty && k8s.GetResourceCache() != nil {
		// Gate: only one goroutine proceeds to rebuild
		b.cachedTopologyMu.Lock()
		if !b.cachedTopologyDirty {
			topo = b.cachedTopology
			b.cachedTopologyMu.Unlock()
			return topo
		}
		b.cachedTopologyDirty = false
		b.cachedTopologyMu.Unlock()

		if !b.rebuildCachedTopology() {
			// Rebuild failed — mark dirty again so next call retries
			b.cachedTopologyMu.Lock()
			b.cachedTopologyDirty = true
			b.cachedTopologyMu.Unlock()
		}
		b.cachedTopologyMu.RLock()
		topo = b.cachedTopology
		b.cachedTopologyMu.RUnlock()
	}

	return topo
}

// rebuildCachedTopology rebuilds the full topology for relationship lookups.
// Returns true if the rebuild succeeded, false otherwise.
func (b *SSEBroadcaster) rebuildCachedTopology() bool {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return false
	}
	if fullTopo, err := buildFullTopology(); err == nil {
		b.updateCachedTopology(fullTopo)
		return true
	} else {
		log.Printf("Error rebuilding topology cache on demand: %v", err)
		return false
	}
}

// updateCachedTopology stores a full topology for relationship lookups
func (b *SSEBroadcaster) updateCachedTopology(topo *topology.Topology) {
	b.cachedTopologyMu.Lock()
	defer b.cachedTopologyMu.Unlock()
	b.cachedTopology = topo
	b.cachedTopologyDirty = false
}

// buildFullTopology constructs a full topology (all namespaces, resources view)
// for relationship lookups. Used by both broadcast and lazy rebuild paths.
func buildFullTopology() (*topology.Topology, error) {
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	opts := topology.DefaultBuildOptions()
	opts.ViewMode = topology.ViewModeResources
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	return builder.Build(opts)
}

// HandleSSE is the HTTP handler for the SSE endpoint
func (b *SSEBroadcaster) HandleSSE(w http.ResponseWriter, r *http.Request, deniedKinds map[topology.NodeKind]bool) {
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
	policyEffect := r.URL.Query().Get("policyEffect") == "true"

	// Ensure we can flush
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to events
	eventCh := b.Subscribe(namespaces, viewMode, deniedKinds, policyEffect)
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
		builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
		opts := topology.DefaultBuildOptions()
		opts.Namespaces = namespaces
		if viewMode == "traffic" {
			opts.ViewMode = topology.ViewModeTraffic
		}
		opts.ShowPolicyEffect = policyEffect
		if topo, err := builder.Build(opts); err == nil {
			topo.StripNodeKinds(deniedKinds)
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
			// Frames are pre-marshaled to json.RawMessage once before fan-out
			// (premarshalEventData), so for the common case write the shared
			// bytes directly — re-marshaling here would re-serialize the same
			// payload for every connected client. Fall back to marshaling for
			// any frame that wasn't pre-marshaled.
			var data []byte
			var err error
			if raw, ok := event.Data.(json.RawMessage); ok {
				data = raw
			} else {
				data, err = json.Marshal(event.Data)
			}
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
