package server

import (
	"context"
	"log"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

const kueueGroup = "kueue.x-k8s.io"
const maxKueueAdmissionWorkloads = 8

type KueueAdmissionResponse struct {
	UID       string                   `json:"uid"`
	Installed bool                     `json:"installed"`
	Workloads []KueueAdmissionWorkload `json:"workloads"`
	Total     int                      `json:"total"`
	Truncated bool                     `json:"truncated"`
}

type KueueAdmissionWorkload struct {
	APIVersion   string                             `json:"apiVersion"`
	Namespace    string                             `json:"namespace"`
	Name         string                             `json:"name"`
	UID          string                             `json:"uid"`
	Generation   int64                              `json:"generation"`
	CreatedAt    metav1.Time                        `json:"createdAt"`
	Deleting     bool                               `json:"deleting"`
	Ref          *resourcecontext.ContextRef        `json:"ref,omitempty"`
	Projection   string                             `json:"projection"` // available, unsupported, forbidden
	Scheduling   *resourcecontext.SchedulingSummary `json:"scheduling,omitempty"`
	LinksLimited bool                               `json:"linksLimited"`
}

func (s *Server) handleKueueAdmission(w http.ResponseWriter, r *http.Request) {
	operation := k8s.OperationContext()
	if !s.requireConnected(w) {
		return
	}
	kind, namespace, name := chi.URLParam(r, "kind"), chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	isJob := kind == "jobs" && group == "batch"
	if (!isJob && (kind != "jobsets" || group != "jobset.x-k8s.io")) || namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "Kueue admission lookup supports batch Jobs and jobset.x-k8s.io JobSets")
		return
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) || !s.canRead(r, group, kind, namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to this workload")
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	discovery, dynamic := k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
	if discovery == nil || dynamic == nil || dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete {
		s.writeError(w, http.StatusServiceUnavailable, "Admission discovery is not ready; retry shortly")
		return
	}
	var root *unstructured.Unstructured
	var err error
	if isJob {
		if cache.Jobs() == nil || !cache.KindCoversNamespace("jobs", namespace) {
			s.writeError(w, http.StatusForbidden, "Radar cannot observe Jobs in this namespace")
			return
		}
		if synced, known := cache.InformerSynced("jobs"); known && !synced {
			s.writeError(w, http.StatusServiceUnavailable, "Job cache is still syncing; retry shortly")
			return
		}
		job, getErr := cache.Jobs().Jobs(namespace).Get(name)
		if getErr != nil {
			s.writeWorkloadError(w, workloadParentGetError("Job", namespace, name, getErr))
			return
		}
		object, convertErr := runtime.DefaultUnstructuredConverter.ToUnstructured(job)
		if convertErr != nil {
			log.Printf("[kueue] Failed to convert Job %s/%s: %v", namespace, name, convertErr)
			s.writeError(w, http.StatusInternalServerError, "Failed to read Job")
			return
		}
		root = &unstructured.Unstructured{Object: object}
		root.SetAPIVersion("batch/v1")
		root.SetKind("Job")
	} else {
		rootGVR, found := discovery.GetGVRWithGroup("JobSet", "jobset.x-k8s.io")
		if !found {
			if discovery.GroupHadPartialDiscovery("jobset.x-k8s.io") {
				s.writeError(w, http.StatusServiceUnavailable, "JobSet discovery is incomplete; retry shortly")
			} else {
				s.writeError(w, http.StatusNotFound, "JobSets are not served by this cluster")
			}
			return
		}
		root, err = cache.GetDynamicWithGroup(r.Context(), "JobSet", namespace, name, "jobset.x-k8s.io")
		if err != nil {
			if workloadParentGetError("JobSet", namespace, name, err).statusCode == http.StatusNotFound && dynamic.IsNamespaceSynced(rootGVR, namespace) {
				s.writeError(w, http.StatusNotFound, "JobSet not found")
			} else {
				log.Printf("[kueue] Failed to read JobSet %s/%s: %v", namespace, name, err)
				s.writeError(w, http.StatusServiceUnavailable, "Radar could not observe the JobSet; retry when its cache is available")
			}
			return
		}
		if !isSupportedJobSet(root) {
			s.writeError(w, http.StatusBadRequest, "Kueue admission lookup supports JobSet v1alpha2")
			return
		}
	}
	unchanged := func() bool {
		return operation.Err() == nil && cache == k8s.GetResourceCache() && dynamic == k8s.GetDynamicResourceCache() && discovery == k8s.GetResourceDiscovery()
	}
	respond := func(response KueueAdmissionResponse) {
		if !unchanged() {
			s.writeError(w, http.StatusServiceUnavailable, "Cluster connection changed; retry admission lookup")
			return
		}
		s.writeJSON(w, response)
	}
	if _, found := discovery.GetGVRWithGroup("Workload", kueueGroup); !found {
		if discovery.GroupHadPartialDiscovery(kueueGroup) {
			s.writeError(w, http.StatusServiceUnavailable, "Kueue discovery is incomplete; retry shortly")
			return
		}
		respond(KueueAdmissionResponse{UID: string(root.GetUID()), Workloads: []KueueAdmissionWorkload{}})
		return
	}
	if !s.canRead(r, kueueGroup, "workloads", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to list Kueue Workloads in this namespace")
		return
	}
	items, err := listDynamicSynced(r.Context(), cache, "Workload", kueueGroup, namespace)
	if err != nil {
		log.Printf("[kueue] Failed to list Workloads for root %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "Radar could not observe Kueue Workloads; retry when its cache is available")
		return
	}
	respond(kueueAdmissionForRoot(r.Context(), root, items, kueueReferenceChecker{s.newRequestScopedChecker(r)}))
}

type kueueReferenceChecker struct{ checker *requestScopedChecker }

func (c kueueReferenceChecker) CanRead(ctx context.Context, group, kind, namespace string) bool {
	// A Workload can reference optional Kueue CRDs that this cluster does not serve.
	// Unlike the topology caller, this projection cannot assume every ref is known.
	if _, resource := k8s.LookupResourceGVR(kind, group); resource == "" {
		return false
	}
	return c.checker.CanRead(ctx, group, kind, namespace)
}

func admissionRootControls(root *unstructured.Unstructured, child metav1.Object) bool {
	if root == nil || child == nil || root.GetUID() == "" || root.GetNamespace() != child.GetNamespace() {
		return false
	}
	if !isSupportedJobSet(root) && (root.GetAPIVersion() != "batch/v1" || root.GetKind() != "Job") {
		return false
	}
	owner := metav1.GetControllerOf(child)
	return owner != nil && owner.APIVersion == root.GetAPIVersion() && owner.Kind == root.GetKind() && owner.Name == root.GetName() && owner.UID == root.GetUID()
}

func kueueAdmissionForRoot(ctx context.Context, root *unstructured.Unstructured, items []*unstructured.Unstructured, checker resourcecontext.RefAccessChecker) KueueAdmissionResponse {
	matches := make([]*unstructured.Unstructured, 0)
	for _, item := range items {
		if item != nil && item.GroupVersionKind().Group == kueueGroup && item.GetKind() == "Workload" && admissionRootControls(root, item) {
			matches = append(matches, item)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		at, bt := a.GetCreationTimestamp(), b.GetCreationTimestamp()
		if !at.Equal(&bt) {
			return at.After(bt.Time)
		}
		if a.GetName() != b.GetName() {
			return a.GetName() < b.GetName()
		}
		return a.GetUID() < b.GetUID()
	})
	response := KueueAdmissionResponse{UID: string(root.GetUID()), Installed: true, Workloads: make([]KueueAdmissionWorkload, 0), Total: len(matches), Truncated: len(matches) > maxKueueAdmissionWorkloads}
	if response.Truncated {
		matches = matches[:maxKueueAdmissionWorkloads]
	}
	for _, item := range matches {
		entry := KueueAdmissionWorkload{
			APIVersion: item.GetAPIVersion(), Namespace: item.GetNamespace(), Name: item.GetName(),
			UID: string(item.GetUID()), Generation: item.GetGeneration(), CreatedAt: item.GetCreationTimestamp(),
			Deleting: item.GetDeletionTimestamp() != nil, Projection: "unsupported",
		}
		if checker == nil || checker.CanRead(ctx, kueueGroup, "Workload", item.GetNamespace()) {
			entry.Ref = &resourcecontext.ContextRef{Group: kueueGroup, Kind: "Workload", Namespace: item.GetNamespace(), Name: item.GetName()}
		}
		if summary := schedulinginsight.ForResource(item, resourcecontext.TierDiagnostic); summary != nil {
			filtered, omitted := resourcecontext.FilterSchedulingSummary(ctx, summary, checker)
			entry.Scheduling, entry.LinksLimited = filtered, filtered != nil && len(omitted) > 0
			entry.Projection = "available"
			if entry.Scheduling == nil {
				entry.Projection = "forbidden"
			}
		}
		response.Workloads = append(response.Workloads, entry)
	}
	return response
}

const provisioningGroup = "autoscaling.x-k8s.io"
const maxProvisioningRequests = 20

type KueueProvisioningResponse struct {
	UID       string                       `json:"uid"`
	Installed bool                         `json:"installed"`
	Requests  []*unstructured.Unstructured `json:"requests"`
	Total     int                          `json:"total"`
	Truncated bool                         `json:"truncated"`
}

func (s *Server) handleKueueProvisioning(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) || !s.canRead(r, kueueGroup, "workloads", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to this Kueue Workload")
		return
	}
	cache := k8s.GetResourceCache()
	discovery, dynamic := k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
	if cache == nil || discovery == nil || dynamic == nil || dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete {
		s.writeError(w, http.StatusServiceUnavailable, "Provisioning discovery is not ready; retry shortly")
		return
	}
	rootGVR, found := discovery.GetGVRWithGroup("Workload", kueueGroup)
	if !found {
		if discovery.GroupHadPartialDiscovery(kueueGroup) {
			s.writeError(w, http.StatusServiceUnavailable, "Kueue discovery is incomplete; retry shortly")
		} else {
			s.writeError(w, http.StatusNotFound, "Kueue Workloads are not served by this cluster")
		}
		return
	}
	root, err := cache.GetDynamicWithGroup(r.Context(), "Workload", namespace, name, kueueGroup)
	if err != nil {
		if workloadParentGetError("Workload", namespace, name, err).statusCode == http.StatusNotFound && dynamic.IsNamespaceSynced(rootGVR, namespace) {
			s.writeError(w, http.StatusNotFound, "Kueue Workload not found")
		} else {
			log.Printf("[kueue] Failed to read Workload %s/%s: %v", namespace, name, err)
			s.writeError(w, http.StatusServiceUnavailable, "Radar could not observe the Workload; retry when its cache is available")
		}
		return
	}
	if _, found := discovery.GetGVRWithGroup("ProvisioningRequest", provisioningGroup); !found {
		if discovery.GroupHadPartialDiscovery(provisioningGroup) {
			s.writeError(w, http.StatusServiceUnavailable, "Provisioning discovery is incomplete; retry shortly")
		} else {
			s.writeJSON(w, KueueProvisioningResponse{UID: string(root.GetUID()), Requests: []*unstructured.Unstructured{}})
		}
		return
	}
	if !s.canRead(r, provisioningGroup, "provisioningrequests", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to list ProvisioningRequests in this namespace")
		return
	}
	items, err := listDynamicSynced(r.Context(), cache, "ProvisioningRequest", provisioningGroup, namespace)
	if err != nil {
		log.Printf("[kueue] Failed to list ProvisioningRequests for Workload %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "Radar could not observe ProvisioningRequests; retry when its cache is available")
		return
	}
	s.writeJSON(w, provisioningForWorkload(root, items))
}

func provisioningForWorkload(root *unstructured.Unstructured, items []*unstructured.Unstructured) KueueProvisioningResponse {
	matches := make([]*unstructured.Unstructured, 0)
	for _, item := range items {
		if item == nil || root.GetUID() == "" || item.GroupVersionKind().Group != provisioningGroup || item.GetKind() != "ProvisioningRequest" || item.GetNamespace() != root.GetNamespace() {
			continue
		}
		owner := metav1.GetControllerOf(item)
		if owner == nil || resourceid.GroupFromAPIVersion(owner.APIVersion) != kueueGroup || owner.Kind != "Workload" || owner.Name != root.GetName() || owner.UID != root.GetUID() {
			continue
		}
		matches = append(matches, item)
	}
	sort.Slice(matches, func(i, j int) bool {
		at, bt := matches[i].GetCreationTimestamp(), matches[j].GetCreationTimestamp()
		if !at.Equal(&bt) {
			return at.After(bt.Time)
		}
		if matches[i].GetName() != matches[j].GetName() {
			return matches[i].GetName() < matches[j].GetName()
		}
		return matches[i].GetUID() < matches[j].GetUID()
	})
	result := KueueProvisioningResponse{UID: string(root.GetUID()), Installed: true, Requests: make([]*unstructured.Unstructured, 0), Total: len(matches), Truncated: len(matches) > maxProvisioningRequests}
	if result.Truncated {
		matches = matches[:maxProvisioningRequests]
	}
	for _, item := range matches {
		summary := &unstructured.Unstructured{Object: map[string]any{"apiVersion": item.GetAPIVersion(), "kind": item.GetKind()}}
		summary.SetName(item.GetName())
		summary.SetNamespace(item.GetNamespace())
		summary.SetUID(item.GetUID())
		summary.SetGeneration(item.GetGeneration())
		summary.SetCreationTimestamp(item.GetCreationTimestamp())
		summary.SetDeletionTimestamp(item.GetDeletionTimestamp())
		class, _, _ := unstructured.NestedString(item.Object, "spec", "provisioningClassName")
		_ = unstructured.SetNestedField(summary.Object, class, "spec", "provisioningClassName")
		if conditions, found, _ := unstructured.NestedSlice(item.Object, "status", "conditions"); found {
			_ = unstructured.SetNestedSlice(summary.Object, conditions, "status", "conditions")
		}
		result.Requests = append(result.Requests, summary)
	}
	return result
}
