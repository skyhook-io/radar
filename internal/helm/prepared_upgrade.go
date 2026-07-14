package helm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
)

// CloudReleaseState is the Helm storage state relevant to Cloud enrollment.
// It deliberately has fewer states than Helm: callers only need to choose the
// fresh path, the deployed-adoption path, or a fail-closed explanation.
type CloudReleaseState string

const (
	CloudReleaseNone     CloudReleaseState = "none"
	CloudReleaseDeployed CloudReleaseState = "deployed"
	CloudReleasePending  CloudReleaseState = "pending"
	CloudReleaseHistory  CloudReleaseState = "history"
)

// CloudReleaseInspection is a side-effect-free classification of the latest
// Helm release record for a Cloud install target.
type CloudReleaseInspection struct {
	State    CloudReleaseState
	Status   string
	Revision int
}

// InspectCloudRelease lets Cloud enrollment select fresh install vs adoption
// without attempting either operation. Reading Helm's Secret-backed storage is
// also an intentional early permission check.
func (c *Client) InspectCloudRelease(namespace, name string) (CloudReleaseInspection, error) {
	if c == nil {
		return CloudReleaseInspection{}, errors.New("inspect cloud release: nil helm client")
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(name) == "" {
		return CloudReleaseInspection{}, errors.New("inspect cloud release: namespace and release name are required")
	}
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return CloudReleaseInspection{}, err
	}
	return inspectCloudReleaseWith(actionConfig, name)
}

func inspectCloudReleaseWith(actionConfig *action.Configuration, name string) (CloudReleaseInspection, error) {
	state, err := inspectStoredRelease(actionConfig, name)
	if err != nil {
		return CloudReleaseInspection{}, err
	}
	inspection := CloudReleaseInspection{Status: state.status, Revision: state.revision}
	switch state.kind {
	case storedReleaseNone:
		inspection.State = CloudReleaseNone
	case storedReleaseDeployed:
		inspection.State = CloudReleaseDeployed
	case storedReleasePending:
		inspection.State = CloudReleasePending
	case storedReleaseUpgradeRecovery, storedReleaseReplaceRecovery:
		inspection.State = CloudReleaseHistory
	default:
		return CloudReleaseInspection{}, fmt.Errorf("unrecognized release classification for %q", name)
	}
	return inspection, nil
}

// CloudReleaseNotDeployedError reports why an adoption target cannot be
// prepared. The caller may use Inspection to render an actionable state.
type CloudReleaseNotDeployedError struct {
	Name       string
	Namespace  string
	Inspection CloudReleaseInspection
}

func (e *CloudReleaseNotDeployedError) Error() string {
	return fmt.Sprintf("release %q in namespace %q is not deployed (state %s, status %s, revision %d)",
		e.Name, e.Namespace, e.Inspection.State, emptyAs(e.Inspection.Status, "none"), e.Inspection.Revision)
}

// NotOfficialRadarReleaseError is returned when Helm storage contains a chart
// that cannot be identified as the Skyhook Radar chart. Helm release records do
// not retain their repository URL, so identity is based on embedded metadata.
type NotOfficialRadarReleaseError struct {
	Name         string
	Namespace    string
	ChartName    string
	ChartVersion string
}

func (e *NotOfficialRadarReleaseError) Error() string {
	return fmt.Sprintf("release %q in namespace %q is not an official Radar chart (found %q version %q)",
		e.Name, e.Namespace, e.ChartName, e.ChartVersion)
}

// ChartDowngradeError prevents Cloud adoption from silently replacing a newer
// Radar chart with an older stable chart selected from the configured repo.
type ChartDowngradeError struct {
	Current string
	Target  string
}

func (e *ChartDowngradeError) Error() string {
	return fmt.Sprintf("refusing to downgrade Radar chart from %s to %s", e.Current, e.Target)
}

// ReleaseChangedError means the Helm release changed after preparation. The
// user must rerun preparation so approval and permission checks describe the
// release that will actually be upgraded.
type ReleaseChangedError struct {
	Name      string
	Namespace string
	Expected  string
	Actual    string
}

func (e *ReleaseChangedError) Error() string {
	return fmt.Sprintf("release %q in namespace %q changed after preparation (expected %s; found %s)",
		e.Name, e.Namespace, e.Expected, e.Actual)
}

