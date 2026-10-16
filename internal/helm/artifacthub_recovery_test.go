package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

func artifactHubRecoveryRelease(t *testing.T, installed *chart.Chart) (*action.Configuration, *release.Release) {
	t.Helper()
	rel := &release.Release{
		Name:      "legacy",
		Namespace: "apps",
		Version:   1,
		Info:      &release.Info{Status: release.StatusDeployed},
		Chart:     installed,
	}
	return chartSourceActionConfig(t, rel), rel
}

func artifactHubTestChart(name, version, template string) *chart.Chart {
	return &chart.Chart{
		Metadata:  &chart.Metadata{APIVersion: "v2", Name: name, Version: version},
		Templates: []*chart.File{{Name: "templates/app.yaml", Data: []byte(template)}},
		Schema:    []byte(`{"type":"object"}`),
	}
}

func artifactHubChartArchive(t *testing.T, ch *chart.Chart) []byte {
	t.Helper()
	path, err := chartutil.Save(ch, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func artifactHubRepository(t *testing.T, ch *chart.Chart) *httptest.Server {
	t.Helper()
	archive := artifactHubChartArchive(t, ch)
	digest := sha256.Sum256(archive)
	index := fmt.Sprintf("apiVersion: v1\nentries:\n  %s:\n  - apiVersion: v2\n    name: %s\n    version: %s\n    urls: [%s-%s.tgz]\n    digest: %s\n", ch.Metadata.Name, ch.Metadata.Name, ch.Metadata.Version, ch.Metadata.Name, ch.Metadata.Version, hex.EncodeToString(digest[:]))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = w.Write([]byte(index))
		case "/" + ch.Metadata.Name + "-" + ch.Metadata.Version + ".tgz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
}

func artifactHubSearchResults(charts ...ArtifactHubChart) artifactHubRecoverySearch {
	return func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		return &ArtifactHubSearchResult{Charts: charts, Total: len(charts)}, nil
	}
}

func TestArtifactHubSearchReturnsTrustMetadataWithoutVerification(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	cfg, rel := artifactHubRecoveryRelease(t, artifactHubTestChart("nginx", "1.2.3", "installed"))
	queries := 0
	search := func(_ context.Context, query string, offset, limit int, official, verified bool, sortOrder string) (*ArtifactHubSearchResult, error) {
		queries++
		if query != "nginx" || offset != 0 || limit != artifactHubRecoveryPageSize || official || verified || sortOrder != "relevance" {
			t.Fatalf("search leaked or changed controls: %q %d %d %v %v %q", query, offset, limit, official, verified, sortOrder)
		}
		return &ArtifactHubSearchResult{Charts: []ArtifactHubChart{
			{Name: "nginx", Repository: ArtifactHubRepository{Name: "community", URL: "https://charts.example.test", OrganizationName: "Example Org", Official: true, VerifiedPublisher: true}},
			{Name: "nginx", Repository: ArtifactHubRepository{Name: "other", URL: "oci://ghcr.io/other/charts/nginx", OrganizationName: "Other Org"}},
			{Name: "nginx-lookalike", Repository: ArtifactHubRepository{Name: "wrong", URL: "https://wrong.example.test"}},
		}}, nil
	}

	found, err := c.searchArtifactHubRecoverySourcesWith(context.Background(), cfg, rel.Name, search)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 || len(found) != 2 {
		t.Fatalf("queries/candidates = %d/%+v, want one metadata search and two exact-name candidates", queries, found)
	}
	if !found[0].Official || !found[0].VerifiedPublisher || found[0].Publisher != "Example Org" || found[0].URL != "https://charts.example.test" {
		t.Fatalf("trust metadata lost: %+v", found[0])
	}
	stored, _ := cfg.Releases.Get(rel.Name, rel.Version)
	if _, ok := chartSourceFromRelease(stored); ok {
		t.Fatal("search mutated release provenance")
	}
}

func TestArtifactHubRecoveryVerifiesOnlySelectedCandidate(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	cfg, rel := artifactHubRecoveryRelease(t, artifactHubTestChart("app", "1.2.3", "installed"))
	classic := ArtifactHubChart{Name: "app", Repository: ArtifactHubRepository{Name: "classic", URL: "https://classic.example.test"}}
	oci := ArtifactHubChart{Name: "app", Repository: ArtifactHubRepository{Name: "oci", URL: "oci://ghcr.io/example/charts/app"}}
	selected, _ := artifactHubRecoveryCandidate(oci, "app")
	verified := 0
	associated := 0
	verify := func(_ context.Context, _ *action.Configuration, got *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
		verified++
		if got.Name != rel.Name || candidate != selected.chartSource() {
			t.Fatalf("verified wrong candidate: %s %+v", got.Name, candidate)
		}
		return candidate, nil
	}
	associate := func(_ *action.Configuration, _ *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
		associated++
		return candidate, nil
	}
	got, err := c.recoverArtifactHubSourceWith(context.Background(), cfg, rel.Name, selected, artifactHubSearchResults(classic, oci), verify, associate)
	if err != nil || got == nil || *got != selected.chartSource() || verified != 1 || associated != 1 {
		t.Fatalf("selected recovery = %+v, %v; verify=%d associate=%d", got, err, verified, associated)
	}
}

func TestArtifactHubRecoveryRefusesUnlistedSelection(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	cfg, rel := artifactHubRecoveryRelease(t, artifactHubTestChart("app", "1.2.3", "installed"))
	verifyCalls := 0
	_, err := c.recoverArtifactHubSourceWith(context.Background(), cfg, rel.Name,
		ArtifactHubSourceCandidate{Type: "repository", Reference: "injected", URL: "https://injected.example.test"},
		artifactHubSearchResults(ArtifactHubChart{Name: "app", Repository: ArtifactHubRepository{Name: "listed", URL: "https://listed.example.test"}}),
		func(context.Context, *action.Configuration, *release.Release, ChartSourceCandidate) (ChartSourceCandidate, error) {
			verifyCalls++
			return ChartSourceCandidate{}, nil
		},
		func(*action.Configuration, *release.Release, ChartSourceCandidate) (ChartSourceCandidate, error) {
			return ChartSourceCandidate{}, nil
		},
	)
	if err == nil || !errors.Is(err, errInvalidChartSource) || verifyCalls != 0 {
		t.Fatalf("unlisted selection = %v, verify calls %d", err, verifyCalls)
	}
}

func TestArtifactHubRecoveryGatesRecordedAndConfiguredSourcesBeforeSearch(t *testing.T) {
	withOCISources(t, nil)
	server := chartSourceTestServer(t, "1.2.3")
	defer server.Close()
	c := newChartSourceTestClient(t)
	if _, err := c.ensureClassicRepository(server.URL, "repo"); err != nil {
		t.Fatal(err)
	}
	source := ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL}
	for _, recorded := range []bool{false, true} {
		installed := artifactHubTestChart("app", "1.2.3", "installed")
		cfg, rel := artifactHubRecoveryRelease(t, installed)
		if recorded {
			rel.Labels = chartSourceLabels(&source)
			cfg = chartSourceActionConfig(t, rel)
		}
		searches := 0
		_, err := c.searchArtifactHubRecoverySourcesWith(context.Background(), cfg, rel.Name, func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
			searches++
			return &ArtifactHubSearchResult{}, nil
		})
		if err == nil || searches != 0 {
			t.Fatalf("recorded=%v: err=%v searches=%d, want pre-search refusal", recorded, err, searches)
		}
	}
}

