package app

import (
	"context"
	"errors"
	"fmt"
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
		if !cfg.PrometheusURLFlag {
			return cfg, errors.New("local Prometheus header flags require --prometheus-url in the same launch; saved credentials are not inherited")
		}
		if !cfg.PrometheusLiteralHeaderFlag && len(cfg.PrometheusHeaders) > 0 || !cfg.PrometheusEnvHeaderFlag && len(cfg.PrometheusHeadersFromEnv) > 0 {
			log.Printf("[prometheus] Warning: headers saved in config.json are not sent with --prometheus-url; pass them with --prometheus-header for this launch")
		}
		c := prom.Connection{URL: cfg.PrometheusURL}
		if cfg.PrometheusLiteralHeaderFlag {
			c.Headers = cfg.PrometheusHeaders
		}
		if cfg.PrometheusEnvHeaderFlag {
			c.HeadersFromEnv = cfg.PrometheusHeadersFromEnv
		}
		if c.URL != "" {
			if err := validateStartupPrometheusURL(c.URL); err != nil {
				return cfg, fmt.Errorf("invalid --prometheus-url: %w", err)
			}
		}
		if err := c.ValidateHeaderSources(); err != nil {
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
		launches[config.IntegrationMetrics] = connections.Bundle{Settings: config.IntegrationSettings{Prometheus: launch}}
	}
	argo, argoManaged, err := argocd.EnvironmentConfiguration()
	if argoManaged {
		launches[config.IntegrationArgoCD] = connections.Bundle{Settings: config.IntegrationSettings{ArgoCD: &argo}, Err: err}
	}
	cost, costManaged, err := opencost.EnvironmentConfiguration()
	if costManaged {
		launches[config.IntegrationCost] = connections.Bundle{Settings: config.IntegrationSettings{Kubecost: &pkgopencost.Connection{URL: cost.URL, APIKey: cost.APIKey}, Mode: string(cost.Source), ClusterID: cost.ClusterID}, Err: err}
	}
	// Overrides bind to the launch context. Without one they can never apply,
	// but Radar still starts so the kubeconfig problem is visible in the UI.
	if targetErr != nil && len(launches) > 0 {
		log.Printf("[connections] Startup integration overrides are not applied because this launch has no usable kubeconfig context: %v", targetErr)
		launches = map[config.Integration]connections.Bundle{}
	}
	cfg.LocalConnections = connections.NewResolver(config.NewProfileStore(), target, launches)
	if targetErr == nil && k8s.GetKubeconfigSummary().ContextCount == 1 {
		if imported, err := cfg.LocalConnections.ImportLegacyForSoleContext(context.Background(), target); err != nil {
			log.Printf("[connections] Previous integration settings were not imported: %v", err)
		} else if len(imported) > 0 {
			log.Printf("[connections] Imported previous %v settings for the only kubeconfig context %q", imported, k8s.SanitizeForLog(target.Context))
		}
	}
	cfg.CostSource, cfg.KubecostURL, cfg.KubecostAPIKey, cfg.KubecostAPIKeyContext, cfg.KubecostClusterID, cfg.KubecostClusterIDContext = "auto", "", "", "", "", ""
	cfg.PrometheusURL = ""
	cfg.PrometheusHeaders = nil
	cfg.PrometheusHeadersFromEnv = nil
	if targetErr == nil {
		selection, err := resolveLocalPrometheus(cfg.LocalConnections)
		if err != nil {
			log.Printf("[prometheus] Cluster settings unavailable: %v", err)
		} else {
			cfg.PrometheusURL = selection.Settings.Prometheus.URL
			cfg.PrometheusHeaders = selection.Settings.Prometheus.Headers
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
		return selection, errors.New("the cluster behind this context changed; confirm in Settings that its saved metrics settings still apply")
	}
	return selection, selection.Err
}
