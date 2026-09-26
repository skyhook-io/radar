package helm

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	orasregistry "oras.land/oras-go/v2/registry"
)

const (
	chartSourceTypeLabel = "radarhq.io/chart-source-type"
	chartSourceRefLabel  = "radarhq.io/chart-source-ref-"
	chartSourceURLLabel  = "radarhq.io/chart-source-url-"
	chartSourceChunkSize = 63
	chartSourceMaxChunks = 16
)

var (
	repositoryNamePartRE        = regexp.MustCompile(`[^a-z0-9._-]+`)
	errInvalidChartSource       = errors.New("invalid chart source")
	errInvalidRepositoryRequest = errors.New("invalid repository request")
	errRepositoryConflict       = errors.New("repository configuration conflict")
)

func addChartSourceLabelChunks(labels map[string]string, prefix, value string) bool {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(value))
	if encoded == "" || len(encoded) > chartSourceChunkSize*chartSourceMaxChunks {
		return false
	}
	for i := 0; len(encoded) > 0; i++ {
		n := min(chartSourceChunkSize, len(encoded))
		labels[fmt.Sprintf("%s%d", prefix, i)] = encoded[:n]
		encoded = encoded[n:]
	}
	return true
}

func decodeChartSourceLabelChunks(labels map[string]string, prefix string) (string, bool) {
	var encoded strings.Builder
	for i := 0; i < chartSourceMaxChunks; i++ {
		chunk, ok := labels[fmt.Sprintf("%s%d", prefix, i)]
		if !ok {
			break
		}
		encoded.WriteString(chunk)
	}
	if encoded.Len() == 0 {
		return "", false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(encoded.String())
	return string(decoded), err == nil && len(decoded) > 0
}

func chartSourceLabels(source *ChartSourceCandidate) map[string]string {
	if source == nil || source.Reference == "" || (source.Type != "repository" && source.Type != "oci") {
		return nil
	}
	canonicalURL := ""
	if source.Type == "repository" {
		var err error
		canonicalURL, err = canonicalClassicRepositoryURL(source.URL)
		if err != nil {
			return nil
		}
	} else {
		if source.URL != "" || !strings.HasPrefix(source.Reference, "oci://") {
			return nil
		}
		stableReference, err := stableOCIChartReference(source.Reference)
		if err != nil || stableReference != source.Reference {
			return nil
		}
	}
	labels := map[string]string{chartSourceTypeLabel: source.Type}
	if !addChartSourceLabelChunks(labels, chartSourceRefLabel, source.Reference) {
		return nil
	}
	if canonicalURL != "" && !addChartSourceLabelChunks(labels, chartSourceURLLabel, canonicalURL) {
		return nil
	}
	return labels
}

func validateChartSourceCandidate(source *ChartSourceCandidate) error {
	if len(chartSourceLabels(source)) == 0 {
		return fmt.Errorf("%w: source is malformed or too long to persist safely", errInvalidChartSource)
	}
	return nil
}

func chartSourceFromRelease(rel *release.Release) (*ChartSourceCandidate, bool) {
	if rel == nil || rel.Labels == nil {
		return nil, false
	}
	typ := rel.Labels[chartSourceTypeLabel]
	if typ != "repository" && typ != "oci" {
		return nil, false
	}
	reference, ok := decodeChartSourceLabelChunks(rel.Labels, chartSourceRefLabel)
	if !ok {
		return nil, false
	}
	source := &ChartSourceCandidate{Type: typ, Reference: reference}
	if rawURL, ok := decodeChartSourceLabelChunks(rel.Labels, chartSourceURLLabel); ok {
		if typ != "repository" {
			return nil, false
		}
		canonicalURL, err := canonicalClassicRepositoryURL(rawURL)
		if err != nil {
			return nil, false
		}
		source.URL = canonicalURL
	}
	if err := validateChartSourceCandidate(source); err != nil {
		return nil, false
	}
	return source, true
}

func mergeChartSourceLabels(current map[string]string, source *ChartSourceCandidate) map[string]string {
	labels := make(map[string]string, len(current)+chartSourceMaxChunks+1)
	for k, v := range current {
		if k != chartSourceTypeLabel && !strings.HasPrefix(k, chartSourceRefLabel) && !strings.HasPrefix(k, chartSourceURLLabel) {
			labels[k] = v
		}
	}
	for k, v := range chartSourceLabels(source) {
		labels[k] = v
	}
	return labels
}

// chartSourceUpgradeLabels returns a Helm action label patch. Helm carries old
// labels forward on upgrade, so obsolete chunks must be explicitly removed.
func chartSourceUpgradeLabels(current map[string]string, source *ChartSourceCandidate) map[string]string {
	desired := chartSourceLabels(source)
	patch := make(map[string]string, len(desired)+chartSourceMaxChunks*2+1)
	for k := range current {
		if k == chartSourceTypeLabel || strings.HasPrefix(k, chartSourceRefLabel) || strings.HasPrefix(k, chartSourceURLLabel) {
			if _, ok := desired[k]; !ok {
				patch[k] = "null"
			}
		}
	}
	for k, v := range desired {
		patch[k] = v
	}
	return patch
}

func normalizedRepositoryName(preferred string) string {
	base := strings.ToLower(strings.TrimSpace(preferred))
	base = repositoryNamePartRE.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-._")
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "-._")
	}
	return base
}

