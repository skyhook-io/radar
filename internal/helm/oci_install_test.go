package helm

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

type chartLocationCall struct {
	chartURL string
	version  string
}

func ociInstallTestClient(t *testing.T, locate func(*action.Configuration, string, string) (string, error)) *Client {
	t.Helper()
	dir := t.TempDir()
	settings := cli.New()
	settings.RepositoryConfig = filepath.Join(dir, "repositories.yaml")
	settings.RepositoryCache = filepath.Join(dir, "repository")
	settings.RegistryConfig = filepath.Join(dir, "registry.json")
	if err := os.MkdirAll(settings.RepositoryCache, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{settings: settings, chartPathLocator: locate}
}

func ociInstallTestChart(t *testing.T, name, version string) string {
	t.Helper()
	archive, err := chartutil.Save(&chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion: "v2",
			Name:       name,
			Version:    version,
			AppVersion: "2026.9",
			Type:       "application",
		},
		Files:  []*chart.File{{Name: "README.md", Data: []byte("OCI chart")}},
		Values: map[string]any{"replicas": 2},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestOCIChartPullClientDoesNotUseProbeTimeout(t *testing.T) {
	client := ociInstallTestClient(t, nil)

	probeClient, err := client.newRegistryClientConcrete()
	if err != nil {
		t.Fatalf("newRegistryClientConcrete: %v", err)
	}
	pullClient, err := client.newRegistryClientForChartPull()
	if err != nil {
		t.Fatalf("newRegistryClientForChartPull: %v", err)
	}

	if got := registryClientHTTPTimeout(t, probeClient); got != ociProbeTimeout {
		t.Fatalf("probe client timeout = %s, want %s", got, ociProbeTimeout)
	}
	if got := registryClientHTTPTimeout(t, pullClient); got != 0 {
		t.Fatalf("chart pull client timeout = %s, want no overall timeout", got)
	}
}

func registryClientHTTPTimeout(t *testing.T, client *registry.Client) time.Duration {
	t.Helper()
	httpClient := reflect.ValueOf(client).Elem().FieldByName("httpClient")
	if !httpClient.IsValid() || httpClient.IsNil() {
		t.Fatal("helm registry client has no HTTP client")
	}
	timeout := httpClient.Elem().FieldByName("Timeout")
	if !timeout.IsValid() {
		t.Fatal("helm registry HTTP client has no timeout field")
	}
	return time.Duration(timeout.Int())
}

func TestResolveOCIChartURLPreservesCompleteReferences(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "tag", source: "oci://registry.example/acme/charts/widget:1.2.3"},
		{name: "digest", source: "oci://registry.example/acme/charts/widget@sha256:d234555386402a5867ef0169fefe5486858b6d8d209eaf32fd26d29b16807fd6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveOCIChartURL(tt.source, "widget")
			if err != nil {
				t.Fatalf("resolveOCIChartURL: %v", err)
			}
			if got != tt.source {
				t.Fatalf("resolved URL = %q, want %q", got, tt.source)
			}
		})
	}
}

func TestInstallWithCompleteOCIReferencePreservesExactVersion(t *testing.T) {
	tests := []struct {
		name       string
		repository string
	}{
		{name: "tag", repository: "oci://registry.example/acme/charts/widget:1.2.3"},
		{name: "digest", repository: "oci://registry.example/acme/charts/widget@sha256:d234555386402a5867ef0169fefe5486858b6d8d209eaf32fd26d29b16807fd6"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := ociInstallTestChart(t, "widget", "1.2.3")
			var call chartLocationCall
			client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
				call = chartLocationCall{chartURL: chartURL, version: version}
				return archive, nil
			})
			req := &InstallRequest{
				ReleaseName: "widget", Namespace: "default", ChartName: "widget",
				Version: "1.2.3", Repository: tt.repository,
			}

			if _, err := client.installWith(memoryActionConfig(t), req); err != nil {
				t.Fatalf("installWith: %v", err)
			}
			if call != (chartLocationCall{chartURL: tt.repository, version: req.Version}) {
				t.Fatalf("location call = %+v", call)
			}
		})
	}
}

func TestGetChartDetailDirectOCI(t *testing.T) {
	archive := ociInstallTestChart(t, "widget", "1.2.3")
	var calls []chartLocationCall
	client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
		calls = append(calls, chartLocationCall{chartURL: chartURL, version: version})
		return archive, nil
	})

	detail, err := client.GetChartDetail("oci://registry.example/acme/charts", "widget", "1.2.3")
	if err != nil {
		t.Fatalf("GetChartDetail: %v", err)
	}
	if len(calls) != 1 || calls[0] != (chartLocationCall{chartURL: "oci://registry.example/acme/charts/widget", version: "1.2.3"}) {
		t.Fatalf("location calls = %+v", calls)
	}
	if detail.Name != "widget" || detail.Version != "1.2.3" || detail.Readme != "OCI chart" {
		t.Fatalf("unexpected detail: %+v", detail)
	}
}

