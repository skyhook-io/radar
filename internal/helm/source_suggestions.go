package helm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/repo"
)

var errSourceUnavailable = errors.New("chart source is unavailable")

type ChartSourceSuggestion struct {
	Source              ChartSourceCandidate `json:"source"`
	Current             bool                 `json:"current"`
	HasInstalledVersion bool                 `json:"hasInstalledVersion"`
	State               string               `json:"state"`
}

func (c *Client) sourceVersions(source ChartSourceCandidate, chartName string) ([]string, error) {
	return c.sourceVersionsWithIndexes(source, chartName, nil)
}

func (c *Client) sourceVersionsWithIndexes(source ChartSourceCandidate, chartName string, indexes map[string]*repo.IndexFile) ([]string, error) {
	selected, err := c.exactConfiguredSource(source, chartName)
	if err != nil {
		return nil, err
	}
	if selected.Type == "repository" {
		index, cached := indexes[selected.Reference]
		if !cached {
			index, err = repo.LoadIndexFile(filepath.Join(c.settings.RepositoryCache, selected.Reference+"-index.yaml"))
			if indexes != nil {
				indexes[selected.Reference] = index
			}
		}
		if err != nil || index == nil {
			return nil, fmt.Errorf("%w: repository index could not be read; refresh the selected repository", errSourceUnavailable)
		}
		versions := make([]string, 0, len(index.Entries[chartName]))
		for _, entry := range index.Entries[chartName] {
			versions = append(versions, entry.Version)
		}
		return capVersions(sortVersionsDesc(versions)), nil
	}
	tags, err := c.ociSourceTags(selected)
	if err != nil {
		return nil, err
	}
	return capVersions(sortVersionsDesc(tags)), nil
}

// ociSourceTags lists the registry's tags live; OCI has no index to cache.
// The registry client bounds every request with ociProbeTimeout.
func (c *Client) ociSourceTags(selected ChartSourceCandidate) ([]string, error) {
	lister := c.newRegistryClient()
	if lister == nil {
		return nil, fmt.Errorf("%w: selected OCI registry could not be reached", errSourceUnavailable)
	}
	tags, err := lister.Tags(strings.TrimPrefix(selected.Reference, "oci://"))
	if err != nil {
		return nil, fmt.Errorf("%w: could not read versions from the selected OCI source", errSourceUnavailable)
	}
	return tags, nil
}

// ReleaseVersions lists chart versions offered by a release's confirmed source.
type ReleaseVersions struct {
	Source    ChartSourceCandidate `json:"source"`
	Versions  []string             `json:"versions"` // newest first
	Truncated bool                 `json:"truncated"`
}

const freshVersionsTimeout = 15 * time.Second

// FreshReleaseVersionsAsUser reads the release as the caller, then lists its
// chart's versions from the source an upgrade would use: a freshly downloaded
// classic index (never the local cache, which can be cold or stale) or the
// registry's OCI tags. Unlike AvailableVersionsAsUser, which returns an empty
// list when no source is usable, it returns an error saying why.
func (c *Client) FreshReleaseVersionsAsUser(namespace, name string, user *pkgauth.User) (*ReleaseVersions, error) {
	cfg, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return nil, err
	}
	return c.freshReleaseVersionsWith(cfg, name)
}

func (c *Client) freshReleaseVersionsWith(cfg *action.Configuration, name string) (*ReleaseVersions, error) {
	op, err := releaseReadContext(cfg, name)
	if err != nil {
		return nil, err
	}
	source, issue, message := upgradeSourceState(op)
	if issue != "" {
		return nil, fmt.Errorf("no usable chart source for this release: %s", message)
	}
	chartName := op.Latest.Chart.Metadata.Name
	selected, err := c.exactConfiguredSource(*source, chartName)
	if err != nil {
		return nil, err
	}
	var versions []string
	if selected.Type == "repository" {
		dir, err := os.MkdirTemp("", "radar-versions-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		_, index, err := c.fetchSelectedRepositoryIndex(selected, dir, freshVersionsTimeout, maxPreparedIndexBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errSourceUnavailable, err)
		}
		for _, entry := range index.Entries[chartName] {
			versions = append(versions, entry.Version)
		}
	} else if versions, err = c.ociSourceTags(selected); err != nil {
		return nil, err
	}
	sorted := sortVersionsDesc(versions)
	return &ReleaseVersions{Source: selected, Versions: capVersions(sorted), Truncated: len(sorted) > maxAvailableVersions}, nil
}