// CloudUpgradeValuesSummary is the effective configuration relevant to
// deciding whether and how to adopt an existing Radar release. It intentionally
// never exposes token or OIDC secret values.
type CloudUpgradeValuesSummary struct {
	AuthMode            string
	CloudEnabled        bool
	CloudExistingSecret string
	CloudTokenSet       bool
	ImageRepository     string
	ImageTag            string
	EffectiveImageTag   string
	RBAC                map[string]bool
}

// PreparedChartSummary is the verified identity of a chart archive resolved
// through the same stable-version selection and repository-digest validation
// used by fresh installs and native-Helm adoption.
type PreparedChartSummary struct {
	ChartVersion string
	AppVersion   string
}

func (s CloudUpgradeValuesSummary) clone() CloudUpgradeValuesSummary {
	s.RBAC = cloneBoolMap(s.RBAC)
	return s
}

// PreparedUpgrade pins the current release identity and exact target chart
// archive across browser approval. Its private fields prevent callers from
// swapping either side of the upgrade after permission checks.
type PreparedUpgrade struct {
	client  *Client
	request InstallRequest
	chart   *chart.Chart

	currentIdentity  preparedReleaseIdentity
	currentManifest  string
	targetManifest   string
	currentValues    CloudUpgradeValuesSummary
	currentWorkload  DeploymentRef
	targetWorkload   DeploymentRef
	targetVersion    string
	targetAppVersion string
}

type preparedReleaseIdentity struct {
	revision     int
	status       release.Status
	chartName    string
	chartVersion string
	appVersion   string
	manifestHash [sha256.Size]byte
	configHash   [sha256.Size]byte
}

func (p *PreparedUpgrade) ReleaseName() string                      { return p.request.ReleaseName }
func (p *PreparedUpgrade) Namespace() string                        { return p.request.Namespace }
func (p *PreparedUpgrade) CurrentRevision() int                     { return p.currentIdentity.revision }
func (p *PreparedUpgrade) CurrentChartVersion() string              { return p.currentIdentity.chartVersion }
func (p *PreparedUpgrade) ChartVersion() string                     { return p.targetVersion }
func (p *PreparedUpgrade) AppVersion() string                       { return p.targetAppVersion }
func (p *PreparedUpgrade) CurrentDeployment() DeploymentRef         { return p.currentWorkload }
func (p *PreparedUpgrade) Deployment() DeploymentRef                { return p.targetWorkload }
func (p *PreparedUpgrade) CurrentManifest() string                  { return p.currentManifest }
func (p *PreparedUpgrade) TargetManifest() string                   { return p.targetManifest }
func (p *PreparedUpgrade) CurrentValues() CloudUpgradeValuesSummary { return p.currentValues.clone() }

// PrepareCloudUpgrade inspects a deployed official Radar release, resolves the
// latest eligible stable chart by default (or req.Version when supplied), and
// performs a server-side Helm upgrade dry-run. The exact chart archive and
// current release revision are retained for a later validated atomic upgrade.
func (c *Client) PrepareCloudUpgrade(ctx context.Context, req *InstallRequest, minimumStableVersion string) (*PreparedUpgrade, error) {
	if c == nil {
		return nil, errors.New("prepare cloud upgrade: nil helm client")
	}
	request, err := cloneInstallRequest(req)
	if err != nil {
		return nil, err
	}
	actionConfig, err := c.getActionConfig(request.Namespace)
	if err != nil {
		return nil, err
	}
	return c.prepareCloudUpgradeWith(ctx, actionConfig, request, minimumStableVersion)
}

// ResolvePreparedChartSummary resolves and verifies a target chart without
// inspecting or mutating a Helm release. GitOps handoff uses this to tell the
// operator the exact stable chart/app version to commit at the source of truth.
func (c *Client) ResolvePreparedChartSummary(ctx context.Context, req *InstallRequest, minimumStableVersion string) (PreparedChartSummary, error) {
	if c == nil {
		return PreparedChartSummary{}, errors.New("resolve prepared chart: nil helm client")
	}
	request, err := cloneInstallRequest(req)
	if err != nil {
		return PreparedChartSummary{}, err
	}
	loaded, exactVersion, err := c.resolvePreparedChart(ctx, &request, minimumStableVersion)
	if err != nil {
		return PreparedChartSummary{}, err
	}
	if err := validateOfficialRadarChart(request.ReleaseName, request.Namespace, loaded); err != nil {
		return PreparedChartSummary{}, err
	}
	return PreparedChartSummary{ChartVersion: exactVersion, AppVersion: loaded.Metadata.AppVersion}, nil
}

