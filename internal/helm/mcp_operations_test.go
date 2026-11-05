package helm

import (
	"helm.sh/helm/v3/pkg/release"
	"net/http"
	"net/http/httptest"
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
			_, err := c.installWithPolicy(cfg, &InstallRequest{ReleaseName: "demo", Namespace: "default", ChartName: "radar", Repository: srv.URL, Version: "1.0.0"}, true)
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
	_, err := c.installWithPolicy(memoryActionConfig(t), &InstallRequest{ReleaseName: "demo", Namespace: "default", ChartName: "radar", Repository: srv.URL, Version: "1.0.0"}, true)
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
