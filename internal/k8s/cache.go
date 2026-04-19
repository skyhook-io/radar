package k8s

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
)

// DebugEvents enables verbose event debugging when true (set via --debug-events flag)
var DebugEvents bool

// TimingLogs enables [startup-timing] log lines when true (set via --dev flag).
// These are useful for profiling startup but too noisy for production.
var TimingLogs bool

// LogTiming prints a [startup-timing] log line if TimingLogs is enabled.
func LogTiming(format string, args ...any) {
	if TimingLogs {
		log.Printf("[startup-timing] "+format, args...)
	}
}

var logTiming = LogTiming

// initialSyncComplete is set to true after the initial cache sync completes.
// During initial sync, "add" events are skipped since they represent existing
// resources, not new creations. Only adds after sync are recorded.
var initialSyncComplete bool

// deferredResources lists informer keys that are NOT required for the initial
// dashboard render. These sync in the background after the critical informers
// complete, so the UI can render immediately with core resources.
// Critical: pods, deployments, services, statefulsets, daemonsets, nodes,
//           namespaces, ingresses, jobs, cronjobs
var deferredResources = map[string]bool{
	"secrets":                  true,
	"events":                   true,
	"configmaps":               true,
	"persistentvolumeclaims":   true,
	"persistentvolumes":        true,
	"storageclasses":           true,
	"poddisruptionbudgets":     true,
	"networkpolicies":          true,
	"replicasets":              true, // topology-only (Deployment→RS→Pod); can be very large
	"horizontalpodautoscalers": true, // problems detection, not critical for first render
	"serviceaccounts":          true, // audit inheritance lookups, not first-render
	"limitranges":              true, // audit inheritance lookups, not first-render
}

// ResourceChange is a type alias for the canonical definition in pkg/k8score.
type ResourceChange = k8score.ResourceChange

// ResourceCache provides fast, eventually-consistent access to K8s resources
// using SharedInformers. It embeds *k8score.ResourceCache for the shared
// informer logic and adds Radar-specific extensions (dynamic cache, resource
// status, pod workload lookup, timeline integration).
type ResourceCache struct {
	*k8score.ResourceCache
	secretsEnabled bool // Whether secrets informer is running (requires RBAC)
}

var (
	resourceCache *ResourceCache
	cacheOnce     = new(sync.Once)
	cacheMu       sync.Mutex
)

