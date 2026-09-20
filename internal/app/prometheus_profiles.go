package app

import (
	"errors"
	"log"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/server"
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
	cfg.PrometheusProfiles = prometheus.NewProfileResolver(config.NewProfileStore(), target, launch)
	cfg.PrometheusURL = ""
	cfg.PrometheusHeaders = nil
	cfg.PrometheusHeadersFromEnv = nil
	if targetErr == nil {
		selection, err := resolveLocalPrometheus(cfg.PrometheusProfiles)
		if err != nil {
			log.Printf("[prometheus] Cluster settings unavailable: %v", err)
		} else {
			cfg.PrometheusURL = selection.Connection.URL
			cfg.PrometheusHeaders = selection.Connection.Headers
		}
	}
	return cfg, nil
}

func resolveLocalPrometheus(resolver *prometheus.ProfileResolver) (prometheus.ProfileSelection, error) {
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		return prometheus.ProfileSelection{}, err
	}
	selection := resolver.Resolve(target, false)
	if selection.View.State == "target_changed" {
		return selection, errors.New("Kubernetes target changed; confirm the metrics connection in Settings")
	}
	return selection, selection.Err
}