func (c *Client) sourceSuggestions(chartName, version string, recorded *ChartSourceCandidate) []ChartSourceSuggestion {
	var suggestions []ChartSourceSuggestion
	add := func(source ChartSourceCandidate, installed bool, state string) {
		for _, item := range suggestions {
			if sameChartSource(item.Source, source) {
				return
			}
		}
		suggestions = append(suggestions, ChartSourceSuggestion{source, recorded != nil && sameChartSource(*recorded, source), installed, state})
	}
	if recorded != nil {
		add(*recorded, false, "unchecked")
	}
	if repos, err := repo.LoadFile(c.settings.RepositoryConfig); err == nil {
		for _, r := range repos.Repositories {
			canonical, err := canonicalClassicRepositoryURL(r.URL)
			if err != nil {
				continue
			}
			source := ChartSourceCandidate{Type: "repository", Reference: r.Name, URL: canonical}
			index, err := repo.LoadIndexFile(filepath.Join(c.settings.RepositoryCache, r.Name+"-index.yaml"))
			if err != nil {
				add(source, false, "index_unavailable")
				continue
			}
			entries := index.Entries[chartName]
			if len(entries) == 0 {
				continue
			}
			installed := slices.ContainsFunc(entries, func(entry *repo.ChartVersion) bool { return entry.Version == version })
			if recorded != nil && sameChartSource(*recorded, source) {
				suggestions[0] = ChartSourceSuggestion{source, true, installed, "available"}
			} else {
				add(source, installed, "available")
			}
		}
	}
	// Registration is a place to search, not evidence that the chart exists.
	// Probe only the selected OCI source so one slow registry cannot block all choices.
	for _, prefix := range ListOCISources() {
		add(ChartSourceCandidate{Type: "oci", Reference: ociChartURL(prefix, chartName)}, false, "unchecked")
	}
	sort.SliceStable(suggestions, func(i, j int) bool {
		a, b := suggestions[i], suggestions[j]
		if a.Current != b.Current {
			return a.Current
		}
		if a.HasInstalledVersion != b.HasInstalledVersion {
			return a.HasInstalledVersion
		}
		return a.Source.URL+a.Source.Reference < b.Source.URL+b.Source.Reference
	})
	return suggestions
}

func (c *Client) sourceStatusForOperation(cfg *action.Configuration, name string) (*ChartSourceStatus, error) {
	ctx, err := releaseReadContext(cfg, name)
	if err != nil {
		return nil, err
	}
	rel := ctx.Latest
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}
	status := &ChartSourceStatus{Revision: rel.Version, SourceChangeRequired: failedSourceChange(ctx), Changing: rel.Info == nil || releaseChanging(rel.Info.Status)}
	status.Recorded, _ = chartSourceFromRelease(rel)
	_, status.Confirmed = confirmedChartSource(rel)
	status.Suggestions = c.sourceSuggestions(rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, status.Recorded)
	for _, suggestion := range status.Suggestions {
		if suggestion.HasInstalledVersion {
			status.Candidates = append(status.Candidates, suggestion.Source)
		}
	}
	if status.Recorded != nil {
		selected, err := c.exactConfiguredSource(*status.Recorded, rel.Chart.Metadata.Name)
		status.Configured = err == nil
		if err == nil {
			versions, err := c.sourceVersions(selected, rel.Chart.Metadata.Name)
			status.Available = err == nil && len(versions) > 0
		}
	}
	return status, nil
}
