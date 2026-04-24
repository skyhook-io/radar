package k8score

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// DynamicResourceCache provides on-demand caching for CRDs and other dynamic
// resources. It is safe for concurrent use. Application-specific callbacks
// (timeline, metrics) are injected via DynamicCacheConfig.
type DynamicResourceCache struct {
	factory         dynamicinformer.DynamicSharedInformerFactory
	informers       map[schema.GroupVersionResource]cache.SharedIndexInformer
	syncComplete    map[schema.GroupVersionResource]bool
	stopCh          chan struct{}
	stopOnce        sync.Once
	mu              sync.RWMutex
	config          DynamicCacheConfig
	discoveryStatus CRDDiscoveryStatus
	discoveryMu     sync.RWMutex
	discoveryDone   chan struct{} // closed when DiscoverAllCRDs() completes

	// CRD discovery completion callbacks
	crdCallbacks   []func()
	crdCallbacksMu sync.RWMutex
}

// NewDynamicResourceCache creates a dynamic resource cache with the given config.
func NewDynamicResourceCache(cfg DynamicCacheConfig) (*DynamicResourceCache, error) {
	if cfg.DynamicClient == nil {
		return nil, fmt.Errorf("dynamic client must not be nil")
	}
	if cfg.NamespaceScoped && cfg.Namespace == "" {
		return nil, fmt.Errorf("namespace must be set when NamespaceScoped is true")
	}

	var factory dynamicinformer.DynamicSharedInformerFactory
	if cfg.NamespaceScoped && cfg.Namespace != "" {
		factory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(
			cfg.DynamicClient, 0, cfg.Namespace, nil,
		)
		log.Printf("Using namespace-scoped dynamic informers for namespace %q", cfg.Namespace)
	} else {
		factory = dynamicinformer.NewDynamicSharedInformerFactory(
			cfg.DynamicClient,
			0, // no resync — updates come via watch
		)
	}

	d := &DynamicResourceCache{
		factory:         factory,
		informers:       make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
		syncComplete:    make(map[schema.GroupVersionResource]bool),
		stopCh:          make(chan struct{}),
		config:          cfg,
		discoveryStatus: CRDDiscoveryIdle,
		discoveryDone:   make(chan struct{}),
	}

	log.Println("Dynamic resource cache initialized")
	return d, nil
}

// ---------------------------------------------------------------------------
// EnsureWatching / startWatching / probeAccess
// ---------------------------------------------------------------------------

// EnsureWatching starts watching a resource type if not already watching.
// The sync happens asynchronously — callers should use WaitForSync if they need to wait.
func (d *DynamicResourceCache) EnsureWatching(gvr schema.GroupVersionResource) error {
	if d == nil {
		return fmt.Errorf("dynamic resource cache not initialized")
	}

	// Check if resource supports list/watch before attempting to watch
	if d.config.Discovery != nil && !d.config.Discovery.SupportsWatchGVR(gvr) {
		return fmt.Errorf("resource %s.%s/%s does not support list/watch", gvr.Resource, gvr.Group, gvr.Version)
	}

	// Quick check under read lock
	d.mu.RLock()
	_, exists := d.informers[gvr]
	d.mu.RUnlock()
	if exists {
		return nil
	}

	// If CRD discovery is in progress, wait for it to finish
	if d.GetDiscoveryStatus() == CRDDiscoveryInProgress {
		select {
		case <-d.discoveryDone:
		case <-time.After(45 * time.Second):
			log.Printf("[dynamic cache] Timeout waiting for CRD discovery, probing %s independently", gvr.Resource)
		}

		d.mu.RLock()
		_, exists = d.informers[gvr]
		d.mu.RUnlock()
		if exists {
			return nil
		}
	}

	// Probe access BEFORE acquiring write lock
	if err := d.probeAccess(gvr); err != nil {
		return fmt.Errorf("no access to %s.%s/%s: %w", gvr.Resource, gvr.Group, gvr.Version, err)
	}

	return d.startWatching(gvr)
}

