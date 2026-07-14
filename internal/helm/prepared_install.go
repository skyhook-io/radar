package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	maxPreparedIndexBytes = 32 << 20
	maxPreparedChartBytes = 128 << 20
)

// ChartVersionError reports why a requested/resolved chart is not an eligible
// stable version. Cloud enrollment uses this before minting a cluster token.
type ChartVersionError struct {
	Requested string
	Resolved  string
	Minimum   string
	Reason    string
}

func (e *ChartVersionError) Error() string {
	version := e.Resolved
	if version == "" {
		version = e.Requested
	}
	if version == "" {
		version = "latest"
	}
	return fmt.Sprintf("chart version %q is not eligible: %s (minimum stable version %s)", version, e.Reason, e.Minimum)
}

// DeploymentRef is the actual Radar workload rendered by Helm. Selector is a
// kubectl-compatible label selector, not a guessed release-name convention.
type DeploymentRef struct {
	Name      string
	Namespace string
	Selector  string
}

// PreparedInstall pins the exact downloaded chart bytes, target and rendered
// workload across an external approval flow. Its fields are deliberately
// private so callers cannot swap a chart or target after the server dry-run.
type PreparedInstall struct {
	client     *Client
	request    InstallRequest
	chart      *chart.Chart
	version    string
	manifest   string
	deployment DeploymentRef
}

func (p *PreparedInstall) ChartVersion() string { return p.version }
func (p *PreparedInstall) AppVersion() string {
	if p == nil || p.chart == nil || p.chart.Metadata == nil {
		return ""
	}
	return p.chart.Metadata.AppVersion
}
func (p *PreparedInstall) ReleaseName() string { return p.request.ReleaseName }
func (p *PreparedInstall) Namespace() string   { return p.request.Namespace }

// TargetManifest is the exact chart manifest rendered with the non-secret
// placeholder Cloud values pinned by PrepareFreshInstall. Callers use it to
// prove the Kubernetes mutation shape before Hub enrollment.
func (p *PreparedInstall) TargetManifest() string {
	if p == nil {
		return ""
	}
	return p.manifest
}
func (p *PreparedInstall) Deployment() DeploymentRef {
	return p.deployment
}

// PrepareFreshInstall resolves and downloads one exact stable chart, rejects
// every existing Helm history state, and asks Helm to render/validate it with a
// server dry-run. The returned plan retains the loaded chart in memory, so a
// later install cannot drift to a different repository version.
func (c *Client) PrepareFreshInstall(ctx context.Context, req *InstallRequest, minimumStableVersion string) (*PreparedInstall, error) {
	if c == nil {
		return nil, errors.New("prepare install: nil helm client")
	}
	request, err := cloneInstallRequest(req)
	if err != nil {
		return nil, err
	}
	actionConfig, err := c.getActionConfig(request.Namespace)
	if err != nil {
		return nil, err
	}
	if err := freshInstallCheck(actionConfig, request.ReleaseName, request.Namespace); err != nil {
		return nil, err
	}

	loaded, exactVersion, err := c.resolvePreparedChart(ctx, &request, minimumStableVersion)
	if err != nil {
		return nil, err
	}
	request.Version = exactVersion
	dryRun, err := runServerDryRun(ctx, actionConfig, &request, loaded)
	if err != nil {
		return nil, fmt.Errorf("server dry-run: %w", err)
	}
	if err := validatePreparedCloudMutationSurface(dryRun); err != nil {
		return nil, err
	}
	deployment, err := cloudDeploymentRef(dryRun.Manifest, request.Namespace)
	if err != nil {
		return nil, fmt.Errorf("inspect rendered cloud workload: %w", err)
	}
	return &PreparedInstall{
		client: c, request: request, chart: loaded,
		version: exactVersion, manifest: dryRun.Manifest, deployment: deployment,
	}, nil
}

