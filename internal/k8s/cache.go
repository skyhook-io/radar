package k8s

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listersautoscalingv2 "k8s.io/client-go/listers/autoscaling/v2"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersnetworkingv1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/internal/timeline"
)

// DebugEvents enables verbose event debugging when true (set via --debug-events flag)
var DebugEvents bool

// initialSyncComplete is set to true after the initial cache sync completes.
// During initial sync, "add" events are skipped since they represent existing
// resources, not new creations. Only adds after sync are recorded.
var initialSyncComplete bool

// ResourceCache provides fast, eventually-consistent access to K8s resources
// using SharedInformers. Optimized for small-mid sized clusters.
type ResourceCache struct {
	factory          informers.SharedInformerFactory
	changes          chan ResourceChange
	stopCh           chan struct{}
	stopOnce         sync.Once
	secretsEnabled   bool            // Whether secrets informer is running (requires RBAC)
	enabledResources map[string]bool // Which resource types have informers running
}

// ResourceChange represents a resource change event
type ResourceChange struct {
	Kind      string // "Service", "Deployment", "Pod", etc.
	Namespace string
	Name      string
	UID       string
	Operation string    // "add", "update", "delete"
	Diff      *DiffInfo // Diff details for updates (from history)
}

var (
	resourceCache *ResourceCache
	cacheOnce     sync.Once
	cacheMu       sync.Mutex
)

// dropManagedFields reduces memory usage by removing heavy metadata
func dropManagedFields(obj any) (any, error) {
	if meta, ok := obj.(metav1.Object); ok {
		meta.SetManagedFields(nil)
	}

	// Special handling for Events - aggressively strip to essentials
	if event, ok := obj.(*corev1.Event); ok {
		return &corev1.Event{
			ObjectMeta: metav1.ObjectMeta{
				Name:              event.Name,
				Namespace:         event.Namespace,
				UID:               event.UID,
				ResourceVersion:   event.ResourceVersion,
				CreationTimestamp: event.CreationTimestamp,
			},
			InvolvedObject: event.InvolvedObject,
			Reason:         event.Reason,
			Message:        event.Message,
			Type:           event.Type,
			Count:          event.Count,
			FirstTimestamp: event.FirstTimestamp,
			LastTimestamp:  event.LastTimestamp,
		}, nil
	}

	// Drop heavy annotations from common resources
	switch obj.(type) {
	case *corev1.Pod, *corev1.Service, *corev1.Node, *corev1.Namespace,
		*corev1.PersistentVolumeClaim, *corev1.ConfigMap, *corev1.Secret,
		*appsv1.Deployment, *appsv1.DaemonSet, *appsv1.StatefulSet, *appsv1.ReplicaSet,
		*networkingv1.Ingress,
		*batchv1.Job, *batchv1.CronJob:
		if meta, ok := obj.(metav1.Object); ok && meta.GetAnnotations() != nil {
			delete(meta.GetAnnotations(), "kubectl.kubernetes.io/last-applied-configuration")
		}
	}

	return obj, nil
}

