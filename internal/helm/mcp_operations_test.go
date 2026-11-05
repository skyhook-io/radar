package helm

import (
	"fmt"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMCPFreshInstallRefusesHistoryBeforeFetching(t *testing.T) {
	for _, status := range []release.Status{release.StatusFailed, release.StatusSuperseded, release.StatusUninstalled, release.StatusDeployed, release.StatusPendingInstall} {
		t.Run(string(status), func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
			defer srv.Close()
			cfg := memoryActionConfig(t)
			seedRelease(t, cfg, "demo", status, 1)
			c := newChartSourceTestClient(t)
			_, err := c.installFreshWith(cfg, &InstallRequest{ReleaseName: "demo", Namespace: "default", ChartName: "radar", Repository: srv.URL, Version: "1.0.0"})
			if err == nil || hits != 0 {
				t.Fatalf("history recovered or source fetched: err=%v hits=%d", err, hits)
			}
			rel, _ := cfg.Releases.Last("demo")
			if rel.Version != 1 {
				t.Fatal("release mutated")
			}
		})
	}
}
func TestMCPInstallRefusesUnconfiguredSourceBeforeFetch(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close()
	c := newChartSourceTestClient(t)
	_, err := c.installFreshWith(memoryActionConfig(t), &InstallRequest{ReleaseName: "demo", Namespace: "default", ChartName: "radar", Repository: srv.URL, Version: "1.0.0"})
	if err == nil || hits != 0 {
		t.Fatalf("unconfigured source fetched: %v hits=%d", err, hits)
	}
}

func TestMCPOverridesRefuseHiddenContainerReplacement(t *testing.T) {
	stored := map[string]any{"auth": map[string]any{"password": "secret", "user": "before"}, "credentials": []any{map[string]any{"password": "secret"}}, "ports": []any{80}}
	for _, tc := range []struct {
		name    string
		values  map[string]any
		refused bool
	}{
		{"nested map preserves password", map[string]any{"auth": map[string]any{"user": "after"}}, false},
		{"empty preserves", map[string]any{}, false},
		{"null hidden object", map[string]any{"auth": nil}, true},
		{"scalar hidden object", map[string]any{"auth": false}, true},
		{"replace hidden list", map[string]any{"credentials": []any{map[string]any{"user": "new"}}}, true},
		{"replace public list", map[string]any{"ports": []any{443}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOverrideReplacement(stored, tc.values)
			if (err != nil) != tc.refused {
				t.Fatalf("refused=%v err=%v", tc.refused, err)
			}
			if stored["auth"].(map[string]any)["password"] != "secret" {
				t.Fatal("validation mutated original values")
			}
		})
	}
}

func TestMCPFreshInstallLoadsColdAuthenticatedSourceAndValidatesMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, chartName, version string
		refused                  bool
	}{
		{"cold authenticated repository", "radar", "1.0.0", false},
		{"wrong archive name", "other", "1.0.0", true},
		{"wrong archive version", "radar", "2.0.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archivePath, err := chartutil.Save(&chart.Chart{Metadata: &chart.Metadata{Name: tc.chartName, Version: tc.version, APIVersion: "v2"}}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			archive, err := os.ReadFile(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			indexHits, chartHits := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "reader" || password != "fixture-password" {
					t.Error("configured repository credentials not forwarded")
					http.Error(w, "unauthorized", 401)
					return
				}
				if r.URL.Path == "/index.yaml" {
					indexHits++
					fmt.Fprint(w, "apiVersion: v1\nentries:\n  radar:\n  - name: radar\n    version: 1.0.0\n    urls: [chart.tgz]\n")
					return
				}
				if r.URL.Path == "/chart.tgz" {
					chartHits++
					w.Write(archive)
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			client := newChartSourceTestClient(t)
			config := &repo.File{Repositories: []*repo.Entry{{Name: "fixture", URL: server.URL, Username: "reader", Password: "fixture-password"}}}
			if err := config.WriteFile(client.settings.RepositoryConfig, 0600); err != nil {
				t.Fatal(err)
			}
			cfg := memoryActionConfig(t)
			result, err := client.installFreshWith(cfg, &InstallRequest{ReleaseName: "demo", Namespace: "default", ChartName: "radar", Repository: "fixture", Version: "1.0.0"})
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("metadata mismatch accepted: %+v %v", result, err)
				}
				if _, err := cfg.Releases.Last("demo"); err == nil {
					t.Fatal("mismatched chart mutated release storage")
				}
			} else if err != nil || result == nil {
				t.Fatalf("cold source install failed: %+v %v", result, err)
			}
			if indexHits != 1 || chartHits != 1 {
				t.Fatalf("expected fresh authenticated index and chart: %d/%d", indexHits, chartHits)
			}
		})
	}
}

func TestMCPOverridesValidateDeployedBaselineAfterFailure(t *testing.T) {
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
	deployed, _ := cfg.Releases.Get("demo", 1)
	deployed.Config = map[string]any{"auth": map[string]any{"password": "secret"}}
	if err := cfg.Releases.Update(deployed); err != nil {
		t.Fatal(err)
	}
	seedRelease(t, cfg, "demo", release.StatusFailed, 2)
	failed, _ := cfg.Releases.Get("demo", 2)
	failed.Config = map[string]any{}
	if err := cfg.Releases.Update(failed); err != nil {
		t.Fatal(err)
	}
	client := newChartSourceTestClient(t)
	err := client.upgradeWithValuesMode(cfg, "demo", "1.0.0", "", map[string]any{"auth": nil}, false, func(string, string, string) {})
	if err == nil || !strings.Contains(err.Error(), "hidden credentials") {
		t.Fatalf("deployed baseline guard missed: %v", err)
	}
	latest, _ := cfg.Releases.Last("demo")
	if latest.Version != 2 {
		t.Fatal("refused upgrade mutated history")
	}
}