// Validate renders the already-pinned chart with final runtime values. Cloud
// enrollment calls this after approval but before writing the token Secret.
func (p *PreparedInstall) Validate(ctx context.Context, values map[string]any) error {
	if p == nil || p.client == nil || p.chart == nil {
		return errors.New("validate prepared install: invalid plan")
	}
	actionConfig, err := p.client.getActionConfig(p.request.Namespace)
	if err != nil {
		return err
	}
	if err := freshInstallCheck(actionConfig, p.request.ReleaseName, p.request.Namespace); err != nil {
		return err
	}
	request := p.request
	request.Values = cloneInstallValues(values)
	dryRun, err := runServerDryRun(ctx, actionConfig, &request, p.chart)
	if err != nil {
		return fmt.Errorf("server dry-run with final values: %w", err)
	}
	if err := validatePreparedCloudMutationSurface(dryRun); err != nil {
		return err
	}
	deployment, err := cloudDeploymentRef(dryRun.Manifest, request.Namespace)
	if err != nil {
		return fmt.Errorf("inspect rendered cloud workload: %w", err)
	}
	if deployment != p.deployment {
		return fmt.Errorf("final values changed the prepared workload from %s/%s (%s) to %s/%s (%s)",
			p.deployment.Namespace, p.deployment.Name, p.deployment.Selector,
			deployment.Namespace, deployment.Name, deployment.Selector)
	}
	return nil
}

// Install applies the pinned chart with final values. It rechecks the release
// immediately before the normal, non-atomic Helm install to catch approval-time
// races. Call Validate first when side-effect ordering matters.
func (p *PreparedInstall) Install(ctx context.Context, values map[string]any) (*HelmRelease, error) {
	if p == nil || p.client == nil || p.chart == nil {
		return nil, errors.New("install prepared chart: invalid plan")
	}
	actionConfig, err := p.client.getActionConfig(p.request.Namespace)
	if err != nil {
		return nil, err
	}
	if err := freshInstallCheck(actionConfig, p.request.ReleaseName, p.request.Namespace); err != nil {
		return nil, err
	}
	request := p.request
	request.Values = cloneInstallValues(values)
	install := action.NewInstall(actionConfig)
	install.ReleaseName = request.ReleaseName
	install.Namespace = request.Namespace
	install.CreateNamespace = request.CreateNamespace
	install.Timeout = 120 * time.Second
	install.Version = p.version
	// action.Install.RunWithContext returns on cancellation while Helm continues
	// mutating the cluster in a background goroutine. That is unsafe here because
	// the caller would interpret the return as failure and clean up its token
	// Secret underneath the still-running install. Run synchronously instead;
	// context remains honored by chart fetches and both dry-runs.
	rel, err := install.Run(p.chart, request.Values)
	if err != nil {
		return nil, fmt.Errorf("install failed: %w", err)
	}
	return installedHelmRelease(rel), nil
}

func runServerDryRun(ctx context.Context, actionConfig *action.Configuration, req *InstallRequest, loaded *chart.Chart) (*release.Release, error) {
	install := action.NewInstall(actionConfig)
	install.ReleaseName = req.ReleaseName
	install.Namespace = req.Namespace
	install.CreateNamespace = req.CreateNamespace
	install.Timeout = 120 * time.Second
	install.Version = req.Version
	install.DryRun = true
	install.DryRunOption = "server"
	install.HideSecret = true
	return install.RunWithContext(ctx, loaded, req.Values)
}

// validatePreparedCloudMutationSurface fails closed when Helm renders objects
// that the install permission preflight cannot prove exactly. Hooks live
// outside release.Manifest, and HideSecret deliberately removes Secret bodies
// from it, so allowing either would create an unreviewed mutation path.
func validatePreparedCloudMutationSurface(rel *release.Release) error {
	if rel == nil {
		return errors.New("prepared chart returned no release")
	}
	for _, hook := range rel.Hooks {
		if hook == nil {
			continue
		}
		return fmt.Errorf("prepared Radar chart renders Helm hook %q; Cloud install permission preflight does not support chart hooks", hook.Name)
	}
	if rel.Chart != nil {
		if crds := rel.Chart.CRDObjects(); len(crds) > 0 {
			return fmt.Errorf("prepared Radar chart contains CRD %q; Cloud install permission preflight does not support chart CRDs", crds[0].Name)
		}
	}
	if strings.Contains(rel.Manifest, "# HIDDEN: The Secret output has been suppressed") {
		return errors.New("prepared Radar chart renders a Secret hidden from Cloud install permission preflight")
	}
	return nil
}

func cloneInstallRequest(req *InstallRequest) (InstallRequest, error) {
	if req == nil {
		return InstallRequest{}, errors.New("prepare install: nil request")
	}
	if strings.TrimSpace(req.ReleaseName) == "" || strings.TrimSpace(req.Namespace) == "" || strings.TrimSpace(req.ChartName) == "" || strings.TrimSpace(req.Repository) == "" {
		return InstallRequest{}, errors.New("prepare install: release name, namespace, chart name, and repository are required")
	}
	copy := *req
	copy.Values = cloneInstallValues(req.Values)
	return copy, nil
}

