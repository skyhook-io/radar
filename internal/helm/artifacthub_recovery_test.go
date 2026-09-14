package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

func artifactHubRecoveryRelease(t *testing.T, chartName, version string) (*action.Configuration, *release.Release) {
	t.Helper()
	cfg := memoryActionConfig(t)
	rel := &release.Release{
		Name: "installed", Namespace: "default", Version: 1,
		Info:  &release.Info{Status: release.StatusDeployed},
		Chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: chartName, Version: version}},
	}
	if err := cfg.Releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	return cfg, rel
}

func artifactHubTestRepository(t *testing.T, ch *chart.Chart, indexVersion string) string {
	t.Helper()
	archive := artifactHubChartArchive(t, ch)
	digest := sha256.Sum256(archive)
	index := fmt.Sprintf("apiVersion: v1\nentries:\n  %s:\n  - apiVersion: v2\n    name: %s\n    version: %s\n    urls: [%s-%s.tgz]\n    digest: %s\n",
		ch.Metadata.Name, ch.Metadata.Name, indexVersion, ch.Metadata.Name, indexVersion, hex.EncodeToString(digest[:]))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = w.Write([]byte(index))
		case "/" + ch.Metadata.Name + "-" + indexVersion + ".tgz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func artifactHubChartArchive(t *testing.T, ch *chart.Chart) []byte {
	t.Helper()
	archivePath, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func artifactHubSearchResult(chartName, version, repositoryName, repositoryURL string) *ArtifactHubSearchResult {
	return &ArtifactHubSearchResult{Charts: []ArtifactHubChart{{
		Name: chartName, Version: version,
		Repository: ArtifactHubRepository{Name: repositoryName, URL: repositoryURL},
	}}}
}

func TestArtifactHubRecoveryUniqueHTTPVerifiesAndAssociates(t *testing.T) {
	withOCISources(t, nil)
	const chartName, version = "example-chart", "1.2.3"
	repositoryURL := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: chartName, Version: version}}, version)
	cfg, rel := artifactHubRecoveryRelease(t, chartName, version)
	dir := t.TempDir()
	c := &Client{settings: testEnvSettings(filepath.Join(dir, "repositories.yaml"), dir)}
	var searchQuery string
	search := func(_ context.Context, query string, offset, limit int, official, verified bool, sort string) (*ArtifactHubSearchResult, error) {
		searchQuery = query
		if offset != 0 || limit != artifactHubRecoveryPageSize || official || verified || sort != "relevance" {
			t.Fatalf("unexpected search controls: %d %d %v %v %q", offset, limit, official, verified, sort)
		}
		return artifactHubSearchResult(chartName, version, "example-repo", repositoryURL), nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChartSourceCandidate{{Type: "repository", Reference: "example-repo", URL: repositoryURL}}
	if searchQuery != chartName || !slices.Equal(found, want) {
		t.Fatalf("query/candidates = %q / %+v, want chart name only / %+v", searchQuery, found, want)
	}
	beforeConfirmation, err := cfg.Releases.Get(rel.Name, rel.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, associated := chartSourceFromRelease(beforeConfirmation); associated {
		t.Fatal("ArtifactHub discovery persisted an unconfirmed source")
	}
	if name, err := c.ensureClassicRepository(found[0].URL, found[0].Reference); err != nil || name != "example-repo" {
		t.Fatalf("add verified repository = %q, %v", name, err)
	}
	if err := c.setSourceWith(cfg, rel.Name, found[0]); err != nil {
		t.Fatal(err)
	}
	stored, err := cfg.Releases.Get(rel.Name, rel.Version)
	if err != nil {
		t.Fatal(err)
	}
	if source, ok := chartSourceFromRelease(stored); !ok || *source != found[0] {
		t.Fatalf("associated source = %+v, %v", source, ok)
	}
	history, err := cfg.Releases.History(rel.Name)
	if err != nil || len(history) != 1 || history[0].Version != 1 {
		t.Fatalf("source association changed Helm revision history: %+v, %v", history, err)
	}
}

func TestArtifactHubRecoveryUniqueOCIRequiresIndependentVerification(t *testing.T) {
	withOCISources(t, nil)
	cfg, rel := artifactHubRecoveryRelease(t, "external-dns", "1.2.3")
	c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
	want := ChartSourceCandidate{Type: "oci", Reference: "oci://ghcr.io/example/charts/external-dns"}
	verified := 0
	verify := func(_ context.Context, _ *action.Configuration, got *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
		verified++
		if got.Name != rel.Name || candidate != want {
			t.Fatalf("verifier input = %s / %+v", got.Name, candidate)
		}
		return candidate, nil
	}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return artifactHubSearchResult("external-dns", "1.2.3", "external-dns", want.Reference), nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, verify)
	if err != nil || verified != 1 || !slices.Equal(found, []ChartSourceCandidate{want}) {
		t.Fatalf("OCI discovery = %+v, verification calls %d, error %v", found, verified, err)
	}
}