// InitResourceCache initializes the resource cache
func InitResourceCache() error {
	var initErr error
	cacheOnce.Do(func() {
		if k8sClient == nil {
			initErr = fmt.Errorf("cannot create resource cache: k8s client not initialized")
			return
		}

		stopCh := make(chan struct{})
		changes := make(chan ResourceChange, 10000)

		// Check RBAC permissions for all resource types before creating informers
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		permResult := CheckResourcePermissions(ctx)
		cancel()
		perms := permResult.Perms

		// Create factory — namespace-scoped if user only has namespace-level access
		factoryOpts := []informers.SharedInformerOption{
			informers.WithTransform(dropManagedFields),
		}
		if permResult.NamespaceScoped && permResult.Namespace != "" {
			factoryOpts = append(factoryOpts, informers.WithNamespace(permResult.Namespace))
			log.Printf("Using namespace-scoped informers for namespace %q", permResult.Namespace)
		}

		factory := informers.NewSharedInformerFactoryWithOptions(
			k8sClient,
			0, // no resync - updates come via watch
			factoryOpts...,
		)

		// Build map of enabled resources
		enabled := map[string]bool{
			"pods":                     perms.Pods,
			"services":                 perms.Services,
			"deployments":              perms.Deployments,
			"daemonsets":               perms.DaemonSets,
			"statefulsets":             perms.StatefulSets,
			"replicasets":              perms.ReplicaSets,
			"ingresses":               perms.Ingresses,
			"configmaps":              perms.ConfigMaps,
			"secrets":                  perms.Secrets,
			"events":                   perms.Events,
			"persistentvolumeclaims":   perms.PersistentVolumeClaims,
			"nodes":                    perms.Nodes,
			"namespaces":               perms.Namespaces,
			"jobs":                     perms.Jobs,
			"cronjobs":                 perms.CronJobs,
			"horizontalpodautoscalers": perms.HorizontalPodAutoscalers,
		}

		// Conditionally create informers and register handlers
		var syncFuncs []cache.InformerSynced
		var handlerErrors []error

		type informerSetup struct {
			key     string
			kind    string
			setup   func() cache.SharedIndexInformer
			isEvent bool // Uses special K8s event handler
		}

		setups := []informerSetup{
			{"services", "Service", func() cache.SharedIndexInformer { return factory.Core().V1().Services().Informer() }, false},
			{"pods", "Pod", func() cache.SharedIndexInformer { return factory.Core().V1().Pods().Informer() }, false},
			{"nodes", "Node", func() cache.SharedIndexInformer { return factory.Core().V1().Nodes().Informer() }, false},
			{"namespaces", "Namespace", func() cache.SharedIndexInformer { return factory.Core().V1().Namespaces().Informer() }, false},
			{"configmaps", "ConfigMap", func() cache.SharedIndexInformer { return factory.Core().V1().ConfigMaps().Informer() }, false},
			{"secrets", "Secret", func() cache.SharedIndexInformer { return factory.Core().V1().Secrets().Informer() }, false},
			{"events", "Event", func() cache.SharedIndexInformer { return factory.Core().V1().Events().Informer() }, true},
			{"persistentvolumeclaims", "PersistentVolumeClaim", func() cache.SharedIndexInformer { return factory.Core().V1().PersistentVolumeClaims().Informer() }, false},
			{"deployments", "Deployment", func() cache.SharedIndexInformer { return factory.Apps().V1().Deployments().Informer() }, false},
			{"daemonsets", "DaemonSet", func() cache.SharedIndexInformer { return factory.Apps().V1().DaemonSets().Informer() }, false},
			{"statefulsets", "StatefulSet", func() cache.SharedIndexInformer { return factory.Apps().V1().StatefulSets().Informer() }, false},
			{"replicasets", "ReplicaSet", func() cache.SharedIndexInformer { return factory.Apps().V1().ReplicaSets().Informer() }, false},
			{"ingresses", "Ingress", func() cache.SharedIndexInformer { return factory.Networking().V1().Ingresses().Informer() }, false},
			{"jobs", "Job", func() cache.SharedIndexInformer { return factory.Batch().V1().Jobs().Informer() }, false},
			{"cronjobs", "CronJob", func() cache.SharedIndexInformer { return factory.Batch().V1().CronJobs().Informer() }, false},
			{"horizontalpodautoscalers", "HorizontalPodAutoscaler", func() cache.SharedIndexInformer { return factory.Autoscaling().V2().HorizontalPodAutoscalers().Informer() }, false},
		}

		enabledCount := 0
		for _, s := range setups {
			if !enabled[s.key] {
				continue
			}
			enabledCount++
			inf := s.setup()
			if s.isEvent {
				handlerErrors = append(handlerErrors, addK8sEventHandlers(inf, changes))
			} else {
				handlerErrors = append(handlerErrors, addChangeHandlers(inf, s.kind, changes))
			}
			syncFuncs = append(syncFuncs, inf.HasSynced)
		}

		for _, err := range handlerErrors {
			if err != nil {
				initErr = fmt.Errorf("failed to register event handlers: %w", err)
				return
			}
		}

		if enabledCount == 0 {
			log.Printf("Warning: No resource types are accessible (all RBAC checks failed)")
			// Still create a valid but empty cache so the server can run
			resourceCache = &ResourceCache{
				factory:          factory,
				changes:          changes,
				stopCh:           stopCh,
				secretsEnabled:   false,
				enabledResources: enabled,
			}
			initialSyncComplete = true
			return
		}

		// Start all informers
		factory.Start(stopCh)

		log.Printf("Starting resource cache with SharedInformers for %d/%d resource types", enabledCount, len(setups))
		syncStart := time.Now()

		// Wait for caches to sync
		if !cache.WaitForCacheSync(stopCh, syncFuncs...) {
			close(stopCh)
			initErr = fmt.Errorf("failed to sync resource caches")
			return
		}

		log.Printf("Resource caches synced successfully in %v", time.Since(syncStart))

		// Mark initial sync as complete - now we can start recording "add" events
		initialSyncComplete = true

		resourceCache = &ResourceCache{
			factory:          factory,
			changes:          changes,
			stopCh:           stopCh,
			secretsEnabled:   enabled["secrets"],
			enabledResources: enabled,
		}
	})
	return initErr
}

// GetResourceCache returns the singleton cache instance
func GetResourceCache() *ResourceCache {
	return resourceCache
}

// ResetResourceCache stops and clears the resource cache so it can be
// reinitialized for a new cluster after context switch.
func ResetResourceCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if resourceCache != nil {
		resourceCache.Stop()
		resourceCache = nil
	}
	cacheOnce = sync.Once{}
	initialSyncComplete = false
}

// addChangeHandlers registers event handlers for change notifications
// Returns an error if handler registration fails (rare, but indicates a broken informer)
func addChangeHandlers(inf cache.SharedIndexInformer, kind string, ch chan<- ResourceChange) error {
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			enqueueChange(ch, kind, obj, nil, "add")
		},
		UpdateFunc: func(oldObj, newObj any) {
			enqueueChange(ch, kind, newObj, oldObj, "update")
		},
		DeleteFunc: func(obj any) {
			enqueueChange(ch, kind, obj, nil, "delete")
		},
	})
	if err != nil {
		return fmt.Errorf("failed to register %s event handler: %w", kind, err)
	}
	return nil
}

