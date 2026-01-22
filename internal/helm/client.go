package helm

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// Client provides access to Helm releases
type Client struct {
	mu         sync.RWMutex
	settings   *cli.EnvSettings
	kubeconfig string
}

var (
	globalClient   *Client
	clientOnce     sync.Once
	helmClientMu   sync.Mutex
)

// Initialize sets up the global Helm client
func Initialize(kubeconfig string) error {
	var initErr error
	clientOnce.Do(func() {
		settings := cli.New()
		if kubeconfig != "" {
			settings.KubeConfig = kubeconfig
		}
		globalClient = &Client{
			settings:   settings,
			kubeconfig: kubeconfig,
		}
		log.Printf("Helm client initialized")
	})
	return initErr
}

// GetClient returns the global Helm client
func GetClient() *Client {
	return globalClient
}

// ResetClient clears the Helm client instance
// This must be called before ReinitClient when switching contexts
func ResetClient() {
	helmClientMu.Lock()
	defer helmClientMu.Unlock()

	globalClient = nil
	clientOnce = sync.Once{}
}

// ReinitClient reinitializes the Helm client after a context switch
// Must call ResetClient first
func ReinitClient(kubeconfig string) error {
	return Initialize(kubeconfig)
}


// getActionConfig creates a new action configuration for the given namespace
func (c *Client) getActionConfig(namespace string) (*action.Configuration, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	actionConfig := new(action.Configuration)

	// Use RESTClientGetter for kubeconfig
	configFlags := genericclioptions.NewConfigFlags(true)
	if c.kubeconfig != "" {
		configFlags.KubeConfig = &c.kubeconfig
	}
	if namespace != "" {
		configFlags.Namespace = &namespace
	}

	if err := actionConfig.Init(configFlags, namespace, "secrets", log.Printf); err != nil {
		return nil, fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	return actionConfig, nil
}

// ListReleases returns all Helm releases, optionally filtered by namespace
func (c *Client) ListReleases(namespace string) ([]HelmRelease, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	listAction := action.NewList(actionConfig)
	listAction.All = true
	listAction.AllNamespaces = namespace == ""
	listAction.StateMask = action.ListAll

	releases, err := listAction.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to list helm releases: %w", err)
	}

	result := make([]HelmRelease, 0, len(releases))
	for _, rel := range releases {
		result = append(result, toHelmRelease(rel))
	}

	// Sort by namespace, then name
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// GetRelease returns details for a specific release
func (c *Client) GetRelease(namespace, name string) (*HelmReleaseDetail, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	// Get the latest release
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release %s/%s: %w", namespace, name, err)
	}

	// Get release history
	historyAction := action.NewHistory(actionConfig)
	historyAction.Max = 256
	history, err := historyAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release history: %w", err)
	}

	// Convert history
	revisions := make([]HelmRevision, 0, len(history))
	for _, h := range history {
		revisions = append(revisions, toHelmRevision(h))
	}

	// Sort by revision descending (newest first)
	sort.Slice(revisions, func(i, j int) bool {
		return revisions[i].Revision > revisions[j].Revision
	})

	// Parse manifest to get owned resources
	resources := parseManifestResources(rel.Manifest, namespace)

	// Enrich resources with live status from k8s cache
	enrichResourcesWithStatus(resources)

	// Extract hooks
	hooks := extractHooks(rel)

	// Extract README from chart files
	readme := extractReadme(rel)

	// Extract dependencies
	dependencies := extractDependencies(rel)

	detail := &HelmReleaseDetail{
		Name:         rel.Name,
		Namespace:    rel.Namespace,
		Chart:        rel.Chart.Metadata.Name,
		ChartVersion: rel.Chart.Metadata.Version,
		AppVersion:   rel.Chart.Metadata.AppVersion,
		Status:       rel.Info.Status.String(),
		Revision:     rel.Version,
		Updated:      rel.Info.LastDeployed.Time,
		Description:  rel.Info.Description,
		Notes:        rel.Info.Notes,
		History:      revisions,
		Resources:    resources,
		Hooks:        hooks,
		Readme:       readme,
		Dependencies: dependencies,
	}

	return detail, nil
}

