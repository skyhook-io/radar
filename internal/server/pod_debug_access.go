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
	if k8s.ForceDisableExec || cloudMode() {
		s.writeJSON(w, podDebugAccess{State: "unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	state := reviewPodDebugAccess(ctx, s.getClientForRequest(r), ns, name)
	response := podDebugAccess{State: state}
	if state == "allowed" {
		response.Image = s.resolveDebugImage("")
	}
	s.writeJSON(w, response)
}

func reviewPodDebugAccess(ctx context.Context, client kubernetes.Interface, ns, name string) string {
	if client == nil {
		return "unknown"
	}
	for _, action := range []struct{ verb, subresource string }{{"create", "exec"}, {"update", "ephemeralcontainers"}} {
		result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: ns, Name: name, Group: "", Verb: action.verb, Resource: "pods", Subresource: action.subresource,
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			return "unknown"
		}
		if result.Status.Allowed {
			continue
		}
		if !result.Status.Denied && result.Status.EvaluationError != "" {
			return "unknown"
		}
		if action.subresource == "exec" {
			return "unavailable"
		}
		return "denied"
	}
	return "allowed"
}
