package helm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/helmhistory"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/repo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	releaseHistoryMax        = 256
	releaseListMaxOperations = 3
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

// ensureHelmWritablePaths sets HELM_CACHE_HOME, HELM_CONFIG_HOME, and HELM_DATA_HOME
// to writable /tmp paths when the default home directory is not writable (e.g.
// readOnlyRootFilesystem containers). On local machines this is a no-op since the
// home directory is writable and the Helm SDK uses its normal XDG-based defaults.
// Must be called BEFORE cli.New(), which reads these env vars at init time.
func ensureHelmWritablePaths() {
	// If all env vars are already set explicitly, nothing to do
	if os.Getenv("HELM_CACHE_HOME") != "" && os.Getenv("HELM_CONFIG_HOME") != "" && os.Getenv("HELM_DATA_HOME") != "" {
		return
	}

	// Check if the home directory is writable by attempting to create a temp file
	homeDir, err := os.UserHomeDir()
	if err != nil || !isDirWritable(homeDir) {
		defaults := map[string]string{
			"HELM_CACHE_HOME":  "/tmp/helm/cache",
			"HELM_CONFIG_HOME": "/tmp/helm/config",
			"HELM_DATA_HOME":   "/tmp/helm/data",
		}
		for key, val := range defaults {
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
		log.Printf("[helm] Home directory not writable, using /tmp/helm for Helm SDK paths")
	}
}

// isDirWritable checks if a directory is writable by creating and removing a temp file.
func isDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".helm-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// Initialize sets up the global Helm client
func Initialize(kubeconfig string) error {
	var initErr error
	clientOnce.Do(func() {
		ensureHelmWritablePaths()
		settings := cli.New()
		if kubeconfig != "" {
			settings.KubeConfig = kubeconfig
		}
		globalClient = &Client{
			settings:   settings,
			kubeconfig: kubeconfig,
		}
		log.Printf("Helm client initialized (cache=%s, config=%s, data=%s)",
			settings.RepositoryCache, settings.RepositoryConfig, settings.PluginsDirectory)
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
	return c.buildActionConfig(namespace, "", nil)
}

// getActionConfigForUser creates an action configuration with K8s impersonation set.
// Used for write operations when auth is enabled.
func (c *Client) getActionConfigForUser(namespace, username string, groups []string) (*action.Configuration, error) {
	return c.buildActionConfig(namespace, username, groups)
}

// buildActionConfig is the shared init path for both anonymous and
// impersonated action configurations. When kubeconfig is empty (running
// in-cluster) we hand Helm an in-cluster RESTClientGetter built from the
// rest.Config the rest of Radar already uses — Helm's default
// ConfigFlags only resolves kubeconfig and would otherwise fall through
// to localhost:8080 inside a pod with no ~/.kube/config.
func (c *Client) buildActionConfig(namespace, username string, groups []string) (*action.Configuration, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	actionConfig := new(action.Configuration)

	getter, err := c.restClientGetter(namespace, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to build helm RESTClientGetter: %w", err)
	}

	if err := actionConfig.Init(getter, namespace, "secrets", log.Printf); err != nil {
		if username != "" {
			return nil, fmt.Errorf("failed to initialize helm action config for user %s: %w", username, err)
		}
		return nil, fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	return actionConfig, nil
}

// restClientGetter picks the RESTClientGetter strategy for this client.
// Caller must hold c.mu (read or write). Reads global k8s package state
// (rest.Config, current context); pure logic lives in
// buildRESTClientGetter so it can be tested without those globals.
func (c *Client) restClientGetter(namespace, username string, groups []string) (genericclioptions.RESTClientGetter, error) {
	return buildRESTClientGetter(restClientGetterParams{
		kubeconfig:     c.kubeconfig,
		restConfig:     k8s.GetConfig(),
		currentContext: k8s.GetContextName(),
		namespace:      namespace,
		username:       username,
		groups:         groups,
	})
}

type restClientGetterParams struct {
	kubeconfig     string
	restConfig     *rest.Config
	currentContext string
	namespace      string
	username       string
	groups         []string
}

// buildRESTClientGetter is the pure logic behind Client.restClientGetter.
// Two strategies:
//
//   - kubeconfig path is set: hand Helm a ConfigFlags pointing at that
//     single file. This is the dominant OSS path (kubectl plugin /
//     standalone binary on a laptop with ~/.kube/config).
//   - kubeconfig path is empty: hand Helm the rest.Config Radar already
//     resolved at boot. Fires for in-cluster deploys (Hub mode, OSS
//     Helm-chart deploy — no ~/.kube/config in the pod) and for
//     multi-source kubeconfig modes (--kubeconfig-dir / multi-path
//     KUBECONFIG, where there's no single file path to hand Helm).
func buildRESTClientGetter(p restClientGetterParams) (genericclioptions.RESTClientGetter, error) {
	if p.kubeconfig == "" {
		if p.restConfig != nil {
			return newRESTConfigGetter(p.restConfig, p.namespace, p.username, p.groups), nil
		}
		// No kubeconfig path AND no resolved rest.Config — no point in
		// handing Helm a getter that would fall through to localhost:8080.
		// Surface the misconfiguration instead.
		return nil, fmt.Errorf("helm: no kubeconfig path and no resolved rest.Config available")
	}

	// usePersistentConfig=false avoids caching issues across context switches.
	configFlags := genericclioptions.NewConfigFlags(false)
	// Override the default discovery cache dir ($HOME/.kube/cache) to a writable path
	// when running on a read-only filesystem (e.g. in-cluster with readOnlyRootFilesystem).
	if homeDir, err := os.UserHomeDir(); err != nil || !isDirWritable(homeDir) {
		kubeCacheDir := "/tmp/helm/kube-cache"
		configFlags.CacheDir = &kubeCacheDir
	}
	configFlags.KubeConfig = &p.kubeconfig
	if p.namespace != "" {
		configFlags.Namespace = &p.namespace
	}

	// Use Explorer's current context (in-memory) instead of kubeconfig's
	// current-context, so Helm tracks Explorer through context switches.
	if p.currentContext != "" && p.currentContext != "in-cluster" {
		configFlags.Context = &p.currentContext
	}

	if p.username != "" {
		configFlags.Impersonate = &p.username
		configFlags.ImpersonateGroup = &p.groups
	}

	return configFlags, nil
}

// GetActionConfig returns an action configuration for the given namespace.
// Exported for use by handlers that need to pass user-specific configs.
func (c *Client) GetActionConfig(namespace string) (*action.Configuration, error) {
	return c.getActionConfig(namespace)
}

// GetActionConfigForUser returns an action configuration with K8s impersonation.
// Exported for use by handlers that need to pass user-specific configs.
func (c *Client) GetActionConfigForUser(namespace, username string, groups []string) (*action.Configuration, error) {
	return c.getActionConfigForUser(namespace, username, groups)
}

// ListReleasesAsUser is ListReleases with K8s impersonation.
// When username is empty, falls back to the ServiceAccount identity (same
// behavior as ListReleases).
func (c *Client) ListReleasesAsUser(namespace, username string, groups []string) ([]HelmRelease, error) {
	if username == "" {
		return c.ListReleases(namespace)
	}
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return listReleasesWith(actionConfig, namespace, username, groups)
}

// ListReleases returns all Helm releases, optionally filtered by namespace
func (c *Client) ListReleases(namespace string) ([]HelmRelease, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return listReleasesWith(actionConfig, namespace, "", nil)
}

// ListReleasesAcrossNamespaces lists releases for an explicit set of namespaces
// and merges the results. A nil slice means "cluster-wide" (a single
// AllNamespaces list). Callers pass the identity's accessible namespaces instead
// of nil when it can't list secrets cluster-wide, so namespace-restricted users
// and ServiceAccounts read Helm without a cluster-scoped `list secrets` (403).
// Per-namespace lists are disjoint, so the merge can't duplicate a release.
//
// The accessible-namespace set is discovered from pod/deployment access, which
// doesn't imply secrets access (Helm storage is Secrets) — a namespace where the
// caller is bound to e.g. `view` denies the read. Those forbidden namespaces are
// skipped so one of them doesn't blank releases the caller CAN see. Only when
// every namespace is forbidden is the 403 surfaced, so the UI still shows
// "Access Restricted" rather than a misleading empty list.
func (c *Client) ListReleasesAcrossNamespaces(namespaces []string, username string, groups []string) ([]HelmRelease, error) {
	if namespaces == nil {
		return c.ListReleasesAsUser("", username, groups)
	}
	var all []HelmRelease
	var lastForbidden error
	authorized := false
	for _, ns := range namespaces {
		rels, err := c.ListReleasesAsUser(ns, username, groups)
		if err != nil {
			if IsForbiddenError(err) {
				lastForbidden = err
				continue
			}
			return nil, err
		}
		authorized = true
		all = append(all, rels...)
	}
	if !authorized && lastForbidden != nil {
		return nil, lastForbidden
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Namespace != all[j].Namespace {
			return all[i].Namespace < all[j].Namespace
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

func listReleasesWith(actionConfig *action.Configuration, namespace, username string, groups []string) ([]HelmRelease, error) {
	if err := actionConfig.KubeClient.IsReachable(); err != nil {
		return nil, fmt.Errorf("failed to list helm releases: %w", err)
	}

	client, err := helmStorageClient(username, groups)
	if err != nil {
		return nil, err
	}
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, namespace)
	if err != nil {
		return nil, err
	}

	result := helmReleaseRowsFromStorageSnapshot(snapshot, fluxHelmReleaseMap(context.Background()))

	// Sort by namespace, then name
	sort.Slice(result, func(i, j int) bool {
		if result[i].Namespace != result[j].Namespace {
			return result[i].Namespace < result[j].Namespace
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

func helmReleaseRowsFromStorageSnapshot(snapshot *helmReleaseStorageSnapshot, fluxMap map[string]string) []HelmRelease {
	if snapshot == nil {
		return nil
	}
	result := make([]HelmRelease, 0, len(snapshot.latest))
	for _, rel := range snapshot.latest {
		storageNs := snapshot.storageNamespaces[releaseStorageKey(rel)]
		hr := toHelmRelease(rel, storageNs)
		historyKey := releaseHistoryKey(rel)
		analysis := helmhistory.Analyze(rel.Name, rel.Version, toHelmHistoryRevisions(snapshot.histories[historyKey]), helmhistory.Options{MaxOperations: releaseListMaxOperations})
		hr.LastOperation = analysis.LastOperation
		hr.Operations = analysis.Operations
		// Match against the release's *actual* storage namespace (the
		// un-normalized value), since toHelmRelease zeroes StorageNamespace
		// when it equals Namespace for compactness.
		effectiveStorage := storageNs
		if effectiveStorage == "" {
			effectiveStorage = rel.Namespace
		}
		hr.ManagedByFluxHelmRelease = applyFluxOwnership(rel.Name, effectiveStorage, fluxMap)
		result = append(result, hr)
	}
	return result
}

// GetReleaseAsUser is GetRelease with K8s impersonation.
// When username is empty, falls back to the ServiceAccount identity.
func (c *Client) GetReleaseAsUser(namespace, name, username string, groups []string) (*HelmReleaseDetail, error) {
	if username == "" {
		return c.GetRelease(namespace, name)
	}
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return getReleaseWith(actionConfig, namespace, name)
}

// GetRelease returns details for a specific release
func (c *Client) GetRelease(namespace, name string) (*HelmReleaseDetail, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return getReleaseWith(actionConfig, namespace, name)
}

func getReleaseWith(actionConfig *action.Configuration, namespace, name string) (*HelmReleaseDetail, error) {
	// Get the latest release
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release %s/%s: %w", namespace, name, err)
	}

	// Get release history
	historyAction := action.NewHistory(actionConfig)
	historyAction.Max = releaseHistoryMax
	history, err := historyAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release history: %w", err)
	}

	// Convert history
	revisions := make([]HelmRevision, 0, len(history))
	for _, h := range history {
		if revision, ok := toHelmRevision(h); ok {
			revisions = append(revisions, revision)
		}
	}

	// Sort by revision descending (newest first)
	sort.Slice(revisions, func(i, j int) bool {
		return revisions[i].Revision > revisions[j].Revision
	})
	analysis := helmhistory.Analyze(rel.Name, rel.Version, toHelmHistoryRevisions(revisions), helmhistory.Options{})

	// Parse manifest to get owned resources
	resources := parseManifestResources(rel.Manifest, rel.Namespace)

	// Enrich resources with live status from k8s cache
	enrichResourcesWithStatus(resources)
	health, issue, summary := computeResourceHealth(resources)

	// Extract hooks
	hooks := extractHooks(rel)
	hookDiagnostics := extractHookDiagnostics(hooks)

	// Extract README from chart files
	readme := extractReadme(rel)

	// Extract dependencies
	dependencies := extractDependencies(rel)

	effectiveStorage := namespace
	if effectiveStorage == "" {
		effectiveStorage = rel.Namespace
	}
	managedByFlux := applyFluxOwnership(rel.Name, effectiveStorage, fluxHelmReleaseMap(context.Background()))

	detail := &HelmReleaseDetail{
		Name:                     rel.Name,
		Namespace:                rel.Namespace,
		StorageNamespace:         namespace,
		Chart:                    rel.Chart.Metadata.Name,
		ChartVersion:             rel.Chart.Metadata.Version,
		AppVersion:               rel.Chart.Metadata.AppVersion,
		Status:                   rel.Info.Status.String(),
		Revision:                 rel.Version,
		Updated:                  rel.Info.LastDeployed.Time,
		Description:              rel.Info.Description,
		Notes:                    rel.Info.Notes,
		History:                  revisions,
		Resources:                resources,
		ResourceHealth:           health,
		HealthIssue:              issue,
		HealthSummary:            summary,
		Hooks:                    hooks,
		HookDiagnostics:          hookDiagnostics,
		Readme:                   readme,
		Dependencies:             dependencies,
		LastOperation:            analysis.LastOperation,
		Operations:               analysis.Operations,
		ManagedByFluxHelmRelease: managedByFlux,
	}
	detail.OperationInsight = buildOperationInsight(detail)
	if detail.StorageNamespace == detail.Namespace {
		detail.StorageNamespace = ""
	}

	return detail, nil
}

// GetManifest returns the rendered manifest for a release at a specific revision
func (c *Client) GetManifest(namespace, name string, revision int) (string, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return "", err
	}
	return getManifestWith(actionConfig, name, revision)
}

// GetManifestAsUser is GetManifest with K8s impersonation.
func (c *Client) GetManifestAsUser(namespace, name string, revision int, username string, groups []string) (string, error) {
	if username == "" {
		return c.GetManifest(namespace, name, revision)
	}
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return "", err
	}
	return getManifestWith(actionConfig, name, revision)
}

func getManifestWith(actionConfig *action.Configuration, name string, revision int) (string, error) {
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
	return c.GetValuesRevision(namespace, name, allValues, 0)
}

// GetValuesRevision returns the values for a release revision. revision=0 uses the latest.
func (c *Client) GetValuesRevision(namespace, name string, allValues bool, revision int) (*HelmValues, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return getValuesWith(actionConfig, name, allValues, revision)
}

// GetValuesAsUser is GetValues with K8s impersonation.
func (c *Client) GetValuesAsUser(namespace, name string, allValues bool, username string, groups []string) (*HelmValues, error) {
	return c.GetValuesRevisionAsUser(namespace, name, allValues, 0, username, groups)
}

// GetValuesRevisionAsUser is GetValuesRevision with K8s impersonation.
func (c *Client) GetValuesRevisionAsUser(namespace, name string, allValues bool, revision int, username string, groups []string) (*HelmValues, error) {
	if username == "" {
		return c.GetValuesRevision(namespace, name, allValues, revision)
	}
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return getValuesWith(actionConfig, name, allValues, revision)
}

func getValuesWith(actionConfig *action.Configuration, name string, allValues bool, revision int) (*HelmValues, error) {
	getValuesAction := action.NewGetValues(actionConfig)
	getValuesAction.AllValues = allValues
	getValuesAction.Version = revision

	values, err := getValuesAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release values: %w", err)
	}

	if allValues {
		result := &HelmValues{
			Computed:     values,
			UserSupplied: map[string]any{},
		}
		getValuesAction.AllValues = false
		getValuesAction.Version = revision
		userValues, err := getValuesAction.Run(name)
		if err == nil {
			result.UserSupplied = userValues
		}
		return result, nil
	}

	return &HelmValues{UserSupplied: values}, nil
}

// GetValuesDiff returns a values diff between two revisions.
func (c *Client) GetValuesDiff(namespace, name string, revision1, revision2 int, allValues bool) (*ValuesDiff, error) {
	return c.getValuesDiff(namespace, name, revision1, revision2, allValues, "", nil)
}

// GetValuesDiffAsUser is GetValuesDiff with K8s impersonation.
func (c *Client) GetValuesDiffAsUser(namespace, name string, revision1, revision2 int, allValues bool, username string, groups []string) (*ValuesDiff, error) {
	return c.getValuesDiff(namespace, name, revision1, revision2, allValues, username, groups)
}

func (c *Client) getValuesDiff(namespace, name string, revision1, revision2 int, allValues bool, username string, groups []string) (*ValuesDiff, error) {
	values1, err := c.GetValuesRevisionAsUser(namespace, name, allValues, revision1, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get values for revision %d: %w", revision1, err)
	}
	values2, err := c.GetValuesRevisionAsUser(namespace, name, allValues, revision2, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get values for revision %d: %w", revision2, err)
	}
	diff, err := computeValuesDiff(values1, values2, revision1, revision2, allValues)
	if err != nil {
		return nil, err
	}
	return &ValuesDiff{Revision1: revision1, Revision2: revision2, AllValues: allValues, Diff: diff}, nil
}

// GetManifestDiff returns the diff between two revisions
func (c *Client) GetManifestDiff(namespace, name string, revision1, revision2 int) (*ManifestDiff, error) {
	return c.getManifestDiff(namespace, name, revision1, revision2, "", nil)
}

// GetManifestDiffAsUser is GetManifestDiff with K8s impersonation.
func (c *Client) GetManifestDiffAsUser(namespace, name string, revision1, revision2 int, username string, groups []string) (*ManifestDiff, error) {
	return c.getManifestDiff(namespace, name, revision1, revision2, username, groups)
}

func (c *Client) getManifestDiff(namespace, name string, revision1, revision2 int, username string, groups []string) (*ManifestDiff, error) {
	manifest1, err := c.GetManifestAsUser(namespace, name, revision1, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest for revision %d: %w", revision1, err)
	}

	manifest2, err := c.GetManifestAsUser(namespace, name, revision2, username, groups)
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

// GetNotesDiff returns a release notes diff between two revisions.
func (c *Client) GetNotesDiff(namespace, name string, revision1, revision2 int) (*NotesDiff, error) {
	return c.getNotesDiff(namespace, name, revision1, revision2, "", nil)
}

// GetNotesDiffAsUser is GetNotesDiff with K8s impersonation.
func (c *Client) GetNotesDiffAsUser(namespace, name string, revision1, revision2 int, username string, groups []string) (*NotesDiff, error) {
	return c.getNotesDiff(namespace, name, revision1, revision2, username, groups)
}

func (c *Client) getNotesDiff(namespace, name string, revision1, revision2 int, username string, groups []string) (*NotesDiff, error) {
	rel1, err := c.getReleaseRevisionAsUser(namespace, name, revision1, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision1, err)
	}
	rel2, err := c.getReleaseRevisionAsUser(namespace, name, revision2, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision2, err)
	}
	return &NotesDiff{
		Revision1: revision1,
		Revision2: revision2,
		Diff:      computeDiff(releaseNotes(rel1), releaseNotes(rel2), revision1, revision2),
	}, nil
}

func releaseNotes(rel *release.Release) string {
	if rel == nil || rel.Info == nil {
		return ""
	}
	return rel.Info.Notes
}

// GetHooksDiff returns a hook metadata diff between two revisions.
func (c *Client) GetHooksDiff(namespace, name string, revision1, revision2 int) (*HooksDiff, error) {
	return c.getHooksDiff(namespace, name, revision1, revision2, "", nil)
}

// GetHooksDiffAsUser is GetHooksDiff with K8s impersonation.
func (c *Client) GetHooksDiffAsUser(namespace, name string, revision1, revision2 int, username string, groups []string) (*HooksDiff, error) {
	return c.getHooksDiff(namespace, name, revision1, revision2, username, groups)
}

func (c *Client) getHooksDiff(namespace, name string, revision1, revision2 int, username string, groups []string) (*HooksDiff, error) {
	rel1, err := c.getReleaseRevisionAsUser(namespace, name, revision1, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision1, err)
	}
	rel2, err := c.getReleaseRevisionAsUser(namespace, name, revision2, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision2, err)
	}
	removed, added, modified, unchanged := diffHooks(extractHooks(rel1), extractHooks(rel2))
	return &HooksDiff{
		Revision1: revision1,
		Revision2: revision2,
		Added:     nonNilHelmHooks(added),
		Removed:   nonNilHelmHooks(removed),
		Modified:  nonNilHelmHooks(modified),
		Unchanged: nonNilHelmHooks(unchanged),
	}, nil
}

func nonNilHelmHooks(hooks []HelmHook) []HelmHook {
	if hooks == nil {
		return []HelmHook{}
	}
	return hooks
}

// GetResourceDiff returns added/removed rendered resources between two revisions.
func (c *Client) GetResourceDiff(namespace, name string, revision1, revision2 int) (*ResourceDiff, error) {
	return c.getResourceDiff(namespace, name, revision1, revision2, "", nil)
}

// GetResourceDiffAsUser is GetResourceDiff with K8s impersonation.
func (c *Client) GetResourceDiffAsUser(namespace, name string, revision1, revision2 int, username string, groups []string) (*ResourceDiff, error) {
	return c.getResourceDiff(namespace, name, revision1, revision2, username, groups)
}

func (c *Client) getResourceDiff(namespace, name string, revision1, revision2 int, username string, groups []string) (*ResourceDiff, error) {
	rel1, err := c.getReleaseRevisionAsUser(namespace, name, revision1, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision1, err)
	}
	rel2, err := c.getReleaseRevisionAsUser(namespace, name, revision2, username, groups)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision2, err)
	}
	leftResources, leftParseErrors := parseManifestResourceObjects(rel1.Manifest, rel1.Namespace)
	rightResources, rightParseErrors := parseManifestResourceObjects(rel2.Manifest, rel2.Namespace)
	removed, added, common := diffResourceRefs(resourceRefsFromRendered(leftResources), resourceRefsFromRendered(rightResources))
	modified, unchanged := diffRenderedResourceObjects(common, leftResources, rightResources)
	return &ResourceDiff{
		Revision1:       revision1,
		Revision2:       revision2,
		Added:           nonNilResourceRefs(added),
		Removed:         nonNilResourceRefs(removed),
		Modified:        nonNilResourceChanges(modified),
		Unchanged:       nonNilResourceRefs(unchanged),
		ParseErrorCount: leftParseErrors + rightParseErrors,
	}, nil
}

