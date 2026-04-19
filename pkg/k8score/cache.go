package k8score

import (
	"fmt"
	"log"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// ResourceCache provides fast, eventually-consistent access to K8s resources
// using SharedInformers. It is the shared core used by both Radar and
// skyhook-connector.
type ResourceCache struct {
	factory          informers.SharedInformerFactory
	changes          chan ResourceChange
	stopCh           chan struct{}
	stopOnce         sync.Once
	enabledResources map[string]bool
	deferredSynced   map[string]bool
	deferredMu       sync.RWMutex
	deferredDone     chan struct{}
	deferredFailed   atomic.Bool // true if WaitForCacheSync failed for deferred informers
	syncComplete     atomic.Bool
	config           CacheConfig
	stdlog           *log.Logger

	// Per-informer sync tracking for diagnostics
	informerStatuses []InformerSyncStatus
	informerMu       sync.RWMutex
	promotedKinds    []string // set when SyncTimeout fires; empty on normal sync
	syncStartTime    time.Time
}

// InformerSyncStatus tracks the sync state of a single informer.
type InformerSyncStatus struct {
	Kind     string `json:"kind"`
	Key      string `json:"key"`      // e.g. "pods", "deployments"
	Deferred bool   `json:"deferred"` // true if deferred (non-critical)
	Synced   bool   `json:"synced"`
	SyncedAt string `json:"syncedAt,omitempty"` // RFC3339 timestamp
	Items    int    `json:"items"`              // current item count in lister
}

// SyncPhase describes the current phase of cache initialization.
type SyncPhase string

const (
	SyncPhaseNotStarted SyncPhase = "not_started"
	SyncPhaseCritical   SyncPhase = "syncing_critical"
	SyncPhaseDeferred   SyncPhase = "syncing_deferred"
	SyncPhaseComplete   SyncPhase = "complete"
)

// CacheSyncStatus is the overall sync status exposed for diagnostics.
type CacheSyncStatus struct {
	Phase             SyncPhase            `json:"phase"`
	SyncStarted       string               `json:"syncStarted,omitempty"` // RFC3339
	ElapsedSec        float64              `json:"elapsedSec"`
	CriticalTotal     int                  `json:"criticalTotal"`
	CriticalSynced    int                  `json:"criticalSynced"`
	DeferredTotal     int                  `json:"deferredTotal"`
	DeferredSynced    int                  `json:"deferredSynced"`
	Informers         []InformerSyncStatus `json:"informers"`
	PendingCritical   []string             `json:"pendingCritical,omitempty"`   // kinds not yet synced
	PendingDeferred   []string             `json:"pendingDeferred,omitempty"`
	PromotedKinds     []string             `json:"promotedKinds,omitempty"`    // critical informers that timed out
}

type informerSetup struct {
	key     string
	kind    string
	setup   func() cache.SharedIndexInformer
	isEvent bool
}

// NewResourceCache creates and starts a ResourceCache from the given config.
// Startup has three tiers:
//   - Critical informers block until synced (or SyncTimeout, then promoted to deferred)
//   - Deferred informers sync in the background after critical completes
//   - Background informers (e.g. Events) sync independently on their own goroutine
func NewResourceCache(cfg CacheConfig) (*ResourceCache, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("CacheConfig.Client must not be nil")
	}
	if cfg.NamespaceScoped && cfg.Namespace == "" {
		return nil, fmt.Errorf("CacheConfig.Namespace must be set when NamespaceScoped is true")
	}

	channelSize := cfg.ChannelSize
	if channelSize <= 0 {
		channelSize = 10000
	}

	logf := cfg.TimingLogger
	if logf == nil {
		logf = func(string, ...any) {} // no-op
	}

	stdlog := cfg.Logger
	if stdlog == nil {
		stdlog = log.Default()
	}

	// Clone caller-owned maps to prevent mutation after construction.
	cfg.ResourceTypes = maps.Clone(cfg.ResourceTypes)
	cfg.DeferredTypes = maps.Clone(cfg.DeferredTypes)

	stopCh := make(chan struct{})
	changes := make(chan ResourceChange, channelSize)

	// Build factory options
	factoryOpts := []informers.SharedInformerOption{
		informers.WithTransform(DropManagedFields),
	}
	if cfg.NamespaceScoped {
		factoryOpts = append(factoryOpts, informers.WithNamespace(cfg.Namespace))
		stdlog.Printf("Using namespace-scoped informers for namespace %q", cfg.Namespace)
	}

	// Factory serves as informer registry and shutdown coordinator.
	// We don't call factory.Start() — informers are Run() individually
	// to stagger critical vs deferred starts. Shutdown() still works
	// because each informer's factory getter (e.g. factory.Core().V1().Pods().Informer())
	// registers it internally when called in the setup loop below.
	factory := informers.NewSharedInformerFactoryWithOptions(
		cfg.Client,
		0, // no resync — updates come via watch
		factoryOpts...,
	)

	// Table-driven informer setup — only create informers for enabled types
	setups := buildInformerSetups(factory)

	enabled := cfg.ResourceTypes
	deferredTypes := cfg.DeferredTypes
	if deferredTypes == nil {
		deferredTypes = map[string]bool{}
	}

	var criticalSyncFuncs []cache.InformerSynced
	var deferredSyncFuncs []cache.InformerSynced
	var deferredKeys []string
	var backgroundSyncFuncs []cache.InformerSynced // Events — sync independently, don't block deferredDone
	var backgroundKeys []string
	enabledCount := 0

	rc := &ResourceCache{
		factory:          factory,
		changes:          changes,
		stopCh:           stopCh,
		enabledResources: enabled,
		config:           cfg,
		stdlog:           stdlog,
	}

	// Track per-informer sync status, HasSynced funcs, and the informer
	// handle (needed for staggered start).
	type informerEntry struct {
		kind     string
		key      string
		deferred bool
		synced   cache.InformerSynced
		informer cache.SharedIndexInformer
	}
	var allEntries []informerEntry

	for _, s := range setups {
		if !enabled[s.key] {
			continue
		}
		enabledCount++
		inf := s.setup()

		var err error
		if s.isEvent {
			err = rc.addEventHandlers(inf, changes)
		} else {
			err = rc.addChangeHandlers(inf, s.kind, changes)
		}
		if err != nil {
			close(stopCh)
			return nil, fmt.Errorf("failed to register %s event handler: %w", s.kind, err)
		}

		isDeferred := deferredTypes[s.key]
		entry := informerEntry{kind: s.kind, key: s.key, deferred: isDeferred, synced: inf.HasSynced, informer: inf}
		allEntries = append(allEntries, entry)

		if isDeferred && s.isEvent {
			// Events sync independently — they can take 60s+ on large clusters
			// and shouldn't block topology completion or warmup transition.
			backgroundSyncFuncs = append(backgroundSyncFuncs, inf.HasSynced)
			backgroundKeys = append(backgroundKeys, s.key)
		} else if isDeferred {
			deferredSyncFuncs = append(deferredSyncFuncs, inf.HasSynced)
			deferredKeys = append(deferredKeys, s.key)
		} else {
			criticalSyncFuncs = append(criticalSyncFuncs, inf.HasSynced)
		}
	}

	// Initialize per-informer tracking
	statuses := make([]InformerSyncStatus, len(allEntries))
	for i, e := range allEntries {
		statuses[i] = InformerSyncStatus{Kind: e.kind, Key: e.key, Deferred: e.deferred}
	}
	rc.informerStatuses = statuses

	if enabledCount == 0 {
		stdlog.Printf("Warning: No resource types are accessible (all RBAC checks failed)")
		rc.deferredSynced = make(map[string]bool)
		rc.deferredDone = make(chan struct{})
		close(rc.deferredDone)
		rc.syncComplete.Store(true)
		return rc, nil
	}

	// Start critical informers first. Deferred informers are started after
	// Phase 1 completes to reduce concurrent LIST pressure on the API server.
	// On large clusters (300+ nodes), ~10 concurrent LISTs is significantly
	// lighter than ~19, giving the heaviest resource (Pods) more API server
	// bandwidth during the critical path.
	for _, e := range allEntries {
		if !e.deferred {
			go e.informer.Run(stopCh)
		}
	}

	if len(backgroundKeys) > 0 {
		stdlog.Printf("Starting resource cache: %d critical + %d deferred + %d background informers (%d total)",
			len(criticalSyncFuncs), len(deferredSyncFuncs), len(backgroundSyncFuncs), enabledCount)
	} else {
		stdlog.Printf("Starting resource cache: %d critical + %d deferred informers (%d total, deferred start after critical sync)",
			len(criticalSyncFuncs), len(deferredSyncFuncs), enabledCount)
	}
	syncStart := time.Now()
	rc.syncStartTime = syncStart

	// Track per-informer sync completion in background goroutines.
	// Each goroutine updates the InformerSyncStatus when its informer syncs.
	for i, e := range allEntries {
		idx := i
		entry := e
		go func() {
			t := time.Now()
			for !entry.synced() {
				select {
				case <-stopCh:
					return
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			tag := "critical"
			if entry.deferred {
				tag = "deferred"
			}
			logf("    Informer synced: %-28s %v (%s)", entry.kind, time.Since(t), tag)
			rc.informerMu.Lock()
			rc.informerStatuses[idx].Synced = true
			rc.informerStatuses[idx].SyncedAt = time.Now().Format(time.RFC3339)
			rc.informerMu.Unlock()
		}()
	}

	// Phase 1: Wait for critical informers with periodic progress logging.
	// Log every 5s so stuck syncs are visible in logs (previously silent).
	timedOut := false
	if len(criticalSyncFuncs) > 0 {
		progressTicker := time.NewTicker(5 * time.Second)
		defer progressTicker.Stop()

		var deadlineCh <-chan time.Time
		if cfg.SyncTimeout > 0 {
			deadline := time.NewTimer(cfg.SyncTimeout)
			defer deadline.Stop()
			deadlineCh = deadline.C
		}

		for {
			// Check if all critical informers are synced
			allSynced := true
			for _, fn := range criticalSyncFuncs {
				if !fn() {
					allSynced = false
					break
				}
			}
			if allSynced {
				break
			}

			// Wait for either: stopCh, deadline, or next progress tick
			select {
			case <-stopCh:
				return nil, fmt.Errorf("failed to sync critical resource caches")
			case <-deadlineCh:
				timedOut = true
			case <-progressTicker.C:
				// Log which informers are still pending, with item counts
				counts := rc.GetKindObjectCounts()
				rc.informerMu.RLock()
				var synced, pendingParts []string
				for _, s := range rc.informerStatuses {
					if s.Deferred {
						continue
					}
					if s.Synced {
						synced = append(synced, s.Kind)
					} else {
						n := counts[s.Kind]
						pendingParts = append(pendingParts, fmt.Sprintf("%s(%d)", s.Kind, n))
					}
				}
				rc.informerMu.RUnlock()
				stdlog.Printf("Critical sync progress: %d/%d synced (%.0fs elapsed) — pending: %s",
					len(synced), len(synced)+len(pendingParts), time.Since(syncStart).Seconds(), strings.Join(pendingParts, ", "))
			default:
				time.Sleep(100 * time.Millisecond)
			}
			if timedOut {
				break
			}
		}
	}

	if timedOut {
		// Promote unsynced critical informers to deferred so they continue
		// syncing in the background. The UI renders with partial data.
		var promoted []string
		rc.informerMu.Lock()
		for i, e := range allEntries {
			if e.deferred || e.synced() {
				continue
			}
			promoted = append(promoted, e.kind)
			// Add to deferred tracking (the informer is already running from Phase 1)
			deferredSyncFuncs = append(deferredSyncFuncs, e.synced)
			deferredKeys = append(deferredKeys, e.key)
			// Mark as deferred in diagnostics
			rc.informerStatuses[i].Deferred = true
		}
		rc.informerMu.Unlock()
		stdlog.Printf("WARNING: Critical sync timed out after %v — promoting %d informers to deferred: %s",
			cfg.SyncTimeout, len(promoted), strings.Join(promoted, ", "))
		stdlog.Printf("UI will render with partial data; promoted informers continue syncing in background")
		logf("    Phase 1 sync TIMED OUT (%d critical, %d promoted to deferred): %v",
			len(criticalSyncFuncs), len(promoted), time.Since(syncStart))
		rc.promotedKinds = promoted
	} else {
		logf("    Phase 1 sync (%d critical informers): %v", len(criticalSyncFuncs), time.Since(syncStart))
		stdlog.Printf("Critical resource caches synced in %v — UI can render", time.Since(syncStart))
	}

	// Log per-type resource counts for startup diagnostics
	if counts := rc.GetKindObjectCounts(); len(counts) > 0 {
		total := 0
		var parts []string
		for kind, count := range counts {
			if count > 0 {
				parts = append(parts, fmt.Sprintf("%s:%d", kind, count))
				total += count
			}
		}
		sort.Strings(parts)
		stdlog.Printf("Resource breakdown (%d total): %s", total, strings.Join(parts, ", "))
	}

	rc.syncComplete.Store(true)

	// Build deferred tracking state (includes both deferred and background keys)
	allDeferredKeys := append(append([]string{}, deferredKeys...), backgroundKeys...)
	deferredSynced := make(map[string]bool, len(allDeferredKeys))
	for _, k := range allDeferredKeys {
		deferredSynced[k] = false
	}
	deferredDone := make(chan struct{})
	rc.deferredSynced = deferredSynced
	rc.deferredDone = deferredDone

	// Phase 2: Start deferred informers now that critical sync is done,
	// then wait for them in background. This staggers the API server load.
	for _, e := range allEntries {
		if e.deferred {
			go e.informer.Run(stopCh)
		}
	}

	if len(deferredSyncFuncs) > 0 {
		go func() {
			deferredStart := time.Now()
			progressTicker := time.NewTicker(5 * time.Second)
			defer progressTicker.Stop()

			var deadlineCh <-chan time.Time
			if cfg.DeferredSyncTimeout > 0 {
				t := time.NewTimer(cfg.DeferredSyncTimeout)
				defer t.Stop()
				deadlineCh = t.C
			}

			timedOut := false
			for {
				// Mark each informer synced the moment its own HasSynced() is
				// true. A permanently-failing informer (e.g. HPA autoscaling/v2
				// on K8s <1.23) must not block siblings from becoming ready.
				rc.deferredMu.Lock()
				allSynced := true
				for i, fn := range deferredSyncFuncs {
					k := deferredKeys[i]
					if rc.deferredSynced[k] {
						continue
					}
					if fn() {
						rc.deferredSynced[k] = true
					} else {
						allSynced = false
					}
				}
				rc.deferredMu.Unlock()
				if allSynced {
					break
				}

				select {
				case <-stopCh:
					rc.deferredFailed.Store(true)
					stdlog.Printf("ERROR: Deferred resource cache sync aborted after %v", time.Since(deferredStart))
					close(deferredDone)
					return
				case <-deadlineCh:
					timedOut = true
				case <-progressTicker.C:
					counts := rc.GetKindObjectCounts()
					rc.informerMu.RLock()
					var synced, pendingParts []string
					for _, s := range rc.informerStatuses {
						if !s.Deferred {
							continue
						}
						// Skip background informers (Events) in deferred progress
						if slices.Contains(backgroundKeys, s.Key) {
							continue
						}
						if s.Synced {
							synced = append(synced, s.Kind)
						} else {
							n := counts[s.Kind]
							pendingParts = append(pendingParts, fmt.Sprintf("%s(%d)", s.Kind, n))
						}
					}
					rc.informerMu.RUnlock()
					stdlog.Printf("Deferred sync progress: %d/%d synced (%.0fs elapsed) — pending: %s",
						len(synced), len(synced)+len(pendingParts), time.Since(deferredStart).Seconds(), strings.Join(pendingParts, ", "))
				default:
					time.Sleep(100 * time.Millisecond)
				}

				if timedOut {
					break
				}
			}

			if timedOut {
				// Stop waiting on stragglers. deferredFailed is the "give up"
				// signal read by IsDeferredPending — stragglers start reporting
				// not-pending, so HTTP handlers return 403 instead of perpetual
				// 503. Informers that already synced keep their own
				// deferredSynced[k]=true and continue serving normally.
				rc.deferredMu.RLock()
				var pending []string
				for _, k := range deferredKeys {
					if !rc.deferredSynced[k] {
						pending = append(pending, k)
					}
				}
				rc.deferredMu.RUnlock()
				rc.deferredFailed.Store(true)
				stdlog.Printf("WARNING: Deferred sync timed out after %v; %d informer(s) never synced: %s. "+
					"These resources will return 403 to API consumers. Common cause: the corresponding API "+
					"version isn't served on this cluster (check `kubectl api-resources | grep <resource>`).",
					cfg.DeferredSyncTimeout, len(pending), strings.Join(pending, ", "))
			} else {
				logf("    Phase 2 sync (%d deferred informers): %v", len(deferredSyncFuncs), time.Since(deferredStart))
				stdlog.Printf("Deferred resource caches synced in %v (total: %v)", time.Since(deferredStart), time.Since(syncStart))
			}
			close(deferredDone)
		}()
	} else {
		close(deferredDone)
	}

	// Background informers (Events) sync independently — they can take 60s+
	// on large clusters and shouldn't block topology/warmup completion.
	if len(backgroundSyncFuncs) > 0 {
		go func() {
			bgStart := time.Now()
			if cache.WaitForCacheSync(stopCh, backgroundSyncFuncs...) {
				rc.deferredMu.Lock()
				for _, k := range backgroundKeys {
					rc.deferredSynced[k] = true
				}
				rc.deferredMu.Unlock()
				stdlog.Printf("Background Events sync complete in %v", time.Since(bgStart))
			} else {
				rc.deferredFailed.Store(true)
				stdlog.Printf("WARNING: Background Events sync failed after %v", time.Since(bgStart))
			}
		}()
	}

	return rc, nil
}

// buildInformerSetups returns the table-driven informer setup list.
func buildInformerSetups(factory informers.SharedInformerFactory) []informerSetup {
	return []informerSetup{
		{Services, "Service", func() cache.SharedIndexInformer { return factory.Core().V1().Services().Informer() }, false},
		{Pods, "Pod", func() cache.SharedIndexInformer { return factory.Core().V1().Pods().Informer() }, false},
		{Nodes, "Node", func() cache.SharedIndexInformer { return factory.Core().V1().Nodes().Informer() }, false},
		{Namespaces, "Namespace", func() cache.SharedIndexInformer { return factory.Core().V1().Namespaces().Informer() }, false},
		{ConfigMaps, "ConfigMap", func() cache.SharedIndexInformer { return factory.Core().V1().ConfigMaps().Informer() }, false},
		{Secrets, "Secret", func() cache.SharedIndexInformer { return factory.Core().V1().Secrets().Informer() }, false},
		{Events, "Event", func() cache.SharedIndexInformer { return factory.Core().V1().Events().Informer() }, true},
		{PersistentVolumeClaims, "PersistentVolumeClaim", func() cache.SharedIndexInformer { return factory.Core().V1().PersistentVolumeClaims().Informer() }, false},
		{PersistentVolumes, "PersistentVolume", func() cache.SharedIndexInformer { return factory.Core().V1().PersistentVolumes().Informer() }, false},
		{Deployments, "Deployment", func() cache.SharedIndexInformer { return factory.Apps().V1().Deployments().Informer() }, false},
		{DaemonSets, "DaemonSet", func() cache.SharedIndexInformer { return factory.Apps().V1().DaemonSets().Informer() }, false},
		{StatefulSets, "StatefulSet", func() cache.SharedIndexInformer { return factory.Apps().V1().StatefulSets().Informer() }, false},
		{ReplicaSets, "ReplicaSet", func() cache.SharedIndexInformer { return factory.Apps().V1().ReplicaSets().Informer() }, false},
		{Ingresses, "Ingress", func() cache.SharedIndexInformer { return factory.Networking().V1().Ingresses().Informer() }, false},
		{IngressClasses, "IngressClass", func() cache.SharedIndexInformer { return factory.Networking().V1().IngressClasses().Informer() }, false},
		{Jobs, "Job", func() cache.SharedIndexInformer { return factory.Batch().V1().Jobs().Informer() }, false},
		{CronJobs, "CronJob", func() cache.SharedIndexInformer { return factory.Batch().V1().CronJobs().Informer() }, false},
		{HorizontalPodAutoscalers, "HorizontalPodAutoscaler", func() cache.SharedIndexInformer {
			return factory.Autoscaling().V2().HorizontalPodAutoscalers().Informer()
		}, false},
		{StorageClasses, "StorageClass", func() cache.SharedIndexInformer { return factory.Storage().V1().StorageClasses().Informer() }, false},
		{PodDisruptionBudgets, "PodDisruptionBudget", func() cache.SharedIndexInformer { return factory.Policy().V1().PodDisruptionBudgets().Informer() }, false},
		{NetworkPolicies, "NetworkPolicy", func() cache.SharedIndexInformer { return factory.Networking().V1().NetworkPolicies().Informer() }, false},
		{ServiceAccounts, "ServiceAccount", func() cache.SharedIndexInformer { return factory.Core().V1().ServiceAccounts().Informer() }, false},
		{LimitRanges, "LimitRange", func() cache.SharedIndexInformer { return factory.Core().V1().LimitRanges().Informer() }, false},
	}
}

// addChangeHandlers registers event handlers for non-Event resource changes.
func (rc *ResourceCache) addChangeHandlers(inf cache.SharedIndexInformer, kind string, ch chan<- ResourceChange) error {
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			rc.enqueueChange(ch, kind, obj, nil, OpAdd)
		},
		UpdateFunc: func(oldObj, newObj any) {
			rc.enqueueChange(ch, kind, newObj, oldObj, OpUpdate)
		},
		DeleteFunc: func(obj any) {
			rc.enqueueChange(ch, kind, obj, nil, OpDelete)
		},
	})
	return err
}

