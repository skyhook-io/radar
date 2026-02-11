package k8s

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

	"github.com/skyhook-io/radar/internal/timeline"
)

// CRDDiscoveryStatus represents the state of CRD discovery
type CRDDiscoveryStatus string

const (
	CRDDiscoveryIdle        CRDDiscoveryStatus = "idle"        // Not started
	CRDDiscoveryInProgress  CRDDiscoveryStatus = "discovering" // Discovery in progress
	CRDDiscoveryComplete    CRDDiscoveryStatus = "ready"       // Discovery complete
)

// DynamicResourceCache provides on-demand caching for CRDs and other dynamic resources
type DynamicResourceCache struct {
	factory         dynamicinformer.DynamicSharedInformerFactory
	informers       map[schema.GroupVersionResource]cache.SharedIndexInformer
	syncComplete    map[schema.GroupVersionResource]bool // Track which informers have completed initial sync
	stopCh          chan struct{}
	stopOnce        sync.Once
	mu              sync.RWMutex
	changes         chan ResourceChange // Channel for change notifications (shared with typed cache)
	discoveryStatus CRDDiscoveryStatus  // Status of CRD discovery
	discoveryMu     sync.RWMutex        // Mutex for discovery status
	discoveryDone   chan struct{}        // closed when DiscoverAllCRDs() completes
}

var (
	dynamicResourceCache *DynamicResourceCache
	dynamicCacheOnce     sync.Once
	dynamicCacheMu       sync.Mutex

	// Callbacks for CRD discovery completion
	crdDiscoveryCallbacks   []func()
	crdDiscoveryCallbacksMu sync.RWMutex
)

// OnCRDDiscoveryComplete registers a callback to be called when CRD discovery completes
func OnCRDDiscoveryComplete(callback func()) {
	crdDiscoveryCallbacksMu.Lock()
	defer crdDiscoveryCallbacksMu.Unlock()
	crdDiscoveryCallbacks = append(crdDiscoveryCallbacks, callback)
}

// notifyCRDDiscoveryComplete calls all registered callbacks
func notifyCRDDiscoveryComplete() {
	crdDiscoveryCallbacksMu.RLock()
	defer crdDiscoveryCallbacksMu.RUnlock()
	for _, cb := range crdDiscoveryCallbacks {
		go cb()
	}
}

// InitDynamicResourceCache initializes the dynamic resource cache
// If changeCh is provided, change notifications will be sent to it (for SSE)
func InitDynamicResourceCache(changeCh chan ResourceChange) error {
	var initErr error
	dynamicCacheOnce.Do(func() {
		client := GetDynamicClient()
		if client == nil {
			initErr = fmt.Errorf("dynamic client not initialized")
			return
		}

		// Use namespace-scoped factory if the user only has namespace-level access
		var factory dynamicinformer.DynamicSharedInformerFactory
		if permResult := GetCachedPermissionResult(); permResult != nil && permResult.NamespaceScoped && permResult.Namespace != "" {
			factory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(
				client, 0, permResult.Namespace, nil,
			)
			log.Printf("Using namespace-scoped dynamic informers for namespace %q", permResult.Namespace)
		} else {
			factory = dynamicinformer.NewDynamicSharedInformerFactory(
				client,
				0, // no resync - updates come via watch
			)
		}

		dynamicResourceCache = &DynamicResourceCache{
			factory:         factory,
			informers:       make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
			syncComplete:    make(map[schema.GroupVersionResource]bool),
			stopCh:          make(chan struct{}),
			changes:         changeCh,
			discoveryStatus: CRDDiscoveryIdle,
			discoveryDone:   make(chan struct{}),
		}

		log.Println("Dynamic resource cache initialized")
	})
	return initErr
}

// GetDynamicResourceCache returns the singleton dynamic cache instance
func GetDynamicResourceCache() *DynamicResourceCache {
	return dynamicResourceCache
}

// ResetDynamicResourceCache stops and clears the dynamic resource cache so it
// can be reinitialized for a new cluster after context switch.
func ResetDynamicResourceCache() {
	dynamicCacheMu.Lock()
	defer dynamicCacheMu.Unlock()

	if dynamicResourceCache != nil {
		dynamicResourceCache.Stop()
		dynamicResourceCache = nil
	}
	dynamicCacheOnce = sync.Once{}
}

