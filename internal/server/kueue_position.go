package server

import (
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/kueuevisibility"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

// KueuePositionResponse answers GET /api/kueue/position/{namespace}/{name}.
// Position is set only when Eligible: Radar's Workload snapshot permits a
// pending-position lookup. Eligibility does not establish queue membership.
type KueuePositionResponse struct {
	UID      string                                `json:"uid"`
	Eligible bool                                  `json:"eligible"`
	Position *resourcecontext.KueuePendingPosition `json:"position,omitempty"`
}

// handleKueuePosition reports where Kueue's visibility API places one
// pending Workload, read as the caller.
//
// The gate is the provisioning sibling's: the caller must see the namespace
// and get the Workload, checked before any live call. The position itself is
// read as the caller (impersonated, or with their token), so the API server
// enforces the visibility API's own RBAC — get on
// localqueues/pendingworkloads in visibility.kueue.x-k8s.io — exactly, and a
// refusal comes back as the answer's status, not as an HTTP error. Without
// authentication the read runs as Radar's identity and never exceeds it.
func (s *Server) handleKueuePosition(w http.ResponseWriter, r *http.Request) {
	operation := k8s.OperationContext()
	binding := kueuevisibility.CurrentBinding()
	if !s.requireConnected(w) {
		return
	}
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	selection, code := s.requestNamespaceSelection(r, k8s.GetResourceCache(), []string{namespace})
	if code != "" {
		s.writeReadFailure(w, "workloads", readFailuref(code, "Radar could not determine your namespace access; retry shortly"), nil)
		return
	}
	if len(selection.Gaps) > 0 {
		s.writeReadFailure(w, "workloads", readFailuref(selection.Gaps[0].Code, "namespace %q is outside this read's scope", namespace), nil)
		return
	}
	allowed, checked := s.canReadDecision(r, kueueGroup, "workloads", namespace, "get")
	if !checked {
		s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.UserPermissionCheckFailed), "Radar could not check your access to this Workload; retry shortly")
		return
	}
	if !allowed {
		s.writeErrorCode(w, http.StatusForbidden, string(readreason.UserForbidden), "no access to this Kueue Workload")
		return
	}
	cache := k8s.GetResourceCache()
	discovery, dynamic := k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
	if cache == nil || discovery == nil || dynamic == nil || dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete {
		s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.KindSyncPending), "Kueue discovery is not ready; retry shortly")
		return
	}
	workloadGVR, found := discovery.GetGVRWithGroup("Workload", kueueGroup)
	if !found {
		if discovery.GroupHadPartialDiscovery(kueueGroup) {
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.ReadError), "Kueue discovery is incomplete; retry shortly")
		} else {
			s.writeErrorCode(w, http.StatusNotFound, string(readreason.KindUnknown), "Kueue Workloads are not served by this cluster")
		}
		return
	}
	workload, err := cache.GetDynamicWithGroup(r.Context(), "Workload", namespace, name, kueueGroup)
	if err != nil {
		if workloadParentGetError("Workload", namespace, name, err).statusCode == http.StatusNotFound && dynamic.IsNamespaceSynced(workloadGVR, namespace) {
			s.writeErrorCode(w, http.StatusNotFound, string(readreason.ObjectNotFound), "Kueue Workload not found")
		} else {
			log.Printf("[kueue] Failed to read Workload %s/%s: %v", namespace, name, err)
			s.writeErrorCode(w, http.StatusServiceUnavailable, string(readreason.ReadError), "Radar could not observe the Workload; retry when its cache is available")
		}
		return
	}
	response := KueuePositionResponse{UID: string(workload.GetUID()), Eligible: schedulinginsight.KueueWorkloadEligibleForPendingPosition(workload)}
	unchanged := true
	if response.Eligible {
		req, readBinding := kueuevisibility.RequestFor(r.Context(), workload)
		unchanged = readBinding == binding
		if unchanged {
			response.Position = kueuevisibility.Positions.Read(r.Context(), req)
		}
	}
	if !unchanged || operation.Err() != nil || cache != k8s.GetResourceCache() || discovery != k8s.GetResourceDiscovery() || binding != kueuevisibility.CurrentBinding() {
		s.writeErrorCode(w, http.StatusConflict, string(readreason.ConnectionChanged), "Cluster connection changed; retry")
		return
	}
	s.writeJSON(w, response)
}