// addEventHandlers registers special handlers for K8s Events.
// Events use a separate path: no noisy filtering, no diff computation.
func (rc *ResourceCache) addEventHandlers(inf cache.SharedIndexInformer, ch chan<- ResourceChange) error {
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			rc.enqueueEvent(ch, obj, OpAdd)
		},
		UpdateFunc: func(oldObj, newObj any) {
			rc.enqueueEvent(ch, newObj, OpUpdate)
		},
		DeleteFunc: func(obj any) {
			rc.enqueueEvent(ch, obj, OpDelete)
		},
	})
	return err
}

// enqueueChange handles non-Event resource change notifications.
func (rc *ResourceCache) enqueueChange(ch chan<- ResourceChange, kind string, obj any, oldObj any, op string) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			meta, ok = tombstone.Obj.(metav1.Object)
			if !ok {
				rc.stdlog.Printf("Warning: tombstone contained non-metav1.Object for %s %s", kind, op)
				return
			}
			obj = tombstone.Obj
		} else {
			return
		}
	}

	ns := meta.GetNamespace()
	name := meta.GetName()
	uid := string(meta.GetUID())

	// Track event received (before any filtering)
	if rc.config.OnReceived != nil {
		rc.safeCallback("OnReceived", func() { rc.config.OnReceived(kind) })
	}

	// Check if noisy — skip OnChange callback and don't send to changes channel.
	// Noisy resources (Lease, Endpoints, EndpointSlice updates, etc.) fire constantly
	// and don't affect topology or produce meaningful k8s_event broadcasts.
	skipCallback := false
	isNoisy := false
	if rc.config.IsNoisyResource != nil && rc.config.IsNoisyResource(kind, name, op) {
		skipCallback = true
		isNoisy = true
		if rc.config.OnDrop != nil {
			rc.config.OnDrop(kind, ns, name, "noisy_filter", op)
		}
	}

	// SuppressInitialAdds: during initial sync, skip OnChange for adds
	if op == "add" && rc.config.SuppressInitialAdds && !rc.syncComplete.Load() {
		skipCallback = true
	}

	// Compute diff for updates
	var diff *DiffInfo
	if op == "update" && oldObj != nil && obj != nil && rc.config.ComputeDiff != nil {
		diff = rc.config.ComputeDiff(kind, oldObj, obj)
	}

	change := ResourceChange{
		Kind:      kind,
		Namespace: ns,
		Name:      name,
		UID:       uid,
		Operation: op,
		Diff:      diff,
	}

	// Fire OnChange callback (before channel send, matching existing behavior)
	if !skipCallback && rc.config.OnChange != nil {
		rc.safeCallback("OnChange", func() { rc.config.OnChange(change, obj, oldObj) })
	}

	// Non-blocking send to changes channel (skip noisy resources entirely —
	// they don't affect topology and would just trigger unnecessary rebuilds)
	if !isNoisy {
		select {
		case ch <- change:
		default:
			if rc.config.OnDrop != nil {
				rc.config.OnDrop(kind, ns, name, "channel_full", op)
			} else {
				rc.stdlog.Printf("Warning: change channel full, dropped %s %s/%s op=%s", kind, ns, name, op)
			}
		}
	}
}

