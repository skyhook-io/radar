package server

import (
	"cmp"
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/images"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// handleJava4Shell reads only Pods the caller may list and uses Node facts
// only with list-nodes access. Registry credentials follow image inspection's
// Pod, Secret and exec permissions. Shared unauthenticated listeners can use
// Pod pull secrets held by Radar's Kubernetes identity. The API's same-origin
// guard applies, and callers never supply an image reference or credential.
// A workload scope additionally requires get access to the workload kind and
// uses only Pods owned by that controller, before any registry reads.
func (s *Server) handleJava4Shell(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	const resource = "pods"
	query := r.URL.Query()
	requested := parseNamespaces(query)
	var workload *resourceid.Ref
	if kind, ns, name, group := query.Get("workloadKind"), query.Get("workloadNamespace"), query.Get("workloadName"), query.Get("workloadGroup"); kind != "" || ns != "" || name != "" || group != "" {
		kind = k8s.CanonicalWorkloadKind(kind)
		if ns == "" || name == "" || (group != "" && group != "apps") ||
			(kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet") {
			s.writeError(w, http.StatusBadRequest, "Specify a Deployment, StatefulSet or DaemonSet with its namespace and name")
			return
		}
		if len(requested) > 0 && !slices.Contains(requested, ns) {
			s.writeError(w, http.StatusBadRequest, "The workload is outside the requested namespaces")
			return
		}
		ref := resourceid.NewRef("apps", kind, ns, name)
		workload = &ref
		requested = []string{ns}
	}
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
	target, scopeFailure := s.namespaceListTarget(r, cache, requested)
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
	var candidates []*corev1.Pod
	if workload != nil {
		workloadResource := workloadReadTargets[strings.ToLower(workload.Kind)]
		if allowed, checked := s.canReadDecision(r, workloadResource.group, workloadResource.resource, workload.Namespace, "get"); !allowed {
			s.writeReadFailure(w, resource, permissionFailure(checked, "Your access does not allow reading this workload"), &target)
			return
		}
		candidates, err = k8s.WorkloadPods(r.Context(), cache, workload.Kind, workload.Namespace, workload.Name)
		if err != nil {
			switch {
			case errors.Is(err, k8s.ErrWorkloadNotFound):
				err = readFailuref(readreason.ObjectNotFound, "Workload no longer exists")
			case errors.Is(err, k8s.ErrWorkloadCacheWarming):
				err = readFailuref(readreason.KindSyncPending, "Workload ownership is still loading. Try again shortly")
			case errors.Is(err, k8s.ErrWorkloadAccessDenied):
				err = readFailuref(readreason.RadarForbidden, "Radar cannot read the resources that establish workload ownership")
			default:
				log.Printf("[java4shell] Failed to read Pods for %s: %v", workload, err)
				err = readFailuref(readreason.ReadError, "Radar could not read this workload's Pods")
			}
			s.writeReadFailure(w, resource, err, &target)
			return
		}
	} else {
		objects, ok := cache.ReadableObjects(resource, plan.read)
		if !ok {
			s.writeReadFailure(w, resource, readFailuref(readreason.KindSyncPending, "Pods are still loading. Try again shortly"), &target)
			return
		}
		candidates = make([]*corev1.Pod, 0, len(objects))
		for _, obj := range objects {
			candidates = append(candidates, obj.(*corev1.Pod))
		}
	}
	candidates = slices.DeleteFunc(candidates, func(pod *corev1.Pod) bool {
		return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
	})
	if !s.checkResourceListSize(w, len(candidates), resourceListHardLimit, target, plan.gaps) {
		return
	}

	nodesReadable := s.canRead(r, "", "nodes", "", "list")
	pods := make([]images.Java4ShellPod, 0, len(candidates))
	for _, pod := range candidates {
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
	// Leave most of the HTTP request budget for the registry. When a permission
	// review is slow, the remaining unauthenticated lookups still make progress.
	accessCtx, cancelAccess := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancelAccess()
	accessRequest := r.WithContext(accessCtx)
	result, err := images.DefaultRuntimeInventory(k8s.IsInCluster()).CheckJava4Shell(r.Context(), pods, func(ctx context.Context, pod *corev1.Pod) resourceid.Ref {
		if workload != nil {
			return *workload
		}
		return s.java4ShellWorkload(r.WithContext(ctx), cache, pod)
	}, func(pod *corev1.Pod) images.Java4ShellRegistryAccess {
		if accessCtx.Err() != nil || !current() {
			return images.Java4ShellRegistryAccess{}
		}
		access := s.java4ShellRegistryAccess(accessRequest, cache, pod)
		if !current() {
			return images.Java4ShellRegistryAccess{}
		}
		return access
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
		Workload *resourceid.Ref      `json:"workload,omitempty"`
	}{Java4ShellResult: result, Coverage: target.coverage(plan.read, plan.gaps), Workload: workload})
}

func (s *Server) java4ShellRegistryAccess(r *http.Request, cache *k8s.ResourceCache, pod *corev1.Pod) images.Java4ShellRegistryAccess {
	access := images.Java4ShellRegistryAccess{}
	user := auth.UserFromContext(r.Context())
	switch {
	case user == nil:
		if s.authConfig.Enabled() {
			return access
		}
		access.RadarCredentials = !k8s.IsInCluster() && !s.sharedListener()
	default:
		if allowed, checked := s.imagePodReadDecision(r, pod.Namespace); !allowed || !checked {
			return access
		}
		allowed, checked := s.imageExecDecision(r, pod.Namespace)
		access.RadarCredentials = allowed && checked
	}
	for _, ref := range pod.Spec.ImagePullSecrets {
		if ref.Name == "" {
			continue
		}
		obj, found, _ := cache.ReadableObject("secrets", pod.Namespace, ref.Name)
		if !found {
			continue
		}
		secret := obj.(*corev1.Secret)
		if secret.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}
		if user != nil && !access.RadarCredentials {
			if allowed, checked := s.imagePullSecretDecision(r, pod.Namespace, ref.Name); !allowed || !checked {
				continue
			}
		}
		access.PullSecrets = append(access.PullSecrets, secret)
	}
	return access
}

func (s *Server) java4ShellWorkload(r *http.Request, cache *k8s.ResourceCache, pod *corev1.Pod) resourceid.Ref {
	fallback := resourceid.NewRef("", "Pod", pod.Namespace, pod.Name)
	allowed := func(kind, group string) bool {
		resolvedGroup, resource, clusterScoped, ok := k8s.ResolveChangeGVR(kind, group)
		return ok && !clusterScoped && resolvedGroup == group && r.Context().Err() == nil &&
			s.canRead(r, resolvedGroup, resource, pod.Namespace, "list")
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
