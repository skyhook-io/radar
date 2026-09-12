package helm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestChartSourceLabelsRoundTripSurvivesReleaseReload(t *testing.T) {
	sources := []ChartSourceCandidate{
		{Type: "repository", Reference: "radar-example-12345678", URL: "https://charts.example.test"},
		{Type: "oci", Reference: "oci://ghcr.io/example/charts/example-chart"},
	}
	for _, source := range sources {
		labels := chartSourceLabels(&source)
		for key, value := range labels {
			if errors := validation.IsValidLabelValue(value); len(errors) > 0 {
				t.Fatalf("label %s value %q is invalid: %v", key, value, errors)
			}
		}
		rel := &release.Release{Labels: labels}
		got, ok := chartSourceFromRelease(rel)
		if !ok || *got != source {
			t.Fatalf("round trip = %+v, %v; want %+v", got, ok, source)
		}
	}
}

func TestEnsureClassicRepositoryPersistsResolvedArtifactHubSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  app:\n  - name: app\n    version: 1.2.3\n    urls: [app-1.2.3.tgz]\n"))
	}))
	defer server.Close()
	dir := t.TempDir()
	c := &Client{settings: &cli.EnvSettings{RepositoryConfig: filepath.Join(dir, "repositories.yaml"), RepositoryCache: dir}}

	name, err := c.ensureClassicRepository(server.URL, "Artifact Hub Example")
	if err != nil {
		t.Fatal(err)
	}
	if name != "artifact-hub-example" {
		t.Fatalf("repository name = %q, want requested normalized name", name)
	}
	if repeated, err := c.ensureClassicRepository(server.URL+"/", name); err != nil || repeated != name {
		t.Fatalf("identical repository must succeed idempotently: %q, %v", repeated, err)
	}
	if _, err := c.ensureClassicRepository(server.URL+"/different", name); err == nil {
		t.Fatal("same name with different URL must report a conflict")
	}
	reloaded, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Get(name) == nil || reloaded.Get(name).URL != server.URL {
		t.Fatalf("persisted repository = %+v, want %s", reloaded.Repositories, server.URL)
	}
	if len(reloaded.Repositories) != 1 {
		t.Fatalf("idempotent add duplicated repository: %d entries", len(reloaded.Repositories))
	}
	if _, err := os.Stat(filepath.Join(dir, name+"-index.yaml")); err != nil {
		t.Fatalf("persisted index: %v", err)
	}
	candidates, err := c.configuredChartSourceCandidates("app", "1.2.3", &fakeTagLister{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(candidates, []ChartSourceCandidate{{Type: "repository", Reference: name, URL: server.URL}}) {
		t.Fatalf("recovery candidates = %+v, want newly added repository", candidates)
	}
}

func TestCanonicalClassicRepositoryURLNormalizesEquivalentURLs(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "https://CHARTS.Example.Test:443/path///", want: "https://charts.example.test/path"},
		{input: "http://CHARTS.Example.Test:80/", want: "http://charts.example.test"},
		{input: "https://charts.example.test/path", want: "https://charts.example.test/path"},
	} {
		got, err := canonicalClassicRepositoryURL(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("canonicalClassicRepositoryURL(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
}

func TestExistingReleaseRecoversAndPersistsUniqueClassicSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  example-chart:\n  - name: example-chart\n    version: 1.2.3\n    urls: [example-chart-1.2.3.tgz]\n"))
	}))
	defer server.Close()
	dir := t.TempDir()
	c := &Client{settings: &cli.EnvSettings{RepositoryConfig: filepath.Join(dir, "repositories.yaml"), RepositoryCache: dir}}
	if name, err := c.ensureClassicRepository(server.URL, "example-repo"); err != nil || name != "example-repo" {
		t.Fatalf("add repository = %q, %v", name, err)
	}

	memory := driver.NewMemory()
	memory.SetNamespace("example-namespace")
	releases := storage.Init(memory)
	rel := &release.Release{
		Name:      "example-release",
		Namespace: "example-namespace",
		Version:   1,
		Info:      &release.Info{Status: release.StatusDeployed},
		Chart:     &chart.Chart{Metadata: &chart.Metadata{Name: "example-chart", Version: "1.2.3"}},
	}
	if err := releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	candidates, err := c.configuredChartSourceCandidates(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, &fakeTagLister{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(candidates, []ChartSourceCandidate{{Type: "repository", Reference: "example-repo", URL: server.URL}}) {
		t.Fatalf("candidates = %+v", candidates)
	}
	rel.Labels = mergeChartSourceLabels(rel.Labels, &candidates[0])
	if err := releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	stored, err := releases.Get(rel.Name, rel.Version)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := chartSourceFromRelease(stored)
	if !ok || source.Type != "repository" || source.Reference != "example-repo" || source.URL != server.URL {
		t.Fatalf("stored source = %+v, %v", source, ok)
	}
}

func TestClassicRepositoryProvenanceRecoversAcrossMachines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  app:\n  - name: app\n    version: 1.2.3\n    urls: [app-1.2.3.tgz]\n"))
	}))
	defer server.Close()
	withOCISources(t, nil)

	newMachine := func() *Client {
		dir := t.TempDir()
		return &Client{settings: &cli.EnvSettings{RepositoryConfig: filepath.Join(dir, "repositories.yaml"), RepositoryCache: dir}}
	}
	machineA := newMachine()
	if name, err := machineA.ensureClassicRepository(server.URL, "example-repo"); err != nil || name != "example-repo" {
		t.Fatalf("machine A repository = %q, %v", name, err)
	}

	memory := driver.NewMemory()
	memory.SetNamespace("example")
	releases := storage.Init(memory)
	rel := &release.Release{
		Name:      "app",
		Namespace: "example",
		Version:   1,
		Info:      &release.Info{Status: release.StatusDeployed},
		Chart:     &chart.Chart{Metadata: &chart.Metadata{Name: "app", Version: "1.2.3"}},
	}
	if err := releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	actionConfig := &action.Configuration{
		Releases:   releases,
		KubeClient: &fake.PrintingKubeClient{},
		Log:        func(string, ...interface{}) {},
	}
	candidates, err := machineA.configuredChartSourceCandidates("app", "1.2.3", &fakeTagLister{})
	if err != nil || len(candidates) != 1 {
		t.Fatalf("machine A candidates = %+v, %v", candidates, err)
	}
	if err := machineA.setSourceWith(actionConfig, rel.Name, candidates[0]); err != nil {
		t.Fatal(err)
	}

	machineB := newMachine()
	if _, err := os.Stat(machineB.settings.RepositoryConfig); !os.IsNotExist(err) {
		t.Fatalf("machine B unexpectedly has repositories.yaml: %v", err)
	}
	recorded, err := machineB.sourceCandidatesWith(actionConfig, rel.Name)
	if err != nil || !slices.Equal(recorded, []ChartSourceCandidate{{Type: "repository", Reference: "example-repo", URL: server.URL}}) {
		t.Fatalf("machine B recorded source = %+v, %v", recorded, err)
	}
	if name, err := machineB.ensureClassicRepository(recorded[0].URL, recorded[0].Reference); err != nil || name != "example-repo" {
		t.Fatalf("machine B add repository = %q, %v", name, err)
	}
	if err := machineB.setSourceWith(actionConfig, rel.Name, recorded[0]); err != nil {
		t.Fatal(err)
	}
	info := &UpgradeInfo{CurrentVersion: "1.2.3"}
	if !machineB.applyCandidateUpgrade(info, recorded[0], "app", "1.2.3", &fakeTagLister{}) {
		t.Fatal("machine B could not reconstruct the exact recorded chart/version")
	}
}