// enqueueEvent handles K8s Event resource changes (separate path).
func (rc *ResourceCache) enqueueEvent(ch chan<- ResourceChange, obj any, op string) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			meta, ok = tombstone.Obj.(metav1.Object)
			if !ok {
				rc.stdlog.Printf("Warning: tombstone contained non-metav1.Object for Event %s", op)
				return
			}
			obj = tombstone.Obj
		} else {
			return
		}
	}

	ns := meta.GetNamespace()
	name := meta.GetName()
	uid := string(meta.GetUID())

	// Fire OnEventChange callback
	if rc.config.OnEventChange != nil {
		rc.safeCallback("OnEventChange", func() { rc.config.OnEventChange(obj, op) })
	}

	change := ResourceChange{
		Kind:      "Event",
		Namespace: ns,
		Name:      name,
		UID:       uid,
		Operation: op,
	}

	select {
	case ch <- change:
	default:
		if rc.config.OnDrop != nil {
			rc.config.OnDrop("Event", ns, name, "channel_full", op)
		} else {
			rc.stdlog.Printf("Warning: change channel full, dropped Event %s/%s op=%s", ns, name, op)
		}
	}
}

// safeCallback invokes fn with panic recovery to protect informer goroutines.
func (rc *ResourceCache) safeCallback(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			rc.stdlog.Printf("ERROR: k8score %s callback panicked: %v\n%s", name, r, buf[:n])
		}
	}()
	fn()
}