func cloneInstallValues(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	copy := make(map[string]any, len(values))
	for key, value := range values {
		copy[key] = cloneInstallValue(value)
	}
	return copy
}

func cloneInstallValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneInstallValues(typed)
	case []any:
		copy := make([]any, len(typed))
		for i := range typed {
			copy[i] = cloneInstallValue(typed[i])
		}
		return copy
	default:
		return value
	}
}

func (c *Client) resolvePreparedChart(ctx context.Context, req *InstallRequest, minimum string) (*chart.Chart, string, error) {
	index, baseURL, err := c.loadPreparedIndex(ctx, req.Repository)
	if err != nil {
		return nil, "", err
	}
	versions := index.Entries[req.ChartName]
	selected, resolved, err := selectStableChartVersion(versions, req.Version, minimum)
	if err != nil {
		return nil, "", err
	}
	if len(selected.URLs) == 0 {
		return nil, "", fmt.Errorf("chart %s version %s has no download URL", req.ChartName, resolved)
	}
	digest := strings.TrimSpace(strings.ToLower(selected.Digest))
	if digest == "" {
		return nil, "", fmt.Errorf("chart %s version %s has no SHA-256 digest in the repository index", req.ChartName, resolved)
	}
	want, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(want) != sha256.Size {
		return nil, "", fmt.Errorf("chart %s version %s has an invalid SHA-256 digest in the repository index", req.ChartName, resolved)
	}
	chartURL, err := resolveChartURL(baseURL, selected.URLs[0])
	if err != nil {
		return nil, "", err
	}
	body, err := fetchPreparedBytes(ctx, chartURL, maxPreparedChartBytes, "chart")
	if err != nil {
		return nil, "", err
	}
	actual := sha256.Sum256(body)
	if !bytes.Equal(actual[:], want) {
		return nil, "", fmt.Errorf("chart %s version %s digest does not match repository index", req.ChartName, resolved)
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("load chart %s version %s: %w", req.ChartName, resolved, err)
	}
	if loaded.Metadata == nil || loaded.Metadata.Name != req.ChartName || loaded.Metadata.Version != selected.Version {
		actualName, actualVersion := "", ""
		if loaded.Metadata != nil {
			actualName, actualVersion = loaded.Metadata.Name, loaded.Metadata.Version
		}
		return nil, "", fmt.Errorf("downloaded chart identity %q version %q does not match index entry %q version %q",
			actualName, actualVersion, req.ChartName, selected.Version)
	}
	return loaded, resolved, nil
}

func (c *Client) loadPreparedIndex(ctx context.Context, repository string) (*repo.IndexFile, string, error) {
	if parsed, err := url.Parse(repository); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		base := strings.TrimSuffix(repository, "/") + "/"
		body, err := fetchPreparedBytes(ctx, base+"index.yaml", maxPreparedIndexBytes, "repository index")
		if err != nil {
			return nil, "", err
		}
		index := &repo.IndexFile{}
		if err := yaml.Unmarshal(body, index); err != nil {
			return nil, "", fmt.Errorf("parse repository index: %w", err)
		}
		index.SortEntries()
		return index, base, nil
	}

	repoFile, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		return nil, "", fmt.Errorf("load repository config: %w", err)
	}
	var entry *repo.Entry
	for _, candidate := range repoFile.Repositories {
		if candidate.Name == repository {
			entry = candidate
			break
		}
	}
	if entry == nil {
		return nil, "", fmt.Errorf("repository %s not found", repository)
	}
	index, err := repo.LoadIndexFile(filepath.Join(c.settings.RepositoryCache, repository+"-index.yaml"))
	if err != nil {
		return nil, "", fmt.Errorf("load repository index: %w", err)
	}
	index.SortEntries()
	return index, strings.TrimSuffix(entry.URL, "/") + "/", nil
}

