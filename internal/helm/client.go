package helm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

// HTTP client for ArtifactHub requests
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// Client provides access to Helm releases
type Client struct {
	mu         sync.RWMutex
	settings   *cli.EnvSettings
	kubeconfig string
}

var (
	globalClient *Client
	clientOnce   sync.Once
	helmClientMu sync.Mutex
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
	// NOTE: Use false for usePersistentConfig to avoid caching issues during context switches
	configFlags := genericclioptions.NewConfigFlags(false)
	if c.kubeconfig != "" {
		configFlags.KubeConfig = &c.kubeconfig
	}
	if namespace != "" {
		configFlags.Namespace = &namespace
	}

	// Use Explorer's current context (in-memory) instead of kubeconfig's current-context
	// This ensures Helm uses the same context as the rest of Explorer after context switches
	currentContext := k8s.GetContextName()
	if currentContext != "" && currentContext != "in-cluster" {
		configFlags.Context = &currentContext
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
	hr := HelmRelease{
		Name:         rel.Name,
		Namespace:    rel.Namespace,
		Chart:        rel.Chart.Metadata.Name,
		ChartVersion: rel.Chart.Metadata.Version,
		AppVersion:   rel.Chart.Metadata.AppVersion,
		Status:       rel.Info.Status.String(),
		Revision:     rel.Version,
		Updated:      rel.Info.LastDeployed.Time,
	}

	// Compute health from owned resources
	resources := parseManifestResources(rel.Manifest, rel.Namespace)
	enrichResourcesWithStatus(resources)
	health, issue, summary := computeResourceHealth(resources)
	hr.ResourceHealth = health
	hr.HealthIssue = issue
	hr.HealthSummary = summary

	return hr
}

// computeResourceHealth analyzes owned resources and returns overall health status
func computeResourceHealth(resources []OwnedResource) (health, issue, summary string) {
	if len(resources) == 0 {
		return "unknown", "", ""
	}

	var unhealthyCount, degradedCount, healthyCount, unknownCount int
	var primaryIssue string
	var issueSeverity int // 0=none, 1=degraded, 2=unhealthy

	// Track workload stats for summary
	var totalPods, readyPods int
	var workloadIssues []string

	for _, r := range resources {
		// Skip non-workload resources for health calculation
		switch r.Kind {
		case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet":
			// Parse ready string like "2/3"
			if r.Ready != "" {
				var ready, total int
				if _, err := fmt.Sscanf(r.Ready, "%d/%d", &ready, &total); err == nil {
					totalPods += total
					readyPods += ready
				}
			}

			// Check for issues
			if r.Issue != "" {
				if primaryIssue == "" || issueSeverity < 2 {
					primaryIssue = r.Issue
					issueSeverity = 2
				}
				workloadIssues = append(workloadIssues, fmt.Sprintf("%s: %s", r.Name, r.Issue))
				unhealthyCount++
			} else if r.Status == "Running" || r.Status == "Active" {
				healthyCount++
			} else if r.Status == "Progressing" {
				degradedCount++
			} else if r.Status != "" {
				unknownCount++
			}

		case "Pod":
			totalPods++
			if r.Issue != "" {
				if primaryIssue == "" || issueSeverity < 2 {
					primaryIssue = r.Issue
					issueSeverity = 2
				}
				unhealthyCount++
			} else if r.Status == "Running" {
				readyPods++
				healthyCount++
			} else if r.Status == "Pending" || r.Status == "ContainerCreating" {
				degradedCount++
			} else if r.Status == "Failed" || r.Status == "Error" {
				unhealthyCount++
			}
		}
	}

	// Determine overall health
	if unhealthyCount > 0 {
		health = "unhealthy"
	} else if degradedCount > 0 {
		health = "degraded"
	} else if healthyCount > 0 {
		health = "healthy"
	} else {
		health = "unknown"
	}

	issue = primaryIssue

	// Build summary
	if totalPods > 0 {
		if primaryIssue != "" {
			summary = fmt.Sprintf("%d/%d %s", readyPods, totalPods, primaryIssue)
		} else if readyPods < totalPods {
			summary = fmt.Sprintf("%d/%d ready", readyPods, totalPods)
		} else {
			summary = fmt.Sprintf("%d/%d ready", readyPods, totalPods)
		}
	}

	return health, issue, summary
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
			resources[i].Summary = status.Summary
			resources[i].Issue = status.Issue
		}
	}
}

// computeDiff generates a unified diff between two manifests using LCS algorithm
func computeDiff(manifest1, manifest2 string, rev1, rev2 int) string {
	var result bytes.Buffer
	result.WriteString(fmt.Sprintf("--- Revision %d\n", rev1))
	result.WriteString(fmt.Sprintf("+++ Revision %d\n", rev2))

	lines1 := strings.Split(manifest1, "\n")
	lines2 := strings.Split(manifest2, "\n")

	result.WriteString(computeUnifiedDiff(lines1, lines2))

	return result.String()
}