// Stop initiates a non-blocking shutdown of the cache.
func (rc *ResourceCache) Stop() {
	if rc == nil {
		return
	}
	rc.stopOnce.Do(func() {
		rc.stdlog.Println("Stopping resource cache")
		close(rc.stopCh)
		go func() {
			done := make(chan struct{})
			go func() {
				rc.factory.Shutdown()
				close(done)
			}()
			select {
			case <-done:
				rc.stdlog.Println("Resource cache factory shutdown complete")
			case <-time.After(5 * time.Second):
				rc.stdlog.Println("Resource cache factory shutdown taking >5s, abandoning")
			}
		}()
	})
}

// Changes returns a read-only channel for resource change notifications.
func (rc *ResourceCache) Changes() <-chan ResourceChange {
	if rc == nil {
		return nil
	}
	return rc.changes
}

// ChangesRaw returns the bidirectional channel for internal use.
func (rc *ResourceCache) ChangesRaw() chan ResourceChange {
	if rc == nil {
		return nil
	}
	return rc.changes
}

// PromotedKinds returns the list of resource kinds that were promoted from
// critical to deferred due to SyncTimeout. Empty if sync completed normally.
func (rc *ResourceCache) PromotedKinds() []string {
	if rc == nil {
		return nil
	}
	return rc.promotedKinds
}