// addK8sEventHandlers registers special handlers for K8s Events
// K8s Events are stored in the timeline store as "k8s_event" source type
// Returns an error if handler registration fails
func addK8sEventHandlers(inf cache.SharedIndexInformer, ch chan<- ResourceChange) error {
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			// Still send to the change channel for SSE broadcasting
			meta, ok := obj.(metav1.Object)
			if !ok {
				return
			}
			change := ResourceChange{
				Kind:      "Event",
				Namespace: meta.GetNamespace(),
				Name:      meta.GetName(),
				UID:       string(meta.GetUID()),
				Operation: "add",
			}
			select {
			case ch <- change:
			default:
				// Channel full, drop event
				timeline.RecordDrop("Event", meta.GetNamespace(), meta.GetName(),
					timeline.DropReasonChannelFull, "add")
				if DebugEvents {
					log.Printf("[DEBUG] K8s Event channel full, dropped: Event/%s/%s", meta.GetNamespace(), meta.GetName())
				}
			}

			// Record K8s Event to timeline store
			recordK8sEventToTimeline(obj)
		},
		UpdateFunc: func(oldObj, newObj any) {
			// K8s Events update when count changes - record to timeline
			meta, ok := newObj.(metav1.Object)
			if !ok {
				return
			}
			change := ResourceChange{
				Kind:      "Event",
				Namespace: meta.GetNamespace(),
				Name:      meta.GetName(),
				UID:       string(meta.GetUID()),
				Operation: "update",
			}
			select {
			case ch <- change:
			default:
				// Channel full, drop event
				timeline.RecordDrop("Event", meta.GetNamespace(), meta.GetName(),
					timeline.DropReasonChannelFull, "update")
				if DebugEvents {
					log.Printf("[DEBUG] K8s Event channel full, dropped: Event/%s/%s op=update", meta.GetNamespace(), meta.GetName())
				}
			}

			// Update K8s Event in timeline store (with new count)
			recordK8sEventToTimeline(newObj)
		},
		DeleteFunc: func(obj any) {
			meta, ok := obj.(metav1.Object)
			if !ok {
				if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					meta, ok = tombstone.Obj.(metav1.Object)
					if !ok {
						return
					}
				} else {
					return
				}
			}
			change := ResourceChange{
				Kind:      "Event",
				Namespace: meta.GetNamespace(),
				Name:      meta.GetName(),
				UID:       string(meta.GetUID()),
				Operation: "delete",
			}
			select {
			case ch <- change:
			default:
				// Channel full, drop event
				timeline.RecordDrop("Event", meta.GetNamespace(), meta.GetName(),
					timeline.DropReasonChannelFull, "delete")
				if DebugEvents {
					log.Printf("[DEBUG] K8s Event channel full, dropped: Event/%s/%s op=delete", meta.GetNamespace(), meta.GetName())
				}
			}
			// Note: We don't need to delete K8s events from timeline store
			// as they represent things that happened and should remain in history
		},
	})
	if err != nil {
		return fmt.Errorf("failed to register Event handler: %w", err)
	}
	return nil
}

// recordK8sEventToTimeline records a K8s Event to the timeline store
func recordK8sEventToTimeline(obj any) {
	event, ok := obj.(*corev1.Event)
	if !ok {
		return
	}

	store := timeline.GetStore()
	if store == nil {
		return
	}

	// Track K8s Event recording in metrics when debug mode is enabled
	if DebugEvents {
		timeline.IncrementReceived("K8sEvent:" + event.InvolvedObject.Kind)
	}

	// Lookup owner reference for the involved object
	var owner *timeline.OwnerInfo
	cache := GetResourceCache()
	if cache != nil {
		if event.InvolvedObject.Kind == "Pod" && cache.Pods() != nil {
			if pod, err := cache.Pods().Pods(event.Namespace).Get(event.InvolvedObject.Name); err == nil && pod != nil {
				for _, ref := range pod.OwnerReferences {
					if ref.Controller != nil && *ref.Controller {
						owner = &timeline.OwnerInfo{Kind: ref.Kind, Name: ref.Name}
						break
					}
				}
			}
		} else if event.InvolvedObject.Kind == "ReplicaSet" && cache.ReplicaSets() != nil {
			if rs, err := cache.ReplicaSets().ReplicaSets(event.Namespace).Get(event.InvolvedObject.Name); err == nil && rs != nil {
				for _, ref := range rs.OwnerReferences {
					if ref.Controller != nil && *ref.Controller {
						owner = &timeline.OwnerInfo{Kind: ref.Kind, Name: ref.Name}
						break
					}
				}
			}
		}
	}

	// Create timeline event using the converter
	timelineEvent := timeline.NewK8sEventTimelineEvent(event, owner)

	// Record to store with broadcast to SSE subscribers
	ctx := context.Background()
	if err := timeline.RecordEventWithBroadcast(ctx, timelineEvent); err != nil {
		log.Printf("Warning: failed to record K8s event to timeline store: %v", err)
	} else if DebugEvents {
		timeline.IncrementRecorded("K8sEvent:" + event.InvolvedObject.Kind)
	}
}

// isNoisyResource returns true if this resource generates constant updates that aren't interesting
// This prevents the history buffer from being flooded with lease renewals, heartbeats, etc.
func isNoisyResource(kind, name, op string) bool {
	// Only filter updates - adds and deletes are always interesting
	if op != "update" {
		return false
	}

	// Noisy resource kinds (constant background updates)
	switch kind {
	case "Lease", "Endpoints", "EndpointSlice", "Event":
		return true
	}

	// Noisy ConfigMaps (leader election, heartbeats, status tracking)
	if kind == "ConfigMap" {
		noisyPatterns := []string{
			"-lock", "-lease", "-leader-election", "-heartbeat",
			"cluster-kubestore", "cluster-autoscaler-status",
			"datadog-token", "datadog-operator-lock", "datadog-leader-election",
			"kube-root-ca.certs",
		}
		for _, pattern := range noisyPatterns {
			if strings.Contains(name, pattern) {
				return true
			}
		}
	}

	// Noisy Secrets (token rotation)
	if kind == "Secret" {
		if strings.HasSuffix(name, "-token") || strings.Contains(name, "leader-election") {
			return true
		}
	}

	return false
}