// GetManifest returns the rendered manifest for a release at a specific revision
func (c *Client) GetManifest(namespace, name string, revision int) (string, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return "", err
	}

	getAction := action.NewGet(actionConfig)
	if revision > 0 {
		getAction.Version = revision
	}

	rel, err := getAction.Run(name)
	if err != nil {
		return "", fmt.Errorf("failed to get helm release manifest: %w", err)
	}

	return rel.Manifest, nil
}

// GetValues returns the values for a release
func (c *Client) GetValues(namespace, name string, allValues bool) (*HelmValues, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	getValuesAction := action.NewGetValues(actionConfig)
	getValuesAction.AllValues = allValues

	values, err := getValuesAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release values: %w", err)
	}

	result := &HelmValues{
		UserSupplied: values,
	}

	// If allValues requested, also get just user-supplied for comparison
	if allValues {
		getValuesAction.AllValues = false
		userValues, err := getValuesAction.Run(name)
		if err == nil {
			result.UserSupplied = userValues
			result.Computed = values
		}
	}

	return result, nil
}

// GetManifestDiff returns the diff between two revisions
func (c *Client) GetManifestDiff(namespace, name string, revision1, revision2 int) (*ManifestDiff, error) {
	manifest1, err := c.GetManifest(namespace, name, revision1)
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest for revision %d: %w", revision1, err)
	}

	manifest2, err := c.GetManifest(namespace, name, revision2)
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest for revision %d: %w", revision2, err)
	}

	// Compute unified diff
	diff := computeDiff(manifest1, manifest2, revision1, revision2)

	return &ManifestDiff{
		Revision1: revision1,
		Revision2: revision2,
		Diff:      diff,
	}, nil
}

// toHelmRelease converts a helm release to our API type
func toHelmRelease(rel *release.Release) HelmRelease {
	return HelmRelease{
		Name:         rel.Name,
		Namespace:    rel.Namespace,
		Chart:        rel.Chart.Metadata.Name,
		ChartVersion: rel.Chart.Metadata.Version,
		AppVersion:   rel.Chart.Metadata.AppVersion,
		Status:       rel.Info.Status.String(),
		Revision:     rel.Version,
		Updated:      rel.Info.LastDeployed.Time,
	}
}

// toHelmRevision converts a helm release to a revision entry
func toHelmRevision(rel *release.Release) HelmRevision {
	return HelmRevision{
		Revision:    rel.Version,
		Status:      rel.Info.Status.String(),
		Chart:       rel.Chart.Metadata.Name + "-" + rel.Chart.Metadata.Version,
		AppVersion:  rel.Chart.Metadata.AppVersion,
		Description: rel.Info.Description,
		Updated:     rel.Info.LastDeployed.Time,
	}
}

// parseManifestResources extracts K8s resources from a rendered manifest
func parseManifestResources(manifest, defaultNamespace string) []OwnedResource {
	var resources []OwnedResource

	// Split manifest into individual documents
	manifests := releaseutil.SplitManifests(manifest)

	for _, m := range manifests {
		// Simple parsing - look for kind, name, and namespace
		lines := strings.Split(m, "\n")
		var kind, name, namespace string

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "kind:") {
				kind = strings.TrimSpace(strings.TrimPrefix(line, "kind:"))
			} else if strings.HasPrefix(line, "name:") && name == "" {
				// Only take first name (metadata.name, not container names etc)
				name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				// Remove quotes if present
				name = strings.Trim(name, `"'`)
			} else if strings.HasPrefix(line, "namespace:") && namespace == "" {
				namespace = strings.TrimSpace(strings.TrimPrefix(line, "namespace:"))
				namespace = strings.Trim(namespace, `"'`)
			}
		}

		if kind != "" && name != "" {
			if namespace == "" {
				namespace = defaultNamespace
			}
			resources = append(resources, OwnedResource{
				Kind:      kind,
				Name:      name,
				Namespace: namespace,
			})
		}
	}

	// Sort by kind, then name
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}
		return resources[i].Name < resources[j].Name
	})

	return resources
}

