package server

import (
	"context"
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
	"k8s.io/client-go/dynamic"
)

type fieldWritersResponse struct {
	Changes []outsideChange `json:"changes"`
}

// fluxLead is the Flux Kustomization or HelmRelease that manages the object
// itself, on evidence that it still does: a status list that names it, or a
// claim whose manager exists here and can't contradict it (the same bar the
// AI context's manager summary uses). A manager inherited from an ancestor
// (a Pod of a Flux-applied Deployment) doesn't count: Flux never applied the
// object, so its edits aren't "outside Flux".
func fluxLead(manager *topology.ManagerAnswer) *topology.ResourceRef {
	if manager == nil || manager.Lead == nil || manager.Via != nil || manager.Lead.Namespace == "" {
		return nil
	}
	lead := manager.Lead
	if !(lead.Group == "kustomize.toolkit.fluxcd.io" && lead.Kind == "Kustomization") &&
		!(lead.Group == "helm.toolkit.fluxcd.io" && lead.Kind == "HelmRelease") {
		return nil
	}
	switch manager.State {
	case "agree", "listed", "shared":
		return lead
	case "claimed":
		for _, claim := range manager.Direct.Claims {
			if claim.Copied || claim.Weak || claim.Manager != *lead {
				continue
			}
			switch claim.Check {
			case "recorded", "omitted", "unreadable":
				return lead
			}
		}
	}
	return nil
}

// fieldWritersFor reads the Flux root as the caller for its policy; without
// it the outcome is unknown, and the changes say so.
func fieldWritersFor(ctx context.Context, dyn dynamic.Interface, target *unstructured.Unstructured, lead *topology.ResourceRef) []outsideChange {
	var ownerObj *unstructured.Unstructured
	if lead != nil {
		if ownerGVR, ok := gitOpsOwnerGVR(lead); ok {
			obj, err := dyn.Resource(ownerGVR).Namespace(lead.Namespace).Get(ctx, lead.Name, metav1.GetOptions{})
			switch {
			case err == nil:
				ownerObj = obj
			case !apierrors.IsForbidden(err) && !apierrors.IsNotFound(err):
				log.Printf("[gitops] Failed to read %s %s/%s for field writers: %v", lead.Kind, sanitizeForLog(lead.Namespace), sanitizeForLog(lead.Name), err)
			}
		}
	}
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

	var manager *topology.ManagerAnswer
	if fetched, ok := k8s.OwnershipObservation(target); ok {
		manager, _ = k8s.AnswerLiveManager(fetched, func(c *ownership.Catalog) ownership.Access { return s.ownershipAccess(r, c) })
	}
	s.writeJSON(w, fieldWritersResponse{Changes: fieldWritersFor(r.Context(), dyn, target, fluxLead(manager))})
}
