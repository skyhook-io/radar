package vulnerabilities

import (
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
)

// target is one container image that runs, or is scheduled to run, keyed the
// way Trivy Operator names the object it scanned: a Deployment's Pods by
// their ReplicaSet, a CronJob's by the CronJob, other Pods by the Pod.
type target struct {
	key   targetKey
	uid   types.UID
	image string
	// scannedGroup is the API group of key.kind, for the caller's access.
	scannedGroup string
	workload     WorkloadRef
	container    string
}

// targetKey identifies the scanned object and container; image tells apart
// two revisions of one StatefulSet or DaemonSet running side by side.
type targetKey struct {
	namespace, kind, name, container, image string
}

func (k targetKey) owner() ownerKey {
	return ownerKey{namespace: k.namespace, kind: k.kind, name: k.name, container: k.container}
}

type ownerKey struct {
	namespace, kind, name, container string
}

// scannedByOwner are the controllers whose Pods Trivy Operator scans through
// the controller. Pods with any other controller (an operator's own CRD) are
// scanned as Pods.
var scannedByOwner = map[string]bool{"ReplicaSet": true, "ReplicationController": true, "StatefulSet": true, "DaemonSet": true, "Job": true}

// liveTargets lists the container images of Pods that aren't finished
// (Pending, Running, Unknown), init containers included, and of CronJobs that
// aren't suspended, which run on schedule and usually have no Pod between
// runs. canRead is the caller's access to a kind in a namespace (nil allows
// all): Pods are read only in namespaces where the caller may read Pods, and
// a target is listed only when the caller may read both the scanned object
// and the workload it names. The caller checks that namespaces' Pods have
// loaded (AssessKindRead); ok is false when Radar has no Pod cache.
func liveTargets(cache *k8s.ResourceCache, namespaces []string, canRead func(group, kind, namespace string) bool) (live liveSet, ok bool) {
	if cache == nil || cache.Pods() == nil {
		return liveSet{}, false
	}
	var targets []target
	var podDenied []string
	withheld := map[ownerKey]bool{}
	may := func(group, kind, ns string) bool { return canRead == nil || canRead(group, kind, ns) }
	podAccess := map[string]bool{}
	mayPods := func(ns string) bool {
		allowed, seen := podAccess[ns]
		if !seen {
			allowed = may("", "Pod", ns)
			podAccess[ns] = allowed
			if !allowed {
				podDenied = append(podDenied, ns)
			}
		}
		return allowed
	}
	seen := map[targetKey]bool{}
	add := func(t target) {
		if seen[t.key] {
			return
		}
		if !may(t.scannedGroup, t.key.kind, t.key.namespace) || !may(t.workload.Group, t.workload.Kind, t.workload.Namespace) {
			withheld[t.key.owner()] = true
			return
		}
		seen[t.key] = true
		targets = append(targets, t)
	}
	for _, ns := range scopeNamespaces(namespaces) {
		var pods []*corev1.Pod
		var err error
		if ns == "" {
			pods, err = cache.Pods().List(labels.Everything())
		} else {
			pods, err = cache.Pods().Pods(ns).List(labels.Everything())
		}
		if err != nil {
			continue
		}
		for _, pod := range pods {
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed || !mayPods(pod.Namespace) {
				continue
			}
			kind, group, name, uid := scannedOwner(cache, pod)
			workload := podWorkload(cache, pod)
			for _, c := range podContainers(pod.Spec) {
				add(target{
					key:          targetKey{namespace: pod.Namespace, kind: kind, name: name, container: c.Name, image: c.Image},
					uid:          uid,
					image:        c.Image,
					scannedGroup: group,
					workload:     workload,
					container:    c.Name,
				})
			}
		}
		if cache.CronJobs() == nil {
			continue
		}
		var cronJobs []*batchv1.CronJob
		if ns == "" {
			cronJobs, _ = cache.CronJobs().List(labels.Everything())
		} else {
			cronJobs, _ = cache.CronJobs().CronJobs(ns).List(labels.Everything())
		}
		for _, cj := range cronJobs {
			if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
				continue
			}
			workload := WorkloadRef{Kind: "CronJob", Group: "batch", Namespace: cj.Namespace, Name: cj.Name}
			for _, c := range podContainers(cj.Spec.JobTemplate.Spec.Template.Spec) {
				add(target{
					key:          targetKey{namespace: cj.Namespace, kind: "CronJob", name: cj.Name, container: c.Name, image: c.Image},
					uid:          cj.UID,
					image:        c.Image,
					scannedGroup: "batch",
					workload:     workload,
					container:    c.Name,
				})
			}
		}
	}
	slices.Sort(podDenied)
	return liveSet{targets: targets, podDenied: podDenied, withheld: withheld}, true
}

// liveSet is what runs, as far as the caller may see it: podDenied are
// namespaces whose Pods the caller can't read, and withheld are scanned
// objects the caller can't read, whose reports must not count as inactive.
type liveSet struct {
	targets   []target
	podDenied []string
	withheld  map[ownerKey]bool
}

func podContainers(spec corev1.PodSpec) []corev1.Container {
	return append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
}

// scopeNamespaces turns a namespace scope into the list calls to make: one
// cluster-wide call ("") for nil.
func scopeNamespaces(namespaces []string) []string {
	if namespaces == nil {
		return []string{""}
	}
	return namespaces
}

// scannedOwner is the object Trivy Operator scans for a Pod: a built-in
// controller, except that a Job's Pods are scanned through the CronJob that
// created the Job; any other Pod is scanned itself.
func scannedOwner(cache *k8s.ResourceCache, pod *corev1.Pod) (kind, group, name string, uid types.UID) {
	owner := controllerOf(pod.OwnerReferences)
	if owner == nil || !scannedByOwner[owner.Kind] {
		return "Pod", "", pod.Name, pod.UID
	}
	ownerGroup := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).Group
	if owner.Kind == "Job" && cache.Jobs() != nil {
		if job, err := cache.Jobs().Jobs(pod.Namespace).Get(owner.Name); err == nil {
			if parent := controllerOf(job.OwnerReferences); parent != nil && parent.Kind == "CronJob" {
				return "CronJob", "batch", parent.Name, parent.UID
			}
		}
	}
	return owner.Kind, ownerGroup, owner.Name, owner.UID
}

// podWorkload is the workload a person would name for a Pod: a Deployment
// (or Rollout) over its ReplicaSet, a CronJob over its Job, else its
// controller, else the Pod.
func podWorkload(cache *k8s.ResourceCache, pod *corev1.Pod) WorkloadRef {
	w, ok := k8s.PodWorkload(cache, pod)
	if !ok {
		return WorkloadRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	}
	if w.Kind == "Job" && cache.Jobs() != nil {
		if job, err := cache.Jobs().Jobs(pod.Namespace).Get(w.Name); err == nil {
			if parent := controllerOf(job.OwnerReferences); parent != nil && parent.Kind == "CronJob" {
				return WorkloadRef{Kind: "CronJob", Group: "batch", Namespace: pod.Namespace, Name: parent.Name}
			}
		}
	}
	return WorkloadRef{Kind: w.Kind, Group: w.Group, Namespace: pod.Namespace, Name: w.Name}
}

func controllerOf(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}