func selectStableChartVersion(versions repo.ChartVersions, requested, minimum string) (*repo.ChartVersion, string, error) {
	minimumVersion, err := semver.StrictNewVersion(minimum)
	if err != nil || minimumVersion.Prerelease() != "" {
		return nil, "", fmt.Errorf("invalid minimum stable chart version %q", minimum)
	}
	var selected *repo.ChartVersion
	var selectedVersion *semver.Version
	for _, candidate := range versions {
		if candidate == nil || candidate.Metadata == nil || candidate.Removed {
			continue
		}
		parsed, parseErr := semver.StrictNewVersion(candidate.Version)
		if parseErr != nil || parsed.Prerelease() != "" {
			if requested != "" && requested != "latest" && candidate.Version == requested {
				return nil, "", &ChartVersionError{Requested: requested, Resolved: candidate.Version, Minimum: minimum, Reason: "version must be stable semantic versioning"}
			}
			continue
		}
		if requested != "" && requested != "latest" && candidate.Version != requested {
			continue
		}
		if selectedVersion == nil || parsed.GreaterThan(selectedVersion) {
			selected, selectedVersion = candidate, parsed
		}
	}
	if selected == nil {
		reason := "no stable published version was found"
		if requested != "" && requested != "latest" {
			reason = "requested version was not found"
		}
		return nil, "", &ChartVersionError{Requested: requested, Minimum: minimum, Reason: reason}
	}
	if selectedVersion.LessThan(minimumVersion) {
		return nil, "", &ChartVersionError{
			Requested: requested, Resolved: selected.Version, Minimum: minimum,
			Reason: "version is too old for enforced Cloud RBAC",
		}
	}
	return selected, selected.Version, nil
}

func resolveChartURL(base, raw string) (string, error) {
	reference, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse chart URL %q: %w", raw, err)
	}
	if reference.IsAbs() {
		return reference.String(), nil
	}
	root, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse repository URL %q: %w", base, err)
	}
	return root.ResolveReference(reference).String(), nil
}

func fetchPreparedBytes(ctx context.Context, target string, maxBytes int64, kind string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", kind, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", kind, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: server returned %s", kind, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("read %s: response exceeds %d bytes", kind, maxBytes)
	}
	return body, nil
}

func cloudDeploymentRef(manifest, defaultNamespace string) (DeploymentRef, error) {
	return deploymentRef(manifest, defaultNamespace, func(object manifestDeployment) bool {
		return object.CloudMode
	}, "Deployment with RADAR_CLOUD_MODE=true")
}

type manifestDeployment struct {
	Labels         map[string]string
	ContainerNames []string
	CloudMode      bool
}

func deploymentRef(manifest, defaultNamespace string, match func(manifestDeployment) bool, description string) (DeploymentRef, error) {
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	var matches []DeploymentRef
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return DeploymentRef{}, err
		}
		if len(object) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: object}
		if u.GetAPIVersion() != "apps/v1" || u.GetKind() != "Deployment" {
			continue
		}
		deployment := manifestDeployment{
			Labels: u.GetLabels(), CloudMode: hasCloudModeEnv(u),
			ContainerNames: deploymentContainerNames(u),
		}
		if !match(deployment) {
			continue
		}
		selectorMap, found, err := unstructured.NestedMap(object, "spec", "selector")
		if err != nil || !found {
			return DeploymentRef{}, fmt.Errorf("deployment %q has no valid spec.selector", u.GetName())
		}
		var selectorSpec metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, &selectorSpec); err != nil {
			return DeploymentRef{}, fmt.Errorf("deployment %q selector: %w", u.GetName(), err)
		}
		selector, err := metav1.LabelSelectorAsSelector(&selectorSpec)
		if err != nil {
			return DeploymentRef{}, fmt.Errorf("deployment %q selector: %w", u.GetName(), err)
		}
		namespace := u.GetNamespace()
		if namespace == "" {
			namespace = defaultNamespace
		}
		matches = append(matches, DeploymentRef{Name: u.GetName(), Namespace: namespace, Selector: selector.String()})
	}
	if len(matches) != 1 {
		return DeploymentRef{}, fmt.Errorf("expected exactly one %s, found %d", description, len(matches))
	}
	return matches[0], nil
}

func deploymentContainerNames(deployment *unstructured.Unstructured) []string {
	containers, found, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if !found {
		return nil
	}
	names := make([]string, 0, len(containers))
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := container["name"].(string)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func hasCloudModeEnv(deployment *unstructured.Unstructured) bool {
	containers, found, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if !found {
		return false
	}
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			continue
		}
		env, _, _ := unstructured.NestedSlice(container, "env")
		for _, raw := range env {
			entry, ok := raw.(map[string]any)
			if !ok || entry["name"] != "RADAR_CLOUD_MODE" || entry["value"] != "true" {
				continue
			}
			return true
		}
	}
	return false
}