func (c *Client) prepareCloudUpgradeWith(ctx context.Context, actionConfig *action.Configuration, request InstallRequest, minimumStableVersion string) (*PreparedUpgrade, error) {
	inspection, err := inspectCloudReleaseWith(actionConfig, request.ReleaseName)
	if err != nil {
		return nil, err
	}
	if inspection.State != CloudReleaseDeployed {
		return nil, &CloudReleaseNotDeployedError{Name: request.ReleaseName, Namespace: request.Namespace, Inspection: inspection}
	}
	current, err := action.NewGet(actionConfig).Run(request.ReleaseName)
	if err != nil {
		return nil, fmt.Errorf("get deployed Radar release: %w", err)
	}
	if err := validateOfficialRadarRelease(current); err != nil {
		return nil, err
	}

	loaded, exactVersion, err := c.resolvePreparedChart(ctx, &request, minimumStableVersion)
	if err != nil {
		return nil, err
	}
	if err := validateOfficialRadarChart(request.ReleaseName, request.Namespace, loaded); err != nil {
		return nil, err
	}
	if err := RejectChartDowngrade(current.Chart.Metadata.Version, exactVersion); err != nil {
		return nil, err
	}
	request.Version = exactVersion
	request.Values = cloudUpgradeValues(request.Values)

	currentWorkload, err := preCloudDeploymentRef(current.Manifest, request.Namespace, request.ReleaseName)
	if err != nil {
		return nil, fmt.Errorf("inspect existing Radar workload: %w", err)
	}
	values, err := summarizeCloudUpgradeValues(current)
	if err != nil {
		return nil, fmt.Errorf("inspect existing Radar values: %w", err)
	}
	dryRun, err := runServerUpgradeDryRun(ctx, actionConfig, &request, loaded)
	if err != nil {
		return nil, fmt.Errorf("server dry-run upgrade: %w", err)
	}
	if err := validatePreparedCloudMutationSurface(dryRun); err != nil {
		return nil, err
	}
	targetWorkload, err := cloudDeploymentRef(dryRun.Manifest, request.Namespace)
	if err != nil {
		return nil, fmt.Errorf("inspect rendered cloud workload: %w", err)
	}

	identity, err := releaseIdentity(current)
	if err != nil {
		return nil, fmt.Errorf("record existing Radar release identity: %w", err)
	}
	return &PreparedUpgrade{
		client: c, request: request, chart: loaded,
		currentIdentity: identity,
		currentManifest: current.Manifest, targetManifest: dryRun.Manifest,
		currentValues: values, currentWorkload: currentWorkload,
		targetWorkload: targetWorkload, targetVersion: exactVersion,
		targetAppVersion: loaded.Metadata.AppVersion,
	}, nil
}

// Validate repeats the revision/identity check and server dry-run using final
// enrollment values. It must run before writing the Cloud token Secret.
func (p *PreparedUpgrade) Validate(ctx context.Context, values map[string]any) error {
	_, err := p.validateManifest(ctx, values)
	return err
}

// ValidateManifest is Validate plus the final rendered manifest. The Cloud
// caller can use it to run exact create/update/delete and bind/escalate SSARs;
// Helm's server dry-run does not itself authorize the eventual update calls.
func (p *PreparedUpgrade) ValidateManifest(ctx context.Context, values map[string]any) (string, error) {
	return p.validateManifest(ctx, values)
}

func (p *PreparedUpgrade) validateManifest(ctx context.Context, values map[string]any) (string, error) {
	if err := p.valid(); err != nil {
		return "", err
	}
	actionConfig, err := p.client.getActionConfig(p.request.Namespace)
	if err != nil {
		return "", err
	}
	return p.validateManifestWith(ctx, actionConfig, values)
}

