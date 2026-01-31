package k8s

import (
	"context"
	"fmt"
	"log"
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

// DynamicResourceCache provides on-demand caching for CRDs and other dynamic resources
type DynamicResourceCache struct {
	factory      dynamicinformer.DynamicSharedInformerFactory
	informers    map[schema.GroupVersionResource]cache.SharedIndexInformer
	syncComplete map[schema.GroupVersionResource]bool // Track which informers have completed initial sync
	stopCh       chan struct{}
	stopOnce     sync.Once
	mu           sync.RWMutex
	changes      chan ResourceChange // Channel for change notifications (shared with typed cache)
}

var (
	dynamicResourceCache *DynamicResourceCache
	dynamicCacheOnce     sync.Once
	dynamicCacheMu       sync.Mutex
)

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

		factory := dynamicinformer.NewDynamicSharedInformerFactory(
			client,
			0, // no resync - updates come via watch
		)

		dynamicResourceCache = &DynamicResourceCache{
			factory:      factory,
			informers:    make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
			syncComplete: make(map[schema.GroupVersionResource]bool),
			stopCh:       make(chan struct{}),
			changes:      changeCh,
		}

		log.Println("Dynamic resource cache initialized")
	})
	return initErr
}

// GetDynamicResourceCache returns the singleton dynamic cache instance
func GetDynamicResourceCache() *DynamicResourceCache {
	return dynamicResourceCache
}

// ResetDynamicResourceCache stops and clears the dynamic resource cache
// This must be called before ReinitDynamicResourceCache when switching contexts
func ResetDynamicResourceCache() {
	dynamicCacheMu.Lock()
	defer dynamicCacheMu.Unlock()

	if dynamicResourceCache != nil {
		dynamicResourceCache.Stop()
		dynamicResourceCache = nil
	}
	dynamicCacheOnce = sync.Once{}
}

// ReinitDynamicResourceCache reinitializes the dynamic cache after a context switch
// Must call ResetDynamicResourceCache first
func ReinitDynamicResourceCache(changeCh chan ResourceChange) error {
	return InitDynamicResourceCache(changeCh)
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

	d.mu.Lock()
	defer d.mu.Unlock()

	// Already watching
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

	log.Printf("Started watching dynamic resource: %s.%s/%s", gvr.Resource, gvr.Group, gvr.Version)
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

	// Skip ADD events during initial sync - they represent existing resources, not new creations
	if op == "add" {
		d.mu.RLock()
		synced := d.syncComplete[gvr]
		d.mu.RUnlock()

		if !synced {
			if DebugEvents {
				log.Printf("[DEBUG] Skipping dynamic initial sync add event: %s/%s/%s", kind, namespace, name)
			}
			timeline.RecordDrop(kind, namespace, name, timeline.DropReasonAlreadySeen, op)
			return
		}
	}

	// Compute diff for updates
	var diff *DiffInfo
	if op == "update" && oldObj != nil && obj != nil {
		diff = ComputeDiff(kind, oldObj, obj)
	}

	// Record to timeline store
	recordToTimelineStore(kind, namespace, name, uid, op, oldObj, obj)

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

// WarmupParallel starts watching multiple resources in parallel and waits for all to sync
func (d *DynamicResourceCache) WarmupParallel(gvrs []schema.GroupVersionResource, timeout time.Duration) {
	if d == nil || len(gvrs) == 0 {
		return
	}

	// Start all informers in parallel (non-blocking)
	var validGVRs []schema.GroupVersionResource
	for _, gvr := range gvrs {
		if err := d.EnsureWatching(gvr); err == nil {
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
		"Application",    // ArgoCD
		"ApplicationSet", // ArgoCD
		"AppProject",     // ArgoCD
	}

	var gvrs []schema.GroupVersionResource
	for _, kind := range commonCRDs {
		if gvr, ok := discovery.GetGVR(kind); ok {
			gvrs = append(gvrs, gvr)
			log.Printf("Warming up CRD: %s", kind)
		}
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