// enqueueChange sends a change notification and records to both legacy history and timeline store
func enqueueChange(ch chan<- ResourceChange, kind string, obj any, oldObj any, op string) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			meta, ok = tombstone.Obj.(metav1.Object)
			if !ok {
				return
			}
			obj = tombstone.Obj
		} else {
			return
		}
	}

	// Track event received
	timeline.IncrementReceived(kind)

	// Debug: log adds for core workload resources
	if DebugEvents && op == "add" && (kind == "Pod" || kind == "Deployment" || kind == "Service") {
		log.Printf("[DEBUG] enqueueChange: %s add %s/%s", kind, meta.GetNamespace(), meta.GetName())
	}

	// Skip recording noisy resources to preserve history buffer for interesting events
	skipHistory := isNoisyResource(kind, meta.GetName(), op)
	if skipHistory {
		timeline.RecordDrop(kind, meta.GetNamespace(), meta.GetName(),
			timeline.DropReasonNoisyFilter, op)
		if DebugEvents {
			log.Printf("[DEBUG] Filtered noisy resource: %s/%s/%s op=%s", kind, meta.GetNamespace(), meta.GetName(), op)
		}
	}

	// Record to timeline store
	if !skipHistory {
		recordToTimelineStore(kind, meta.GetNamespace(), meta.GetName(), string(meta.GetUID()), op, oldObj, obj)
	}

	// Compute diff for updates
	var diff *DiffInfo
	if op == "update" && oldObj != nil && obj != nil {
		diff = ComputeDiff(kind, oldObj, obj)
	}

	change := ResourceChange{
		Kind:      kind,
		Namespace: meta.GetNamespace(),
		Name:      meta.GetName(),
		UID:       string(meta.GetUID()),
		Operation: op,
		Diff:      diff,
	}

	// Non-blocking send
	select {
	case ch <- change:
	default:
		// Channel full, drop event
		timeline.RecordDrop(kind, meta.GetNamespace(), meta.GetName(),
			timeline.DropReasonChannelFull, op)
		if DebugEvents {
			log.Printf("[DEBUG] Change channel full, dropped: %s/%s/%s op=%s", kind, meta.GetNamespace(), meta.GetName(), op)
		}
	}
}

// recordToTimelineStore records an event to the timeline store
func recordToTimelineStore(kind, namespace, name, uid, op string, oldObj, newObj any) {
	store := timeline.GetStore()
	if store == nil {
		return
	}

	// Check if we've already seen this resource (for dedup on restart)
	// For "add", we check if seen and skip if so. We mark as seen AFTER successful append
	// to avoid the race where a failed append leaves the resource marked as seen.
	if op == "add" {
		if store.IsResourceSeen(kind, namespace, name) {
			timeline.RecordDrop(kind, namespace, name, timeline.DropReasonAlreadySeen, op)
			if DebugEvents {
				log.Printf("[DEBUG] Already seen, skipping: %s/%s/%s", kind, namespace, name)
			}
			return
		}
		// Don't mark as seen yet - do it after successful append
	} else if op == "delete" {
		store.ClearResourceSeen(kind, namespace, name)
	}
	// For "update", we don't need to track seen state - updates are always recorded

	// Determine the object to analyze
	obj := newObj
	if obj == nil {
		obj = oldObj
	}

	// Extract owner reference
	owner := timeline.ExtractOwner(obj)

	// Extract labels for grouping
	labels := timeline.ExtractLabels(obj)

	// Determine health state
	healthState := timeline.DetermineHealthState(kind, obj)

	// Extract creationTimestamp from resource metadata
	var createdAt *time.Time
	if obj != nil {
		if meta, ok := obj.(metav1.Object); ok {
			ct := meta.GetCreationTimestamp().Time
			if !ct.IsZero() {
				createdAt = &ct
			}
		}
	}

	// Compute diff for updates
	var diff *timeline.DiffInfo
	if op == "update" && oldObj != nil && newObj != nil {
		if localDiff := ComputeDiff(kind, oldObj, newObj); localDiff != nil {
			diff = &timeline.DiffInfo{
				Fields:  make([]timeline.FieldChange, len(localDiff.Fields)),
				Summary: localDiff.Summary,
			}
			for i, f := range localDiff.Fields {
				diff.Fields[i] = timeline.FieldChange{
					Path:     f.Path,
					OldValue: f.OldValue,
					NewValue: f.NewValue,
				}
			}
		}
	}

	// Create the timeline event
	event := timeline.NewInformerEvent(
		kind, namespace, name, uid,
		timeline.OperationToEventType(op),
		healthState,
		diff,
		owner,
		labels,
		createdAt,
	)

	// For "add" operations, also extract historical events from resource status
	// and record them to the timeline store
	var events []timeline.TimelineEvent
	if op == "add" && newObj != nil {
		historicalEvents := extractTimelineHistoricalEvents(kind, namespace, name, newObj, owner)
		events = append(events, historicalEvents...)
	}

	// Skip recording the "add" event if it's just a sync (resource already existed).
	// We detect this by comparing the resource's creationTimestamp with current time.
	// If the resource is older than 30 seconds, it's a sync event, not a real create.
	// We still record historical events extracted from status (PodScheduled, ContainersReady, etc.)
	if op == "add" {
		isSyncEvent := false

		// Method 1: Check initialSyncComplete flag (fast path during startup)
		if !initialSyncComplete {
			isSyncEvent = true
		}

		// Method 2: Check creationTimestamp (handles race conditions and context switches)
		if !isSyncEvent && obj != nil {
			if meta, ok := obj.(metav1.Object); ok {
				creationTime := meta.GetCreationTimestamp().Time
				age := time.Since(creationTime)
				// If resource is older than 30 seconds, it's a sync, not a real create
				if age > 30*time.Second {
					isSyncEvent = true
					if DebugEvents {
						log.Printf("[DEBUG] Skipping stale add event (age=%v): %s/%s/%s", age, kind, namespace, name)
					}
				}
			}
		}

		if isSyncEvent {
			if DebugEvents {
				log.Printf("[DEBUG] Skipping sync add event: %s/%s/%s (extracted %d historical events)", kind, namespace, name, len(events))
			}
			// Only record historical events, not the add event
			if len(events) > 0 {
				ctx := context.Background()
				if err := timeline.RecordEventsWithBroadcast(ctx, events); err != nil {
					log.Printf("Warning: failed to record historical events: %v", err)
				}
			}
			return
		}
	}

	events = append(events, event)

	// Record all events to the store with broadcast to SSE subscribers
	ctx := context.Background()
	if err := timeline.RecordEventsWithBroadcast(ctx, events); err != nil {
		log.Printf("Warning: failed to record to timeline store: %v", err)
		timeline.RecordDrop(kind, namespace, name, timeline.DropReasonStoreFailed, op)
		return
	}

	// Track successful recording
	timeline.IncrementRecorded(kind)

	// Mark resource as seen AFTER successful append to avoid race condition
	// where a failed append leaves the resource marked as seen
	if op == "add" {
		store.MarkResourceSeen(kind, namespace, name)
	}
}