// startWatching creates and starts an informer for a GVR (no access probe).
func (d *DynamicResourceCache) startWatching(gvr schema.GroupVersionResource) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.informers[gvr]; exists {
		return nil
	}

	informer := d.factory.ForResource(gvr).Informer()
	// Apply the dynamic-cache transform BEFORE informer.Run so every
	// object entering the store is shrunk in place. SetTransform must
	// be called pre-Run (returns ErrRunning otherwise). If it ever
	// fails we log and continue — the informer still functions, just
	// with fattier cached objects.
	if err := informer.SetTransform(DropUnstructuredManagedFields); err != nil {
		log.Printf("Warning: SetTransform failed for %v: %v (cache will retain managedFields/CRD schemas)", gvr, err)
	}
	d.informers[gvr] = informer

	kind := d.gvrToKind(gvr)
	d.addDynamicChangeHandlers(informer, kind, gvr)

	go informer.Run(d.stopCh)

	informerCount := len(d.informers)
	log.Printf("Started watching dynamic resource: %s.%s/%s (total dynamic informers: %d)", gvr.Resource, gvr.Group, gvr.Version, informerCount)

	go func() {
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer syncCancel()
		go func() {
			select {
			case <-d.stopCh:
				syncCancel()
			case <-syncCtx.Done():
			}
		}()

		if !cache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced) {
			select {
			case <-d.stopCh:
				return
			default:
				log.Printf("Warning: cache sync timeout for %v", gvr)
			}
		} else {
			log.Printf("Dynamic resource synced: %s.%s/%s", gvr.Resource, gvr.Group, gvr.Version)
		}

		d.mu.Lock()
		d.syncComplete[gvr] = true
		d.mu.Unlock()
	}()
	return nil
}

// probeAccess does a quick list with limit=1 to verify the user can access this resource.
func (d *DynamicResourceCache) probeAccess(gvr schema.GroupVersionResource) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var err error
	if d.config.NamespaceScoped && d.config.Namespace != "" {
		_, err = d.config.DynamicClient.Resource(gvr).Namespace(d.config.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	} else {
		_, err = d.config.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	}

	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "forbidden") || strings.Contains(errLower, "unauthorized") {
			return err
		}
		log.Printf("[dynamic cache] Probe for %s.%s/%s returned non-auth error (allowing): %v", gvr.Resource, gvr.Group, gvr.Version, err)
	}

	return nil
}

// probeCount does a quick list with limit=1 and returns the approximate resource count.
// Returns -1 if access is denied, -2 if the probe failed for non-auth reasons (caller
// should defer), or the count (items + remainingItemCount) on success.
func (d *DynamicResourceCache) probeCount(gvr schema.GroupVersionResource) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var list *unstructured.UnstructuredList
	var err error
	if d.config.NamespaceScoped && d.config.Namespace != "" {
		list, err = d.config.DynamicClient.Resource(gvr).Namespace(d.config.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	} else {
		list, err = d.config.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	}

	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "forbidden") || strings.Contains(errLower, "unauthorized") {
			return -1
		}
		log.Printf("[dynamic cache] probeCount for %s.%s/%s returned non-auth error (deferring): %v",
			gvr.Resource, gvr.Group, gvr.Version, err)
		return -2
	}

	count := len(list.Items)
	if list.GetRemainingItemCount() != nil {
		count += int(*list.GetRemainingItemCount())
	}
	return count
}

// gvrToKind converts a GVR to a Kind name using resource discovery.
func (d *DynamicResourceCache) gvrToKind(gvr schema.GroupVersionResource) string {
	if d.config.Discovery != nil {
		if kind := d.config.Discovery.GetKindForGVR(gvr); kind != "" {
			return kind
		}
	}
	// Fallback: capitalize and singularize
	name := gvr.Resource
	if len(name) > 1 && name[len(name)-1] == 's' {
		name = name[:len(name)-1]
	}
	if len(name) > 0 {
		return strings.ToUpper(name[:1]) + name[1:]
	}
	return name
}

// ---------------------------------------------------------------------------
// Change handlers
// ---------------------------------------------------------------------------

// safeCallback invokes fn with panic recovery to protect informer goroutines.
func (d *DynamicResourceCache) safeCallback(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ERROR: k8score dynamic cache %s callback panicked: %v", name, r)
		}
	}()
	fn()
}

func (d *DynamicResourceCache) addDynamicChangeHandlers(inf cache.SharedIndexInformer, kind string, gvr schema.GroupVersionResource) {
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			d.enqueueDynamicChange(kind, gvr, obj, nil, OpAdd)
		},
		UpdateFunc: func(oldObj, newObj any) {
			d.enqueueDynamicChange(kind, gvr, newObj, oldObj, OpUpdate)
		},
		DeleteFunc: func(obj any) {
			d.enqueueDynamicChange(kind, gvr, obj, nil, OpDelete)
		},
	})
}