func TestLegacyNameOnlyClassicProvenanceCompatibility(t *testing.T) {
	c := testHelmClientWithRepoVersions(t, map[string][]string{"bitnami": {"1.2.3"}})
	legacyLabels := map[string]string{chartSourceTypeLabel: "repository"}
	if !addChartSourceLabelChunks(legacyLabels, chartSourceRefLabel, "missing-on-machine-b") {
		t.Fatal("encode legacy source")
	}
	rel := &release.Release{
		Name: "argo", Namespace: "default", Version: 1, Labels: legacyLabels,
		Info:  &release.Info{Status: release.StatusDeployed},
		Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "argo-cd", Version: "1.2.3"}},
	}
	memory := driver.NewMemory()
	memory.SetNamespace("default")
	releases := storage.Init(memory)
	if err := releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	legacy, ok := chartSourceFromRelease(rel)
	if !ok || legacy.Reference != "missing-on-machine-b" || legacy.URL != "" {
		t.Fatalf("legacy source = %+v, %v", legacy, ok)
	}
	recovered, err := c.recoverLegacyClassicSource(&action.Configuration{Releases: releases}, rel, *legacy, &fakeTagLister{})
	if err != nil || recovered == nil || recovered.Reference != "bitnami" || recovered.URL != "https://charts.bitnami.com/bitnami" {
		t.Fatalf("recovered source = %+v, %v", recovered, err)
	}
	stored, err := releases.Get(rel.Name, rel.Version)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, ok := chartSourceFromRelease(stored)
	if !ok || *upgraded != *recovered {
		t.Fatalf("upgraded provenance = %+v, %v; want %+v", upgraded, ok, recovered)
	}

	localLabels := map[string]string{chartSourceTypeLabel: "repository"}
	if !addChartSourceLabelChunks(localLabels, chartSourceRefLabel, "bitnami") {
		t.Fatal("encode local legacy source")
	}
	localRel := &release.Release{
		Name: "argo-local", Namespace: "default", Version: 1, Labels: localLabels,
		Info:  &release.Info{Status: release.StatusDeployed},
		Chart: rel.Chart,
	}
	if err := releases.Create(localRel); err != nil {
		t.Fatal(err)
	}
	localLegacy := ChartSourceCandidate{Type: "repository", Reference: "bitnami"}
	localRecovered, err := c.recoverLegacyClassicSource(&action.Configuration{Releases: releases}, localRel, localLegacy, &fakeTagLister{})
	if err != nil || localRecovered == nil || localRecovered.Reference != "bitnami" || localRecovered.URL != "https://charts.bitnami.com/bitnami" {
		t.Fatalf("local alias migration = %+v, %v", localRecovered, err)
	}
	info := &UpgradeInfo{CurrentVersion: "1.2.3"}
	if !c.applyCandidateUpgrade(info, *localRecovered, "argo-cd", "1.2.3", &fakeTagLister{}) {
		t.Fatal("legacy name-only provenance with a local alias no longer resolves")
	}

	emptyDir := t.TempDir()
	emptyClient := &Client{settings: &cli.EnvSettings{
		RepositoryConfig: filepath.Join(emptyDir, "repositories.yaml"),
		RepositoryCache:  emptyDir,
	}}
	missing, err := emptyClient.recoverLegacyClassicSource(
		&action.Configuration{Releases: releases},
		&release.Release{Chart: rel.Chart},
		ChartSourceCandidate{Type: "repository", Reference: "unknown"},
		&fakeTagLister{},
	)
	if err != nil || missing != nil {
		t.Fatalf("legacy source without an exact configured candidate must fail closed: %+v, %v", missing, err)
	}
}