// extractTimelineHistoricalEvents extracts historical events from resource metadata/status for the timeline store
func extractTimelineHistoricalEvents(kind, namespace, name string, obj any, owner *timeline.OwnerInfo) []timeline.TimelineEvent {
	var events []timeline.TimelineEvent

	switch kind {
	case "Pod":
		if pod, ok := obj.(*corev1.Pod); ok {
			// Pod creation
			if !pod.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					pod.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner))
			}
			// Pod started
			if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					pod.Status.StartTime.Time, "started", "", timeline.HealthDegraded, owner))
			}
			// Check conditions
			for _, cond := range pod.Status.Conditions {
				if cond.LastTransitionTime.IsZero() {
					continue
				}
				health := timeline.HealthUnknown
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					health = timeline.HealthHealthy
				} else if cond.Status == corev1.ConditionFalse {
					health = timeline.HealthDegraded
				}
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					cond.LastTransitionTime.Time, string(cond.Type), cond.Message, health, owner))
			}
		}

	case "Deployment":
		if deploy, ok := obj.(*appsv1.Deployment); ok {
			if !deploy.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					deploy.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner))
			}
			// Check conditions
			for _, cond := range deploy.Status.Conditions {
				if cond.LastTransitionTime.IsZero() {
					continue
				}
				health := timeline.HealthUnknown
				if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
					health = timeline.HealthHealthy
				} else if cond.Status == corev1.ConditionFalse {
					health = timeline.HealthDegraded
				}
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					cond.LastTransitionTime.Time, string(cond.Type), cond.Message, health, owner))
			}
		}

	case "Service":
		if svc, ok := obj.(*corev1.Service); ok {
			if !svc.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					svc.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner))
			}
		}

	case "Job":
		if job, ok := obj.(*batchv1.Job); ok {
			if !job.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					job.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner))
			}
			if job.Status.StartTime != nil && !job.Status.StartTime.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					job.Status.StartTime.Time, "started", "", timeline.HealthDegraded, owner))
			}
			if job.Status.CompletionTime != nil && !job.Status.CompletionTime.IsZero() {
				health := timeline.HealthHealthy
				if job.Status.Failed > 0 {
					health = timeline.HealthUnhealthy
				}
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					job.Status.CompletionTime.Time, "completed", "", health, owner))
			}
		}
	}

	return events
}

// Listers

func (c *ResourceCache) isEnabled(key string) bool {
	if c == nil || c.enabledResources == nil {
		return false
	}
	return c.enabledResources[key]
}

func (c *ResourceCache) Services() listerscorev1.ServiceLister {
	if c == nil || !c.isEnabled("services") {
		return nil
	}
	return c.factory.Core().V1().Services().Lister()
}

func (c *ResourceCache) Pods() listerscorev1.PodLister {
	if c == nil || !c.isEnabled("pods") {
		return nil
	}
	return c.factory.Core().V1().Pods().Lister()
}

func (c *ResourceCache) Nodes() listerscorev1.NodeLister {
	if c == nil || !c.isEnabled("nodes") {
		return nil
	}
	return c.factory.Core().V1().Nodes().Lister()
}

func (c *ResourceCache) Namespaces() listerscorev1.NamespaceLister {
	if c == nil || !c.isEnabled("namespaces") {
		return nil
	}
	return c.factory.Core().V1().Namespaces().Lister()
}

func (c *ResourceCache) ConfigMaps() listerscorev1.ConfigMapLister {
	if c == nil || !c.isEnabled("configmaps") {
		return nil
	}
	return c.factory.Core().V1().ConfigMaps().Lister()
}

func (c *ResourceCache) Secrets() listerscorev1.SecretLister {
	if c == nil || !c.isEnabled("secrets") {
		return nil
	}
	return c.factory.Core().V1().Secrets().Lister()
}

func (c *ResourceCache) Events() listerscorev1.EventLister {
	if c == nil || !c.isEnabled("events") {
		return nil
	}
	return c.factory.Core().V1().Events().Lister()
}

func (c *ResourceCache) PersistentVolumeClaims() listerscorev1.PersistentVolumeClaimLister {
	if c == nil || !c.isEnabled("persistentvolumeclaims") {
		return nil
	}
	return c.factory.Core().V1().PersistentVolumeClaims().Lister()
}

func (c *ResourceCache) Deployments() listersappsv1.DeploymentLister {
	if c == nil || !c.isEnabled("deployments") {
		return nil
	}
	return c.factory.Apps().V1().Deployments().Lister()
}

func (c *ResourceCache) DaemonSets() listersappsv1.DaemonSetLister {
	if c == nil || !c.isEnabled("daemonsets") {
		return nil
	}
	return c.factory.Apps().V1().DaemonSets().Lister()
}

func (c *ResourceCache) StatefulSets() listersappsv1.StatefulSetLister {
	if c == nil || !c.isEnabled("statefulsets") {
		return nil
	}
	return c.factory.Apps().V1().StatefulSets().Lister()
}

func (c *ResourceCache) ReplicaSets() listersappsv1.ReplicaSetLister {
	if c == nil || !c.isEnabled("replicasets") {
		return nil
	}
	return c.factory.Apps().V1().ReplicaSets().Lister()
}