// computeUnifiedDiff creates a unified diff from two sets of lines
func computeUnifiedDiff(lines1, lines2 []string) string {
	var result bytes.Buffer

	// Use LCS-based diff algorithm
	lcs := computeLCS(lines1, lines2)

	i, j := 0, 0
	lcsIdx := 0

	// Track hunks for unified diff format
	var hunkLines []string
	hunkStart1, hunkStart2 := 1, 1
	hunkLen1, hunkLen2 := 0, 0
	contextLines := 3
	pendingContext := []string{}

	flushHunk := func() {
		if len(hunkLines) > 0 {
			result.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n",
				hunkStart1, hunkLen1, hunkStart2, hunkLen2))
			for _, line := range hunkLines {
				result.WriteString(line)
				result.WriteString("\n")
			}
			hunkLines = nil
			hunkLen1, hunkLen2 = 0, 0
		}
	}

	for i < len(lines1) || j < len(lines2) {
		if lcsIdx < len(lcs) && i < len(lines1) && j < len(lines2) &&
			lines1[i] == lcs[lcsIdx] && lines2[j] == lcs[lcsIdx] {
			// Common line
			if len(hunkLines) > 0 {
				// Add context to current hunk
				hunkLines = append(hunkLines, " "+lines1[i])
				hunkLen1++
				hunkLen2++
				pendingContext = append(pendingContext, " "+lines1[i])
				if len(pendingContext) > contextLines {
					// Too much context, might need to end hunk
					flushHunk()
					pendingContext = nil
					hunkStart1 = i + 2
					hunkStart2 = j + 2
				}
			}
			i++
			j++
			lcsIdx++
		} else if i < len(lines1) && (lcsIdx >= len(lcs) || lines1[i] != lcs[lcsIdx]) {
			// Line removed
			if len(hunkLines) == 0 {
				// Start new hunk with context
				hunkStart1 = max(1, i-contextLines+1)
				hunkStart2 = max(1, j-contextLines+1)
				// Add leading context
				for k := max(0, i-contextLines); k < i; k++ {
					if k < len(lines1) {
						hunkLines = append(hunkLines, " "+lines1[k])
						hunkLen1++
						hunkLen2++
					}
				}
			}
			pendingContext = nil
			hunkLines = append(hunkLines, "-"+lines1[i])
			hunkLen1++
			i++
		} else if j < len(lines2) {
			// Line added
			if len(hunkLines) == 0 {
				hunkStart1 = max(1, i-contextLines+1)
				hunkStart2 = max(1, j-contextLines+1)
				// Add leading context
				for k := max(0, i-contextLines); k < i; k++ {
					if k < len(lines1) {
						hunkLines = append(hunkLines, " "+lines1[k])
						hunkLen1++
						hunkLen2++
					}
				}
			}
			pendingContext = nil
			hunkLines = append(hunkLines, "+"+lines2[j])
			hunkLen2++
			j++
		}
	}

	flushHunk()
	return result.String()
}

// computeLCS computes the Longest Common Subsequence of two string slices
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	// Build LCS table
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}

	// Backtrack to find LCS
	lcs := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			lcs = append([]string{a[i-1]}, lcs...)
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
			i--
		} else {
			j--
		}
	}

	return lcs
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

// PreviewValuesChange previews the effect of new values on a release via dry-run
func (c *Client) PreviewValuesChange(namespace, name string, newValues map[string]any) (*ValuesPreviewResponse, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}

	// Get the current release
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get current release: %w", err)
	}

	// Get current user-supplied values
	getValuesAction := action.NewGetValues(actionConfig)
	currentValues, err := getValuesAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get current values: %w", err)
	}

	// Get current manifest
	currentManifest := rel.Manifest

	// Perform a dry-run upgrade with the new values
	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = namespace
	upgradeAction.DryRun = true
	upgradeAction.DryRunOption = "client"
	upgradeAction.ResetValues = true // Use only the provided values, don't merge

	// Run the dry-run upgrade
	newRel, err := upgradeAction.Run(name, rel.Chart, newValues)
	if err != nil {
		return nil, fmt.Errorf("failed to preview values change: %w", err)
	}

	// Compute the manifest diff
	diff := computeDiff(currentManifest, newRel.Manifest, rel.Version, rel.Version)

	return &ValuesPreviewResponse{
		CurrentValues: currentValues,
		NewValues:     newValues,
		ManifestDiff:  diff,
	}, nil
}

