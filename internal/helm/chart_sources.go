package helm

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

const (
	chartSourceTypeLabel = "radar.skyhook.io/chart-source-type"
	chartSourceRefLabel  = "radar.skyhook.io/chart-source-ref-"
	chartSourceURLLabel  = "radar.skyhook.io/chart-source-url-"
	chartSourceChunkSize = 63
	chartSourceMaxChunks = 16
)

var repositoryNamePartRE = regexp.MustCompile(`[^a-z0-9._-]+`)

// chartSourceLabels encodes the credential-free source reference into Helm
// release labels. Helm persists these labels with the release Secret and merges
// them forward on upgrades, so provenance is cluster-scoped and survives Radar
// restarts without introducing a second database.
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
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	return string(decoded), true
}

func chartSourceLabels(source *ChartSourceCandidate) map[string]string {
	if source == nil || (source.Type != "repository" && source.Type != "oci") || source.Reference == "" {
		return nil
	}
	var repositoryURL string
	if source.Type == "repository" {
		var err error
		repositoryURL, err = canonicalClassicRepositoryURL(source.URL)
		if err != nil {
			return nil
		}
	} else if source.URL != "" {
		return nil
	}
	labels := map[string]string{chartSourceTypeLabel: source.Type}
	if !addChartSourceLabelChunks(labels, chartSourceRefLabel, source.Reference) {
		return nil
	}
	if repositoryURL != "" && !addChartSourceLabelChunks(labels, chartSourceURLLabel, repositoryURL) {
		return nil
	}
	return labels
}

