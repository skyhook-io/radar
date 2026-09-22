package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func (s *Server) auditOptions(r *http.Request) *audit.RunOptions {
	if auth.UserFromContext(r.Context()) == nil {
		return nil
	}
	namespaces := s.getUserNamespaces(r, nil)
	secrets := namespaces
	if secrets == nil && !s.canRead(r, "", "secrets", "", "list") {
		candidates := k8s.AllNamespaceNames()
		if len(candidates) == 0 {
			candidates, _ = k8s.GetAccessibleNamespaces(r.Context())
		}
		secrets = append([]string{}, candidates...)
	}
	secrets = s.secretReadableNamespaces(r, secrets)
	scope := audit.ResolveReadScope(namespaces, secrets, func(group, resource, ns string) bool { return s.canRead(r, group, resource, ns, "list") })
	return &audit.RunOptions{Scope: scope}
}
