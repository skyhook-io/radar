package k8s

import (
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// WorkloadPods returns the pods a workload controls right now, by controller
// ownership rather than label selector: a Deployment's selector can also
// match bare pods, a sibling controller's pods during a Rollout workloadRef
// migration, or anything that shares its labels, and none of those are the
// workload's pods. Ownership is the same relation kube-state-metrics
// records in kube_pod_owner, so history and the present agree on membership.
//
// Supported kinds (singular or plural, any case): Deployment and Rollout
// through their ReplicaSets, CronJob through its Jobs, and StatefulSet,
// DaemonSet, ReplicaSet, Job and Workflow directly. An unavailable lister
// returns ErrWorkloadAccessDenied and one still filling its initial sync
// returns ErrWorkloadCacheWarming, so an empty answer is never mistaken for
// "no pods". Pods are sorted by name.
func WorkloadPods(cache *ResourceCache, kind, namespace, name string) ([]*corev1.Pod, error) {
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
	ownedBy, err := workloadOwnershipTest(cache, kind, namespace, name)
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
func WorkloadPodNames(cache *ResourceCache, kind, namespace, name string) ([]string, error) {
	pods, err := WorkloadPods(cache, kind, namespace, name)
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
// ends at kind/name". Intermediate owners are looked up in the cache; when
// that lister is unavailable the answer is ErrWorkloadAccessDenied rather
// than a silently empty set.
func workloadOwnershipTest(cache *ResourceCache, kind, namespace, name string) (func(*corev1.Pod) bool, error) {
	direct := func(pod *corev1.Pod) bool {
		owner := metav1.GetControllerOf(pod)
		return owner != nil && owner.Kind == kind && owner.Name == name
	}
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
			rsOwner := metav1.GetControllerOf(rs)
			return rsOwner != nil && rsOwner.Kind == kind && rsOwner.Name == name
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
			jobOwner := metav1.GetControllerOf(job)
			return jobOwner != nil && jobOwner.Kind == "CronJob" && jobOwner.Name == name
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