// IsSyncComplete returns true after the initial critical informer sync.
func (rc *ResourceCache) IsSyncComplete() bool {
	if rc == nil {
		return false
	}
	return rc.syncComplete.Load()
}

// IsDeferredSynced returns true when all deferred (non-background) informers have
// completed sync. Background informers (e.g. Events) sync independently and are
// not included. Returns false if still syncing or if sync failed.
func (rc *ResourceCache) IsDeferredSynced() bool {
	if rc == nil {
		return false
	}
	select {
	case <-rc.deferredDone:
		return !rc.deferredFailed.Load()
	default:
		return false
	}
}

// DeferredDone returns a channel that is closed when all deferred (non-background)
// informers have completed their initial sync. Background informers sync independently.
func (rc *ResourceCache) DeferredDone() <-chan struct{} {
	if rc == nil {
		return nil
	}
	return rc.deferredDone
}

// GetSyncStatus returns the current sync status of all informers for diagnostics.
// Safe to call at any time, including during sync.
func (rc *ResourceCache) GetSyncStatus() CacheSyncStatus {
	if rc == nil {
		return CacheSyncStatus{Phase: SyncPhaseNotStarted}
	}

	rc.informerMu.RLock()
	statuses := make([]InformerSyncStatus, len(rc.informerStatuses))
	copy(statuses, rc.informerStatuses)
	rc.informerMu.RUnlock()

	// Enrich with live item counts from listers
	counts := rc.GetKindObjectCounts()
	for i := range statuses {
		if n, ok := counts[statuses[i].Kind]; ok {
			statuses[i].Items = n
		}
	}

	var critTotal, critSynced, defTotal, defSynced int
	var pendingCritical, pendingDeferred []string
	for _, s := range statuses {
		if s.Deferred {
			defTotal++
			if s.Synced {
				defSynced++
			} else {
				pendingDeferred = append(pendingDeferred, s.Kind)
			}
		} else {
			critTotal++
			if s.Synced {
				critSynced++
			} else {
				pendingCritical = append(pendingCritical, s.Kind)
			}
		}
	}

	var phase SyncPhase
	switch {
	case rc.syncStartTime.IsZero():
		phase = SyncPhaseNotStarted
	case !rc.syncComplete.Load():
		phase = SyncPhaseCritical
	case !rc.IsDeferredSynced():
		phase = SyncPhaseDeferred
	default:
		phase = SyncPhaseComplete
	}

	result := CacheSyncStatus{
		Phase:           phase,
		ElapsedSec:      time.Since(rc.syncStartTime).Seconds(),
		CriticalTotal:   critTotal,
		CriticalSynced:  critSynced,
		DeferredTotal:   defTotal,
		DeferredSynced:  defSynced,
		Informers:       statuses,
		PendingCritical: pendingCritical,
		PendingDeferred: pendingDeferred,
		PromotedKinds:   rc.promotedKinds,
	}
	if !rc.syncStartTime.IsZero() {
		result.SyncStarted = rc.syncStartTime.Format(time.RFC3339)
	}
	return result
}