func nonNilResourceRefs(refs []ResourceRef) []ResourceRef {
	if refs == nil {
		return []ResourceRef{}
	}
	return refs
}

func nonNilResourceChanges(changes []ResourceChange) []ResourceChange {
	if changes == nil {
		return []ResourceChange{}
	}
	return changes
}

func (c *Client) getReleaseRevisionAsUser(namespace, name string, revision int, username string, groups []string) (*release.Release, error) {
	var (
		actionConfig *action.Configuration
		err          error
	)
	if username == "" {
		actionConfig, err = c.getActionConfig(namespace)
	} else {
		actionConfig, err = c.getActionConfigForUser(namespace, username, groups)
	}
	if err != nil {
		return nil, err
	}
	getAction := action.NewGet(actionConfig)
	if revision > 0 {
		getAction.Version = revision
	}
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release: %w", err)
	}
	return rel, nil
}

func computeValuesDiff(values1, values2 *HelmValues, rev1, rev2 int, allValues bool) (string, error) {
	var left, right map[string]any
	if allValues {
		left = values1.Computed
		right = values2.Computed
	} else {
		left = values1.UserSupplied
		right = values2.UserSupplied
	}
	leftYAML, err := valuesMapYAML(left)
	if err != nil {
		return "", fmt.Errorf("failed to serialize values for revision %d: %w", rev1, err)
	}
	rightYAML, err := valuesMapYAML(right)
	if err != nil {
		return "", fmt.Errorf("failed to serialize values for revision %d: %w", rev2, err)
	}
	return computeDiff(leftYAML, rightYAML, rev1, rev2), nil
}