func stableRepositoryName(preferred, rawURL string) string {
	base := normalizedRepositoryName(preferred)
	if base == "" {
		if parsed, err := url.Parse(rawURL); err == nil {
			base = normalizedRepositoryName(parsed.Hostname())
		}
	}
	if base == "" {
		base = "source"
	}
	sum := sha256.Sum256([]byte(rawURL))
	return fmt.Sprintf("radar-%s-%x", base, sum[:4])
}

func canonicalClassicRepositoryURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil {
		return "", fmt.Errorf("%w: invalid Helm repository URL", errInvalidRepositoryRequest)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("%w: invalid Helm repository URL", errInvalidRepositoryRequest)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: repository URL must not contain credentials", errInvalidRepositoryRequest)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: repository URL must not contain query credentials or fragments", errInvalidRepositoryRequest)
	}
	hostname := strings.ToLower(parsed.Hostname())
	if err := rejectLinkLocalHost(parsed.Host); err != nil {
		return "", fmt.Errorf("%w: %v", errInvalidRepositoryRequest, err)
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	parsed.Host = hostname
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.RawPath, "/")
	return parsed.String(), nil
}

// stableOCIChartReference removes the install-only tag or digest selector from
// an OCI reference. Provenance records the repository/chart identity so later
// tag discovery and exact-version resolution can use it.
func stableOCIChartReference(raw string) (string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsedURL.Scheme != "oci" || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return "", fmt.Errorf("%w: invalid OCI chart source", errInvalidChartSource)
	}
	parsedReference, err := orasregistry.ParseReference(strings.TrimPrefix(strings.TrimSpace(raw), "oci://"))
	if err != nil || parsedReference.Registry == "" || parsedReference.Repository == "" {
		return "", fmt.Errorf("%w: invalid OCI chart source", errInvalidChartSource)
	}
	return "oci://" + parsedReference.Registry + "/" + parsedReference.Repository, nil
}

// installChartSource derives provenance without mutating source configuration.
// Callers invoke it only after preInstallCheck has accepted the install.
func (c *Client) installChartSource(req *InstallRequest) (*ChartSourceCandidate, error) {
	if strings.HasPrefix(req.Repository, "oci://") {
		chartURL, err := resolveOCIChartURL(req.Repository, req.ChartName)
		if err != nil {
			return nil, err
		}
		stableReference, err := stableOCIChartReference(chartURL)
		if err != nil {
			return nil, err
		}
		return &ChartSourceCandidate{Type: "oci", Reference: stableReference}, nil
	}
	if strings.HasPrefix(req.Repository, "http://") || strings.HasPrefix(req.Repository, "https://") {
		repoURL, err := canonicalClassicRepositoryURL(req.Repository)
		if err != nil {
			return nil, err
		}
		name := normalizedRepositoryName(req.RepositoryName)
		if name == "" {
			name = stableRepositoryName("", repoURL)
		}
		return &ChartSourceCandidate{Type: "repository", Reference: name, URL: repoURL}, nil
	}
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load repo file: %w", err)
	}
	entry := f.Get(req.Repository)
	if entry == nil {
		return nil, fmt.Errorf("%w: repository %q not found", errInvalidRepositoryRequest, req.Repository)
	}
	repoURL, err := canonicalClassicRepositoryURL(entry.URL)
	if err != nil {
		return nil, err
	}
	return &ChartSourceCandidate{Type: "repository", Reference: entry.Name, URL: repoURL}, nil
}