// ApplyValues upgrades a release with new values (same chart version)
func (c *Client) ApplyValues(namespace, name string, newValues map[string]any) error {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}

	// Get the current release to reuse its chart
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return fmt.Errorf("failed to get current release: %w", err)
	}

	// Create upgrade action
	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = namespace
	upgradeAction.Wait = true
	upgradeAction.Timeout = 300 * time.Second
	upgradeAction.ResetValues = true // Use only the provided values, don't merge

	// Run the upgrade with the existing chart and new values
	_, err = upgradeAction.Run(name, rel.Chart, newValues)
	if err != nil {
		return fmt.Errorf("failed to apply values: %w", err)
	}

	return nil
}

// ============================================================================
// Chart Browser Methods
// ============================================================================

// ListRepositories returns all configured Helm repositories
func (c *Client) ListRepositories() ([]HelmRepository, error) {
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []HelmRepository{}, nil
		}
		return nil, fmt.Errorf("failed to load repo file: %w", err)
	}

	repos := make([]HelmRepository, 0, len(f.Repositories))
	cacheDir := c.settings.RepositoryCache

	for _, r := range f.Repositories {
		hr := HelmRepository{
			Name: r.Name,
			URL:  r.URL,
		}

		// Check index file for last updated time
		indexPath := filepath.Join(cacheDir, r.Name+"-index.yaml")
		if info, err := os.Stat(indexPath); err == nil {
			hr.LastUpdated = info.ModTime()
		}

		repos = append(repos, hr)
	}

	return repos, nil
}

// UpdateRepository updates the index for a specific repository
func (c *Client) UpdateRepository(repoName string) error {
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		return fmt.Errorf("failed to load repo file: %w", err)
	}

	var repoEntry *repo.Entry
	for _, r := range f.Repositories {
		if r.Name == repoName {
			repoEntry = r
			break
		}
	}

	if repoEntry == nil {
		return fmt.Errorf("repository %s not found", repoName)
	}

	// Create chart repository and download index
	chartRepo, err := repo.NewChartRepository(repoEntry, nil)
	if err != nil {
		return fmt.Errorf("failed to create chart repository: %w", err)
	}

	chartRepo.CachePath = c.settings.RepositoryCache

	_, err = chartRepo.DownloadIndexFile()
	if err != nil {
		return fmt.Errorf("failed to download index: %w", err)
	}

	return nil
}

// SearchCharts searches for charts across all repositories
func (c *Client) SearchCharts(query string, allVersions bool) (*ChartSearchResult, error) {
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &ChartSearchResult{Charts: []ChartInfo{}}, nil
		}
		return nil, fmt.Errorf("failed to load repo file: %w", err)
	}

	cacheDir := c.settings.RepositoryCache
	queryLower := strings.ToLower(query)

	var charts []ChartInfo
	seen := make(map[string]bool) // Track seen chart names (for !allVersions)

	for _, r := range f.Repositories {
		indexPath := filepath.Join(cacheDir, r.Name+"-index.yaml")
		indexFile, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			continue
		}

		for chartName, versions := range indexFile.Entries {
			// Filter by query if provided
			if query != "" {
				nameLower := strings.ToLower(chartName)
				if !strings.Contains(nameLower, queryLower) {
					// Also check description
					matches := false
					for _, v := range versions {
						if strings.Contains(strings.ToLower(v.Description), queryLower) {
							matches = true
							break
						}
					}
					if !matches {
						continue
					}
				}
			}

			if allVersions {
				for _, v := range versions {
					charts = append(charts, chartVersionToInfo(v, r.Name))
				}
			} else {
				// Only include latest version
				key := r.Name + "/" + chartName
				if !seen[key] && len(versions) > 0 {
					seen[key] = true
					charts = append(charts, chartVersionToInfo(versions[0], r.Name))
				}
			}
		}
	}

	// Sort by name
	sort.Slice(charts, func(i, j int) bool {
		if charts[i].Repository != charts[j].Repository {
			return charts[i].Repository < charts[j].Repository
		}
		if charts[i].Name != charts[j].Name {
			return charts[i].Name < charts[j].Name
		}
		return compareVersions(charts[i].Version, charts[j].Version) > 0
	})

	return &ChartSearchResult{
		Charts: charts,
		Total:  len(charts),
	}, nil
}

