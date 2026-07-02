package k8s

import (
	"context"
	"errors"
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

// ListPageSize, when > 0, makes high-cardinality informers (Pods, ReplicaSets)
// paginate their initial LIST instead of pulling it in one response (set via
// --list-page-size flag). Opt-in hardening for very large clusters where
// WatchList streaming isn't available; 0 keeps the standard behavior.
var ListPageSize int64

// ForceNamespaceScope pins all namespaced informer caches to one namespace.
// Cluster-scoped resources still use cluster-wide informers.
var ForceNamespaceScope bool

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
// Critical: pods, deployments, services, statefulsets, daemonsets, nodes, namespaces, ingresses, jobs, cronjobs.
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
	"resourcequotas":           true, // scheduling/admission diagnostics, not first-render
}

// minimalFirstPaintSet is the subset of critical informers the home
// dashboard needs to feel coherent. Pods are included despite being
// typically the largest kind — without pods the topology graph and
// resource counts are empty. The patience window absorbs pod-sync
// latency on healthy clusters; on slow ones, the user sees a working
// home view sooner with a "still loading" hint for the rest.
var minimalFirstPaintSet = map[string]bool{
	"pods":        true,
	"namespaces":  true,
	"nodes":       true,
	"services":    true,
	"deployments": true,
}

// firstPaintPatience is how long we wait for ALL critical informers before
// falling back to the minimal set. On most clusters the full critical set
// syncs well inside this window, so first paint is complete and there is
// no progressive fill-in. Slow clusters fall through to the minimal-set
// gate and render with whatever is ready then.
const firstPaintPatience = 8 * time.Second