func TestConfiguredChartSourceCandidatesExactRecovery(t *testing.T) {
	c := testHelmClientWithRepoVersions(t, map[string][]string{
		"bitnami": {"1.2.3"},
		"argo":    {"1.2.2"},
	})
	withOCISources(t, []string{"oci://reg/charts"})
	lister := &fakeTagLister{tags: map[string][]string{"reg/charts/argo-cd": {"1.2.2"}}}

	got, err := c.configuredChartSourceCandidates("argo-cd", "1.2.3", lister)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChartSourceCandidate{{Type: "repository", Reference: "bitnami", URL: "https://charts.bitnami.com/bitnami"}}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
}

func TestConfiguredChartSourceCandidatesAmbiguousAcrossClassicAndOCI(t *testing.T) {
	c := testHelmClientWithRepoVersions(t, map[string][]string{
		"bitnami": {"1.2.3"},
		"argo":    {"1.2.3"},
	})
	withOCISources(t, []string{"oci://reg/charts"})
	lister := &fakeTagLister{tags: map[string][]string{"reg/charts/argo-cd": {"1.2.3"}}}

	got, err := c.configuredChartSourceCandidates("argo-cd", "1.2.3", lister)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("candidates = %+v, want two classic and one OCI match", got)
	}
}

func TestConfiguredChartSourceCandidatesNoMatchFailsClosed(t *testing.T) {
	c := testHelmClientWithRepoVersions(t, map[string][]string{"bitnami": {"1.2.2"}, "argo": {"1.2.2"}})
	withOCISources(t, []string{"oci://reg/charts"})
	got, err := c.configuredChartSourceCandidates("argo-cd", "1.2.3", &fakeTagLister{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("candidates = %+v, want none", got)
	}
}

func TestResolveOCIUpgradeURLMultipleExactMatchesNeverGuesses(t *testing.T) {
	withOCISources(t, []string{"oci://reg1/charts", "oci://reg2/charts"})
	lister := &fakeTagLister{tags: map[string][]string{
		"reg1/charts/app": {"1.2.3"},
		"reg2/charts/app": {"1.2.3"},
	}}
	c := &Client{}
	if got, ok := c.resolveOCIUpgradeURLWithLister("app", "1.2.3", lister); ok || got != "" {
		t.Fatalf("ambiguous OCI resolution = %q, %v; want fail closed", got, ok)
	}
	if got := c.resolveOCIUpgradeURLsWithLister("app", "1.2.3", lister); len(got) != 2 {
		t.Fatalf("candidate URLs = %v, want both sources", got)
	}
}

func TestResolveOCIInstallSourceAcceptsPrefixAndFullReference(t *testing.T) {
	for _, input := range []string{"oci://ghcr.io/acme/charts", "oci://ghcr.io/acme/charts/app"} {
		chartURL, prefix, err := resolveOCIInstallSource(input, "app")
		if err != nil {
			t.Fatal(err)
		}
		if chartURL != "oci://ghcr.io/acme/charts/app" || prefix != "oci://ghcr.io/acme/charts" {
			t.Fatalf("input %q => %q, %q", input, chartURL, prefix)
		}
	}
}

func TestChartSourcesNeverPersistCredentialsOrTruncateReferences(t *testing.T) {
	if _, err := normalizeOCIPrefix("oci://user:password@ghcr.io/acme"); err == nil {
		t.Fatal("expected credential-bearing OCI source to be rejected")
	}
	tooLong := &ChartSourceCandidate{Type: "oci", Reference: "oci://registry.example/" + string(make([]byte, chartSourceChunkSize*chartSourceMaxChunks))}
	if err := validateChartSourceCandidate(tooLong); err == nil {
		t.Fatal("expected an unpersistable source reference to fail closed")
	}
	credentialURL := &ChartSourceCandidate{Type: "repository", Reference: "private", URL: "https://user:password@charts.example.test"}
	if err := validateChartSourceCandidate(credentialURL); err == nil {
		t.Fatal("expected credential-bearing classic repository URL to be rejected")
	}
}