func TestArtifactHubRecoveryMultipleCandidatesNeverSelects(t *testing.T) {
	withOCISources(t, nil)
	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return &ArtifactHubSearchResult{Charts: []ArtifactHubChart{
			{Name: "app", Repository: ArtifactHubRepository{Name: "one", URL: "https://one.example.test"}},
			{Name: "app", Repository: ArtifactHubRepository{Name: "two", URL: "https://two.example.test"}},
		}}, nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, func(_ context.Context, _ *action.Configuration, _ *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
		return candidate, nil
	})
	if err != nil || len(found) != 2 {
		t.Fatalf("multiple candidates = %+v, %v", found, err)
	}
}

func TestArtifactHubRecoveryRejectsAbsentVersionAndIncompleteUmbrella(t *testing.T) {
	withOCISources(t, nil)
	for _, tc := range []struct {
		name         string
		chart        *chart.Chart
		indexVersion string
	}{
		{name: "exact version absent", chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "9.9.9"}}, indexVersion: "9.9.9"},
		{name: "dependency body absent", chart: &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3", Dependencies: []*chart.Dependency{{Name: "child", Version: "1.0.0"}}}}, indexVersion: "1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repositoryURL := artifactHubTestRepository(t, tc.chart, tc.indexVersion)
			cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
			c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
			search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
				return artifactHubSearchResult("app", "1.2.3", "example", repositoryURL), nil
			}
			found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
			if err != nil || len(found) != 0 {
				t.Fatalf("unverified source was offered: %+v, %v", found, err)
			}
		})
	}
}

func TestArtifactHubRecoveryNoResultAndExactProvenancePriority(t *testing.T) {
	withOCISources(t, nil)
	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	dir := t.TempDir()
	c := &Client{settings: testEnvSettings(filepath.Join(dir, "repositories.yaml"), dir)}
	searches := 0
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		searches++
		return &ArtifactHubSearchResult{}, nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err != nil || found == nil || len(found) != 0 || searches != 1 {
		t.Fatalf("empty discovery = %+v, searches %d, %v", found, searches, err)
	}
	repositoryURL := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}, "1.2.3")
	rel.Labels = mergeChartSourceLabels(rel.Labels, &ChartSourceCandidate{Type: "repository", Reference: "recorded", URL: repositoryURL})
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource); err == nil || searches != 1 {
		t.Fatalf("ArtifactHub queried despite exact provenance: searches=%d err=%v", searches, err)
	}
}

func TestArtifactHubRecoveryConfiguredSourcePriority(t *testing.T) {
	withOCISources(t, nil)
	cfg, rel := artifactHubRecoveryRelease(t, "argo-cd", "1.2.3")
	c := testHelmClientWithRepoVersions(t, map[string][]string{"bitnami": {"1.2.3"}})
	searches := 0
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		searches++
		return &ArtifactHubSearchResult{}, nil
	}
	if _, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource); err == nil || searches != 0 {
		t.Fatalf("ArtifactHub queried despite configured exact source: searches=%d err=%v", searches, err)
	}
}

