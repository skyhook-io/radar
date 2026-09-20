package app

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/audit"
	"k8s.io/apimachinery/pkg/util/validation"
)

func loadOperatorSettings(cfg AppConfig) error {
	settings.SetOperatorConfig(nil)
	path := os.Getenv(settings.OperatorFileEnv)
	if path == "" {
		if server.ConfigurationManagement(cfg.AuthConfig, cfg.CloudTunnelConfigured, cfg.ListenAddress) == "operator" {
			if saved := settings.Load(); saved.Audit != nil || len(saved.HelmOCISources) > 0 {
				log.Printf("[settings] Ignoring UI-saved audit/OCI settings in this managed installation; supply %s to preserve the intended policy. config.json remains a startup-default source", settings.OperatorFileEnv)
			}
			settings.SetOperatorConfig(&settings.OperatorConfig{Version: 1})
		}
		return nil
	}
	if cloud.Mode() || cfg.CloudTunnelConfigured {
		log.Printf("[settings] Ignoring %s in Radar Cloud mode; Cloud configuration behavior is unchanged", settings.OperatorFileEnv)
		return nil
	}
	operator, err := settings.ReadOperatorConfig(path)
	if err != nil {
		return err
	}
	operator.HelmOCISources, err = helm.ValidateOCISources(operator.HelmOCISources)
	if err != nil {
		return fmt.Errorf("helmOciSources: %w", err)
	}
	if operator.Audit != nil {
		for _, id := range operator.Audit.DisabledChecks {
			if _, ok := audit.CheckRegistry[id]; !ok {
				return fmt.Errorf("audit.disabledChecks: unknown check %q", id)
			}
		}
		for _, ns := range operator.Audit.IgnoredNamespaces {
			// Audit supports one leading or trailing wildcard, not general globs.
			literal := strings.ReplaceAll(ns, "*", "a")
			if strings.Count(ns, "*") > 1 || (strings.Contains(ns, "*") && !strings.HasPrefix(ns, "*") && !strings.HasSuffix(ns, "*")) || len(validation.IsDNS1123Label(literal)) > 0 {
				return fmt.Errorf("audit.ignoredNamespaces: invalid namespace pattern %q", ns)
			}
		}
	}
	settings.SetOperatorConfig(operator)
	return nil
}