// GetChartDetail returns detailed information about a specific chart version
func (c *Client) GetChartDetail(repoName, chartName, version string) (*ChartDetail, error) {
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load repo file: %w", err)
	}

	// Find the repository
	var repoEntry *repo.Entry
	for _, r := range f.Repositories {
		if r.Name == repoName {
			repoEntry = r
			break
		}
	}

	if repoEntry == nil {
		return nil, fmt.Errorf("repository %s not found", repoName)
	}

	// Load index
	cacheDir := c.settings.RepositoryCache
	indexPath := filepath.Join(cacheDir, repoName+"-index.yaml")
	indexFile, err := repo.LoadIndexFile(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load index file: %w", err)
	}

	// Find the chart version
	versions, ok := indexFile.Entries[chartName]
	if !ok || len(versions) == 0 {
		return nil, fmt.Errorf("chart %s not found in repository %s", chartName, repoName)
	}

	var chartVersion *repo.ChartVersion
	if version == "" || version == "latest" {
		chartVersion = versions[0]
	} else {
		for _, v := range versions {
			if v.Version == version {
				chartVersion = v
				break
			}
		}
	}

	if chartVersion == nil {
		return nil, fmt.Errorf("version %s not found for chart %s", version, chartName)
	}

	// Download and load the chart to get README and values
	chartURL := chartVersion.URLs[0]
	if !strings.HasPrefix(chartURL, "http://") && !strings.HasPrefix(chartURL, "https://") {
		chartURL = strings.TrimSuffix(repoEntry.URL, "/") + "/" + chartURL
	}

	// Use ChartPathOptions to locate/download
	actionConfig, err := c.getActionConfig("")
	if err != nil {
		return nil, err
	}

	client := action.NewInstall(actionConfig)
	client.Version = chartVersion.Version

	cp, err := client.ChartPathOptions.LocateChart(chartURL, c.settings)
	if err != nil {
		// If we can't download, return basic info from index
		return &ChartDetail{
			ChartInfo: chartVersionToInfo(chartVersion, repoName),
		}, nil
	}

	chart, err := loader.Load(cp)
	if err != nil {
		return &ChartDetail{
			ChartInfo: chartVersionToInfo(chartVersion, repoName),
		}, nil
	}

	// Build detail response
	detail := &ChartDetail{
		ChartInfo: chartVersionToInfo(chartVersion, repoName),
	}

	// Extract README
	for _, f := range chart.Files {
		name := strings.ToLower(f.Name)
		if name == "readme.md" || name == "readme.txt" || name == "readme" {
			detail.Readme = string(f.Data)
			break
		}
	}

	// Get default values
	if chart.Values != nil {
		detail.Values = chart.Values
	}

	// Get values schema if present
	if chart.Schema != nil {
		detail.ValuesSchema = string(chart.Schema)
	}

	// Get maintainers
	if chart.Metadata.Maintainers != nil {
		for _, m := range chart.Metadata.Maintainers {
			detail.Maintainers = append(detail.Maintainers, Maintainer{
				Name:  m.Name,
				Email: m.Email,
				URL:   m.URL,
			})
		}
	}

	// Get sources and keywords
	detail.Sources = chart.Metadata.Sources
	detail.Keywords = chart.Metadata.Keywords

	return detail, nil
}