func (d *DynamicResourceCache) enqueueDynamicChange(kind string, gvr schema.GroupVersionResource, obj any, oldObj any, op string) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			u, ok = tombstone.Obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
		} else {
			return
		}
	}

	namespace := u.GetNamespace()
	name := u.GetName()
	uid := string(u.GetUID())

	if d.config.OnReceived != nil {
		d.safeCallback("OnReceived", func() { d.config.OnReceived(kind) })
	}

	// During initial sync, still fire OnChange (for historical recording)
	// but skip the channel send (no SSE flood).
	isSyncAdd := false
	if op == OpAdd {
		d.mu.RLock()
		synced := d.syncComplete[gvr]
		d.mu.RUnlock()

		if !synced {
			isSyncAdd = true
			if d.config.DebugEvents {
				log.Printf("[DEBUG] Dynamic initial sync add event: %s/%s/%s (recording historical only)", kind, namespace, name)
			}
		}
	}

	var diff *DiffInfo
	if op == OpUpdate && oldObj != nil && obj != nil && d.config.ComputeDiff != nil {
		d.safeCallback("ComputeDiff", func() { diff = d.config.ComputeDiff(kind, oldObj, obj) })
	}

	change := ResourceChange{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		UID:       uid,
		Operation: op,
		Diff:      diff,
	}

	// Always fire OnChange (even during sync adds — Radar uses this for timeline)
	if d.config.OnChange != nil {
		d.safeCallback("OnChange", func() { d.config.OnChange(change, obj, oldObj) })
	}

	// Skip channel send during initial sync
	if isSyncAdd {
		return
	}

	// Send to change channel
	if d.config.Changes != nil {
		select {
		case d.config.Changes <- change:
		default:
			if d.config.OnDrop != nil {
				d.safeCallback("OnDrop", func() { d.config.OnDrop(kind, namespace, name, "channel_full", op) })
			}
			if d.config.DebugEvents {
				log.Printf("[DEBUG] Dynamic change channel full, dropped: %s/%s/%s op=%s", kind, namespace, name, op)
			}
		}
	}

	if d.config.OnRecorded != nil {
		d.safeCallback("OnRecorded", func() { d.config.OnRecorded(kind) })
	}
}

// ---------------------------------------------------------------------------
// Read methods
// ---------------------------------------------------------------------------

// Count returns the number of resources for a given GVR, optionally filtered by namespaces.
// Unlike List(), this avoids allocating a result slice and skips StripUnstructuredFields.
func (d *DynamicResourceCache) Count(gvr schema.GroupVersionResource, namespaces []string) (int, error) {
	if d == nil {
		return 0, fmt.Errorf("dynamic resource cache not initialized")
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	synced := d.syncComplete[gvr]
	d.mu.RUnlock()

	if !exists || !synced {
		return 0, fmt.Errorf("informer not found or not synced for %v", gvr)
	}

	if len(namespaces) == 0 {
		return len(informer.GetIndexer().List()), nil
	}

	total := 0
	for _, ns := range namespaces {
		items, err := informer.GetIndexer().ByIndex(cache.NamespaceIndex, ns)
		if err != nil {
			return 0, fmt.Errorf("failed to count resources in namespace %s: %w", ns, err)
		}
		total += len(items)
	}
	return total, nil
}

// List returns all resources of a given GVR, optionally filtered by namespace.
// This is non-blocking — returns whatever data is available immediately.
func (d *DynamicResourceCache) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if err := d.EnsureWatching(gvr); err != nil {
		return nil, err
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	var items []any
	var err error

	if namespace != "" {
		items, err = informer.GetIndexer().ByIndex(cache.NamespaceIndex, namespace)
	} else {
		items = informer.GetIndexer().List()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		if u, ok := item.(*unstructured.Unstructured); ok {
			u = StripUnstructuredFields(u)
			result = append(result, u)
		}
	}

	return result, nil
}

// ListBlocking returns all resources, waiting for cache sync first.
func (d *DynamicResourceCache) ListBlocking(gvr schema.GroupVersionResource, namespace string, timeout time.Duration) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if err := d.EnsureWatching(gvr); err != nil {
		return nil, err
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	if !informer.HasSynced() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)
	}

	var items []any
	var err error

	if namespace != "" {
		items, err = informer.GetIndexer().ByIndex(cache.NamespaceIndex, namespace)
	} else {
		items = informer.GetIndexer().List()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		if u, ok := item.(*unstructured.Unstructured); ok {
			u = StripUnstructuredFields(u)
			result = append(result, u)
		}
	}

	return result, nil
}