func valuesMapYAML(values map[string]any) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	b, err := yaml.Marshal(values)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(b), "\n"), nil
}

func resourceRefs(resources []OwnedResource) []ResourceRef {
	refs := make([]ResourceRef, 0, len(resources))
	for _, r := range resources {
		refs = append(refs, ResourceRef{
			Kind:       r.Kind,
			APIVersion: r.APIVersion,
			Name:       r.Name,
			Namespace:  r.Namespace,
		})
	}
	sortResourceRefs(refs)
	return refs
}

func resourceRefsFromRendered(resources []renderedResource) []ResourceRef {
	refs := make([]ResourceRef, 0, len(resources))
	for _, r := range resources {
		refs = append(refs, r.Ref)
	}
	sortResourceRefs(refs)
	return refs
}

func diffResourceRefs(left, right []ResourceRef) (removed, added, unchanged []ResourceRef) {
	leftMap := make(map[string]ResourceRef, len(left))
	rightMap := make(map[string]ResourceRef, len(right))
	for _, ref := range left {
		leftMap[resourceRefKey(ref)] = ref
	}
	for _, ref := range right {
		rightMap[resourceRefKey(ref)] = ref
	}
	for key, ref := range leftMap {
		if _, ok := rightMap[key]; ok {
			unchanged = append(unchanged, ref)
			continue
		}
		removed = append(removed, ref)
	}
	for key, ref := range rightMap {
		if _, ok := leftMap[key]; ok {
			continue
		}
		added = append(added, ref)
	}
	sortResourceRefs(removed)
	sortResourceRefs(added)
	sortResourceRefs(unchanged)
	return removed, added, unchanged
}

func diffHooks(left, right []HelmHook) (removed, added, modified, unchanged []HelmHook) {
	leftMap := make(map[string]HelmHook, len(left))
	rightMap := make(map[string]HelmHook, len(right))
	for _, hook := range left {
		leftMap[helmHookKey(hook)] = hook
	}
	for _, hook := range right {
		rightMap[helmHookKey(hook)] = hook
	}
	for key, hook := range leftMap {
		next, ok := rightMap[key]
		if !ok {
			removed = append(removed, hook)
			continue
		}
		if helmHookSignature(hook) == helmHookSignature(next) {
			unchanged = append(unchanged, next)
			continue
		}
		next.ManifestChanged = hook.ManifestDigest != next.ManifestDigest
		modified = append(modified, next)
	}
	for key, hook := range rightMap {
		if _, ok := leftMap[key]; ok {
			continue
		}
		added = append(added, hook)
	}
	sortHelmHooks(removed)
	sortHelmHooks(added)
	sortHelmHooks(modified)
	sortHelmHooks(unchanged)
	return removed, added, modified, unchanged
}

func sortHelmHooks(hooks []HelmHook) {
	sort.Slice(hooks, func(i, j int) bool {
		return helmHookKey(hooks[i]) < helmHookKey(hooks[j])
	})
}

func helmHookKey(hook HelmHook) string {
	return hook.Namespace + "/" + hook.Kind + "/" + hook.Name
}