func TestArtifactHubClassicVerificationUsesStoredChartMatch(t *testing.T) {
	withOCISources(t, nil)
	installed := artifactHubTestChart("app", "1.2.3", "kind: ConfigMap\nmetadata:\n  name: installed")
	cfg, rel := artifactHubRecoveryRelease(t, installed)

	t.Run("matching chart", func(t *testing.T) {
		server := artifactHubRepository(t, artifactHubTestChart("app", "1.2.3", "kind: ConfigMap\nmetadata:\n  name: installed"))
		defer server.Close()
		c := newChartSourceTestClient(t)
		got, err := c.verifyArtifactHubSource(context.Background(), cfg, rel, ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL})
		if err != nil || got.URL != server.URL {
			t.Fatalf("matching source = %+v, %v", got, err)
		}
	})

	t.Run("same name and version but different template", func(t *testing.T) {
		server := artifactHubRepository(t, artifactHubTestChart("app", "1.2.3", "kind: Secret\nmetadata:\n  name: impostor"))
		defer server.Close()
		c := newChartSourceTestClient(t)
		_, err := c.verifyArtifactHubSource(context.Background(), cfg, rel, ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL})
		if err == nil || !strings.Contains(err.Error(), "parent template") {
			t.Fatalf("impostor accepted or unclear error: %v", err)
		}
	})
}

