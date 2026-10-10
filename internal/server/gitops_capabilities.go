package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
)

// gitOpsPermission is one API operation an action performs. Source marks a
// Flux source the action reconciles alongside its target.
type gitOpsPermission struct {
	Verb      string `json:"verb"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Source    bool   `json:"source,omitempty"`
}

// gitOpsActionCapability is allowed, denied (with every denied operation, so
// the grant guidance is complete), unknown (Allowed nil, Reason set), or
// unsupported for this object.
type gitOpsActionCapability struct {
	Allowed     *bool              `json:"allowed,omitempty"`
	Denied      []gitOpsPermission `json:"denied,omitempty"`
	Unsupported bool               `json:"unsupported,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

type gitOpsAccessReview func(context.Context, authv1.ResourceAttributes) (bool, error)

// handleGitOpsCapabilities reviews named API operations as the caller. Flux
// targets are read as that identity to resolve source references. Lifecycle gates
// stay in the UI and the operation engine; these advisory checks never authorize a write themselves.
func (s *Server) handleGitOpsCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	kind := chi.URLParam(r, "kind")
	argo := strings.EqualFold(kind, "applications") || strings.EqualFold(kind, "application")
	var client dynamic.Interface
	if !argo {
		if _, err := gitops.ResolveFluxKind(kind); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		client = s.getDynamicClientForRequest(r)
		if client == nil {
			s.writeError(w, http.StatusServiceUnavailable, "cluster client not available")
			return
		}
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
			if !status.Allowed && status.EvaluationError != "" {
				return false, fmt.Errorf("%s", status.EvaluationError)
			}
			return status.Allowed, nil
		}
		result, err := kubeClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attrs}}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		if !result.Status.Allowed && result.Status.EvaluationError != "" {
			return false, fmt.Errorf("%s", result.Status.EvaluationError)
		}
		return result.Status.Allowed, nil
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
	on := func(verb string, target gitops.FluxKindEntry, ns, targetName string, source bool) gitOpsPermission {
		return gitOpsPermission{Verb: verb, Group: target.GVR.Group, Resource: target.GVR.Resource, Namespace: ns, Name: targetName, Kind: target.Kind, Source: source}
	}
	read, patch := on("get", entry, namespace, name, false), on("patch", entry, namespace, name, false)

	type outcome struct {
		allowed bool
		err     error
	}
	reviewed := map[gitOpsPermission]outcome{}
	required := map[string][]gitOpsPermission{}
	actions := map[string]gitOpsActionCapability{}
	if argo {
		required["refresh"] = []gitOpsPermission{patch}
		for _, action := range []string{"sync", "terminate", "suspend", "resume", "rollback", "validate"} {
			required[action] = []gitOpsPermission{read, patch}
		}
	} else {
		// Every Flux operation reads its target first, so the GET's own answer
		// is the read permission. A forbidden read still has patch reviewed,
		// so the grant guidance names both when both are missing.
		obj, err := client.Resource(entry.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			var denied *gitops.PermissionDenied
			if err = gitops.ClassifyPermissionError(err, "get", entry.GVR, namespace, name); !errors.As(err, &denied) {
				return nil, err
			}
			obj = nil
		}
		reviewed[read] = outcome{allowed: obj != nil}
		for _, action := range []string{"reconcile", "suspend", "resume"} {
			required[action] = []gitOpsPermission{read, patch}
		}
		if entry.Kind == "Kustomization" || entry.Kind == "HelmRelease" {
			required["sync-with-source"] = []gitOpsPermission{read, patch}
			if obj != nil {
				source, err := gitops.FluxSyncSource(obj, entry, namespace, name)
				if err != nil {
					delete(required, "sync-with-source")
					actions["sync-with-source"] = gitOpsActionCapability{Unsupported: true, Reason: err.Error() + "."}
				} else {
					sourceEntry, err := gitops.ResolveFluxKind(source.Kind)
					if err != nil {
						return nil, err
					}
					required["sync-with-source"] = append(required["sync-with-source"], on("get", sourceEntry, source.Namespace, source.Name, true), on("patch", sourceEntry, source.Namespace, source.Name, true))
				}
			}
		}
	}

	for action, permissions := range required {
		var denied []gitOpsPermission
		unknown := false
		for _, permission := range permissions {
			result, done := reviewed[permission]
			if !done {
				result.allowed, result.err = review(ctx, authv1.ResourceAttributes{Verb: permission.Verb, Group: permission.Group, Resource: permission.Resource, Namespace: permission.Namespace, Name: permission.Name})
				reviewed[permission] = result
			}
			if result.err != nil {
				unknown = true
			} else if !result.allowed {
				denied = append(denied, permission)
			}
		}
		allowed := len(denied) == 0
		switch {
		case !allowed:
			actions[action] = gitOpsActionCapability{Allowed: &allowed, Denied: denied}
		case unknown:
			actions[action] = gitOpsActionCapability{Reason: "Couldn't check your permissions — retrying"}
		default:
			actions[action] = gitOpsActionCapability{Allowed: &allowed}
		}
	}
	return actions, nil
}