// EnsureWatching starts watching a resource type if not already watching
// The sync happens asynchronously - callers should use WaitForSync if they need to wait
func (d *DynamicResourceCache) EnsureWatching(gvr schema.GroupVersionResource) error {
	if d == nil {
		return fmt.Errorf("dynamic resource cache not initialized")
	}

	// Check if resource supports list/watch verbs before attempting to watch
	// Resources like selfsubjectreviews and tokenreviews are create-only
	discovery := GetResourceDiscovery()
	if discovery != nil && !discovery.SupportsWatchGVR(gvr) {
		return fmt.Errorf("resource %s.%s/%s does not support list/watch", gvr.Resource, gvr.Group, gvr.Version)
	}

	// Quick check under read lock — skip if already watching
	d.mu.RLock()
	_, exists := d.informers[gvr]
	d.mu.RUnlock()
	if exists {
		return nil
	}

	// If CRD discovery is in progress, wait for it to finish instead of
	// probing independently. DiscoverAllCRDs() probes all CRDs efficiently
	// in parallel — individual probes here would compete for the same QPS
	// budget and cause throttling contention on clusters with many CRDs.
	// The discoveryDone channel is closed when DiscoverAllCRDs() completes,
	// when Stop() is called, or if the discovery goroutine panics.
	if d.GetDiscoveryStatus() == CRDDiscoveryInProgress {
		select {
		case <-d.discoveryDone:
		case <-time.After(45 * time.Second): // covers API resource fetch + parallel probes + 30s sync timeout
			log.Printf("[dynamic cache] Timeout waiting for CRD discovery, probing %s independently", gvr.Resource)
		}

		// Re-check: warmup may have created this informer
		d.mu.RLock()
		_, exists = d.informers[gvr]
		d.mu.RUnlock()
		if exists {
			return nil
		}
	}

	// Probe access BEFORE acquiring write lock — this is a network call and
	// must not hold the mutex. Prevents creating reflectors that would
	// endlessly retry on forbidden/unauthorized resources.
	if err := d.probeAccess(gvr); err != nil {
		return fmt.Errorf("no access to %s.%s/%s: %w", gvr.Resource, gvr.Group, gvr.Version, err)
	}

	return d.startWatching(gvr)
}

// startWatching creates and starts an informer for a GVR (no access probe).
// Callers must verify access before calling this method.
func (d *DynamicResourceCache) startWatching(gvr schema.GroupVersionResource) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Re-check after acquiring write lock (another goroutine may have started it)
	if _, exists := d.informers[gvr]; exists {
		return nil
	}

	// Create informer for this GVR
	informer := d.factory.ForResource(gvr).Informer()
	d.informers[gvr] = informer

	// Get the kind name from discovery (e.g., "Rollout" from "rollouts")
	kind := gvrToKind(gvr)

	// Add event handlers for change tracking (timeline + SSE)
	d.addDynamicChangeHandlers(informer, kind, gvr)

	// Start the informer
	go informer.Run(d.stopCh)

	// Log the current count of dynamic informers for diagnostics
	informerCount := len(d.informers)
	log.Printf("Started watching dynamic resource: %s.%s/%s (total dynamic informers: %d)", gvr.Resource, gvr.Group, gvr.Version, informerCount)

	// Wait for initial sync asynchronously (non-blocking)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
			log.Printf("Warning: cache sync timeout for %v", gvr)
		} else {
			log.Printf("Dynamic resource synced: %s.%s/%s", gvr.Resource, gvr.Group, gvr.Version)
		}

		// Mark this informer as sync complete - now we can record ADD events for it
		d.mu.Lock()
		d.syncComplete[gvr] = true
		d.mu.Unlock()
	}()
	return nil
}

// probeAccess does a quick list with limit=1 to verify the user can access this resource.
// This prevents creating informers/reflectors that would endlessly retry on 403/401 errors.
func (d *DynamicResourceCache) probeAccess(gvr schema.GroupVersionResource) error {
	client := GetDynamicClient()
	if client == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use namespace-scoped list if we're running in namespace-scoped mode
	var err error
	if permResult := GetCachedPermissionResult(); permResult != nil && permResult.NamespaceScoped && permResult.Namespace != "" {
		_, err = client.Resource(gvr).Namespace(permResult.Namespace).List(ctx, metav1.ListOptions{Limit: 1})
	} else {
		_, err = client.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	}

	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "forbidden") || strings.Contains(errLower, "unauthorized") {
			return err
		}
		// Non-auth errors (e.g. network timeout) — allow the informer to be created;
		// the reflector has its own retry logic for transient failures
		log.Printf("[dynamic cache] Probe for %s.%s/%s returned non-auth error (allowing): %v", gvr.Resource, gvr.Group, gvr.Version, err)
	}

	return nil
}