// InitResourceCache initializes the resource cache with timeline-wired callbacks.
func InitResourceCache(ctx context.Context) error {
	var initErr error
	cacheOnce.Do(func() {
		if k8sClient == nil {
			initErr = fmt.Errorf("cannot create resource cache: k8s client not initialized")
			return
		}

		// Check RBAC permissions for all resource types before creating informers.
		rbacStart := time.Now()
		rbacCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		permResult := CheckResourcePermissions(rbacCtx)
		cancel()
		logTiming("    RBAC permission checks: %v", time.Since(rbacStart))

		if ctx.Err() != nil {
			initErr = ctx.Err()
			return
		}

		perms := permResult.Perms

		enabled := map[string]bool{
			"pods":                     perms.Pods,
			"services":                 perms.Services,
			"deployments":              perms.Deployments,
			"daemonsets":               perms.DaemonSets,
			"statefulsets":             perms.StatefulSets,
			"replicasets":              perms.ReplicaSets,
			"ingresses":                perms.Ingresses,
			"configmaps":               perms.ConfigMaps,
			"secrets":                  perms.Secrets,
			"events":                   perms.Events,
			"persistentvolumeclaims":   perms.PersistentVolumeClaims,
			"nodes":                    perms.Nodes,
			"namespaces":               perms.Namespaces,
			"jobs":                     perms.Jobs,
			"cronjobs":                 perms.CronJobs,
			"horizontalpodautoscalers": perms.HorizontalPodAutoscalers,
			"persistentvolumes":        perms.PersistentVolumes,
			"storageclasses":           perms.StorageClasses,
			"poddisruptionbudgets":     perms.PodDisruptionBudgets,
			"networkpolicies":          perms.NetworkPolicies,
			"serviceaccounts":          perms.ServiceAccounts,
			"limitranges":              perms.LimitRanges,
		}

		cfg := k8score.CacheConfig{
			Client:              k8sClient,
			ResourceTypes:       enabled,
			DeferredTypes:       deferredResources,
			NamespaceScoped:     permResult.NamespaceScoped,
			Namespace:           permResult.Namespace,
			DebugEvents:         DebugEvents,
			TimingLogger:        logTiming,
			SyncTimeout:         60 * time.Second,
			DeferredSyncTimeout: 3 * time.Minute,

			OnReceived: func(kind string) {
				timeline.IncrementReceived(kind)
			},

			OnChange: func(change k8score.ResourceChange, obj, oldObj any) {
				if DebugEvents && change.Operation == "add" &&
					(change.Kind == "Pod" || change.Kind == "Deployment" || change.Kind == "Service") {
					log.Printf("[DEBUG] enqueueChange: %s add %s/%s", change.Kind, change.Namespace, change.Name)
				}

				// Record to timeline store
				recordToTimelineStore(change.Kind, change.Namespace, change.Name, change.UID, change.Operation, oldObj, obj)
			},

			OnEventChange: func(obj any, op string) {
				// Event deletes are not recorded to timeline — events represent
				// things that happened and should remain in history.
				if op == "delete" {
					return
				}
				recordK8sEventToTimeline(obj)
			},

			OnDrop: func(kind, ns, name, reason, op string) {
				timeline.RecordDrop(kind, ns, name, reason, op)
				if DebugEvents {
					log.Printf("[DEBUG] Change dropped: %s/%s/%s reason=%s op=%s", kind, ns, name, reason, op)
				}
			},

			ComputeDiff: func(kind string, oldObj, newObj any) *k8score.DiffInfo {
				return ComputeDiff(kind, oldObj, newObj)
			},

			IsNoisyResource: isNoisyResource,
		}

		core, err := k8score.NewResourceCache(cfg)
		if err != nil {
			initErr = err
			return
		}

		initialSyncComplete = core.IsSyncComplete()

		resourceCache = &ResourceCache{
			ResourceCache:  core,
			secretsEnabled: enabled["secrets"],
		}
	})
	return initErr
}

// GetResourceCache returns the singleton cache instance.
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
	cacheOnce = new(sync.Once)
	initialSyncComplete = false
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

	if DebugEvents {
		timeline.IncrementReceived("K8sEvent:" + event.InvolvedObject.Kind)
	}

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

	timelineEvent := timeline.NewK8sEventTimelineEvent(event, owner)

	ctx := context.Background()
	if err := timeline.RecordEventWithBroadcast(ctx, timelineEvent); err != nil {
		log.Printf("Warning: failed to record K8s event to timeline store: %v", err)
	} else if DebugEvents {
		timeline.IncrementRecorded("K8sEvent:" + event.InvolvedObject.Kind)
	}
}

// isNoisyResource returns true if this resource generates constant updates that aren't interesting
func isNoisyResource(kind, name, op string) bool {
	if op != "update" {
		return false
	}

	switch kind {
	case "Lease", "Endpoints", "EndpointSlice", "Event":
		return true
	}

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

	if kind == "Secret" {
		if strings.HasSuffix(name, "-token") || strings.Contains(name, "leader-election") {
			return true
		}
	}

	return false
}

