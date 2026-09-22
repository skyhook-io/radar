package helm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
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

func TestLatestOCIChartVersionAcrossCallers(t *testing.T) {
	for _, caller := range []string{"detail", "install", "progress"} {
		t.Run(caller, func(t *testing.T) {
			archive := ociInstallTestChart(t, "widget", "1.3.0")
			var calls []chartLocationCall
			client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
				calls = append(calls, chartLocationCall{chartURL: chartURL, version: version})
				return archive, nil
			})
			source := "oci://registry.example/acme/charts"
			req := &InstallRequest{ReleaseName: "widget", Namespace: "default", ChartName: "widget", Repository: source, Version: "latest"}
			var err error
			switch caller {
			case "detail":
				_, err = client.GetChartDetail(source, "widget", "latest")
			case "install":
				_, err = client.installWith(memoryActionConfig(t), req)
			case "progress":
				_, err = client.installWithProgressUsing(memoryActionConfig(t), req, make(chan InstallProgress, 16))
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []chartLocationCall{{chartURL: source + "/widget", version: ""}}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %+v, want %+v", calls, want)
			}
		})
	}
}

func TestLocateChartPathLatestNormalization(t *testing.T) {
	for _, tc := range []struct{ name, ref, version, want string }{
		{"oci latest", "oci://registry.example/charts/widget", "latest", ""},
		{"oci explicit", "oci://registry.example/charts/widget", "1.2.3", "1.2.3"},
		{"oci empty", "oci://registry.example/charts/widget", "", ""},
		{"embedded tag", "oci://registry.example/charts/widget:1.2.3", "latest", ""},
		{"embedded digest", "oci://registry.example/charts/widget@sha256:d234555386402a5867ef0169fefe5486858b6d8d209eaf32fd26d29b16807fd6", "latest", ""},
		{"http unchanged", "https://charts.example/widget.tgz", "latest", "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got chartLocationCall
			client := ociInstallTestClient(t, func(_ *action.Configuration, chartURL, version string) (string, error) {
				got = chartLocationCall{chartURL: chartURL, version: version}
				return "chart.tgz", nil
			})
			if _, err := client.locateChartPath(nil, tc.ref, tc.version); err != nil {
				t.Fatal(err)
			}
			if got != (chartLocationCall{chartURL: tc.ref, version: tc.want}) {
				t.Fatalf("call = %+v", got)
			}
		})
	}
}

func TestLocateLatestOCIChartFromRegistry(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	blobs, manifests, digests := map[string][]byte{}, map[string][]byte{}, map[string]string{}
	for _, version := range []string{"1.2.3", "1.3.0", "1.4.0-beta.1"} {
		archive, err := os.ReadFile(ociInstallTestChart(t, "widget", version))
		if err != nil {
			t.Fatal(err)
		}
		config, err := json.Marshal(map[string]string{"apiVersion": "v2", "name": "widget", "version": version})
		if err != nil {
			t.Fatal(err)
		}
		configDigest, chartDigest := digest.FromBytes(config).String(), digest.FromBytes(archive).String()
		blobs[configDigest], blobs[chartDigest] = config, archive
		manifest, err := json.Marshal(map[string]any{
			"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]any{"mediaType": registry.ConfigMediaType, "digest": configDigest, "size": len(config)},
			"layers": []any{map[string]any{"mediaType": registry.ChartLayerMediaType, "digest": chartDigest, "size": len(archive)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		manifestDigest := digest.FromBytes(manifest).String()
		manifests[version], manifests[manifestDigest], digests[version] = manifest, manifest, manifestDigest
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/charts/widget/tags/list" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"name":"charts/widget","tags":["1.2.3","1.3.0","1.4.0-beta.1"]}`)
			return
		}
		var data []byte
		if strings.HasPrefix(r.URL.Path, "/v2/charts/widget/manifests/") {
			data = manifests[strings.TrimPrefix(r.URL.Path, "/v2/charts/widget/manifests/")]
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		} else if strings.HasPrefix(r.URL.Path, "/v2/charts/widget/blobs/") {
			data = blobs[strings.TrimPrefix(r.URL.Path, "/v2/charts/widget/blobs/")]
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Docker-Content-Digest", digest.FromBytes(data).String())
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}))
	t.Cleanup(server.Close)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	ref := "oci://" + strings.TrimPrefix(server.URL, "https://") + "/charts/widget"
	for _, tc := range []struct{ name, ref, version, want string }{
		{"latest stable", ref, "latest", "1.3.0"},
		{"explicit version", ref, "1.2.3", "1.2.3"},
		{"embedded tag", ref + ":1.2.3", "latest", "1.2.3"},
		{"embedded digest", ref + "@" + digests["1.2.3"], "latest", "1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := ociInstallTestClient(t, nil)
			path, err := client.locateChartPath(memoryActionConfig(t), tc.ref, tc.version)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := loader.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Metadata.Version != tc.want {
				t.Fatalf("version = %q, want %q", loaded.Metadata.Version, tc.want)
			}
		})
	}
}
