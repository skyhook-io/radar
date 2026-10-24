package helm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
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
	archives := map[string]string{}
	for _, version := range versions {
		filename, err := chartutil.Save(&chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: version}}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		archives["/app-"+version+".tgz"] = filename
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if archive, ok := archives[r.URL.Path]; ok {
			http.ServeFile(w, r, archive)
			return
		}
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
		{name: "link-local IPv6 with zone", url: "http://[fe80::1%25eth0]/charts", wantErr: true},
		{name: "link-local IPv6 with zone and port", url: "http://[fe80::1%25eth0]:8080/charts", wantErr: true},
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
	if got := c.configuredRepositoryForSource(recorded); got == nil || got.Name != "new-alias" || got.URL != server.URL {
		t.Fatalf("saved URL must win over repointed alias: %+v", got)
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
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}}
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

func TestSetSourceMovesRecordedRepositoryToLocalNameWhenVersionPruned(t *testing.T) {
	withOCISources(t, nil)
	recordedServer := chartSourceTestServer(t, "1.3.0")
	defer recordedServer.Close()
	otherServer := chartSourceTestServer(t, "9.9.9")
	defer otherServer.Close()
	c := newChartSourceTestClient(t)
	// The recorded alias points at a different repository on this machine, and
	// neither repository lists the installed 1.2.3 any more.
	if _, err := c.ensureClassicRepository(otherServer.URL, "podinfo"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensureClassicRepository(recordedServer.URL, "podinfo-2"); err != nil {
		t.Fatal(err)
	}
	recordedURL, _ := canonicalClassicRepositoryURL(recordedServer.URL)
	recorded := ChartSourceCandidate{Type: "repository", Reference: "podinfo", URL: recordedURL}
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Labels: chartSourceLabels(&recorded), Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}}
	rel.Labels[chartSourceBindingLabel] = chartSourceBinding(rel.Chart)
	cfg := chartSourceActionConfig(t, rel)

	otherURL, _ := canonicalClassicRepositoryURL(otherServer.URL)
	if err := c.setSourceWith(cfg, rel.Name, ChartSourceCandidate{Type: "repository", Reference: "podinfo", URL: otherURL}); err == nil {
		t.Fatal("a repository other than the recorded one was associated")
	}
	if err := c.setSourceWith(cfg, rel.Name, ChartSourceCandidate{Type: "repository", Reference: "podinfo", URL: recordedURL}); err != nil {
		t.Fatalf("URL identity must resolve to the matching local alias: %v", err)
	}
	if err := c.setSourceWith(cfg, rel.Name, ChartSourceCandidate{Type: "repository", Reference: "podinfo-2", URL: recordedURL}); err != nil {
		t.Fatalf("recorded repository under its local name: %v", err)
	}
	stored, err := cfg.Releases.Last(rel.Name)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := chartSourceFromRelease(stored)
	if !ok || got.Reference != "podinfo-2" || got.URL != recordedURL {
		t.Fatalf("recorded source after association = %+v", got)
	}
	info := &UpgradeInfo{CurrentVersion: "1.2.3"}
	if !c.applyRecordedUpgrade(info, *got, "app", "1.2.3", nil) || info.RepositoryName != "podinfo-2" || info.LatestVersion != "1.3.0" {
		t.Fatalf("upgrade after association = %+v", info)
	}
}

func TestRecordedButUnavailableIsDistinctFromAbsentProvenance(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	source := ChartSourceCandidate{Type: "repository", Reference: "missing", URL: "https://charts.example.test"}
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Labels: chartSourceLabels(&source), Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}}
	status, err := c.sourceStatusWith(chartSourceActionConfig(t, rel), rel.Name)
	if err != nil || status.Recorded == nil || status.Configured || status.Available {
		t.Fatalf("unavailable status = %+v, %v", status, err)
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
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Info: &release.Info{Status: release.StatusDeployed}, Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}}
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

func TestRepositoryConfigurationPersistsAcrossClientRestart(t *testing.T) {
	server := chartSourceTestServer(t, "1.2.3")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "repo"); err != nil {
		t.Fatal(err)
	}
	restarted := &Client{settings: &cli.EnvSettings{RepositoryConfig: c.settings.RepositoryConfig, RepositoryCache: c.settings.RepositoryCache}}
	versions, err := restarted.sourceVersions(ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL}, "app")
	if err != nil || len(versions) != 1 || versions[0] != "1.2.3" {
		t.Fatalf("restart recovery = %+v, %v", versions, err)
	}
	if _, err := os.Stat(c.settings.RepositoryConfig); err != nil {
		t.Fatal(err)
	}
}

func TestRecordedUpgradeSurvivesPrunedInstalledVersion(t *testing.T) {
	server := chartSourceTestServer(t, "2.0.0")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "repo"); err != nil {
		t.Fatal(err)
	}
	info := &UpgradeInfo{CurrentVersion: "1.0.0"}
	source := ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL}
	if !c.applyRecordedUpgrade(info, source, "app", "1.0.0", nil) {
		t.Fatal("recorded source rejected because the index no longer lists the installed version")
	}
	if info.LatestVersion != "2.0.0" || !info.UpdateAvailable || info.RepositoryName != "repo" {
		t.Fatalf("upgrade info = %+v", info)
	}
}