func helmHookSignature(hook HelmHook) string {
	stable := struct {
		Path              string
		ManifestDigest    string
		Events            []string
		Weight            int
		DeletePolicies    []string
		OutputLogPolicies []string
	}{
		Path:              hook.Path,
		ManifestDigest:    hook.ManifestDigest,
		Events:            sortedStrings(hook.Events),
		Weight:            hook.Weight,
		DeletePolicies:    sortedStrings(hook.DeletePolicies),
		OutputLogPolicies: sortedStrings(hook.OutputLogPolicies),
	}
	b, err := json.Marshal(stable)
	if err != nil {
		return fmt.Sprintf("%#v", stable)
	}
	return string(b)
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sortResourceRefs(refs []ResourceRef) {
	sort.Slice(refs, func(i, j int) bool {
		return resourceRefKey(refs[i]) < resourceRefKey(refs[j])
	})
}

func resourceRefKey(ref ResourceRef) string {
	return ref.APIVersion + "/" + ref.Kind + "/" + ref.Namespace + "/" + ref.Name
}

func diffRenderedResourceObjects(common []ResourceRef, leftResources, rightResources []renderedResource) (modified []ResourceChange, unchanged []ResourceRef) {
	leftMap := renderedResourceMap(leftResources)
	rightMap := renderedResourceMap(rightResources)
	for _, ref := range common {
		oldResource, oldOK := leftMap[resourceRefKey(ref)]
		newResource, newOK := rightMap[resourceRefKey(ref)]
		if !oldOK || !newOK {
			unchanged = append(unchanged, ref)
			continue
		}
		diff := k8s.ComputeDiffFromUnstructured(
			ref.Kind,
			normalizeRenderedResourceForDiff(oldResource.Object),
			normalizeRenderedResourceForDiff(newResource.Object),
		)
		if diff == nil || len(diff.Fields) == 0 {
			unchanged = append(unchanged, ref)
			continue
		}
		modified = append(modified, ResourceChange{
			ResourceRef: ref,
			Summary:     diff.Summary,
			FieldCount:  len(diff.Fields),
			Fields:      diff.Fields,
		})
	}
	sortResourceChanges(modified)
	sortResourceRefs(unchanged)
	return modified, unchanged
}

func normalizeRenderedResourceForDiff(in *unstructured.Unstructured) *unstructured.Unstructured {
	if in == nil {
		return nil
	}
	out := in.DeepCopy()
	labels := out.GetLabels()
	if len(labels) == 0 {
		return out
	}
	normalized := make(map[string]string, len(labels))
	for key, value := range labels {
		if key == "helm.sh/chart" {
			continue
		}
		normalized[key] = value
	}
	if len(normalized) == 0 {
		unstructured.RemoveNestedField(out.Object, "metadata", "labels")
		return out
	}
	out.SetLabels(normalized)
	return out
}

func renderedResourceMap(resources []renderedResource) map[string]renderedResource {
	out := make(map[string]renderedResource, len(resources))
	for _, resource := range resources {
		out[resourceRefKey(resource.Ref)] = resource
	}
	return out
}

func sortResourceChanges(changes []ResourceChange) {
	sort.Slice(changes, func(i, j int) bool {
		return resourceRefKey(changes[i].ResourceRef) < resourceRefKey(changes[j].ResourceRef)
	})
}

// releaseStorageKey identifies a release independent of where Helm stored the
// record. Flux commonly stores the release secret in its controller namespace
// while the release targets a different namespace.
func releaseStorageKey(rel *release.Release) string {
	if rel == nil {
		return ""
	}
	return fmt.Sprintf("%s/%s/%d", rel.Namespace, rel.Name, rel.Version)
}

func releaseHistoryKey(rel *release.Release) string {
	if rel == nil {
		return ""
	}
	return rel.Namespace + "/" + rel.Name
}

func releaseUpgradeKey(rel *release.Release, storageNamespace string) string {
	if storageNamespace == "" {
		storageNamespace = rel.Namespace
	}
	return storageNamespace + "/" + rel.Name
}

// toHelmRelease converts a helm release to our API type
func toHelmRelease(rel *release.Release, storageNamespace string) HelmRelease {
	hr := HelmRelease{
		Name:             rel.Name,
		Namespace:        rel.Namespace,
		StorageNamespace: storageNamespace,
		Chart:            rel.Chart.Metadata.Name,
		ChartVersion:     rel.Chart.Metadata.Version,
		AppVersion:       rel.Chart.Metadata.AppVersion,
		Status:           rel.Info.Status.String(),
		Revision:         rel.Version,
		Updated:          rel.Info.LastDeployed.Time,
	}
	if hr.StorageNamespace == hr.Namespace {
		hr.StorageNamespace = ""
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

// fluxHelmReleaseMap returns a map keyed by "<storageNamespace>/<releaseName>"
// to "<hrNamespace>/<hrName>" for every Flux HelmRelease CR in the cluster.
// Helm releases that match a key were installed by Flux's helm-controller and
// shouldn't be helm-upgraded directly — the next Flux reconcile would revert
// the change. Built from the dynamic informer cache so this is a constant-time
// lookup per release.
//
// Effective storageNamespace: defaults to spec.storageNamespace if set, else
// the HelmRelease's own metadata.namespace. Effective releaseName: defaults
// to the HelmRelease's metadata.name. Both match helm-controller's behavior.
//
// Returns an empty map (not an error) when the cluster has no Flux CRDs or
// the cache lookup fails — the badge is best-effort, not load-bearing.
func fluxHelmReleaseMap(ctx context.Context) map[string]string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	hrs, err := cache.ListDynamicWithGroup(ctx, "HelmRelease", "", "helm.toolkit.fluxcd.io")
	if err != nil || len(hrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(hrs))
	for _, hr := range hrs {
		spec, _, _ := unstructured.NestedMap(hr.Object, "spec")
		releaseName, _ := spec["releaseName"].(string)
		if releaseName == "" {
			releaseName = hr.GetName()
		}
		// helm-controller defaults storageNamespace to the HelmRelease's
		// own namespace, NOT spec.targetNamespace (the latter is where the
		// chart's resources go; the former is where Helm's release Secret
		// lives). The fixture's HelmRelease in flux-system targeting
		// demo-flux-helm stores its release Secret in flux-system, confirming
		// this default.
		storageNs, _ := spec["storageNamespace"].(string)
		if storageNs == "" {
			storageNs = hr.GetNamespace()
		}
		out[storageNs+"/"+releaseName] = hr.GetNamespace() + "/" + hr.GetName()
	}
	return out
}

// applyFluxOwnership stamps ManagedByFluxHelmRelease on a HelmRelease (or
// HelmReleaseDetail via the type-conversion call sites). storageNamespace is
// the release's actual storage namespace (callers normalize when it equals
// the release namespace — pass the un-normalized value here so the lookup
// matches helm-controller's map).
func applyFluxOwnership(name, storageNamespace string, fluxMap map[string]string) string {
	if fluxMap == nil {
		return ""
	}
	return fluxMap[storageNamespace+"/"+name]
}

type helmReleaseStorageSnapshot struct {
	storageNamespaces map[string]string
	histories         map[string][]HelmRevision
	latest            []*release.Release
}

func helmStorageClient(username string, groups []string) (kubernetes.Interface, error) {
	var client kubernetes.Interface = k8s.GetClient()
	if username != "" {
		impersonated, err := k8s.ImpersonatedClient(username, groups)
		if err != nil {
			return nil, fmt.Errorf("failed to build impersonated client for release storage lookup: %w", err)
		}
		client = impersonated
	}
	if client == nil {
		return nil, fmt.Errorf("kubernetes client not initialized for release storage lookup")
	}
	return client, nil
}

func helmReleaseStorageNamespaces(username string, groups []string) (map[string]string, error) {
	client, err := helmStorageClient(username, groups)
	if err != nil {
		return nil, err
	}
	return helmReleaseStorageNamespacesWithClient(client)
}

func helmReleaseStorageNamespacesWithClient(client kubernetes.Interface) (map[string]string, error) {
	snapshot, err := helmReleaseStorageSnapshotWithClient(client, "")
	if err != nil {
		return nil, err
	}
	return snapshot.storageNamespaces, nil
}

func helmReleaseStorageSnapshotWithClient(client kubernetes.Interface, namespace string) (*helmReleaseStorageSnapshot, error) {
	secrets, err := client.CoreV1().Secrets(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: "owner=helm",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect release storage namespaces: %w", err)
	}

	snapshot := &helmReleaseStorageSnapshot{
		storageNamespaces: make(map[string]string, len(secrets.Items)),
		histories:         make(map[string][]HelmRevision),
	}
	latestByRelease := make(map[string]*release.Release)
	for _, secret := range secrets.Items {
		encoded := secret.Data["release"]
		if len(encoded) == 0 {
			continue
		}
		rel, err := decodeHelmReleaseData(string(encoded))
		if err != nil {
			log.Printf("[helm] failed to decode release secret %s/%s: %v", secret.Namespace, secret.Name, err)
			continue
		}
		snapshot.storageNamespaces[releaseStorageKey(rel)] = secret.Namespace

		historyKey := releaseHistoryKey(rel)
		if revision, ok := toHelmRevision(rel); ok {
			snapshot.histories[historyKey] = append(snapshot.histories[historyKey], revision)
		}

		if latest, exists := latestByRelease[historyKey]; !exists || latest.Version <= rel.Version {
			latestByRelease[historyKey] = rel
		}
	}
	for key := range snapshot.histories {
		sort.Slice(snapshot.histories[key], func(i, j int) bool {
			return snapshot.histories[key][i].Revision > snapshot.histories[key][j].Revision
		})
		if len(snapshot.histories[key]) > releaseHistoryMax {
			snapshot.histories[key] = snapshot.histories[key][:releaseHistoryMax]
		}
	}
	snapshot.latest = make([]*release.Release, 0, len(latestByRelease))
	for _, rel := range latestByRelease {
		if !helmListAllIncludes(rel) {
			continue
		}
		snapshot.latest = append(snapshot.latest, rel)
	}
	return snapshot, nil
}

func helmListAllIncludes(rel *release.Release) bool {
	if rel == nil || rel.Info == nil || rel.Chart == nil || rel.Chart.Metadata == nil {
		return false
	}
	return action.ListAll&action.ListAll.FromName(rel.Info.Status.String()) != 0
}

func decodeHelmReleaseData(data string) (*release.Release, error) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	if len(b) > 3 && bytes.Equal(b[0:3], []byte{0x1f, 0x8b, 0x08}) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		b, err = io.ReadAll(r)
		if err != nil {
			return nil, err
		}
	}
	var rel release.Release
	if err := json.Unmarshal(b, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
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

// toHelmRevision converts a helm release to a revision entry.
func toHelmRevision(rel *release.Release) (HelmRevision, bool) {
	if rel == nil || rel.Info == nil || rel.Chart == nil || rel.Chart.Metadata == nil {
		return HelmRevision{}, false
	}
	return HelmRevision{
		Revision:    rel.Version,
		Status:      rel.Info.Status.String(),
		Chart:       rel.Chart.Metadata.Name + "-" + rel.Chart.Metadata.Version,
		AppVersion:  rel.Chart.Metadata.AppVersion,
		Description: rel.Info.Description,
		Updated:     rel.Info.LastDeployed.Time,
	}, true
}

func toHelmHistoryRevisions(revisions []HelmRevision) []helmhistory.Revision {
	out := make([]helmhistory.Revision, 0, len(revisions))
	for _, r := range revisions {
		out = append(out, helmhistory.Revision{
			Revision:    r.Revision,
			Status:      r.Status,
			Chart:       r.Chart,
			AppVersion:  r.AppVersion,
			Description: r.Description,
			Updated:     r.Updated,
		})
	}
	return out
}

type renderedResource struct {
	Ref    ResourceRef
	Object *unstructured.Unstructured
}

func parseManifestResourceObjects(manifest, defaultNamespace string) ([]renderedResource, int) {
	resources := []renderedResource{}
	manifests := releaseutil.SplitManifests(manifest)
	parseErrorCount := 0

	for _, m := range manifests {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		jsonBytes, err := yaml.YAMLToJSON([]byte(m))
		if err != nil {
			parseErrorCount++
			continue
		}
		var obj unstructured.Unstructured
		if err := json.Unmarshal(jsonBytes, &obj.Object); err != nil {
			parseErrorCount++
			continue
		}
		if obj.GetKind() == "" || obj.GetName() == "" {
			continue
		}
		apiGroup := ""
		if group, _, ok := strings.Cut(obj.GetAPIVersion(), "/"); ok {
			apiGroup = group
		}
		clusterScoped, _, _ := k8s.ClassifyKindScope(obj.GetKind(), apiGroup)
		if obj.GetNamespace() == "" && !clusterScoped {
			obj.SetNamespace(defaultNamespace)
		}
		resources = append(resources, renderedResource{
			Ref: ResourceRef{
				Kind:       obj.GetKind(),
				APIVersion: obj.GetAPIVersion(),
				Name:       obj.GetName(),
				Namespace:  obj.GetNamespace(),
			},
			Object: &obj,
		})
	}

	sort.Slice(resources, func(i, j int) bool {
		return resourceRefKey(resources[i].Ref) < resourceRefKey(resources[j].Ref)
	})

	return resources, parseErrorCount
}

// parseManifestResources extracts K8s resources from a rendered manifest
func parseManifestResources(manifest, defaultNamespace string) []OwnedResource {
	rendered, _ := parseManifestResourceObjects(manifest, defaultNamespace)
	resources := make([]OwnedResource, 0, len(rendered))
	for _, resource := range rendered {
		resources = append(resources, OwnedResource{
			Kind:       resource.Ref.Kind,
			APIVersion: resource.Ref.APIVersion,
			Name:       resource.Ref.Name,
			Namespace:  resource.Ref.Namespace,
		})
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
		return []HelmHook{}
	}

	hooks := make([]HelmHook, 0, len(rel.Hooks))
	for _, h := range rel.Hooks {
		namespace := rel.Namespace
		for _, ref := range parseManifestResources(h.Manifest, rel.Namespace) {
			if ref.Name == h.Name && strings.EqualFold(ref.Kind, h.Kind) {
				namespace = ref.Namespace
				break
			}
		}

		events := make([]string, 0, len(h.Events))
		for _, e := range h.Events {
			events = append(events, string(e))
		}
		deletePolicies := make([]string, 0, len(h.DeletePolicies))
		for _, p := range h.DeletePolicies {
			deletePolicies = append(deletePolicies, string(p))
		}
		outputLogPolicies := make([]string, 0, len(h.OutputLogPolicies))
		for _, p := range h.OutputLogPolicies {
			outputLogPolicies = append(outputLogPolicies, string(p))
		}

		hook := HelmHook{
			Name:              h.Name,
			Namespace:         namespace,
			Kind:              h.Kind,
			Path:              h.Path,
			ManifestDigest:    manifestDigest(h.Manifest),
			Events:            events,
			Weight:            h.Weight,
			DeletePolicies:    deletePolicies,
			OutputLogPolicies: outputLogPolicies,
		}

		// Add status if available
		if h.LastRun.Phase != "" {
			hook.Status = string(h.LastRun.Phase)
			if !h.LastRun.StartedAt.Time.IsZero() {
				startedAt := h.LastRun.StartedAt.Time
				hook.StartedAt = &startedAt
			}
			if !h.LastRun.CompletedAt.Time.IsZero() {
				completedAt := h.LastRun.CompletedAt.Time
				hook.CompletedAt = &completedAt
			}
		}

		hooks = append(hooks, hook)
	}

	return hooks
}

func manifestDigest(manifest string) string {
	trimmed := strings.TrimSpace(manifest)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return fmt.Sprintf("%x", sum)
}

func extractHookDiagnostics(hooks []HelmHook) []HookDiagnostic {
	var out []HookDiagnostic
	for _, h := range hooks {
		phase := strings.ToLower(h.Status)
		if phase != "failed" && phase != "running" {
			continue
		}
		diag := HookDiagnostic{
			Name:      h.Name,
			Namespace: h.Namespace,
			Kind:      h.Kind,
			Events:    h.Events,
			Phase:     h.Status,
			Message:   fmt.Sprintf("Helm hook %q last ran with phase %q.", h.Name, h.Status),
		}
		if len(h.DeletePolicies) > 0 {
			diag.EvidenceUnavailable = true
			diag.EvidenceUnavailableReason = fmt.Sprintf("Hook delete policies may remove the Job/Pod evidence: %s.", strings.Join(h.DeletePolicies, ", "))
		}
		out = append(out, diag)
	}
	return out
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
		return []ChartDependency{}
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
	return c.checkForUpgrade(namespace, name, "", nil)
}

// CheckForUpgradeAsUser is CheckForUpgrade with K8s impersonation on the
// release read.
func (c *Client) CheckForUpgradeAsUser(namespace, name, username string, groups []string) (*UpgradeInfo, error) {
	return c.checkForUpgrade(namespace, name, username, groups)
}

func (c *Client) checkForUpgrade(namespace, name, username string, groups []string) (*UpgradeInfo, error) {
	var actionConfig *action.Configuration
	var err error
	if username != "" {
		actionConfig, err = c.getActionConfigForUser(namespace, username, groups)
	} else {
		actionConfig, err = c.getActionConfig(namespace)
	}
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

	// Load repository file. A missing/empty/unreadable repo config is not fatal —
	// the user may rely solely on registered OCI sources, so we fall through to the
	// OCI fallback with an empty classic-candidate set rather than returning early.
	var candidates []repoVersionInfo
	noClassicRepos := false
	indexLoadFailed := false
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	switch {
	case err != nil:
		if !os.IsNotExist(err) {
			log.Printf("[helm] failed to load repository config %s (treating as no classic repos): %v", repoFile, err)
		}
		noClassicRepos = true
	case len(f.Repositories) == 0:
		noClassicRepos = true
	default:
		// Search through all repo indexes, tracking which repos contain the current version
		cacheDir := c.settings.RepositoryCache
		for _, r := range f.Repositories {
			indexPath := filepath.Join(cacheDir, fmt.Sprintf("%s-index.yaml", r.Name))
			indexFile, err := repo.LoadIndexFile(indexPath)
			if err != nil {
				log.Printf("[helm] skipping repo %q: failed to load index %s: %v", r.Name, indexPath, err)
				indexLoadFailed = true
				continue
			}

			if versions, ok := indexFile.Entries[chartName]; ok {
				var latestInRepo string
				hasCurrentVersion := false
				for _, v := range versions {
					if latestInRepo == "" || compareVersions(v.Version, latestInRepo) > 0 {
						latestInRepo = v.Version
					}
					if v.Version == currentVersion {
						hasCurrentVersion = true
					}
				}
				if latestInRepo != "" {
					candidates = append(candidates, repoVersionInfo{
						repoName:          r.Name,
						repoURL:           r.URL,
						latestVersion:     latestInRepo,
						hasCurrentVersion: hasCurrentVersion,
					})
				}
			}
		}
	}

	if len(candidates) == 0 {
		// A failed classic index could be hiding the release's real (classic)
		// source, so surface that before OCI — matching resolveUpgradeChartPath —
		// rather than mislabel a classic-repo release as OCI-tracked.
		if indexLoadFailed {
			info.Error = "failed to load one or more configured repository indexes"
			return info, nil
		}
		// Genuine absence → registered OCI sources are the fallback for the user's
		// own OCI-published charts. Only here, never on classic ambiguity below —
		// falling back on ambiguity could advertise an OCI source for a release
		// that actually came from a classic repo.
		if c.applyOCIUpgrade(info, chartName, currentVersion, nil, nil) {
			return info, nil
		}
		// Genuine untracked source — registering a chart source could fix it.
		info.Untracked = true
		if noClassicRepos && len(ListOCISources()) == 0 {
			info.Error = "no chart sources configured"
		} else {
			info.Error = "chart not found in configured repositories or registered OCI sources"
		}
		return info, nil
	}

	sourceHosts := chartSourceHosts(rel.Chart.Metadata.Home, rel.Chart.Metadata.Sources)
	latestVersion, repoName := findBestUpgradeVersion(candidates, sourceHosts)
	if latestVersion == "" {
		info.Error = "could not identify upstream chart repository"
		return info, nil
	}

	info.LatestVersion = latestVersion
	info.RepositoryName = repoName
	info.SourceType = "repository"
	info.UpdateAvailable = compareVersions(latestVersion, currentVersion) > 0

	return info, nil
}

// applyOCIUpgrade probes registered OCI sources for chartName and, if one
// publishes it, fills info (LatestVersion/ChartRef/SourceType/UpdateAvailable)
// and returns true. The classic-repo inference is always tried first; this is the
// fallback for the user's own OCI-published charts that no repo index lists.
func (c *Client) applyOCIUpgrade(info *UpgradeInfo, chartName, currentVersion string, lister ociTagLister, tagCache map[string][]string) bool {
	match := c.discoverOCIUpgrade(chartName, lister, tagCache)
	if match == nil {
		return false
	}
	info.LatestVersion = match.LatestVersion
	info.ChartRef = match.ChartURL
	info.SourceType = "oci"
	info.UpdateAvailable = compareVersions(match.LatestVersion, currentVersion) > 0
	return true
}

// AvailableVersions is AvailableVersionsAsUser without impersonation.
func (c *Client) AvailableVersions(namespace, name string) ([]string, error) {
	return c.availableVersions(namespace, name, "", nil)
}

// AvailableVersionsAsUser returns the newest-first list of chart versions a
// release could be upgraded (or downgraded) to, resolved from its source — the
// matching classic repo's index or, failing that, a registered OCI source. Lets
// the upgrade dialog offer a specific target version instead of only "latest".
// Returns an empty list (not an error) when the source can't be determined; the
// dialog then falls back to latest-only.
func (c *Client) AvailableVersionsAsUser(namespace, name, username string, groups []string) ([]string, error) {
	return c.availableVersions(namespace, name, username, groups)
}

func (c *Client) availableVersions(namespace, name, username string, groups []string) ([]string, error) {
	var actionConfig *action.Configuration
	var err error
	if username != "" {
		actionConfig, err = c.getActionConfigForUser(namespace, username, groups)
	} else {
		actionConfig, err = c.getActionConfig(namespace)
	}
	if err != nil {
		return nil, err
	}

	rel, err := action.NewGet(actionConfig).Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get release: %w", err)
	}
	chartName := rel.Chart.Metadata.Name

	// Resolve the classic repo the same way the upgrade check does, then return
	// that repo's full version list — never a union across repos, which could mix
	// an unrelated same-named chart's versions.
	var candidates []repoVersionInfo
	versionsByRepo := map[string][]string{}
	if f, err := repo.LoadFile(c.settings.RepositoryConfig); err == nil {
		cacheDir := c.settings.RepositoryCache
		for _, r := range f.Repositories {
			idx, err := repo.LoadIndexFile(filepath.Join(cacheDir, fmt.Sprintf("%s-index.yaml", r.Name)))
			if err != nil {
				continue
			}
			entries, ok := idx.Entries[chartName]
			if !ok {
				continue
			}
			latest := ""
			all := make([]string, 0, len(entries))
			hasCurrent := false
			for _, v := range entries {
				all = append(all, v.Version)
				if latest == "" || compareVersions(v.Version, latest) > 0 {
					latest = v.Version
				}
				if v.Version == rel.Chart.Metadata.Version {
					hasCurrent = true
				}
			}
			if latest != "" {
				candidates = append(candidates, repoVersionInfo{repoName: r.Name, repoURL: r.URL, latestVersion: latest, hasCurrentVersion: hasCurrent})
				versionsByRepo[r.Name] = all
			}
		}
	}

	if len(candidates) > 0 {
		sourceHosts := chartSourceHosts(rel.Chart.Metadata.Home, rel.Chart.Metadata.Sources)
		if _, repoName := findBestUpgradeVersion(candidates, sourceHosts); repoName != "" {
			return capVersions(sortVersionsDesc(versionsByRepo[repoName])), nil
		}
		// Ambiguous classic source — don't guess a version list.
		return nil, nil
	}

	return capVersions(c.discoverOCIVersions(chartName)), nil
}

// maxAvailableVersions bounds the version list returned to the upgrade dialog.
// Some charts publish hundreds of versions; the newest N covers realistic upgrade
// targets without an unwieldy dropdown or a large payload. The list is already
// sorted newest-first, so this keeps the most relevant versions.
const maxAvailableVersions = 50

func capVersions(versions []string) []string {
	if len(versions) > maxAvailableVersions {
		return versions[:maxAvailableVersions]
	}
	return versions
}

// sortVersionsDesc returns versions sorted newest-first by semver.
func sortVersionsDesc(versions []string) []string {
	out := slices.Clone(versions)
	sort.SliceStable(out, func(i, j int) bool { return compareVersions(out[i], out[j]) > 0 })
	return out
}

// repoVersionInfo holds version information from a single repository for upgrade comparison.
type repoVersionInfo struct {
	repoName          string
	repoURL           string
	latestVersion     string
	hasCurrentVersion bool
}

// findBestUpgradeVersion picks the upstream repo for a release whose chart name
// may collide across configured repos (e.g. Bitnami ships an `argo-cd` chart
// that's unrelated to argoproj's `argo-cd`). Tiers, in order:
//
//  1. A repo that lists the currently installed version — strongest signal that
//     the release came from there. Ties require source-affinity.
//  2. A repo whose URL host matches source-affinity hosts from Home/Sources.
//     Catches the "installed version was pruned from index.yaml" case without
//     letting an unrelated mirror win.
//  3. Single candidate — only one configured repo lists this chart name, so
//     there is nothing to confuse it with.
//
// If none of these apply we return empty strings; the caller surfaces an
// "upstream not detected" state rather than guessing.
func findBestUpgradeVersion(candidates []repoVersionInfo, sourceHosts []string) (latestVersion, repoName string) {
	var currentMatches []repoVersionInfo
	for _, c := range candidates {
		if c.hasCurrentVersion {
			currentMatches = append(currentMatches, c)
		}
	}
	if len(currentMatches) == 1 {
		return currentMatches[0].latestVersion, currentMatches[0].repoName
	}
	if len(currentMatches) > 1 {
		return bestSourceAffinityVersion(currentMatches, sourceHosts)
	}

	return bestSourceAffinityVersion(candidates, sourceHosts)
}

func bestSourceAffinityVersion(candidates []repoVersionInfo, sourceHosts []string) (latestVersion, repoName string) {
	if len(sourceHosts) == 0 {
		if len(candidates) == 1 {
			return candidates[0].latestVersion, candidates[0].repoName
		}
		return "", ""
	}

	for _, c := range candidates {
		if !repoURLMatchesAny(c.repoURL, sourceHosts) {
			continue
		}
		if latestVersion == "" || compareVersions(c.latestVersion, latestVersion) > 0 {
			latestVersion = c.latestVersion
			repoName = c.repoName
		}
	}
	if latestVersion == "" && len(candidates) == 1 {
		return candidates[0].latestVersion, candidates[0].repoName
	}
	return latestVersion, repoName
}

// chartSourceHosts builds the host-affinity set for a chart from its declared
// Home and Sources URLs. Some charts declare GitHub source URLs while publishing
// their Helm repo via GitHub Pages, so we also derive `<org>.github.io` from any
// `github.com/<org>/<repo>` URL.
func chartSourceHosts(home string, sources []string) []string {
	urls := make([]string, 0, 1+len(sources))
	if home != "" {
		urls = append(urls, home)
	}
	urls = append(urls, sources...)

	hosts := make([]string, 0, len(urls)*2)
	seen := make(map[string]struct{}, len(urls)*2)
	add := func(h string) {
		if h == "" {
			return
		}
		if _, dup := seen[h]; dup {
			return
		}
		seen[h] = struct{}{}
		hosts = append(hosts, h)
	}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			continue
		}
		h := strings.ToLower(u.Hostname())
		add(h)
		add(registeredDomain(h))
		if h == "github.com" {
			if org := firstPathSegment(u.Path); org != "" {
				add(org + ".github.io")
			}
		}
	}
	return hosts
}

