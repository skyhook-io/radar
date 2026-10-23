package summarycontext

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// trivyScanTarget resolves the object a Trivy Operator report scanned and,
// when that object is or belongs to a workload, the workload a human would
// name for it. Trivy reports are kept out of topology, so the relationship
// walk finds no owner for them; this reads the report's controller reference
// (or trivy-operator's resource labels) and walks a ReplicaSet or Pod up to
// its workload through the cache. Reports about non-workloads (a Service, a
// Role) get only the scanned object: managedBy names workloads and GitOps
// owners, not whatever a report happens to describe.
func trivyScanTarget(cache *k8s.ResourceCache, u *unstructured.Unstructured) (*resourcecontext.ManagedByRef, *resourcecontext.ScannedRef) {
	labels := u.GetLabels()
	ns := u.GetNamespace()
	var group, kind, name string
	var uid types.UID
	if ref := controllerRef(u.GetOwnerReferences()); ref != nil {
		group = schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).Group
		kind, name, uid = ref.Kind, ref.Name, ref.UID
	} else {
		kind, name = labels["trivy-operator.resource.kind"], labels["trivy-operator.resource.name"]
	}
	if kind == "" || name == "" {
		return nil, nil
	}
	scanned := &resourcecontext.ScannedRef{Kind: kind, Name: name, Container: labels["trivy-operator.container.name"]}
	var self *resourcecontext.ManagedByRef
	if workloadKinds[kind] {
		self = resourcecontext.ManagedByFromOwner(kind, group, ns, name)
	}
	if cache == nil {
		return self, scanned
	}

	switch kind {
	case "ReplicaSet":
		// Until the informer has synced, a miss isn't a deletion.
		if cache.ReplicaSets() == nil || !cache.IsKindReady("replicasets") {
			return self, scanned
		}
		rs, err := cache.ReplicaSets().ReplicaSets(ns).Get(name)
		if err != nil || !sameObject(uid, rs.UID) {
			scanned.Missing = true
			return nil, scanned
		}
		replicas := int32(1)
		if rs.Spec.Replicas != nil {
			replicas = *rs.Spec.Replicas
		}
		scanned.Replicas = &replicas
		if parent := controllerRef(rs.OwnerReferences); parent != nil {
			return resourcecontext.ManagedByFromOwner(parent.Kind, schema.FromAPIVersionAndKind(parent.APIVersion, parent.Kind).Group, ns, parent.Name), scanned
		}
		return self, scanned
	case "Pod":
		if cache.Pods() == nil || !cache.IsKindReady("pods") {
			return self, scanned
		}
		pod, err := cache.Pods().Pods(ns).Get(name)
		if err != nil || !sameObject(uid, pod.UID) {
			scanned.Missing = true
			return nil, scanned
		}
		if w, ok := k8s.PodWorkload(cache, pod); ok {
			return resourcecontext.ManagedByFromOwner(w.Kind, w.Group, ns, w.Name), scanned
		}
		return nil, scanned
	}

	cachedUID, exists, known := workloadUID(cache, kind, ns, name)
	if known && (!exists || !sameObject(uid, cachedUID)) {
		scanned.Missing = true
		return nil, scanned
	}
	return self, scanned
}

// sameObject reports whether a cached object is the one a report's owner
// reference names. A report's owner can be deleted and recreated under the
// same name (a ReplicaSet's name comes from its pod-template hash), and the
// replacement is not what was scanned. Label-only references carry no UID.
func sameObject(ref, cached types.UID) bool {
	return ref == "" || ref == cached
}

// workloadKinds are the scanned kinds that are themselves the workload.
// ReplicaSet and Pod are resolved to theirs.
var workloadKinds = map[string]bool{"ReplicaSet": true, "Deployment": true, "StatefulSet": true, "DaemonSet": true, "Job": true, "CronJob": true, "ReplicationController": true}

// workloadResources maps the workload kinds workloadUID reads to their
// cache resource names.
var workloadResources = map[string]string{"Deployment": "deployments", "StatefulSet": "statefulsets", "DaemonSet": "daemonsets", "Job": "jobs", "CronJob": "cronjobs"}

// workloadUID returns a cached workload's UID; known is false for kinds this
// lookup doesn't cover or whose informer isn't running or hasn't synced.
func workloadUID(cache *k8s.ResourceCache, kind, ns, name string) (uid types.UID, exists, known bool) {
	if resource := workloadResources[kind]; resource == "" || !cache.IsKindReady(resource) {
		return "", false, false
	}
	var obj metav1.Object
	var err error
	switch kind {
	case "Deployment":
		if cache.Deployments() == nil {
			return "", false, false
		}
		obj, err = cache.Deployments().Deployments(ns).Get(name)
	case "StatefulSet":
		if cache.StatefulSets() == nil {
			return "", false, false
		}
		obj, err = cache.StatefulSets().StatefulSets(ns).Get(name)
	case "DaemonSet":
		if cache.DaemonSets() == nil {
			return "", false, false
		}
		obj, err = cache.DaemonSets().DaemonSets(ns).Get(name)
	case "Job":
		if cache.Jobs() == nil {
			return "", false, false
		}
		obj, err = cache.Jobs().Jobs(ns).Get(name)
	case "CronJob":
		if cache.CronJobs() == nil {
			return "", false, false
		}
		obj, err = cache.CronJobs().CronJobs(ns).Get(name)
	default:
		return "", false, false
	}
	if err != nil {
		return "", false, true
	}
	return obj.GetUID(), true, true
}

func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}
