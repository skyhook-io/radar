package server

import (
	"cmp"
	"errors"
	"net/http"
	"slices"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// handleJava4Shell reads only Pods the caller may list and uses Node facts
// only with list-nodes access. The same bounded, anonymous registry read is
// available on shared listeners. No registry or credential is supplied by
// the caller, and the route is protected by the API's same-origin guard.
func (s *Server) handleJava4Shell(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	const resource = "pods"
	cache := readCache()
	_, generation := k8s.GetClientWithGeneration()
	current := func() bool {
		_, now := k8s.GetClientWithGeneration()
		return generation == now && readCache() == cache
	}
	if auth.UserFromContext(r.Context()) != nil && s.permCache == nil {
		s.writeCollectionFailure(w, resource, permissionFailure(false, ""))
		return
	}
	target, scopeFailure := s.namespaceListTarget(r, cache, parseNamespaces(r.URL.Query()))
	if !current() {
		s.writeCollectionFailure(w, resource, errReadHandlesChanged)
		return
	}
	if scopeFailure != "" || (target.namespaces != nil && len(target.namespaces) == 0) {
		failure := noAccessFailure(readFailuref(readreason.UserNoNamespaceAccess, "No namespaces to check"), target, scopeFailure == readreason.UserPermissionCheckFailed)
		s.writeReadFailure(w, resource, failure, &target)
		return
	}

	namespaces := target.namespaces
	if namespaces == nil {
		if allowed, checked := s.canReadDecision(r, "", resource, "", "list"); !allowed {
			s.writeReadFailure(w, resource, permissionFailure(checked, "Your access does not allow listing Pods across namespaces"), &target)
			return
		}
	} else {
		allowed, denied, unchecked := s.partitionNamespacesByCanRead(r, "", resource, "list", namespaces)
		if len(denied) > 0 {
			target = target.withGaps([]resourceapi.Gap{gapOf(readreason.UserForbidden, denied)})
		}
		if len(unchecked) > 0 {
			target = target.withGaps([]resourceapi.Gap{gapOf(readreason.UserPermissionCheckFailed, unchecked)})
		}
		if len(allowed) == 0 {
			s.writeReadFailure(w, resource, permissionFailure(len(unchecked) == 0, "Your access does not allow listing Pods in this scope"), &target)
			return
		}
		namespaces = allowed
	}
	plan, err := typedReadCoverage(cache, resource, namespaces, false, target.explicit)
	if len(plan.collected) > 0 {
		target = target.narrowedByCollection(plan.collected, plan.uncovered)
	}
	if err != nil {
		s.writeReadFailure(w, resource, err, &target)
		return
	}
	objects, ok := cache.ReadableObjects(resource, plan.read)
	if !ok {
		s.writeReadFailure(w, resource, readFailuref(readreason.KindSyncPending, "Pods are still loading. Try again shortly"), &target)
		return
	}
	objects = slices.DeleteFunc(objects, func(obj runtime.Object) bool {
		phase := obj.(*corev1.Pod).Status.Phase
		return phase == corev1.PodSucceeded || phase == corev1.PodFailed
	})
	if !s.checkResourceListSize(w, len(objects), resourceListHardLimit, target, plan.gaps) {
		return
	}

	nodesReadable := s.canRead(r, "", "nodes", "", "list")
	pods := make([]images.Java4ShellPod, 0, len(objects))
	for _, obj := range objects {
		pod := obj.(*corev1.Pod)
		var node *corev1.Node
		if nodesReadable && pod.Spec.NodeName != "" {
			obj, found, _ := cache.ReadableObject("nodes", "", pod.Spec.NodeName)
			if found {
				node = obj.(*corev1.Node)
			}
		}
		pods = append(pods, images.Java4ShellPod{Pod: pod, Node: node})
	}
	slices.SortFunc(pods, func(a, b images.Java4ShellPod) int {
		return cmp.Or(cmp.Compare(a.Pod.Namespace, b.Pod.Namespace), cmp.Compare(a.Pod.Name, b.Pod.Name))
	})
	if !current() {
		s.writeReadFailure(w, resource, errReadHandlesChanged, &target)
		return
	}
	result, err := images.DefaultRuntimeInventory(k8s.IsInCluster()).CheckJava4Shell(r.Context(), pods, func(pod *corev1.Pod) resourceid.Ref {
		return s.java4ShellWorkload(r, cache, pod)
	})
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, images.ErrJava4ShellBusy) {
		s.writeError(w, http.StatusConflict, "A Java4Shell check is already running. Try again shortly.")
		return
	}
	if !current() {
		s.writeReadFailure(w, resource, errReadHandlesChanged, &target)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	s.writeJSON(w, struct {
		images.Java4ShellResult
		Coverage resourceapi.Coverage `json:"coverage"`
	}{Java4ShellResult: result, Coverage: target.coverage(plan.read, plan.gaps)})
}

func (s *Server) java4ShellWorkload(r *http.Request, cache *k8s.ResourceCache, pod *corev1.Pod) resourceid.Ref {
	fallback := resourceid.NewRef("", "Pod", pod.Namespace, pod.Name)
	allowed := func(kind, group string) bool {
		resolvedGroup, resource, clusterScoped, ok := k8s.ResolveChangeGVR(kind, group)
		return ok && !clusterScoped && resolvedGroup == group && s.canRead(r, resolvedGroup, resource, pod.Namespace, "list")
	}
	owner := metav1.GetControllerOfNoCopy(pod)
	if owner == nil || !allowed(owner.Kind, resourceid.GroupFromAPIVersion(owner.APIVersion)) {
		return fallback
	}
	if workload, ok := k8s.PodWorkload(cache, pod); ok && allowed(workload.Kind, workload.Group) {
		return resourceid.NewRef(workload.Group, workload.Kind, pod.Namespace, workload.Name)
	}
	return fallback
}