func TestInstallFromCredentialedDirectURLSkipsProvenance(t *testing.T) {
	c := newChartSourceTestClient(t)
	for _, repository := range []string{"https://reader:secret@charts.example.test/private", "https://charts.example.test/private?token=secret"} {
		source, err := c.installChartSource(&InstallRequest{Repository: repository, ChartName: "app"})
		if err != nil || source != nil {
			t.Fatalf("%s: source = %+v, err = %v; want install to proceed without provenance", repository, source, err)
		}
	}
	if source, err := c.installChartSource(&InstallRequest{Repository: "http://reader:secret@169.254.169.254/charts", ChartName: "app"}); err == nil || source != nil {
		t.Fatalf("credentialed link-local direct URL = %+v, %v; want rejection", source, err)
	}
}

func TestInstallFromCredentialedAliasSkipsProvenance(t *testing.T) {
	c := newChartSourceTestClient(t)
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "private", URL: "https://user:secret@charts.example.test"})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := c.installChartSource(&InstallRequest{Repository: "private", ChartName: "app"})
	if err != nil || source != nil {
		t.Fatalf("source = %+v, err = %v; want install to proceed without provenance", source, err)
	}
	if labels := chartSourceLabels(source); labels != nil {
		t.Fatalf("labels for a missing source = %v", labels)
	}
}

func TestSetSourceRefusesPendingRelease(t *testing.T) {
	withOCISources(t, nil)
	server := chartSourceTestServer(t, "1.2.3")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "repo"); err != nil {
		t.Fatal(err)
	}
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 2, Info: &release.Info{Status: release.StatusPendingUpgrade}, Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}}
	cfg := chartSourceActionConfig(t, rel)
	err := c.setSourceWith(cfg, rel.Name, ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL})
	if !errors.Is(err, errReleaseChanging) {
		t.Fatalf("association during a pending upgrade = %v, want errReleaseChanging", err)
	}
	stored, _ := cfg.Releases.Get(rel.Name, rel.Version)
	if stored.Info.Status != release.StatusPendingUpgrade {
		t.Fatalf("pending release status rewritten to %s", stored.Info.Status)
	}
	if _, ok := chartSourceFromRelease(stored); ok {
		t.Fatal("pending release gained provenance")
	}
}

func TestEnsureClassicRepositoryReusesConfiguredCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "reader" || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("apiVersion: v1\nentries: {}\n"))
	}))
	defer server.Close()
	c := newChartSourceTestClient(t)
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "private", URL: server.URL, Username: "reader", Password: "secret"})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := c.ensureClassicRepository(server.URL+"/", "another-name")
	if err != nil || name != "private" {
		t.Fatalf("re-adding a configured private repository = %q, %v", name, err)
	}
}

func TestInstallFromBlockedAliasStillFails(t *testing.T) {
	c := newChartSourceTestClient(t)
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "metadata", URL: "http://169.254.169.254/charts"})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if source, err := c.installChartSource(&InstallRequest{Repository: "metadata", ChartName: "app"}); err == nil || source != nil {
		t.Fatalf("link-local alias = %+v, %v; want the address policy to reject it", source, err)
	}
}

type tagListerFunc func(string) ([]string, error)

func (f tagListerFunc) Tags(ref string) ([]string, error) { return f(ref) }

func TestInstallFromCredentialedBlockedAliasStillFails(t *testing.T) {
	for _, rawURL := range []string{"http://user:secret@169.254.169.254/charts", "http://[fe80::1]:8080/charts?token=x"} {
		c := newChartSourceTestClient(t)
		f := repo.NewFile()
		f.Update(&repo.Entry{Name: "blocked", URL: rawURL})
		if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
			t.Fatal(err)
		}
		if source, err := c.installChartSource(&InstallRequest{Repository: "blocked", ChartName: "app"}); err == nil || source != nil {
			t.Fatalf("%s = %+v, %v; want the address policy to reject it", rawURL, source, err)
		}
	}
}

func TestSetSourceRefusesUninstallingRelease(t *testing.T) {
	c := newChartSourceTestClient(t)
	rel := &release.Release{Name: "app", Namespace: "ns", Version: 1, Info: &release.Info{Status: release.StatusUninstalling}, Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}}
	err := c.setSourceWith(chartSourceActionConfig(t, rel), rel.Name, ChartSourceCandidate{Type: "repository", Reference: "repo", URL: "https://charts.example.test"})
	if !errors.Is(err, errReleaseChanging) {
		t.Fatalf("association during uninstall = %v, want errReleaseChanging", err)
	}
}

func TestWriteRepositoryFileAtomicReplacesWithoutLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repositories.yaml")
	for _, name := range []string{"first", "second"} {
		f := repo.NewFile()
		f.Update(&repo.Entry{Name: name, URL: "https://charts.example.test/" + name})
		if err := writeRepositoryFileAtomic(f, path); err != nil {
			t.Fatal(err)
		}
		loaded, err := repo.LoadFile(path)
		if err != nil || !loaded.Has(name) || len(loaded.Repositories) != 1 {
			t.Fatalf("after writing %q: %+v, %v", name, loaded, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v; want 0600", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the config: %v", len(entries), err)
	}
}
