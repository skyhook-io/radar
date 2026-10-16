package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/yaml"
)

const (
	artifactHubRecoveryPageSize   = 50
	artifactHubRecoveryMaxResults = 250
	artifactHubRecoveryTimeout    = 20 * time.Second
	artifactHubRecoveryMaxBytes   = 4 << 20
)

type artifactHubRecoverySearch func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error)
type artifactHubRecoveryVerifier func(context.Context, *action.Configuration, *release.Release, ChartSourceCandidate) (ChartSourceCandidate, error)
type artifactHubRecoveryAssociator func(*action.Configuration, *release.Release, ChartSourceCandidate) (ChartSourceCandidate, error)

type artifactHubVerificationError struct {
	kind string
	err  error
}

func (e *artifactHubVerificationError) Error() string { return e.err.Error() }
func (e *artifactHubVerificationError) Unwrap() error { return e.err }

// SearchArtifactHubRecoverySources is the explicit, read-only discovery step.
// ArtifactHub receives only the installed chart name. Results are unverified:
// no repository index, chart archive, or OCI artifact is fetched here.
func (c *Client) SearchArtifactHubRecoverySources(ctx context.Context, namespace, name string) ([]ArtifactHubSourceCandidate, error) {
	cfg, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.searchArtifactHubRecoverySourcesWith(ctx, cfg, name, searchArtifactHubRecovery)
}

func (c *Client) SearchArtifactHubRecoverySourcesAsUser(ctx context.Context, namespace, name, username string, groups []string) ([]ArtifactHubSourceCandidate, error) {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.searchArtifactHubRecoverySourcesWith(ctx, cfg, name, searchArtifactHubRecovery)
}

func (c *Client) searchArtifactHubRecoverySourcesWith(ctx context.Context, cfg *action.Configuration, releaseName string, search artifactHubRecoverySearch) ([]ArtifactHubSourceCandidate, error) {
	rel, err := c.artifactHubRecoveryRelease(cfg, releaseName)
	if err != nil {
		return nil, err
	}
	return searchArtifactHubRecoveryCandidates(ctx, rel.Chart.Metadata.Name, search)
}

// RecoverArtifactHubSource verifies and associates exactly the candidate the
// operator selected. It is deliberately separate from search so common chart
// names never fan out into index and archive downloads.
func (c *Client) RecoverArtifactHubSource(ctx context.Context, namespace, name string, selected ArtifactHubSourceCandidate) (*ChartSourceCandidate, error) {
	cfg, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.recoverArtifactHubSourceWith(ctx, cfg, name, selected, searchArtifactHubRecovery, c.verifyArtifactHubSource, c.associateVerifiedArtifactHubSource)
}

func (c *Client) RecoverArtifactHubSourceAsUser(ctx context.Context, namespace, name string, selected ArtifactHubSourceCandidate, username string, groups []string) (*ChartSourceCandidate, error) {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.recoverArtifactHubSourceWith(ctx, cfg, name, selected, searchArtifactHubRecovery, c.verifyArtifactHubSource, c.associateVerifiedArtifactHubSource)
}

func (c *Client) recoverArtifactHubSourceWith(ctx context.Context, cfg *action.Configuration, releaseName string, selected ArtifactHubSourceCandidate, search artifactHubRecoverySearch, verify artifactHubRecoveryVerifier, associate artifactHubRecoveryAssociator) (*ChartSourceCandidate, error) {
	rel, err := c.artifactHubRecoveryRelease(cfg, releaseName)
	if err != nil {
		return nil, err
	}
	candidates, err := searchArtifactHubRecoveryCandidates(ctx, rel.Chart.Metadata.Name, search)
	if err != nil {
		return nil, err
	}
	var actual *ArtifactHubSourceCandidate
	for i := range candidates {
		if chartSourceCandidateIdentity(candidates[i].chartSource()) == chartSourceCandidateIdentity(selected.chartSource()) {
			actual = &candidates[i]
			break
		}
	}
	if actual == nil {
		return nil, fmt.Errorf("%w: selected source is not an ArtifactHub result for chart %s", errInvalidChartSource, rel.Chart.Metadata.Name)
	}
	verified, err := verify(ctx, cfg, rel, actual.chartSource())
	if err != nil {
		return nil, classifyArtifactHubVerificationError(err)
	}
	persisted, err := associate(cfg, rel, verified)
	if err != nil {
		return nil, err
	}
	return &persisted, nil
}