func TestArtifactHubRecoveryPaginationBoundsAndCancellation(t *testing.T) {
	withOCISources(t, nil)
	t.Run("valid package on later page", func(t *testing.T) {
		cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
		c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
		offsets := []int{}
		search := func(ctx context.Context, _ string, offset, limit int, _ bool, _ bool, _ string) (*ArtifactHubSearchResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			offsets = append(offsets, offset)
			if limit != artifactHubRecoveryPageSize {
				t.Fatalf("limit = %d", limit)
			}
			if offset == 0 {
				page := make([]ArtifactHubChart, artifactHubRecoveryPageSize)
				for i := range page {
					page[i] = ArtifactHubChart{Name: fmt.Sprintf("unrelated-%d", i)}
				}
				return &ArtifactHubSearchResult{Charts: page}, nil
			}
			return artifactHubSearchResult("app", "1.2.3", "later", "https://later.example.test"), nil
		}
		verify := func(_ context.Context, _ *action.Configuration, _ *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
			return candidate, nil
		}
		found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, verify)
		if err != nil || !slices.Equal(offsets, []int{0, artifactHubRecoveryPageSize}) || len(found) != 1 || found[0].Reference != "later" {
			t.Fatalf("later-page discovery = %+v offsets=%v err=%v", found, offsets, err)
		}
	})

	t.Run("search is bounded", func(t *testing.T) {
		cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
		c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
		calls := 0
		search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
			calls++
			page := make([]ArtifactHubChart, artifactHubRecoveryPageSize)
			for i := range page {
				page[i] = ArtifactHubChart{Name: "unrelated"}
			}
			return &ArtifactHubSearchResult{Charts: page}, nil
		}
		found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
		if err != nil || len(found) != 0 || calls != artifactHubRecoveryMaxResults/artifactHubRecoveryPageSize {
			t.Fatalf("bounded discovery = %+v calls=%d err=%v", found, calls, err)
		}
	})

	t.Run("request cancellation stops paging", func(t *testing.T) {
		cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
		c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		search := func(ctx context.Context, _ string, _ int, _ int, _ bool, _ bool, _ string) (*ArtifactHubSearchResult, error) {
			calls++
			return nil, ctx.Err()
		}
		if _, err := c.discoverArtifactHubSourcesWith(ctx, cfg, rel.Name, search, c.verifyArtifactHubSource); err == nil || !strings.Contains(err.Error(), "canceled") || calls != 0 {
			t.Fatalf("canceled discovery calls=%d err=%v", calls, err)
		}
	})
}

func TestArtifactHubRecoveryExactSemVerAndIdentity(t *testing.T) {
	withOCISources(t, nil)
	for _, version := range []string{"1.2.3", "1.2.3-beta.1", "1.2.3+build.7", "v1.2.3"} {
		t.Run(version, func(t *testing.T) {
			repositoryURL := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: version}}, version)
			cfg, rel := artifactHubRecoveryRelease(t, "app", version)
			c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
			search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
				return artifactHubSearchResult("app", version, "semver", repositoryURL), nil
			}
			found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
			if err != nil || len(found) != 1 {
				t.Fatalf("exact version %q = %+v, %v", version, found, err)
			}
		})
	}

	t.Run("semantic neighbor is never substituted", func(t *testing.T) {
		repositoryURL := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.4"}}, "1.2.4")
		cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
		c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
		search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
			return artifactHubSearchResult("app", "1.2.4", "newer", repositoryURL), nil
		}
		found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
		if err != nil || len(found) != 0 {
			t.Fatalf("semantic neighbor was accepted: %+v, %v", found, err)
		}
	})

	for _, tc := range []struct {
		name           string
		archiveName    string
		archiveVersion string
	}{
		{name: "chart name mismatch", archiveName: "different", archiveVersion: "1.2.3"},
		{name: "chart version mismatch", archiveName: "app", archiveVersion: "1.2.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := artifactHubChartArchive(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: tc.archiveName, Version: tc.archiveVersion}})
			digest := sha256.Sum256(archive)
			index := fmt.Sprintf("apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: 1.2.3\n    urls: [app-1.2.3.tgz]\n    digest: %s\n", hex.EncodeToString(digest[:]))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index.yaml" {
					_, _ = w.Write([]byte(index))
					return
				}
				_, _ = w.Write(archive)
			}))
			defer server.Close()
			cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
			c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
			search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
				return artifactHubSearchResult("app", "1.2.3", "mismatch", server.URL), nil
			}
			found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
			if err != nil || len(found) != 0 {
				t.Fatalf("mismatched package was accepted: %+v, %v", found, err)
			}
		})
	}
}