// recordToTimelineStore records an event to the timeline store
func recordToTimelineStore(kind, namespace, name, uid, op string, oldObj, newObj any) {
	store := timeline.GetStore()
	if store == nil {
		return
	}

	if op == "add" {
		if store.IsResourceSeen(kind, namespace, name) {
			timeline.RecordDrop(kind, namespace, name, timeline.DropReasonAlreadySeen, op)
			if DebugEvents {
				log.Printf("[DEBUG] Already seen, skipping: %s/%s/%s", kind, namespace, name)
			}
			return
		}
	} else if op == "delete" {
		store.ClearResourceSeen(kind, namespace, name)
	}

	obj := newObj
	if obj == nil {
		obj = oldObj
	}

	owner := timeline.ExtractOwner(obj)
	labels := timeline.ExtractLabels(obj)
	healthState := timeline.DetermineHealthState(kind, obj)

	var createdAt *time.Time
	if obj != nil {
		if meta, ok := obj.(metav1.Object); ok {
			ct := meta.GetCreationTimestamp().Time
			if !ct.IsZero() {
				createdAt = &ct
			}
		}
	}

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

	event := timeline.NewInformerEvent(
		kind, namespace, name, uid,
		timeline.OperationToEventType(op),
		healthState,
		diff,
		owner,
		labels,
		createdAt,
	)

	var events []timeline.TimelineEvent
	if op == "add" && newObj != nil {
		historicalEvents := extractTimelineHistoricalEvents(kind, namespace, name, newObj, owner, labels)
		events = append(events, historicalEvents...)
	}

	if op == "add" {
		isSyncEvent := false

		if !initialSyncComplete {
			isSyncEvent = true
		}

		if !isSyncEvent && obj != nil {
			if meta, ok := obj.(metav1.Object); ok {
				creationTime := meta.GetCreationTimestamp().Time
				age := time.Since(creationTime)
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

	ctx := context.Background()
	if err := timeline.RecordEventsWithBroadcast(ctx, events); err != nil {
		log.Printf("Warning: failed to record to timeline store: %v", err)
		timeline.RecordDrop(kind, namespace, name, timeline.DropReasonStoreFailed, op)
		return
	}

	timeline.IncrementRecorded(kind)

	if op == "add" {
		store.MarkResourceSeen(kind, namespace, name)
	}
}

// extractTimelineHistoricalEvents extracts historical events from resource metadata/status
func extractTimelineHistoricalEvents(kind, namespace, name string, obj any, owner *timeline.OwnerInfo, labels map[string]string) []timeline.TimelineEvent {
	var events []timeline.TimelineEvent

	switch kind {
	case "Pod":
		if pod, ok := obj.(*corev1.Pod); ok {
			if !pod.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					pod.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
			if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					pod.Status.StartTime.Time, "started", "", timeline.HealthDegraded, owner, labels))
			}
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
					cond.LastTransitionTime.Time, string(cond.Type), cond.Message, health, owner, labels))
			}
		}

	case "Deployment":
		if deploy, ok := obj.(*appsv1.Deployment); ok {
			if !deploy.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					deploy.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
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
					cond.LastTransitionTime.Time, string(cond.Type), cond.Message, health, owner, labels))
			}
		}

	case "ReplicaSet":
		if rs, ok := obj.(*appsv1.ReplicaSet); ok {
			if !rs.CreationTimestamp.IsZero() {
				health := timeline.HealthUnknown
				if rs.Status.ReadyReplicas > 0 && rs.Status.ReadyReplicas == rs.Status.Replicas {
					health = timeline.HealthHealthy
				}
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					rs.CreationTimestamp.Time, "created", "", health, owner, labels))
			}
		}

	case "StatefulSet":
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			if !sts.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					sts.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
		}

	case "DaemonSet":
		if ds, ok := obj.(*appsv1.DaemonSet); ok {
			if !ds.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					ds.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
		}

	case "Service":
		if svc, ok := obj.(*corev1.Service); ok {
			if !svc.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					svc.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "Ingress":
		if ing, ok := obj.(*networkingv1.Ingress); ok {
			if !ing.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					ing.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "CronJob":
		if cj, ok := obj.(*batchv1.CronJob); ok {
			if !cj.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					cj.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "HorizontalPodAutoscaler":
		if hpa, ok := obj.(*autoscalingv2.HorizontalPodAutoscaler); ok {
			if !hpa.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					hpa.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "Job":
		if job, ok := obj.(*batchv1.Job); ok {
			if !job.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					job.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
			if job.Status.StartTime != nil && !job.Status.StartTime.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					job.Status.StartTime.Time, "started", "", timeline.HealthDegraded, owner, labels))
			}
			if job.Status.CompletionTime != nil && !job.Status.CompletionTime.IsZero() {
				health := timeline.HealthHealthy
				if job.Status.Failed > 0 {
					health = timeline.HealthUnhealthy
				}
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					job.Status.CompletionTime.Time, "completed", "", health, owner, labels))
			}
		}

	default:
		if u, ok := obj.(*unstructured.Unstructured); ok {
			ct := u.GetCreationTimestamp()
			if !ct.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, namespace, name,
					ct.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
		}
	}

	return events
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
	"persistentvolume": true, "persistentvolumes": true, "pv": true, "pvs": true,
	"storageclass": true, "storageclasses": true, "sc": true,
	"poddisruptionbudget": true, "poddisruptionbudgets": true, "pdb": true, "pdbs": true,
	"networkpolicy": true, "networkpolicies": true, "netpol": true,
}

