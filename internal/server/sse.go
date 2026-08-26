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
	// cachedTopologyIndex is the inverted edge index over cachedTopology, built
	// lazily on first relationship lookup and reused across drawer opens until
	// the topology is replaced. Nil whenever cachedTopology changes (set under
	// cachedTopologyMu alongside it). Guarded by cachedTopologyMu.
	cachedTopologyIndex *topology.RelationshipsIndex

	// warmupDone is closed when deferred informers finish syncing. During warmup,
	// topology broadcasts use longer debounce and skip the expensive full-topology
	// cache build. Protected by watchMu (same as watchStopCh).
	warmupDone chan struct{}

	// topoTrigger carries broadcast requests to the topology worker. One slot:
	// a request already waiting subsumes any later one, so a full slot means
	// coalesce, not queue.
	//
	// Keeping the build off the caller is load-bearing. It takes seconds on a
	// large cluster, and it used to run on the sole reader of the resource-change
	// channel — the same goroutine that fans k8s_event frames out to clients. For
	// as long as it built, no change was drained and no live update was
	// delivered; past a few seconds per cycle the channel overflows on top of
	// that and changes are lost outright.
	topoTrigger chan struct{}

	// topoEpoch counts resets of the cluster view (context switch, namespace
	// rescope). A build reads the process-global caches for seconds, so one
	// that started before a reset describes a cluster the UI has already left
	// by the time it finishes. Every build captures the epoch up front and
	// every publish re-checks it, so an in-flight build is discarded rather
	// than shown or cached.
	topoEpoch atomic.Uint64

	// topoEpochAtCycleStart reads the epoch a cycle is built against. A real
	// context switch lands on another goroutine at an arbitrary point inside a
	// build, so injecting the capture is the only deterministic way to exercise
	// a build that spans one.
	topoEpochAtCycleStart func() uint64

	// topoBuild is the cycle the worker runs per trigger. A seam for the same
	// reason: the worker's scheduling (what a trigger arriving mid-build does)
	// is otherwise only observable by running real multi-second builds.
	topoBuild func() bool
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
	// Authorize authorizes a per-resource change frame for this client's user
	// via SubjectAccessReview, memoized in a connection-lived TTL cache
	// (context-scoped, independent of the shared permission cache) so a
	// long-lived stream doesn't re-SAR every frame. Bound at subscribe time to
	// the request's user + a connection-lived context, so the broadcast
	// goroutine can gate diff-bearing k8s_event frames per kind without holding a
	// request. nil when no authorizer was wired (defensive / tests) —
	// clientCanSeeChange then falls back to the namespace + denied-kind gate.
	// When auth is disabled the closure is still set and returns true.
	Authorize func(group, resource, namespace, verb string) bool
}

type clientRegistration struct {
	ch               chan SSEEvent
	namespaces       []string
	viewMode         string
	showPolicyEffect bool
	deniedKinds      map[topology.NodeKind]bool
	authorize        func(group, resource, namespace, verb string) bool
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
	b := &SSEBroadcaster{
		clients:     make(map[chan SSEEvent]ClientInfo),
		register:    make(chan clientRegistration),
		unregister:  make(chan chan SSEEvent),
		stopCh:      make(chan struct{}),
		watchStopCh: make(chan struct{}),
		warmupDone:  make(chan struct{}),
		topoTrigger: make(chan struct{}, 1),
	}
	b.topoEpochAtCycleStart = b.topoEpoch.Load
	b.topoBuild = b.broadcastTopologyUpdate
	return b
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
	go b.topologyWorker()
	go b.watchResourceChanges()
	go b.watchDeferredSync()
	go b.heartbeat()
}

// requestTopologyBroadcast asks the topology worker to run a broadcast cycle.
// Never blocks and never runs the build on the caller's goroutine.
func (b *SSEBroadcaster) requestTopologyBroadcast() {
	select {
	case b.topoTrigger <- struct{}{}:
	default:
		// A request is already queued; it will pick up the current state.
		perfstats.IncSSECoalesced()
	}
}