// firstPaintBackstop now lives in deadlines.go as the exported package
// variable FirstPaintBackstop so operators can widen the bound from the
// command line without recompiling. The 5-minute default is preserved.

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

		// Probe per-resource list access before creating informers. The
		// returned scope map is authoritative for both enablement and
		// per-kind namespace scoping (some kinds may be cluster-wide while
		// others are namespace-scoped to the same fallback namespace).
		rbacStart := time.Now()
		rbacCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		permResult := CheckResourcePermissions(rbacCtx)
		cancel()
		logTiming("    Resource access probes: %v", time.Since(rbacStart))

		if ctx.Err() != nil {
			initErr = ctx.Err()
			return
		}

		// scopes drives which informers are created and at what scope.
		// k8score routes each kind through the matching factory (cluster-wide
		// or namespace-scoped) based on these per-kind decisions.
		scopes := permResult.Scopes
		if scopes == nil {
			scopes = map[string]k8score.ResourceScope{}
		}

		cfg := k8score.CacheConfig{
			Client:              k8sClient,
			ResourceScopes:      scopes,
			DeferredTypes:       deferredResources,
			DebugEvents:         DebugEvents,
			TimingLogger:        logTiming,
			PatienceWindow:      firstPaintPatience,
			MinimalSet:          minimalFirstPaintSet,
			SyncTimeout:         FirstPaintBackstop,
			SyncProgress:        emitSyncProgress,
			DeferredSyncTimeout: 3 * time.Minute,
			ListPageSize:        ListPageSize,

			OnReceived: func(kind string) {
				timeline.IncrementReceived(kind)
			},

			OnChange: func(change k8score.ResourceChange, obj, oldObj any) {
				if DebugEvents && change.Operation == "add" &&
					(change.Kind == "Pod" || change.Kind == "Deployment" || change.Kind == "Service") {
					log.Printf("[DEBUG] enqueueChange: %s add %s/%s", change.Kind, change.Namespace, change.Name)
				}

				// Record to timeline store
				recordToTimelineStore(change.Kind, change.Namespace, change.Name, change.UID, change.Operation, oldObj, obj, change.Diff, true)
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
			secretsEnabled: scopes["secrets"].Enabled,
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
	resetRecreateStash()
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
	timelineEvent.ClusterContext = ActiveClusterContext()

	ctx := context.Background()
	if err := timeline.RecordEventWithBroadcast(ctx, timelineEvent); err != nil {
		log.Printf("Warning: failed to record K8s event to timeline store: %v", err)
	} else if DebugEvents {
		timeline.IncrementRecorded("K8sEvent:" + event.InvolvedObject.Kind)
	}
}

// emitSyncProgress is the SyncProgress callback wired into the resource
// cache. It keeps the connection's progressMessage in step with the live
// informer-sync count so the connecting screen ticks up instead of
// sitting on a static message during a 30–60s sync. Once the cache
// returns and connection state flips to "connected", further progress
// lives in the home dashboard's deferred-loading indicator.
func emitSyncProgress(synced, total int, minimalReady bool) {
	if total == 0 {
		return
	}
	// Only update while we're still in the connecting phase. Once
	// connected, the connecting screen is gone and the message is moot.
	if GetConnectionStatus().State != StateConnecting {
		return
	}
	var msg string
	switch {
	case synced == total:
		msg = "Finalizing…"
	case minimalReady:
		msg = fmt.Sprintf("Loading cluster data… %d of %d ready (showing partial)", synced, total)
	default:
		msg = fmt.Sprintf("Loading cluster data… %d of %d ready", synced, total)
	}
	UpdateConnectionProgress(msg)
}

// isNoisyResource returns true if this resource generates constant updates that aren't interesting
func isNoisyResource(kind, name, op string) bool {
	if op != "update" {
		return false
	}

	switch kind {
	case "Lease", "Endpoints", "EndpointSlice", "Event":
		return true
	case "VerticalPodAutoscalerCheckpoint":
		// VPA writes per-container resource recommendations here on every
		// reconcile (~1m). The whole point of the resource is to be a live
		// ticker — per-update is never user-meaningful.
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

// getGeneration returns the metadata.generation of an informer object, or 0
// if the object is nil or doesn't expose metav1.Object. Both typed K8s
// resources and *unstructured.Unstructured satisfy metav1.Object.
func getGeneration(obj any) int64 {
	if obj == nil {
		return 0
	}
	m, ok := obj.(metav1.Object)
	if !ok {
		return 0
	}
	return m.GetGeneration()
}

// recordToTimelineStore records a resource change to the timeline. When the
// caller has already computed the update diff (the cache layer does, before
// firing OnChange), it passes it via precomputedDiff + diffPrecomputed=true so
// we don't recompute the identical diff on the hottest per-update path. Callers
// without a precomputed diff (tests, non-cache paths) pass nil + false and the
// diff is computed here as before.
func recordToTimelineStore(kind, namespace, name, uid, op string, oldObj, newObj any, precomputedDiff *DiffInfo, diffPrecomputed bool) {
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

	if op == "delete" {
		stashDeletedForRecreate(kind, namespace, name, uid, obj)
	}

	owner := timeline.ExtractOwner(obj)
	labels := timeline.ExtractLabels(obj)
	healthState := classifyTimelineHealth(kind, obj, time.Now())
	apiVersion := extractAPIVersion(obj)

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
		// Reuse the cache layer's already-computed diff on the hot path; only
		// recompute for callers (tests / non-cache) that didn't precompute it.
		localDiff := precomputedDiff
		if !diffPrecomputed {
			localDiff = ComputeDiff(kind, oldObj, newObj)
		}
		if localDiff != nil {
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
		} else if KindHasDiffer(kind) || isUnstructuredUpdate(oldObj, newObj) {
			// Diff handling found nothing observable — usually a heartbeat,
			// managedFields-only update, or reconcile counter.
			// Before dropping, check metadata.generation: it bumps only on
			// spec changes (status updates don't touch it), so a generation
			// flip with a nil diff means our diff function missed a real spec
			// field. Record those with a fallback summary instead of silently
			// losing them — diff coverage gaps shouldn't become silent drops.
			if oldGen, newGen := getGeneration(oldObj), getGeneration(newObj); oldGen != newGen && oldGen > 0 && newGen > 0 {
				diff = &timeline.DiffInfo{
					Fields: []timeline.FieldChange{{
						Path:     "metadata.generation",
						OldValue: oldGen,
						NewValue: newGen,
					}},
					Summary: fmt.Sprintf("spec changed (gen %d→%d, fields not specifically tracked)", oldGen, newGen),
				}
			} else {
				timeline.RecordDrop(kind, namespace, name, timeline.DropReasonNoDiff, op)
				if DebugEvents {
					log.Printf("[DEBUG] No-diff update, skipping: %s/%s/%s", kind, namespace, name)
				}
				return
			}
		}
	}

	// Recreate-join: an add that replaces a just-deleted object of the same
	// name but a different UID carries the diff against its predecessor, so
	// the change feed can show what the recreate changed instead of a
	// contentless delete+add pair. Guarded to young objects post-sync — the
	// same conditions under which the add below is recorded at all.
	//
	// Status is stripped from BOTH sides before diffing: every recreate
	// resets status, so cross-recreate status deltas (ready 1→0, condition
	// flips) are tautological noise — and worse, a spec-identical recreate
	// (namespace re-apply) would otherwise emit a status-only "recreated
	// with changes" entry that reads as a config change.
	recreated := false
	if op == "add" && newObj != nil && initialSyncComplete {
		if meta, ok := newObj.(metav1.Object); ok && time.Since(meta.GetCreationTimestamp().Time) <= 30*time.Second {
			if stashed, ok := takeRecreateMatch(kind, namespace, name, uid); ok {
				if localDiff := ComputeDiff(kind, stripStatusForRecreateDiff(stashed), stripStatusForRecreateDiff(newObj)); localDiff != nil && len(localDiff.Fields) > 0 {
					diff = &timeline.DiffInfo{
						Fields:  make([]timeline.FieldChange, len(localDiff.Fields)),
						Summary: "recreated with changes: " + localDiff.Summary,
					}
					for i, f := range localDiff.Fields {
						diff.Fields[i] = timeline.FieldChange{
							Path:     f.Path,
							OldValue: f.OldValue,
							NewValue: f.NewValue,
						}
					}
					recreated = true
				}
			}
		}
	}

	event := timeline.NewInformerEvent(
		kind, apiVersion, namespace, name, uid,
		timeline.OperationToEventType(op),
		healthState,
		diff,
		owner,
		labels,
		createdAt,
	)
	event.ClusterContext = ActiveClusterContext()
	if recreated {
		event.Reason = timeline.ReasonRecreated
	}

	var events []timeline.TimelineEvent
	if op == "add" && newObj != nil {
		historicalEvents := extractTimelineHistoricalEvents(kind, apiVersion, namespace, name, newObj, owner, labels)
		for i := range historicalEvents {
			historicalEvents[i].ClusterContext = event.ClusterContext
		}
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
					timeline.RecordDrop(kind, namespace, name, timeline.DropReasonStoreFailed, op)
					return
				}
			}
			store.MarkResourceSeen(kind, namespace, name)
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

func isUnstructuredUpdate(oldObj, newObj any) bool {
	_, oldOK := oldObj.(*unstructured.Unstructured)
	_, newOK := newObj.(*unstructured.Unstructured)
	return oldOK && newOK
}

// extractAPIVersion returns the resource's apiVersion (e.g. "cluster.x-k8s.io/v1beta1")
// for unstructured/CRD objects. Typed informer objects strip kind/apiVersion, so they
// fall through to "" — the navigation layer treats an empty group as "core/typed kind",
// which is correct since core kinds don't collide.
func extractAPIVersion(obj any) string {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u.GetAPIVersion()
	}
	return ""
}

// extractTimelineHistoricalEvents extracts historical events from resource metadata/status
func extractTimelineHistoricalEvents(kind, apiVersion, namespace, name string, obj any, owner *timeline.OwnerInfo, labels map[string]string) []timeline.TimelineEvent {
	var events []timeline.TimelineEvent

	switch kind {
	case "Pod":
		if pod, ok := obj.(*corev1.Pod); ok {
			if !pod.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					pod.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
			if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
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
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					cond.LastTransitionTime.Time, string(cond.Type), cond.Message, health, owner, labels))
			}
		}

	case "Deployment":
		if deploy, ok := obj.(*appsv1.Deployment); ok {
			if !deploy.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
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
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
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
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					rs.CreationTimestamp.Time, "created", "", health, owner, labels))
			}
		}

	case "StatefulSet":
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			if !sts.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					sts.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
		}

	case "DaemonSet":
		if ds, ok := obj.(*appsv1.DaemonSet); ok {
			if !ds.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					ds.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
		}

	case "Service":
		if svc, ok := obj.(*corev1.Service); ok {
			if !svc.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					svc.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "Ingress":
		if ing, ok := obj.(*networkingv1.Ingress); ok {
			if !ing.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					ing.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "CronJob":
		if cj, ok := obj.(*batchv1.CronJob); ok {
			if !cj.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					cj.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "HorizontalPodAutoscaler":
		if hpa, ok := obj.(*autoscalingv2.HorizontalPodAutoscaler); ok {
			if !hpa.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					hpa.CreationTimestamp.Time, "created", "", timeline.HealthHealthy, owner, labels))
			}
		}

	case "Job":
		if job, ok := obj.(*batchv1.Job); ok {
			if !job.CreationTimestamp.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					job.CreationTimestamp.Time, "created", "", timeline.HealthUnknown, owner, labels))
			}
			if job.Status.StartTime != nil && !job.Status.StartTime.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					job.Status.StartTime.Time, "started", "", timeline.HealthDegraded, owner, labels))
			}
			if job.Status.CompletionTime != nil && !job.Status.CompletionTime.IsZero() {
				// CompletionTime is set only on SUCCESS — a completed Job is
				// neutral/idle (done by design), even if earlier attempts failed
				// (Status.Failed > 0 counts retries, not a terminal failure).
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
					job.Status.CompletionTime.Time, "completed", "", timeline.HealthNeutral, owner, labels))
			} else {
				// Terminal failure (backoffLimit exceeded) has no CompletionTime —
				// surface the JobFailed condition as unhealthy at its transition time.
				for _, cond := range job.Status.Conditions {
					if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
						events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
							cond.LastTransitionTime.Time, "failed", cond.Message, timeline.HealthUnhealthy, owner, labels))
						break
					}
				}
			}
		}

	default:
		if u, ok := obj.(*unstructured.Unstructured); ok {
			ct := u.GetCreationTimestamp()
			if !ct.IsZero() {
				events = append(events, timeline.NewHistoricalEvent(kind, apiVersion, namespace, name,
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
	"role": true, "roles": true,
	"clusterrole": true, "clusterroles": true,
	"rolebinding": true, "rolebindings": true,
	"clusterrolebinding": true, "clusterrolebindings": true,
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
		gvr, ok = builtinGVRFallback(kind, group)
	}

	if !ok {
		if group != "" {
			return nil, fmt.Errorf("%w: %s (group: %s)", ErrUnknownDynamicKind, kind, group)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknownDynamicKind, kind)
	}

	dynamicCache := GetDynamicResourceCache()
	if dynamicCache == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	if gvr.Group == "discovery.k8s.io" && gvr.Resource == "endpointslices" {
		return dynamicCache.ListDirect(ctx, gvr, namespace)
	}

	return dynamicCache.List(gvr, namespace)
}

// builtinGVRFallback resolves a built-in kind's GVR from the static table when
// API discovery couldn't (partial discovery under restricted RBAC, or a
// transient refresh miss). It only resolves built-ins addressed by their own
// group, so a CRD whose plural shadows a built-in still falls through as
// unknown. Live/dynamic fetch paths (notably the GitOps drift last-applied GET,
// which can't use the typed cache) rely on this so they don't silently fail
// when discovery is incomplete.
func builtinGVRFallback(kind, group string) (schema.GroupVersionResource, bool) {
	return BuiltinGVR(kind, group)
}

// ErrUnknownDynamicKind is returned by ListDynamic / GetDynamicWithGroup when
// the requested kind has no registered GVR in API discovery. Wrapped with
// fmt.Errorf("%w: ..."), so callers should match with errors.Is. The HTTP
// layer translates this to 400 Bad Request.
var ErrUnknownDynamicKind = errors.New("unknown resource kind")

// GetDynamic returns a single resource of any type using the dynamic cache
func (c *ResourceCache) GetDynamic(ctx context.Context, kind string, namespace string, name string) (*unstructured.Unstructured, error) {
	return c.GetDynamicWithGroup(ctx, kind, namespace, name, "")
}

// GetDynamicWithGroup returns a single resource, using the group to disambiguate
func (c *ResourceCache) GetDynamicWithGroup(ctx context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error) {
	return c.getDynamicWithGroup(ctx, kind, namespace, name, group, false)
}

// GetDynamicWithGroupPreserveLastApplied returns a single resource while
// preserving kubectl last-applied for internal drift computation. Do not use
// for API/UI/MCP responses.
func (c *ResourceCache) GetDynamicWithGroupPreserveLastApplied(ctx context.Context, kind string, namespace string, name string, group string) (*unstructured.Unstructured, error) {
	return c.getDynamicWithGroup(ctx, kind, namespace, name, group, true)
}

func (c *ResourceCache) getDynamicWithGroup(ctx context.Context, kind string, namespace string, name string, group string, preserveLastApplied bool) (*unstructured.Unstructured, error) {
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
		gvr, ok = builtinGVRFallback(kind, group)
	}

	if !ok {
		if group != "" {
			return nil, fmt.Errorf("%w: %s (group: %s)", ErrUnknownDynamicKind, kind, group)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknownDynamicKind, kind)
	}

	dynamicCache := GetDynamicResourceCache()
	if dynamicCache == nil {
		return nil, fmt.Errorf("dynamic resource cache not initialized")
	}

	// CRD detail views need spec.versions[].schema and spec.conversion, which
	// the dynamic cache strips to save memory. Bypass the cache on single-CRD
	// fetches so the YAML tab and MCP get_resource see the full object; list
	// views (which don't render schemas) still go through the cache.
	//
	// preserveLastApplied also goes via direct GET. The cached path would
	// otherwise force-start a dynamic informer for the resource's GVR — fine
	// for CRDs but a memory regression for core kinds (apps/Deployment,
	// /v1/Service, etc.) that Argo's status.resources commonly references,
	// since the informer would retain last-applied across every object
	// cluster-wide just to power a per-page-load drift diff.
	var u *unstructured.Unstructured
	var err error
	if gvr.Group == "discovery.k8s.io" && gvr.Resource == "endpointslices" {
		u, err = dynamicCache.GetDirect(ctx, gvr, namespace, name)
	} else if gvr.Group == "apiextensions.k8s.io" && gvr.Resource == "customresourcedefinitions" {
		u, err = dynamicCache.GetDirect(ctx, gvr, namespace, name)
	} else if gvr.Group == "apiregistration.k8s.io" && gvr.Resource == "apiservices" {
		u, err = dynamicCache.GetDirect(ctx, gvr, namespace, name)
	} else if preserveLastApplied {
		u, err = dynamicCache.GetDirectPreserveLastApplied(ctx, gvr, namespace, name)
	} else {
		u, err = dynamicCache.Get(gvr, namespace, name)
	}
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

	case "role", "roles":
		if c.Roles() == nil {
			return nil
		}
		if _, err := c.Roles().Roles(namespace).Get(name); err != nil {
			return nil
		}
		return &ResourceStatus{Status: "Active"}

	case "clusterrole", "clusterroles":
		if c.ClusterRoles() == nil {
			return nil
		}
		if _, err := c.ClusterRoles().Get(name); err != nil {
			return nil
		}
		return &ResourceStatus{Status: "Active"}

	case "rolebinding", "rolebindings":
		if c.RoleBindings() == nil {
			return nil
		}
		if _, err := c.RoleBindings().RoleBindings(namespace).Get(name); err != nil {
			return nil
		}
		return &ResourceStatus{Status: "Active"}

	case "clusterrolebinding", "clusterrolebindings":
		if c.ClusterRoleBindings() == nil {
			return nil
		}
		if _, err := c.ClusterRoleBindings().Get(name); err != nil {
			return nil
		}
		return &ResourceStatus{Status: "Active"}

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