// Install installs a new Helm release
func (c *Client) Install(req *InstallRequest) (*HelmRelease, error) {
	actionConfig, err := c.getActionConfig(req.Namespace)
	if err != nil {
		return nil, err
	}

	var chartURL string

	// Check if the repository is a URL (for ArtifactHub installs) or a local repo name
	isRepoURL := strings.HasPrefix(req.Repository, "http://") || strings.HasPrefix(req.Repository, "https://")

	if isRepoURL {
		// Direct URL - fetch the repository index to find the chart
		repoURL := strings.TrimSuffix(req.Repository, "/")

		// Try to fetch the index.yaml from the repo to find the chart URL
		indexURL := repoURL + "/index.yaml"
		resp, err := httpClient.Get(indexURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch repository index: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("repository %s returned status %d", req.Repository, resp.StatusCode)
		}

		// Save to temp file and load (repo package doesn't have LoadIndexFromBytes)
		tmpFile, err := os.CreateTemp("", "helm-index-*.yaml")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		indexBytes := new(bytes.Buffer)
		indexBytes.ReadFrom(resp.Body)
		if _, err := tmpFile.Write(indexBytes.Bytes()); err != nil {
			return nil, fmt.Errorf("failed to write temp index: %w", err)
		}
		tmpFile.Close()

		indexFile, err := repo.LoadIndexFile(tmpFile.Name())
		if err != nil {
			return nil, fmt.Errorf("failed to parse repository index: %w", err)
		}

		// Find the chart version
		versions, ok := indexFile.Entries[req.ChartName]
		if !ok || len(versions) == 0 {
			return nil, fmt.Errorf("chart %s not found in repository", req.ChartName)
		}

		var chartVersion *repo.ChartVersion
		if req.Version == "" || req.Version == "latest" {
			chartVersion = versions[0]
		} else {
			for _, v := range versions {
				if v.Version == req.Version {
					chartVersion = v
					break
				}
			}
		}

		if chartVersion == nil {
			return nil, fmt.Errorf("version %s not found for chart %s", req.Version, req.ChartName)
		}

		// Build chart URL
		chartURL = chartVersion.URLs[0]
		if !strings.HasPrefix(chartURL, "http://") && !strings.HasPrefix(chartURL, "https://") {
			chartURL = repoURL + "/" + chartURL
		}
	} else {
		// Local repository name - use existing logic
		repoFile := c.settings.RepositoryConfig
		f, err := repo.LoadFile(repoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load repo file: %w", err)
		}

		// Find repository
		var repoEntry *repo.Entry
		for _, r := range f.Repositories {
			if r.Name == req.Repository {
				repoEntry = r
				break
			}
		}

		if repoEntry == nil {
			return nil, fmt.Errorf("repository %s not found", req.Repository)
		}

		// Load index and find chart
		cacheDir := c.settings.RepositoryCache
		indexPath := filepath.Join(cacheDir, req.Repository+"-index.yaml")
		indexFile, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load index file: %w", err)
		}

		versions, ok := indexFile.Entries[req.ChartName]
		if !ok || len(versions) == 0 {
			return nil, fmt.Errorf("chart %s not found", req.ChartName)
		}

		var chartVersion *repo.ChartVersion
		if req.Version == "" || req.Version == "latest" {
			chartVersion = versions[0]
		} else {
			for _, v := range versions {
				if v.Version == req.Version {
					chartVersion = v
					break
				}
			}
		}

		if chartVersion == nil {
			return nil, fmt.Errorf("version %s not found for chart %s", req.Version, req.ChartName)
		}

		// Build chart URL
		chartURL = chartVersion.URLs[0]
		if !strings.HasPrefix(chartURL, "http://") && !strings.HasPrefix(chartURL, "https://") {
			chartURL = strings.TrimSuffix(repoEntry.URL, "/") + "/" + chartURL
		}
	}

	// Create install action
	installAction := action.NewInstall(actionConfig)
	installAction.ReleaseName = req.ReleaseName
	installAction.Namespace = req.Namespace
	installAction.CreateNamespace = req.CreateNamespace
	installAction.Wait = true
	installAction.Timeout = 300 * time.Second
	installAction.Version = req.Version

	// Locate/download chart
	cp, err := installAction.ChartPathOptions.LocateChart(chartURL, c.settings)
	if err != nil {
		return nil, fmt.Errorf("failed to locate chart: %w", err)
	}

	// Load chart
	chart, err := loader.Load(cp)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart: %w", err)
	}

	// Run install
	rel, err := installAction.Run(chart, req.Values)
	if err != nil {
		return nil, fmt.Errorf("install failed: %w", err)
	}

	return &HelmRelease{
		Name:         rel.Name,
		Namespace:    rel.Namespace,
		Chart:        rel.Chart.Metadata.Name,
		ChartVersion: rel.Chart.Metadata.Version,
		AppVersion:   rel.Chart.Metadata.AppVersion,
		Status:       rel.Info.Status.String(),
		Revision:     rel.Version,
		Updated:      rel.Info.LastDeployed.Time,
	}, nil
}

