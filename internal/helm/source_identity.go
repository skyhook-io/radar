package helm

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

const chartSourceBindingLabel = "radarhq.io/chart-source-binding"
const chartSourceAttemptLabel = "radarhq.io/chart-source-attempt"

var errSourceRequired = errors.New("select and confirm a chart source to continue")
var errSourceAlreadyConfirmed = errors.New("release already has a confirmed source; choose a source for the next upgrade to replace it")
var errSourceConflict = errors.New("chart source changed; select the source and preview again")

// Helm alters values and dependency metadata before storing a chart. Only bind
// material that survives that transformation and the release storage roundtrip.
func chartSourceBinding(ch *chart.Chart) string {
	if ch == nil || ch.Metadata == nil {
		return ""
	}
	type template struct {
		Name string
		Data []byte
	}
	templates := make([]template, 0, len(ch.Templates))
	for _, file := range ch.Templates {
		if file != nil {
			templates = append(templates, template{file.Name, file.Data})
		}
	}
	sort.Slice(templates, func(i, j int) bool { return templates[i].Name < templates[j].Name })
	metadata := *ch.Metadata
	metadata.Dependencies = nil
	if metadata.APIVersion == "" {
		metadata.APIVersion = chart.APIVersionV1
	}
	data, err := json.Marshal(struct {
		Metadata  chart.Metadata
		Templates []template
		Schema    []byte
		Files     map[string][]byte
	}{metadata, templates, ch.Schema, filesByName(ch.Files)})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
}

func confirmedChartSource(rel *release.Release) (*ChartSourceCandidate, bool) {
	source, ok := chartSourceFromRelease(rel)
	if !ok {
		return nil, false
	}
	binding := chartSourceBinding(rel.Chart)
	return source, binding != "" && rel.Labels[chartSourceBindingLabel] == binding
}

func boundChartSourceLabels(current map[string]string, source *ChartSourceCandidate, ch *chart.Chart) map[string]string {
	labels := chartSourceUpgradeLabels(current, source)
	labels[chartSourceAttemptLabel] = "null"
	labels[chartSourceBindingLabel] = "null"
	if source != nil {
		labels[chartSourceBindingLabel] = chartSourceBinding(ch)
	}
	return labels
}

func (c *Client) exactConfiguredSource(selected ChartSourceCandidate, chartName string) (ChartSourceCandidate, error) {
	if err := validateChartSourceCandidate(&selected); err != nil {
		return selected, err
	}
	if selected.Type == "repository" {
		canonical, err := canonicalClassicRepositoryURL(selected.URL)
		if err != nil {
			return selected, err
		}
		selected.URL = canonical
		configured := c.configuredRepositoryForSource(selected)
		if configured == nil {
			return selected, fmt.Errorf("%w: selected repository is not configured", errInvalidChartSource)
		}
		selected.Reference = configured.Name
		return selected, nil
	}
	if !ociSourceConfigured(selected.Reference) || !ociReferenceNamesChart(selected.Reference, chartName) {
		return selected, fmt.Errorf("%w: select a configured OCI source for chart %s", errInvalidChartSource, chartName)
	}
	return selected, nil
}

type releaseSourceContext struct{ Latest, Baseline *release.Release }

func releaseSourceUnchanged(cfg *action.Configuration, original *release.Release) error {
	latest, err := cfg.Releases.Last(original.Name)
	if err != nil {
		return err
	}
	source, _ := chartSourceFromRelease(original)
	expectation := SourceUpgradeSelection{ExpectedRevision: original.Version, ExpectedSource: source}
	if !sourceSelectionMatches(expectation, latest) || latest.Info == nil || releaseChanging(latest.Info.Status) ||
		chartSourceBinding(latest.Chart) != chartSourceBinding(original.Chart) || latest.Labels[chartSourceBindingLabel] != original.Labels[chartSourceBindingLabel] || !latest.Info.FirstDeployed.Equal(original.Info.FirstDeployed) {
		return errSourceConflict
	}
	return nil
}

func releaseReadContext(cfg *action.Configuration, name string) (releaseSourceContext, error) {
	latest, err := cfg.Releases.Last(name)
	if err != nil {
		return releaseSourceContext{}, err
	}
	if latest.Info == nil || releaseChanging(latest.Info.Status) {
		return releaseSourceContext{latest, latest}, nil
	}
	baseline := latest
	if latest.Info.Status != release.StatusDeployed {
		baseline, err = cfg.Releases.Deployed(name)
		if errors.Is(err, driver.ErrNoDeployedReleases) && (latest.Info.Status == release.StatusFailed || latest.Info.Status == release.StatusSuperseded) {
			baseline, err = latest, nil
		}
		if err != nil {
			return releaseSourceContext{}, err
		}
	}
	return releaseSourceContext{latest, baseline}, nil
}

func releaseContext(cfg *action.Configuration, name string) (releaseSourceContext, error) {
	ctx, err := releaseReadContext(cfg, name)
	if err != nil {
		return ctx, err
	}
	if ctx.Latest.Info == nil || releaseChanging(ctx.Latest.Info.Status) {
		return ctx, errReleaseChanging
	}
	return ctx, nil
}

func failedSourceChange(ctx releaseSourceContext) bool {
	if ctx.Latest == nil || ctx.Latest.Info == nil || ctx.Latest.Info.Status != release.StatusFailed {
		return false
	}
	if ctx.Latest.Labels[chartSourceAttemptLabel] == "true" {
		return true
	}
	a, aOK := chartSourceFromRelease(ctx.Latest)
	b, bOK := chartSourceFromRelease(ctx.Baseline)
	return aOK && bOK && !sameChartSource(*a, *b)
}

func upgradeSourceState(ctx releaseSourceContext) (*ChartSourceCandidate, UpgradeSourceIssue, string) {
	if ctx.Latest.Info == nil || releaseChanging(ctx.Latest.Info.Status) {
		return nil, UpgradeSourceIssueChanging, "A Helm operation is in progress; wait for it to finish"
	}
	if failedSourceChange(ctx) {
		return nil, UpgradeSourceIssueSourceChange, "Select a source and preview again after the failed chart change"
	}
	if source, ok := confirmedChartSource(ctx.Latest); ok {
		return source, "", ""
	}
	if _, ok := chartSourceFromRelease(ctx.Latest); ok {
		return nil, UpgradeSourceIssueConfirmation, "Confirm the last recorded chart source before upgrading"
	}
	return nil, UpgradeSourceIssueUntracked, errSourceRequired.Error()
}

func (c *Client) normalUpgradeSource(cfg *action.Configuration, rel *release.Release, repositoryName string) (*ChartSourceCandidate, error) {
	source, ok := confirmedChartSource(rel)
	if !ok {
		return nil, errSourceRequired
	}
	if cfg != nil && cfg.Releases != nil {
		ctx, err := releaseContext(cfg, rel.Name)
		if err != nil {
			return nil, err
		}
		if failedSourceChange(ctx) {
			return nil, errSourceRequired
		}
	}
	if repositoryName != "" {
		if source.Type != "repository" {
			return nil, errSourceConflict
		}
		selected, err := c.exactConfiguredSource(ChartSourceCandidate{Type: "repository", Reference: repositoryName, URL: source.URL}, rel.Chart.Metadata.Name)
		if err != nil || selected.Reference != repositoryName {
			return nil, errSourceConflict
		}
	}
	return source, nil
}
