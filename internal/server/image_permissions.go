package server

import (
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	authv1 "k8s.io/api/authorization/v1"
)

func (s *Server) imagePodReadDecision(r *http.Request, namespace string) (bool, bool) {
	if auth.UserFromContext(r.Context()) != nil && s.permCache == nil {
		return false, false
	}
	return s.canReadDecision(r, "", "pods", namespace, "get")
}

func (s *Server) imageExecDecision(r *http.Request, namespace string) (bool, bool) {
	if auth.UserFromContext(r.Context()) == nil || s.permCache == nil {
		return false, false
	}
	return s.grantDecision(r, Grant{Verb: "create", Resource: "pods", Subresource: "exec", Namespace: namespace})
}

func (s *Server) imagePullSecretDecision(r *http.Request, namespace, secretName string) (bool, bool) {
	user := auth.UserFromContext(r.Context())
	if user == nil || s.permCache == nil {
		return false, false
	}
	if allowed, authoritative := s.imageExecDecision(r, namespace); allowed && authoritative {
		return true, true
	}
	perms := s.permCache.Get(user.Username, user.Groups)
	key := "secrets/" + secretName
	if perms != nil {
		if allowed, ok := perms.CanI("get", "", key, namespace); ok {
			return allowed, true
		}
	}
	client := k8s.GetClient()
	if client == nil {
		return false, false
	}
	// Include Name so resourceNames-restricted Secret grants apply.
	status, err := pkgauth.ReviewSubjectAccess(r.Context(), client, user.Username, user.Groups, authv1.ResourceAttributes{
		Verb: "get", Resource: "secrets", Namespace: namespace, Name: secretName,
	})
	if err != nil {
		log.Printf("[images] Failed to authorize pull secret %s/%s: %v", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(secretName), err)
		return false, false
	}
	authoritative := status.EvaluationError == ""
	if authoritative && perms != nil {
		perms.SetCanI("get", "", key, namespace, status.Allowed)
	}
	return status.Allowed, authoritative
}
