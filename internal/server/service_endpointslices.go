package server

import (
	"errors"
	"log"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	serviceEndpointSliceLimit = 500
	endpointSliceServiceLabel = "kubernetes.io/service-name"
	endpointSliceGroup        = "discovery.k8s.io"
)

type serviceEndpointSlicesResponse struct {
	Items     []*unstructured.Unstructured `json:"items"`
	Truncated bool                         `json:"truncated"`
}

// handleServiceEndpointSlices returns the EndpointSlices published for one
// Service: those carrying kubernetes.io/service-name=<name>, the label kube-proxy
// and the EndpointSlice controllers key on. Owner references alone do not
// publish endpoints, so they are not consulted.
//
// EndpointSlices bypass the informer cache, so this is a direct API LIST —
// label-selected and capped at serviceEndpointSliceLimit so a Service drawer
// never pulls a whole namespace's slices. truncated reports more beyond the cap.
//
// Gating is that of GET /api/resources/endpointslices?namespace=<ns>&group=discovery.k8s.io
// (same namespace clamp, RBAC intersection and preflight), except a denial is
// an explicit 403 rather than an empty list: the drawer must not present
// "no EndpointSlices" to a user who simply cannot read them.
func (s *Server) handleServiceEndpointSlices(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "namespace and name are required")
		return
	}
	selector, err := labels.ValidatedSelectorFromSet(labels.Set{endpointSliceServiceLabel: name})
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid service name: "+err.Error())
		return
	}

	namespaces := s.parseNamespacesForUser(withNamespaceFilter(r, namespace))
	if _, status, msg, ok := s.preflightResourceList(r, "endpointslices", endpointSliceGroup, namespaces); !ok {
		s.writeError(w, status, msg)
		return
	}
	cache, ok := s.gateResourceRead(w, "endpointslices", endpointSliceGroup)
	if !ok {
		return
	}

	items, truncated, err := cache.ListDirectSelectedWithGroup(r.Context(), "endpointslices", namespace, endpointSliceGroup, selector.String(), serviceEndpointSliceLimit)
	if err != nil {
		switch {
		case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
			s.writeError(w, http.StatusForbidden, "insufficient permissions to list endpointslices")
		case errors.Is(err, k8s.ErrDynamicNotReady):
			s.writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			log.Printf("[endpointslices] Failed to list EndpointSlices for Service %s/%s: %v", namespace, name, err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if items == nil {
		items = []*unstructured.Unstructured{}
	}
	s.writeJSON(w, serviceEndpointSlicesResponse{Items: items, Truncated: truncated})
}

// withNamespaceFilter presents a path namespace to parseNamespacesForUser as an
// explicit ?namespace= filter, so the route gets the generic list route's
// --namespace-scope clamp and RBAC intersection rather than a reimplementation.
func withNamespaceFilter(r *http.Request, namespace string) *http.Request {
	clone := r.Clone(r.Context())
	clone.URL.RawQuery = url.Values{"namespace": {namespace}}.Encode()
	return clone
}