// GetEnabledResources returns a copy of the enabled resources map.
func (rc *ResourceCache) GetEnabledResources() map[string]bool {
	if rc == nil {
		return nil
	}
	result := make(map[string]bool, len(rc.enabledResources))
	maps.Copy(result, rc.enabledResources)
	return result
}

// GetResourceCount returns total cached resources across all enabled non-Event listers.
func (rc *ResourceCache) GetResourceCount() int {
	if rc == nil {
		return 0
	}
	counts := rc.GetKindObjectCounts()
	total := 0
	for kind, n := range counts {
		if kind == "Event" {
			continue // Events are not counted as "resources"
		}
		total += n
	}
	return total
}

// kindLister maps a Kind name to a lister accessor for table-driven counting.
type kindLister struct {
	kind   string
	group  string // API group (empty for core, e.g. "apps", "batch", "networking.k8s.io")
	lister func(rc *ResourceCache) any
}

// allKindListers is the table of all resource kinds and their lister accessors.
var allKindListers = []kindLister{
	{"Pod", "", func(rc *ResourceCache) any { return rc.Pods() }},
	{"Service", "", func(rc *ResourceCache) any { return rc.Services() }},
	{"Node", "", func(rc *ResourceCache) any { return rc.Nodes() }},
	{"Namespace", "", func(rc *ResourceCache) any { return rc.Namespaces() }},
	{"ConfigMap", "", func(rc *ResourceCache) any { return rc.ConfigMaps() }},
	{"Secret", "", func(rc *ResourceCache) any { return rc.Secrets() }},
	{"Event", "", func(rc *ResourceCache) any { return rc.Events() }},
	{"PersistentVolumeClaim", "", func(rc *ResourceCache) any { return rc.PersistentVolumeClaims() }},
	{"PersistentVolume", "", func(rc *ResourceCache) any { return rc.PersistentVolumes() }},
	{"Deployment", "apps", func(rc *ResourceCache) any { return rc.Deployments() }},
	{"DaemonSet", "apps", func(rc *ResourceCache) any { return rc.DaemonSets() }},
	{"StatefulSet", "apps", func(rc *ResourceCache) any { return rc.StatefulSets() }},
	{"ReplicaSet", "apps", func(rc *ResourceCache) any { return rc.ReplicaSets() }},
	{"Ingress", "networking.k8s.io", func(rc *ResourceCache) any { return rc.Ingresses() }},
	{"IngressClass", "networking.k8s.io", func(rc *ResourceCache) any { return rc.IngressClasses() }},
	{"Job", "batch", func(rc *ResourceCache) any { return rc.Jobs() }},
	{"CronJob", "batch", func(rc *ResourceCache) any { return rc.CronJobs() }},
	{"HorizontalPodAutoscaler", "autoscaling", func(rc *ResourceCache) any { return rc.HorizontalPodAutoscalers() }},
	{"StorageClass", "storage.k8s.io", func(rc *ResourceCache) any { return rc.StorageClasses() }},
	{"PodDisruptionBudget", "policy", func(rc *ResourceCache) any { return rc.PodDisruptionBudgets() }},
	{"NetworkPolicy", "networking.k8s.io", func(rc *ResourceCache) any { return rc.NetworkPolicies() }},
	{"ServiceAccount", "", func(rc *ResourceCache) any { return rc.ServiceAccounts() }},
	{"LimitRange", "", func(rc *ResourceCache) any { return rc.LimitRanges() }},
}

