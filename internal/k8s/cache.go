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
	"k8s.io/client-go/informers"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listersautoscalingv2 "k8s.io/client-go/listers/autoscaling/v2"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersnetworkingv1 "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
)

// ResourceCache provides fast, eventually-consistent access to K8s resources
// using SharedInformers. Optimized for small-mid sized clusters.
type ResourceCache struct {
	factory  informers.SharedInformerFactory
	changes  chan ResourceChange
	stopCh   chan struct{}
	stopOnce sync.Once
}

// ResourceChange represents a resource change event
type ResourceChange struct {
	Kind      string    // "Service", "Deployment", "Pod", etc.
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

		factory := informers.NewSharedInformerFactoryWithOptions(
			k8sClient,
			0, // no resync - updates come via watch
			informers.WithTransform(dropManagedFields),
		)

		stopCh := make(chan struct{})
		changes := make(chan ResourceChange, 1000)

		// Core resources
		svcInf := factory.Core().V1().Services().Informer()
		podInf := factory.Core().V1().Pods().Informer()
		nodeInf := factory.Core().V1().Nodes().Informer()
		nsInf := factory.Core().V1().Namespaces().Informer()
		cmInf := factory.Core().V1().ConfigMaps().Informer()
		secretInf := factory.Core().V1().Secrets().Informer()
		eventInf := factory.Core().V1().Events().Informer()
		pvcInf := factory.Core().V1().PersistentVolumeClaims().Informer()

		// Apps resources
		depInf := factory.Apps().V1().Deployments().Informer()
		dsInf := factory.Apps().V1().DaemonSets().Informer()
		stsInf := factory.Apps().V1().StatefulSets().Informer()
		rsInf := factory.Apps().V1().ReplicaSets().Informer()

		// Networking resources
		ingInf := factory.Networking().V1().Ingresses().Informer()

		// Batch resources
		jobInf := factory.Batch().V1().Jobs().Informer()
		cronJobInf := factory.Batch().V1().CronJobs().Informer()

		// Autoscaling resources
		hpaInf := factory.Autoscaling().V2().HorizontalPodAutoscalers().Informer()

		// Add event handlers
		addChangeHandlers(svcInf, "Service", changes)
		addChangeHandlers(podInf, "Pod", changes)
		addChangeHandlers(nodeInf, "Node", changes)
		addChangeHandlers(nsInf, "Namespace", changes)
		addChangeHandlers(cmInf, "ConfigMap", changes)
		addChangeHandlers(secretInf, "Secret", changes)
		addChangeHandlers(eventInf, "Event", changes)
		addChangeHandlers(pvcInf, "PersistentVolumeClaim", changes)
		addChangeHandlers(depInf, "Deployment", changes)
		addChangeHandlers(dsInf, "DaemonSet", changes)
		addChangeHandlers(stsInf, "StatefulSet", changes)
		addChangeHandlers(rsInf, "ReplicaSet", changes)
		addChangeHandlers(ingInf, "Ingress", changes)
		addChangeHandlers(jobInf, "Job", changes)
		addChangeHandlers(cronJobInf, "CronJob", changes)
		addChangeHandlers(hpaInf, "HorizontalPodAutoscaler", changes)

		// Start all informers
		factory.Start(stopCh)

		log.Println("Starting resource cache with SharedInformers for 16 resource types")
		syncStart := time.Now()

		// Wait for caches to sync
		if !cache.WaitForCacheSync(stopCh,
			svcInf.HasSynced,
			podInf.HasSynced,
			nodeInf.HasSynced,
			nsInf.HasSynced,
			cmInf.HasSynced,
			secretInf.HasSynced,
			eventInf.HasSynced,
			pvcInf.HasSynced,
			depInf.HasSynced,
			dsInf.HasSynced,
			stsInf.HasSynced,
			rsInf.HasSynced,
			ingInf.HasSynced,
			jobInf.HasSynced,
			cronJobInf.HasSynced,
			hpaInf.HasSynced,
		) {
			close(stopCh)
			initErr = fmt.Errorf("failed to sync resource caches")
			return
		}

		log.Printf("Resource caches synced successfully in %v", time.Since(syncStart))

		resourceCache = &ResourceCache{
			factory: factory,
			changes: changes,
			stopCh:  stopCh,
		}
	})
	return initErr
}

