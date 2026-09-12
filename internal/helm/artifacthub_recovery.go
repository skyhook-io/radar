package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

type artifactHubRecoverySearch func(context.Context, string, int, int, bool, bool, string) (*ArtifactHubSearchResult, error)
type artifactHubRecoveryVerifier func(context.Context, *action.Configuration, *release.Release, ChartSourceCandidate) (ChartSourceCandidate, error)

type artifactHubVerificationError struct {
	kind string
	err  error
}

func (e *artifactHubVerificationError) Error() string { return e.err.Error() }
func (e *artifactHubVerificationError) Unwrap() error { return e.err }

// DiscoverArtifactHubSources performs the optional, user-triggered final source
// discovery step. ArtifactHub only supplies possible locations; every returned
// candidate has independently yielded the exact complete installed chart.
func (c *Client) DiscoverArtifactHubSources(ctx context.Context, namespace, name string) ([]ChartSourceCandidate, error) {
	cfg, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.discoverArtifactHubSourcesWith(ctx, cfg, name, SearchArtifactHubContext, c.verifyArtifactHubSource)
}

func (c *Client) DiscoverArtifactHubSourcesAsUser(ctx context.Context, namespace, name, username string, groups []string) ([]ChartSourceCandidate, error) {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.discoverArtifactHubSourcesWith(ctx, cfg, name, SearchArtifactHubContext, c.verifyArtifactHubSource)
}

func (c *Client) discoverArtifactHubSourcesWith(ctx context.Context, cfg *action.Configuration, releaseName string, search artifactHubRecoverySearch, verify artifactHubRecoveryVerifier) ([]ChartSourceCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, artifactHubRecoveryTimeout)
	defer cancel()

	rel, err := action.NewGet(cfg).Run(releaseName)
	if err != nil {
		return nil, fmt.Errorf("get release for source discovery: %w", err)
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil || rel.Chart.Metadata.Name == "" || rel.Chart.Metadata.Version == "" {
		return nil, fmt.Errorf("release has no usable chart name and version")
	}

	// Recorded provenance remains authoritative while it is usable. If it has
	// become unavailable, an explicit ArtifactHub request may suggest a
	// replacement, but discovery never persists that replacement itself.
	var unavailableRecorded *ChartSourceCandidate
	if recorded, ok := chartSourceFromRelease(rel); ok && (recorded.Type == "oci" || recorded.URL != "") {
		if _, verifyErr := verify(ctx, cfg, rel, *recorded); verifyErr == nil {
			return nil, fmt.Errorf("release already has exact recorded chart-source provenance")
		}
		unavailableRecorded = recorded
	}
	configured, err := c.configuredChartSourceCandidates(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, nil)
	if err != nil {
		return nil, fmt.Errorf("check configured chart sources before ArtifactHub: %w", err)
	}
	if unavailableRecorded != nil {
		configured = slices.DeleteFunc(configured, func(candidate ChartSourceCandidate) bool {
			return chartSourceCandidatesEquivalent(candidate, *unavailableRecorded)
		})
	}
	if len(configured) != 0 {
		return nil, fmt.Errorf("an exact configured chart source is already available")
	}

	// The search receives only the chart name. Namespace, release name, cluster
	// identity, credentials, and kubeconfig data never leave Radar.
	hits := make([]ArtifactHubChart, 0, artifactHubRecoveryPageSize)
	for offset := 0; offset < artifactHubRecoveryMaxResults; offset += artifactHubRecoveryPageSize {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("ArtifactHub discovery stopped: %w", err)
		}
		result, searchErr := search(ctx, rel.Chart.Metadata.Name, offset, artifactHubRecoveryPageSize, false, false, "relevance")
		if searchErr != nil {
			return nil, fmt.Errorf("ArtifactHub discovery failed: %w", searchErr)
		}
		if result == nil {
			return nil, fmt.Errorf("ArtifactHub discovery returned an empty response")
		}
		remaining := artifactHubRecoveryMaxResults - len(hits)
		page := result.Charts
		if len(page) > remaining {
			page = page[:remaining]
		}
		hits = append(hits, page...)
		if len(result.Charts) < artifactHubRecoveryPageSize || len(hits) >= artifactHubRecoveryMaxResults {
			break
		}
	}
	verified := make([]ChartSourceCandidate, 0)
	seen := make(map[string]struct{})
	var authFailure, infrastructureFailure error
	for _, hit := range hits {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("ArtifactHub verification stopped: %w", err)
		}
		if hit.Name != rel.Chart.Metadata.Name {
			continue
		}
		candidate, ok := artifactHubRecoveryCandidate(hit, rel.Chart.Metadata.Name)
		if !ok {
			continue
		}
		key := chartSourceCandidateIdentity(candidate)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		verifiedCandidate, verifyErr := verify(ctx, cfg, rel, candidate)
		if verifyErr != nil {
			var classified *artifactHubVerificationError
			if errors.As(verifyErr, &classified) {
				switch classified.kind {
				case "authentication":
					authFailure = verifyErr
				case "infrastructure":
					infrastructureFailure = verifyErr
				}
			}
			continue
		}
		verifiedKey := chartSourceCandidateIdentity(verifiedCandidate)
		if slices.ContainsFunc(verified, func(existing ChartSourceCandidate) bool {
			return chartSourceCandidateIdentity(existing) == verifiedKey
		}) {
			continue
		}
		verified = append(verified, verifiedCandidate)
	}
	if len(verified) == 0 {
		if authFailure != nil {
			return nil, fmt.Errorf("source found but verification requires authentication using existing Helm credentials: %w", authFailure)
		}
		if infrastructureFailure != nil {
			return nil, fmt.Errorf("source found but verification failed due to a network or service error: %w", infrastructureFailure)
		}
	}
	slices.SortFunc(verified, func(a, b ChartSourceCandidate) int {
		return strings.Compare(a.Type+"\x00"+a.Reference+"\x00"+a.URL, b.Type+"\x00"+b.Reference+"\x00"+b.URL)
	})
	return verified, nil
}