// gvrToKind converts a GVR to a kind name using resource discovery
// Falls back to capitalizing the singular resource name
func gvrToKind(gvr schema.GroupVersionResource) string {
	discovery := GetResourceDiscovery()
	if discovery != nil {
		if kind := discovery.GetKindForGVR(gvr); kind != "" {
			return kind
		}
	}
	// Fallback: capitalize and singularize the resource name
	// e.g., "rollouts" -> "Rollout"
	name := gvr.Resource
	if len(name) > 1 && name[len(name)-1] == 's' {
		name = name[:len(name)-1]
	}
	if len(name) > 0 {
		return strings.ToUpper(name[:1]) + name[1:]
	}
	return name
}

// addDynamicChangeHandlers registers event handlers for change notifications on dynamic resources
func (d *DynamicResourceCache) addDynamicChangeHandlers(inf cache.SharedIndexInformer, kind string, gvr schema.GroupVersionResource) {
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			d.enqueueDynamicChange(kind, gvr, obj, nil, "add")
		},
		UpdateFunc: func(oldObj, newObj any) {
			d.enqueueDynamicChange(kind, gvr, newObj, oldObj, "update")
		},
		DeleteFunc: func(obj any) {
			d.enqueueDynamicChange(kind, gvr, obj, nil, "delete")
		},
	})
}

// enqueueDynamicChange records a change and sends notification for dynamic (unstructured) resources
func (d *DynamicResourceCache) enqueueDynamicChange(kind string, gvr schema.GroupVersionResource, obj any, oldObj any, op string) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		// Handle tombstone for deleted objects
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

	// Track event received
	timeline.IncrementReceived(kind)

	// During initial sync, still record to timeline store (historical events)
	// but skip SSE notification
	isSyncAdd := false
	if op == "add" {
		d.mu.RLock()
		synced := d.syncComplete[gvr]
		d.mu.RUnlock()

		if !synced {
			isSyncAdd = true
			if DebugEvents {
				log.Printf("[DEBUG] Dynamic initial sync add event: %s/%s/%s (recording historical only)", kind, namespace, name)
			}
		}
	}

	// Compute diff for updates
	var diff *DiffInfo
	if op == "update" && oldObj != nil && obj != nil {
		diff = ComputeDiff(kind, oldObj, obj)
	}

	// Record to timeline store (handles sync vs real add internally)
	recordToTimelineStore(kind, namespace, name, uid, op, oldObj, obj)

	// Skip SSE notification during initial sync
	if isSyncAdd {
		return
	}

	// Send to change channel for SSE if configured
	if d.changes != nil {
		change := ResourceChange{
			Kind:      kind,
			Namespace: namespace,
			Name:      name,
			UID:       uid,
			Operation: op,
			Diff:      diff,
		}

		// Non-blocking send
		select {
		case d.changes <- change:
		default:
			// Channel full, drop event
			timeline.RecordDrop(kind, namespace, name,
				timeline.DropReasonChannelFull, op)
			if DebugEvents {
				log.Printf("[DEBUG] Dynamic change channel full, dropped: %s/%s/%s op=%s", kind, namespace, name, op)
			}
		}
	}

	// Track successful recording (for dynamic resources that get sent to SSE)
	timeline.IncrementRecorded(kind)
}

// WaitForSync waits for a resource's cache to be synced (with timeout)
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

// IsSynced checks if a resource's cache is synced (non-blocking)
func (d *DynamicResourceCache) IsSynced(gvr schema.GroupVersionResource) bool {
	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return false
	}

	return informer.HasSynced()
}

// List returns all resources of a given GVR, optionally filtered by namespace
// This is non-blocking - returns whatever data is available immediately
func (d *DynamicResourceCache) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	// Ensure we're watching this resource (non-blocking)
	if err := d.EnsureWatching(gvr); err != nil {
		return nil, err
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	// Return whatever data is available - don't block waiting for sync
	// The cache will populate via watch events
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
			// Strip managed fields to reduce memory
			u = stripManagedFieldsUnstructured(u)
			result = append(result, u)
		}
	}

	return result, nil
}

// ListBlocking returns all resources, waiting for cache sync first
// Use this when you need guaranteed complete data
func (d *DynamicResourceCache) ListBlocking(gvr schema.GroupVersionResource, namespace string, timeout time.Duration) ([]*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	// Ensure we're watching this resource
	if err := d.EnsureWatching(gvr); err != nil {
		return nil, err
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	// Wait for sync
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
			u = stripManagedFieldsUnstructured(u)
			result = append(result, u)
		}
	}

	return result, nil
}

