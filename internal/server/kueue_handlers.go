package server

import (
	"context"
	"log"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

const kueueGroup = "kueue.x-k8s.io"
const maxKueueAdmissionWorkloads = 8

type KueueAdmissionResponse struct {
	Installed bool                     `json:"installed"`
	Workloads []KueueAdmissionWorkload `json:"workloads"`
	Total     int                      `json:"total"`
	Truncated bool                     `json:"truncated"`
}

type KueueAdmissionWorkload struct {
	APIVersion string                             `json:"apiVersion"`
	Namespace  string                             `json:"namespace"`
	Name       string                             `json:"name"`
	UID        string                             `json:"uid"`
	Generation int64                              `json:"generation"`
	CreatedAt  metav1.Time                        `json:"createdAt"`
	Deleting   bool                               `json:"deleting"`
	Ref        *resourcecontext.ContextRef        `json:"ref,omitempty"`
	Projection string                             `json:"projection"` // available, unsupported, forbidden
	Scheduling *resourcecontext.SchedulingSummary `json:"scheduling,omitempty"`
	Omitted    []resourcecontext.OmittedField     `json:"omitted,omitempty"`
}

func (s *Server) handleKueueAdmission(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind, namespace, name := chi.URLParam(r, "kind"), chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if kind != "jobsets" || r.URL.Query().Get("group") != "jobset.x-k8s.io" || namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "Kueue admission lookup supports jobsets in jobset.x-k8s.io")
		return
	}
	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) || !s.canRead(r, "jobset.x-k8s.io", "jobsets", namespace, "get") {
		s.writeError(w, http.StatusForbidden, "no access to this JobSet")
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	root, err := cache.GetDynamicWithGroup(r.Context(), "JobSet", namespace, name, "jobset.x-k8s.io")
	if err != nil {
		if apierrors.IsNotFound(err) {
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
	discovery, dynamic := k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
	if discovery == nil || dynamic == nil || dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete {
		s.writeError(w, http.StatusServiceUnavailable, "Kueue discovery is not ready; retry shortly")
		return
	}
	if _, found := discovery.GetGVRWithGroup("Workload", kueueGroup); !found {
		s.writeJSON(w, KueueAdmissionResponse{Workloads: []KueueAdmissionWorkload{}})
		return
	}
	if !s.canRead(r, kueueGroup, "workloads", namespace, "list") {
		s.writeError(w, http.StatusForbidden, "no access to list Kueue Workloads in this namespace")
		return
	}
	items, err := listDynamicSynced(r.Context(), cache, "Workload", kueueGroup, namespace)
	if err != nil {
		log.Printf("[kueue] Failed to list Workloads for JobSet %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusServiceUnavailable, "Radar could not observe Kueue Workloads; retry when its cache is available")
		return
	}
	s.writeJSON(w, kueueAdmissionForJobSet(r.Context(), root, items, kueueReferenceChecker{s.newRequestScopedChecker(r)}))
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

func kueueAdmissionForJobSet(ctx context.Context, root *unstructured.Unstructured, items []*unstructured.Unstructured, checker resourcecontext.RefAccessChecker) KueueAdmissionResponse {
	matches := make([]*unstructured.Unstructured, 0)
	for _, item := range items {
		if item != nil && item.GroupVersionKind().Group == kueueGroup && item.GetKind() == "Workload" && jobSetControls(root, item) {
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
	response := KueueAdmissionResponse{Installed: true, Workloads: make([]KueueAdmissionWorkload, 0), Total: len(matches), Truncated: len(matches) > maxKueueAdmissionWorkloads}
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
			entry.Scheduling, entry.Omitted = resourcecontext.FilterSchedulingSummary(ctx, summary, checker)
			entry.Projection = "available"
			if entry.Scheduling == nil {
				entry.Projection = "forbidden"
			}
		}
		response.Workloads = append(response.Workloads, entry)
	}
	return response
}
