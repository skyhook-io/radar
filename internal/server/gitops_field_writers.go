package server

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type fieldWritersResponse struct {
	Manager *topology.ManagerAnswer `json:"manager,omitempty"`
	Changes []outsideChange         `json:"changes"`
}

// fluxLead is the manager answer's lead when it is a Flux Kustomization or
// HelmRelease, the only roots whose hand edits this reports.
func fluxLead(manager *topology.ManagerAnswer) *topology.ResourceRef {
	if manager == nil || manager.Lead == nil {
		return nil
	}
	switch lead := manager.Lead; {
	case lead.Group == "kustomize.toolkit.fluxcd.io" && lead.Kind == "Kustomization",
		lead.Group == "helm.toolkit.fluxcd.io" && lead.Kind == "HelmRelease":
		return lead
	}
	return nil
}

func fieldWriterChanges(target *unstructured.Unstructured, lead *topology.ResourceRef, ownerObj *unstructured.Unstructured) []outsideChange {
	if lead == nil {
		return []outsideChange{}
	}
	out, _, _ := classifyOutsideObject(target, lead, ownerObj)
	if out.Changes == nil {
		return []outsideChange{}
	}
	return out.Changes
}

// handleGitOpsFieldWriters serves GET /api/gitops/field-writers/{kind}/{namespace}/{name}:
// the hand edits on one object Radar names a Flux manager for. Same reads as
// write evidence: the object (and its Flux root, for the policy) are fetched
// as the caller, and only manager names, times, paths and allowlisted values
// leave the server. Secrets are refused before any read.
func (s *Server) handleGitOpsFieldWriters(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	group := r.URL.Query().Get("group")
	if namespace == "_" {
		namespace = ""
	}
	if group == "" && (strings.EqualFold(kind, "secret") || strings.EqualFold(kind, "secrets")) {
		s.writeError(w, http.StatusBadRequest, "field writers are not read for Secrets")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		s.writeError(w, http.StatusServiceUnavailable, "resource discovery not available")
		return
	}
	gvr, ok := discovery.GetGVRWithGroup(kind, group)
	if !ok {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown kind %q in group %q", kind, group))
		return
	}
	if gvr.Group == "" && gvr.Resource == "secrets" {
		s.writeError(w, http.StatusBadRequest, "field writers are not read for Secrets")
		return
	}
	dyn := s.getDynamicClientForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kubernetes client not available")
		return
	}
	target, err := dyn.Resource(gvr).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		switch {
		case apierrors.IsForbidden(err):
			s.writeError(w, http.StatusForbidden, err.Error())
		case apierrors.IsNotFound(err):
			s.writeError(w, http.StatusNotFound, err.Error())
		default:
			log.Printf("[gitops] Failed to read %s for field writers %s/%s: %v", sanitizeForLog(kind), sanitizeForLog(namespace), sanitizeForLog(name), err)
			s.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	resp := fieldWritersResponse{Changes: []outsideChange{}}
	if fetched, ok := k8s.OwnershipObservation(target); ok {
		resp.Manager, _ = k8s.AnswerLiveManager(fetched, func(c *ownership.Catalog) ownership.Access { return s.ownershipAccess(r, c) })
	}
	lead := fluxLead(resp.Manager)
	var ownerObj *unstructured.Unstructured
	if lead != nil && lead.Namespace != "" {
		if ownerGVR, ok := gitOpsOwnerGVR(lead); ok {
			// Without the root the policy is unknown, and the changes say so.
			ownerObj, _ = dyn.Resource(ownerGVR).Namespace(lead.Namespace).Get(r.Context(), lead.Name, metav1.GetOptions{})
		}
	}
	resp.Changes = fieldWriterChanges(target, lead, ownerObj)
	s.writeJSON(w, resp)
}