func (c *Client) artifactHubRecoveryRelease(cfg *action.Configuration, releaseName string) (*release.Release, error) {
	status, err := c.sourceStatusWith(cfg, releaseName)
	if err != nil {
		return nil, fmt.Errorf("check chart sources before ArtifactHub recovery: %w", err)
	}
	if status.Recorded != nil && status.Configured && status.Available {
		return nil, fmt.Errorf("release already has usable recorded chart-source provenance")
	}
	if len(status.Candidates) != 0 {
		return nil, fmt.Errorf("an exact configured chart source is already available")
	}
	rel, err := action.NewGet(cfg).Run(releaseName)
	if err != nil {
		return nil, fmt.Errorf("get release for ArtifactHub recovery: %w", err)
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil || rel.Chart.Metadata.Name == "" || rel.Chart.Metadata.Version == "" {
		return nil, fmt.Errorf("release has no usable chart name and version")
	}
	if rel.Info == nil || releaseChanging(rel.Info.Status) {
		return nil, fmt.Errorf("%w: another Helm operation on %s is in progress or just finished; try again", errReleaseChanging, releaseName)
	}
	return rel, nil
}

func searchArtifactHubRecoveryCandidates(ctx context.Context, chartName string, search artifactHubRecoverySearch) ([]ArtifactHubSourceCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, artifactHubRecoveryTimeout)
	defer cancel()

	found := make([]ArtifactHubSourceCandidate, 0, artifactHubRecoveryPageSize)
	byIdentity := make(map[string]int)
	for offset := 0; offset < artifactHubRecoveryMaxResults; offset += artifactHubRecoveryPageSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("ArtifactHub discovery stopped: %w", err)
		}
		// Do not add cluster, release, namespace, version, or credential data.
		result, err := search(ctx, chartName, offset, artifactHubRecoveryPageSize, false, false, "relevance")
		if err != nil {
			return nil, fmt.Errorf("ArtifactHub discovery failed: %w", classifyArtifactHubVerificationError(err))
		}
		if result == nil {
			return nil, fmt.Errorf("ArtifactHub discovery returned an empty response")
		}
		for _, hit := range result.Charts {
			if hit.Name != chartName {
				continue
			}
			candidate, ok := artifactHubRecoveryCandidate(hit, chartName)
			if !ok {
				continue
			}
			key := chartSourceCandidateIdentity(candidate.chartSource())
			if existing, ok := byIdentity[key]; ok {
				found[existing].Official = found[existing].Official || candidate.Official
				found[existing].VerifiedPublisher = found[existing].VerifiedPublisher || candidate.VerifiedPublisher
				if found[existing].Publisher == "" {
					found[existing].Publisher = candidate.Publisher
				}
				continue
			}
			byIdentity[key] = len(found)
			found = append(found, candidate)
			if len(found) == artifactHubRecoveryMaxResults {
				break
			}
		}
		if len(result.Charts) < artifactHubRecoveryPageSize || len(found) == artifactHubRecoveryMaxResults {
			break
		}
	}
	slices.SortFunc(found, func(a, b ArtifactHubSourceCandidate) int {
		if a.Official != b.Official {
			if a.Official {
				return -1
			}
			return 1
		}
		if a.VerifiedPublisher != b.VerifiedPublisher {
			if a.VerifiedPublisher {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Publisher+"\x00"+a.RepositoryName+"\x00"+a.Reference+"\x00"+a.URL, b.Publisher+"\x00"+b.RepositoryName+"\x00"+b.Reference+"\x00"+b.URL)
	})
	return found, nil
}

func chartSourceCandidateIdentity(candidate ChartSourceCandidate) string {
	if candidate.Type == "repository" {
		return candidate.Type + "\x00" + candidate.URL
	}
	return candidate.Type + "\x00" + candidate.Reference
}