func TestArtifactHubRecoveryRejectsBrokenRepositoryPackages(t *testing.T) {
	withOCISources(t, nil)
	validArchive := artifactHubChartArchive(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}})
	validDigest := sha256.Sum256(validArchive)
	for _, tc := range []struct {
		name          string
		archiveStatus int
		archive       []byte
		digest        string
	}{
		{name: "archive missing", archiveStatus: http.StatusNotFound, digest: hex.EncodeToString(validDigest[:])},
		{name: "archive corrupt", archiveStatus: http.StatusOK, archive: []byte("not a chart"), digest: hex.EncodeToString(sha256.New().Sum(nil))},
		{name: "digest mismatch", archiveStatus: http.StatusOK, archive: validArchive, digest: strings.Repeat("0", sha256.Size*2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := tc.digest
			if tc.name == "archive corrupt" {
				sum := sha256.Sum256(tc.archive)
				digest = hex.EncodeToString(sum[:])
			}
			index := fmt.Sprintf("apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: 1.2.3\n    urls: [app-1.2.3.tgz]\n    digest: %s\n", digest)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index.yaml" {
					_, _ = w.Write([]byte(index))
					return
				}
				w.WriteHeader(tc.archiveStatus)
				if tc.archiveStatus == http.StatusOK {
					_, _ = w.Write(tc.archive)
				}
			}))
			defer server.Close()
			cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
			c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
			search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
				return artifactHubSearchResult("app", "1.2.3", "broken", server.URL), nil
			}
			found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
			if err != nil || len(found) != 0 {
				t.Fatalf("broken package was accepted: %+v, %v", found, err)
			}
		})
	}
}

func TestArtifactHubRecoveryVerificationInfrastructureErrorIsNotNoResult(t *testing.T) {
	withOCISources(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return artifactHubSearchResult("app", "1.2.3", "unavailable", server.URL), nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err == nil || !strings.Contains(err.Error(), "network or service error") || found != nil {
		t.Fatalf("infrastructure failure became no-result: %+v, %v", found, err)
	}
}

func TestArtifactHubRecoveryRepositoryAuthentication(t *testing.T) {
	withOCISources(t, nil)
	const username, password = "existing-user", "existing-password"
	archive := artifactHubChartArchive(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}})
	digest := sha256.Sum256(archive)
	index := fmt.Sprintf("apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: 1.2.3\n    urls: [app-1.2.3.tgz]\n    digest: %s\n", hex.EncodeToString(digest[:]))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, ok := r.BasicAuth()
		if !ok || gotUser != username || gotPassword != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte(index))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return artifactHubSearchResult("app", "1.2.3", "private", server.URL), nil
	}

	t.Run("missing credentials is explicit", func(t *testing.T) {
		cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
		c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
		if found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource); err == nil || !strings.Contains(err.Error(), "verification requires authentication") || found != nil {
			t.Fatalf("missing auth = %+v, %v", found, err)
		}
	})

	t.Run("existing Helm credentials are reused", func(t *testing.T) {
		dir := t.TempDir()
		settings := testEnvSettings(filepath.Join(dir, "repositories.yaml"), dir)
		file := repo.NewFile()
		file.Update(&repo.Entry{Name: "private", URL: server.URL, Username: username, Password: password})
		if err := file.WriteFile(settings.RepositoryConfig, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
		c := &Client{settings: settings}
		found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
		if err != nil || len(found) != 1 || strings.Contains(found[0].URL, username) || strings.Contains(found[0].URL, password) {
			t.Fatalf("credential reuse = %+v, %v", found, err)
		}
	})
}

