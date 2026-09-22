package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadOperatorConfig(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		valid      bool
	}{
		{"default", `{"version":1}`, true},
		{"explicit empty policy", `{"version":1,"audit":{"ignoredNamespaces":[],"disabledChecks":[]},"helmOciSources":[]}`, true},
		{"missing version", `{}`, false},
		{"missing exclusions", `{"version":1,"audit":{"disabledChecks":[]}}`, false},
		{"missing checks", `{"version":1,"audit":{"ignoredNamespaces":[]}}`, false},
		{"null audit arrays", `{"version":1,"audit":{"ignoredNamespaces":null,"disabledChecks":null}}`, false},
		{"unsupported version", `{"version":2}`, false},
		{"unknown field", `{"version":1,"theme":"dark"}`, false},
		{"unknown nested field", `{"version":1,"audit":{"ignoredNamespace":[]}}`, false},
		{"trailing object", `{"version":1}{}`, false},
		{"null", `null`, false},
		{"broken json", `{`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator.json")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadOperatorConfig(path)
			if (err == nil) != tt.valid {
				t.Fatalf("error = %v, valid = %v", err, tt.valid)
			}
		})
	}
	if _, err := ReadOperatorConfig(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestOperatorSnapshotDoesNotRewriteLocalSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Cleanup(func() { SetOperatorConfig(nil) })
	local := Settings{Theme: "dark", Audit: &AuditConfig{IgnoredNamespaces: []string{"local"}}, HelmOCISources: []string{"oci://local/charts"}}
	if err := Save(local); err != nil {
		t.Fatal(err)
	}
	cfg := &OperatorConfig{Version: 1, Audit: &AuditConfig{IgnoredNamespaces: []string{"operator"}}, HelmOCISources: []string{"oci://operator/charts"}}
	SetOperatorConfig(cfg)
	cfg.Audit.IgnoredNamespaces[0] = "mutated"
	cfg.HelmOCISources[0] = "mutated"
	if got := EffectiveAudit().IgnoredNamespaces; !reflect.DeepEqual(got, []string{"operator"}) {
		t.Fatal(got)
	}
	if got := EffectiveOCISources(); !reflect.DeepEqual(got, []string{"oci://operator/charts"}) {
		t.Fatal(got)
	}
	EffectiveOCISources()[0] = "also mutated"
	EffectiveAudit().IgnoredNamespaces[0] = "also mutated"
	if _, err := Update(func(s *Settings) { s.Theme = "light" }); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.Audit.IgnoredNamespaces[0] != "local" || got.HelmOCISources[0] != "oci://local/charts" {
		t.Fatalf("operator state persisted: %+v", got)
	}
	SetOperatorConfig(&OperatorConfig{Version: 1})
	if !reflect.DeepEqual(EffectiveAudit(), DefaultAuditConfig()) || len(EffectiveOCISources()) != 0 {
		t.Fatal("operator defaults inherited local settings")
	}
	SetOperatorConfig(nil)
	if EffectiveAudit().IgnoredNamespaces[0] != "local" {
		t.Fatal("local configuration lost")
	}
}