func chartSourceCandidateIdentity(candidate ChartSourceCandidate) string {
	if candidate.Type == "repository" {
		return candidate.Type + "\x00" + candidate.URL
	}
	return candidate.Type + "\x00" + candidate.Reference
}

func chartSourceCandidatesEquivalent(a, b ChartSourceCandidate) bool {
	return a.Type == b.Type && chartSourceCandidateIdentity(a) == chartSourceCandidateIdentity(b)
}

func artifactHubRecoveryCandidate(hit ArtifactHubChart, chartName string) (ChartSourceCandidate, bool) {
	raw := strings.TrimSpace(hit.Repository.URL)
	if strings.HasPrefix(raw, "oci://") {
		ref, err := normalizeOCIPrefix(raw)
		if err != nil || !strings.HasSuffix(ref, "/"+chartName) {
			return ChartSourceCandidate{}, false
		}
		candidate := ChartSourceCandidate{Type: "oci", Reference: ref}
		return candidate, validateChartSourceCandidate(&candidate) == nil
	}
	repositoryURL, err := canonicalClassicRepositoryURL(raw)
	if err != nil {
		return ChartSourceCandidate{}, false
	}
	name := normalizedRepositoryName(hit.Repository.Name)
	if name == "" {
		name = stableRepositoryName(hit.Repository.Name, repositoryURL)
	}
	candidate := ChartSourceCandidate{Type: "repository", Reference: name, URL: repositoryURL}
	return candidate, validateChartSourceCandidate(&candidate) == nil
}

func (c *Client) verifyArtifactHubSource(ctx context.Context, cfg *action.Configuration, rel *release.Release, candidate ChartSourceCandidate) (ChartSourceCandidate, error) {
	if err := ctx.Err(); err != nil {
		return ChartSourceCandidate{}, classifyArtifactHubVerificationError(err)
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
		copy := *rel
		copy.Labels = mergeChartSourceLabels(rel.Labels, &candidate)
		loaded, err = c.loadTargetChart(cfg, &copy, rel.Chart.Metadata.Version, "", func(string, string, string) {})
	default:
		err = fmt.Errorf("unsupported chart source type %q", candidate.Type)
	}
	if err != nil {
		return ChartSourceCandidate{}, classifyArtifactHubVerificationError(err)
	}
	if loaded.Metadata == nil || loaded.Metadata.Name != rel.Chart.Metadata.Name || loaded.Metadata.Version != rel.Chart.Metadata.Version {
		return ChartSourceCandidate{}, fmt.Errorf("source returned a different chart identity or version")
	}
	if err := validateChartDependencyBodies(loaded); err != nil {
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
	indexBody, finalIndexURL, err := fetchArtifactHubVerificationBytes(ctx, repositoryURL+"/index.yaml", maxPreparedIndexBytes, "repository index", credentials)
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
	for _, candidate := range index.Entries[chartName] {
		if candidate != nil && candidate.Metadata != nil && !candidate.Removed && candidate.Version == version && len(candidate.URLs) > 0 {
			selectedURL, digest = candidate.URLs[0], strings.TrimSpace(strings.ToLower(candidate.Digest))
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
	body, _, err := fetchArtifactHubVerificationBytes(ctx, chartURL, maxPreparedChartBytes, "chart", credentials)
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

func fetchArtifactHubVerificationBytes(ctx context.Context, target string, maxBytes int64, kind string, credentials *repo.Entry) ([]byte, *url.URL, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", kind, err)
	}
	if credentials != nil && (credentials.Username != "" || credentials.Password != "") {
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
		if credentials != nil && (credentials.Username != "" || credentials.Password != "") &&
			(credentials.PassCredentialsAll || strings.EqualFold(previous.Hostname(), next.URL.Hostname())) {
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
	if strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication required") || strings.Contains(lower, "denied") || strings.Contains(lower, "401") || strings.Contains(lower, "403") {
		return &artifactHubVerificationError{kind: "authentication", err: err}
	}
	return err
}
