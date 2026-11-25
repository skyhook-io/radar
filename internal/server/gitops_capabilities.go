package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
)

const (
	unknownPermissionReason = "Couldn't check your permissions — retrying"
	systemReadOnlyReason    = "Background identities can only read through Radar without a user token."
)

// gitOpsPermission is one API operation an action performs.
type gitOpsPermission struct {
	Verb      string `json:"verb"`
	Group     string `json:"group"`
	Version   string `json:"-"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	// Source is true when the tuple is for a Flux source the action also reconciles.
	Source bool `json:"source,omitempty"`
}

// gitOpsActionCapability has four answer forms: {allowed:true},
// {allowed:false, denied:[…]}, {reason} (unknown after a failed or expired check;
// permit an attempt), or {unsupported:true, reason}.
// Denied lists operations checked and denied, not necessarily every missing grant:
// a denied read can prevent discovering later Flux source hops.
// Action map keys use sync for selective sync, refresh for hard refresh, validate
// for resource validation, and sync-with-source for the Flux source chain.
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
	conn := k8s.ConnectionFrom(r.Context())
	if conn == nil {
		conn = k8s.Current()
		r = r.WithContext(k8s.WithConnection(r.Context(), conn))
	}
	kind := chi.URLParam(r, "kind")
	user := auth.UserFromContext(r.Context())
	useToken := user != nil && pkgauth.UserTokenMode()
	var client dynamic.Interface
	if !isArgoPermissionTarget(kind) {
		if _, err := gitops.ResolveFluxKind(kind); err != nil {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !useToken || user.Token != "" {
			client = s.getDynamicClientForRequest(r)
		}
	}
	kubeClient := conn.ClientInterface()
	if useToken {
		kubeClient = nil
		if user.Token != "" {
			kubeClient = k8s.ClientFromContext(r.Context())
		}
	}

	review := func(ctx context.Context, attrs authv1.ResourceAttributes) (bool, error) {
		if kubeClient == nil {
			return false, fmt.Errorf("cluster client not available")
		}
		if user != nil && !useToken {
			groups := user.Groups
			switch {
			case user.Username == "system:anonymous" && !slices.Contains(groups, "system:unauthenticated"):
				groups = append(slices.Clone(groups), "system:unauthenticated")
			case user.Username != "" && !slices.Contains(groups, "system:authenticated") && !slices.Contains(groups, "system:unauthenticated"):
				groups = append(slices.Clone(groups), "system:authenticated")
			}
			status, err := pkgauth.ReviewSubjectAccess(ctx, kubeClient, user.Username, groups, attrs)
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
	capabilities, err := gitOpsCapabilities(r.Context(), client, kind, chi.URLParam(r, "namespace"), chi.URLParam(r, "name"), review)
	if errors.Is(err, errGitOpsCapabilitiesClientUnavailable) {
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		s.writeGitOpsError(w, err, "gitops", "capabilities", chi.URLParam(r, "namespace"), chi.URLParam(r, "name"))
		return
	}
	s.writeJSON(w, map[string]any{"actions": capabilities})
}

var errGitOpsCapabilitiesClientUnavailable = errors.New("cluster client not available")

func isArgoPermissionTarget(kind string) bool {
	return strings.EqualFold(kind, "applications") || strings.EqualFold(kind, "application")
}

func gitOpsCapabilities(ctx context.Context, client dynamic.Interface, kind, namespace, name string, review gitOpsAccessReview) (map[string]gitOpsActionCapability, error) {
	argo := isArgoPermissionTarget(kind)
	resolve := k8s.ServedGVRFor(ctx)
	entry := gitops.FluxKindEntry{GVR: schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, Kind: "Application"}
	if !argo {
		var err error
		entry, err = gitops.ResolveFluxKind(kind)
		if err != nil {
			return nil, err
		}
		entry = entry.Served(resolve)
	}
	on := func(verb string, target gitops.FluxKindEntry, ns, targetName string, source bool) gitOpsPermission {
		return gitOpsPermission{Verb: verb, Group: target.GVR.Group, Version: target.GVR.Version, Resource: target.GVR.Resource, Namespace: ns, Name: targetName, Kind: target.Kind, Source: source}
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
		for _, action := range []string{"reconcile", "suspend", "resume"} {
			required[action] = []gitOpsPermission{read, patch}
		}
		if entry.Kind == "Kustomization" || entry.Kind == "HelmRelease" {
			required["sync-with-source"] = []gitOpsPermission{read, patch}
		}
	}

	if user := auth.UserFromContext(ctx); user != nil && pkgauth.UserTokenMode() && user.Token == "" {
		result := gitOpsActionCapability{Reason: unknownPermissionReason}
		if user.System {
			result = gitOpsActionCapability{Unsupported: true, Reason: systemReadOnlyReason}
		}
		for action := range required {
			actions[action] = result
		}
		return actions, nil
	}

	if !argo {
		if client == nil {
			return nil, errGitOpsCapabilitiesClientUnavailable
		}
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
		if obj != nil && required["sync-with-source"] != nil {
			chain, err := gitops.FluxSyncSources(obj, func(target gitops.FluxSyncTarget) (*unstructured.Unstructured, error) {
				read := on("get", target.Entry, target.Namespace, target.Name, true)
				required["sync-with-source"] = append(required["sync-with-source"], read, on("patch", target.Entry, target.Namespace, target.Name, true))
				if target.Entry.Kind != "HelmChart" {
					return nil, nil
				}
				// Only a HelmChart needs a read to discover another link. Never
				// use the shared cache to resolve a source the caller can't read.
				chart, err := client.Resource(target.Entry.GVR).Namespace(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
				err = gitops.ClassifyPermissionError(err, "get", target.Entry.GVR, target.Namespace, target.Name)
				var denied *gitops.PermissionDenied
				if errors.As(err, &denied) {
					reviewed[read] = outcome{allowed: false}
					return nil, nil
				}
				if err == nil {
					reviewed[read] = outcome{allowed: true}
				}
				return chart, err
			}, resolve)
			// A source problem affects only sync-with-source; reconcile, suspend
			// and resume keep their own answers.
			switch {
			case errors.Is(err, gitops.ErrSyncWithSourceUnsupported):
				delete(required, "sync-with-source")
				actions["sync-with-source"] = gitOpsActionCapability{Unsupported: true, Reason: err.Error() + "."}
			case apierrors.IsNotFound(err) && len(chain) > 0:
				delete(required, "sync-with-source")
				link := chain[0]
				reason := fmt.Sprintf("%s %s/%s was not found. Check the chart reference and that the object exists.", link.Entry.Kind, link.Namespace, link.Name)
				if _, explicit, _ := unstructured.NestedMap(obj.Object, "spec", "chartRef"); !explicit {
					reason = fmt.Sprintf("Flux hasn't created %s %s/%s yet, so there's no source to reconcile.", link.Entry.Kind, link.Namespace, link.Name)
				}
				actions["sync-with-source"] = gitOpsActionCapability{Unsupported: true, Reason: reason}
			case err != nil:
				delete(required, "sync-with-source")
				actions["sync-with-source"] = gitOpsActionCapability{Reason: unknownPermissionReason}
			}
		}
	}

	for action, permissions := range required {
		var denied []gitOpsPermission
		unknown := false
		for _, permission := range permissions {
			result, done := reviewed[permission]
			if !done {
				result.allowed, result.err = review(ctx, authv1.ResourceAttributes{Verb: permission.Verb, Group: permission.Group, Version: permission.Version, Resource: permission.Resource, Namespace: permission.Namespace, Name: permission.Name})
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
			actions[action] = gitOpsActionCapability{Reason: unknownPermissionReason}
		default:
			actions[action] = gitOpsActionCapability{Allowed: &allowed}
		}
	}
	return actions, nil
}