func TestArtifactHubRecoveryFollowsSafeHTTPSRedirectAndDeduplicates(t *testing.T) {
	withOCISources(t, nil)
	archive := artifactHubChartArchive(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}})
	digest := sha256.Sum256(archive)
	index := fmt.Sprintf("apiVersion: v1\nentries:\n  app:\n  - apiVersion: v2\n    name: app\n    version: 1.2.3\n    urls: [app-1.2.3.tgz]\n    digest: %s\n", hex.EncodeToString(digest[:]))
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			_, _ = w.Write([]byte(index))
			return
		}
		if r.URL.Path == "/app-1.2.3.tgz" {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	defer tlsServer.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsServer.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer redirect.Close()
	oldClient := httpClient
	httpClient = tlsServer.Client()
	defer func() { httpClient = oldClient }()

	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return &ArtifactHubSearchResult{Charts: []ArtifactHubChart{
			{Name: "app", Repository: ArtifactHubRepository{Name: "redirected", URL: redirect.URL + "/"}},
			{Name: "app", Repository: ArtifactHubRepository{Name: "canonical", URL: tlsServer.URL}},
		}}, nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err != nil || len(found) != 1 || found[0].URL != tlsServer.URL {
		t.Fatalf("redirected/deduplicated source = %+v, %v", found, err)
	}
}

func TestArtifactHubRecoveryMovedProvenanceRequiresConfirmation(t *testing.T) {
	withOCISources(t, nil)
	newURL := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}, "1.2.3")
	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	oldSource := ChartSourceCandidate{Type: "repository", Reference: "old", URL: "http://127.0.0.1:1/unavailable"}
	rel.Labels = mergeChartSourceLabels(rel.Labels, &oldSource)
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := &Client{settings: testEnvSettings(filepath.Join(dir, "repositories.yaml"), dir)}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return artifactHubSearchResult("app", "1.2.3", "moved", newURL), nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err != nil || len(found) != 1 {
		t.Fatalf("moved source discovery = %+v, %v", found, err)
	}
	stored, _ := cfg.Releases.Get(rel.Name, rel.Version)
	if source, ok := chartSourceFromRelease(stored); !ok || *source != oldSource {
		t.Fatalf("discovery silently replaced provenance: %+v", source)
	}
	if _, err := c.ensureClassicRepository(found[0].URL, found[0].Reference); err != nil {
		t.Fatal(err)
	}
	if err := c.setSourceWith(cfg, rel.Name, found[0]); err != nil {
		t.Fatal(err)
	}
	stored, _ = cfg.Releases.Get(rel.Name, rel.Version)
	if source, ok := chartSourceFromRelease(stored); !ok || *source != found[0] {
		t.Fatalf("confirmed replacement not persisted: %+v", source)
	}
}

func TestArtifactHubRecoveryMultipleVerifiedPublishersRemainAmbiguous(t *testing.T) {
	withOCISources(t, nil)
	one := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}, "1.2.3")
	two := artifactHubTestRepository(t, &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3"}}, "1.2.3")
	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return &ArtifactHubSearchResult{Charts: []ArtifactHubChart{
			{Name: "app", Repository: ArtifactHubRepository{Name: "publisher-one", URL: one}},
			{Name: "app", Repository: ArtifactHubRepository{Name: "publisher-two", URL: two}},
		}}, nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err != nil || len(found) != 2 {
		t.Fatalf("verified alternatives = %+v, %v", found, err)
	}
	if _, associated := chartSourceFromRelease(rel); associated {
		t.Fatal("ambiguous alternatives were automatically associated")
	}
}