func artifactHubRecoveryCandidate(hit ArtifactHubChart, chartName string) (ArtifactHubSourceCandidate, bool) {
	raw := strings.TrimSpace(hit.Repository.URL)
	candidate := ArtifactHubSourceCandidate{
		RepositoryName:    hit.Repository.Name,
		Publisher:         hit.Repository.OrganizationDisplayName,
		Official:          hit.Official || hit.Repository.Official,
		VerifiedPublisher: hit.Repository.VerifiedPublisher,
	}
	if candidate.Publisher == "" {
		candidate.Publisher = hit.Repository.OrganizationName
	}
	if candidate.Publisher == "" {
		candidate.Publisher = hit.Repository.UserAlias
	}
	if candidate.Publisher == "" {
		candidate.Publisher = hit.Repository.DisplayName
	}
	if candidate.Publisher == "" {
		candidate.Publisher = hit.Repository.Name
	}
	if strings.HasPrefix(raw, "oci://") {
		ref, err := normalizeOCIPrefix(raw)
		if err != nil || !strings.HasSuffix(ref, "/"+chartName) {
			return ArtifactHubSourceCandidate{}, false
		}
		candidate.Type, candidate.Reference = "oci", ref
		return candidate, validateChartSourceCandidate(ptr(candidate.chartSource())) == nil
	}
	repositoryURL, err := canonicalClassicRepositoryURL(raw)
	if err != nil {
		return ArtifactHubSourceCandidate{}, false
	}
	name := normalizedRepositoryName(hit.Repository.Name)
	if name == "" {
		name = stableRepositoryName(hit.Repository.Name, repositoryURL)
	}
	candidate.Type, candidate.Reference, candidate.URL = "repository", name, repositoryURL
	return candidate, validateChartSourceCandidate(ptr(candidate.chartSource())) == nil
}

func ptr[T any](value T) *T { return &value }

func (candidate ArtifactHubSourceCandidate) chartSource() ChartSourceCandidate {
	return ChartSourceCandidate{Type: candidate.Type, Reference: candidate.Reference, URL: candidate.URL}
}

func (c *Client) verifyArtifactHubSource(ctx context.Context, cfg *action.Configuration, rel *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
	if err := ctx.Err(); err != nil {
		return ChartSourceCandidate{}, err
	}
	var loaded *chart.Chart
	var err error
	switch candidate.Type {
	case "repository":
		var canonicalURL string
		loaded, canonicalURL, err = c.loadExactHTTPChart(ctx, candidate.URL, rel.Chart.Metadata.Name, rel.Chart.Metadata.Version)
		if err == nil {
			candidate.URL = canonicalURL
		}
	case "oci":
		var path string
		path, err = c.locateChartPath(cfg, candidate.Reference, rel.Chart.Metadata.Version)
		if err == nil {
			loaded, err = loader.Load(path)
		}
	default:
		err = fmt.Errorf("unsupported chart source type %q", candidate.Type)
	}
	if err != nil {
		return ChartSourceCandidate{}, err
	}
	if loaded.Metadata == nil || loaded.Metadata.Name != rel.Chart.Metadata.Name || loaded.Metadata.Version != rel.Chart.Metadata.Version {
		return ChartSourceCandidate{}, fmt.Errorf("source returned a different chart identity or version")
	}
	if err := validateChartDependencyBodies(loaded); err != nil {
		return ChartSourceCandidate{}, err
	}
	if err := storedChartMatchesReconstructed(rel.Chart, loaded); err != nil {
		return ChartSourceCandidate{}, fmt.Errorf("selected source does not match the chart stored in this release: %w", err)
	}
	return candidate, nil
}

