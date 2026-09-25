package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func (s *Server) handleRayClusterPods(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	q := r.URL.Query()
	uid, nodeType, group := q.Get("ownerUID"), q.Get("nodeType"), q.Get("workerGroup")
	if uid == "" || (nodeType != "" && nodeType != "head" && nodeType != "worker") || (group != "" && nodeType == "head") {
		s.writeError(w, http.StatusBadRequest, "ownerUID is required; nodeType must be head or worker, and workerGroup cannot select head Pods")
		return
	}
	limit, err := parseCapacityLimit(q, maxWorkloadPodResponseLimit, maxWorkloadPodResponseLimit)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{ns})) || !s.canRead(r, "ray.io", "rayclusters", ns, "get") || !s.canRead(r, "", "pods", ns, "list") {
		s.writeError(w, http.StatusForbidden, "Reading RayCluster Pods requires get rayclusters and list pods in this namespace")
		return
	}
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Cluster connection unavailable")
		return
	}
	root, err := client.Resource(schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayclusters"}).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		failure := workloadSelectorGetError(err)
		if failure.statusCode == http.StatusInternalServerError {
			log.Printf("[raycluster] Failed to read %s/%s: %v", ns, name, err)
		}
		s.writeWorkloadError(w, failure)
		return
	}
	if root.GetAPIVersion() != "ray.io/v1" || root.GetKind() != "RayCluster" {
		s.writeError(w, http.StatusBadRequest, "Expected ray.io/v1 RayCluster")
		return
	}
	if string(root.GetUID()) != uid {
		s.writeError(w, http.StatusConflict, "RayCluster was recreated; refresh the resource to inspect its current Pods")
		return
	}
	pods, err := k8s.DirectlyOwnedPods(k8s.GetResourceCache(), ns, name, "ray.io", "RayCluster", root.GetUID())
	if err != nil {
		failure := workloadSelectorGetError(err)
		if errors.Is(err, k8s.ErrWorkloadCacheWarming) {
			failure.statusCode = http.StatusServiceUnavailable
		}
		if failure.statusCode == http.StatusInternalServerError {
			log.Printf("[raycluster] Failed to list Pods for %s/%s: %v", ns, name, err)
		}
		s.writeWorkloadError(w, failure)
		return
	}
	pods = filterRayClusterPods(pods, nodeType, group)
	infos, truncated := limitWorkloadPodInfos(buildPodInfos(pods), limit)
	s.writeJSON(w, map[string]any{"pods": infos, "total": len(pods), "truncated": truncated})
}

func filterRayClusterPods(pods []*corev1.Pod, nodeType, group string) []*corev1.Pod {
	result := []*corev1.Pod{}
	if group != "" {
		nodeType = "worker"
	}
	for _, pod := range pods {
		if nodeType != "" && pod.Labels["ray.io/node-type"] != nodeType {
			continue
		}
		if group != "" && pod.Labels["ray.io/group"] != group {
			continue
		}
		result = append(result, pod)
	}
	return result
}
