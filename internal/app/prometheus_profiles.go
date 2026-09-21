package app

import (
	"errors"
	"log"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/server"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

func preparePrometheusConfiguration(cfg AppConfig) (AppConfig, error) {
	if err := loadOperatorSettings(cfg); err != nil {
		return cfg, err
	}
	cfg.operatorSettingsLoaded = true
	if server.ConfigurationManagement(cfg.AuthConfig, cfg.CloudTunnelConfigured, cfg.ListenAddress) != "local" {
		inherits := !cfg.PrometheusLiteralHeaderFlag && len(cfg.PrometheusHeaders) > 0 || !cfg.PrometheusEnvHeaderFlag && len(cfg.PrometheusHeadersFromEnv) > 0
		if err := ValidatePrometheusHeaderDestination(cfg.PrometheusSavedURL, cfg.PrometheusURL, inherits); err != nil {
			return cfg, err
		}
		headers, err := ResolvePrometheusHeaders(cfg.PrometheusHeaders, cfg.PrometheusHeadersFromEnv)
		cfg.PrometheusHeaders = headers
		return cfg, err
	}
	target, targetErr := k8s.CurrentProfileTarget()
	var launch *prom.Connection
	if cfg.PrometheusURLFlag || cfg.PrometheusHeaderFlags {
		if targetErr != nil {
			return cfg, errors.New("local Prometheus flags require a selected kubeconfig context; select one before launching Radar")
		}
		if !cfg.PrometheusURLFlag {
			return cfg, errors.New("local Prometheus header flags require --prometheus-url in the same launch; saved credentials are not inherited")
		}
		c := prom.Connection{URL: cfg.PrometheusURL}
		if cfg.PrometheusLiteralHeaderFlag {
			c.Headers = cfg.PrometheusHeaders
		}
		if cfg.PrometheusEnvHeaderFlag {
			c.HeadersFromEnv = cfg.PrometheusHeadersFromEnv
		}
		if err := c.Validate(); err != nil {
			return cfg, err
		}
		headers, err := prom.ResolveHeaders(c.Headers, c.HeadersFromEnv)
		if err != nil {
			return cfg, err
		}
		c.Headers = headers
		c.HeadersFromEnv = nil
		launch = &c
	}
	launches := map[config.Integration]connections.Bundle{}
	if launch != nil {
		launches[config.IntegrationMetrics] = connections.Bundle{Connection: config.SavedConnection{Type: config.IntegrationMetrics, Prometheus: launch}, Assignment: config.IntegrationAssignment{Mode: "connection"}}
	}
	argo, argoManaged, err := argocd.EnvironmentConfiguration()
	if argoManaged {
		if targetErr != nil {
			return cfg, errors.New("local Argo CD environment configuration requires a selected kubeconfig context")
		}
		mode := "auto"
		if argo.URL != "" {
			mode = "connection"
		}
		launches[config.IntegrationArgoCD] = connections.Bundle{Connection: config.SavedConnection{Type: config.IntegrationArgoCD, ArgoCD: &argo}, Assignment: config.IntegrationAssignment{Mode: mode}, Err: err}
	}
	cost, costManaged, err := opencost.EnvironmentConfiguration()
	if costManaged {
		if targetErr != nil {
			return cfg, errors.New("local cost environment configuration requires a selected kubeconfig context")
		}
		launches[config.IntegrationCost] = connections.Bundle{Connection: config.SavedConnection{Type: config.IntegrationCost, Kubecost: &pkgopencost.Connection{URL: cost.URL, APIKey: cost.APIKey}}, Assignment: config.IntegrationAssignment{Mode: string(cost.Source), ClusterID: cost.ClusterID}, Err: err}
	}
	cfg.LocalConnections = connections.NewResolver(config.NewProfileStore(), target, launches)
	cfg.CostSource, cfg.KubecostURL, cfg.KubecostAPIKey, cfg.KubecostAPIKeyContext, cfg.KubecostClusterID, cfg.KubecostClusterIDContext = "auto", "", "", "", "", ""
	cfg.PrometheusURL = ""
	cfg.PrometheusHeaders = nil
	cfg.PrometheusHeadersFromEnv = nil
	if targetErr == nil {
		selection, err := resolveLocalPrometheus(cfg.LocalConnections)
		if err != nil {
			log.Printf("[prometheus] Cluster settings unavailable: %v", err)
		} else {
			cfg.PrometheusURL = selection.Connection.Prometheus.URL
			cfg.PrometheusHeaders = selection.Connection.Prometheus.Headers
		}
	}
	return cfg, nil
}

func resolveLocalPrometheus(resolver *connections.Resolver) (connections.Selection, error) {
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		return connections.Selection{}, err
	}
	selection := resolver.Resolve(target, config.IntegrationMetrics, false)
	if selection.View.State == "target_changed" {
		return selection, errors.New("Kubernetes target changed; confirm the metrics connection in Settings")
	}
	return selection, selection.Err
}
