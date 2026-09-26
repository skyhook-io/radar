package helm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestChartSourceLabelsRoundTripAndRejectCredentials(t *testing.T) {
	for _, source := range []ChartSourceCandidate{
		{Type: "repository", Reference: "old-alias", URL: "https://charts.example.test"},
		{Type: "oci", Reference: "oci://registry.example.test/team/charts/app"},
	} {
		labels := chartSourceLabels(&source)
		if len(labels) == 0 {
			t.Fatalf("no labels for %+v", source)
		}
		for key, value := range labels {
			if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
				t.Fatalf("invalid label %s=%q: %v", key, value, errs)
			}
			if len(key) < len("radarhq.io/") || key[:len("radarhq.io/")] != "radarhq.io/" {
				t.Fatalf("label %q is outside radarhq.io namespace", key)
			}
		}
		got, ok := chartSourceFromRelease(&release.Release{Labels: labels})
		if !ok || *got != source {
			t.Fatalf("round trip = %+v, %v; want %+v", got, ok, source)
		}
	}
	for _, source := range []ChartSourceCandidate{
		{Type: "repository", Reference: "repo", URL: "https://user:secret@charts.example.test"},
		{Type: "repository", Reference: "repo", URL: "https://charts.example.test?token=secret"},
		{Type: "oci", Reference: "oci://user:secret@registry.example.test/charts/app"},
	} {
		if labels := chartSourceLabels(&source); labels != nil {
			t.Fatalf("credential-bearing source was persisted: %+v", source)
		}
	}
}

func TestChartSourceUpgradeLabelsRemoveStaleChunks(t *testing.T) {
	old := chartSourceLabels(&ChartSourceCandidate{Type: "repository", Reference: "a-very-long-repository-alias-that-needs-more-than-one-label-chunk-because-it-keeps-going", URL: "https://charts.example.test/a/very/long/path/that/also/needs/chunks"})
	next := ChartSourceCandidate{Type: "oci", Reference: "oci://registry.example.test/app"}
	patch := chartSourceUpgradeLabels(old, &next)
	for key := range old {
		if _, retained := chartSourceLabels(&next)[key]; !retained && patch[key] != "null" {
			t.Fatalf("stale label %s was not removed: %q", key, patch[key])
		}
	}
}

func TestCanonicalClassicRepositoryURL(t *testing.T) {
	got, err := canonicalClassicRepositoryURL("https://CHARTS.Example.Test:443/path///")
	if err != nil || got != "https://charts.example.test/path" {
		t.Fatalf("canonical URL = %q, %v", got, err)
	}
}

func chartSourceTestServer(t *testing.T, versions ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  app:\n"))
		for _, version := range versions {
			_, _ = w.Write([]byte("  - name: app\n    version: " + version + "\n    urls: [app-" + version + ".tgz]\n"))
		}
	}))
}

func newChartSourceTestClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	return &Client{settings: &cli.EnvSettings{RepositoryConfig: filepath.Join(dir, "repositories.yaml"), RepositoryCache: dir}}
}