// InstallWithProgress installs a new Helm release and streams progress updates
func (c *Client) InstallWithProgress(req *InstallRequest, progressCh chan<- InstallProgress) (*HelmRelease, error) {
	sendProgress := func(phase, message, detail string) {
		select {
		case progressCh <- InstallProgress{Phase: phase, Message: message, Detail: detail}:
		default:
			// Channel full or closed, skip
		}
	}

	actionConfig, err := c.getActionConfig(req.Namespace)
	if err != nil {
		return nil, err
	}

	var chartURL string

	// Check if the repository is a URL (for ArtifactHub installs) or a local repo name
	isRepoURL := strings.HasPrefix(req.Repository, "http://") || strings.HasPrefix(req.Repository, "https://")

	if isRepoURL {
		sendProgress("fetching", "Fetching repository index...", req.Repository)

		repoURL := strings.TrimSuffix(req.Repository, "/")
		indexURL := repoURL + "/index.yaml"
		resp, err := httpClient.Get(indexURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch repository index: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("repository %s returned status %d", req.Repository, resp.StatusCode)
		}

		sendProgress("parsing", "Parsing repository index...", "")

		tmpFile, err := os.CreateTemp("", "helm-index-*.yaml")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		defer os.Remove(tmpFile.Name())
		defer tmpFile.Close()

		indexBytes := new(bytes.Buffer)
		indexBytes.ReadFrom(resp.Body)
		if _, err := tmpFile.Write(indexBytes.Bytes()); err != nil {
			return nil, fmt.Errorf("failed to write temp index: %w", err)
		}
		tmpFile.Close()

		indexFile, err := repo.LoadIndexFile(tmpFile.Name())
		if err != nil {
			return nil, fmt.Errorf("failed to parse repository index: %w", err)
		}

		versions, ok := indexFile.Entries[req.ChartName]
		if !ok || len(versions) == 0 {
			return nil, fmt.Errorf("chart %s not found in repository", req.ChartName)
		}

		var chartVersion *repo.ChartVersion
		if req.Version == "" || req.Version == "latest" {
			chartVersion = versions[0]
		} else {
			for _, v := range versions {
				if v.Version == req.Version {
					chartVersion = v
					break
				}
			}
		}

		if chartVersion == nil {
			return nil, fmt.Errorf("version %s not found for chart %s", req.Version, req.ChartName)
		}

		chartURL = chartVersion.URLs[0]
		if !strings.HasPrefix(chartURL, "http://") && !strings.HasPrefix(chartURL, "https://") {
			chartURL = repoURL + "/" + chartURL
		}
	} else {
		sendProgress("resolving", "Resolving chart from local repository...", req.Repository)

		repoFile := c.settings.RepositoryConfig
		f, err := repo.LoadFile(repoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load repo file: %w", err)
		}

		var repoEntry *repo.Entry
		for _, r := range f.Repositories {
			if r.Name == req.Repository {
				repoEntry = r
				break
			}
		}

		if repoEntry == nil {
			return nil, fmt.Errorf("repository %s not found", req.Repository)
		}

		cacheDir := c.settings.RepositoryCache
		indexPath := filepath.Join(cacheDir, req.Repository+"-index.yaml")
		indexFile, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load index file: %w", err)
		}

		versions, ok := indexFile.Entries[req.ChartName]
		if !ok || len(versions) == 0 {
			return nil, fmt.Errorf("chart %s not found", req.ChartName)
		}

		var chartVersion *repo.ChartVersion
		if req.Version == "" || req.Version == "latest" {
			chartVersion = versions[0]
		} else {
			for _, v := range versions {
				if v.Version == req.Version {
					chartVersion = v
					break
				}
			}
		}

		if chartVersion == nil {
			return nil, fmt.Errorf("version %s not found for chart %s", req.Version, req.ChartName)
		}

		chartURL = chartVersion.URLs[0]
		if !strings.HasPrefix(chartURL, "http://") && !strings.HasPrefix(chartURL, "https://") {
			chartURL = strings.TrimSuffix(repoEntry.URL, "/") + "/" + chartURL
		}
	}

	sendProgress("downloading", fmt.Sprintf("Downloading chart %s-%s...", req.ChartName, req.Version), chartURL)

	installAction := action.NewInstall(actionConfig)
	installAction.ReleaseName = req.ReleaseName
	installAction.Namespace = req.Namespace
	installAction.CreateNamespace = req.CreateNamespace
	installAction.Wait = true
	installAction.Timeout = 300 * time.Second
	installAction.Version = req.Version

	cp, err := installAction.ChartPathOptions.LocateChart(chartURL, c.settings)
	if err != nil {
		return nil, fmt.Errorf("failed to locate chart: %w", err)
	}

	sendProgress("loading", "Loading chart...", cp)

	chart, err := loader.Load(cp)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart: %w", err)
	}

	sendProgress("installing", fmt.Sprintf("Installing %s to namespace %s...", req.ReleaseName, req.Namespace), "")

	if req.CreateNamespace {
		sendProgress("installing", fmt.Sprintf("Creating namespace %s if needed...", req.Namespace), "")
	}

	rel, err := installAction.Run(chart, req.Values)
	if err != nil {
		return nil, fmt.Errorf("install failed: %w", err)
	}

	sendProgress("complete", fmt.Sprintf("Successfully installed %s", req.ReleaseName), "")

	return &HelmRelease{
		Name:         rel.Name,
		Namespace:    rel.Namespace,
		Chart:        rel.Chart.Metadata.Name,
		ChartVersion: rel.Chart.Metadata.Version,
		AppVersion:   rel.Chart.Metadata.AppVersion,
		Status:       rel.Info.Status.String(),
		Revision:     rel.Version,
		Updated:      rel.Info.LastDeployed.Time,
	}, nil
}

// Helper function to convert chart version to ChartInfo
func chartVersionToInfo(v *repo.ChartVersion, repoName string) ChartInfo {
	return ChartInfo{
		Name:        v.Name,
		Version:     v.Version,
		AppVersion:  v.AppVersion,
		Description: v.Description,
		Icon:        v.Icon,
		Repository:  repoName,
		Home:        v.Home,
		Deprecated:  v.Deprecated,
	}
}

