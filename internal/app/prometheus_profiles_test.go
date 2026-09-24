package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/timeline"
	"k8s.io/client-go/rest"
)

func TestPrepareLocalPrometheusConfiguration(t *testing.T) {
	useTempHome(t)
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestProfileSource("/test/local", "dev", "developer"))
	old := k8s.SetTestConfig(&rest.Config{Host: "https://cluster"})
	t.Cleanup(func() { k8s.SetTestConfig(old) })
	t.Setenv(settings.OperatorFileEnv, "")
	t.Cleanup(func() { settings.SetOperatorConfig(nil) })
	for _, tc := range []struct {
		name    string
		cfg     AppConfig
		wantURL string
		denied  bool
	}{
		{"inactive legacy environment", AppConfig{PrometheusURL: "https://legacy", PrometheusHeadersFromEnv: map[string]string{"Authorization": "UNSET_LEGACY_CREDENTIAL"}}, "", false},
		{"URL-only override", AppConfig{PrometheusURL: "https://launch", PrometheusURLFlag: true, PrometheusHeaders: map[string]string{"Authorization": "legacy"}}, "https://launch", false},
		{"header-only override", AppConfig{PrometheusURL: "https://legacy", PrometheusHeaderFlags: true, PrometheusLiteralHeaderFlag: true, PrometheusHeaders: map[string]string{"Authorization": "new"}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := preparePrometheusConfiguration(tc.cfg)
			if (err != nil) != tc.denied {
				t.Fatalf("prepare: %v", err)
			}
			if err == nil && (got.PrometheusURL != tc.wantURL || len(got.PrometheusHeaders) != 0 || got.LocalConnections == nil) {
				t.Fatalf("unexpected selected config: url=%q headers=%d profiles=%v", got.PrometheusURL, len(got.PrometheusHeaders), got.LocalConnections != nil)
			}
		})
	}
	if _, _, err := config.NewProfileStore().Read(); err != nil {
		t.Fatal(err)
	}
}

func TestUnusableProfileDoesNotPreventStartupWithScope(t *testing.T) {
	if scenario := os.Getenv("RADAR_PROFILE_STARTUP_CASE"); scenario != "" {
		useTempHome(t)
		t.Cleanup(k8s.SetTestLocalMode())
		t.Cleanup(k8s.SetTestProfileSource("/test/local", "dev", "developer"))
		old := k8s.SetTestConfig(&rest.Config{Host: "https://cluster"})
		t.Cleanup(func() { k8s.SetTestConfig(old) })
		t.Setenv(settings.OperatorFileEnv, "")
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
		store := config.NewProfileStore()
		if scenario == "malformed" {
			if err := os.MkdirAll(filepath.Dir(store.Path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path, []byte("invalid"), 0600); err != nil {
				t.Fatal(err)
			}
		} else {
			target, err := k8s.CurrentProfileTarget()
			if err != nil {
				t.Fatal(err)
			}
			_, rev, _ := store.Read()
			_, err = store.Update(context.Background(), rev, func(file *config.ClusterProfiles) error {
				assignment := config.IntegrationSettings{Target: target.Fingerprint}
				connection := prom.Connection{URL: "https://prom"}
				if scenario == "target" {
					assignment.Target = "old-target"
				} else {
					connection.HeadersFromEnv = map[string]string{"Authorization": "UNSET_PROFILE_STARTUP_TOKEN"}
				}
				assignment.Prometheus = &connection
				file.Profiles[target.Binding] = config.ClusterProfile{Context: "dev", Integrations: map[config.Integration]config.IntegrationSettings{config.IntegrationMetrics: assignment}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		RegisterCallbacks(AppConfig{WorkloadMetricsScope: prom.WorkloadMetricsScope{SingleCluster: true}}, timeline.StoreConfig{})
		if prometheus.GetClient() != nil {
			t.Fatal("unusable profile activated metrics")
		}
		return
	}
	for _, scenario := range []string{"malformed", "target", "environment"} {
		t.Run(scenario, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestUnusableProfileDoesNotPreventStartupWithScope$")
			cmd.Env = append(os.Environ(), "RADAR_PROFILE_STARTUP_CASE="+scenario)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("startup failed: %v\n%s", err, out)
			}
		})
	}
}

func TestPrepareSharedPrometheusKeepsExistingConfiguration(t *testing.T) {
	useTempHome(t)
	t.Cleanup(k8s.SetTestLocalMode())
	t.Setenv(settings.OperatorFileEnv, "")
	t.Setenv("RADAR_PROFILE_TEST_TOKEN", "shared-secret")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Cleanup(func() { settings.SetOperatorConfig(nil) })
	for _, cfg := range []AppConfig{{ListenAddress: "0.0.0.0"}, {CloudTunnelConfigured: true}, {AuthConfig: auth.Config{Mode: "proxy"}}} {
		cfg.PrometheusURL = "https://shared"
		cfg.PrometheusSavedURL = cfg.PrometheusURL
		cfg.PrometheusHeadersFromEnv = map[string]string{"Authorization": "RADAR_PROFILE_TEST_TOKEN"}
		got, err := preparePrometheusConfiguration(cfg)
		if err != nil || got.LocalConnections != nil || got.PrometheusHeaders["Authorization"] != "shared-secret" {
			t.Fatalf("shared configuration changed: %v", err)
		}
	}
}