func (p *PreparedUpgrade) validateManifestWith(ctx context.Context, actionConfig *action.Configuration, values map[string]any) (string, error) {
	if _, err := p.recheck(actionConfig); err != nil {
		return "", err
	}
	request := p.request
	request.Values = cloudUpgradeValues(values)
	dryRun, err := runServerUpgradeDryRun(ctx, actionConfig, &request, p.chart)
	if err != nil {
		return "", fmt.Errorf("server dry-run upgrade with final values: %w", err)
	}
	if err := validatePreparedCloudMutationSurface(dryRun); err != nil {
		return "", err
	}
	deployment, err := cloudDeploymentRef(dryRun.Manifest, request.Namespace)
	if err != nil {
		return "", fmt.Errorf("inspect rendered cloud workload: %w", err)
	}
	if deployment != p.targetWorkload {
		return "", fmt.Errorf("final values changed the prepared workload from %s/%s (%s) to %s/%s (%s)",
			p.targetWorkload.Namespace, p.targetWorkload.Name, p.targetWorkload.Selector,
			deployment.Namespace, deployment.Name, deployment.Selector)
	}
	return dryRun.Manifest, nil
}

// Upgrade applies the pinned chart synchronously with Helm's atomic rollback
// and cleanup of resources newly created by a failed attempt. It performs its
// own final dry-run and identity recheck even if the caller already validated.
func (p *PreparedUpgrade) Upgrade(ctx context.Context, values map[string]any) (*HelmRelease, error) {
	if err := p.valid(); err != nil {
		return nil, err
	}
	actionConfig, err := p.client.getActionConfig(p.request.Namespace)
	if err != nil {
		return nil, err
	}
	if _, err := p.validateManifestWith(ctx, actionConfig, values); err != nil {
		return nil, fmt.Errorf("final upgrade validation: %w", err)
	}
	if _, err := p.recheck(actionConfig); err != nil {
		return nil, err
	}

	request := p.request
	request.Values = cloudUpgradeValues(values)
	upgrade := preparedUpgradeAction(actionConfig, p.request.Namespace)
	upgrade.Atomic = true
	upgrade.CleanupOnFail = true
	// Run synchronously. RunWithContext returns when ctx is cancelled while the
	// Helm operation continues in a goroutine, which would let the caller clean
	// up the token Secret underneath an in-flight upgrade.
	rel, err := upgrade.Run(p.request.ReleaseName, p.chart, request.Values)
	if err != nil {
		return nil, fmt.Errorf("atomic cloud upgrade failed: %w", err)
	}
	return installedHelmRelease(rel), nil
}

