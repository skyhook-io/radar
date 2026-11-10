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

var (
	// errChartVersionMissing marks a classic index without the requested version.
	errChartVersionMissing = errors.New("selected repository has no chart version")
	// errChartDownload marks a failed chart download from the selected source;
	// for OCI that is how a missing version surfaces.
	errChartDownload = errors.New("download selected chart")
)

// ChartVersionUnavailable reports whether err came from the selected source
// lacking the requested chart version or failing to download it.
func ChartVersionUnavailable(err error) bool {
	return errors.Is(err, errChartVersionMissing) || errors.Is(err, errChartDownload)
}

var errSourceTimedOut = errors.New("the chart source didn't respond in time")

// SourceTimedOut reports whether err comes from a chart source that didn't
// answer in time: a large index, or a source throttling parallel downloads.
// Retrying shortly is the remedy, not reconfiguring the source.
func SourceTimedOut(err error) bool {
	if errors.Is(err, errSourceTimedOut) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

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
			snapshot, index, err := c.fetchSelectedRepositoryIndex(selected, dir, 30*time.Second, 0)
			if err != nil {
				return nil, "", err
			}
			dl.Getters = c.repositoryGettersWithTimeout(&snapshot, 30*time.Second)
			entry, err := index.Get(name, version)
			if err != nil {
				return nil, "", fmt.Errorf("%w %s for chart %s: %v", errChartVersionMissing, version, name, err)
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
			return nil, "", fmt.Errorf("%w: %w", errChartDownload, err)
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

// fetchSelectedRepositoryIndex downloads the selected classic repository's
// current index into dir instead of trusting the local cache, so a read sees
// the same entries a following download would. maxBytes caps the index
// (0: no cap).
func (c *Client) fetchSelectedRepositoryIndex(selected ChartSourceCandidate, dir string, timeout time.Duration, maxBytes int64) (repo.Entry, *repo.IndexFile, error) {
	configured := c.configuredRepositoryForSource(selected)
	if configured == nil {
		return repo.Entry{}, nil, errSourceConflict
	}
	snapshot := *configured
	snapshot.URL = selected.URL
	index, err := c.downloadRepositoryIndex(snapshot, dir, timeout, maxBytes)
	if err != nil {
		return repo.Entry{}, nil, err
	}
	snapshot.Name = "selected"
	return snapshot, index, nil
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