// Get returns a single resource by namespace and name
// Waits briefly for sync if cache is empty (for better UX on specific resource requests)
func (d *DynamicResourceCache) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if d == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	// Ensure we're watching this resource
	if err := d.EnsureWatching(gvr); err != nil {
		return nil, err
	}

	d.mu.RLock()
	informer, exists := d.informers[gvr]
	d.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("informer not found for %v", gvr)
	}

	// Build the key
	var key string
	if namespace != "" {
		key = namespace + "/" + name
	} else {
		key = name
	}

	// Try to get immediately
	item, exists, err := informer.GetIndexer().GetByKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to get resource: %w", err)
	}

	// If not found and cache not synced, wait briefly and retry
	if !exists && !informer.HasSynced() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)

		// Retry after sync
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

	// Strip managed fields
	return stripManagedFieldsUnstructured(u), nil
}

// ListWithSelector returns resources matching a label selector
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

// GetWatchedResources returns a list of GVRs currently being watched
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

// GetInformerCount returns the number of active dynamic informers
func (d *DynamicResourceCache) GetInformerCount() int {
	if d == nil {
		return 0
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	return len(d.informers)
}

// GetDiscoveryStatus returns the current CRD discovery status
func (d *DynamicResourceCache) GetDiscoveryStatus() CRDDiscoveryStatus {
	if d == nil {
		return CRDDiscoveryIdle
	}

	d.discoveryMu.RLock()
	defer d.discoveryMu.RUnlock()

	return d.discoveryStatus
}

// DiscoverAllCRDs discovers and starts watching all CRDs that support list/watch.
// This runs asynchronously and updates the discovery status.
// Call GetDiscoveryStatus() to check progress.
func (d *DynamicResourceCache) DiscoverAllCRDs() {
	if d == nil {
		log.Println("[CRD Discovery] Cache is nil, skipping")
		return
	}

	// Check if already discovering or complete
	d.discoveryMu.Lock()
	if d.discoveryStatus != CRDDiscoveryIdle {
		log.Printf("[CRD Discovery] Already in status: %s, skipping", d.discoveryStatus)
		d.discoveryMu.Unlock()
		return
	}
	d.discoveryStatus = CRDDiscoveryInProgress
	d.discoveryMu.Unlock()
	log.Println("[CRD Discovery] Starting CRD discovery...")

	// Run discovery in background
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

			// Notify callbacks to trigger topology update
			notifyCRDDiscoveryComplete()
		}()

		discovery := GetResourceDiscovery()
		if discovery == nil {
			log.Println("Resource discovery not available for CRD discovery")
			return
		}

		resources, err := discovery.GetAPIResources()
		if err != nil {
			log.Printf("Failed to get API resources for CRD discovery: %v", err)
			return
		}

		// Collect watchable CRDs, keeping only the most stable version per group+resource.
		best := make(map[string]schema.GroupVersionResource) // key: "group/resource"
		for _, res := range resources {
			if !res.IsCRD {
				continue
			}
			// Check if it supports list/watch
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
				if !isMoreStableVersion(res.Version, existing.Version) {
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

		log.Printf("Discovering %d CRDs...", len(gvrs))
		d.WarmupParallel(gvrs, 30*time.Second)
	}()
}

// WarmupParallel starts watching multiple resources in parallel and waits for all to sync
func (d *DynamicResourceCache) WarmupParallel(gvrs []schema.GroupVersionResource, timeout time.Duration) {
	if d == nil || len(gvrs) == 0 {
		return
	}

	// Phase 1: Probe access for all GVRs concurrently (network calls).
	// Limit concurrency to avoid overwhelming the API server on clusters
	// with 100+ CRDs and to keep probes within their 5s timeout.
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

	// Phase 2: Create informers for accessible resources (fast, no network)
	var validGVRs []schema.GroupVersionResource
	for _, gvr := range accessibleGVRs {
		if err := d.startWatching(gvr); err == nil {
			validGVRs = append(validGVRs, gvr)
		}
	}

	if len(validGVRs) == 0 {
		return
	}

	// Collect all HasSynced funcs
	d.mu.RLock()
	syncFuncs := make([]cache.InformerSynced, 0, len(validGVRs))
	for _, gvr := range validGVRs {
		if informer, ok := d.informers[gvr]; ok {
			syncFuncs = append(syncFuncs, informer.HasSynced)
		}
	}
	d.mu.RUnlock()

	// Wait for all to sync with timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if !cache.WaitForCacheSync(ctx.Done(), syncFuncs...) {
		log.Printf("Warning: not all dynamic caches synced within timeout")
	} else {
		log.Printf("All %d dynamic resources synced", len(syncFuncs))
	}
}