// GetResourceCache returns the singleton cache instance
func GetResourceCache() *ResourceCache {
	return resourceCache
}

// ResetResourceCache stops and clears the resource cache
// This must be called before ReinitResourceCache when switching contexts
func ResetResourceCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if resourceCache != nil {
		resourceCache.Stop()
		resourceCache = nil
	}
	cacheOnce = sync.Once{}
}

// ReinitResourceCache reinitializes the resource cache after a context switch
// Must call ResetResourceCache first
func ReinitResourceCache() error {
	return InitResourceCache()
}

// addChangeHandlers registers event handlers for change notifications
func addChangeHandlers(inf cache.SharedIndexInformer, kind string, ch chan<- ResourceChange) {
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
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
}

// enqueueChange sends a change notification and records to history
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

	// Record to change history for timeline (with diff computation)
	var record *ChangeRecord
	if history := GetChangeHistory(); history != nil {
		record = history.RecordChange(kind, meta.GetNamespace(), meta.GetName(), op, oldObj, obj)
	}

	change := ResourceChange{
		Kind:      kind,
		Namespace: meta.GetNamespace(),
		Name:      meta.GetName(),
		UID:       string(meta.GetUID()),
		Operation: op,
	}

	// Attach diff info if available
	if record != nil && record.Diff != nil {
		change.Diff = record.Diff
	}

	// Non-blocking send
	select {
	case ch <- change:
	default:
		// Channel full, drop event
	}
}

// Listers

func (c *ResourceCache) Services() listerscorev1.ServiceLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().Services().Lister()
}

func (c *ResourceCache) Pods() listerscorev1.PodLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().Pods().Lister()
}

func (c *ResourceCache) Nodes() listerscorev1.NodeLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().Nodes().Lister()
}

func (c *ResourceCache) Namespaces() listerscorev1.NamespaceLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().Namespaces().Lister()
}

func (c *ResourceCache) ConfigMaps() listerscorev1.ConfigMapLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().ConfigMaps().Lister()
}

func (c *ResourceCache) Secrets() listerscorev1.SecretLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().Secrets().Lister()
}

func (c *ResourceCache) Events() listerscorev1.EventLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().Events().Lister()
}

func (c *ResourceCache) PersistentVolumeClaims() listerscorev1.PersistentVolumeClaimLister {
	if c == nil {
		return nil
	}
	return c.factory.Core().V1().PersistentVolumeClaims().Lister()
}

func (c *ResourceCache) Deployments() listersappsv1.DeploymentLister {
	if c == nil {
		return nil
	}
	return c.factory.Apps().V1().Deployments().Lister()
}

func (c *ResourceCache) DaemonSets() listersappsv1.DaemonSetLister {
	if c == nil {
		return nil
	}
	return c.factory.Apps().V1().DaemonSets().Lister()
}

func (c *ResourceCache) StatefulSets() listersappsv1.StatefulSetLister {
	if c == nil {
		return nil
	}
	return c.factory.Apps().V1().StatefulSets().Lister()
}

func (c *ResourceCache) ReplicaSets() listersappsv1.ReplicaSetLister {
	if c == nil {
		return nil
	}
	return c.factory.Apps().V1().ReplicaSets().Lister()
}

func (c *ResourceCache) Ingresses() listersnetworkingv1.IngressLister {
	if c == nil {
		return nil
	}
	return c.factory.Networking().V1().Ingresses().Lister()
}

func (c *ResourceCache) Jobs() listersbatchv1.JobLister {
	if c == nil {
		return nil
	}
	return c.factory.Batch().V1().Jobs().Lister()
}