func (c *ResourceCache) Ingresses() listersnetworkingv1.IngressLister {
	if c == nil || !c.isEnabled("ingresses") {
		return nil
	}
	return c.factory.Networking().V1().Ingresses().Lister()
}

func (c *ResourceCache) Jobs() listersbatchv1.JobLister {
	if c == nil || !c.isEnabled("jobs") {
		return nil
	}
	return c.factory.Batch().V1().Jobs().Lister()
}

func (c *ResourceCache) CronJobs() listersbatchv1.CronJobLister {
	if c == nil || !c.isEnabled("cronjobs") {
		return nil
	}
	return c.factory.Batch().V1().CronJobs().Lister()
}

func (c *ResourceCache) HorizontalPodAutoscalers() listersautoscalingv2.HorizontalPodAutoscalerLister {
	if c == nil || !c.isEnabled("horizontalpodautoscalers") {
		return nil
	}
	return c.factory.Autoscaling().V2().HorizontalPodAutoscalers().Lister()
}

// GetEnabledResources returns the map of which resource types have informers running
func (c *ResourceCache) GetEnabledResources() map[string]bool {
	if c == nil {
		return nil
	}
	// Return a copy
	result := make(map[string]bool, len(c.enabledResources))
	for k, v := range c.enabledResources {
		result[k] = v
	}
	return result
}

// Changes returns the channel for resource change notifications
func (c *ResourceCache) Changes() <-chan ResourceChange {
	if c == nil {
		return nil
	}
	return c.changes
}

// ChangesRaw returns the bidirectional channel for internal use (e.g., sharing with dynamic cache)
func (c *ResourceCache) ChangesRaw() chan ResourceChange {
	if c == nil {
		return nil
	}
	return c.changes
}

// Stop gracefully shuts down the cache
func (c *ResourceCache) Stop() {
	if c == nil {
		return
	}

	c.stopOnce.Do(func() {
		log.Println("Stopping resource cache")
		close(c.stopCh)
		c.factory.Shutdown()
		close(c.changes)
	})
}