// topologyRetryDelay is how long the worker waits before re-arming a trigger it
// consumed while the cluster view was torn down. Reconnect and context switch
// both queue their own trigger, so this only covers the window where neither
// fires — but the token it consumed stood for every request that coalesced into
// it, so dropping it silently strands the graph until the next cluster change.
const topologyRetryDelay = 2 * time.Second

// topologyWorker owns every broadcast build, so concurrent triggers (debounce
// fire, context switch, CRD discovery) coalesce into a single build instead of
// racing several multi-second ones.
func (b *SSEBroadcaster) topologyWorker() {
	for {
		select {
		case <-b.stopCh:
			return
		case <-b.topoTrigger:
			if b.topoBuild() || b.ClientCount() == 0 {
				continue
			}
			select {
			case <-b.stopCh:
				return
			case <-time.After(topologyRetryDelay):
				perfstats.IncSSERetry()
				b.requestTopologyBroadcast()
			}
		}
	}
}

// registerCRDDiscoveryCallback registers for CRD discovery completion
// When discovery completes, broadcast topology to update the discovery status in UI
func (b *SSEBroadcaster) registerCRDDiscoveryCallback() {
	k8s.OnCRDDiscoveryComplete(func() {
		log.Printf("SSE broadcaster: CRD discovery complete, broadcasting topology update")
		b.requestTopologyBroadcast()
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
				b.requestTopologyBroadcast()

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
			b.requestTopologyBroadcast()
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
		// Bump before clearing: a build already in flight against the previous
		// cluster must fail its epoch check no matter where it is, including
		// between this clear and its own write.
		b.topoEpoch.Add(1)

		// Clear cached topology and dirty flag for the old context
		b.cachedTopologyMu.Lock()
		b.cachedTopology = nil
		b.cachedTopologyIndex = nil
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

		// Broadcast the new topology so clients can complete the switch.
		// Handed to the worker so the context switch isn't blocked on a build.
		log.Printf("SSE broadcaster: scheduling topology broadcast")
		b.requestTopologyBroadcast()
	})

	k8s.OnNamespaceRescope(func(namespace string) {
		log.Printf("SSE broadcaster: namespace cache rescoped to %q, clearing cached topology", k8s.SanitizeForLog(namespace))
		resetCacheView()
		log.Printf("SSE broadcaster: scheduling topology broadcast")
		b.requestTopologyBroadcast()
	})
}

// initCachedTopology builds the initial topology cache
func (b *SSEBroadcaster) initCachedTopology() {
	epoch := b.topoEpoch.Load()
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	opts := topology.DefaultBuildOptions()
	opts.ViewMode = topology.ViewModeResources
	// Include ReplicaSets in the cache so relationship lookups work for them
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true

	if topo, err := builder.Build(opts); err == nil {
		if b.updateCachedTopology(topo, epoch) {
			log.Printf("Initialized topology cache with %d nodes and %d edges", len(topo.Nodes), len(topo.Edges))
		}
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
			b.clients[reg.ch] = ClientInfo{Namespaces: reg.namespaces, ViewMode: reg.viewMode, ShowPolicyEffect: reg.showPolicyEffect, DeniedKinds: reg.deniedKinds, Authorize: reg.authorize}
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
			// Sampled here rather than at the producer: this is the only point
			// that sees the queue as the consumer left it, which is what says
			// whether the consumer is keeping up.
			perfstats.RecordChangeReceived(len(changes), cap(changes))

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
				// Resolve the GVR for per-kind authorization. The dynamic cache
				// stamps the exact GVR on the change (disambiguates CRD kind
				// collisions); the typed cache leaves it empty, so resolve from
				// Kind — unambiguous for the well-known typed kinds.
				group, resource := change.Group, change.Resource
				namespace := change.Namespace
				if resource == "" {
					if g, r, clusterScoped, ok := k8s.ResolveChangeGVR(change.Kind, change.Group); ok {
						group, resource = g, r
						if clusterScoped {
							namespace = ""
						}
					}
				}
				b.broadcastResourceChange(SSEEvent{
					Event: "k8s_event",
					Data:  eventData,
				}, namespace, group, resource, change.Kind)
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
				perfstats.SetSSEDebounce(dur)
				debounceTimer.Reset(dur)
				pendingUpdate = true
			}

		case <-debounceTimer.C:
			if pendingUpdate {
				pendingUpdate = false
				b.requestTopologyBroadcast()
			}
		}
	}
}