// ensureClassicRepository downloads into an isolated directory before
// acquiring c.mu. The accepted alias is revalidated under the lock, then the
// repository config is persisted before the staged index is atomically
// published into the shared cache.
func (c *Client) ensureClassicRepository(rawURL, preferredName string) (string, error) {
	repoURL, err := canonicalClassicRepositoryURL(rawURL)
	if err != nil {
		return "", err
	}
	name := normalizedRepositoryName(preferredName)
	if name == "" {
		name = stableRepositoryName("", repoURL)
	}
	// Reuse the current machine's alias when the same canonical URL is already
	// configured. This snapshot is local I/O only; no network call occurs while
	// either mutex mode is held.
	c.mu.RLock()
	if existingFile, loadErr := repo.LoadFile(c.settings.RepositoryConfig); loadErr == nil {
		for _, existing := range existingFile.Repositories {
			existingURL, canonicalErr := canonicalClassicRepositoryURL(existing.URL)
			if canonicalErr == nil && existingURL == repoURL {
				name = existing.Name
				break
			}
		}
	}
	c.mu.RUnlock()
	entry := &repo.Entry{Name: name, URL: repoURL}
	if err := os.MkdirAll(c.settings.RepositoryCache, 0o755); err != nil {
		return "", fmt.Errorf("prepare repository cache: %w", err)
	}
	stageDir, err := os.MkdirTemp(c.settings.RepositoryCache, ".radar-repository-")
	if err != nil {
		return "", fmt.Errorf("stage repository index: %w", err)
	}
	defer os.RemoveAll(stageDir)
	chartRepo, err := repo.NewChartRepository(entry, getter.All(c.settings))
	if err != nil {
		return "", fmt.Errorf("failed to create chart repository: %w", err)
	}
	chartRepo.CachePath = stageDir
	stagedIndex, err := chartRepo.DownloadIndexFile()
	if err != nil {
		return "", fmt.Errorf("failed to download repository index: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("failed to load repo file: %w", err)
		}
		f = repo.NewFile()
	}
	for _, existing := range f.Repositories {
		existingURL, canonicalErr := canonicalClassicRepositoryURL(existing.URL)
		if canonicalErr == nil && existingURL == repoURL {
			if existing.Name != name {
				return "", fmt.Errorf("%w: repository configuration changed concurrently; retry the request", errRepositoryConflict)
			}
			if err := publishStagedRepositoryCache(stageDir, stagedIndex, c.settings.RepositoryCache); err != nil {
				return "", err
			}
			return existing.Name, nil
		}
	}
	if existing := f.Get(name); existing != nil {
		return "", fmt.Errorf("%w: repository name %q is already configured with a different URL", errRepositoryConflict, name)
	}
	f.Update(entry)
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		return "", fmt.Errorf("failed to persist Helm repository: %w", err)
	}
	if err := publishStagedRepositoryCache(stageDir, stagedIndex, c.settings.RepositoryCache); err != nil {
		return "", err
	}
	return name, nil
}

func publishStagedRepositoryCache(stageDir, stagedIndex, cacheDir string) error {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return fmt.Errorf("read staged repository cache: %w", err)
	}
	indexBase := filepath.Base(stagedIndex)
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == indexBase {
			continue
		}
		if err := os.Rename(filepath.Join(stageDir, entry.Name()), filepath.Join(cacheDir, entry.Name())); err != nil {
			return fmt.Errorf("publish repository cache metadata: %w", err)
		}
	}
	if err := os.Rename(stagedIndex, filepath.Join(cacheDir, indexBase)); err != nil {
		return fmt.Errorf("publish repository index: %w", err)
	}
	return nil
}

