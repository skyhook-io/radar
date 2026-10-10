package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
)

type gitOpsActionCapability struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type gitOpsAccessReview func(context.Context, authv1.ResourceAttributes) (bool, error)

// handleGitOpsCapabilities reads the object as the caller and reviews named
// API operations as that same identity. Lifecycle gates stay in the UI and the
// operation engine; these advisory checks never authorize a write themselves.
func (s *Server) handleGitOpsCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	client := s.getDynamicClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
		return
	}
	review := func(ctx context.Context, attrs authv1.ResourceAttributes) (bool, error) {
		kubeClient := k8s.GetClient()
		if kubeClient == nil {
			return false, fmt.Errorf("cluster client not available")
		}
		user := auth.UserFromContext(ctx)
		if user != nil {
			status, err := pkgauth.ReviewSubjectAccess(ctx, kubeClient, user.Username, user.Groups, attrs)
			if err != nil {
				return false, err
			}
			if status.EvaluationError != "" {
				return false, fmt.Errorf("%s", status.EvaluationError)
			}
			return status.Allowed, nil
		}
		result, err := kubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs}}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		if result.Status.EvaluationError != "" {
			return false, fmt.Errorf("%s", result.Status.EvaluationError)
		}
		return result.Status.Allowed, nil
	}
	kind := chi.URLParam(r, "kind")
	if !strings.EqualFold(kind, "applications") && !strings.EqualFold(kind, "application") {
		if _, err := gitops.ResolveFluxKind(kind); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	capabilities, err := gitOpsCapabilities(r.Context(), client, chi.URLParam(r, "kind"), chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), review)
	if err != nil {
		s.writeGitOpsError(w, err, "gitops", "capabilities", chi.URLParam(r, "namespace"), chi.URLParam(r, "name"))
		return
	}
	s.writeJSON(w, map[string]any{"actions": capabilities})
}

func gitOpsCapabilities(ctx context.Context, client dynamic.Interface, kind, namespace, name string, review gitOpsAccessReview) (map[string]gitOpsActionCapability, error) {
	argo := strings.EqualFold(kind, "applications") || strings.EqualFold(kind, "application")
	entry := gitops.FluxKindEntry{GVR: schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, Kind: "Application"}
	if !argo {
		var err error
		entry, err = gitops.ResolveFluxKind(kind)
		if err != nil {
			return nil, err
		}
	}
	obj, err := client.Resource(entry.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return nil, &gitops.PermissionDenied{Verb: "get", Group: entry.GVR.Group, Resource: entry.GVR.Resource, Namespace: namespace, Err: err}
		}
		return nil, err
	}
	check := func(verb string, target gitops.FluxKindEntry, ns, targetName string) gitOpsActionCapability {
		allowed, err := review(ctx, authv1.ResourceAttributes{Verb: verb, Group: target.GVR.Group, Resource: target.GVR.Resource, Namespace: ns, Name: targetName})
		if err != nil {
			return gitOpsActionCapability{Reason: "Couldn't verify your permissions. Try again."}
		}
		if allowed {
			return gitOpsActionCapability{Allowed: true}
		}
		label := "Flux " + target.Kind + " resources"
		if argo && target.Kind == "Application" {
			label = "Argo CD Applications"
		}
		return gitOpsActionCapability{Reason: fmt.Sprintf("Your role can't %s %s in %s. Ask your cluster admin for access.", verb, label, ns)}
	}
	patch := check("patch", entry, namespace, name)
	actions := map[string]gitOpsActionCapability{}
	if argo {
		for _, action := range []string{"sync", "refresh", "terminate", "suspend", "resume", "rollback", "validate"} {
			actions[action] = patch
		}
		return actions, nil
	}
	for _, action := range []string{"reconcile", "suspend", "resume"} {
		actions[action] = patch
	}
	if entry.Kind == "Kustomization" || entry.Kind == "HelmRelease" {
		source, err := gitops.FluxSyncSource(obj, entry, namespace, name)
		sourceCapability := patch
		if err != nil {
			sourceCapability = gitOpsActionCapability{Reason: err.Error()}
		} else if patch.Allowed {
			sourceEntry, err := gitops.ResolveFluxKind(source.Kind)
			if err != nil {
				return nil, err
			}
			sourceCapability = check("get", sourceEntry, source.Namespace, source.Name)
			if sourceCapability.Allowed {
				sourceCapability = check("patch", sourceEntry, source.Namespace, source.Name)
			}
		}
		actions["sync-with-source"] = sourceCapability
	}
	return actions, nil
}