func TestArtifactHubVerificationRejectsIncompleteUmbrella(t *testing.T) {
	withOCISources(t, nil)
	installed := artifactHubTestChart("umbrella", "1.0.0", "kind: ConfigMap")
	packaged := artifactHubTestChart("umbrella", "1.0.0", "kind: ConfigMap")
	packaged.Metadata.Dependencies = []*chart.Dependency{{Name: "child", Version: "2.0.0", Repository: "https://charts.example.test"}}
	server := artifactHubRepository(t, packaged)
	defer server.Close()
	cfg, rel := artifactHubRecoveryRelease(t, installed)
	_, err := newChartSourceTestClient(t).verifyArtifactHubSource(context.Background(), cfg, rel, ChartSourceCandidate{Type: "repository", Reference: "repo", URL: server.URL})
	if err == nil || !strings.Contains(err.Error(), "dependency") {
		t.Fatalf("incomplete umbrella accepted or unclear error: %v", err)
	}
}

func TestArtifactHubOCIVerificationMatchesCompleteStoredChart(t *testing.T) {
	withOCISources(t, nil)
	installed := artifactHubTestChart("app", "1.2.3", "kind: ConfigMap")
	path, err := chartutil.Save(artifactHubTestChart("app", "1.2.3", "kind: ConfigMap"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newChartSourceTestClient(t)
	c.chartPathLocator = func(_ *action.Configuration, ref, version string) (string, error) {
		if ref != "oci://ghcr.io/example/charts/app" || version != "1.2.3" {
			t.Fatalf("OCI pull = %q version %q", ref, version)
		}
		return path, nil
	}
	cfg, rel := artifactHubRecoveryRelease(t, installed)
	if _, err := c.verifyArtifactHubSource(context.Background(), cfg, rel, ChartSourceCandidate{Type: "oci", Reference: "oci://ghcr.io/example/charts/app"}); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactHubVerificationNeverSendsRepositoryCredentialsToArchiveHost(t *testing.T) {
	withOCISources(t, nil)
	ch := artifactHubTestChart("app", "1.2.3", "kind: ConfigMap")
	archive := artifactHubChartArchive(t, ch)
	digest := sha256.Sum256(archive)
	archiveAuthorization := "unset"
	archiveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		archiveAuthorization = r.Header.Get("Authorization")
		_, _ = w.Write(archive)
	}))
	defer archiveServer.Close()
	indexAuthorization := ""
	indexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		indexAuthorization = r.Header.Get("Authorization")
		_, _ = fmt.Fprintf(w, "apiVersion: v1\nentries:\n  app:\n  - name: app\n    version: 1.2.3\n    urls: [%s/app-1.2.3.tgz]\n    digest: %s\n", archiveServer.URL, hex.EncodeToString(digest[:]))
	}))
	defer indexServer.Close()

	c := newChartSourceTestClient(t)
	f := repo.NewFile()
	f.Update(&repo.Entry{Name: "private", URL: indexServer.URL, Username: "reader", Password: "secret", PassCredentialsAll: true})
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.loadExactHTTPChart(context.Background(), indexServer.URL, "app", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if indexAuthorization == "" {
		t.Fatal("configured credentials were not used for the selected repository")
	}
	if archiveAuthorization != "" {
		t.Fatalf("credentials leaked to index-selected archive host: %q", archiveAuthorization)
	}
}

