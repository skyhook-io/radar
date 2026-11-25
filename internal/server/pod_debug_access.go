package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type podDebugAccess struct {
	State string `json:"state"`
	Exec  bool   `json:"exec,omitempty"`
	Image string `json:"image,omitempty"`
}

// The namespace capability cannot establish a denial for a particular Pod:
// resourceNames can grant a narrower update. Use the same identity as exec.
func (s *Server) handlePodDebugAccess(w http.ResponseWriter, r *http.Request) {
	ns, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	w.Header().Set("Cache-Control", "no-store")
	if outsideNamespacePin(ns) != nil {
		s.writeError(w, http.StatusForbidden, "namespace is outside Radar's namespace scope")
		return
	}
	if k8s.ForceDisableExec {
		s.writeJSON(w, podDebugAccess{State: "unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	response, err := reviewPodDebugAccess(ctx, s.getClientForRequest(r), ns, name, cloudMode() || r.URL.Query().Get("execOnly") == "true", s.resolveDebugImage(""))
	if err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes could not complete the access check for this Pod. Recheck access.")
		return
	}
	s.writeJSON(w, response)
}

func reviewPodDebugAccess(ctx context.Context, client kubernetes.Interface, ns, name string, execOnly bool, defaultImage string) (podDebugAccess, error) {
	response := podDebugAccess{State: "unknown"}
	if client == nil {
		return podDebugAccess{}, errors.New("Kubernetes client unavailable")
	}
	for _, action := range []struct{ verb, subresource string }{{"create", "exec"}, {"update", "ephemeralcontainers"}} {
		result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: ns, Name: name, Group: "", Verb: action.verb, Resource: "pods", Subresource: action.subresource,
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			if response.Exec {
				return podDebugAccess{State: "check_failed", Exec: true}, nil
			}
			return podDebugAccess{}, err
		}
		if result.Status.Allowed {
			if action.subresource == "exec" {
				response.Exec = true
				if execOnly {
					response.State = "unavailable"
					return response, nil
				}
				// The chooser can preview a confirmed attempt when only the update review is inconclusive.
				response.Image = defaultImage
			}
			continue
		}
		if !result.Status.Denied && result.Status.EvaluationError != "" {
			return response, nil
		}
		if action.subresource == "exec" {
			response.State = "unavailable"
			return response, nil
		}
		response.State = "denied"
		response.Image = ""
		return response, nil
	}
	response.State = "allowed"
	return response, nil
}