// markCurrentVersion returns a copy of base with hasCurrentVersion set on
// each candidate whose repo's index lists installedVersion. The copy matters:
// multiple releases share the base slice (indexed by chart name), so mutating
// it would leak one release's flags onto another with the same chart name.
func markCurrentVersion(base []repoVersionInfo, versionsByRepo map[string][]string, installedVersion string) []repoVersionInfo {
	out := slices.Clone(base)
	for i := range out {
		if slices.Contains(versionsByRepo[out[i].repoName], installedVersion) {
			out[i].hasCurrentVersion = true
		}
	}
	return out
}

// firstPathSegment returns the first non-empty path segment lowercased,
// e.g. "/argoproj/argo-helm" → "argoproj".
func firstPathSegment(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.Index(p, "/"); i > 0 {
		return strings.ToLower(p[:i])
	}
	return strings.ToLower(p)
}

// repoURLMatchesAny is coarse on purpose: reject unrelated mirrors, not
// RFC-correct domain matching.
func repoURLMatchesAny(repoURL string, hosts []string) bool {
	if repoURL == "" || len(hosts) == 0 {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Host == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	candidates := []string{h}
	if reg := registeredDomain(h); reg != "" && reg != h {
		candidates = append(candidates, reg)
	}
	for _, c := range candidates {
		for _, want := range hosts {
			if c == want {
				return true
			}
		}
	}
	return false
}

// multiTenantSuffixes are two-label hosts where the registered-domain
// fallback would produce false positives (every project hosts on the same
// suffix). We treat the full host as the matching unit instead.
var multiTenantSuffixes = map[string]bool{
	"github.io": true,
	"gitlab.io": true,
}

// registeredDomain returns the last two host labels (e.g. "charts.bitnami.com"
// → "bitnami.com"), used as a fallback for source-affinity matching. Returns
// "" for IP literals and for known multi-tenant suffixes (github.io etc.)
// where the last two labels would collapse unrelated projects together.
func registeredDomain(host string) string {
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	candidate := parts[len(parts)-2] + "." + parts[len(parts)-1]
	if multiTenantSuffixes[candidate] {
		return ""
	}
	return candidate
}

// compareVersions compares two semver strings
// Returns: 1 if v1 > v2, -1 if v1 < v2, 0 if equal
func compareVersions(v1, v2 string) int {
	sv1, err1 := semver.NewVersion(v1)
	sv2, err2 := semver.NewVersion(v2)

	// If both parse, use proper semver comparison (handles prereleases correctly)
	if err1 == nil && err2 == nil {
		return sv1.Compare(sv2)
	}

	// Fallback: lexicographic comparison for non-semver strings
	if v1 > v2 {
		return 1
	}
	if v1 < v2 {
		return -1
	}
	return 0
}

// Rollback rolls back a release to a previous revision
func (c *Client) Rollback(namespace, name string, revision int) error {
	return c.RollbackWithProgress(namespace, name, revision, nil)
}

// RollbackWithProgress rolls back a release with progress reporting via a channel.
// If progressCh is nil, progress messages are silently discarded.
func (c *Client) RollbackWithProgress(namespace, name string, revision int, progressCh chan<- InstallProgress) error {
	sendProgress := func(phase, message, detail string) {
		if progressCh == nil {
			return
		}
		select {
		case progressCh <- InstallProgress{Phase: phase, Message: message, Detail: detail}:
		default:
		}
	}

	sendProgress("preparing", fmt.Sprintf("Preparing rollback of %s to revision %d...", name, revision), "")

	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}
	sendProgress("rolling-back", fmt.Sprintf("Rolling back %s to revision %d...", name, revision), "")
	if err := c.rollbackWith(actionConfig, name, revision); err != nil {
		return err
	}
	sendProgress("complete", fmt.Sprintf("Successfully rolled back %s to revision %d", name, revision), "")
	return nil
}