// enrichResourcesWithStatus adds live status from k8s cache to resources
func enrichResourcesWithStatus(resources []OwnedResource) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return
	}

	for i := range resources {
		status := cache.GetResourceStatus(resources[i].Kind, resources[i].Namespace, resources[i].Name)
		if status != nil {
			resources[i].Status = status.Status
			resources[i].Ready = status.Ready
			resources[i].Message = status.Message
		}
	}
}

// computeDiff generates a simple unified diff between two manifests
func computeDiff(manifest1, manifest2 string, rev1, rev2 int) string {
	lines1 := strings.Split(manifest1, "\n")
	lines2 := strings.Split(manifest2, "\n")

	var diff bytes.Buffer
	diff.WriteString(fmt.Sprintf("--- Revision %d\n", rev1))
	diff.WriteString(fmt.Sprintf("+++ Revision %d\n", rev2))

	// Simple line-by-line diff (could use a proper diff library for better results)
	// For now, show all lines with their status
	i, j := 0, 0
	for i < len(lines1) || j < len(lines2) {
		if i < len(lines1) && j < len(lines2) {
			if lines1[i] == lines2[j] {
				diff.WriteString(" " + lines1[i] + "\n")
				i++
				j++
			} else {
				// Look ahead to find matching line
				foundMatch := false
				for k := j; k < len(lines2) && k < j+10; k++ {
					if lines1[i] == lines2[k] {
						// Output added lines
						for l := j; l < k; l++ {
							diff.WriteString("+" + lines2[l] + "\n")
						}
						j = k
						foundMatch = true
						break
					}
				}
				if !foundMatch {
					for k := i; k < len(lines1) && k < i+10; k++ {
						if lines2[j] == lines1[k] {
							// Output removed lines
							for l := i; l < k; l++ {
								diff.WriteString("-" + lines1[l] + "\n")
							}
							i = k
							foundMatch = true
							break
						}
					}
				}
				if !foundMatch {
					// Lines are different
					diff.WriteString("-" + lines1[i] + "\n")
					diff.WriteString("+" + lines2[j] + "\n")
					i++
					j++
				}
			}
		} else if i < len(lines1) {
			diff.WriteString("-" + lines1[i] + "\n")
			i++
		} else {
			diff.WriteString("+" + lines2[j] + "\n")
			j++
		}
	}

	return diff.String()
}

// extractHooks extracts hook information from a release
func extractHooks(rel *release.Release) []HelmHook {
	if rel.Hooks == nil {
		return nil
	}

	hooks := make([]HelmHook, 0, len(rel.Hooks))
	for _, h := range rel.Hooks {
		events := make([]string, 0, len(h.Events))
		for _, e := range h.Events {
			events = append(events, string(e))
		}

		hook := HelmHook{
			Name:   h.Name,
			Kind:   h.Kind,
			Events: events,
			Weight: h.Weight,
		}

		// Add status if available
		if h.LastRun.Phase != "" {
			hook.Status = string(h.LastRun.Phase)
		}

		hooks = append(hooks, hook)
	}

	return hooks
}

// extractReadme extracts the README content from chart files
func extractReadme(rel *release.Release) string {
	if rel.Chart == nil || rel.Chart.Files == nil {
		return ""
	}

	// Look for README.md (case-insensitive)
	for _, f := range rel.Chart.Files {
		name := strings.ToLower(f.Name)
		if name == "readme.md" || name == "readme.txt" || name == "readme" {
			return string(f.Data)
		}
	}

	return ""
}