func (c *ResourceCache) CronJobs() listersbatchv1.CronJobLister {
	if c == nil {
		return nil
	}
	return c.factory.Batch().V1().CronJobs().Lister()
}

func (c *ResourceCache) HorizontalPodAutoscalers() listersautoscalingv2.HorizontalPodAutoscalerLister {
	if c == nil {
		return nil
	}
	return c.factory.Autoscaling().V2().HorizontalPodAutoscalers().Lister()
}

// Changes returns the channel for resource change notifications
func (c *ResourceCache) Changes() <-chan ResourceChange {
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
	if services, err := c.Services().List(labels.Everything()); err == nil {
		count += len(services)
	}
	if pods, err := c.Pods().List(labels.Everything()); err == nil {
		count += len(pods)
	}
	if nodes, err := c.Nodes().List(labels.Everything()); err == nil {
		count += len(nodes)
	}
	if namespaces, err := c.Namespaces().List(labels.Everything()); err == nil {
		count += len(namespaces)
	}
	if deployments, err := c.Deployments().List(labels.Everything()); err == nil {
		count += len(deployments)
	}
	if daemonsets, err := c.DaemonSets().List(labels.Everything()); err == nil {
		count += len(daemonsets)
	}
	if statefulsets, err := c.StatefulSets().List(labels.Everything()); err == nil {
		count += len(statefulsets)
	}
	if replicasets, err := c.ReplicaSets().List(labels.Everything()); err == nil {
		count += len(replicasets)
	}
	if ingresses, err := c.Ingresses().List(labels.Everything()); err == nil {
		count += len(ingresses)
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

	return dynamicCache.Get(gvr, namespace, name)
}

// ResourceStatus holds status information for a resource
type ResourceStatus struct {
	Status  string // Running, Pending, Failed, Succeeded, Active, etc.
	Ready   string // e.g., "3/3" for deployments
	Message string // Status message or reason
}

// GetResourceStatus looks up a resource and returns its status
func (c *ResourceCache) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	if c == nil {
		return nil
	}

	kindLower := strings.ToLower(kind)

	switch kindLower {
	case "pod", "pods":
		pod, err := c.Pods().Pods(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status:  string(pod.Status.Phase),
			Ready:   getPodReadyCount(pod),
			Message: getPodStatusMessage(pod),
		}

	case "deployment", "deployments":
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
		return &ResourceStatus{
			Status: status,
			Ready:  ready,
		}

	case "statefulset", "statefulsets":
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
		return &ResourceStatus{
			Status: status,
			Ready:  ready,
		}

	case "daemonset", "daemonsets":
		ds, err := c.DaemonSets().DaemonSets(namespace).Get(name)
		if err != nil {
			return nil
		}
		ready := fmt.Sprintf("%d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		status := "Progressing"
		if ds.Status.NumberReady == ds.Status.DesiredNumberScheduled && ds.Status.DesiredNumberScheduled > 0 {
			status = "Running"
		}
		return &ResourceStatus{
			Status: status,
			Ready:  ready,
		}

	case "replicaset", "replicasets":
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
		_, err := c.Services().Services(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "configmap", "configmaps":
		_, err := c.ConfigMaps().ConfigMaps(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "secret", "secrets":
		_, err := c.Secrets().Secrets(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "ingress", "ingresses":
		_, err := c.Ingresses().Ingresses(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "job", "jobs":
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
		return &ResourceStatus{
			Status: status,
			Ready:  fmt.Sprintf("%d/%d", job.Status.Succeeded, *job.Spec.Completions),
		}

	case "cronjob", "cronjobs":
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
		hpa, err := c.HorizontalPodAutoscalers().HorizontalPodAutoscalers(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
			Ready:  fmt.Sprintf("%d/%d", hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas),
		}

	case "persistentvolumeclaim", "persistentvolumeclaims", "pvc":
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
