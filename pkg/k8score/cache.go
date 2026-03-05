package k8score

import (
	"fmt"
	"log"
	"maps"
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
	syncComplete     atomic.Bool
	config           CacheConfig
	stdlog           *log.Logger
}

type informerSetup struct {
	key     string
	kind    string
	setup   func() cache.SharedIndexInformer
	isEvent bool
}

// NewResourceCache creates and starts a ResourceCache from the given config.
// It blocks until critical (non-deferred) informers have synced, then returns.
// Deferred informers sync in the background.
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
	enabledCount := 0

	rc := &ResourceCache{
		factory:          factory,
		changes:          changes,
		stopCh:           stopCh,
		enabledResources: enabled,
		config:           cfg,
		stdlog:           stdlog,
	}

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

		if deferredTypes[s.key] {
			deferredSyncFuncs = append(deferredSyncFuncs, inf.HasSynced)
			deferredKeys = append(deferredKeys, s.key)
		} else {
			criticalSyncFuncs = append(criticalSyncFuncs, inf.HasSynced)
		}
	}

	if enabledCount == 0 {
		stdlog.Printf("Warning: No resource types are accessible (all RBAC checks failed)")
		rc.deferredSynced = make(map[string]bool)
		rc.deferredDone = make(chan struct{})
		close(rc.deferredDone)
		rc.syncComplete.Store(true)
		return rc, nil
	}

	// Start all informers
	factory.Start(stopCh)

	stdlog.Printf("Starting resource cache: %d critical + %d deferred informers (%d total)",
		len(criticalSyncFuncs), len(deferredSyncFuncs), enabledCount)
	syncStart := time.Now()

	// Track per-informer sync times
	for _, s := range setups {
		if !enabled[s.key] {
			continue
		}
		kind := s.kind
		key := s.key
		isDeferred := deferredTypes[key]
		var fn cache.InformerSynced
		if isDeferred {
			for i, dk := range deferredKeys {
				if dk == key {
					fn = deferredSyncFuncs[i]
					break
				}
			}
		} else {
			idx := 0
			for _, ss := range setups {
				if !enabled[ss.key] || deferredTypes[ss.key] {
					continue
				}
				if ss.key == key {
					fn = criticalSyncFuncs[idx]
					break
				}
				idx++
			}
		}
		if fn != nil {
			tag := "critical"
			if isDeferred {
				tag = "deferred"
			}
			go func() {
				t := time.Now()
				for !fn() {
					select {
					case <-stopCh:
						return
					default:
					}
					time.Sleep(10 * time.Millisecond)
				}
				logf("    Informer synced: %-28s %v (%s)", kind, time.Since(t), tag)
			}()
		}
	}

	// Phase 1: Wait for critical informers
	if len(criticalSyncFuncs) > 0 {
		if !cache.WaitForCacheSync(stopCh, criticalSyncFuncs...) {
			close(stopCh)
			return nil, fmt.Errorf("failed to sync critical resource caches")
		}
	}
	logf("    Phase 1 sync (%d critical informers): %v", len(criticalSyncFuncs), time.Since(syncStart))
	stdlog.Printf("Critical resource caches synced in %v — UI can render", time.Since(syncStart))

	rc.syncComplete.Store(true)

	// Build deferred tracking state
	deferredSynced := make(map[string]bool, len(deferredKeys))
	for _, k := range deferredKeys {
		deferredSynced[k] = false
	}
	deferredDone := make(chan struct{})
	rc.deferredSynced = deferredSynced
	rc.deferredDone = deferredDone

	// Phase 2: Wait for deferred informers in background
	if len(deferredSyncFuncs) > 0 {
		go func() {
			deferredStart := time.Now()
			if cache.WaitForCacheSync(stopCh, deferredSyncFuncs...) {
				rc.deferredMu.Lock()
				for _, k := range deferredKeys {
					rc.deferredSynced[k] = true
				}
				rc.deferredMu.Unlock()
				close(deferredDone)
				logf("    Phase 2 sync (%d deferred informers): %v", len(deferredSyncFuncs), time.Since(deferredStart))
				stdlog.Printf("Deferred resource caches synced in %v (total: %v)", time.Since(deferredStart), time.Since(syncStart))
			} else {
				stdlog.Printf("ERROR: Deferred resource cache sync failed after %v", time.Since(deferredStart))
				close(deferredDone)
			}
		}()
	} else {
		close(deferredDone)
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
		{ServiceAccounts, "ServiceAccount", func() cache.SharedIndexInformer { return factory.Core().V1().ServiceAccounts().Informer() }, false},
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
			rc.stdlog.Printf("ERROR: k8score %s callback panicked: %v", name, r)
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

// IsSyncComplete returns true after the initial critical informer sync.
func (rc *ResourceCache) IsSyncComplete() bool {
	if rc == nil {
		return false
	}
	return rc.syncComplete.Load()
}

// IsDeferredSynced returns true when all deferred informers have completed sync.
func (rc *ResourceCache) IsDeferredSynced() bool {
	if rc == nil {
		return false
	}
	select {
	case <-rc.deferredDone:
		return true
	default:
		return false
	}
}

// DeferredDone returns a channel that is closed when all deferred informers
// have completed their initial sync.
func (rc *ResourceCache) DeferredDone() <-chan struct{} {
	if rc == nil {
		return nil
	}
	return rc.deferredDone
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
	{"ServiceAccount", "", func(rc *ResourceCache) any { return rc.ServiceAccounts() }},
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
