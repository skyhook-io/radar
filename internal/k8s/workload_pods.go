package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// WorkloadPods returns the pods a workload controls right now, by controller
// ownership rather than label selector: a Deployment's selector can also
// match bare pods, a sibling controller's pods during a Rollout workloadRef
// migration, or anything that shares its labels, and none of those are the
// workload's pods. The current root and every intermediate owner must match
// by API group, kind, name and UID.
//
// Supported kinds (singular or plural, any case): Deployment and Rollout
// through their ReplicaSets, CronJob through its Jobs, and StatefulSet,
// DaemonSet, ReplicaSet, Job and Workflow directly. An unavailable lister
// returns ErrWorkloadAccessDenied and one still filling its initial sync
// returns ErrWorkloadCacheWarming, so an empty answer is never mistaken for
// "no pods"; a workload the synced cache does not hold returns
// ErrWorkloadNotFound. Pods are sorted by name. Callers already holding the root use
// WorkloadPodsForUID to keep membership tied to that observation.
//
// Typed roots and every hop read local listers. A Rollout or Workflow root
// reads the dynamic cache, whose first read of a namespace may probe access
// and start a watch; that wait is bounded by ctx and dynamicRootReadBudget,
// past which the answer is ErrWorkloadCacheWarming.
func WorkloadPods(ctx context.Context, cache *ResourceCache, kind, namespace, name string) ([]*corev1.Pod, error) {
	canonical := CanonicalWorkloadKind(kind)
	if canonical == "" {
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
	kind = canonical
	if cache == nil || cache.ResourceCache == nil || cache.Pods() == nil {
		return nil, fmt.Errorf("%w: list pods", ErrWorkloadAccessDenied)
	}
	// Membership is answered from the pod lister, so a namespace it does not
	// cover is refused before the root is read; a dynamic root outside a
	// forced namespace scope would otherwise surface as a cache failure.
	if err := requireCovers(cache, "pods", namespace); err != nil {
		return nil, err
	}
	var obj runtime.Object
	var err error
	gvr, typed := lookupTypedBuiltinGVR(kind)
	if typed {
		if _, known := cache.InformerSynced(gvr.Resource); !known {
			return nil, fmt.Errorf("%w: list %s", ErrWorkloadAccessDenied, gvr.Resource)
		}
		if err := requireCovers(cache, gvr.Resource, namespace); err != nil {
			return nil, err
		}
		obj, err = FetchResource(cache, kind, namespace, name)
	} else {
		discovery, dynamicCache := GetResourceDiscovery(), GetDynamicResourceCache()
		if discovery == nil || dynamicCache == nil {
			return nil, fmt.Errorf("%w: read %s", ErrWorkloadCacheWarming, kind)
		}
		var found bool
		gvr, found = discovery.GetGVRWithGroup(kind, "argoproj.io")
		if !found {
			return nil, fmt.Errorf("%w: read %s", ErrUnknownDynamicKind, kind)
		}
		obj, err = readDynamicRoot(ctx, dynamicCache, gvr, namespace, name)
		if errors.Is(err, ErrWorkloadCacheWarming) {
			return nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return nil, fmt.Errorf("%w: read %s in %s: %v", ErrWorkloadAccessDenied, kind, namespace, err)
		}
		if err != nil && !errors.Is(err, k8score.ErrResourceNotFound) {
			return nil, err
		}
		if !dynamicCache.IsNamespaceSynced(gvr, namespace) {
			return nil, fmt.Errorf("%w: %s", ErrWorkloadCacheWarming, kind)
		}
		if errors.Is(err, k8score.ErrResourceNotFound) {
			return nil, fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, kind, namespace, name)
		}
	}
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: %s %s/%s", ErrWorkloadNotFound, kind, namespace, name)
	}
	if err != nil {
		return nil, err
	}
	objectMeta, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	return WorkloadPodsForUID(cache, kind, namespace, name, gvr.Group, objectMeta.GetUID())
}

// dynamicRootReadBudget bounds how long a membership read waits for a Rollout
// or Workflow inventory that is not yet watched. The acquisition keeps running
// after the wait ends, so a later read finds the inventory ready.
var dynamicRootReadBudget = 3 * time.Second