// broadcastTopologyUpdate sends the current topology to all clients. Reports
// false when the cluster view was torn down and nothing could be built, so the
// worker can re-arm the trigger it consumed instead of dropping it.
func (b *SSEBroadcaster) broadcastTopologyUpdate() bool {
	// Skip if resource cache is torn down (e.g. during context switch).
	if k8s.GetResourceCache() == nil {
		return false
	}

	// The cluster this cycle describes. A switch landing mid-cycle re-triggers
	// the worker, so abandoning here costs nothing and keeps the previous
	// cluster's graph off the wire.
	epoch := b.topoEpochAtCycleStart()

	b.mu.RLock()
	clients := make(map[chan SSEEvent]ClientInfo, len(b.clients))
	maps.Copy(clients, b.clients)
	b.mu.RUnlock()

	if len(clients) == 0 {
		// No clients — mark the relationship cache as dirty so it gets
		// rebuilt on next GetCachedTopology() call. Skip the expensive build.
		b.markCachedTopologyDirty()
		// Forget the last cycle's estimate so a future session doesn't inherit
		// a disconnected session's debounce (a small namespace shouldn't keep a
		// big one's 15s cadence). topologyDebounceFor falls back to the resource-
		// count proxy until the next broadcast records a real estimate.
		b.lastBroadcastMaxEstimated.Store(0)
		return true
	}

	// Clock starts past the no-clients exit: that path does no work, and
	// recording it would bury real cycles under zero-duration samples.
	// Recorded from a defer so the epoch checks below — which return after the
	// full build, and again after each group's build and marshal — report the
	// wall time they spent rather than looking like they never ran.
	cycleStart := time.Now()
	var clientGroupCount, authGroupCount int
	var marshalTotal time.Duration
	defer func() {
		perfstats.RecordBroadcastCycle(time.Since(cycleStart), clientGroupCount, authGroupCount, marshalTotal)
	}()

	// Checked before the full-topology build, which is the most expensive one
	// in the cycle (every namespace, ReplicaSets included): a switch that has
	// already landed makes it wrong before it costs anything, and the reset
	// queued its own trigger to replace it.
	if b.topoEpoch.Load() != epoch {
		return b.abandonCycleForNewCluster()
	}

	// During warmup, skip the expensive full-topology cache build. Nobody is
	// clicking into resource details while the connecting spinner is showing,
	// so the relationship cache isn't needed yet. Mark dirty for lazy rebuild.
	if b.isWarmingUp() {
		b.markCachedTopologyDirty()
	} else {
		if fullTopo, err := buildFullTopology(); err == nil {
			// A rejected write leaves the new cluster with nothing cached, so
			// leave the cache dirty for the next reader to rebuild — the same
			// rule the on-demand rebuild path follows.
			if !b.updateCachedTopology(fullTopo, epoch) {
				b.markCachedTopologyDirty()
			}
		} else {
			log.Printf("Error building full topology for cache: %v", err)
		}
	}

	if b.topoEpoch.Load() != epoch {
		return b.abandonCycleForNewCluster()
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
		clients          map[chan SSEEvent]ClientInfo
	}
	clientGroups := make(map[clientKey]*clientGroup)
	for ch, info := range clients {
		nsKey := strings.Join(info.Namespaces, ",") // namespaces already sorted at subscribe time
		// Users with identical namespace + cluster-scoped RBAC share one frame;
		// a user denied cluster-scoped kinds gets a distinct, stripped frame
		// rather than the unfiltered bytes of a more-privileged peer.
		key := clientKey{namespacesKey: nsKey, deniedKindsKey: deniedKindsKey(info.DeniedKinds), viewMode: info.ViewMode, showPolicyEffect: info.ShowPolicyEffect}
		if clientGroups[key] == nil {
			clientGroups[key] = &clientGroup{namespaces: info.Namespaces, showPolicyEffect: info.ShowPolicyEffect, deniedKinds: info.DeniedKinds, clients: make(map[chan SSEEvent]ClientInfo)}
		}
		clientGroups[key].clients[ch] = info
	}
	clientGroupCount = len(clientGroups)

	// Build topology for each group and send. Pre-marshal once per group so
	// the same bytes go out to every client in the group (the per-client SSE
	// writer would otherwise re-marshal the same large topology N times).
	// Also gives us a single point to record payload bytes and the max
	// estimated node count across active groups — the latter drives the
	// next cycle's debounce ladder.
	var maxEstimated int64
	published := false
	for key, group := range clientGroups {
		// Re-checked per group, not just once: a cycle with many groups is
		// many builds long, and every one after the switch is both wrong and
		// time the new cluster's build spends waiting for this one to finish.
		if b.topoEpoch.Load() != epoch {
			return b.abandonCycleForNewCluster()
		}

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
		if b.topoEpoch.Load() != epoch {
			return b.abandonCycleForNewCluster()
		}
		topo.StripNodeKinds(group.deniedKinds)

		if int64(topo.EstimatedNodes) > maxEstimated {
			maxEstimated = int64(topo.EstimatedNodes)
		}

		// A synthesized NodeClass kind, cluster-scoped Crossplane XR/MR nodes,
		// and Calico policy nodes each contain independently authorized provider
		// APIs. Regroup against the exact tuples present in this build, then
		// marshal once per effective provider permission set.
		type nodeClassGroup struct {
			allowed        map[topology.SARTuple]bool
			allowedDynamic map[topology.SARTuple]bool
			allowedCalico  map[topology.SARTuple]bool
			channels       []chan SSEEvent
		}
		nodeClassGroups := make(map[string]*nodeClassGroup)
		for ch, info := range group.clients {
			authorize := nodeClassAuthorizer(info.Authorize)
			allowed := authorizedNodeClassTuples(topo, authorize)
			allowedDynamic := authorizedClusterScopedDynamicTuples(topo, authorize)
			allowedCalico := authorizedCalicoPolicyTuples(topo, info.Authorize)
			authKey := nodeClassTuplesKey(allowed) + "\x02" + nodeClassTuplesKey(allowedDynamic) + "\x03" + calicoTuplesKey(allowedCalico)
			if nodeClassGroups[authKey] == nil {
				nodeClassGroups[authKey] = &nodeClassGroup{allowed: allowed, allowedDynamic: allowedDynamic, allowedCalico: allowedCalico}
			}
			nodeClassGroups[authKey].channels = append(nodeClassGroups[authKey].channels, ch)
		}
		authGroupCount += len(nodeClassGroups)
		for _, authGroup := range nodeClassGroups {
			filtered := cloneTopology(topo)
			filtered.StripNodeClassesExcept(authGroup.allowed)
			filtered.StripClusterScopedDynamicExcept(authGroup.allowedDynamic)
			filtered.StripCalicoPoliciesExcept(authGroup.allowedCalico)
			marshalStart := time.Now()
			data, marshalErr := json.Marshal(filtered)
			marshalTotal += time.Since(marshalStart)
			if marshalErr != nil {
				log.Printf("Error marshaling topology for broadcast: %v", marshalErr)
				continue
			}
			perfstats.RecordTopologyPayload(len(data))
			event := SSEEvent{Event: "topology", Data: json.RawMessage(data)}
			for _, ch := range authGroup.channels {
				safeSend(ch, event)
			}
			published = true
		}
	}

	// One broadcast cycle = one debounce fire that reached clients. Counted on
	// the way out rather than on entry so a cycle abandoned for the previous
	// cluster, or one where every group's build errored, isn't reported as a
	// broadcast that happened.
	if published {
		perfstats.IncSSEBroadcast()
	}

	// Store the max for the next cycle's debounce decision. Stored even
	// when maxEstimated stayed 0 (eg. every build errored) — that just
	// falls through to the bootstrap proxy in topologyDebounceFor.
	b.lastBroadcastMaxEstimated.Store(maxEstimated)
	return true
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

// nodeClassAuthorizer narrows the per-kind change authorizer to the exact
// provider resource carried by a NodeClass topology node. NodeClass kinds are
// cluster-scoped and unbounded (one CRD per provider), so the gate must ask
// about the node's own group/resource rather than a finite kind table. A nil
// authorizer fails closed: an unwired caller must not widen NodeClass
// visibility.
func nodeClassAuthorizer(authorize func(group, resource, namespace, verb string) bool) func(topology.SARTuple) bool {
	return func(tuple topology.SARTuple) bool {
		if authorize == nil {
			return false
		}
		return authorize(tuple.Group, tuple.Resource, "", "list")
	}
}

func authorizedNodeClassTuples(topo *topology.Topology, authorize func(topology.SARTuple) bool) map[topology.SARTuple]bool {
	allowed := make(map[topology.SARTuple]bool)
	if authorize == nil {
		return allowed
	}
	for _, tuple := range topo.NodeClassRBACTuples() {
		if authorize(tuple) {
			allowed[tuple] = true
		}
	}
	return allowed
}

func authorizedClusterScopedDynamicTuples(topo *topology.Topology, authorize func(topology.SARTuple) bool) map[topology.SARTuple]bool {
	allowed := make(map[topology.SARTuple]bool)
	if authorize == nil {
		return allowed
	}
	for _, tuple := range topo.ClusterScopedDynamicRBACTuples() {
		if authorize(tuple) {
			allowed[tuple] = true
		}
	}
	return allowed
}

// authorizedCalicoPolicyTuples applies the client's exact API-group and
// namespace authorization to the Calico policy identities present in a
// topology. A missing authorizer authorizes nothing, matching how the NodeClass
// and cluster-scoped dynamic filters treat it.
func authorizedCalicoPolicyTuples(topo *topology.Topology, authorize func(group, resource, namespace, verb string) bool) map[topology.SARTuple]bool {
	allowed := make(map[topology.SARTuple]bool)
	if topo == nil || authorize == nil {
		return allowed
	}
	for _, tuple := range topo.CalicoPolicyRBACTuples() {
		if authorize(tuple.Group, tuple.Resource, tuple.Namespace, "list") {
			allowed[tuple] = true
		}
	}
	return allowed
}

func calicoTuplesKey(tuples map[topology.SARTuple]bool) string {
	if len(tuples) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tuples))
	for tuple := range tuples {
		keys = append(keys, tuple.Group+"\x00"+tuple.Resource+"\x00"+tuple.Namespace)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x01")
}

