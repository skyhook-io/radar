package server

import (
	"context"
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
	response := reviewPodDebugAccess(ctx, s.getClientForRequest(r), ns, name, cloudMode() || r.URL.Query().Get("execOnly") == "true", s.resolveDebugImage(""))
	s.writeJSON(w, response)
}

func reviewPodDebugAccess(ctx context.Context, client kubernetes.Interface, ns, name string, execOnly bool, defaultImage string) podDebugAccess {
	response := podDebugAccess{State: "unknown"}
	if client == nil {
		return response
	}
	for _, action := range []struct{ verb, subresource string }{{"create", "exec"}, {"update", "ephemeralcontainers"}} {
		result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: ns, Name: name, Group: "", Verb: action.verb, Resource: "pods", Subresource: action.subresource,
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			return response
		}
		if result.Status.Allowed {
			if action.subresource == "exec" {
				response.Exec = true
				if execOnly {
					response.State = "unavailable"
					return response
				}
				// The chooser can preview a confirmed attempt when only the update review is inconclusive.
				response.Image = defaultImage
			}
			continue
		}
		if !result.Status.Denied && result.Status.EvaluationError != "" {
			return response
		}
		if action.subresource == "exec" {
			response.State = "unavailable"
			return response
		}
		response.State = "denied"
		response.Image = ""
		return response
	}
	response.State = "allowed"
	return response
}