// VerifyRolledBack reports whether the active release has the exact chart,
// manifest, and values from before adoption (Helm may record that rollback as a
// later revision). After a failed atomic upgrade, callers may delete the
// attempt's token Secret only when this returns true; an error or false result
// must keep the Secret so a partially-active Cloud workload is not stranded.
func (p *PreparedUpgrade) VerifyRolledBack(ctx context.Context) (bool, error) {
	if err := p.valid(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	actionConfig, err := p.client.getActionConfig(p.request.Namespace)
	if err != nil {
		return false, err
	}
	current, err := action.NewGet(actionConfig).Run(p.request.ReleaseName)
	if err != nil {
		return false, fmt.Errorf("verify cloud upgrade rollback: %w", err)
	}
	actual, err := releaseIdentity(current)
	if err != nil {
		return false, fmt.Errorf("verify cloud upgrade rollback identity: %w", err)
	}
	return p.currentIdentity.sameReleaseContent(actual), nil
}

func (i preparedReleaseIdentity) sameReleaseContent(actual preparedReleaseIdentity) bool {
	return actual.status == release.StatusDeployed &&
		i.status == release.StatusDeployed &&
		actual.chartName == i.chartName &&
		actual.chartVersion == i.chartVersion &&
		actual.appVersion == i.appVersion &&
		actual.manifestHash == i.manifestHash &&
		actual.configHash == i.configHash
}

func deployedCloudDisabled(current *release.Release) (bool, error) {
	if current.Info == nil || current.Info.Status != release.StatusDeployed {
		return false, nil
	}
	summary, err := summarizeCloudUpgradeValues(current)
	if err != nil {
		return false, err
	}
	return !summary.CloudEnabled, nil
}

func (p *PreparedUpgrade) valid() error {
	if p == nil || p.client == nil || p.chart == nil {
		return errors.New("prepared cloud upgrade is invalid")
	}
	return nil
}

func (p *PreparedUpgrade) recheck(actionConfig *action.Configuration) (*release.Release, error) {
	current, err := action.NewGet(actionConfig).Run(p.request.ReleaseName)
	if err != nil {
		return nil, &ReleaseChangedError{
			Name: p.request.ReleaseName, Namespace: p.request.Namespace,
			Expected: p.currentIdentity.String(), Actual: err.Error(),
		}
	}
	actual, err := releaseIdentity(current)
	if err != nil {
		return nil, &ReleaseChangedError{
			Name: p.request.ReleaseName, Namespace: p.request.Namespace,
			Expected: p.currentIdentity.String(), Actual: fmt.Sprintf("unreadable values: %v", err),
		}
	}
	if actual != p.currentIdentity {
		return nil, &ReleaseChangedError{
			Name: p.request.ReleaseName, Namespace: p.request.Namespace,
			Expected: p.currentIdentity.String(), Actual: actual.String(),
		}
	}
	return current, nil
}

func (i preparedReleaseIdentity) String() string {
	return fmt.Sprintf("revision %d, status %s, chart %s %s, app %s, manifest %x, values %x",
		i.revision, i.status, i.chartName, i.chartVersion, i.appVersion, i.manifestHash[:6], i.configHash[:6])
}

func releaseIdentity(rel *release.Release) (preparedReleaseIdentity, error) {
	if rel == nil {
		return preparedReleaseIdentity{}, errors.New("release is nil")
	}
	identity := preparedReleaseIdentity{manifestHash: sha256.Sum256([]byte(rel.Manifest))}
	configJSON, err := json.Marshal(rel.Config)
	if err != nil {
		return preparedReleaseIdentity{}, fmt.Errorf("marshal release values: %w", err)
	}
	identity.configHash = sha256.Sum256(configJSON)
	identity.revision = rel.Version
	if rel.Info != nil {
		identity.status = rel.Info.Status
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		identity.chartName = rel.Chart.Metadata.Name
		identity.chartVersion = rel.Chart.Metadata.Version
		identity.appVersion = rel.Chart.Metadata.AppVersion
	}
	return identity, nil
}

func runServerUpgradeDryRun(ctx context.Context, actionConfig *action.Configuration, req *InstallRequest, loaded *chart.Chart) (*release.Release, error) {
	upgrade := preparedUpgradeAction(actionConfig, req.Namespace)
	upgrade.DryRun = true
	upgrade.DryRunOption = "server"
	upgrade.HideSecret = true
	return upgrade.RunWithContext(ctx, req.ReleaseName, loaded, req.Values)
}

func preparedUpgradeAction(actionConfig *action.Configuration, namespace string) *action.Upgrade {
	upgrade := action.NewUpgrade(actionConfig)
	upgrade.Namespace = namespace
	upgrade.Timeout = 120 * time.Second
	upgrade.ResetThenReuseValues = true
	return upgrade
}

// cloudUpgradeValues clones the caller's overlay and deliberately clears an
// old image.tag pin. Adoption upgrades Radar to the selected chart's AppVersion
// by default while preserving every other existing user-supplied value through
// ResetThenReuseValues.
func cloudUpgradeValues(values map[string]any) map[string]any {
	result := cloneInstallValues(values)
	if result == nil {
		result = map[string]any{}
	}
	image, _ := result["image"].(map[string]any)
	image = cloneInstallValues(image)
	if image == nil {
		image = map[string]any{}
	}
	image["tag"] = ""
	result["image"] = image
	return result
}

// RejectChartDowngrade validates two exact stable versions and refuses a lower
// target. Native Helm adoption and GitOps handoff share this invariant.
func RejectChartDowngrade(current, target string) error {
	currentVersion, err := semver.StrictNewVersion(current)
	if err != nil {
		return fmt.Errorf("existing Radar chart has invalid semantic version %q", current)
	}
	targetVersion, err := semver.StrictNewVersion(target)
	if err != nil {
		return fmt.Errorf("target Radar chart has invalid semantic version %q", target)
	}
	if targetVersion.LessThan(currentVersion) {
		return &ChartDowngradeError{Current: current, Target: target}
	}
	return nil
}

func validateOfficialRadarRelease(rel *release.Release) error {
	name, namespace := "", ""
	if rel != nil {
		name, namespace = rel.Name, rel.Namespace
	}
	var loaded *chart.Chart
	if rel != nil {
		loaded = rel.Chart
	}
	return validateOfficialRadarChart(name, namespace, loaded)
}

func validateOfficialRadarChart(releaseName, namespace string, loaded *chart.Chart) error {
	metadata := (*chart.Metadata)(nil)
	if loaded != nil {
		metadata = loaded.Metadata
	}
	chartName, chartVersion := "", ""
	if metadata != nil {
		chartName, chartVersion = metadata.Name, metadata.Version
	}
	officialSource := false
	if metadata != nil {
		for _, source := range metadata.Sources {
			if strings.TrimSuffix(source, "/") == "https://github.com/skyhook-io/radar" {
				officialSource = true
				break
			}
		}
	}
	if chartName != "radar" || !officialSource {
		return &NotOfficialRadarReleaseError{
			Name: releaseName, Namespace: namespace,
			ChartName: chartName, ChartVersion: chartVersion,
		}
	}
	return nil
}

func summarizeCloudUpgradeValues(rel *release.Release) (CloudUpgradeValuesSummary, error) {
	if rel == nil || rel.Chart == nil || rel.Chart.Metadata == nil {
		return CloudUpgradeValuesSummary{}, errors.New("release has no chart metadata")
	}
	effective, err := chartutil.CoalesceValues(rel.Chart, rel.Config)
	if err != nil {
		return CloudUpgradeValuesSummary{}, err
	}
	imageTag := stringValue(effective, "image", "tag")
	if imageTag == "" {
		imageTag = rel.Chart.Metadata.AppVersion
	}
	summary := CloudUpgradeValuesSummary{
		AuthMode:            stringValue(effective, "auth", "mode"),
		CloudEnabled:        boolValue(effective, "cloud", "enabled"),
		CloudExistingSecret: stringValue(effective, "cloud", "existingSecret"),
		CloudTokenSet:       stringValue(effective, "cloud", "token") != "",
		ImageRepository:     stringValue(effective, "image", "repository"),
		ImageTag:            stringValue(effective, "image", "tag"),
		EffectiveImageTag:   imageTag,
		RBAC:                map[string]bool{},
	}
	if summary.AuthMode == "" {
		summary.AuthMode = "none"
	}
	if rbac, ok := nestedMap(effective, "rbac"); ok {
		for key, value := range rbac {
			if enabled, ok := value.(bool); ok {
				summary.RBAC[key] = enabled
			}
		}
	}
	return summary, nil
}

func preCloudDeploymentRef(manifest, defaultNamespace, releaseName string) (DeploymentRef, error) {
	return deploymentRef(manifest, defaultNamespace, func(object manifestDeployment) bool {
		if object.Labels["app.kubernetes.io/instance"] != "" && object.Labels["app.kubernetes.io/instance"] != releaseName {
			return false
		}
		if strings.HasPrefix(object.Labels["helm.sh/chart"], "radar-") {
			return true
		}
		return slices.Contains(object.ContainerNames, "radar")
	}, "Radar Deployment in the existing release")
}

func nestedMap(values map[string]any, path ...string) (map[string]any, bool) {
	current := values
	for i, key := range path {
		value, ok := current[key]
		if !ok {
			return nil, false
		}
		if i == len(path)-1 {
			result, ok := value.(map[string]any)
			return result, ok
		}
		current, ok = value.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func nestedValue(values map[string]any, path ...string) (any, bool) {
	if len(path) == 0 {
		return nil, false
	}
	parent, ok := nestedMap(values, path[:len(path)-1]...)
	if !ok {
		if len(path) == 1 {
			parent = values
		} else {
			return nil, false
		}
	}
	value, ok := parent[path[len(path)-1]]
	return value, ok
}

func stringValue(values map[string]any, path ...string) string {
	value, _ := nestedValue(values, path...)
	result, _ := value.(string)
	return result
}

func boolValue(values map[string]any, path ...string) bool {
	value, _ := nestedValue(values, path...)
	result, _ := value.(bool)
	return result
}

func cloneBoolMap(values map[string]bool) map[string]bool {
	result := make(map[string]bool, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
