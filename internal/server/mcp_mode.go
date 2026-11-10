package server

import (
	"net"
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
)

func (s *Server) mcpEnabled() bool {
	return s.mcpHandler != nil || s.mcpReadOnlyHandler != nil || s.mcpInvestigationHandler != nil
}

func (s *Server) mcpReadOnly() bool {
	return s.mcpEnabled() && s.mcpHandler == nil
}

func (s *Server) mcpApplicationEvidenceAvailable(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return s.mcpHandler != nil && err == nil && ip != nil && ip.IsLoopback() && requestHostIsLoopback(r) &&
		deploymentMode() == k8s.DeploymentModeLocal && !s.authConfig.Enabled() &&
		!s.cloudConnectCfg.CloudTunnelConfigured && !cloud.IsAuthenticatedTunnelRequest(r.Context()) &&
		auth.UserFromContext(r.Context()) == nil
}