func TestArtifactHubVerificationStripsCredentialsOnCrossHostRedirect(t *testing.T) {
	targetAuthorization := "unset"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetAuthorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("archive"))
	}))
	defer target.Close()

	sourceAuthorization := ""
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceAuthorization = r.Header.Get("Authorization")
		http.Redirect(w, r, target.URL+"/app.tgz", http.StatusFound)
	}))
	defer source.Close()

	credentials := &repo.Entry{Username: "reader", Password: "secret", PassCredentialsAll: true}
	if _, _, err := fetchArtifactHubVerificationBytes(context.Background(), source.URL+"/app.tgz", 1024, "chart", credentials, source.URL); err != nil {
		t.Fatal(err)
	}
	if sourceAuthorization == "" {
		t.Fatal("configured credentials were not used for the selected repository host")
	}
	if targetAuthorization != "" {
		t.Fatalf("credentials leaked across redirect: %q", targetAuthorization)
	}
}

func TestArtifactHubVerificationClassifiesAuthenticationAndInfrastructureErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind string
	}{
		{name: "authentication", err: &artifactHubHTTPStatusError{kind: "chart", statusCode: http.StatusUnauthorized, status: "401 Unauthorized"}, kind: "authentication"},
		{name: "service", err: &artifactHubHTTPStatusError{kind: "chart", statusCode: http.StatusServiceUnavailable, status: "503 Service Unavailable"}, kind: "infrastructure"},
		{name: "DNS", err: &url.Error{Op: "Get", URL: "https://charts.invalid", Err: &netDNSError{}}, kind: "infrastructure"},
		{name: "timeout", err: context.DeadlineExceeded, kind: "infrastructure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyArtifactHubVerificationError(tc.err)
			var classified *artifactHubVerificationError
			if !errors.As(got, &classified) || classified.kind != tc.kind {
				t.Fatalf("classification = %T %+v, want %s", got, got, tc.kind)
			}
		})
	}
}

type netDNSError struct{}

func (*netDNSError) Error() string   { return "DNS lookup failed" }
func (*netDNSError) Timeout() bool   { return false }
func (*netDNSError) Temporary() bool { return false }

func TestArtifactHubClassicRecoveryAssociatesWithoutChangingRevision(t *testing.T) {
	withOCISources(t, nil)
	installed := artifactHubTestChart("app", "1.2.3", "kind: ConfigMap")
	server := artifactHubRepository(t, artifactHubTestChart("app", "1.2.3", "kind: ConfigMap"))
	defer server.Close()
	c := newChartSourceTestClient(t)
	cfg, rel := artifactHubRecoveryRelease(t, installed)
	hit := ArtifactHubChart{Name: "app", Repository: ArtifactHubRepository{Name: "publisher", URL: server.URL, OrganizationName: "Publisher", VerifiedPublisher: true}}
	selected, _ := artifactHubRecoveryCandidate(hit, "app")
	got, err := c.recoverArtifactHubSourceWith(context.Background(), cfg, rel.Name, selected, artifactHubSearchResults(hit), c.verifyArtifactHubSource, c.associateVerifiedArtifactHubSource)
	if err != nil || got == nil || got.Type != "repository" || got.URL != server.URL {
		t.Fatalf("recovery = %+v, %v", got, err)
	}
	stored, err := cfg.Releases.Last(rel.Name)
	if err != nil || stored.Version != 1 {
		t.Fatalf("release revision changed: %+v, %v", stored, err)
	}
	if source, ok := chartSourceFromRelease(stored); !ok || source.URL != server.URL || strings.Contains(source.URL, "reader") {
		t.Fatalf("stored source = %+v, %v", source, ok)
	}
	configured, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil || configured.Get(got.Reference) == nil {
		t.Fatalf("repository not configured: %+v, %v", configured, err)
	}
}