func TestEnsureClassicRepositoryDoesNotHoldMutexAcrossNetwork(t *testing.T) {
	c := newChartSourceTestClient(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		locked := make(chan struct{})
		go func() {
			c.mu.Lock()
			c.mu.Unlock()
			close(locked)
		}()
		select {
		case <-locked:
			_, _ = w.Write([]byte("apiVersion: v1\nentries: {}\n"))
		case <-time.After(time.Second):
			http.Error(w, "client mutex held during network request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	if _, err := c.ensureClassicRepository(server.URL, "repo"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureClassicRepositoryConcurrentAliasConflictCannotCorruptCache(t *testing.T) {
	c := newChartSourceTestClient(t)
	ready := sync.WaitGroup{}
	ready.Add(2)
	releaseResponses := make(chan struct{})
	server := func(chartName string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/index.yaml" {
				http.NotFound(w, r)
				return
			}
			ready.Done()
			<-releaseResponses
			_, _ = w.Write([]byte("apiVersion: v1\nentries:\n  " + chartName + ":\n  - name: " + chartName + "\n    version: 1.0.0\n    urls: [" + chartName + "-1.0.0.tgz]\n"))
		}))
	}
	first, second := server("first-chart"), server("second-chart")
	defer first.Close()
	defer second.Close()

	type result struct {
		url string
		err error
	}
	results := make(chan result, 2)
	for _, repositoryURL := range []string{first.URL, second.URL} {
		go func() {
			_, err := c.ensureClassicRepository(repositoryURL, "shared-alias")
			results <- result{url: repositoryURL, err: err}
		}()
	}
	ready.Wait()
	close(releaseResponses)

	var acceptedURL string
	conflicts := 0
	for range 2 {
		got := <-results
		if got.err == nil {
			acceptedURL = got.url
		} else if errors.Is(got.err, errRepositoryConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected repository-add error: %v", got.err)
		}
	}
	if acceptedURL == "" || conflicts != 1 {
		t.Fatalf("accepted URL = %q, conflicts = %d; want one accepted and one rejected", acceptedURL, conflicts)
	}
	configured, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil || configured.Get("shared-alias") == nil || configured.Get("shared-alias").URL != acceptedURL {
		t.Fatalf("configured repository = %+v, %v", configured, err)
	}
	index, err := repo.LoadIndexFile(filepath.Join(c.settings.RepositoryCache, "shared-alias-index.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wantChart, rejectedChart := "first-chart", "second-chart"
	if acceptedURL == second.URL {
		wantChart, rejectedChart = rejectedChart, wantChart
	}
	if len(index.Entries[wantChart]) != 1 || len(index.Entries[rejectedChart]) != 0 {
		t.Fatalf("published cache entries = %v; want only %q from accepted URL", index.Entries, wantChart)
	}
}

func TestCanonicalClassicRepositoryURLAppliesExistingAddressPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "metadata IPv4", url: "http://169.254.169.254/charts", wantErr: true},
		{name: "link-local IPv4 with port", url: "http://169.254.1.2:8080/charts", wantErr: true},
		{name: "link-local IPv6", url: "http://[fe80::1]/charts", wantErr: true},
		{name: "link-local IPv6 with port", url: "http://[fe80::1]:8080/charts", wantErr: true},
		{name: "loopback remains allowed by policy", url: "http://127.0.0.1:8080/charts"},
		{name: "localhost remains allowed by policy", url: "http://localhost:8080/charts"},
		{name: "private address remains allowed by policy", url: "http://10.0.0.5/charts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := canonicalClassicRepositoryURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Fatalf("canonicalClassicRepositoryURL(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestConfiguredCandidatesExactAndAmbiguous(t *testing.T) {
	withOCISources(t, []string{"oci://registry.example.test/charts"})
	server := chartSourceTestServer(t, "1.2.3", "2.0.0")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "classic"); err != nil {
		t.Fatal(err)
	}
	lister := &fakeTagLister{tags: map[string][]string{"registry.example.test/charts/app": {"1.2.3"}}}
	candidates, err := c.configuredChartSourceCandidates("app", "1.2.3", lister)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("exact candidates = %+v, %v", candidates, err)
	}
	missing, err := c.configuredChartSourceCandidates("app", "9.9.9", lister)
	if err != nil || len(missing) != 0 {
		t.Fatalf("version mismatch candidates = %+v, %v", missing, err)
	}
}