// AllKindListers returns the table of all resource kinds with their group and lister.
// Used by the resource-counts endpoint to enumerate typed resources.
func AllKindListers() []kindLister {
	return allKindListers
}

// Kind returns the resource kind name.
func (kl kindLister) Kind() string { return kl.kind }

// Group returns the API group (empty for core resources).
func (kl kindLister) Group() string { return kl.group }

// Lister returns the lister accessor function.
func (kl kindLister) Lister() func(rc *ResourceCache) any { return kl.lister }

// CountKey returns the key used in resource-counts responses: "group/Kind" or just "Kind" for core.
func (kl kindLister) CountKey() string {
	if kl.group != "" {
		return kl.group + "/" + kl.kind
	}
	return kl.kind
}

// GetKindObjectCounts returns the number of cached objects per resource kind.
// Only includes kinds that are enabled. Returns nil if cache is nil.
func (rc *ResourceCache) GetKindObjectCounts() map[string]int {
	if rc == nil {
		return nil
	}
	counts := make(map[string]int)
	for _, kl := range allKindListers {
		l := kl.lister(rc)
		if l == nil {
			continue
		}
		n := listCount(l)
		if n > 0 {
			counts[kl.kind] = n
		}
	}
	return counts
}

// isEnabled returns true if the resource type has an informer running.
func (rc *ResourceCache) isEnabled(key string) bool {
	if rc == nil || rc.enabledResources == nil {
		return false
	}
	return rc.enabledResources[key]
}

// isReady returns true if the resource is enabled and, if deferred, synced.
func (rc *ResourceCache) isReady(key string) bool {
	if !rc.isEnabled(key) {
		return false
	}
	if rc.config.DeferredTypes == nil || !rc.config.DeferredTypes[key] {
		return true
	}
	rc.deferredMu.RLock()
	defer rc.deferredMu.RUnlock()
	return rc.deferredSynced[key]
}

// IsDeferredPending returns true when the resource type passed RBAC checks
// (informer is enabled) but deferred sync has not completed yet. Callers
// can use this to distinguish "no permission" (return 403) from "not ready
// yet" (return 503) when a lister returns nil.
// Returns false once deferred sync has permanently failed (avoids infinite spinner).
func (rc *ResourceCache) IsDeferredPending(key string) bool {
	if rc == nil {
		return false
	}
	if !rc.isEnabled(key) {
		return false
	}
	if rc.config.DeferredTypes == nil || !rc.config.DeferredTypes[key] {
		return false
	}
	if rc.deferredFailed.Load() {
		return false
	}
	rc.deferredMu.RLock()
	defer rc.deferredMu.RUnlock()
	return !rc.deferredSynced[key]
}
