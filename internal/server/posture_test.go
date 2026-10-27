package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

type postureDeployment struct {
	listen      string
	authMode    string // "" or "proxy"
	tunnel      bool   // --cloud-url
	hostedCloud bool   // RADAR_CLOUD_MODE
	inCluster   bool   // in-cluster ServiceAccount config
	inPod       bool   // KUBERNETES_SERVICE_HOST, which inCluster implies
	operator    bool   // operator settings file
}

func (d postureDeployment) server(t *testing.T) *Server {
	t.Helper()
	if d.hostedCloud {
		t.Setenv("RADAR_CLOUD_MODE", "true")
	} else {
		t.Setenv("RADAR_CLOUD_MODE", "")
	}
	mode := "single"
	if d.inCluster {
		mode = "in-cluster"
	}
	if d.inCluster || d.inPod {
		t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	} else {
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
	}
	t.Cleanup(k8s.SetTestKubeconfigMode(mode))
	if d.operator {
		settings.SetOperatorConfig(&settings.OperatorConfig{Version: 1})
	} else {
		settings.SetOperatorConfig(nil)
	}
	t.Cleanup(func() { settings.SetOperatorConfig(nil) })
	prevForce := k8s.ForceDisableLocalTerminal
	k8s.ForceDisableLocalTerminal = false
	t.Cleanup(func() { k8s.ForceDisableLocalTerminal = prevForce })
	return &Server{
		listenAddress:           d.listen,
		authConfig:              auth.Config{Mode: d.authMode},
		cloudConnectCfg:         CloudConnectConfig{CloudTunnelConfigured: d.tunnel},
		mcpInvestigationHandler: http.NotFoundHandler(),
		aiInvestigationRefs:     &investigationrefs.Registry{},
	}
}

func postureRequest(host string, tunnel bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	r.Host = host
	if tunnel {
		cloud.AuthenticatedTunnelHandler(http.HandlerFunc(func(_ http.ResponseWriter, tr *http.Request) { r = tr })).ServeHTTP(httptest.NewRecorder(), r)
	}
	return r
}

type postureDecisions struct {
	hostCheck      hostCheckMode
	terminal       bool
	pprof          bool
	pprofCmdline   bool
	cloudDriver    bool
	cloudAck       bool
	ai             bool
	usageShared    bool
	openWarning    bool
	anyHostWarning bool
}

func decide(s *Server, r *http.Request) postureDecisions {
	status, _ := s.localTerminalUnavailable(r)
	return postureDecisions{
		hostCheck:      s.hostCheck(r),
		terminal:       status == 0,
		pprof:          s.pprofEnabled(),
		pprofCmdline:   s.pprofCmdlineAllowed(),
		cloudDriver:    s.cloudConnectDriverEnabled(),
		cloudAck:       s.cloudConnectNeedsSharedAck(),
		ai:             s.aiDeploymentSupported(),
		usageShared:    s.usageShared(),
		openWarning:    unauthenticatedListenerWarning(s.listenAddress, s.authConfig.Enabled(), cloud.Mode()),
		anyHostWarning: allowAnyHostWarning(true, s.authConfig.Enabled(), s.listenAddress),
	}
}