func TestClassicProvenanceCrossMachineAliasRecoveryAndMismatchFailsClosed(t *testing.T) {
	server := chartSourceTestServer(t, "1.2.3")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "new-alias"); err != nil {
		t.Fatal(err)
	}
	recorded := ChartSourceCandidate{Type: "repository", Reference: "old-alias", URL: server.URL}
	if got := c.configuredRepositoryForSource(recorded); got == nil || got.Name != "new-alias" {
		t.Fatalf("cross-machine URL recovery = %+v", got)
	}
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		t.Fatal(err)
	}
	f.Update(&repo.Entry{Name: "old-alias", URL: "https://different.example.test"})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.configuredRepositoryForSource(recorded); got != nil {
		t.Fatalf("mismatched recorded alias must fail closed, got %+v", got)
	}
}

func chartSourceActionConfig(t *testing.T, rel *release.Release) *action.Configuration {
	t.Helper()
	memory := driver.NewMemory()
	memory.SetNamespace(rel.Namespace)
	releases := storage.Init(memory)
	if err := releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	return &action.Configuration{Releases: releases, KubeClient: &fake.PrintingKubeClient{}, Log: func(string, ...interface{}) {}}
}

func TestSourceStatusReadOnlyAndManualAssociation(t *testing.T) {
	withOCISources(t, nil)
	server := chartSourceTestServer(t, "1.2.3")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "configured-alias"); err != nil {
		t.Fatal(err)
	}
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "app", Version: "1.2.3"}}}
	cfg := chartSourceActionConfig(t, rel)
	status, err := c.sourceStatusWith(cfg, rel.Name)
	if err != nil || status.Recorded != nil || status.Configured || status.Available || len(status.Candidates) != 1 {
		t.Fatalf("unrecorded status = %+v, %v", status, err)
	}
	stored, _ := cfg.Releases.Get(rel.Name, rel.Version)
	if _, ok := chartSourceFromRelease(stored); ok {
		t.Fatal("GET status mutated release provenance")
	}
	if err := c.setSourceWith(cfg, rel.Name, status.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	status, err = c.sourceStatusWith(cfg, rel.Name)
	if err != nil || status.Recorded == nil || !status.Configured || !status.Available {
		t.Fatalf("associated status = %+v, %v", status, err)
	}
	bad := ChartSourceCandidate{Type: "repository", Reference: "configured-alias", URL: server.URL + "/wrong"}
	if err := c.setSourceWith(cfg, rel.Name, bad); err == nil {
		t.Fatal("unverified source association succeeded")
	}
}

func TestRecordedButUnavailableIsDistinctFromAbsentProvenance(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	source := ChartSourceCandidate{Type: "repository", Reference: "missing", URL: "https://charts.example.test"}
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Labels: chartSourceLabels(&source), Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "app", Version: "1.2.3"}}}
	status, err := c.sourceStatusWith(chartSourceActionConfig(t, rel), rel.Name)
	if err != nil || status.Recorded == nil || status.Configured || status.Available {
		t.Fatalf("unavailable status = %+v, %v", status, err)
	}
}

func TestInstallChartSourceStripsOCISelectorForRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		repository string
	}{
		{name: "tag", repository: "oci://registry.example.test/team/charts/app:1.2.3"},
		{name: "digest", repository: "oci://registry.example.test/team/charts/app@sha256:" + strings.Repeat("a", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withOCISources(t, []string{"oci://registry.example.test/team/charts"})
			c := newChartSourceTestClient(t)
			installRef, err := resolveOCIChartURL(tc.repository, "app")
			if err != nil || installRef != tc.repository {
				t.Fatalf("install resolution = %q, %v; selector must be preserved", installRef, err)
			}
			source, err := c.installChartSource(&InstallRequest{Repository: tc.repository, ChartName: "app"})
			want := ChartSourceCandidate{Type: "oci", Reference: "oci://registry.example.test/team/charts/app"}
			if err != nil || *source != want {
				t.Fatalf("recorded source = %+v, %v; want %+v", source, err, want)
			}
			lister := &fakeTagLister{tags: map[string][]string{"registry.example.test/team/charts/app": {"1.2.3", "1.2.2"}}}
			versions := c.candidateVersions(*source, "app", lister)
			if !slices.Equal(versions, []string{"1.2.3", "1.2.2"}) {
				t.Fatalf("version discovery = %v", versions)
			}
			resolved, kind, err := c.resolveRecordedChartPathWithLister(nil, *source, "app", "1.2.3", lister)
			if err != nil || resolved != want.Reference || kind != "oci" {
				t.Fatalf("recorded source resolution = %q, %q, %v", resolved, kind, err)
			}
		})
	}
}