// extractDependencies extracts chart dependencies
func extractDependencies(rel *release.Release) []ChartDependency {
	if rel.Chart == nil || rel.Chart.Metadata == nil || rel.Chart.Metadata.Dependencies == nil {
		return nil
	}

	deps := make([]ChartDependency, 0, len(rel.Chart.Metadata.Dependencies))
	for _, d := range rel.Chart.Metadata.Dependencies {
		dep := ChartDependency{
			Name:       d.Name,
			Version:    d.Version,
			Repository: d.Repository,
			Condition:  d.Condition,
			Enabled:    d.Enabled,
		}
		deps = append(deps, dep)
	}

	return deps
}

// CheckForUpgrade checks if a newer version of the chart is available in configured repos
func (c *Client) CheckForUpgrade(namespace, name string) (*UpgradeInfo, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	// Get current release
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get release: %w", err)
	}

	currentVersion := rel.Chart.Metadata.Version
	chartName := rel.Chart.Metadata.Name

	info := &UpgradeInfo{
		CurrentVersion: currentVersion,
	}

	// Load repository file
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		if os.IsNotExist(err) {
			info.Error = "no helm repositories configured"
			return info, nil
		}
		info.Error = fmt.Sprintf("failed to load repo file: %v", err)
		return info, nil
	}

	if len(f.Repositories) == 0 {
		info.Error = "no helm repositories configured"
		return info, nil
	}

	// Search through all repo indexes
	var latestVersion string
	var repoName string
	cacheDir := c.settings.RepositoryCache

	for _, r := range f.Repositories {
		// Load the index file for this repo
		indexPath := filepath.Join(cacheDir, fmt.Sprintf("%s-index.yaml", r.Name))
		indexFile, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			// Skip repos with missing/invalid index
			continue
		}

		// Look for the chart
		if versions, ok := indexFile.Entries[chartName]; ok {
			for _, v := range versions {
				if latestVersion == "" || compareVersions(v.Version, latestVersion) > 0 {
					latestVersion = v.Version
					repoName = r.Name
				}
			}
		}
	}

	if latestVersion == "" {
		info.Error = "chart not found in configured repositories"
		return info, nil
	}

	info.LatestVersion = latestVersion
	info.RepositoryName = repoName
	info.UpdateAvailable = compareVersions(latestVersion, currentVersion) > 0

	return info, nil
}

// compareVersions compares two semver strings
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal
func compareVersions(v1, v2 string) int {
	// Strip 'v' prefix if present
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			// Extract numeric part (ignore prerelease suffixes)
			numStr := strings.Split(parts1[i], "-")[0]
			fmt.Sscanf(numStr, "%d", &n1)
		}
		if i < len(parts2) {
			numStr := strings.Split(parts2[i], "-")[0]
			fmt.Sscanf(numStr, "%d", &n2)
		}

		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}

	return 0
}

// Rollback rolls back a release to a previous revision
func (c *Client) Rollback(namespace, name string, revision int) error {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}

	rollbackAction := action.NewRollback(actionConfig)
	rollbackAction.Version = revision
	rollbackAction.Wait = true
	rollbackAction.Timeout = 300 * time.Second

	if err := rollbackAction.Run(name); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	return nil
}

// Uninstall removes a release
func (c *Client) Uninstall(namespace, name string) error {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}

	uninstallAction := action.NewUninstall(actionConfig)
	uninstallAction.Wait = true
	uninstallAction.Timeout = 300 * time.Second

	_, err = uninstallAction.Run(name)
	if err != nil {
		return fmt.Errorf("uninstall failed: %w", err)
	}

	return nil
}