// RollbackAsUser performs a rollback with K8s impersonation.
func (c *Client) RollbackAsUser(namespace, name string, revision int, username string, groups []string) error {
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	return c.rollbackWith(actionConfig, name, revision)
}

func (c *Client) rollbackWith(actionConfig *action.Configuration, name string, revision int) error {
	rollbackAction := action.NewRollback(actionConfig)
	rollbackAction.Version = revision
	rollbackAction.Timeout = 120 * time.Second

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
	return c.uninstallWith(actionConfig, name)
}

// UninstallAsUser removes a release with K8s impersonation.
func (c *Client) UninstallAsUser(namespace, name string, username string, groups []string) error {
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	return c.uninstallWith(actionConfig, name)
}

func (c *Client) uninstallWith(actionConfig *action.Configuration, name string) error {
	uninstallAction := action.NewUninstall(actionConfig)
	uninstallAction.Timeout = 120 * time.Second

	_, err := uninstallAction.Run(name)
	if err != nil {
		return fmt.Errorf("uninstall failed: %w", err)
	}

	return nil
}

// Upgrade upgrades a release to a new version
func (c *Client) Upgrade(namespace, name, targetVersion, repositoryName string) error {
	return c.UpgradeWithProgress(namespace, name, targetVersion, repositoryName, nil)
}

// UpgradeWithProgress upgrades a release with progress reporting via a channel.
// If progressCh is nil, progress messages are silently discarded.
func (c *Client) UpgradeWithProgress(namespace, name, targetVersion, repositoryName string, progressCh chan<- InstallProgress) error {
	sendProgress := progressSender(progressCh)
	sendProgress("preparing", fmt.Sprintf("Getting current release %s...", name), "")

	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}
	return c.upgradeWith(actionConfig, name, targetVersion, repositoryName, sendProgress)
}

