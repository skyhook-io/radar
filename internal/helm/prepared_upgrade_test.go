package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
)

func TestInspectCloudReleaseWithClassifiesEnrollmentStates(t *testing.T) {
	tests := []struct {
		name   string
		status release.Status
		want   CloudReleaseState
	}{
		{name: "deployed", status: release.StatusDeployed, want: CloudReleaseDeployed},
		{name: "pending", status: release.StatusPendingUpgrade, want: CloudReleasePending},
		{name: "failed history", status: release.StatusFailed, want: CloudReleaseHistory},
		{name: "uninstalled history", status: release.StatusUninstalled, want: CloudReleaseHistory},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := memoryActionConfig(t)
			seedRelease(t, cfg, "radar", tc.status, 4)
			got, err := inspectCloudReleaseWith(cfg, "radar")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.want || got.Status != tc.status.String() || got.Revision != 4 {
				t.Fatalf("inspection = %+v, want state %s/status %s/revision 4", got, tc.want, tc.status)
			}
		})
	}

	got, err := inspectCloudReleaseWith(memoryActionConfig(t), "radar")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != CloudReleaseNone || got.Revision != 0 {
		t.Fatalf("missing inspection = %+v, want none", got)
	}
}

func TestCloudUpgradeValuesClearsOnlyImageTag(t *testing.T) {
	input := map[string]any{
		"image": map[string]any{"repository": "mirror.example/radar", "tag": "old-pin"},
		"rbac":  map[string]any{"selfUpgrade": false},
	}
	got := cloudUpgradeValues(input)
	image := got["image"].(map[string]any)
	if image["tag"] != "" || image["repository"] != "mirror.example/radar" {
		t.Fatalf("image override = %#v", image)
	}
	if got["rbac"].(map[string]any)["selfUpgrade"] != false {
		t.Fatalf("unrelated values changed: %#v", got)
	}
	if input["image"].(map[string]any)["tag"] != "old-pin" {
		t.Fatal("cloudUpgradeValues mutated its input")
	}
}

func TestRejectChartDowngrade(t *testing.T) {
	if err := RejectChartDowngrade("1.6.0", "1.6.0"); err != nil {
		t.Fatalf("same version rejected: %v", err)
	}
	if err := RejectChartDowngrade("1.6.0", "1.7.0"); err != nil {
		t.Fatalf("upgrade rejected: %v", err)
	}
	err := RejectChartDowngrade("1.7.0", "1.6.0")
	if _, ok := err.(*ChartDowngradeError); !ok {
		t.Fatalf("downgrade error = %T: %v", err, err)
	}
}