// Upgrade upgrades a release to a new version
func (c *Client) Upgrade(namespace, name, targetVersion string) error {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}

	// First, get the current release to find chart info
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return fmt.Errorf("failed to get current release: %w", err)
	}

	chartName := rel.Chart.Metadata.Name

	// Find the chart in local repos
	repoFile := c.settings.RepositoryConfig
	repoCache := c.settings.RepositoryCache

	// Load repo file
	repos, err := repo.LoadFile(repoFile)
	if err != nil {
		return fmt.Errorf("failed to load repo file: %w", err)
	}

	// Find the chart in repos
	var chartPath string
	for _, r := range repos.Repositories {
		indexPath := filepath.Join(repoCache, r.Name+"-index.yaml")
		idx, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			continue
		}

		if entries, ok := idx.Entries[chartName]; ok {
			for _, entry := range entries {
				if entry.Version == targetVersion {
					// Found the chart - we need to download it
					if len(entry.URLs) > 0 {
						// Use helm's chart downloader
						chartPath = entry.URLs[0]
						// If relative URL, prepend repo URL
						if !strings.HasPrefix(chartPath, "http://") && !strings.HasPrefix(chartPath, "https://") {
							chartPath = strings.TrimSuffix(r.URL, "/") + "/" + chartPath
						}
						break
					}
				}
			}
		}
		if chartPath != "" {
			break
		}
	}

	if chartPath == "" {
		return fmt.Errorf("chart %s version %s not found in configured repositories", chartName, targetVersion)
	}

	// Create upgrade action
	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = namespace
	upgradeAction.Wait = true
	upgradeAction.Timeout = 300 * time.Second
	upgradeAction.ReuseValues = true // Keep existing values

	// Download and load the chart
	// Use ChartPathOptions to locate/download the chart
	client := action.NewInstall(actionConfig)
	client.Version = targetVersion

	// Get chart path (will download if needed)
	cp, err := client.ChartPathOptions.LocateChart(chartPath, c.settings)
	if err != nil {
		return fmt.Errorf("failed to locate chart: %w", err)
	}

	// Load the chart from the path
	chart, err := loader.Load(cp)
	if err != nil {
		return fmt.Errorf("failed to load chart: %w", err)
	}

	// Run the upgrade
	_, err = upgradeAction.Run(name, chart, rel.Config)
	if err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	return nil
}

// BatchCheckUpgrades checks for upgrades for all releases at once (more efficient)
func (c *Client) BatchCheckUpgrades(namespace string) (*BatchUpgradeInfo, error) {
	// Get all releases
	releases, err := c.ListReleases(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list releases: %w", err)
	}

	result := &BatchUpgradeInfo{
		Releases: make(map[string]*UpgradeInfo),
	}

	if len(releases) == 0 {
		return result, nil
	}

	// Load repo indexes once
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		// No repos configured - return empty results with error
		for _, rel := range releases {
			key := rel.Namespace + "/" + rel.Name
			result.Releases[key] = &UpgradeInfo{
				CurrentVersion: rel.ChartVersion,
				Error:          "no helm repositories configured",
			}
		}
		return result, nil
	}

	// Build a map of chart name -> latest version info from all repos
	chartLatestVersions := make(map[string]struct {
		version  string
		repoName string
	})

	cacheDir := c.settings.RepositoryCache
	for _, r := range f.Repositories {
		indexPath := filepath.Join(cacheDir, fmt.Sprintf("%s-index.yaml", r.Name))
		indexFile, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			continue
		}

		for chartName, versions := range indexFile.Entries {
			if len(versions) == 0 {
				continue
			}
			// versions[0] is typically the latest
			latestInRepo := versions[0].Version
			for _, v := range versions {
				if compareVersions(v.Version, latestInRepo) > 0 {
					latestInRepo = v.Version
				}
			}

			existing, exists := chartLatestVersions[chartName]
			if !exists || compareVersions(latestInRepo, existing.version) > 0 {
				chartLatestVersions[chartName] = struct {
					version  string
					repoName string
				}{latestInRepo, r.Name}
			}
		}
	}

	// Check each release against the chart versions map
	for _, rel := range releases {
		key := rel.Namespace + "/" + rel.Name
		info := &UpgradeInfo{
			CurrentVersion: rel.ChartVersion,
		}

		if latest, ok := chartLatestVersions[rel.Chart]; ok {
			info.LatestVersion = latest.version
			info.RepositoryName = latest.repoName
			info.UpdateAvailable = compareVersions(latest.version, rel.ChartVersion) > 0
		} else {
			info.Error = "chart not found in configured repositories"
		}

		result.Releases[key] = info
	}

	return result, nil
}