// Stop gracefully shuts down the dynamic cache
func (d *DynamicResourceCache) Stop() {
	if d == nil {
		return
	}

	d.stopOnce.Do(func() {
		log.Println("Stopping dynamic resource cache")

		// Unblock any HTTP handlers waiting for discovery to finish
		d.discoveryMu.Lock()
		if d.discoveryStatus != CRDDiscoveryComplete {
			d.discoveryStatus = CRDDiscoveryComplete
			close(d.discoveryDone)
		}
		d.discoveryMu.Unlock()

		close(d.stopCh)
		d.factory.Shutdown()
	})
}

// WarmupCommonCRDs starts watching common CRDs (Rollouts, Workflows, etc.) at startup
// This ensures they appear in the initial timeline before the first topology request
func WarmupCommonCRDs() {
	cache := GetDynamicResourceCache()
	if cache == nil {
		return
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return
	}

	// Common CRDs that should be warmed up for timeline visibility
	commonCRDs := []string{
		"Rollout",        // Argo Rollouts
		"Workflow",       // Argo Workflows
		"CronWorkflow",   // Argo Workflows
		"Certificate",    // cert-manager
		"GitRepository",  // FluxCD source
		"OCIRepository",  // FluxCD source
		"HelmRepository", // FluxCD source
		"Kustomization",  // FluxCD kustomize
		"HelmRelease",    // FluxCD helm
		"Alert",          // FluxCD notification
		"ApplicationSet", // ArgoCD
		"AppProject",     // ArgoCD
		"Gateway",        // Gateway API
		"HTTPRoute",      // Gateway API
		"GRPCRoute",      // Gateway API
		"TCPRoute",       // Gateway API
		"TLSRoute",       // Gateway API
	}

	var gvrs []schema.GroupVersionResource
	for _, kind := range commonCRDs {
		if gvr, ok := discovery.GetGVR(kind); ok {
			gvrs = append(gvrs, gvr)
			log.Printf("Warming up CRD: %s", kind)
		}
	}

	// ArgoCD Application needs group-qualified lookup to avoid collision with
	// the Kubernetes SIG Application CRD (app.k8s.io), which shares the same Kind name.
	if gvr, ok := discovery.GetGVRWithGroup("Application", "argoproj.io"); ok {
		gvrs = append(gvrs, gvr)
		log.Printf("Warming up CRD: Application (argoproj.io)")
	}

	if len(gvrs) > 0 {
		cache.WarmupParallel(gvrs, 10*time.Second)
	}
}

// stripManagedFieldsUnstructured removes managed fields from unstructured objects
func stripManagedFieldsUnstructured(u *unstructured.Unstructured) *unstructured.Unstructured {
	if u == nil {
		return nil
	}

	// Create a copy to avoid mutating the cached object
	copy := u.DeepCopy()

	// Remove managed fields
	unstructured.RemoveNestedField(copy.Object, "metadata", "managedFields")

	// Remove last-applied-configuration annotation
	annotations := copy.GetAnnotations()
	if annotations != nil {
		delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
		if len(annotations) == 0 {
			copy.SetAnnotations(nil)
		} else {
			copy.SetAnnotations(annotations)
		}
	}

	return copy
}

// ListDirect fetches resources directly from the API (bypasses cache)
// Use this sparingly - prefer cached List() for performance
func (d *DynamicResourceCache) ListDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	client := GetDynamicClient()
	if client == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	var list *unstructured.UnstructuredList
	var err error

	if namespace != "" {
		list, err = client.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = client.Resource(gvr).List(ctx, metav1.ListOptions{})
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}

	result := make([]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		result[i] = stripManagedFieldsUnstructured(&list.Items[i])
	}

	return result, nil
}

// GetDirect fetches a single resource directly from the API (bypasses cache)
func (d *DynamicResourceCache) GetDirect(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	client := GetDynamicClient()
	if client == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	var u *unstructured.Unstructured
	var err error

	if namespace != "" {
		u, err = client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	} else {
		u, err = client.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	}

	if err != nil {
		return nil, err
	}

	return stripManagedFieldsUnstructured(u), nil
}
