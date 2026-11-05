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