// IsKnownKind returns true if the kind is handled by the typed cache
func IsKnownKind(kind string) bool {
	return knownKinds[strings.ToLower(kind)]
}

// ListDynamic returns resources of any type using the dynamic cache
func (c *ResourceCache) ListDynamic(ctx context.Context, kind string, namespace string) ([]*unstructured.Unstructured, error) {
	return c.ListDynamicWithGroup(ctx, kind, namespace, "")
}

// ListDynamicWithGroup returns resources, using the group to disambiguate
func (c *ResourceCache) ListDynamicWithGroup(ctx context.Context, kind string, namespace string, group string) ([]*unstructured.Unstructured, error) {
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

	return dynamicCache.List(gvr, namespace)
}

// GetDynamic returns a single resource of any type using the dynamic cache
func (c *ResourceCache) GetDynamic(ctx context.Context, kind string, namespace string, name string) (*unstructured.Unstructured, error) {
	return c.GetDynamicWithGroup(ctx, kind, namespace, name, "")
}

// GetDynamicWithGroup returns a single resource, using the group to disambiguate
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

// ResourceStatus is an alias for topology.ResourceStatus so both packages share one definition.
type ResourceStatus = topology.ResourceStatus

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

		result := &ResourceStatus{
			Status:  status,
			Ready:   ready,
			Summary: ready + " ready",
		}

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

	case "persistentvolume", "persistentvolumes", "pv":
		if c.PersistentVolumes() == nil {
			return nil
		}
		pv, err := c.PersistentVolumes().Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: string(pv.Status.Phase),
		}

	case "storageclass", "storageclasses", "sc":
		if c.StorageClasses() == nil {
			return nil
		}
		if _, err := c.StorageClasses().Get(name); err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	case "poddisruptionbudget", "poddisruptionbudgets", "pdb":
		if c.PodDisruptionBudgets() == nil {
			return nil
		}
		pdb, err := c.PodDisruptionBudgets().PodDisruptionBudgets(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
			Ready:  fmt.Sprintf("%d/%d", pdb.Status.CurrentHealthy, pdb.Status.DesiredHealthy),
		}

	case "networkpolicy", "networkpolicies", "netpol":
		if c.NetworkPolicies() == nil {
			return nil
		}
		_, err := c.NetworkPolicies().NetworkPolicies(namespace).Get(name)
		if err != nil {
			return nil
		}
		return &ResourceStatus{
			Status: "Active",
		}

	default:
		return nil
	}
}

// GetResourceCount shadows the embedded method to count only core topology
// resource types (the 9 types rendered in the UI topology view).
func (c *ResourceCache) GetResourceCount() int {
	if c == nil {
		return 0
	}
	counts := c.ResourceCache.GetKindObjectCounts()
	total := 0
	for kind, n := range counts {
		switch kind {
		case "Pod", "Service", "Node", "Namespace", "Deployment",
			"DaemonSet", "StatefulSet", "ReplicaSet", "Ingress":
			total += n
		}
	}
	return total
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
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Status == corev1.ConditionFalse && cond.Message != "" {
			return cond.Message
		}
	}
	return ""
}

// getPodIssue returns the primary issue affecting a pod (if any)
func getPodIssue(pod *corev1.Pod) string {
	for _, cs := range pod.Status.InitContainerStatuses {
		if issue := getContainerIssue(&cs); issue != "" {
			return issue
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if issue := getContainerIssue(&cs); issue != "" {
			return issue
		}
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			if cond.Reason != "" {
				return cond.Reason
			}
		}
	}
	return ""
}

// getContainerIssue extracts the issue from a container status
func getContainerIssue(cs *corev1.ContainerStatus) string {
	if cs.State.Waiting != nil {
		reason := cs.State.Waiting.Reason
		if reason != "" && reason != "PodInitializing" && reason != "ContainerCreating" {
			return reason
		}
	}
	if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
		if cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
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
	Issues   map[string]int
	TopIssue string
	TopCount int
}

// getPodsIssueSummary analyzes a list of pods and returns issue summary
func getPodsIssueSummary(pods []*corev1.Pod) *PodIssueSummary {
	summary := &PodIssueSummary{
		Total:  len(pods),
		Issues: make(map[string]int),
	}

	for _, pod := range pods {
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
