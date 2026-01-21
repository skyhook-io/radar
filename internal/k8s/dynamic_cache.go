package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// DynamicResourceCache provides on-demand caching for CRDs and other dynamic resources
type DynamicResourceCache struct {
	factory   dynamicinformer.DynamicSharedInformerFactory
	informers map[schema.GroupVersionResource]cache.SharedIndexInformer
	stopCh    chan struct{}
	stopOnce  sync.Once
	mu        sync.RWMutex
}

var (
	dynamicResourceCache *DynamicResourceCache
	dynamicCacheOnce     sync.Once
)

// InitDynamicResourceCache initializes the dynamic resource cache
func InitDynamicResourceCache() error {
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
			factory:   factory,
			informers: make(map[schema.GroupVersionResource]cache.SharedIndexInformer),
			stopCh:    make(chan struct{}),
		}

		log.Println("Dynamic resource cache initialized")
	})
	return initErr
}

// GetDynamicResourceCache returns the singleton dynamic cache instance
func GetDynamicResourceCache() *DynamicResourceCache {
	return dynamicResourceCache
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
	}()

	log.Printf("Started watching dynamic resource: %s.%s/%s", gvr.Resource, gvr.Group, gvr.Version)
	return nil
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