func TestInstallChartSourceNeverPersistsOCICredentials(t *testing.T) {
	c := newChartSourceTestClient(t)
	credentialed, err := c.installChartSource(&InstallRequest{Repository: "oci://user:secret@registry.example.test/charts", ChartName: "app"})
	if err == nil || credentialed != nil || !errors.Is(err, errInvalidChartSource) {
		t.Fatalf("credential-bearing OCI provenance = %+v, %v", credentialed, err)
	}
}

func TestInstallSourceClassicUsesCanonicalURLAndStableAlias(t *testing.T) {
	c := newChartSourceTestClient(t)
	source, err := c.installChartSource(&InstallRequest{Repository: "https://CHARTS.example.test:443/path/", RepositoryName: "Upstream Charts", ChartName: "app"})
	want := ChartSourceCandidate{Type: "repository", Reference: "upstream-charts", URL: "https://charts.example.test/path"}
	if err != nil || *source != want {
		t.Fatalf("classic source = %+v, %v; want %+v", source, err, want)
	}
}

func TestRejectedPreInstallDoesNotPersistSource(t *testing.T) {
	c := newChartSourceTestClient(t)
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "app", Version: "1.2.3"}}}
	cfg := chartSourceActionConfig(t, rel)
	req := &InstallRequest{ReleaseName: rel.Name, Namespace: rel.Namespace, ChartName: "app", Version: "1.2.3", Repository: "oci://user:secret@registry.example.test/charts"}
	if _, err := c.installWith(cfg, req); err == nil {
		t.Fatal("install over a deployed release unexpectedly passed preflight")
	}
	stored, err := cfg.Releases.Get(rel.Name, rel.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := chartSourceFromRelease(stored); ok || req.resolvedSource != nil {
		t.Fatal("preflight-rejected install persisted or prepared chart provenance")
	}
}

func TestCandidateOrderingStable(t *testing.T) {
	candidates := []ChartSourceCandidate{{Type: "repository", Reference: "b"}, {Type: "oci", Reference: "z"}, {Type: "repository", Reference: "a"}}
	slices.SortFunc(candidates, func(a, b ChartSourceCandidate) int {
		if a.Type != b.Type {
			if a.Type < b.Type {
				return -1
			}
			return 1
		}
		if a.Reference < b.Reference {
			return -1
		}
		if a.Reference > b.Reference {
			return 1
		}
		return 0
	})
	if candidates[0].Reference != "z" || candidates[1].Reference != "a" || candidates[2].Reference != "b" {
		t.Fatalf("unexpected ordering: %+v", candidates)
	}
}

func TestRepositoryConfigurationPersistsAcrossClientRestart(t *testing.T) {
	server := chartSourceTestServer(t, "1.2.3")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "repo"); err != nil {
		t.Fatal(err)
	}
	restarted := &Client{settings: &cli.EnvSettings{RepositoryConfig: c.settings.RepositoryConfig, RepositoryCache: c.settings.RepositoryCache}}
	candidates, err := restarted.configuredChartSourceCandidates("app", "1.2.3", &fakeTagLister{})
	if err != nil || len(candidates) != 1 || candidates[0].Reference != "repo" {
		t.Fatalf("restart recovery = %+v, %v", candidates, err)
	}
	if _, err := os.Stat(c.settings.RepositoryConfig); err != nil {
		t.Fatal(err)
	}
}