// readDynamicRoot serves a watched, synced root from the store without
// blocking. Otherwise it runs the ordinary acquiring read, which may wait on
// CRD discovery, access probes and an initial sync without a context, and
// stops waiting for it at ctx or the budget.
func readDynamicRoot(ctx context.Context, cache *DynamicResourceCache, gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if cache.HasCoveringInformer(gvr, namespace) && cache.IsNamespaceSynced(gvr, namespace) {
		return cache.GetWatched(gvr, namespace, name)
	}
	type result struct {
		obj *unstructured.Unstructured
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[k8s] Dynamic root read for %s %s/%s panicked: %v\n%s", gvr.Resource, namespace, name, r, debug.Stack())
				done <- result{err: fmt.Errorf("read %s %s/%s: %v", gvr.Resource, namespace, name, r)}
			}
		}()
		obj, err := cache.Get(gvr, namespace, name)
		done <- result{obj: obj, err: err}
	}()
	budget := time.NewTimer(dynamicRootReadBudget)
	defer budget.Stop()
	select {
	case r := <-done:
		return r.obj, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-budget.C:
		return nil, fmt.Errorf("%w: %s inventory in %s is still starting", ErrWorkloadCacheWarming, gvr.Resource, namespace)
	}
}

// WorkloadPodsForUID follows the current root and every intermediate controller
// by API group, kind, name and UID. It reads only already-watched cache listers.
// A missing root UID cannot establish current membership and returns no Pods.
func WorkloadPodsForUID(cache *ResourceCache, kind, namespace, name, group string, uid types.UID) ([]*corev1.Pod, error) {
	return workloadPods(cache, kind, namespace, name, workloadOwnerIdentity{group: group, uid: uid})
}

type workloadOwnerIdentity struct {
	group string
	uid   types.UID
}

func workloadPods(cache *ResourceCache, kind, namespace, name string, identity workloadOwnerIdentity) ([]*corev1.Pod, error) {
	canonical := CanonicalWorkloadKind(kind)
	if canonical == "" {
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
	kind = canonical
	if cache == nil || cache.ResourceCache == nil || cache.Pods() == nil {
		return nil, fmt.Errorf("%w: list pods", ErrWorkloadAccessDenied)
	}
	// Every kind reaches its pods through this lister, including the ones
	// that need no ownership hop, so it is gated before the hop is chosen.
	if err := requireCovers(cache, "pods", namespace); err != nil {
		return nil, err
	}
	ownedBy, err := workloadOwnershipTest(cache, kind, namespace, name, identity)
	if err != nil {
		return nil, err
	}
	pods, err := cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}
	var owned []*corev1.Pod
	for _, pod := range pods {
		if pod != nil && ownedBy(pod) {
			owned = append(owned, pod)
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	return owned, nil
}

// WorkloadPodNames is WorkloadPods reduced to sorted names.
func WorkloadPodNames(ctx context.Context, cache *ResourceCache, kind, namespace, name string) ([]string, error) {
	pods, err := WorkloadPods(ctx, cache, kind, namespace, name)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names, nil
}

// CanonicalWorkloadKind maps the accepted spellings to the controller Kind as
// it appears in an ownerReference, or "" for a kind whose pods this package
// cannot establish. Callers use it to reject a kind before doing work.
func CanonicalWorkloadKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments":
		return "Deployment"
	case "statefulset", "statefulsets":
		return "StatefulSet"
	case "daemonset", "daemonsets":
		return "DaemonSet"
	case "replicaset", "replicasets":
		return "ReplicaSet"
	case "job", "jobs":
		return "Job"
	case "cronjob", "cronjobs":
		return "CronJob"
	case "rollout", "rollouts":
		return "Rollout"
	case "workflow", "workflows":
		return "Workflow"
	default:
		return ""
	}
}