func (c *Client) associateVerifiedArtifactHubSource(cfg *action.Configuration, rel *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
	switch candidate.Type {
	case "repository":
		name, err := c.ensureClassicRepository(candidate.URL, candidate.Reference)
		if err != nil {
			return ChartSourceCandidate{}, classifyArtifactHubVerificationError(err)
		}
		candidate.Reference = name
	case "oci":
		suffix := "/" + rel.Chart.Metadata.Name
		if !strings.HasSuffix(candidate.Reference, suffix) {
			return ChartSourceCandidate{}, fmt.Errorf("%w: OCI source does not end with chart name", errInvalidChartSource)
		}
		if _, err := AddOCISource(strings.TrimSuffix(candidate.Reference, suffix)); err != nil {
			return ChartSourceCandidate{}, err
		}
	default:
		return ChartSourceCandidate{}, fmt.Errorf("%w: unsupported chart source type %q", errInvalidChartSource, candidate.Type)
	}
	if err := c.persistVerifiedSource(cfg, rel, candidate); err != nil {
		return ChartSourceCandidate{}, err
	}
	return candidate, nil
}

func (c *Client) loadExactHTTPChart(ctx context.Context, repositoryURL, chartName, version string) (*chart.Chart, string, error) {
	repositoryURL, err := canonicalClassicRepositoryURL(repositoryURL)
	if err != nil {
		return nil, "", err
	}
	credentials := c.configuredRepositoryByURL(repositoryURL)
	indexBody, finalIndexURL, err := fetchArtifactHubVerificationBytes(ctx, repositoryURL+"/index.yaml", maxPreparedIndexBytes, "repository index", credentials, repositoryURL)
	if err != nil {
		return nil, "", err
	}
	index := &repo.IndexFile{}
	if err := yaml.Unmarshal(indexBody, index); err != nil {
		return nil, "", fmt.Errorf("parse repository index: %w", err)
	}
	index.SortEntries()
	baseURL := *finalIndexURL
	baseURL.RawQuery, baseURL.Fragment = "", ""
	baseURL.Path = strings.TrimSuffix(baseURL.Path, "index.yaml")
	canonicalURL, err := canonicalClassicRepositoryURL(baseURL.String())
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize redirected repository URL: %w", err)
	}
	var selectedURL, digest string
	for _, entry := range index.Entries[chartName] {
		if entry != nil && entry.Metadata != nil && !entry.Removed && entry.Version == version && len(entry.URLs) > 0 {
			selectedURL, digest = entry.URLs[0], strings.TrimSpace(strings.ToLower(entry.Digest))
			break
		}
	}
	if selectedURL == "" {
		return nil, "", fmt.Errorf("chart %s version %s is absent from repository", chartName, version)
	}
	want, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(want) != sha256.Size {
		return nil, "", fmt.Errorf("chart %s version %s has no valid SHA-256 digest", chartName, version)
	}
	chartURL, err := resolveChartURL(canonicalURL+"/", selectedURL)
	if err != nil {
		return nil, "", err
	}
	body, _, err := fetchArtifactHubVerificationBytes(ctx, chartURL, maxPreparedChartBytes, "chart", credentials, repositoryURL)
	if err != nil {
		return nil, "", err
	}
	actual := sha256.Sum256(body)
	if !bytes.Equal(actual[:], want) {
		return nil, "", fmt.Errorf("chart %s version %s digest does not match repository index", chartName, version)
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("load chart %s version %s: %w", chartName, version, err)
	}
	return loaded, canonicalURL, nil
}

func (c *Client) configuredRepositoryByURL(repositoryURL string) *repo.Entry {
	if c.settings == nil {
		return nil
	}
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		return nil
	}
	for _, configured := range f.Repositories {
		configuredURL, err := canonicalClassicRepositoryURL(configured.URL)
		if err == nil && configuredURL == repositoryURL {
			return configured
		}
	}
	return nil
}