func TestGetChartDetailRepositoryIndexOCIURL(t *testing.T) {
	archive := ociInstallTestChart(t, "widget", "2.4.6")
	var calls []chartLocationCall
	client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
		calls = append(calls, chartLocationCall{chartURL: chartURL, version: version})
		return archive, nil
	})

	repositories := repo.NewFile()
	repositories.Update(&repo.Entry{Name: "classic", URL: "https://charts.example.test"})
	if err := repositories.WriteFile(client.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	index := `apiVersion: v1
entries:
  widget:
  - apiVersion: v2
    name: widget
    version: 2.4.6
    urls:
    - oci://registry.example/acme/charts/widget
`
	if err := os.WriteFile(filepath.Join(client.settings.RepositoryCache, "classic-index.yaml"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	detail, err := client.GetChartDetail("classic", "widget", "2.4.6")
	if err != nil {
		t.Fatalf("GetChartDetail: %v", err)
	}
	if len(calls) != 1 || calls[0] != (chartLocationCall{chartURL: "oci://registry.example/acme/charts/widget", version: "2.4.6"}) {
		t.Fatalf("location calls = %+v", calls)
	}
	if detail.Version != "2.4.6" {
		t.Fatalf("version = %q", detail.Version)
	}
}

func TestInstallWithDirectOCI(t *testing.T) {
	archive := ociInstallTestChart(t, "widget", "3.1.4")
	var call chartLocationCall
	client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
		call = chartLocationCall{chartURL: chartURL, version: version}
		return archive, nil
	})

	req := &InstallRequest{ReleaseName: "widget", Namespace: "default", ChartName: "widget", Version: "3.1.4", Repository: "oci://registry.example/acme/charts/widget"}
	installed, err := client.installWith(memoryActionConfig(t), req)
	if err != nil {
		t.Fatalf("installWith: %v", err)
	}
	if installed == nil || installed.Name != "widget" {
		t.Fatalf("unexpected installed release: %+v", installed)
	}
	if call != (chartLocationCall{chartURL: req.Repository, version: req.Version}) {
		t.Fatalf("location call = %+v", call)
	}
}

func TestInstallWithProgressDirectOCI(t *testing.T) {
	archive := ociInstallTestChart(t, "widget", "4.0.1")
	var call chartLocationCall
	client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
		call = chartLocationCall{chartURL: chartURL, version: version}
		return archive, nil
	})

	req := &InstallRequest{ReleaseName: "widget", Namespace: "default", ChartName: "widget", Version: "4.0.1", Repository: "oci://registry.example/acme/charts"}
	progress := make(chan InstallProgress, 16)
	installed, err := client.installWithProgressUsing(memoryActionConfig(t), req, progress)
	if err != nil {
		t.Fatalf("installWithProgressUsing: %v", err)
	}
	if installed == nil || installed.Name != "widget" {
		t.Fatalf("unexpected installed release: %+v", installed)
	}
	if call != (chartLocationCall{chartURL: req.Repository + "/widget", version: req.Version}) {
		t.Fatalf("location call = %+v", call)
	}
	if len(progress) == 0 {
		t.Fatal("expected progress events")
	}
}

func TestInstallWithOCIPropagatesLocateError(t *testing.T) {
	want := errors.New("registry unavailable")
	client := ociInstallTestClient(t, func(_ *action.Configuration, _, _ string) (string, error) {
		return "", want
	})
	req := &InstallRequest{ReleaseName: "widget", Namespace: "default", ChartName: "widget", Version: "5.0.0", Repository: "oci://registry.example/acme/charts"}

	_, err := client.installWith(memoryActionConfig(t), req)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "failed to locate chart") {
		t.Fatalf("error = %v, want wrapped registry error", err)
	}
}

func TestRejectedOCIInstallDoesNotChangeSourceInventory(t *testing.T) {
	before := ListOCISources()
	called := false
	client := ociInstallTestClient(t, func(_ *action.Configuration, _, _ string) (string, error) {
		called = true
		return "", errors.New("must not locate")
	})
	cfg := memoryActionConfig(t)
	seedRelease(t, cfg, "widget", release.StatusDeployed, 1)
	req := &InstallRequest{ReleaseName: "widget", Namespace: "default", ChartName: "widget", Version: "6.0.0", Repository: "oci://registry.example/acme/charts"}

	if _, err := client.installWith(cfg, req); err == nil {
		t.Fatal("expected pre-install rejection")
	}
	if called {
		t.Fatal("chart lookup ran before pre-install rejection")
	}
	if after := ListOCISources(); !reflect.DeepEqual(after, before) {
		t.Fatalf("OCI source inventory changed: before=%v after=%v", before, after)
	}
}