// Get returns a single resource by namespace and name.
func (d *DynamicResourceCache) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if err := d.EnsureWatching(gvr); err != nil {
		return nil, err
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	var key string
	if namespace != "" {
		key = namespace + "/" + name
	} else {
		key = name
	}

	item, exists, err := informer.GetIndexer().GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	if !exists && !informer.HasSynced() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)

		item, exists, err = informer.GetIndexer().GetByKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to get resource: %w", err)
		}
	}

	if !exists {
		return nil, fmt.Errorf("resource not found: %s", key)
	}

	u, ok := item.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("unexpected type in cache")
	}

	return StripUnstructuredFields(u), nil
}

// ListWithSelector returns resources matching a label selector.
func (d *DynamicResourceCache) ListWithSelector(gvr schema.GroupVersionResource, namespace string, selector labels.Selector) ([]*unstructured.Unstructured, error) {
	items, err := d.List(gvr, namespace)
	if err != nil {
		return nil, err
	}

	if selector == nil || selector.Empty() {
		return items, nil
	}

	result := make([]*unstructured.Unstructured, 0)
	for _, item := range items {
		if selector.Matches(labels.Set(item.GetLabels())) {
			result = append(result, item)
		}
	}

	return result, nil
}

// ListDirect fetches resources directly from the API (bypasses cache).
func (d *DynamicResourceCache) ListDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	var list *unstructured.UnstructuredList
	var err error

	if namespace != "" {
		list, err = d.config.DynamicClient.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = d.config.DynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		result[i] = StripUnstructuredFields(&list.Items[i])
	}

	return result, nil
}

// GetDirect fetches a single resource directly from the API (bypasses cache).
func (d *DynamicResourceCache) GetDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	var u *unstructured.Unstructured
	var err error

	if namespace != "" {
		u, err = d.config.DynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		u, err = d.config.DynamicClient.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	}

	if err != nil {
		return nil, err
	}

	return StripUnstructuredFields(u), nil
}

// ---------------------------------------------------------------------------
// Batch / warmup
// ---------------------------------------------------------------------------

// WarmupParallel starts watching multiple resources in parallel and waits for all to sync.
func (d *DynamicResourceCache) WarmupParallel(gvrs []schema.GroupVersionResource, timeout time.Duration) {
	if d == nil || len(gvrs) == 0 {
		return
	}

	const maxConcurrentProbes = 50
	type probeResult struct {
		gvr schema.GroupVersionResource
		ok  bool
	}
	results := make(chan probeResult, len(gvrs))
	sem := make(chan struct{}, maxConcurrentProbes)
	for _, gvr := range gvrs {
		go func(g schema.GroupVersionResource) {
			sem <- struct{}{}
			err := d.probeAccess(g)
			<-sem
			results <- probeResult{gvr: g, ok: err == nil}
		}(gvr)
	}

	var accessibleGVRs []schema.GroupVersionResource
	for range gvrs {
		r := <-results
		if r.ok {
			accessibleGVRs = append(accessibleGVRs, r.gvr)
		}
	}

	if len(accessibleGVRs) == 0 {
		return
	}

	var validGVRs []schema.GroupVersionResource
	for _, gvr := range accessibleGVRs {
		if err := d.startWatching(gvr); err == nil {
			validGVRs = append(validGVRs, gvr)
		}
	}

	if len(validGVRs) == 0 {
		return
	}

	d.mu.RLock()
	syncFuncs := make([]cache.InformerSynced, 0, len(validGVRs))
	for _, gvr := range validGVRs {
		if informer, ok := d.informers[gvr]; ok {
			syncFuncs = append(syncFuncs, informer.HasSynced)
		}
	}
	d.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if !cache.WaitForCacheSync(ctx.Done(), syncFuncs...) {
		log.Printf("Warning: not all dynamic caches synced within timeout")
	} else {
		log.Printf("All %d dynamic resources synced", len(syncFuncs))
	}
}

