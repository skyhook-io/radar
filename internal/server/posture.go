package server

import (
	"net/http"
	"os"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

// Every decision that depends on how Radar is deployed lives here, built from
// the rules in docs/authentication.md#without-authentication:
//
//   - Without sign-in, the request Host must be one a web page cannot point at
//     Radar's address. Signed-in users and the Hub tunnel have their own boundary.
//   - What acts as Radar's operating-system user or process (the host terminal,
//     profiles, command-line arguments) is for the operator alone: never in
//     Radar Cloud, never to people who reach Radar without signing in.
//   - When others can reach Radar, its configuration decides for everyone.
//
// Code elsewhere in this package calls the decisions; it doesn't read the
// listener, the tunnel marker or the posture facts itself
// (TestPostureFactsReadOnlyHere).

type listenerExposure int

const (
	// listenerUnstarted is a Server built by New and never started: tests and
	// direct Handler() users. Start replaces "" with 127.0.0.1, and the CLI and
	// Desktop pass a normalized address, so production never has it. It counts
	// as reachable by others except where noted.
	listenerUnstarted listenerExposure = iota
	listenerLoopback
	listenerShared
)

func exposureOf(listenAddress string) listenerExposure {
	switch {
	case listenAddress == "":
		return listenerUnstarted
	case cloud.IsLoopbackHostname(listenAddress):
		return listenerLoopback
	default:
		return listenerShared
	}
}

type posture struct {
	hostedCloud      bool // Radar Cloud: the app is served only over the tunnel
	tunnelConfigured bool // an OSS Radar connected to Radar Hub (--cloud-url)
	auth             bool
	listener         listenerExposure
}

func (s *Server) posture() posture {
	return posture{
		hostedCloud:      cloudMode(),
		tunnelConfigured: s.cloudConnectCfg.CloudTunnelConfigured,
		auth:             s.authConfig.Enabled(),
		listener:         exposureOf(s.listenAddress),
	}
}

func (p posture) reachableByOthers() bool { return p.listener != listenerLoopback }

// openToOthers: anyone who reaches the listener uses Radar without signing in.
func (p posture) openToOthers() bool { return !p.auth && p.reachableByOthers() }

func viaTunnel(r *http.Request) bool {
	return cloud.IsAuthenticatedTunnelRequest(r.Context())
}

type hostCheckMode int

const (
	hostCheckNone hostCheckMode = iota
	hostCheckLoopbackOnly
	hostCheckSharedAllowlist
)

// hostCheck: an unstarted server is exempt because tests send httptest's
// example.com Host.
func (s *Server) hostCheck(r *http.Request) hostCheckMode {
	p := s.posture()
	switch {
	case p.listener == listenerUnstarted || p.auth || viaTunnel(r):
		return hostCheckNone
	case p.listener == listenerLoopback:
		return hostCheckLoopbackOnly
	default:
		return hostCheckSharedAllowlist
	}
}

// localTerminalUnavailable says why the host terminal is refused, or returns 0
// when it may open. It runs as Radar's OS user and can't be impersonated per
// caller, so it needs the operator at a loopback listener and a loopback Host:
// signed-in users and the tunnel are other people.
func (s *Server) localTerminalUnavailable(r *http.Request) (int, string) {
	p := s.posture()
	switch {
	case k8s.ForceDisableLocalTerminal:
		return http.StatusForbidden, "local terminal is disabled"
	case p.auth:
		return http.StatusForbidden, "local terminal is unavailable when authentication is enabled"
	case viaTunnel(r):
		return http.StatusForbidden, "local terminal is unavailable over the Radar Hub tunnel"
	case k8s.IsInCluster():
		return http.StatusBadRequest, "local terminal not available in-cluster mode"
	case p.reachableByOthers() || !requestHostIsLoopback(r):
		return http.StatusForbidden, "local terminal is only available over a loopback address"
	}
	return 0, ""
}

// pprofEnabled: profiles describe the process, so they are off when Radar is
// open to others unless RADAR_PPROF=true. Elsewhere they stay on, because
// restarting to turn them on could erase a leak or stuck state. Signed-in users
// may read them (command-line arguments excepted); an unstarted server keeps
// them for tests.
func (s *Server) pprofEnabled() bool {
	p := s.posture()
	if p.hostedCloud {
		return false
	}
	if s.enablePprof != nil {
		return *s.enablePprof
	}
	return !p.openToOthers() || p.listener == listenerUnstarted
}

// pprofCmdlineAllowed: once users sign in, process arguments can hold session
// keys, OIDC client secrets or authorization headers.
func (s *Server) pprofCmdlineAllowed() bool {
	return !s.posture().auth
}

// cloudConnectDriverEnabled is not gated on the listener: anyone who can reach
// an unauthenticated Radar already wields its kubeconfig through apply and
// pods/exec, which is more than installing a chart. A listener others can reach
// needs an explicit acknowledgement instead (cloudConnectNeedsSharedAck).
func (s *Server) cloudConnectDriverEnabled() bool {
	p := s.posture()
	return !p.hostedCloud && !p.auth && !p.tunnelConfigured &&
		cloudConnectDeploymentMode() == k8s.DeploymentModeLocal
}

func (s *Server) cloudConnectNeedsSharedAck() bool {
	return s.posture().reachableByOthers()
}

func (s *Server) configManagement() string {
	return ConfigurationManagement(s.authConfig, s.cloudConnectCfg.CloudTunnelConfigured, s.listenAddress)
}

// ConfigurationManagement says who owns integration settings: Radar Cloud, the
// operator (Helm values or flags), or the local user. internal/app calls it
// before a Server exists.
func ConfigurationManagement(authConfig auth.Config, cloudTunnel bool, listenAddress string) string {
	p := posture{hostedCloud: cloudMode(), tunnelConfigured: cloudTunnel, auth: authConfig.Enabled(), listener: exposureOf(listenAddress)}
	switch {
	case p.hostedCloud || p.tunnelConfigured:
		return "cloud"
	// A shared Pod can use kubeconfig; a loopback CLI in a dev workspace is still local.
	case p.auth || k8s.GetKubeconfigSummary().Mode == "in-cluster" ||
		(os.Getenv("KUBERNETES_SERVICE_HOST") != "" && p.reachableByOthers()) || settings.OperatorConfigured():
		return "operator"
	}
	return "local"
}

// aiDeploymentSupported: the engine drives an agent CLI against this server's
// own investigation MCP mount with no credentials, so it needs the mount and no
// auth; under auth, investigations are the embedding host's job (Radar Hub). An
// operator-managed installation is never a user's own machine.
func (s *Server) aiDeploymentSupported() bool {
	return !s.posture().auth && s.configManagement() != "operator" &&
		s.mcpInvestigationHandler != nil && s.aiInvestigationRefs != nil
}

// usageShared: several people use this Radar, so its configuration decides for
// everyone and nobody is asked.
func (s *Server) usageShared() bool {
	return s.posture().reachableByOthers() ||
		deploymentMode() != k8s.DeploymentModeLocal ||
		s.configManagement() != "local"
}

// unauthenticatedListenerWarning: Radar Cloud's own listener serves only health
// probes.
func unauthenticatedListenerWarning(listenAddress string, authEnabled, hostedCloud bool) bool {
	return !hostedCloud && posture{auth: authEnabled, listener: exposureOf(listenAddress)}.openToOthers()
}

// allowAnyHostWarning reads the configured address, before Start normalizes ""
// to loopback.
func allowAnyHostWarning(allowAnyHost, authEnabled bool, listenAddress string) bool {
	return allowAnyHost && posture{auth: authEnabled, listener: exposureOf(listenAddress)}.openToOthers()
}