func fetchArtifactHubVerificationBytes(ctx context.Context, target string, maxBytes int64, kind string, credentials *repo.Entry, credentialSource string) ([]byte, *url.URL, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", kind, err)
	}
	credentialHost := ""
	if sourceURL, parseErr := url.Parse(credentialSource); parseErr == nil {
		credentialHost = sourceURL.Host
	}
	if credentials != nil && credentialHost != "" && strings.EqualFold(request.URL.Host, credentialHost) {
		request.SetBasicAuth(credentials.Username, credentials.Password)
	}
	client := *httpClient
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		previous := via[len(via)-1].URL
		if next.URL.Scheme != "http" && next.URL.Scheme != "https" {
			return fmt.Errorf("repository redirect uses unsupported scheme %q", next.URL.Scheme)
		}
		if previous.Scheme == "https" && next.URL.Scheme != "https" {
			return fmt.Errorf("repository redirect would downgrade HTTPS")
		}
		if next.URL.User != nil {
			return fmt.Errorf("repository redirect contains credentials")
		}
		// net/http may copy Authorization before CheckRedirect. Remove it and
		// restore credentials only inside the selected repository's host.
		next.Header.Del("Authorization")
		if credentials != nil && credentialHost != "" && strings.EqualFold(next.URL.Host, credentialHost) {
			next.SetBasicAuth(credentials.Username, credentials.Password)
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", kind, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.Request.URL, &artifactHubHTTPStatusError{kind: kind, statusCode: response.StatusCode, status: response.Status}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, response.Request.URL, fmt.Errorf("read %s: %w", kind, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, response.Request.URL, fmt.Errorf("read %s: response exceeds %d bytes", kind, maxBytes)
	}
	return body, response.Request.URL, nil
}

func searchArtifactHubRecovery(ctx context.Context, query string, offset, limit int, official, verified bool, sortOrder string) (*ArtifactHubSearchResult, error) {
	return searchArtifactHubRecoveryAt(ctx, artifactHubBaseURL, query, offset, limit, official, verified, sortOrder)
}

func searchArtifactHubRecoveryAt(ctx context.Context, baseURL, query string, offset, limit int, official, verified bool, sortOrder string) (*ArtifactHubSearchResult, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/packages/search")
	if err != nil {
		return nil, err
	}
	params := endpoint.Query()
	params.Set("kind", "0")
	params.Set("ts_query_web", query)
	params.Set("offset", fmt.Sprint(offset))
	params.Set("limit", fmt.Sprint(limit))
	if official {
		params.Set("official", "true")
	}
	if verified {
		params.Set("verified_publisher", "true")
	}
	if sortOrder != "" && sortOrder != "relevance" {
		params.Set("sort", sortOrder)
	}
	endpoint.RawQuery = params.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &artifactHubHTTPStatusError{kind: "ArtifactHub search", statusCode: response.StatusCode, status: response.Status}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, artifactHubRecoveryMaxBytes+1))
	var apiResponse artifactHubSearchResponse
	if err := decoder.Decode(&apiResponse); err != nil {
		return nil, fmt.Errorf("parse ArtifactHub search response: %w", err)
	}
	result := &ArtifactHubSearchResult{Charts: make([]ArtifactHubChart, 0, len(apiResponse.Packages)), Total: len(apiResponse.Packages)}
	for _, pkg := range apiResponse.Packages {
		result.Charts = append(result.Charts, convertArtifactHubPackage(pkg))
	}
	return result, nil
}

type artifactHubHTTPStatusError struct {
	kind       string
	statusCode int
	status     string
}

func (e *artifactHubHTTPStatusError) Error() string {
	return fmt.Sprintf("fetch %s: server returned %s", e.kind, e.status)
}

func classifyArtifactHubVerificationError(err error) error {
	if err == nil {
		return nil
	}
	var classified *artifactHubVerificationError
	if errors.As(err, &classified) {
		return err
	}
	var statusErr *artifactHubHTTPStatusError
	if errors.As(err, &statusErr) {
		if statusErr.statusCode == http.StatusUnauthorized || statusErr.statusCode == http.StatusForbidden {
			return &artifactHubVerificationError{kind: "authentication", err: err}
		}
		if statusErr.statusCode == http.StatusTooManyRequests || statusErr.statusCode >= 500 {
			return &artifactHubVerificationError{kind: "infrastructure", err: err}
		}
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &artifactHubVerificationError{kind: "infrastructure", err: err}
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return &artifactHubVerificationError{kind: "infrastructure", err: err}
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication required") || strings.Contains(lower, "denied") {
		return &artifactHubVerificationError{kind: "authentication", err: err}
	}
	return err
}