// GetResourceCount returns total cached resources
func (c *ResourceCache) GetResourceCount() int {
	if c == nil {
		return 0
	}

	count := 0
	if lister := c.Services(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.Pods(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.Nodes(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.Namespaces(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.Deployments(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.DaemonSets(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.StatefulSets(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.ReplicaSets(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	if lister := c.Ingresses(); lister != nil {
		if items, err := lister.List(labels.Everything()); err == nil {
			count += len(items)
		}
	}
	return count
}

// knownKinds maps lowercase kind names to whether they're handled by the typed cache
var knownKinds = map[string]bool{
	"pod": true, "pods": true,
	"service": true, "services": true,
	"deployment": true, "deployments": true,
	"daemonset": true, "daemonsets": true,
	"statefulset": true, "statefulsets": true,
	"replicaset": true, "replicasets": true,
	"ingress": true, "ingresses": true,
	"configmap": true, "configmaps": true,
	"secret": true, "secrets": true,
	"event": true, "events": true,
	"persistentvolumeclaim": true, "persistentvolumeclaims": true, "pvc": true, "pvcs": true,
	"node": true, "nodes": true,
	"namespace": true, "namespaces": true,
	"job": true, "jobs": true,
	"cronjob": true, "cronjobs": true,
	"horizontalpodautoscaler": true, "horizontalpodautoscalers": true, "hpa": true, "hpas": true,
}

// IsKnownKind returns true if the kind is handled by the typed cache
func IsKnownKind(kind string) bool {
	return knownKinds[strings.ToLower(kind)]
}

// ListDynamic returns resources of any type using the dynamic cache
// Falls back to typed cache for known resources
func (c *ResourceCache) ListDynamic(ctx context.Context, kind string, namespace string) ([]*unstructured.Unstructured, error) {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	gvr, ok := discovery.GetGVR(kind)
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s", kind)
	}

	dynamicCache := GetDynamicResourceCache()
	if dynamicCache == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	return dynamicCache.List(gvr, namespace)
}

// GetDynamic returns a single resource of any type using the dynamic cache
func (c *ResourceCache) GetDynamic(ctx context.Context, kind string, namespace string, name string) (*unstructured.Unstructured, error) {
	return c.GetDynamicWithGroup(ctx, kind, namespace, name, "")
}

// GetDynamicWithGroup returns a single resource, using the group to disambiguate
// when multiple API groups have resources with similar names
func (c *ResourceCache) GetDynamicWithGroup(ctx context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error) {
	discovery := GetResourceDiscovery()
	if discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	var gvr schema.GroupVersionResource
	var ok bool

	if group != "" {
		gvr, ok = discovery.GetGVRWithGroup(kind, group)
	} else {
		gvr, ok = discovery.GetGVR(kind)
	}

	if !ok {
		if group != "" {
			return nil, fmt.Errorf("unknown resource kind: %s (group: %s)", kind, group)
		}
		return nil, fmt.Errorf("unknown resource kind: %s", kind)
	}

	dynamicCache := GetDynamicResourceCache()
	if dynamicCache == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	u, err := dynamicCache.Get(gvr, namespace, name)
	if err != nil {
		return nil, err
	}

	// Ensure apiVersion and kind are set (informers may not populate these)
	if u.GetAPIVersion() == "" || u.GetKind() == "" {
		apiVersion := gvr.Version
		if gvr.Group != "" {
			apiVersion = gvr.Group + "/" + gvr.Version
		}
		u.SetAPIVersion(apiVersion)
		if kindName := discovery.GetKindForGVR(gvr); kindName != "" {
			u.SetKind(kindName)
		}
	}

	return u, nil
}

// ResourceStatus holds status information for a resource
type ResourceStatus struct {
	Status  string // Running, Pending, Failed, Succeeded, Active, etc.
	Ready   string // e.g., "3/3" for deployments
	Message string // Status message or reason
	Summary string // Brief human-readable status like "3/5 ready" or "0/3 OOMKilled"
	Issue   string // Primary issue if unhealthy (e.g., "OOMKilled", "CrashLoopBackOff")
}

// GetResourceStatus looks up a resource and returns its status
func (c *ResourceCache) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	if c == nil {
		return nil
	}

	kindLower := strings.ToLower(kind)

	switch kindLower {
	case "pod", "pods":
		if c.Pods() == nil {
			return nil
		}
		pod, err := c.Pods().Pods(namespace).Get(name)
		if err != nil {
			return nil
		}
		issue := getPodIssue(pod)
		status := string(pod.Status.Phase)
		summary := status
		if issue != "" {
			summary = issue
			status = issue
		}
		return &ResourceStatus{
			Status:  status,
			Ready:   getPodReadyCount(pod),
			Message: getPodStatusMessage(pod),
			Summary: summary,
			Issue:   issue,
		}

	case "deployment", "deployments":
		if c.Deployments() == nil {
			return nil
		}
		dep, err := c.Deployments().Deployments(namespace).Get(name)
		if err != nil {
			return nil
		}
		ready := fmt.Sprintf("%d/%d", dep.Status.ReadyReplicas, dep.Status.Replicas)
		status := "Progressing"
		if dep.Status.ReadyReplicas == dep.Status.Replicas && dep.Status.Replicas > 0 {
			status = "Running"
		} else if dep.Status.Replicas == 0 {
			status = "Scaled to 0"
		}

		result := &ResourceStatus{
			Status:  status,
			Ready:   ready,
			Summary: ready + " ready",
		}

		// Check pod-level issues for unhealthy deployments
		if dep.Status.ReadyReplicas < dep.Status.Replicas && dep.Status.Replicas > 0 {
			pods := c.GetPodsForWorkload(namespace, dep.Spec.Selector)
			if len(pods) > 0 {
				issueSummary := getPodsIssueSummary(pods)
				if issueSummary.TopIssue != "" {
					result.Status = issueSummary.TopIssue
					result.Issue = issueSummary.TopIssue
					result.Summary = issueSummary.FormatStatusSummary()
				}
			}
		}

		return result

	case "statefulset", "statefulsets":
		if c.StatefulSets() == nil {
			return nil
		}
		sts, err := c.StatefulSets().StatefulSets(namespace).Get(name)
		if err != nil {
			return nil
		}
		replicas := int32(1)
		if sts.Spec.Replicas != nil {
			replicas = *sts.Spec.Replicas
		}
		ready := fmt.Sprintf("%d/%d", sts.Status.ReadyReplicas, replicas)
		status := "Progressing"
		if sts.Status.ReadyReplicas == replicas && replicas > 0 {
			status = "Running"
		} else if replicas == 0 {
			status = "Scaled to 0"
		}

		result := &ResourceStatus{
			Status:  status,
			Ready:   ready,
			Summary: ready + " ready",
		}

		// Check pod-level issues for unhealthy statefulsets
		if sts.Status.ReadyReplicas < replicas && replicas > 0 {
			pods := c.GetPodsForWorkload(namespace, sts.Spec.Selector)
			if len(pods) > 0 {
				issueSummary := getPodsIssueSummary(pods)
				if issueSummary.TopIssue != "" {
					result.Status = issueSummary.TopIssue
					result.Issue = issueSummary.TopIssue
					result.Summary = issueSummary.FormatStatusSummary()
				}
			}
		}

		return result

	case "daemonset", "daemonsets":
		if c.DaemonSets() == nil {
			return nil
		}
		ds, err := c.DaemonSets().DaemonSets(namespace).Get(name)
		if err != nil {
			return nil
		}
		ready := fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		status := "Progressing"
		if ds.Status.NumberReady == ds.Status.DesiredNumberScheduled && ds.Status.DesiredNumberScheduled > 0 {
			status = "Running"
		}

		// Check pod-level issues for better status reporting
		result := &ResourceStatus{
			Status:  status,
			Ready:   ready,
			Summary: ready + " ready",
		}

		// Get pods owned by this DaemonSet
		if ds.Status.NumberReady < ds.Status.DesiredNumberScheduled {
			pods := c.GetPodsForWorkload(namespace, ds.Spec.Selector)
			if len(pods) > 0 {
				issueSummary := getPodsIssueSummary(pods)
				if issueSummary.TopIssue != "" {
					result.Status = issueSummary.TopIssue
					result.Issue = issueSummary.TopIssue
					result.Summary = issueSummary.FormatStatusSummary()
				}
			}
		}

		return result

	case "replicaset", "replicasets":
		if c.ReplicaSets() == nil {
			return nil
		}
		rs, err := c.ReplicaSets().ReplicaSets(namespace).Get(name)
		if err != nil {
			return nil
		}
		replicas := int32(1)
		if rs.Spec.Replicas != nil {
			replicas = *rs.Spec.Replicas
		}
		ready := fmt.Sprintf("%d/%d", rs.Status.ReadyReplicas, replicas)
		status := "Progressing"
		if rs.Status.ReadyReplicas == replicas && replicas > 0 {
			status = "Running"
		} else if replicas == 0 {
			status = "Scaled to 0"
		}
		return &ResourceStatus{
			Status: status,
			Ready:  ready,
		}

	case "service", "services":
		if c.Services() == nil {
			return nil
		}
		_, err := c.Services().Services(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "configmap", "configmaps":
		if c.ConfigMaps() == nil {
			return nil
		}
		_, err := c.ConfigMaps().ConfigMaps(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "secret", "secrets":
		lister := c.Secrets()
		if lister == nil {
			return nil
		}
		_, err := lister.Secrets(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "ingress", "ingresses":
		if c.Ingresses() == nil {
			return nil
		}
		_, err := c.Ingresses().Ingresses(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "job", "jobs":
		if c.Jobs() == nil {
			return nil
		}
		job, err := c.Jobs().Jobs(namespace).Get(name)
		if err != nil {
			return nil
		}
		status := "Running"
		if job.Status.Succeeded > 0 {
			status = "Succeeded"
		} else if job.Status.Failed > 0 {
			status = "Failed"
		}
		// Completions defaults to 1 when nil
		completions := int32(1)
		if job.Spec.Completions != nil {
			completions = *job.Spec.Completions
		}
		return &ResourceStatus{
			Status: status,
			Ready:  fmt.Sprintf("%d/%d", job.Status.Succeeded, completions),
		}

	case "cronjob", "cronjobs":
		if c.CronJobs() == nil {
			return nil
		}
		cj, err := c.CronJobs().CronJobs(namespace).Get(name)
		if err != nil {
			return nil
		}
		status := "Active"
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			status = "Suspended"
		}
		return &ResourceStatus{
			Status: status,
		}

	case "horizontalpodautoscaler", "horizontalpodautoscalers", "hpa":
		if c.HorizontalPodAutoscalers() == nil {
			return nil
		}
		hpa, err := c.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
			Ready:  fmt.Sprintf("%d/%d", hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas),
		}

	case "persistentvolumeclaim", "persistentvolumeclaims", "pvc":
		if c.PersistentVolumeClaims() == nil {
			return nil
		}
		pvc, err := c.PersistentVolumeClaims().PersistentVolumeClaims(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: string(pvc.Status.Phase),
		}

	default:
		// For unknown types, return nil (no status available)
		return nil
	}
}

// getPodReadyCount returns the ready container count as "ready/total"
func getPodReadyCount(pod *corev1.Pod) string {
	ready := 0
	total := len(pod.Spec.Containers)
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
	}
	return fmt.Sprintf("%d/%d", ready, total)
}

// getPodStatusMessage returns a brief status message for a pod
func getPodStatusMessage(pod *corev1.Pod) string {
	// Check for waiting containers
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	// Check conditions
	for _, cond := range pod.Status.Conditions {
		if cond.Status == corev1.ConditionFalse && cond.Message != "" {
			return cond.Message
		}
	}
	return ""
}

// getPodIssue returns the primary issue affecting a pod (if any)
// Returns empty string if pod is healthy
func getPodIssue(pod *corev1.Pod) string {
	// Check init containers first
	for _, cs := range pod.Status.InitContainerStatuses {
		if issue := getContainerIssue(&cs); issue != "" {
			return issue
		}
	}
	// Check main containers
	for _, cs := range pod.Status.ContainerStatuses {
		if issue := getContainerIssue(&cs); issue != "" {
			return issue
		}
	}
	// Check pod conditions
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			if cond.Reason != "" {
				return cond.Reason // e.g., "Unschedulable"
			}
		}
	}
	return ""
}

// getContainerIssue extracts the issue from a container status
func getContainerIssue(cs *corev1.ContainerStatus) string {
	// Check current state first
	if cs.State.Waiting != nil {
		reason := cs.State.Waiting.Reason
		if reason != "" && reason != "PodInitializing" && reason != "ContainerCreating" {
			return reason // CrashLoopBackOff, ImagePullBackOff, etc.
		}
	}
	if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
		if cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason // OOMKilled, Error, etc.
		}
	}
	// Check last state for recent failures
	if cs.LastTerminationState.Terminated != nil {
		if cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
			return "OOMKilled"
		}
	}
	return ""
}

// PodIssueSummary holds aggregated pod issue information
type PodIssueSummary struct {
	Total    int
	Ready    int
	Issues   map[string]int // issue -> count (e.g., "OOMKilled" -> 3)
	TopIssue string         // Most common issue
	TopCount int            // Count of most common issue
}

// getPodsIssueSummary analyzes a list of pods and returns issue summary
func getPodsIssueSummary(pods []*corev1.Pod) *PodIssueSummary {
	summary := &PodIssueSummary{
		Total:  len(pods),
		Issues: make(map[string]int),
	}

	for _, pod := range pods {
		// Count ready pods
		if pod.Status.Phase == corev1.PodRunning {
			allReady := true
			for _, cs := range pod.Status.ContainerStatuses {
				if !cs.Ready {
					allReady = false
					break
				}
			}
			if allReady {
				summary.Ready++
			}
		}

		// Track issues
		if issue := getPodIssue(pod); issue != "" {
			summary.Issues[issue]++
			if summary.Issues[issue] > summary.TopCount {
				summary.TopIssue = issue
				summary.TopCount = summary.Issues[issue]
			}
		}
	}

	return summary
}

// FormatStatusSummary creates a brief human-readable status string
func (s *PodIssueSummary) FormatStatusSummary() string {
	if s.Total == 0 {
		return "No pods"
	}
	if s.TopIssue != "" {
		return fmt.Sprintf("%d/%d %s", s.Ready, s.Total, s.TopIssue)
	}
	if s.Ready == s.Total {
		return fmt.Sprintf("%d/%d ready", s.Ready, s.Total)
	}
	return fmt.Sprintf("%d/%d ready", s.Ready, s.Total)
}

// GetPodsForWorkload returns pods matching the given label selector in a namespace
func (c *ResourceCache) GetPodsForWorkload(namespace string, selector *metav1.LabelSelector) []*corev1.Pod {
	if c == nil || selector == nil || c.Pods() == nil {
		return nil
	}

	allPods, err := c.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil
	}

	// Convert LabelSelector to Selector
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil
	}

	var matchingPods []*corev1.Pod
	for _, pod := range allPods {
		if labelSelector.Matches(labels.Set(pod.Labels)) {
			matchingPods = append(matchingPods, pod)
		}
	}

	return matchingPods
}
