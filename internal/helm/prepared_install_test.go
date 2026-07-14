package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/repo"
)

func chartVersion(version string) *repo.ChartVersion {
	return &repo.ChartVersion{
		Metadata: &chart.Metadata{Name: "radar", Version: version},
		URLs:     []string{"radar-" + version + ".tgz"},
	}
}

func TestSelectStableChartVersion(t *testing.T) {
	versions := repo.ChartVersions{
		chartVersion("1.7.0-beta.1"),
		chartVersion("1.5.4"),
		chartVersion("1.6.0"),
		chartVersion("1.5.3"),
	}
	selected, exact, err := selectStableChartVersion(versions, "", "1.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if exact != "1.6.0" || selected.Version != "1.6.0" {
		t.Fatalf("latest stable = %q, want 1.6.0", exact)
	}

	selected, exact, err = selectStableChartVersion(versions, "1.5.4", "1.5.4")
	if err != nil || selected.Version != "1.5.4" || exact != "1.5.4" {
		t.Fatalf("exact stable selection = %q, %v", exact, err)
	}
}

func TestSelectStableChartVersion_RejectsPrereleaseOldAndMissing(t *testing.T) {
	versions := repo.ChartVersions{chartVersion("1.7.0-beta.1"), chartVersion("1.5.3")}
	tests := []struct {
		name      string
		requested string
	}{
		{"prerelease", "1.7.0-beta.1"},
		{"too-old", "1.5.3"},
		{"missing", "9.9.9"},
		{"latest-has-no-eligible-version", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := selectStableChartVersion(versions, tc.requested, "1.5.4")
			var versionErr *ChartVersionError
			if !errors.As(err, &versionErr) {
				t.Fatalf("expected ChartVersionError, got %T: %v", err, err)
			}
		})
	}
}

func TestServerDryRunFindsActualCloudDeployment(t *testing.T) {
	loaded, err := loader.Load("../../deploy/helm/radar")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"cloud": map[string]any{
			"enabled": true, "url": "wss://preflight.invalid/agent",
			"clusterName": "preflight-cluster-id", "existingSecret": "radar-cloud-config",
		},
		"auth": map[string]any{"mode": "proxy"},
		"rbac": map[string]any{
			"helm": true, "secrets": true, "podExec": true,
			"portForward": true, "metrics": true, "selfUpgrade": true,
		},
	}
	req := &InstallRequest{
		ReleaseName: "custom", Namespace: "observability", ChartName: "radar",
		Version: loaded.Metadata.Version, Repository: "unused",
		Values: values, CreateNamespace: true,
	}
	rel, err := runServerDryRun(context.Background(), memoryActionConfig(t), req, loaded)
	if err != nil {
		t.Fatalf("server dry-run: %v", err)
	}
	ref, err := cloudDeploymentRef(rel.Manifest, req.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "custom-radar" || ref.Namespace != "observability" {
		t.Fatalf("deployment = %+v, want observability/custom-radar", ref)
	}
	if !strings.Contains(ref.Selector, "app.kubernetes.io/instance=custom") || !strings.Contains(ref.Selector, "app.kubernetes.io/name=radar") {
		t.Fatalf("selector = %q", ref.Selector)
	}
	prepared := &PreparedInstall{manifest: rel.Manifest}
	if prepared.TargetManifest() != rel.Manifest {
		t.Fatal("prepared install did not expose the pinned rendered manifest")
	}
}

func TestCloudDeploymentRefRejectsAmbiguousManifest(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: one
spec:
  selector:
    matchLabels: {app: radar}
  template:
    spec:
      containers:
      - name: radar
        env:
        - {name: RADAR_CLOUD_MODE, value: "true"}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: two
spec:
  selector:
    matchLabels: {app: radar-two}
  template:
    spec:
      containers:
      - name: radar
        env:
        - {name: RADAR_CLOUD_MODE, value: "true"}
`
	if _, err := cloudDeploymentRef(manifest, "radar"); err == nil {
		t.Fatal("expected ambiguous cloud Deployments to fail")
	}
}

func TestResolvePreparedChartPinsExactArchiveAndDigest(t *testing.T) {
	chartObject := &chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "radar", Version: "1.6.0", Type: "application"},
		Templates: []*chart.File{{Name: "templates/deployment.yaml", Data: []byte(`apiVersion: apps/v1
kind: Deployment
metadata: {name: radar}
spec:
  selector: {matchLabels: {app: radar}}
  template:
    spec:
      containers:
      - name: radar
        env:
        - {name: RADAR_CLOUD_MODE, value: "true"}
`)}},
	}
	archivePath, err := chartutil.Save(chartObject, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	validIndex := fmt.Sprintf(`apiVersion: v1
entries:
  radar:
  - apiVersion: v2
    name: radar
    version: 1.6.0
    urls: [radar-1.6.0.tgz]
    digest: %s
`, hex.EncodeToString(digest[:]))
	index := validIndex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = w.Write([]byte(index))
		case "/radar-1.6.0.tgz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	loaded, exact, err := (&Client{}).resolvePreparedChart(context.Background(), &InstallRequest{
		ChartName: "radar", Repository: server.URL,
	}, "1.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if exact != "1.6.0" || loaded.Metadata.Version != "1.6.0" {
		t.Fatalf("resolved %q / loaded %q", exact, loaded.Metadata.Version)
	}

	index = strings.Replace(validIndex, fmt.Sprintf("    digest: %s\n", hex.EncodeToString(digest[:])), "", 1)
	_, _, err = (&Client{}).resolvePreparedChart(context.Background(), &InstallRequest{
		ChartName: "radar", Repository: server.URL,
	}, "1.5.4")
	if err == nil || !strings.Contains(err.Error(), "has no SHA-256 digest") {
		t.Fatalf("missing repository digest error = %v", err)
	}

	index = strings.Replace(validIndex, hex.EncodeToString(digest[:]), "sha512:deadbeef", 1)
	_, _, err = (&Client{}).resolvePreparedChart(context.Background(), &InstallRequest{
		ChartName: "radar", Repository: server.URL,
	}, "1.5.4")
	if err == nil || !strings.Contains(err.Error(), "has an invalid SHA-256 digest") {
		t.Fatalf("malformed repository digest error = %v", err)
	}
}

func TestResolvePreparedChartHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := (&Client{}).resolvePreparedChart(ctx, &InstallRequest{
		ChartName: "radar", Repository: "https://example.invalid/charts",
	}, "1.5.4")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