// UpgradeWithProgressAsUser upgrades a release with K8s impersonation and progress reporting.
func (c *Client) UpgradeWithProgressAsUser(namespace, name, targetVersion, repositoryName, username string, groups []string, progressCh chan<- InstallProgress) error {
	sendProgress := progressSender(progressCh)
	sendProgress("preparing", fmt.Sprintf("Getting current release %s...", name), "")

	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	return c.upgradeWith(actionConfig, name, targetVersion, repositoryName, sendProgress)
}

// UpgradeAsUser upgrades a release with K8s impersonation.
func (c *Client) UpgradeAsUser(namespace, name, targetVersion, repositoryName string, username string, groups []string) error {
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	noop := func(phase, message, detail string) {}
	return c.upgradeWith(actionConfig, name, targetVersion, repositoryName, noop)
}

func progressSender(progressCh chan<- InstallProgress) func(phase, message, detail string) {
	return func(phase, message, detail string) {
		if progressCh == nil {
			return
		}
		select {
		case progressCh <- InstallProgress{Phase: phase, Message: message, Detail: detail}:
		default:
		}
	}
}

func (c *Client) upgradeWith(actionConfig *action.Configuration, name, targetVersion, repositoryName string, sendProgress func(phase, message, detail string)) error {
	// First, get the current release to find chart info
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return fmt.Errorf("failed to get current release: %w", err)
	}

	chartName := rel.Chart.Metadata.Name
	sendProgress("resolving", fmt.Sprintf("Finding %s version %s in repositories...", chartName, targetVersion), "")

	chartPath, resolvedRepo, err := c.resolveUpgradeChartPath(chartName, targetVersion, repositoryName, chartSourceHosts(rel.Chart.Metadata.Home, rel.Chart.Metadata.Sources))
	if err != nil {
		return err
	}

	sendProgress("downloading", fmt.Sprintf("Downloading %s-%s from %s...", chartName, targetVersion, resolvedRepo), chartPath)

	// Create upgrade action — don't use Wait=true because Radar already
	// shows real-time resource status via SSE. Waiting blocks the dialog
	// for minutes with zero feedback; users can monitor the rollout in the UI.
	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = rel.Namespace
	upgradeAction.Timeout = 120 * time.Second
	// Reset to the new chart's defaults, then re-merge the user's previously-supplied
	// values on top — preserves their overrides while picking up the new chart's new
	// default keys. Plain ReuseValues keeps the old merged values and can render nil
	// for keys a newer chart added (a cross-version upgrade footgun).
	upgradeAction.ResetThenReuseValues = true

	// Use ChartPathOptions to locate/download the chart
	client := action.NewInstall(actionConfig)
	client.Version = targetVersion

	// OCI pulls need a registry client on the action; Radar's action config
	// doesn't carry one by default. Wire it from the user's helm registry login.
	if registry.IsOCI(chartPath) {
		rc, err := c.newRegistryClientConcrete()
		if err != nil {
			return fmt.Errorf("failed to build OCI registry client: %w", err)
		}
		client.SetRegistryClient(rc)
	}

	cp, err := client.ChartPathOptions.LocateChart(chartPath, c.settings)
	if err != nil {
		return fmt.Errorf("failed to locate chart: %w", err)
	}

	sendProgress("loading", "Loading chart...", cp)

	chart, err := loader.Load(cp)
	if err != nil {
		return fmt.Errorf("failed to load chart: %w", err)
	}

	// Refuse a silent chart-swap: the resolved source must publish the SAME chart
	// the release runs, not merely a chart at the same version. Matters most for
	// OCI prefix probing, where "<prefix>/<chartName>" is derived, not asserted.
	if chart.Metadata != nil && chart.Metadata.Name != chartName {
		return fmt.Errorf("resolved chart is %q but release %q runs chart %q — refusing to swap charts", chart.Metadata.Name, name, chartName)
	}

	sendProgress("upgrading", fmt.Sprintf("Applying %s %s...", chartName, targetVersion), "")

	// Run the upgrade
	_, err = upgradeAction.Run(name, chart, rel.Config)
	if err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	sendProgress("complete", fmt.Sprintf("Successfully upgraded %s to %s", name, targetVersion), "")
	return nil
}

type chartPathCandidate struct {
	repoName  string
	repoURL   string
	chartPath string
}

func (c *Client) resolveUpgradeChartPath(chartName, targetVersion, repositoryName string, sourceHosts []string) (chartPath, resolvedRepo string, err error) {
	// A missing/unreadable repo config is not fatal: a pure-OCI user has no
	// repositories.yaml, and discovery may have advertised an OCI upgrade. Proceed
	// with an empty classic set so the OCI fallback below can still resolve.
	repos, err := repo.LoadFile(c.settings.RepositoryConfig)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[helm] failed to load repository config during upgrade (treating as no classic repos): %v", err)
		}
		repos = &repo.File{}
	}

	var candidates []chartPathCandidate
	var indexErrors []string
	for _, r := range repos.Repositories {
		if repositoryName != "" && r.Name != repositoryName {
			continue
		}

		indexPath := filepath.Join(c.settings.RepositoryCache, r.Name+"-index.yaml")
		idx, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			log.Printf("[helm] skipping repo %q during upgrade: failed to load index %s: %v", r.Name, indexPath, err)
			indexErrors = append(indexErrors, fmt.Sprintf("%s: %v", r.Name, err))
			continue
		}

		if entries, ok := idx.Entries[chartName]; ok {
			for _, entry := range entries {
				if entry.Version != targetVersion || len(entry.URLs) == 0 {
					continue
				}
				path := entry.URLs[0]
				if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
					path = strings.TrimSuffix(r.URL, "/") + "/" + path
				}
				candidates = append(candidates, chartPathCandidate{repoName: r.Name, repoURL: r.URL, chartPath: path})
				break
			}
		}
	}

	if len(candidates) == 1 {
		return candidates[0].chartPath, candidates[0].repoName, nil
	}
	if len(candidates) > 1 {
		var sourceMatches []chartPathCandidate
		for _, candidate := range candidates {
			if repoURLMatchesAny(candidate.repoURL, sourceHosts) {
				sourceMatches = append(sourceMatches, candidate)
			}
		}
		if len(sourceMatches) == 1 {
			return sourceMatches[0].chartPath, sourceMatches[0].repoName, nil
		}
		return "", "", fmt.Errorf("could not identify upstream chart repository for %s version %s", chartName, targetVersion)
	}

	if repositoryName != "" {
		if len(indexErrors) > 0 {
			return "", "", fmt.Errorf("failed to load Helm repository index for %s: %s", repositoryName, strings.Join(indexErrors, "; "))
		}
		return "", "", fmt.Errorf("chart %s version %s not found in repository %s", chartName, targetVersion, repositoryName)
	}

	// Surface classic-repo index failures before the OCI fallback: a stale/missing
	// index is a "fix your repo" condition the user should see, not something to
	// silently paper over by pulling the same version from an OCI source.
	if len(indexErrors) > 0 {
		return "", "", fmt.Errorf("chart %s version %s not found in configured repositories; failed to load indexes: %s", chartName, targetVersion, strings.Join(indexErrors, "; "))
	}

	// No classic-repo match and indexes loaded cleanly. Fall back to registered OCI
	// sources — the server re-derives the oci:// ref from a configured prefix (never
	// a client-supplied ref), keeping the upgrade path configured-only.
	if url, ok := c.resolveOCIUpgradeURL(chartName, targetVersion); ok {
		return url, "oci", nil
	}

	return "", "", fmt.Errorf("chart %s version %s not found in configured repositories or registered OCI sources", chartName, targetVersion)
}

// BatchCheckUpgrades checks for upgrades for all releases at once (more efficient)
func (c *Client) BatchCheckUpgrades(namespace string) (*BatchUpgradeInfo, error) {
	return c.batchCheckUpgrades(namespace, "", nil)
}

// BatchCheckUpgradesAsUser is BatchCheckUpgrades with K8s impersonation on
// the release listing (the repo index reads are local-file only and don't
// touch K8s).
func (c *Client) BatchCheckUpgradesAsUser(namespace, username string, groups []string) (*BatchUpgradeInfo, error) {
	return c.batchCheckUpgrades(namespace, username, groups)
}

// BatchCheckUpgradesAcrossNamespaces is BatchCheckUpgradesAsUser over an explicit
// set of namespaces, merging the per-namespace maps. A nil slice means
// "cluster-wide". Mirrors ListReleasesAcrossNamespaces so the Helm view's
// upgrade checks degrade the same way for namespace-restricted identities. Keys
// are "storageNamespace/name" and namespaces are queried once each, so the merge
// can't collide.
//
// Upgrade info is best-effort enrichment layered on top of the release list, so
// forbidden namespaces are skipped and an all-forbidden result returns an empty
// map rather than an error — the release list itself is what surfaces the 403.
func (c *Client) BatchCheckUpgradesAcrossNamespaces(namespaces []string, username string, groups []string) (*BatchUpgradeInfo, error) {
	if namespaces == nil {
		return c.BatchCheckUpgradesAsUser("", username, groups)
	}
	merged := &BatchUpgradeInfo{Releases: make(map[string]*UpgradeInfo)}
	for _, ns := range namespaces {
		info, err := c.BatchCheckUpgradesAsUser(ns, username, groups)
		if err != nil {
			if IsForbiddenError(err) {
				continue
			}
			return nil, err
		}
		for k, v := range info.Releases {
			merged.Releases[k] = v
		}
	}
	return merged, nil
}