func validateChartSourceCandidate(source *ChartSourceCandidate) error {
	if len(chartSourceLabels(source)) == 0 {
		return fmt.Errorf("chart source is invalid or too long to persist safely")
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
	if rawURL, hasURL := decodeChartSourceLabelChunks(rel.Labels, chartSourceURLLabel); hasURL {
		if typ != "repository" {
			return nil, false
		}
		canonicalURL, err := canonicalClassicRepositoryURL(rawURL)
		if err != nil {
			return nil, false
		}
		source.URL = canonicalURL
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

func resolveOCIInstallSource(raw, chartName string) (chartURL, prefix string, err error) {
	ref := strings.TrimRight(strings.TrimSpace(raw), "/")
	if !strings.HasPrefix(ref, "oci://") || chartName == "" {
		return "", "", fmt.Errorf("invalid OCI chart source")
	}
	if strings.HasSuffix(ref, "/"+chartName) {
		chartURL = ref
		prefix = strings.TrimSuffix(ref, "/"+chartName)
	} else {
		prefix = ref
		chartURL = ref + "/" + chartName
	}
	if _, err := normalizeOCIPrefix(prefix); err != nil {
		return "", "", err
	}
	return chartURL, prefix, nil
}

// ensureClassicRepository reuses repositories.yaml as the durable registry for
// a resolved HTTP source (notably ArtifactHub discovery). Credentials embedded
// in repository URLs are rejected; authenticated repos continue to use Helm's
// own repository configuration mechanisms.
func canonicalClassicRepositoryURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(rawURL), "/"))
	if err != nil {
		return "", fmt.Errorf("invalid Helm repository URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid Helm repository URL")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("repository URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("repository URL must not contain query credentials or fragments")
	}
	hostname := strings.ToLower(parsed.Hostname())
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

func (c *Client) ensureClassicRepository(rawURL, preferredName string) (string, error) {
	repoURL, err := canonicalClassicRepositoryURL(rawURL)
	if err != nil {
		return "", err
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
			chartRepo, err := repo.NewChartRepository(existing, getter.All(c.settings))
			if err != nil {
				return "", fmt.Errorf("failed to create chart repository: %w", err)
			}
			chartRepo.CachePath = c.settings.RepositoryCache
			if _, err := chartRepo.DownloadIndexFile(); err != nil {
				return "", fmt.Errorf("failed to refresh repository index: %w", err)
			}
			return existing.Name, nil
		}
	}

	name := normalizedRepositoryName(preferredName)
	if name == "" {
		name = stableRepositoryName("", repoURL)
	}
	if existing := f.Get(name); existing != nil {
		existingURL, canonicalErr := canonicalClassicRepositoryURL(existing.URL)
		if canonicalErr != nil || existingURL != repoURL {
			return "", fmt.Errorf("repository name %q is already configured with a different URL", name)
		}
	}
	entry := &repo.Entry{Name: name, URL: repoURL}
	chartRepo, err := repo.NewChartRepository(entry, getter.All(c.settings))
	if err != nil {
		return "", fmt.Errorf("failed to create chart repository: %w", err)
	}
	chartRepo.CachePath = c.settings.RepositoryCache
	if _, err := chartRepo.DownloadIndexFile(); err != nil {
		return "", fmt.Errorf("failed to download repository index: %w", err)
	}
	f.Update(entry)
	if err := f.WriteFile(c.settings.RepositoryConfig, 0o600); err != nil {
		return "", fmt.Errorf("failed to persist Helm repository: %w", err)
	}
	return name, nil
}

func (c *Client) configuredChartSourceCandidates(chartName, version string, lister ociTagLister) ([]ChartSourceCandidate, error) {
	candidates := make([]ChartSourceCandidate, 0)
	f, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[helm] source recovery could not read classic repository config: %v", err)
	}
	if err == nil {
		for _, configured := range f.Repositories {
			repositoryURL, urlErr := canonicalClassicRepositoryURL(configured.URL)
			if urlErr != nil {
				continue
			}
			idx, loadErr := repo.LoadIndexFile(c.settings.RepositoryCache + string(os.PathSeparator) + configured.Name + "-index.yaml")
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
	}
	if len(ListOCISources()) > 0 {
		if lister == nil {
			lister = c.newRegistryClient()
		}
		if lister != nil {
			for _, prefix := range ListOCISources() {
				tags, tagErr := lister.Tags(ociRef(prefix, chartName))
				if tagErr == nil && slices.Contains(tags, version) {
					candidates = append(candidates, ChartSourceCandidate{Type: "oci", Reference: ociChartURL(prefix, chartName)})
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

// configuredRepositoryForSource resolves durable classic provenance on the
// current machine. Legacy metadata resolves by local alias only. New metadata
// requires the recorded alias to have the same URL, but can reuse an identical
// URL registered under another local alias.
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

func (c *Client) applyCandidateUpgrade(info *UpgradeInfo, candidate ChartSourceCandidate, chartName, currentVersion string, lister ociTagLister) bool {
	switch candidate.Type {
	case "repository":
		configured := c.configuredRepositoryForSource(candidate)
		if configured == nil {
			return false
		}
		idx, err := repo.LoadIndexFile(c.settings.RepositoryCache + string(os.PathSeparator) + configured.Name + "-index.yaml")
		if err != nil {
			return false
		}
		var latest string
		for _, entry := range idx.Entries[chartName] {
			if latest == "" || compareVersions(entry.Version, latest) > 0 {
				latest = entry.Version
			}
		}
		if latest == "" {
			return false
		}
		info.LatestVersion = latest
		info.RepositoryName = configured.Name
		info.SourceType = "repository"
		info.UpdateAvailable = compareVersions(latest, currentVersion) > 0
		return true
	case "oci":
		if lister == nil {
			lister = c.newRegistryClient()
		}
		if lister == nil {
			return false
		}
		tags, err := lister.Tags(strings.TrimPrefix(candidate.Reference, "oci://"))
		if err != nil || !slices.Contains(tags, currentVersion) || len(tags) == 0 {
			return false
		}
		info.LatestVersion = tags[0]
		info.ChartRef = candidate.Reference
		info.SourceType = "oci"
		info.UpdateAvailable = compareVersions(tags[0], currentVersion) > 0
		return true
	default:
		return false
	}
}

func (c *Client) candidateVersions(candidate ChartSourceCandidate, chartName string, lister ociTagLister) []string {
	switch candidate.Type {
	case "repository":
		configured := c.configuredRepositoryForSource(candidate)
		if configured == nil {
			return nil
		}
		idx, err := repo.LoadIndexFile(c.settings.RepositoryCache + string(os.PathSeparator) + configured.Name + "-index.yaml")
		if err != nil {
			return nil
		}
		versions := make([]string, 0, len(idx.Entries[chartName]))
		for _, entry := range idx.Entries[chartName] {
			versions = append(versions, entry.Version)
		}
		return sortVersionsDesc(versions)
	case "oci":
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
		return tags
	default:
		return nil
	}
}

func (c *Client) sourceCandidatesWith(actionConfig *action.Configuration, name string) ([]ChartSourceCandidate, error) {
	rel, err := action.NewGet(actionConfig).Run(name)
	if err != nil {
		return nil, err
	}
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}
	// New-format classic provenance is sufficient to bootstrap a machine that
	// does not yet have the repository in repositories.yaml. Return it directly;
	// SetSource still verifies the exact chart/version after the repository is
	// added and its index is refreshed.
	if recorded, ok := chartSourceFromRelease(rel); ok && recorded.Type == "repository" && recorded.URL != "" {
		return []ChartSourceCandidate{*recorded}, nil
	}
	return c.configuredChartSourceCandidates(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, nil)
}

func (c *Client) SourceCandidates(namespace, name string) ([]ChartSourceCandidate, error) {
	cfg, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.sourceCandidatesWith(cfg, name)
}

func (c *Client) SourceCandidatesAsUser(namespace, name, username string, groups []string) ([]ChartSourceCandidate, error) {
	cfg, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.sourceCandidatesWith(cfg, name)
}

// recoverLegacyClassicSource upgrades old name-only provenance when the local
// alias still publishes the exact installed chart/version, or when that alias
// is absent and exactly one configured classic repository does. Ambiguous,
// absent, version-mismatched, and OCI-only matches remain fail-closed.
func (c *Client) recoverLegacyClassicSource(actionConfig *action.Configuration, rel *release.Release, legacy ChartSourceCandidate, lister ociTagLister) (*ChartSourceCandidate, error) {
	if legacy.Type != "repository" || legacy.URL != "" {
		return nil, nil
	}
	if rel == nil || rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}
	candidates, err := c.configuredChartSourceCandidates(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, lister)
	if err != nil {
		return nil, err
	}
	var recovered *ChartSourceCandidate
	if configured := c.configuredRepositoryForSource(legacy); configured != nil {
		for i := range candidates {
			if candidates[i].Type == "repository" && candidates[i].Reference == configured.Name {
				recovered = &candidates[i]
				break
			}
		}
	} else if len(candidates) == 1 && candidates[0].Type == "repository" {
		recovered = &candidates[0]
	}
	if recovered == nil {
		return nil, nil
	}
	if err := validateChartSourceCandidate(recovered); err != nil {
		return nil, err
	}
	rel.Labels = mergeChartSourceLabels(rel.Labels, recovered)
	if err := actionConfig.Releases.Update(rel); err != nil {
		return nil, fmt.Errorf("persist recovered chart source: %w", err)
	}
	return recovered, nil
}

func (c *Client) setSourceWith(actionConfig *action.Configuration, name string, selected ChartSourceCandidate) error {
	if selected.Reference == "" || (selected.Type != "repository" && selected.Type != "oci") {
		return fmt.Errorf("chart source is invalid")
	}
	selectedURL := ""
	if selected.Type == "repository" && selected.URL != "" {
		var err error
		selectedURL, err = canonicalClassicRepositoryURL(selected.URL)
		if err != nil {
			return err
		}
	} else if selected.Type == "oci" {
		if err := validateChartSourceCandidate(&selected); err != nil {
			return err
		}
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
		candidate := &candidates[i]
		switch selected.Type {
		case "repository":
			if candidate.Type != "repository" {
				continue
			}
			if selectedURL != "" {
				if candidate.URL != selectedURL {
					continue
				}
			} else if candidate.Reference != selected.Reference {
				continue
			}
			persisted = candidate
		case "oci":
			if *candidate == selected {
				persisted = candidate
			}
		}
		if persisted != nil {
			break
		}
	}
	if persisted == nil {
		return fmt.Errorf("selected source does not publish %s version %s", rel.Chart.Metadata.Name, rel.Chart.Metadata.Version)
	}
	if err := validateChartSourceCandidate(persisted); err != nil {
		return err
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
