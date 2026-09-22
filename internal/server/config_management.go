package server

import (
	"net/http"
	"os"
	"sort"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/prom"
)

func (s *Server) configManagement() string {
	return ConfigurationManagement(s.authConfig, s.cloudConnectCfg.CloudTunnelConfigured, s.listenAddress)
}

func ConfigurationManagement(authConfig auth.Config, cloudTunnel bool, listenAddress string) string {
	if cloudMode() || cloudTunnel {
		return "cloud"
	}
	// A shared Pod can use kubeconfig; a loopback CLI in a dev workspace is still local.
	if authConfig.Enabled() || k8s.GetKubeconfigSummary().Mode == "in-cluster" || (os.Getenv("KUBERNETES_SERVICE_HOST") != "" && !cloud.IsLoopbackHostname(listenAddress)) || settings.OperatorConfigured() {
		return "operator"
	}
	return "local"
}

func (s *Server) requireConfigEditable(w http.ResponseWriter, r *http.Request) bool {
	if s.configManagement() != "operator" {
		return true
	}
	s.writeErrorCode(w, http.StatusForbidden, "operator_managed",
		"This Radar installation is configured by its operator. Update Helm values or startup configuration and restart Radar.")
	return false
}

func (s *Server) handleGetOperatorConfig(w http.ResponseWriter, r *http.Request) {
	url, headers := prometheuspkg.CurrentConfig()
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	cost := opencost.ConfigSnapshot()
	effective := config.Config{
		PrometheusURL:     prom.SafeAddress(url),
		CostSource:        string(cost.Source),
		KubecostURL:       cost.URL,
		KubecostClusterID: cost.ClusterID,
	}
	if s.effectiveConfig != nil {
		effective.Port = s.effectiveConfig.Port
		effective.MCP = s.effectiveConfig.MCP
		effective.TimelineStorage = s.effectiveConfig.TimelineStorage
		effective.TimelineRetention = s.effectiveConfig.TimelineRetention
		effective.TimelineMaxSize = s.effectiveConfig.TimelineMaxSize
		effective.HistoryLimit = s.effectiveConfig.HistoryLimit
		effective.OpenCostCurrency = s.effectiveConfig.OpenCostCurrency
	}
	argoURL, insecure, envManaged := argocd.EnvManagedConfig()
	if envManaged {
		effective.ArgoCDURL = argoURL
		effective.ArgoCDInsecureTLS = insecure
	} else {
		file := config.Load()
		if argocd.ValidateServerURL(file.ArgoCDURL) == nil {
			effective.ArgoCDURL = file.ArgoCDURL
		}
		effective.ArgoCDInsecureTLS = file.ArgoCDInsecureTLS
	}
	argoError := ""
	if argocd.EnvManagedError() != "" {
		argoError = "Argo CD startup configuration is invalid. Ask the operator to check Radar's logs."
	}
	s.writeJSON(w, configResponse{
		Management:               "operator",
		File:                     effective,
		Effective:                effective,
		OpenCostManaged:          true,
		PrometheusHeaderKeys:     keys,
		PrometheusServerManaged:  true,
		PrometheusHeadersManaged: true,
		PrometheusURLFromFlag:    s.promURLFlag,
		KubecostAPIKeySet:        cost.APIKey != "",
		KubecostEnvManaged:       opencost.IsEnvManaged(),
		KubecostEnvError:         opencost.EnvManagedError(),
		ArgoCDTokenSet:           argocd.TokenSet(),
		ArgoCDEnvManaged:         envManaged,
		ArgoCDEnvError:           argoError,
	})
}
