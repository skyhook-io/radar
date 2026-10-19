package mcp

import (
	"context"
	"errors"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type waitBlockerKey struct {
	uid               types.UID
	container, reason string
}
type waitBlocker struct {
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	key       waitBlockerKey
}
type waitBlockerResult struct {
	blockers    []waitBlocker
	unavailable string
}

func waitBlockerPermission(ctx context.Context, group, resource, ns, verb, denial string) string {
	allowed, determined := canReadInNamespaceDecision(ctx, group, resource, ns, verb)
	if !determined {
		return "permission check failed"
	}
	if !allowed {
		return denial
	}
	return ""
}

func readWaitBlockers(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) waitBlockerResult {
	ns := obj.GetNamespace()
	unavailable := func(reason string) waitBlockerResult { return waitBlockerResult{unavailable: reason} }
	if !namespaceWithinPin(ns) {
		return unavailable("outside Radar's namespace scope")
	}
	if !checkNamespaceAccess(ctx, ns) {
		return unavailable("pods are not readable with your permissions")
	}
	if reason := waitBlockerPermission(ctx, "", "pods", ns, "list", "pods are not readable with your permissions"); reason != "" {
		return unavailable(reason)
	}
	if isWaitController(gvr) && gvr.Group != "argoproj.io" {
		gen, present := waitGeneration(obj)
		if !present || gen < obj.GetGeneration() {
			return waitBlockerResult{}
		}
	}
	dependencyDenied := "workload's ReplicaSets or ControllerRevisions are not readable"
	needsRS := gvr.Group == "apps" && gvr.Resource == "deployments" || gvr.Group == "argoproj.io" && gvr.Resource == "rollouts"
	if needsRS {
		if reason := waitBlockerPermission(ctx, "apps", "replicasets", ns, "get", dependencyDenied); reason != "" {
			return unavailable(reason)
		}
	}
	if gvr.Group == "apps" && gvr.Resource == "deployments" {
		if reason := waitBlockerPermission(ctx, "apps", "replicasets", ns, "list", dependencyDenied); reason != "" {
			return unavailable(reason)
		}
	}
	if gvr.Group == "apps" && gvr.Resource == "daemonsets" {
		if reason := waitBlockerPermission(ctx, "apps", "controllerrevisions", ns, "list", dependencyDenied); reason != "" {
			return unavailable(reason)
		}
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.ResourceCache == nil {
		return unavailable("pod cache is still loading")
	}
	if cache.Pods() == nil {
		return unavailable("pods are not readable with your permissions")
	}
	if synced, known := cache.InformerSynced("pods"); known && !synced {
		return unavailable("pod cache is still loading")
	}
	if !cache.KindCoversNamespace("pods", ns) {
		return unavailable("pods are not readable with your permissions")
	}
	var pods []*corev1.Pod
	if gvr.Group == "" && gvr.Resource == "pods" {
		pod, err := cache.Pods().Pods(ns).Get(obj.GetName())
		if err != nil {
			return unavailable("pod cache is still loading")
		}
		if pod.UID == obj.GetUID() {
			pods = []*corev1.Pod{pod}
		}
	} else {
		if needsRS {
			if cache.ReplicaSets() == nil || !cache.KindCoversNamespace("replicasets", ns) {
				return unavailable(dependencyDenied)
			}
			if synced, known := cache.InformerSynced("replicasets"); known && !synced {
				return unavailable(dependencyDenied)
			}
		}
		var err error
		pods, err = k8s.WorkloadPodsForUID(cache, obj.GetKind(), ns, obj.GetName(), gvr.Group, obj.GetUID())
		if err != nil {
			if errors.Is(err, k8s.ErrWorkloadCacheWarming) {
				return unavailable("pod cache is still loading")
			}
			return unavailable(dependencyDenied)
		}
	}
	var revision string
	var rsUID types.UID
	switch gvr.GroupResource() {
	case schema.GroupResource{Group: "apps", Resource: "deployments"}:
		sets, err := cache.ReplicaSets().ReplicaSets(ns).List(labels.Everything())
		if err != nil {
			return unavailable(dependencyDenied)
		}
		revision, rsUID = currentWaitReplicaSet(obj, sets)
	case schema.GroupResource{Group: "apps", Resource: "statefulsets"}:
		revision, _, _ = unstructured.NestedString(obj.Object, "status", "updateRevision")
	case schema.GroupResource{Group: "argoproj.io", Resource: "rollouts"}:
		revision, _, _ = unstructured.NestedString(obj.Object, "status", "currentPodHash")
	case schema.GroupResource{Group: "apps", Resource: "daemonsets"}:
		// ControllerRevisions aren't warmed in the cache, so list the DaemonSet's
		// through the caller's own client, as workload logs do.
		raw, _, _ := unstructured.NestedMap(obj.Object, "spec", "selector")
		var labelSelector metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &labelSelector); err != nil {
			return unavailable(dependencyDenied)
		}
		selector, err := metav1.LabelSelectorAsSelector(&labelSelector)
		if err != nil {
			return unavailable(dependencyDenied)
		}
		revisionGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
		revisions, err := dyn.Resource(revisionGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return unavailable(dependencyDenied)
		}
		revision = k8score.CurrentControllerRevisionPodHashes(revisions.Items)[obj.GetUID()]
	}
	return waitBlockerResult{blockers: waitPodBlockers(gvr, pods, revision, rsUID)}
}

func isWaitController(gvr schema.GroupVersionResource) bool {
	return defaultWaitCondition(gvr) == "rollout"
}

func currentWaitReplicaSet(obj *unstructured.Unstructured, sets []*appsv1.ReplicaSet) (string, types.UID) {
	revision := obj.GetAnnotations()["deployment.kubernetes.io/revision"]
	if revision == "" {
		return "", ""
	}
	for _, rs := range sets {
		owner := metav1.GetControllerOf(rs)
		if owner != nil && owner.UID == obj.GetUID() && owner.Kind == "Deployment" && rs.Annotations["deployment.kubernetes.io/revision"] == revision {
			return rs.Labels["pod-template-hash"], rs.UID
		}
	}
	return "", ""
}

func waitPodBlockers(gvr schema.GroupVersionResource, pods []*corev1.Pod, revision string, rsUID types.UID) []waitBlocker {
	var out []waitBlocker
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || waitPodReady(pod) {
			continue
		}
		switch gvr.GroupResource() {
		case schema.GroupResource{Group: "apps", Resource: "deployments"}:
			owner := metav1.GetControllerOf(pod)
			if revision == "" || owner == nil || owner.UID != rsUID || pod.Labels["pod-template-hash"] != revision {
				continue
			}
		case schema.GroupResource{Group: "apps", Resource: "statefulsets"}, schema.GroupResource{Group: "apps", Resource: "daemonsets"}:
			if revision == "" || pod.Labels["controller-revision-hash"] != revision {
				continue
			}
		case schema.GroupResource{Group: "argoproj.io", Resource: "rollouts"}:
			if revision == "" || pod.Labels["rollouts-pod-template-hash"] != revision {
				continue
			}
		}
		container, reason, message := health.PodBlockingReason(pod)
		if reason == "" && health.IsPodUnschedulable(pod) {
			reason = "Unschedulable"
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodScheduled {
					message = c.Message
					break
				}
			}
		}
		if reason != "" {
			out = append(out, waitBlocker{Pod: pod.Name, Container: container, Reason: reason, Message: truncateCrashLogLine(message, 300), key: waitBlockerKey{pod.UID, container, waitBlockerFamily(reason)}})
		}
	}
	return out
}

// waitBlockerFamily groups reasons a kubelet alternates between for one
// problem, so the persistence clock doesn't restart on every image pull retry.
func waitBlockerFamily(reason string) string {
	if reason == "ErrImagePull" || reason == "ImagePullBackOff" {
		return "ImagePull"
	}
	return reason
}

func waitPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
