package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/settings"
)

func TestLoadOperatorSettings(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", `{"version":1}`, true},
		{"audit", `{"version":1,"audit":{"ignoredNamespaces":["*-system","demo-*","default","*"],"disabledChecks":[]}}`, true},
		{"unknown check", `{"version":1,"audit":{"ignoredNamespaces":[],"disabledChecks":["not-a-check"]}}`, false},
		{"bad namespace", `{"version":1,"audit":{"ignoredNamespaces":["bad/name"],"disabledChecks":[]}}`, false},
		{"unsupported glob", `{"version":1,"audit":{"ignoredNamespaces":["foo*bar"],"disabledChecks":[]}}`, false},
		{"oci", `{"version":1,"helmOciSources":["oci://ghcr.io/example/charts/"]}`, true},
		{"invalid oci", `{"version":1,"helmOciSources":["https://ghcr.io"]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings.SetOperatorConfig(nil)
			t.Cleanup(func() { settings.SetOperatorConfig(nil) })
			path := filepath.Join(t.TempDir(), "operator.json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(settings.OperatorFileEnv, path)
			err := loadOperatorSettings(AppConfig{})
			if (err == nil) != tt.valid {
				t.Fatalf("error = %v, valid = %v", err, tt.valid)
			}
			if settings.OperatorConfigured() != tt.valid {
				t.Fatal("invalid settings partially applied")
			}
		})
	}
}

func TestOperatorDefaultsWithoutFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv(settings.OperatorFileEnv, "")
	t.Setenv("RADAR_CLOUD_MODE", "false")
	t.Cleanup(func() { settings.SetOperatorConfig(nil) })
	legacy := settings.Settings{Audit: &settings.AuditConfig{IgnoredNamespaces: []string{"legacy"}}, HelmOCISources: []string{"oci://legacy/charts"}}
	if err := settings.Save(legacy); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, authMode, pod, listen string
		cloud, operator             bool
	}{
		{name: "local", listen: server.DefaultListenAddress},
		{name: "authenticated", authMode: "proxy", operator: true},
		{name: "raw deployment", pod: "10.0.0.1", listen: server.AllInterfacesAddress, operator: true},
		{name: "dev Pod", pod: "10.0.0.1", listen: server.DefaultListenAddress},
		{name: "Cloud", authMode: "proxy", pod: "10.0.0.1", cloud: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("KUBERNETES_SERVICE_HOST", tt.pod)
			if err := loadOperatorSettings(AppConfig{AuthConfig: auth.Config{Mode: tt.authMode}, ListenAddress: tt.listen, CloudTunnelConfigured: tt.cloud}); err != nil {
				t.Fatal(err)
			}
			wantAudit, wantSources := *legacy.Audit, legacy.HelmOCISources
			if tt.operator {
				wantAudit, wantSources = settings.DefaultAuditConfig(), []string{}
			}
			if !reflect.DeepEqual(settings.EffectiveAudit(), wantAudit) || !reflect.DeepEqual(settings.EffectiveOCISources(), wantSources) {
				t.Fatalf("unexpected effective policy: %+v / %v", settings.EffectiveAudit(), settings.EffectiveOCISources())
			}
			if got := settings.Load(); !reflect.DeepEqual(got.Audit, legacy.Audit) || !reflect.DeepEqual(got.HelmOCISources, legacy.HelmOCISources) {
				t.Fatal("startup changed the local settings file")
			}
		})
	}
}

func TestCloudDoesNotLoadOperatorSettings(t *testing.T) {
	t.Setenv(settings.OperatorFileEnv, "/does-not-exist/operator.json")
	t.Setenv("RADAR_CLOUD_MODE", "false")
	if err := loadOperatorSettings(AppConfig{CloudTunnelConfigured: true}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_CLOUD_MODE", "true")
	if err := loadOperatorSettings(AppConfig{}); err != nil {
		t.Fatal(err)
	}
	if settings.OperatorConfigured() {
		t.Fatal("Cloud acquired an operator overlay")
	}
}