// TestPostureDecisions pins each deployment decision for the ways Radar runs,
// requested from localhost. A changed row is a changed answer to who may use a
// capability.
func TestPostureDecisions(t *testing.T) {
	for _, tt := range []struct {
		name string
		d    postureDeployment
		want postureDecisions
	}{
		{"local", postureDeployment{listen: "127.0.0.1"},
			postureDecisions{hostCheck: hostCheckLoopbackOnly, terminal: true, pprof: true, pprofCmdline: true, cloudDriver: true, ai: true}},
		{"local, connected to Radar Hub", postureDeployment{listen: "127.0.0.1", tunnel: true},
			postureDecisions{hostCheck: hostCheckLoopbackOnly, terminal: true, pprof: true, pprofCmdline: true, ai: true, usageShared: true}},
		{"local on 0.0.0.0, no auth", postureDeployment{listen: "0.0.0.0"},
			postureDecisions{hostCheck: hostCheckSharedAllowlist, pprofCmdline: true, cloudDriver: true, cloudAck: true, ai: true, usageShared: true, openWarning: true, anyHostWarning: true}},
		{"in-cluster, no auth (chart default)", postureDeployment{listen: "0.0.0.0", inCluster: true},
			postureDecisions{hostCheck: hostCheckSharedAllowlist, pprofCmdline: true, cloudAck: true, usageShared: true, openWarning: true, anyHostWarning: true}},
		{"in-cluster, no auth, connected to Radar Hub without Cloud mode", postureDeployment{listen: "0.0.0.0", tunnel: true, inCluster: true},
			postureDecisions{hostCheck: hostCheckSharedAllowlist, pprofCmdline: true, cloudAck: true, ai: true, usageShared: true, openWarning: true, anyHostWarning: true}},
		{"Pod with a supplied kubeconfig on 0.0.0.0", postureDeployment{listen: "0.0.0.0", inPod: true},
			postureDecisions{hostCheck: hostCheckSharedAllowlist, pprofCmdline: true, cloudDriver: true, cloudAck: true, usageShared: true, openWarning: true, anyHostWarning: true}},
		{"Pod with a supplied kubeconfig on loopback (dev workspace)", postureDeployment{listen: "127.0.0.1", inPod: true},
			postureDecisions{hostCheck: hostCheckLoopbackOnly, terminal: true, pprof: true, pprofCmdline: true, cloudDriver: true, ai: true}},
		{"in-cluster with auth", postureDeployment{listen: "0.0.0.0", authMode: "proxy", inCluster: true},
			postureDecisions{hostCheck: hostCheckNone, pprof: true, cloudAck: true, usageShared: true}},
		{"Radar Cloud", postureDeployment{listen: "0.0.0.0", authMode: "proxy", tunnel: true, hostedCloud: true, inCluster: true},
			postureDecisions{hostCheck: hostCheckNone, cloudAck: true, usageShared: true}},
		{"unstarted server (tests)", postureDeployment{},
			postureDecisions{hostCheck: hostCheckNone, pprof: true, pprofCmdline: true, cloudDriver: true, cloudAck: true, ai: true, usageShared: true, openWarning: true, anyHostWarning: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.d.server(t)
			if got := decide(s, postureRequest("localhost:9280", false)); got != tt.want {
				t.Errorf("decisions\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestPostureTwists(t *testing.T) {
	local := postureDeployment{listen: "127.0.0.1"}

	t.Run("tunnel request skips the Host check and gets no terminal", func(t *testing.T) {
		s := postureDeployment{listen: "127.0.0.1", tunnel: true}.server(t)
		r := postureRequest("localhost:9280", true)
		if s.hostCheck(r) != hostCheckNone {
			t.Error("tunnel request is Host-checked")
		}
		if status, _ := s.localTerminalUnavailable(r); status != http.StatusForbidden {
			t.Errorf("terminal over the tunnel: status %d", status)
		}
	})
	t.Run("terminal needs a loopback Host", func(t *testing.T) {
		s := local.server(t)
		if status, _ := s.localTerminalUnavailable(postureRequest("192.168.1.5:9280", false)); status != http.StatusForbidden {
			t.Errorf("status %d", status)
		}
	})
	t.Run("terminal off when force-disabled", func(t *testing.T) {
		s := local.server(t)
		k8s.ForceDisableLocalTerminal = true
		if status, _ := s.localTerminalUnavailable(postureRequest("localhost:9280", false)); status != http.StatusForbidden {
			t.Errorf("status %d", status)
		}
	})
	t.Run("RADAR_PPROF overrides, except in Radar Cloud", func(t *testing.T) {
		on, off := true, false
		s := postureDeployment{listen: "0.0.0.0"}.server(t)
		if s.enablePprof = &on; !s.pprofEnabled() {
			t.Error("RADAR_PPROF=true ignored on an open listener")
		}
		s = local.server(t)
		if s.enablePprof = &off; s.pprofEnabled() {
			t.Error("RADAR_PPROF=false ignored locally")
		}
		s = postureDeployment{listen: "0.0.0.0", hostedCloud: true}.server(t)
		if s.enablePprof = &on; s.pprofEnabled() {
			t.Error("Radar Cloud mounted pprof")
		}
	})
	t.Run("operator settings make AI unavailable and usage shared", func(t *testing.T) {
		s := postureDeployment{listen: "127.0.0.1", operator: true}.server(t)
		if s.aiDeploymentSupported() || !s.usageShared() {
			t.Errorf("ai %v, usageShared %v", s.aiDeploymentSupported(), s.usageShared())
		}
	})
	t.Run("AI needs the investigation MCP mount", func(t *testing.T) {
		s := local.server(t)
		s.mcpInvestigationHandler = nil
		if s.aiDeploymentSupported() {
			t.Error("AI supported without the MCP mount")
		}
	})
	t.Run("Radar Cloud's listener does not warn", func(t *testing.T) {
		if unauthenticatedListenerWarning("0.0.0.0", false, true) {
			t.Error("warned")
		}
	})
}