// ============================================================================
// ArtifactHub Integration
// ============================================================================

const artifactHubBaseURL = "https://artifacthub.io/api/v1"

// SearchArtifactHub searches for charts on ArtifactHub
// sort can be: "relevance" (default), "stars", or "last_updated"
func SearchArtifactHub(query string, offset, limit int, official, verified bool, sort string) (*ArtifactHubSearchResult, error) {
	// Build query URL
	url := fmt.Sprintf("%s/packages/search?kind=0&ts_query_web=%s&offset=%d&limit=%d",
		artifactHubBaseURL, query, offset, limit)

	// Add sort parameter (ArtifactHub uses "sort" query param)
	if sort != "" && sort != "relevance" {
		url += "&sort=" + sort
	}

	// Add filters
	if official {
		url += "&official=true"
	}
	if verified {
		url += "&verified_publisher=true"
	}

	// Make HTTP request
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to search ArtifactHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ArtifactHub returned status %d", resp.StatusCode)
	}

	// Parse response
	var apiResp artifactHubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse ArtifactHub response: %w", err)
	}

	// Convert to our types
	result := &ArtifactHubSearchResult{
		Charts: make([]ArtifactHubChart, 0, len(apiResp.Packages)),
		Total:  len(apiResp.Packages),
	}

	for _, pkg := range apiResp.Packages {
		chart := convertArtifactHubPackage(pkg)
		result.Charts = append(result.Charts, chart)
	}

	return result, nil
}

// GetArtifactHubChart gets detailed chart info from ArtifactHub
func GetArtifactHubChart(repoName, chartName, version string) (*ArtifactHubChartDetail, error) {
	url := fmt.Sprintf("%s/packages/helm/%s/%s", artifactHubBaseURL, repoName, chartName)
	if version != "" && version != "latest" {
		url += "/" + version
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get chart from ArtifactHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("chart %s/%s not found on ArtifactHub", repoName, chartName)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ArtifactHub returned status %d", resp.StatusCode)
	}

	var apiResp artifactHubPackageDetail
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse ArtifactHub response: %w", err)
	}

	detail := convertArtifactHubDetail(apiResp)

	// If values not included in main response, fetch separately using package ID
	if detail.Values == "" && detail.PackageID != "" {
		chartVersion := version
		if chartVersion == "" || chartVersion == "latest" {
			chartVersion = detail.Version
		}
		if values, err := GetArtifactHubValuesByPackageID(detail.PackageID, chartVersion); err == nil && values != "" {
			detail.Values = values
		}
	}

	return detail, nil
}

// GetArtifactHubReadme gets the README for a chart
func GetArtifactHubReadme(repoName, chartName, version string) (string, error) {
	url := fmt.Sprintf("%s/packages/helm/%s/%s/%s/readme", artifactHubBaseURL, repoName, chartName, version)

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to get README: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil // README not available
	}

	body := new(bytes.Buffer)
	body.ReadFrom(resp.Body)
	return body.String(), nil
}

// GetArtifactHubValuesByPackageID gets the default values for a chart using its package ID
func GetArtifactHubValuesByPackageID(packageID, version string) (string, error) {
	// ArtifactHub uses package ID in the values URL: /api/v1/packages/{packageId}/{version}/values
	url := fmt.Sprintf("%s/packages/%s/%s/values", artifactHubBaseURL, packageID, version)

	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to get values: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", nil // Values not available
	}

	// Check content type - should be text/plain or application/x-yaml, not text/html
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		return "", nil // Got HTML instead of YAML, values not available
	}

	body := new(bytes.Buffer)
	body.ReadFrom(resp.Body)
	content := body.String()

	// Double-check: if content looks like HTML, reject it
	if strings.HasPrefix(strings.TrimSpace(content), "<!DOCTYPE") || strings.HasPrefix(strings.TrimSpace(content), "<html") {
		return "", nil
	}

	return content, nil
}

// Internal types for ArtifactHub API responses

type artifactHubSearchResponse struct {
	Packages []artifactHubPackage `json:"packages"`
}

type artifactHubPackage struct {
	PackageID             string                     `json:"package_id"`
	Name                  string                     `json:"name"`
	NormalizedName        string                     `json:"normalized_name"`
	LogoImageID           string                     `json:"logo_image_id,omitempty"`
	Stars                 int                        `json:"stars"`
	Description           string                     `json:"description,omitempty"`
	Version               string                     `json:"version"`
	AppVersion            string                     `json:"app_version,omitempty"`
	Deprecated            bool                       `json:"deprecated"`
	Signed                bool                       `json:"signed"`
	HasValuesSchema       bool                       `json:"has_values_schema"`
	SecurityReportSummary *artifactHubSecurityReport `json:"security_report_summary,omitempty"`
	ProductionOrgsCount   int                        `json:"production_organizations_count"`
	TS                    int64                      `json:"ts"` // Unix timestamp
	Repository            artifactHubRepo            `json:"repository"`
	License               string                     `json:"license,omitempty"`
}