func TestArtifactHubOCIRecoveryRegistersPrefixAndAssociates(t *testing.T) {
	withOCISources(t, nil)
	installed := artifactHubTestChart("app", "1.2.3", "kind: ConfigMap")
	path, err := chartutil.Save(artifactHubTestChart("app", "1.2.3", "kind: ConfigMap"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := newChartSourceTestClient(t)
	c.chartPathLocator = func(*action.Configuration, string, string) (string, error) { return path, nil }
	cfg, rel := artifactHubRecoveryRelease(t, installed)
	hit := ArtifactHubChart{Name: "app", Repository: ArtifactHubRepository{Name: "oci", URL: "oci://ghcr.io/example/charts/app"}}
	selected, _ := artifactHubRecoveryCandidate(hit, "app")
	got, err := c.recoverArtifactHubSourceWith(context.Background(), cfg, rel.Name, selected, artifactHubSearchResults(hit), c.verifyArtifactHubSource, c.associateVerifiedArtifactHubSource)
	if err != nil || got == nil || got.Reference != "oci://ghcr.io/example/charts/app" {
		t.Fatalf("OCI recovery = %+v, %v", got, err)
	}
	if !slices.Contains(ListOCISources(), "oci://ghcr.io/example/charts") {
		t.Fatalf("OCI prefix not registered: %v", ListOCISources())
	}
	stored, _ := cfg.Releases.Last(rel.Name)
	if source, ok := chartSourceFromRelease(stored); !ok || source.Reference != got.Reference {
		t.Fatalf("stored OCI source = %+v, %v", source, ok)
	}
}

func TestSearchArtifactHubRecoveryAtSendsOnlyChartNameAndTrustFilters(t *testing.T) {
	var query url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"packages":[{"name":"app","version":"9.9.9","official":true,"repository":{"name":"repo","url":"https://charts.example.test","official":false,"verified_publisher":true,"user_alias":"Publisher Alias"}}]}`))
	}))
	defer server.Close()
	oldClient := httpClient
	httpClient = server.Client()
	defer func() { httpClient = oldClient }()
	result, err := searchArtifactHubRecoveryAt(context.Background(), server.URL, "app", 0, 50, false, false, "relevance")
	if err != nil || len(result.Charts) != 1 || !result.Charts[0].Official || !result.Charts[0].Repository.VerifiedPublisher {
		t.Fatalf("search result = %+v, %v", result, err)
	}
	candidate, ok := artifactHubRecoveryCandidate(result.Charts[0], "app")
	if !ok || !candidate.Official || !candidate.VerifiedPublisher || candidate.Publisher != "Publisher Alias" {
		t.Fatalf("recovery candidate = %+v, %v", candidate, ok)
	}
	if query.Get("ts_query_web") != "app" || query.Get("kind") != "0" || query.Has("official") || query.Has("verified_publisher") || len(query) != 4 {
		t.Fatalf("unexpected ArtifactHub query: %v", query)
	}
}

func TestArtifactHubSearchInfrastructureErrorDoesNotReturnPartialCandidates(t *testing.T) {
	withOCISources(t, nil)
	c := newChartSourceTestClient(t)
	cfg, rel := artifactHubRecoveryRelease(t, artifactHubTestChart("app", "1.2.3", "installed"))
	calls := 0
	_, err := c.searchArtifactHubRecoverySourcesWith(context.Background(), cfg, rel.Name, func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error) {
		calls++
		if calls == 1 {
			charts := make([]ArtifactHubChart, artifactHubRecoveryPageSize)
			for i := range charts {
				charts[i] = ArtifactHubChart{Name: "app", Repository: ArtifactHubRepository{Name: fmt.Sprintf("repo-%d", i), URL: fmt.Sprintf("https://repo-%d.example.test", i)}}
			}
			return &ArtifactHubSearchResult{Charts: charts}, nil
		}
		return nil, &artifactHubHTTPStatusError{kind: "ArtifactHub search", statusCode: 503, status: "503 Service Unavailable"}
	})
	var classified *artifactHubVerificationError
	if err == nil || !errors.As(err, &classified) || classified.kind != "infrastructure" {
		t.Fatalf("search error = %T %v", err, err)
	}
}