// DiscoverAllCRDs discovers all CRDs that support list/watch and decides which
// to watch eagerly vs on-demand. Known integrations (cert-manager, KEDA, etc.)
// are already watching from WarmupCommonCRDs. For the rest, CRDs with ≤100
// resources are watched eagerly (cheap, full timeline coverage). CRDs with >100
// resources (calico, cilium, etc.) are deferred to on-demand via EnsureWatching()
// when the user browses them, avoiding expensive watch connections.
func (d *DynamicResourceCache) DiscoverAllCRDs() {
	if d == nil {
		log.Println("[CRD Discovery] Cache is nil, skipping")
		return
	}

	d.discoveryMu.Lock()
	if d.discoveryStatus != CRDDiscoveryIdle {
		log.Printf("[CRD Discovery] Already in status: %s, skipping", d.discoveryStatus)
		d.discoveryMu.Unlock()
		return
	}
	d.discoveryStatus = CRDDiscoveryInProgress
	d.discoveryMu.Unlock()
	log.Println("[CRD Discovery] Starting CRD discovery...")

	go func() {
		defer func() {
			panicked := false
			if r := recover(); r != nil {
				panicked = true
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				log.Printf("PANIC in CRD discovery goroutine: %v\n%s", r, buf[:n])
			}
			d.discoveryMu.Lock()
			if d.discoveryStatus != CRDDiscoveryComplete {
				d.discoveryStatus = CRDDiscoveryComplete
				close(d.discoveryDone)
			}
			d.discoveryMu.Unlock()
			if panicked {
				log.Println("[CRD Discovery] CRD discovery terminated due to panic (marked complete to unblock waiters)")
			} else {
				log.Println("[CRD Discovery] CRD discovery complete")
			}

			d.notifyCRDDiscoveryComplete()
		}()

		if d.config.Discovery == nil {
			log.Println("Resource discovery not available for CRD discovery")
			return
		}

		resources, err := d.config.Discovery.GetAPIResources()
		if err != nil {
			log.Printf("Failed to get API resources for CRD discovery: %v", err)
			return
		}

		best := make(map[string]schema.GroupVersionResource)
		for _, res := range resources {
			if !res.IsCRD {
				continue
			}
			hasList := false
			hasWatch := false
			for _, verb := range res.Verbs {
				if verb == "list" {
					hasList = true
				}
				if verb == "watch" {
					hasWatch = true
				}
			}
			if !hasList || !hasWatch {
				continue
			}
			key := res.Group + "/" + res.Name
			if existing, ok := best[key]; ok {
				if !IsMoreStableVersion(res.Version, existing.Version) {
					continue
				}
			}
			best[key] = schema.GroupVersionResource{
				Group:    res.Group,
				Version:  res.Version,
				Resource: res.Name,
			}
		}

		var gvrs []schema.GroupVersionResource
		for _, gvr := range best {
			gvrs = append(gvrs, gvr)
		}

		if len(gvrs) == 0 {
			log.Println("No watchable CRDs found")
			return
		}

		// Filter out GVRs already watched from Phase 1 warmup
		d.mu.RLock()
		alreadyWatching := len(d.informers)
		var remaining []schema.GroupVersionResource
		for _, gvr := range gvrs {
			if _, exists := d.informers[gvr]; !exists {
				remaining = append(remaining, gvr)
			}
		}
		d.mu.RUnlock()

		if len(remaining) == 0 {
			log.Printf("Discovered %d watchable CRDs (all %d already watching from warmup)", len(gvrs), alreadyWatching)
			return
		}

		// Probe each remaining CRD to get resource count. CRDs with few resources
		// (≤100) are cheap to watch and give full timeline coverage. CRDs with many
		// resources (calico policies, cilium endpoints) are deferred to on-demand.
		const maxEagerResources = 100
		const maxConcurrentProbes = 50
		type probeResult struct {
			gvr   schema.GroupVersionResource
			count int // -1 = no access
		}
		results := make(chan probeResult, len(remaining))
		sem := make(chan struct{}, maxConcurrentProbes)
		for _, gvr := range remaining {
			go func(g schema.GroupVersionResource) {
				sem <- struct{}{}
				defer func() {
					<-sem
					if r := recover(); r != nil {
						log.Printf("[CRD Discovery] Panic probing %s.%s/%s: %v", g.Resource, g.Group, g.Version, r)
						results <- probeResult{gvr: g, count: -1}
					}
				}()
				count := d.probeCount(g)
				results <- probeResult{gvr: g, count: count}
			}(gvr)
		}

		var eager []schema.GroupVersionResource
		var deferredCount int
		var noAccessCount int
		for range remaining {
			r := <-results
			if r.count == -1 {
				noAccessCount++
				continue
			}
			if r.count == -2 {
				// Probe failed (timeout, network error) — defer to be safe
				deferredCount++
				continue
			}
			if r.count <= maxEagerResources {
				eager = append(eager, r.gvr)
			} else {
				deferredCount++
				if d.config.DebugEvents {
					kind := d.gvrToKind(r.gvr)
					log.Printf("[CRD Discovery] Deferring %s (%d resources > %d threshold)", kind, r.count, maxEagerResources)
				}
			}
		}

		log.Printf("Discovered %d watchable CRDs (%d already watching, %d small → eager, %d large → on-demand, %d no access)",
			len(gvrs), alreadyWatching, len(eager), deferredCount, noAccessCount)

		if len(eager) > 0 {
			d.WarmupParallel(eager, 30*time.Second)
		}
	}()
}