type artifactHubRepo struct {
	Name              string `json:"name"`
	URL               string `json:"url"`
	Official          bool   `json:"official"`
	VerifiedPublisher bool   `json:"verified_publisher"`
	OrganizationName  string `json:"organization_name,omitempty"`
	DisplayName       string `json:"organization_display_name,omitempty"`
}

type artifactHubSecurityReport struct {
	Critical int `json:"critical,omitempty"`
	High     int `json:"high,omitempty"`
	Medium   int `json:"medium,omitempty"`
	Low      int `json:"low,omitempty"`
	Unknown  int `json:"unknown,omitempty"`
}

type artifactHubPackageDetail struct {
	artifactHubPackage
	Readme            string                  `json:"readme,omitempty"`
	DefaultValues     string                  `json:"default_values,omitempty"`
	ValuesSchema      map[string]any          `json:"values_schema,omitempty"`
	HomeURL           string                  `json:"home_url,omitempty"`
	Maintainers       []artifactHubMaintainer `json:"maintainers,omitempty"`
	Links             []artifactHubLink       `json:"links,omitempty"`
	AvailableVersions []artifactHubVersion    `json:"available_versions,omitempty"`
	Install           string                  `json:"install,omitempty"`
	Keywords          []string                `json:"keywords,omitempty"`
}

type artifactHubMaintainer struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type artifactHubLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type artifactHubVersion struct {
	Version string `json:"version"`
	TS      int64  `json:"ts"`
}

// Converters

func convertArtifactHubPackage(pkg artifactHubPackage) ArtifactHubChart {
	chart := ArtifactHubChart{
		PackageID:   pkg.PackageID,
		Name:        pkg.Name,
		Version:     pkg.Version,
		AppVersion:  pkg.AppVersion,
		Description: pkg.Description,
		Deprecated:  pkg.Deprecated,
		Stars:       pkg.Stars,
		License:     pkg.License,
		UpdatedAt:   pkg.TS,
		Signed:      pkg.Signed,
		HasSchema:   pkg.HasValuesSchema,
		OrgCount:    pkg.ProductionOrgsCount,
		Repository: ArtifactHubRepository{
			Name:              pkg.Repository.Name,
			URL:               pkg.Repository.URL,
			Official:          pkg.Repository.Official,
			VerifiedPublisher: pkg.Repository.VerifiedPublisher,
			OrganizationName:  pkg.Repository.OrganizationName,
		},
	}

	// Build logo URL if available
	if pkg.LogoImageID != "" {
		chart.LogoURL = fmt.Sprintf("https://artifacthub.io/image/%s", pkg.LogoImageID)
	}

	// Convert security info
	if pkg.SecurityReportSummary != nil {
		chart.Security = &ArtifactHubSecurity{
			Critical: pkg.SecurityReportSummary.Critical,
			High:     pkg.SecurityReportSummary.High,
			Medium:   pkg.SecurityReportSummary.Medium,
			Low:      pkg.SecurityReportSummary.Low,
			Unknown:  pkg.SecurityReportSummary.Unknown,
		}
	}

	return chart
}

func convertArtifactHubDetail(pkg artifactHubPackageDetail) *ArtifactHubChartDetail {
	detail := &ArtifactHubChartDetail{
		ArtifactHubChart: convertArtifactHubPackage(pkg.artifactHubPackage),
		Readme:           pkg.Readme,
		Values:           pkg.DefaultValues,
		Install:          pkg.Install,
	}

	detail.HomeURL = pkg.HomeURL
	detail.Keywords = pkg.Keywords

	// Convert values schema to string if present
	if pkg.ValuesSchema != nil {
		if schemaBytes, err := json.Marshal(pkg.ValuesSchema); err == nil {
			detail.ValuesSchema = string(schemaBytes)
		}
	}

	// Convert maintainers
	for _, m := range pkg.Maintainers {
		detail.Maintainers = append(detail.Maintainers, ArtifactHubMaintainer{
			Name:  m.Name,
			Email: m.Email,
		})
	}

	// Convert links
	for _, l := range pkg.Links {
		detail.Links = append(detail.Links, ArtifactHubLink{
			Name: l.Name,
			URL:  l.URL,
		})
	}

	// Convert available versions
	for _, v := range pkg.AvailableVersions {
		detail.Versions = append(detail.Versions, ArtifactHubVersionSummary{
			Version:   v.Version,
			CreatedAt: v.TS,
		})
	}

	return detail
}