// workloadOwnershipTest returns the predicate "this pod's controller chain
// ends at the observed root incarnation". Intermediate owners are looked up
// in the cache; when that lister is unavailable the answer is ErrWorkloadAccessDenied rather
// than a silently empty set.
func workloadOwnershipTest(cache *ResourceCache, kind, namespace, name string, identity workloadOwnerIdentity) (func(*corev1.Pod) bool, error) {
	matchesRoot := func(owner *metav1.OwnerReference) bool {
		return controllerMatchesIdentity(owner, kind, name, identity.group, identity.uid)
	}
	direct := func(pod *corev1.Pod) bool { return matchesRoot(metav1.GetControllerOf(pod)) }
	switch kind {
	case "Deployment", "Rollout":
		rsLister := cache.ReplicaSets()
		if rsLister == nil {
			return nil, fmt.Errorf("%w: list replicasets", ErrWorkloadAccessDenied)
		}
		if err := requireCovers(cache, "replicasets", namespace); err != nil {
			return nil, err
		}
		return func(pod *corev1.Pod) bool {
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.Kind != "ReplicaSet" {
				return false
			}
			rs, err := rsLister.ReplicaSets(pod.Namespace).Get(owner.Name)
			if err != nil {
				return false
			}
			if !controllerMatchesIdentity(owner, "ReplicaSet", rs.Name, "apps", rs.UID) {
				return false
			}
			return matchesRoot(metav1.GetControllerOf(rs))
		}, nil
	case "CronJob":
		jobLister := cache.Jobs()
		if jobLister == nil {
			return nil, fmt.Errorf("%w: list jobs", ErrWorkloadAccessDenied)
		}
		if err := requireCovers(cache, "jobs", namespace); err != nil {
			return nil, err
		}
		return func(pod *corev1.Pod) bool {
			owner := metav1.GetControllerOf(pod)
			if owner == nil || owner.Kind != "Job" {
				return false
			}
			job, err := jobLister.Jobs(pod.Namespace).Get(owner.Name)
			if err != nil {
				return false
			}
			if !controllerMatchesIdentity(owner, "Job", job.Name, "batch", job.UID) {
				return false
			}
			return matchesRoot(metav1.GetControllerOf(job))
		}, nil
	default:
		return direct, nil
	}
}

// requireCovers refuses to read an informer that cannot answer authoritatively
// for this namespace: one still filling its initial sync, or one scoped to
// other namespaces. Both return an empty list rather than an error, and an
// empty list here becomes "this workload has no pods" — a claim neither state
// supports. KindCoversNamespace says so itself: a miss is not zero objects.
func requireCovers(cache *ResourceCache, key, namespace string) error {
	if err := requireSynced(cache, key); err != nil {
		return err
	}
	if !cache.KindCoversNamespace(key, namespace) {
		return fmt.Errorf("%w: list %s in %s", ErrWorkloadAccessDenied, key, namespace)
	}
	return nil
}

// requireSynced refuses a hop through an informer that has not finished its
// initial sync. Its lister is non-nil the whole time it fills, so every
// ownership lookup would miss and the workload would come back with no pods at
// all — an empty answer indistinguishable from a workload that has none. A
// kind this cache never watched is not an error here: the caller reached a
// lister for it, and only the sync state is in question.
func requireSynced(cache *ResourceCache, key string) error {
	if synced, known := cache.InformerSynced(key); known && !synced {
		return fmt.Errorf("%w: %s", ErrWorkloadCacheWarming, key)
	}
	return nil
}

// DirectlyOwnedPods excludes same-name previous incarnations and label-only matches.
func DirectlyOwnedPods(cache *ResourceCache, namespace, name, group, kind string, uid types.UID) ([]*corev1.Pod, error) {
	if cache == nil || cache.ResourceCache == nil || cache.Pods() == nil {
		return nil, fmt.Errorf("%w: list pods", ErrWorkloadAccessDenied)
	}
	if err := requireCovers(cache, "pods", namespace); err != nil {
		return nil, err
	}
	pods, err := cache.Pods().Pods(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	result := []*corev1.Pod{}
	if uid == "" {
		return result, nil
	}
	for _, pod := range pods {
		if pod == nil || pod.Namespace != namespace {
			continue
		}
		owner := metav1.GetControllerOf(pod)
		if controllerMatchesIdentity(owner, kind, name, group, uid) {
			result = append(result, pod)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// EventDescribesIncarnation reports whether an event about a same-named
// object describes the incarnation whose UID is uid. Events outlive their
// object, so a known, different UID belongs to an earlier incarnation; an
// event without a UID cannot say which one it is about and is kept. Kubelet
// reports a static Pod under its static UID, which the API mirror records in
// kubernetes.io/config.mirror; pass it as staticUID, or "" for anything else.
func EventDescribesIncarnation(eventUID, uid, staticUID types.UID) bool {
	return eventUID == "" || uid == "" || eventUID == uid || (staticUID != "" && eventUID == staticUID)
}

// controllerMatchesIdentity is shared by direct and multi-hop ownership checks.
func controllerMatchesIdentity(owner *metav1.OwnerReference, kind, name, group string, uid types.UID) bool {
	if owner == nil || uid == "" || owner.UID != uid || owner.Kind != kind || owner.Name != name {
		return false
	}
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	return err == nil && gv.Group == group
}