func (c *Client) configuredChartSourceCandidates(chartName, version string, lister ociTagLister) ([]ChartSourceCandidate, error) {
	var candidates []ChartSourceCandidate
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err == nil {
		for _, configured := range f.Repositories {
			repositoryURL, urlErr := canonicalClassicRepositoryURL(configured.URL)
			if urlErr != nil {
				continue
			}
			idx, loadErr := repo.LoadIndexFile(filepath.Join(c.settings.RepositoryCache, configured.Name+"-index.yaml"))
			if loadErr != nil {
				continue
			}
			for _, entry := range idx.Entries[chartName] {
				if entry.Version == version {
					candidates = append(candidates, ChartSourceCandidate{Type: "repository", Reference: configured.Name, URL: repositoryURL})
					break
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load repository configuration: %w", err)
	}
	if len(ListOCISources()) > 0 {
		if lister == nil {
			lister = c.newRegistryClient()
		}
		if lister != nil {
			for _, prefix := range ListOCISources() {
				tags, tagErr := lister.Tags(ociRef(prefix, chartName))
				if tagErr == nil && slices.Contains(tags, version) {
					candidate := ChartSourceCandidate{Type: "oci", Reference: ociChartURL(prefix, chartName)}
					if validateChartSourceCandidate(&candidate) == nil {
						candidates = append(candidates, candidate)
					}
				}
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Type != candidates[j].Type {
			return candidates[i].Type < candidates[j].Type
		}
		return candidates[i].Reference < candidates[j].Reference
	})
	return candidates, nil
}

func (c *Client) configuredRepositoryForSource(source ChartSourceCandidate) *repo.Entry {
	if source.Type != "repository" || source.Reference == "" {
		return nil
	}
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		return nil
	}
	byName := f.Get(source.Reference)
	if source.URL == "" {
		return byName
	}
	recordedURL, err := canonicalClassicRepositoryURL(source.URL)
	if err != nil {
		return nil
	}
	if byName != nil {
		configuredURL, err := canonicalClassicRepositoryURL(byName.URL)
		if err != nil || configuredURL != recordedURL {
			return nil
		}
		return byName
	}
	for _, configured := range f.Repositories {
		configuredURL, err := canonicalClassicRepositoryURL(configured.URL)
		if err == nil && configuredURL == recordedURL {
			return configured
		}
	}
	return nil
}

func (c *Client) applyRecordedUpgrade(info *UpgradeInfo, source ChartSourceCandidate, chartName, currentVersion string, lister ociTagLister) bool {
	versions := c.candidateVersions(source, chartName, lister)
	if !slices.Contains(versions, currentVersion) || len(versions) == 0 {
		return false
	}
	info.LatestVersion = versions[0]
	info.SourceType = source.Type
	info.UpdateAvailable = compareVersions(versions[0], currentVersion) > 0
	if source.Type == "repository" {
		configured := c.configuredRepositoryForSource(source)
		if configured == nil {
			return false
		}
		info.RepositoryName = configured.Name
	} else {
		info.ChartRef = source.Reference
	}
	return true
}

func (c *Client) candidateVersions(candidate ChartSourceCandidate, chartName string, lister ociTagLister) []string {
	switch candidate.Type {
	case "repository":
		configured := c.configuredRepositoryForSource(candidate)
		if configured == nil {
			return nil
		}
		idx, err := repo.LoadIndexFile(filepath.Join(c.settings.RepositoryCache, configured.Name+"-index.yaml"))
		if err != nil {
			return nil
		}
		versions := make([]string, 0, len(idx.Entries[chartName]))
		for _, entry := range idx.Entries[chartName] {
			versions = append(versions, entry.Version)
		}
		return sortVersionsDesc(versions)
	case "oci":
		if !ociSourceConfigured(candidate.Reference) {
			return nil
		}
		if lister == nil {
			lister = c.newRegistryClient()
		}
		if lister == nil {
			return nil
		}
		tags, err := lister.Tags(strings.TrimPrefix(candidate.Reference, "oci://"))
		if err != nil {
			return nil
		}
		return sortVersionsDesc(tags)
	default:
		return nil
	}
}

func ociSourceConfigured(reference string) bool {
	ref := strings.TrimSuffix(strings.TrimPrefix(reference, "oci://"), "/")
	lastSlash := strings.LastIndex(ref, "/")
	if lastSlash <= 0 {
		return false
	}
	prefix := "oci://" + ref[:lastSlash]
	return slices.Contains(ListOCISources(), prefix)
}

func (c *Client) resolveRecordedChartPath(actionConfig *action.Configuration, source ChartSourceCandidate, chartName, version string) (string, string, error) {
	return c.resolveRecordedChartPathWithLister(actionConfig, source, chartName, version, nil)
}

func (c *Client) resolveRecordedChartPathWithLister(_ *action.Configuration, source ChartSourceCandidate, chartName, version string, lister ociTagLister) (string, string, error) {
	switch source.Type {
	case "repository":
		configured := c.configuredRepositoryForSource(source)
		if configured == nil {
			return "", "", fmt.Errorf("recorded chart repository is not configured on this Radar installation")
		}
		return c.resolveUpgradeChartPathWithOCIResolver(chartName, version, configured.Name, nil, func(string, string) (string, bool) { return "", false })
	case "oci":
		if !ociSourceConfigured(source.Reference) {
			return "", "", fmt.Errorf("recorded OCI chart source is not configured on this Radar installation")
		}
		if lister == nil {
			lister = c.newRegistryClient()
		}
		if lister == nil {
			return "", "", fmt.Errorf("recorded OCI chart source is unavailable")
		}
		tags, err := lister.Tags(strings.TrimPrefix(source.Reference, "oci://"))
		if err != nil {
			return "", "", fmt.Errorf("recorded OCI chart source is unavailable: %w", err)
		}
		if !slices.Contains(tags, version) {
			return "", "", fmt.Errorf("recorded OCI chart source does not publish %s version %s", chartName, version)
		}
		return source.Reference, "oci", nil
	default:
		return "", "", fmt.Errorf("recorded chart source type is invalid")
	}
}

func (c *Client) sourceStatusWith(actionConfig *action.Configuration, name string) (*ChartSourceStatus, error) {
	rel, err := action.NewGet(actionConfig).Run(name)
	if err != nil {
		return nil, err
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}
	status := &ChartSourceStatus{}
	status.Recorded, _ = chartSourceFromRelease(rel)
	status.Candidates, err = c.configuredChartSourceCandidates(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, nil)
	if err != nil {
		return nil, err
	}
	if status.Recorded == nil {
		return status, nil
	}
	if status.Recorded.Type == "repository" {
		status.Configured = c.configuredRepositoryForSource(*status.Recorded) != nil
	} else {
		status.Configured = ociSourceConfigured(status.Recorded.Reference)
	}
	if status.Configured {
		for _, candidate := range status.Candidates {
			if sameChartSource(candidate, *status.Recorded) {
				status.Available = true
				break
			}
		}
	}
	return status, nil
}

func sameChartSource(a, b ChartSourceCandidate) bool {
	if a.Type != b.Type {
		return false
	}
	if a.Type == "oci" {
		return a.Reference == b.Reference
	}
	if a.URL != "" && b.URL != "" {
		return a.URL == b.URL
	}
	return a.Reference == b.Reference
}

func (c *Client) SourceStatus(namespace, name string) (*ChartSourceStatus, error) {
	cfg, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.sourceStatusWith(cfg, name)
}

func (c *Client) SourceStatusAsUser(namespace, name, username string, groups []string) (*ChartSourceStatus, error) {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.sourceStatusWith(cfg, name)
}

func (c *Client) setSourceWith(actionConfig *action.Configuration, name string, selected ChartSourceCandidate) error {
	if err := validateChartSourceCandidate(&selected); err != nil {
		return err
	}
	rel, err := action.NewGet(actionConfig).Run(name)
	if err != nil {
		return err
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		return fmt.Errorf("release has no usable chart metadata")
	}
	candidates, err := c.configuredChartSourceCandidates(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, nil)
	if err != nil {
		return err
	}
	var persisted *ChartSourceCandidate
	for i := range candidates {
		if sameChartSource(candidates[i], selected) {
			persisted = &candidates[i]
			break
		}
	}
	if persisted == nil {
		return fmt.Errorf("%w: selected source does not publish %s version %s", errInvalidChartSource, rel.Chart.Metadata.Name, rel.Chart.Metadata.Version)
	}
	rel.Labels = mergeChartSourceLabels(rel.Labels, persisted)
	return actionConfig.Releases.Update(rel)
}

func (c *Client) SetSource(namespace, name string, selected ChartSourceCandidate) error {
	cfg, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}
	return c.setSourceWith(cfg, name, selected)
}

func (c *Client) SetSourceAsUser(namespace, name string, selected ChartSourceCandidate, username string, groups []string) error {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	return c.setSourceWith(cfg, name, selected)
}