// ---------------------------------------------------------------------------
// Discovery status / sync
// ---------------------------------------------------------------------------

// GetDiscoveryStatus returns the current CRD discovery status.
func (d *DynamicResourceCache) GetDiscoveryStatus() CRDDiscoveryStatus {
	if d == nil {
		return CRDDiscoveryIdle
	}

	d.discoveryMu.RLock()
	defer d.discoveryMu.RUnlock()

	return d.discoveryStatus
}

// WaitForSync waits for a resource's cache to be synced (with timeout).
func (d *DynamicResourceCache) WaitForSync(gvr schema.GroupVersionResource, timeout time.Duration) bool {
	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)
}

// IsSynced checks if a resource's cache is synced (non-blocking).
func (d *DynamicResourceCache) IsSynced(gvr schema.GroupVersionResource) bool {
	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return false
	}

	return informer.HasSynced()
}

// ---------------------------------------------------------------------------
// Introspection
// ---------------------------------------------------------------------------

// GetWatchedResources returns a list of GVRs currently being watched.
func (d *DynamicResourceCache) GetWatchedResources() []schema.GroupVersionResource {
	if d == nil {
		return nil
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]schema.GroupVersionResource, 0, len(d.informers))
	for gvr := range d.informers {
		result = append(result, gvr)
	}
	return result
}

// GetInformerCount returns the number of active dynamic informers.
func (d *DynamicResourceCache) GetInformerCount() int {
	if d == nil {
		return 0
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	return len(d.informers)
}

// ---------------------------------------------------------------------------
// CRD discovery callbacks
// ---------------------------------------------------------------------------

// OnCRDDiscoveryComplete registers a callback to be called when CRD discovery completes.
func (d *DynamicResourceCache) OnCRDDiscoveryComplete(callback func()) {
	d.crdCallbacksMu.Lock()
	defer d.crdCallbacksMu.Unlock()
	d.crdCallbacks = append(d.crdCallbacks, callback)
}

func (d *DynamicResourceCache) notifyCRDDiscoveryComplete() {
	d.crdCallbacksMu.RLock()
	defer d.crdCallbacksMu.RUnlock()
	for _, cb := range d.crdCallbacks {
		go cb()
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Stop initiates a non-blocking shutdown of the dynamic cache.
func (d *DynamicResourceCache) Stop() {
	if d == nil {
		return
	}

	d.stopOnce.Do(func() {
		log.Println("Stopping dynamic resource cache")

		d.discoveryMu.Lock()
		if d.discoveryStatus != CRDDiscoveryComplete {
			d.discoveryStatus = CRDDiscoveryComplete
			close(d.discoveryDone)
		}
		d.discoveryMu.Unlock()

		close(d.stopCh)

		go func() {
			done := make(chan struct{})
			go func() {
				d.factory.Shutdown()
				close(done)
			}()
			select {
			case <-done:
				log.Println("Dynamic resource cache factory shutdown complete")
			case <-time.After(5 * time.Second):
				log.Println("Dynamic resource cache factory shutdown taking >5s, abandoning (goroutine will finish on its own)")
			}
		}()
	})
}
