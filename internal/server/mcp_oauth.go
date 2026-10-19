package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
)

func (s *Server) authenticate(next http.Handler) http.Handler {
	standard := auth.Authenticate(s.authConfig)(next)
	if s.mcpOAuth == nil {
		return standard
	}
	mcp := s.mcpOAuth.Authenticate(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" || (r.URL.Path == "/mcp-readonly" && s.mcpReadOnlyHandler != nil) {
			mcp.ServeHTTP(w, r)
			return
		}
		standard.ServeHTTP(w, r)
	})
}

func (s *Server) mountMCPOAuthMetadata(r chi.Router, prefix string) {
	r.Get("/.well-known/oauth-authorization-server"+prefix, s.mcpOAuth.HandleMetadata)
	r.Get("/.well-known/openid-configuration"+prefix, s.mcpOAuth.HandleMetadata)
	for _, resource := range []string{"/mcp", "/mcp-readonly"} {
		if resource == "/mcp-readonly" && s.mcpReadOnlyHandler == nil {
			continue
		}
		r.Get("/.well-known/oauth-protected-resource"+prefix+resource, s.mcpOAuth.ProtectedResourceMetadata(resource))
	}
}
