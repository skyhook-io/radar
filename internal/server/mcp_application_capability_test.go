package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestMCPApplicationEvidenceCapability(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	t.Setenv("RADAR_CLOUD_MODE", "false")
	for _, tc := range []struct {
		name                                                                                 string
		peer                                                                                 string
		host                                                                                 string
		readOnly, authenticated, configuredTunnel, tunnelRequest, inCluster, cloudMode, want bool
	}{
		{name: "local", peer: "127.0.0.1:9000", want: true},
		{name: "ipv6", peer: "[::1]:9000", want: true},
		{name: "remote", peer: "192.0.2.1:9000"},
		{name: "nonlocal host", peer: "127.0.0.1:9000", host: "radar.example.com"},
		{name: "unknown peer"},
		{name: "read only", peer: "127.0.0.1:9000", readOnly: true},
		{name: "auth configured", peer: "127.0.0.1:9000", authenticated: true},
		{name: "tunnel configured", peer: "127.0.0.1:9000", configuredTunnel: true},
		{name: "tunnel request", peer: "127.0.0.1:9000", tunnelRequest: true},
		{name: "in cluster", peer: "127.0.0.1:9000", inCluster: true},
		{name: "cloud", peer: "127.0.0.1:9000", cloudMode: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{mcpHandler: http.NotFoundHandler()}
			if tc.readOnly {
				s.mcpHandler = nil
			}
			if tc.authenticated {
				s.authConfig = auth.Config{Mode: "proxy"}
			}
			s.cloudConnectCfg.CloudTunnelConfigured = tc.configuredTunnel
			k8s.ForceInCluster = tc.inCluster
			defer func() { k8s.ForceInCluster = false }()
			if tc.cloudMode {
				t.Setenv("RADAR_CLOUD_MODE", "true")
			}
			r := httptest.NewRequest(http.MethodGet, "http://localhost/api/capabilities", nil)
			r.RemoteAddr = tc.peer
			if tc.host != "" {
				r.Host = tc.host
			}
			handler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if got := s.mcpApplicationEvidenceAvailable(r); got != tc.want {
					t.Fatalf("availability = %v, want %v", got, tc.want)
				}
			})
			if tc.tunnelRequest {
				cloud.AuthenticatedTunnelHandler(handler).ServeHTTP(httptest.NewRecorder(), r)
			} else {
				handler.ServeHTTP(httptest.NewRecorder(), r)
			}
		})
	}
}
