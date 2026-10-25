package helm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

type SourceUpgradeSelection struct {
	Source           ChartSourceCandidate  `json:"source"`
	ExpectedRevision int                   `json:"expectedRevision"`
	ExpectedSource   *ChartSourceCandidate `json:"expectedSource"`
	PreviewDigest    string                `json:"previewDigest,omitempty"`
}

func ociReferenceNamesChart(reference, name string) bool {
	parts := strings.Split(strings.TrimRight(reference, "/"), "/")
	return parts[len(parts)-1] == name
}

// Separate destinations prevent same-name/version packages from different
// repositories overwriting one another between download, digest and load.
func (c *Client) loadSourceChart(cfg *action.Configuration, source ChartSourceCandidate, name, version string) (*chart.Chart, string, error) {
	if version == "" || version == "latest" {
		return nil, "", fmt.Errorf("%w: select an exact chart version", errInvalidChartSource)
	}
	selected, err := c.exactConfiguredSource(source, name)
	if err != nil {
		return nil, "", err
	}
	var data []byte
	var indexDigest string
	if c.chartPathLocator != nil {
		ref := selected.Reference
		if selected.Type == "repository" {
			ref += "/" + name
		}
		path, err := c.chartPathLocator(cfg, ref, version)
		if err != nil {
			return nil, "", err
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
	} else {
		dir, err := os.MkdirTemp("", "radar-chart-")
		if err != nil {
			return nil, "", err
		}
		defer os.RemoveAll(dir)
		dl := downloader.ChartDownloader{
			Out: io.Discard, Getters: getter.All(c.settings),
			Options:          []getter.Option{getter.WithTimeout(30 * time.Second)},
			RepositoryConfig: c.settings.RepositoryConfig, RepositoryCache: c.settings.RepositoryCache,
		}
		ref := selected.Reference
		if selected.Type == "oci" {
			rc, err := c.newRegistryClientForChartPull()
			if err != nil {
				return nil, "", err
			}
			dl.RegistryClient = rc
			dl.Options = append(dl.Options, getter.WithRegistryClient(rc))
		} else {
			configured := c.configuredRepositoryForSource(selected)
			if configured == nil {
				return nil, "", errSourceConflict
			}
			snapshot := *configured
			snapshot.Name = "selected"
			snapshot.URL = selected.URL
			dl.Options = append(dl.Options, getter.WithInsecureSkipVerifyTLS(snapshot.InsecureSkipTLSverify))
			repository, err := repo.NewChartRepository(&snapshot, getter.All(c.settings))
			if err != nil {
				return nil, "", err
			}
			repository.Client, err = getter.NewHTTPGetter(getter.WithTimeout(30 * time.Second))
			if err != nil {
				return nil, "", err
			}
			repository.CachePath = dir
			if _, err := repository.DownloadIndexFile(); err != nil {
				return nil, "", fmt.Errorf("could not refresh the selected repository: %w", err)
			}
			index, err := repo.LoadIndexFile(filepath.Join(dir, "selected-index.yaml"))
			if err != nil {
				return nil, "", fmt.Errorf("read selected repository index: %w", err)
			}
			entry, err := index.Get(name, version)
			if err != nil {
				return nil, "", fmt.Errorf("selected repository has no chart %s version %s: %w", name, version, err)
			}
			indexDigest = entry.Digest
			config := &repo.File{Repositories: []*repo.Entry{&snapshot}}
			dl.RepositoryConfig = filepath.Join(dir, "repositories.yaml")
			dl.RepositoryCache = dir
			if err := config.WriteFile(dl.RepositoryConfig, 0o600); err != nil {
				return nil, "", err
			}
			ref = snapshot.Name + "/" + name
		}
		path, _, err := dl.DownloadTo(ref, version, dir)
		if err != nil {
			return nil, "", fmt.Errorf("download selected chart: %w", err)
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
	}
	if err := verifyChartDigest(data, indexDigest); err != nil {
		return nil, "", fmt.Errorf("chart %s version %s: %w", name, version, withChartDigestGuidance(err, true))
	}
	sum := sha256.Sum256(data)
	ch, err := loader.LoadArchive(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if ch.Metadata == nil || ch.Metadata.Name != name || ch.Metadata.Version != version {
		return nil, "", fmt.Errorf("%w: downloaded chart does not match the selected name and version", errInvalidChartSource)
	}
	if err := validateChartDependencyBodies(ch); err != nil {
		return nil, "", err
	}
	return ch, hex.EncodeToString(sum[:]), nil
}

func sourceSelectionMatches(selection SourceUpgradeSelection, latest *release.Release) bool {
	if selection.ExpectedRevision != latest.Version {
		return false
	}
	source, ok := chartSourceFromRelease(latest)
	if selection.ExpectedSource == nil {
		return !ok
	}
	return ok && sameChartSource(*selection.ExpectedSource, *source)
}

func sourcePreviewDigest(selection SourceUpgradeSelection, version, artifact, revisionState string, values map[string]any) (string, error) {
	selection.PreviewDigest = ""
	if selection.Source.Type == "repository" {
		selection.Source.Reference = ""
	}
	if selection.ExpectedSource != nil && selection.ExpectedSource.Type == "repository" {
		expected := *selection.ExpectedSource
		expected.Reference = ""
		selection.ExpectedSource = &expected
	}
	data, err := json.Marshal(struct {
		Selection                        SourceUpgradeSelection
		Version, Artifact, RevisionState string
		Values                           map[string]any
	}{selection, version, artifact, revisionState, values})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (c *Client) sourceUpgradeWith(cfg *action.Configuration, name, version string, values map[string]any, selection SourceUpgradeSelection, preview bool, progress func(string, string, string)) (*ValuesPreviewResponse, error) {
	if values == nil || selection.ExpectedRevision < 1 {
		return nil, fmt.Errorf("%w: values and expected release revision are required", errInvalidChartSource)
	}
	ctx, err := releaseContext(cfg, name)
	if err != nil {
		return nil, err
	}
	if !sourceSelectionMatches(selection, ctx.Latest) {
		return nil, errSourceConflict
	}
	if ctx.Baseline.Chart == nil || ctx.Baseline.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}
	selected, err := c.exactConfiguredSource(selection.Source, ctx.Baseline.Chart.Metadata.Name)
	if err != nil {
		return nil, err
	}
	selection.Source = selected
	progress("resolving", "Loading the selected chart source...", "")
	ch, artifact, err := c.loadSourceChart(cfg, selected, ctx.Baseline.Chart.Metadata.Name, version)
	if err != nil {
		return nil, err
	}
	revisionState := fmt.Sprintf("%s/%s/%s/%d/%s", ctx.Latest.Info.FirstDeployed.UTC().Format(time.RFC3339Nano), chartSourceBinding(ctx.Latest.Chart), ctx.Latest.Labels[chartSourceBindingLabel], ctx.Baseline.Version, chartSourceBinding(ctx.Baseline.Chart))
	digest, err := sourcePreviewDigest(selection, version, artifact, revisionState, values)
	if err != nil {
		return nil, err
	}
	if !preview && selection.PreviewDigest != digest {
		return nil, fmt.Errorf("%w: chart or settings changed; preview again before applying", errSourceConflict)
	}
	latest, err := cfg.Releases.Last(name)
	if err != nil {
		return nil, err
	}
	if !sourceSelectionMatches(selection, latest) || latest.Info == nil || releaseChanging(latest.Info.Status) || chartSourceBinding(latest.Chart) != chartSourceBinding(ctx.Latest.Chart) || !latest.Info.FirstDeployed.Equal(ctx.Latest.Info.FirstDeployed) || latest.Labels[chartSourceBindingLabel] != ctx.Latest.Labels[chartSourceBindingLabel] {
		return nil, errSourceConflict
	}
	if err := useReleaseTargetNamespace(cfg, name); err != nil {
		return nil, err
	}
	upgrade := action.NewUpgrade(cfg)
	upgrade.Namespace = ctx.Baseline.Namespace
	upgrade.Timeout = 120 * time.Second
	upgrade.ResetValues = true
	upgrade.Labels = boundChartSourceLabels(latest.Labels, &selected, ch)
	upgrade.Labels[chartSourceAttemptLabel] = "true"
	upgrade.DryRun = preview
	if preview {
		upgrade.DryRunOption = "client"
	}
	progress("upgrading", "Applying the selected chart and settings...", "")
	result, err := upgrade.Run(name, ch, values)
	if err != nil {
		return nil, err
	}
	if !preview {
		return nil, nil
	}
	baselineSource, _ := chartSourceFromRelease(ctx.Baseline)
	return &ValuesPreviewResponse{
		BaselineSource: baselineSource,
		CurrentValues:  ctx.Baseline.Config, NewValues: values,
		ManifestDiff:  computeDiff(ctx.Baseline.Manifest, result.Manifest, ctx.Baseline.Version, result.Version),
		PreviewDigest: digest, BaselineRevision: ctx.Baseline.Version,
	}, nil
}