func (c *Client) batchCheckUpgrades(namespace, username string, groups []string) (*BatchUpgradeInfo, error) {
	var actionConfig *action.Configuration
	var err error
	if username != "" {
		actionConfig, err = c.getActionConfigForUser(namespace, username, groups)
	} else {
		actionConfig, err = c.getActionConfig(namespace)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to build helm action config: %w", err)
	}

	// We need full *release.Release objects (Chart.Metadata.Home/Sources are
	// used for source-affinity disambiguation), so call action.NewList here
	// instead of going through ListReleases which projects to HelmRelease.
	listAction := action.NewList(actionConfig)
	listAction.All = true
	listAction.AllNamespaces = namespace == ""
	listAction.StateMask = action.ListAll
	releases, err := listAction.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to list helm releases: %w", err)
	}

	result := &BatchUpgradeInfo{
		Releases: make(map[string]*UpgradeInfo),
	}
	if len(releases) == 0 {
		return result, nil
	}

	storageNamespaces := make(map[string]string, len(releases))
	if namespace == "" {
		storageNamespaces, err = helmReleaseStorageNamespaces(username, groups)
		if err != nil {
			return nil, err
		}
	} else {
		for _, rel := range releases {
			storageNamespaces[releaseStorageKey(rel)] = namespace
		}
	}

	// A missing/unreadable repo config is not fatal: a user may rely solely on
	// registered OCI sources, so we proceed with an empty classic-repo set and let
	// the per-release OCI fallback run rather than failing every release here.
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[helm] failed to load repository config %s (treating as no classic repos): %v", repoFile, err)
		}
		f = &repo.File{}
	}

	// Split into two maps: latest-per-repo drives ranking; per-repo full
	// version lists let us detect whether a release's installed version
	// (which may not be the latest) is present in that repo's index.
	chartRepoVersions := make(map[string][]repoVersionInfo)
	chartAllVersions := make(map[string]map[string][]string)

	cacheDir := c.settings.RepositoryCache
	indexLoadFailed := false
	for _, r := range f.Repositories {
		indexPath := filepath.Join(cacheDir, fmt.Sprintf("%s-index.yaml", r.Name))
		indexFile, err := repo.LoadIndexFile(indexPath)
		if err != nil {
			log.Printf("[helm] skipping repo %q: failed to load index %s: %v", r.Name, indexPath, err)
			indexLoadFailed = true
			continue
		}

		for chartName, versions := range indexFile.Entries {
			if len(versions) == 0 {
				continue
			}
			latestInRepo := versions[0].Version
			var allVersions []string
			for _, v := range versions {
				allVersions = append(allVersions, v.Version)
				if compareVersions(v.Version, latestInRepo) > 0 {
					latestInRepo = v.Version
				}
			}

			chartRepoVersions[chartName] = append(chartRepoVersions[chartName], repoVersionInfo{
				repoName:      r.Name,
				repoURL:       r.URL,
				latestVersion: latestInRepo,
			})
			if chartAllVersions[chartName] == nil {
				chartAllVersions[chartName] = make(map[string][]string)
			}
			chartAllVersions[chartName][r.Name] = allVersions
		}
	}

	// One registry client + tag cache shared across all releases in this batch, so
	// the same OCI ref isn't re-listed per release. Built lazily on first miss so
	// batches with no registered OCI sources pay nothing.
	var ociLister ociTagLister
	ociReady := false
	tagCache := map[string][]string{}
	ociFallback := func(info *UpgradeInfo, chartName, currentVersion string) bool {
		if len(ListOCISources()) == 0 {
			return false
		}
		if !ociReady {
			ociLister = c.newRegistryClient()
			ociReady = true
		}
		if ociLister == nil {
			return false
		}
		return c.applyOCIUpgrade(info, chartName, currentVersion, ociLister, tagCache)
	}

	for _, rel := range releases {
		key := releaseUpgradeKey(rel, storageNamespaces[releaseStorageKey(rel)])
		currentVersion := rel.Chart.Metadata.Version
		chartName := rel.Chart.Metadata.Name
		info := &UpgradeInfo{CurrentVersion: currentVersion}

		baseCandidates, ok := chartRepoVersions[chartName]
		if !ok {
			// A failed classic index could be hiding this release's real source —
			// surface that before OCI rather than mislabel it as OCI-tracked.
			if indexLoadFailed {
				info.Error = "failed to load one or more configured repository indexes"
			} else if !ociFallback(info, chartName, currentVersion) {
				// Genuine absence → OCI fallback for the user's own OCI charts.
				info.Untracked = true
				info.Error = "chart not found in configured repositories or registered OCI sources"
			}
			result.Releases[key] = info
			continue
		}

		candidates := markCurrentVersion(baseCandidates, chartAllVersions[chartName], currentVersion)
		sourceHosts := chartSourceHosts(rel.Chart.Metadata.Home, rel.Chart.Metadata.Sources)
		latestVersion, repoName := findBestUpgradeVersion(candidates, sourceHosts)
		if latestVersion == "" {
			// Classic candidates exist but are ambiguous — don't let OCI override
			// a release that came from a classic repo.
			info.Error = "could not identify upstream chart repository"
		} else {
			info.LatestVersion = latestVersion
			info.RepositoryName = repoName
			info.SourceType = "repository"
			info.UpdateAvailable = compareVersions(latestVersion, currentVersion) > 0
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
	upgradeAction.Namespace = rel.Namespace
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
	return c.applyValuesWith(actionConfig, name, newValues)
}

// ApplyValuesAsUser applies values with K8s impersonation.
func (c *Client) ApplyValuesAsUser(namespace, name string, newValues map[string]any, username string, groups []string) error {
	actionConfig, err := c.getActionConfigForUser(namespace, username, groups)
	if err != nil {
		return err
	}
	return c.applyValuesWith(actionConfig, name, newValues)
}

func (c *Client) applyValuesWith(actionConfig *action.Configuration, name string, newValues map[string]any) error {
	// Get the current release to reuse its chart
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return fmt.Errorf("failed to get current release: %w", err)
	}

	// Create upgrade action — no Wait, Radar shows resource status in real-time
	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = rel.Namespace
	upgradeAction.Timeout = 120 * time.Second
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
	return c.installWith(actionConfig, req)
}

// InstallAsUser installs a new Helm release with K8s impersonation.
func (c *Client) InstallAsUser(req *InstallRequest, username string, groups []string) (*HelmRelease, error) {
	actionConfig, err := c.getActionConfigForUser(req.Namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.installWith(actionConfig, req)
}

func (c *Client) installWith(actionConfig *action.Configuration, req *InstallRequest) (*HelmRelease, error) {

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

	mode, err := preInstallCheck(actionConfig, req.ReleaseName, req.Namespace)
	if err != nil {
		return nil, err
	}

	// action.Install carries ChartPathOptions; instantiated here as a locator only.
	locator := action.NewInstall(actionConfig)
	locator.Version = req.Version
	cp, err := locator.ChartPathOptions.LocateChart(chartURL, c.settings)
	if err != nil {
		return nil, fmt.Errorf("failed to locate chart: %w", err)
	}
	chart, err := loader.Load(cp)
	if err != nil {
		return nil, fmt.Errorf("failed to load chart: %w", err)
	}

	if mode != installFresh {
		log.Printf("[helm] install %q/%q: prior release record exists, recovering via %s", req.Namespace, req.ReleaseName, recoveryMode(mode))
	}
	rel, err := runInstallOrUpgrade(actionConfig, req, chart, mode)
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
	actionConfig, err := c.getActionConfig(req.Namespace)
	if err != nil {
		return nil, err
	}
	return c.installWithProgressUsing(actionConfig, req, progressCh)
}

// InstallWithProgressAsUser installs a new Helm release with K8s impersonation and streams progress.
func (c *Client) InstallWithProgressAsUser(req *InstallRequest, progressCh chan<- InstallProgress, username string, groups []string) (*HelmRelease, error) {
	actionConfig, err := c.getActionConfigForUser(req.Namespace, username, groups)
	if err != nil {
		return nil, err
	}
	return c.installWithProgressUsing(actionConfig, req, progressCh)
}

func (c *Client) installWithProgressUsing(actionConfig *action.Configuration, req *InstallRequest, progressCh chan<- InstallProgress) (*HelmRelease, error) {
	sendProgress := func(phase, message, detail string) {
		select {
		case progressCh <- InstallProgress{Phase: phase, Message: message, Detail: detail}:
		default:
			// Channel full or closed, skip
		}
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

	// Pre-flight before downloading: a deployed/pending release is knowable
	// from local Helm storage and we shouldn't waste bandwidth + show
	// "Downloading..." progress to a user who'll get a 409 anyway.
	mode, err := preInstallCheck(actionConfig, req.ReleaseName, req.Namespace)
	if err != nil {
		return nil, err
	}

	sendProgress("downloading", fmt.Sprintf("Downloading chart %s-%s...", req.ChartName, req.Version), chartURL)

	// Download the chart archive directly via HTTP, bypassing the Helm SDK's
	// ChartPathOptions.LocateChart / ChartDownloader machinery. That code loads
	// every locally-registered repo's cached index file and fails with "no cached
	// repo found" if any index file is stale or missing (e.g. a bitnami repo
	// entry exists in repositories.yaml but the index cache was deleted).
	chartResp, err := httpClient.Get(chartURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download chart: %w", err)
	}
	defer chartResp.Body.Close()
	if chartResp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to download chart: server returned %d", chartResp.StatusCode)
	}

	tmpChart, err := os.CreateTemp("", "helm-chart-*.tgz")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file for chart: %w", err)
	}
	defer os.Remove(tmpChart.Name())
	defer tmpChart.Close()

	if _, err := tmpChart.ReadFrom(chartResp.Body); err != nil {
		return nil, fmt.Errorf("failed to write chart to temp file: %w", err)
	}
	tmpChart.Close()

	sendProgress("loading", "Loading chart...", tmpChart.Name())

	chart, err := loader.Load(tmpChart.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to load chart: %w", err)
	}

	switch mode {
	case installFresh:
		sendProgress("installing", fmt.Sprintf("Installing %s to namespace %s...", req.ReleaseName, req.Namespace), "")
		if req.CreateNamespace {
			sendProgress("installing", fmt.Sprintf("Creating namespace %s if needed...", req.Namespace), "")
		}
	case installReplace:
		sendProgress("installing", fmt.Sprintf("Replacing prior uninstalled release %s in %s...", req.ReleaseName, req.Namespace), "")
	case installUpgrade:
		sendProgress("installing", fmt.Sprintf("Recovering prior failed release %s in %s...", req.ReleaseName, req.Namespace), "")
	}

	rel, err := runInstallOrUpgrade(actionConfig, req, chart, mode)
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
	// Build query URL (escape user input to prevent query string injection)
	searchURL := fmt.Sprintf("%s/packages/search?kind=0&ts_query_web=%s&offset=%d&limit=%d",
		artifactHubBaseURL, url.QueryEscape(query), offset, limit)

	// Add sort parameter (ArtifactHub uses "sort" query param)
	if sort != "" && sort != "relevance" {
		searchURL += "&sort=" + url.QueryEscape(sort)
	}

	// Add filters
	if official {
		searchURL += "&official=true"
	}
	if verified {
		searchURL += "&verified_publisher=true"
	}

	// Make HTTP request
	resp, err := httpClient.Get(searchURL)
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