func TestArtifactHubRecoveryRecursiveDependencyRemainsFailClosed(t *testing.T) {
	withOCISources(t, nil)
	child := &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "child", Version: "1.0.0", Dependencies: []*chart.Dependency{{Name: "grandchild", Version: "1.0.0"}}}}
	parent := &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "app", Version: "1.2.3", Dependencies: []*chart.Dependency{{Name: "child", Version: "1.0.0"}}}}
	parent.AddDependency(child)
	repositoryURL := artifactHubTestRepository(t, parent, "1.2.3")
	cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
	c := &Client{settings: testEnvSettings(filepath.Join(t.TempDir(), "repositories.yaml"), t.TempDir())}
	search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return artifactHubSearchResult("app", "1.2.3", "incomplete", repositoryURL), nil
	}
	found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, c.verifyArtifactHubSource)
	if err != nil || len(found) != 0 {
		t.Fatalf("recursive dependency gap was accepted: %+v, %v", found, err)
	}
}

func TestArtifactHubRecoveryOCIAuthenticationOutcomes(t *testing.T) {
	withOCISources(t, nil)
	for _, tc := range []struct {
		name                string
		existingCredentials bool
		verifyErr           error
		wantError           string
	}{
		{name: "public OCI"},
		{name: "private OCI with existing Helm registry credentials", existingCredentials: true},
		{name: "private OCI without credentials", verifyErr: errors.New("unauthorized: authentication required"), wantError: "verification requires authentication"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, rel := artifactHubRecoveryRelease(t, "app", "1.2.3")
			dir := t.TempDir()
			settings := testEnvSettings(filepath.Join(dir, "repositories.yaml"), dir)
			if tc.existingCredentials {
				settings.RegistryConfig = filepath.Join(dir, "registry.json")
				if err := os.WriteFile(settings.RegistryConfig, []byte(`{"auths":{"registry.example.test":{"auth":"dGVzdDp0ZXN0"}}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c := &Client{settings: settings}
			search := func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
				return artifactHubSearchResult("app", "1.2.3", "oci", "oci://registry.example.test/charts/app"), nil
			}
			verify := func(_ context.Context, _ *action.Configuration, _ *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
				if tc.existingCredentials {
					if _, err := c.newRegistryClientConcrete(); err != nil {
						return ChartSourceCandidate{}, err
					}
				}
				if tc.verifyErr != nil {
					return ChartSourceCandidate{}, classifyArtifactHubVerificationError(tc.verifyErr)
				}
				return candidate, nil
			}
			found, err := c.discoverArtifactHubSourcesWith(context.Background(), cfg, rel.Name, search, verify)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || found != nil {
					t.Fatalf("OCI auth failure = %+v, %v", found, err)
				}
				return
			}
			if err != nil || len(found) != 1 {
				t.Fatalf("OCI auth success = %+v, %v", found, err)
			}
		})
	}
}

func TestArtifactHubSearchNetworkFailuresRemainDistinct(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := searchArtifactHubAt(context.Background(), server.URL, "app", 0, 1, false, false, "relevance")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("status %d error = %v", status, err)
			}
		})
	}

	oldClient := httpClient
	defer func() { httpClient = oldClient }()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "DNS", err: &url.Error{Op: "Get", URL: "https://artifacthub.invalid", Err: &net.DNSError{Err: "no such host", Name: "artifacthub.invalid"}}, want: "DNS lookup failed"},
		{name: "TLS", err: errors.New("x509: certificate signed by unknown authority"), want: "TLS validation failed"},
		{name: "timeout", err: &timeoutNetworkError{}, want: "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err })}
			_, err := searchArtifactHubAt(context.Background(), "https://artifacthub.example.test", "app", 0, 1, false, false, "relevance")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s error = %v", tc.name, err)
			}
		})
	}

	t.Run("canceled", func(t *testing.T) {
		httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := searchArtifactHubAt(ctx, "https://artifacthub.example.test", "app", 0, 1, false, false, "relevance")
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("cancel error = %v", err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type timeoutNetworkError struct{}

func (*timeoutNetworkError) Error() string   { return "network timeout" }
func (*timeoutNetworkError) Timeout() bool   { return true }
func (*timeoutNetworkError) Temporary() bool { return true }

func testEnvSettings(repositoryConfig, repositoryCache string) *cli.EnvSettings {
	return &cli.EnvSettings{RepositoryConfig: repositoryConfig, RepositoryCache: repositoryCache}
}