func nodeClassTuplesKey(tuples map[topology.SARTuple]bool) string {
	if len(tuples) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tuples))
	for tuple := range tuples {
		keys = append(keys, tuple.Group+"\x00"+tuple.Resource)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\x01")
}

func cloneTopology(topo *topology.Topology) *topology.Topology {
	if topo == nil {
		return nil
	}
	clone := *topo
	clone.Nodes = append([]topology.Node(nil), topo.Nodes...)
	clone.Edges = append([]topology.Edge(nil), topo.Edges...)
	return &clone
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
//
// The group/resource come from the change's GVR (dynamic cache) or are resolved
// from its Kind (typed cache); an empty resource means the kind couldn't be
// resolved and the frame fails closed for authenticated clients.
//
// Clients are snapshotted under the lock, then authorized + sent WITHOUT it: an
// authorization can miss the per-user memo and do a SAR round-trip, and holding
// b.mu across that would stall registrations and other broadcasts. safeSend
// tolerates a channel closed by a concurrent Unsubscribe after the snapshot.
func (b *SSEBroadcaster) broadcastResourceChange(event SSEEvent, namespace, group, resource, kind string) {
	event = premarshalEventData(event)

	type target struct {
		ch   chan SSEEvent
		info ClientInfo
	}
	b.mu.RLock()
	targets := make([]target, 0, len(b.clients))
	for ch, info := range b.clients {
		targets = append(targets, target{ch: ch, info: info})
	}
	b.mu.RUnlock()

	for _, t := range targets {
		if clientCanSeeChange(t.info, namespace, group, resource, kind) {
			safeSend(t.ch, event)
		}
	}
}

// clientCanSeeChange reports whether a client's RBAC allows a change frame for
// the given (namespace, group, resource, kind).
func clientCanSeeChange(info ClientInfo, namespace, group, resource, kind string) bool {
	if info.Authorize == nil {
		// No authorizer wired (tests / defensive): fall back to the namespace +
		// topology-denied-kind gate.
		if namespace != "" {
			return info.Namespaces == nil || slices.Contains(info.Namespaces, namespace)
		}
		return !info.DeniedKinds[topology.NodeKind(kind)]
	}

	if namespace != "" {
		// Namespaced change: must see the namespace (nil = all) AND hold read on
		// this kind within it.
		if info.Namespaces != nil && !slices.Contains(info.Namespaces, namespace) {
			return false
		}
		if resource == "" {
			return false // unresolved kind under auth — fail closed
		}
		return info.Authorize(group, resource, namespace, "list")
	}

	// Cluster-scoped change (no namespace): require the cluster-scoped read.
	if resource == "" {
		return false
	}
	return info.Authorize(group, resource, "", "list")
}

// Subscribe adds a new SSE client. Returns nil if max clients reached.
// authorize gates per-kind change frames for the client's user (may be nil in
// tests / when no authorizer is wired).
func (b *SSEBroadcaster) Subscribe(namespaces []string, viewMode string, deniedKinds map[topology.NodeKind]bool, authorize func(group, resource, namespace, verb string) bool, showPolicyEffect ...bool) chan SSEEvent {
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
	b.register <- clientRegistration{ch: ch, namespaces: sortedNs, viewMode: viewMode, showPolicyEffect: policyEffect, deniedKinds: deniedKinds, authorize: authorize}
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

// GetCachedTopologyWithIndex returns the cached topology together with its
// inverted edge index, building (and memoizing) the index on first use after a
// topology refresh. Relationship lookups that pass the index skip the O(edges)
// scan that edgesForNode/walkTopmostOwner otherwise do per call, so reusing one
// index across drawer opens turns repeated O(E) work into O(in-degree) per lookup.
//
// The returned (topo, index) pair is always consistent: the index is built from
// the exact topo returned. When the topology is replaced between the two reads,
// a fresh index is built for the returned topo without polluting the cache.
func (b *SSEBroadcaster) GetCachedTopologyWithIndex() (*topology.Topology, *topology.RelationshipsIndex) {
	topo := b.GetCachedTopology() // handles lazy rebuild when dirty
	if topo == nil {
		return nil, nil
	}

	b.cachedTopologyMu.RLock()
	idx := b.cachedTopologyIndex
	current := b.cachedTopology
	b.cachedTopologyMu.RUnlock()
	if idx != nil && current == topo {
		return topo, idx
	}

	// Build outside the lock — IndexByResource is O(edges).
	indexStart := time.Now()
	built := topology.IndexByResource(topo)
	perfstats.RecordRelationshipIndex(time.Since(indexStart))
	b.cachedTopologyMu.Lock()
	if b.cachedTopology == topo {
		b.cachedTopologyIndex = built
	}
	b.cachedTopologyMu.Unlock()
	return topo, built
}

// rebuildCachedTopology rebuilds the full topology for relationship lookups.
// Returns true if the rebuild succeeded, false otherwise.
func (b *SSEBroadcaster) rebuildCachedTopology() bool {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return false
	}
	// Only reached from GetCachedTopology's dirty path, i.e. on whichever
	// request goroutine happened to read next. A full build here blocks that
	// request for its entire duration.
	rebuildStart := time.Now()
	defer func() { perfstats.RecordRelationshipRebuild(time.Since(rebuildStart)) }()

	epoch := b.topoEpoch.Load()
	if fullTopo, err := buildFullTopology(); err == nil {
		// A rejected write leaves nothing cached for the new cluster, so the
		// caller must re-dirty and let the next read rebuild against it.
		return b.updateCachedTopology(fullTopo, epoch)
	} else {
		log.Printf("Error rebuilding topology cache on demand: %v", err)
		return false
	}
}

// abandonCycleForNewCluster drops a cycle whose cluster the UI has already left.
// The relationship cache is flagged on the way out: whatever it holds was built
// for the previous cluster, and the reset that bumped the epoch left it empty
// and clean, which a reader would otherwise take as "this cluster has no
// topology". Reports the cycle as run — the reset queued its own trigger, so
// there is nothing for the worker to re-arm.
func (b *SSEBroadcaster) abandonCycleForNewCluster() bool {
	perfstats.IncSSEAbandoned()
	b.markCachedTopologyDirty()
	return true
}

// markCachedTopologyDirty flags the relationship cache for rebuild on the next
// read. Used wherever a cycle declines to store a graph — no clients, warmup, or
// a build rejected for the previous cluster — so a reader rebuilds rather than
// being told an empty cache is current.
func (b *SSEBroadcaster) markCachedTopologyDirty() {
	b.cachedTopologyMu.Lock()
	b.cachedTopologyDirty = true
	b.cachedTopologyMu.Unlock()
}

// updateCachedTopology stores a full topology for relationship lookups, unless
// the cluster view was reset while it was being built. Reports whether it
// stored.
func (b *SSEBroadcaster) updateCachedTopology(topo *topology.Topology, epoch uint64) bool {
	b.cachedTopologyMu.Lock()
	defer b.cachedTopologyMu.Unlock()
	if b.topoEpoch.Load() != epoch {
		return false
	}
	b.cachedTopology = topo
	b.cachedTopologyIndex = nil // rebuilt lazily on next relationship lookup
	b.cachedTopologyDirty = false
	return true
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
func (b *SSEBroadcaster) HandleSSE(w http.ResponseWriter, r *http.Request, deniedKinds map[topology.NodeKind]bool, authorize func(group, resource, namespace, verb string) bool) {
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
	eventCh := b.Subscribe(namespaces, viewMode, deniedKinds, authorize, policyEffect)
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
		// Same rule as the broadcast cycle: this build reads the caches for as
		// long as any other, so a switch landing inside it would otherwise make
		// the previous cluster's graph this client's first frame.
		epoch := b.topoEpoch.Load()
		builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
		opts := topology.DefaultBuildOptions()
		opts.Namespaces = namespaces
		if viewMode == "traffic" {
			opts.ViewMode = topology.ViewModeTraffic
		}
		opts.ShowPolicyEffect = policyEffect
		if topo, err := builder.Build(opts); err == nil && b.topoEpoch.Load() == epoch {
			topo.StripNodeKinds(deniedKinds)
			topo.StripNodeClassesExcept(authorizedNodeClassTuples(topo, nodeClassAuthorizer(authorize)))
			topo.StripClusterScopedDynamicExcept(authorizedClusterScopedDynamicTuples(topo, nodeClassAuthorizer(authorize)))
			topo.StripCalicoPoliciesExcept(authorizedCalicoPolicyTuples(topo, authorize))
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