func TestPreCloudDeploymentRefDoesNotRequireCloudMode(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: custom-radar
  namespace: observability
  labels:
    helm.sh/chart: radar-1.4.0
    app.kubernetes.io/instance: custom
spec:
  selector:
    matchLabels: {app.kubernetes.io/name: radar, app.kubernetes.io/instance: custom}
  template:
    spec:
      containers:
      - name: radar
        image: ghcr.io/skyhook-io/radar:1.4.2
`
	ref, err := preCloudDeploymentRef(manifest, "fallback", "custom")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "custom-radar" || ref.Namespace != "observability" {
		t.Fatalf("deployment = %+v", ref)
	}
	if _, err := cloudDeploymentRef(manifest, "fallback"); err == nil {
		t.Fatal("pre-Cloud deployment unexpectedly matched Cloud finder")
	}
}

func TestSummarizeCloudUpgradeValuesUsesEffectiveChartDefaults(t *testing.T) {
	loaded, err := loader.Load("../../deploy/helm/radar")
	if err != nil {
		t.Fatal(err)
	}
	rel := &release.Release{
		Chart: loaded,
		Config: map[string]any{
			"auth":  map[string]any{"mode": "oidc"},
			"image": map[string]any{"tag": "1.6.1-custom"},
			"rbac":  map[string]any{"selfUpgrade": true},
		},
	}
	summary, err := summarizeCloudUpgradeValues(rel)
	if err != nil {
		t.Fatal(err)
	}
	if summary.AuthMode != "oidc" || summary.ImageTag != "1.6.1-custom" || summary.EffectiveImageTag != "1.6.1-custom" {
		t.Fatalf("summary = %+v", summary)
	}
	if !summary.RBAC["selfUpgrade"] || !summary.RBAC["podLogs"] || summary.RBAC["helm"] {
		t.Fatalf("effective RBAC = %#v", summary.RBAC)
	}

	summary.RBAC["podLogs"] = false
	second, err := summarizeCloudUpgradeValues(rel)
	if err != nil || !second.RBAC["podLogs"] {
		t.Fatal("summary mutation leaked into release values")
	}
}

func TestValidateOfficialRadarChart(t *testing.T) {
	official := &chart.Chart{Metadata: &chart.Metadata{
		Name: "radar", Version: "1.7.0", Sources: []string{"https://github.com/skyhook-io/radar"},
	}}
	if err := validateOfficialRadarChart("radar", "radar", official); err != nil {
		t.Fatal(err)
	}
	lookalike := &chart.Chart{Metadata: &chart.Metadata{
		Name: "radar", Version: "1.7.0", Sources: []string{"https://example.com/lookalike"},
	}}
	err := validateOfficialRadarChart("radar", "radar", lookalike)
	if _, ok := err.(*NotOfficialRadarReleaseError); !ok {
		t.Fatalf("lookalike error = %T: %v", err, err)
	}
}

func TestDeployedCloudDisabledRequiresSafeRollbackState(t *testing.T) {
	loaded, err := loader.Load("../../deploy/helm/radar")
	if err != nil {
		t.Fatal(err)
	}
	rel := &release.Release{
		Chart: loaded, Config: map[string]any{},
		Info: &release.Info{Status: release.StatusDeployed},
	}
	safe, err := deployedCloudDisabled(rel)
	if err != nil || !safe {
		t.Fatalf("standalone deployed release safe = %v, %v", safe, err)
	}
	rel.Config = map[string]any{"cloud": map[string]any{"enabled": true, "existingSecret": "radar-cloud-config"}}
	safe, err = deployedCloudDisabled(rel)
	if err != nil || safe {
		t.Fatalf("Cloud deployed release safe = %v, %v", safe, err)
	}
	rel.Info.Status = release.StatusFailed
	rel.Config = map[string]any{}
	safe, err = deployedCloudDisabled(rel)
	if err != nil || safe {
		t.Fatalf("failed release safe = %v, %v", safe, err)
	}
}

func TestPreparedReleaseIdentityRollbackMatchIgnoresOnlyRevision(t *testing.T) {
	original := preparedReleaseIdentity{
		revision:     4,
		status:       release.StatusDeployed,
		chartName:    "radar",
		chartVersion: "1.5.4",
		appVersion:   "1.6.1",
		manifestHash: sha256.Sum256([]byte("manifest")),
		configHash:   sha256.Sum256([]byte("config")),
	}
	rolledBack := original
	rolledBack.revision = 6
	if !original.sameReleaseContent(rolledBack) {
		t.Fatal("exact rollback content at a later Helm revision was rejected")
	}
	changed := rolledBack
	changed.configHash = sha256.Sum256([]byte("changed"))
	if original.sameReleaseContent(changed) {
		t.Fatal("different rollback values were accepted")
	}
	changed = rolledBack
	changed.status = release.StatusFailed
	if original.sameReleaseContent(changed) {
		t.Fatal("non-deployed rollback was accepted")
	}
}

func TestValidatePreparedCloudMutationSurfaceRejectsUnpreflightedObjects(t *testing.T) {
	if err := validatePreparedCloudMutationSurface(&release.Release{Hooks: []*release.Hook{{Name: "migrate"}}}); err == nil || !strings.Contains(err.Error(), "hook") {
		t.Fatalf("hook validation error = %v", err)
	}
	if err := validatePreparedCloudMutationSurface(&release.Release{Manifest: "# HIDDEN: The Secret output has been suppressed\n"}); err == nil || !strings.Contains(err.Error(), "Secret") {
		t.Fatalf("hidden Secret validation error = %v", err)
	}
	withCRD := &chart.Chart{Files: []*chart.File{{Name: "crds/example.yaml", Data: []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n")}}}
	if err := validatePreparedCloudMutationSurface(&release.Release{Chart: withCRD}); err == nil || !strings.Contains(err.Error(), "CRD") {
		t.Fatalf("CRD validation error = %v", err)
	}
	if err := validatePreparedCloudMutationSurface(&release.Release{Manifest: "apiVersion: v1\nkind: Service\n"}); err != nil {
		t.Fatalf("ordinary manifest rejected: %v", err)
	}
}

func TestPrepareCloudUpgradePinsReleaseAndUpgradesToChartAppVersion(t *testing.T) {
	cfg := memoryActionConfig(t)
	currentChart, err := loader.Load("../../deploy/helm/radar")
	if err != nil {
		t.Fatal(err)
	}
	currentChart.Metadata.Version = "1.6.0"
	currentChart.Metadata.AppVersion = "1.6.2"
	currentReq := &InstallRequest{
		ReleaseName: "radar", Namespace: "default", ChartName: "radar",
		Version: "1.6.0", Repository: "unused",
		Values: map[string]any{"image": map[string]any{"tag": "1.5.9-pinned"}},
	}
	current, err := runServerDryRun(context.Background(), cfg, currentReq, currentChart)
	if err != nil {
		t.Fatalf("render current release: %v", err)
	}
	current.Info.Status = release.StatusDeployed
	current.Version = 6
	current.Config = currentReq.Values
	if err := cfg.Releases.Create(current); err != nil {
		t.Fatal(err)
	}

	targetChart, err := loader.Load("../../deploy/helm/radar")
	if err != nil {
		t.Fatal(err)
	}
	server := preparedChartServer(t, targetChart)
	defer server.Close()
	request := InstallRequest{
		ReleaseName: "radar", Namespace: "default", ChartName: "radar", Repository: server.URL,
		Values: map[string]any{
			"cloud": map[string]any{
				"enabled": true, "url": "wss://cloud.invalid/agent",
				"clusterName": "cluster-id", "existingSecret": "radar-cloud-config",
			},
			"auth": map[string]any{"mode": "proxy"},
		},
	}
	prepared, err := (&Client{}).prepareCloudUpgradeWith(context.Background(), cfg, request, "1.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.CurrentRevision() != 6 || prepared.CurrentChartVersion() != "1.6.0" {
		t.Fatalf("current identity = revision %d/chart %s", prepared.CurrentRevision(), prepared.CurrentChartVersion())
	}
	if prepared.ChartVersion() != targetChart.Metadata.Version || prepared.AppVersion() != targetChart.Metadata.AppVersion {
		t.Fatalf("target = chart %s/app %s", prepared.ChartVersion(), prepared.AppVersion())
	}
	if prepared.CurrentValues().ImageTag != "1.5.9-pinned" {
		t.Fatalf("current values = %+v", prepared.CurrentValues())
	}
	if !strings.Contains(prepared.TargetManifest(), "ghcr.io/skyhook-io/radar:"+targetChart.Metadata.AppVersion) {
		t.Fatalf("target manifest did not clear image pin:\n%s", prepared.TargetManifest())
	}
	if strings.Contains(prepared.TargetManifest(), "1.5.9-pinned") {
		t.Fatal("target manifest retained the old image.tag pin")
	}
	if prepared.CurrentDeployment().Name != "radar" || prepared.Deployment().Name != "radar" {
		t.Fatalf("workloads = current %+v, target %+v", prepared.CurrentDeployment(), prepared.Deployment())
	}

	configChanged := *current
	configChanged.Config = map[string]any{"image": map[string]any{"tag": "out-of-band-edit"}}
	if err := cfg.Releases.Update(&configChanged); err != nil {
		t.Fatal(err)
	}
	_, err = prepared.recheck(cfg)
	if _, ok := err.(*ReleaseChangedError); !ok {
		t.Fatalf("same-revision values race error = %T: %v", err, err)
	}
	if err := cfg.Releases.Update(current); err != nil {
		t.Fatal(err)
	}

	changed := *current
	changed.Version = 7
	changed.Info = &release.Info{Status: release.StatusDeployed}
	if err := cfg.Releases.Create(&changed); err != nil {
		t.Fatal(err)
	}
	_, err = prepared.recheck(cfg)
	if _, ok := err.(*ReleaseChangedError); !ok {
		t.Fatalf("revision race error = %T: %v", err, err)
	}
}

func TestResolvePreparedChartSummaryUsesVerifiedArchive(t *testing.T) {
	loaded, err := loader.Load("../../deploy/helm/radar")
	if err != nil {
		t.Fatal(err)
	}
	server := preparedChartServer(t, loaded)
	defer server.Close()
	client := &Client{}
	summary, err := client.ResolvePreparedChartSummary(context.Background(), &InstallRequest{
		ReleaseName: "radar", Namespace: "radar", ChartName: "radar", Repository: server.URL,
	}, "1.5.4")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ChartVersion != loaded.Metadata.Version || summary.AppVersion != loaded.Metadata.AppVersion {
		t.Fatalf("summary = %+v", summary)
	}
}

func preparedChartServer(t *testing.T, loaded *chart.Chart) *httptest.Server {
	t.Helper()
	archivePath, err := chartutil.Save(loaded, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	index := fmt.Sprintf(`apiVersion: v1
entries:
  radar:
  - apiVersion: v2
    name: radar
    version: %s
    appVersion: %q
    sources: [https://github.com/skyhook-io/radar]
    urls: [radar-%s.tgz]
    digest: %s
`, loaded.Metadata.Version, loaded.Metadata.AppVersion, loaded.Metadata.Version, hex.EncodeToString(digest[:]))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = w.Write([]byte(index))
		case "/radar-" + loaded.Metadata.Version + ".tgz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}
