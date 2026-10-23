package helm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/helmhistory"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/repo"
	corev1 "k8s.io/api/core/v1"
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

// Repository credentials may also appear in URL paths, so hide the whole URL.
var repositoryRefreshErrorURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"<>]*`)

// HTTP client for ArtifactHub requests
var httpClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if !sameHTTPOrigin(via[0].URL, req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
		return nil
	},
	Timeout: 30 * time.Second,
}

// Client provides access to Helm releases
type Client struct {
	mu         sync.RWMutex
	settings   *cli.EnvSettings
	kubeconfig string
	// chartPathLocator is a test seam for chart transport. Production callers
	// leave it nil and use locateChartPath, which wires Helm's registry client
	// for OCI references.
	chartPathLocator func(*action.Configuration, string, string) (string, error)
	// restConfig, when set, is the explicit rest.Config all actions target —
	// used by callers that resolve the cluster themselves before Radar's k8s
	// singleton is up (the CLI install driver), so Helm can't diverge onto a
	// different kubeconfig current-context than the caller's own client.
	restConfig *rest.Config
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

// InitializeWithRESTConfig sets up the global Helm client to operate against an
// explicit rest.Config. Used by the CLI install driver, which resolves the
// target cluster itself (before Radar's k8s singleton is initialized) — this
// guarantees Helm targets the SAME cluster the caller's kube client does, rather
// than falling through to a possibly-divergent kubeconfig current-context.
func InitializeWithRESTConfig(restCfg *rest.Config) error {
	clientOnce.Do(func() {
		ensureHelmWritablePaths()
		globalClient = &Client{
			settings:   cli.New(),
			restConfig: restCfg,
		}
	})
	return nil
}

// GetClient returns the global Helm client
func GetClient() *Client {
	return globalClient
}

// NewStandaloneClient returns a Helm client bound to an explicit rest.Config,
// independent of the process-global client. Every action configuration it
// builds targets exactly this config (restConfig wins in restClientGetter), so
// a later kubeconfig context switch in the host process cannot retarget its
// writes — the property the Cloud install flow depends on while a prepared
// install waits for Hub approval.
func NewStandaloneClient(restCfg *rest.Config) *Client {
	ensureHelmWritablePaths()
	return &Client{
		settings:   cli.New(),
		restConfig: rest.CopyConfig(restCfg),
	}
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
	return c.buildActionConfig(namespace, nil)
}

// getActionConfigForUser creates an action configuration with K8s impersonation set.
// Used for write operations when auth is enabled.
func (c *Client) getActionConfigForUser(namespace string, user *pkgauth.User) (*action.Configuration, error) {
	return c.buildActionConfig(namespace, user)
}

// buildActionConfig is the shared init path for both anonymous and
// impersonated action configurations. When kubeconfig is empty (running
// in-cluster) we hand Helm an in-cluster RESTClientGetter built from the
// rest.Config the rest of Radar already uses — Helm's default
// ConfigFlags only resolves kubeconfig and would otherwise fall through
// to localhost:8080 inside a pod with no ~/.kube/config.
func (c *Client) buildActionConfig(namespace string, user *pkgauth.User) (*action.Configuration, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	actionConfig := new(action.Configuration)

	getter, err := c.restClientGetter(namespace, user)
	if err != nil {
		return nil, fmt.Errorf("failed to build helm RESTClientGetter: %w", err)
	}

	if err := actionConfig.Init(getter, namespace, "secrets", log.Printf); err != nil {
		if user != nil {
			return nil, fmt.Errorf("failed to initialize helm action config for user %s: %w", user.Username, err)
		}
		return nil, fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	return actionConfig, nil
}

// restClientGetter picks the RESTClientGetter strategy for this client.
// Caller must hold c.mu (read or write). Reads global k8s package state
// (rest.Config, current context); pure logic lives in
// buildRESTClientGetter so it can be tested without those globals.
func (c *Client) restClientGetter(namespace string, user *pkgauth.User) (genericclioptions.RESTClientGetter, error) {
	// An explicit restConfig (CLI install driver) wins and forces the
	// restConfig getter path (kubeconfig empty), so Helm targets exactly the
	// cluster the caller resolved.
	restConfig := c.restConfig
	kubeconfig := c.kubeconfig
	if restConfig != nil {
		kubeconfig = ""
	} else {
		restConfig = k8s.GetConfig()
	}
	return buildRESTClientGetter(restClientGetterParams{
		kubeconfig:     kubeconfig,
		restConfig:     restConfig,
		currentContext: k8s.GetContextName(),
		namespace:      namespace,
		user:           user,
	})
}

type restClientGetterParams struct {
	kubeconfig     string
	restConfig     *rest.Config
	currentContext string
	namespace      string
	user           *pkgauth.User
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
	// A user token can't ride kubeconfig flags (they'd add the kubeconfig's
	// own credentials), so user-token mode always uses the resolved config.
	if p.kubeconfig == "" || (p.user != nil && pkgauth.UserTokenMode()) {
		if p.restConfig != nil {
			return newRESTConfigGetter(p.restConfig, p.namespace, p.user), nil
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

	if p.user != nil {
		configFlags.Impersonate = &p.user.Username
		configFlags.ImpersonateGroup = &p.user.Groups
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
func (c *Client) GetActionConfigForUser(namespace string, user *pkgauth.User) (*action.Configuration, error) {
	return c.getActionConfigForUser(namespace, user)
}

// ListReleasesAsUser is ListReleases with K8s impersonation.
// When username is empty, falls back to the ServiceAccount identity (same
// behavior as ListReleases).
func (c *Client) ListReleasesAsUser(ctx context.Context, namespace string, user *pkgauth.User) ([]HelmRelease, error) {
	if user == nil {
		return c.ListReleases(ctx, namespace)
	}
	contextName := k8s.GetContextName()
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return nil, err
	}
	return c.listReleasesWith(ctx, actionConfig, contextName, namespace, user)
}

// ListReleases returns all Helm releases, optionally filtered by namespace
func (c *Client) ListReleases(ctx context.Context, namespace string) ([]HelmRelease, error) {
	contextName := k8s.GetContextName()
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.listReleasesWith(ctx, actionConfig, contextName, namespace, nil)
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
func (c *Client) ListReleasesAcrossNamespaces(ctx context.Context, namespaces []string, user *pkgauth.User) ([]HelmRelease, error) {
	if namespaces != nil && len(namespaces) == 0 {
		return []HelmRelease{}, nil
	}
	contextName := k8s.GetContextName()
	client, err := c.storageClient(user)
	if err != nil {
		return nil, err
	}
	return listReleasesAcrossNamespacesWithClient(ctx, client, contextName, namespaces, true)
}

// contextName is the cluster context the read started on; a release history
// analysed after a switch away from it records no uninstall start.
func listReleasesAcrossNamespacesWithClient(ctx context.Context, client kubernetes.Interface, contextName string, namespaces []string, includeResourceHealth bool) ([]HelmRelease, error) {
	rows, err := listReleaseRows(ctx, contextName, namespaces, includeResourceHealth, false, ReleaseReadDeniedForCaller, func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
		return helmReleaseStorageSnapshotWithClient(ctx, client, namespace)
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// PartialReadError reports the release storage namespaces whose read failed
// while other namespaces were read. The releases returned with it come from
// those other namespaces.
type PartialReadError struct {
	Namespaces []string
	// Err is the first failure; Errs holds every failure, so callers can tell
	// whether the namespaces failed for one reason or several.
	Err  error
	Errs []error
}

func (e *PartialReadError) Error() string {
	return fmt.Sprintf("failed to read Helm releases in %d namespace(s) (%s): %v", len(e.Namespaces), strings.Join(e.Namespaces, ", "), e.Err)
}

func (e *PartialReadError) Unwrap() error { return e.Err }

// listReleaseRows reads one storage snapshot per namespace scope (nil means
// one cluster-wide read) and builds a row for each release in it. A namespace
// whose read fails with an error that denied matches is skipped. Any other
// failure fails the whole read, unless keepGoing is set: then the remaining
// namespaces are still read, and when at least one namespace was read the
// rows come back with a *PartialReadError naming the namespaces that failed.
func listReleaseRows(ctx context.Context, contextName string, namespaces []string, includeResourceHealth, keepGoing bool, denied func(error) bool, read func(context.Context, string) (*helmReleaseStorageSnapshot, error)) ([]HelmRelease, error) {
	scopes := namespaces
	if scopes == nil {
		scopes = []string{""}
	}
	fluxMap := fluxHelmReleaseMap(ctx)
	all := make([]HelmRelease, 0)
	var lastForbidden, firstFailure error
	var failed []string
	var failures []error
	authorized := false
	for index, ns := range scopes {
		if err := ctx.Err(); err != nil {
			if !keepGoing || !authorized {
				return nil, err
			}
			failed = append(failed, scopes[index:]...)
			failures = append(failures, err)
			if firstFailure == nil {
				firstFailure = err
			}
			break
		}
		snapshot, err := read(ctx, ns)
		if err == nil {
			var rows []HelmRelease
			rows, err = helmReleaseRowsFromStorageSnapshot(ctx, snapshot, fluxMap, contextName, includeResourceHealth)
			if err == nil {
				authorized = true
				all = append(all, rows...)
				continue
			}
		}
		if denied(err) {
			lastForbidden = err
			continue
		}
		if !keepGoing {
			return nil, err
		}
		failed = append(failed, ns)
		failures = append(failures, err)
		if firstFailure == nil {
			firstFailure = err
		}
	}
	if firstFailure != nil {
		if !authorized {
			return nil, firstFailure
		}
		sortHelmReleases(all)
		return all, &PartialReadError{Namespaces: failed, Err: firstFailure, Errs: failures}
	}
	if !authorized && lastForbidden != nil {
		return nil, lastForbidden
	}
	sortHelmReleases(all)
	return all, nil
}

func sortHelmReleases(releases []HelmRelease) {
	sort.Slice(releases, func(i, j int) bool {
		if releases[i].Namespace != releases[j].Namespace {
			return releases[i].Namespace < releases[j].Namespace
		}
		return releases[i].Name < releases[j].Name
	})
}

// ListManifestResourcesAcrossNamespaces returns resource declarations from the
// latest stored manifest of each visible Helm release. It follows the same
// namespace and impersonation rules as ListReleasesAcrossNamespaces.
func (c *Client) ListManifestResourcesAcrossNamespaces(ctx context.Context, namespaces []string, user *pkgauth.User) ([]ReleaseManifestResource, []string, int, error) {
	if namespaces == nil {
		resources, parseErrors, err := c.listManifestResourcesAsUser(ctx, "", user)
		return resources, nil, parseErrors, err
	}
	all := []ReleaseManifestResource{}
	var unavailable []string
	var firstError error
	parseErrors := 0
	for index, namespace := range namespaces {
		if err := ctx.Err(); err != nil {
			unavailable = append(unavailable, namespaces[index:]...)
			return all, unavailable, parseErrors, err
		}
		resources, namespaceParseErrors, err := c.listManifestResourcesAsUser(ctx, namespace, user)
		parseErrors += namespaceParseErrors
		if err != nil {
			unavailable = append(unavailable, namespace)
			if IsForbiddenError(err) {
				continue
			}
			if firstError == nil {
				firstError = err
			}
			continue
		}
		all = append(all, resources...)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ReleaseNamespace != all[j].ReleaseNamespace {
			return all[i].ReleaseNamespace < all[j].ReleaseNamespace
		}
		if all[i].ReleaseName != all[j].ReleaseName {
			return all[i].ReleaseName < all[j].ReleaseName
		}
		return resourceRefKey(ResourceRef{
			Kind: all[i].Resource.Kind, APIVersion: all[i].Resource.APIVersion,
			Name: all[i].Resource.Name, Namespace: all[i].Resource.Namespace,
		}) < resourceRefKey(ResourceRef{
			Kind: all[j].Resource.Kind, APIVersion: all[j].Resource.APIVersion,
			Name: all[j].Resource.Name, Namespace: all[j].Resource.Namespace,
		})
	})
	return all, unavailable, parseErrors, firstError
}

func (c *Client) listManifestResourcesAsUser(ctx context.Context, namespace string, user *pkgauth.User) ([]ReleaseManifestResource, int, error) {
	var actionConfig *action.Configuration
	var err error
	if user == nil {
		actionConfig, err = c.getActionConfig(namespace)
	} else {
		actionConfig, err = c.getActionConfigForUser(namespace, user)
	}
	if err != nil {
		return nil, 0, err
	}
	if err := actionConfig.KubeClient.IsReachable(); err != nil {
		return nil, 0, fmt.Errorf("failed to inspect helm manifests: %w", err)
	}
	client, err := c.storageClient(user)
	if err != nil {
		return nil, 0, err
	}
	snapshot, err := helmReleaseStorageSnapshotWithClient(ctx, client, namespace)
	if err != nil {
		return nil, 0, err
	}
	resources := []ReleaseManifestResource{}
	parseErrors := 0
	for _, rel := range snapshot.latest {
		if err := ctx.Err(); err != nil {
			return resources, parseErrors, err
		}
		if rel == nil {
			continue
		}
		rendered, errors := parseManifestResourceObjects(rel.Manifest, rel.Namespace)
		parseErrors += errors
		for _, resource := range rendered {
			resources = append(resources, ReleaseManifestResource{
				ReleaseName: rel.Name, ReleaseNamespace: rel.Namespace,
				Resource: OwnedResource{Kind: resource.Ref.Kind, APIVersion: resource.Ref.APIVersion, Name: resource.Ref.Name, Namespace: resource.Ref.Namespace},
				Object:   resource.Object,
			})
		}
	}
	return resources, parseErrors, nil
}

func (c *Client) listReleasesWith(ctx context.Context, actionConfig *action.Configuration, contextName, namespace string, user *pkgauth.User) ([]HelmRelease, error) {
	if err := actionConfig.KubeClient.IsReachable(); err != nil {
		return nil, fmt.Errorf("failed to list helm releases: %w", err)
	}

	client, err := c.storageClient(user)
	if err != nil {
		return nil, err
	}
	return listReleasesAcrossNamespacesWithClient(ctx, client, contextName, []string{namespace}, true)
}

func helmReleaseRowsFromStorageSnapshot(ctx context.Context, snapshot *helmReleaseStorageSnapshot, fluxMap map[string]string, contextName string, includeResourceHealth bool) ([]HelmRelease, error) {
	if snapshot == nil {
		return nil, nil
	}
	result := make([]HelmRelease, 0, len(snapshot.latest))
	for _, rel := range snapshot.latest {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		storageNs := snapshot.storageNamespaces[releaseStorageKey(rel)]
		hr := toHelmReleaseSummary(rel, storageNs)
		if includeResourceHealth {
			addResourceHealth(&hr, rel)
		}
		// Match against the release's *actual* storage namespace (the
		// un-normalized value), since toHelmRelease zeroes StorageNamespace
		// when it equals Namespace for compactness.
		effectiveStorage := storageNs
		if effectiveStorage == "" {
			effectiveStorage = rel.Namespace
		}
		historyKey := releaseHistoryKey(rel)
		analysis := analyzeReleaseHistory(contextName, effectiveStorage, rel.Name, rel.Version, snapshot.histories[historyKey], helmhistory.Options{MaxOperations: releaseListMaxOperations})
		hr.LastOperation = analysis.LastOperation
		hr.Operations = analysis.Operations
		hr.ManagedByFluxHelmRelease = applyFluxOwnership(rel.Name, effectiveStorage, fluxMap)
		result = append(result, hr)
	}
	return result, nil
}

// GetReleaseAsUser is GetRelease with K8s impersonation.
// When username is empty, falls back to the ServiceAccount identity.
func (c *Client) GetReleaseAsUser(namespace, name string, user *pkgauth.User) (*HelmReleaseDetail, error) {
	if user == nil {
		return c.GetRelease(namespace, name)
	}
	contextName := k8s.GetContextName()
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return nil, err
	}
	return getReleaseWith(actionConfig, contextName, namespace, name)
}

// GetRelease returns details for a specific release
func (c *Client) GetRelease(namespace, name string) (*HelmReleaseDetail, error) {
	contextName := k8s.GetContextName()
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return getReleaseWith(actionConfig, contextName, namespace, name)
}

func getReleaseWith(actionConfig *action.Configuration, contextName, namespace, name string) (*HelmReleaseDetail, error) {
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
	effectiveStorage := namespace
	if effectiveStorage == "" {
		effectiveStorage = rel.Namespace
	}
	analysis := analyzeReleaseHistory(contextName, effectiveStorage, rel.Name, rel.Version, revisions, helmhistory.Options{})

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
func (c *Client) GetManifestAsUser(namespace, name string, revision int, user *pkgauth.User) (string, error) {
	if user == nil {
		return c.GetManifest(namespace, name, revision)
	}
	actionConfig, err := c.getActionConfigForUser(namespace, user)
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
func (c *Client) GetValuesAsUser(namespace, name string, allValues bool, user *pkgauth.User) (*HelmValues, error) {
	return c.GetValuesRevisionAsUser(namespace, name, allValues, 0, user)
}

// GetValuesRevisionAsUser is GetValuesRevision with K8s impersonation.
func (c *Client) GetValuesRevisionAsUser(namespace, name string, allValues bool, revision int, user *pkgauth.User) (*HelmValues, error) {
	if user == nil {
		return c.GetValuesRevision(namespace, name, allValues, revision)
	}
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return nil, err
	}
	return getValuesWith(actionConfig, name, allValues, revision)
}

func getValuesWith(actionConfig *action.Configuration, name string, allValues bool, revision int) (*HelmValues, error) {
	getAction := action.NewGet(actionConfig)
	getAction.Version = revision
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get helm release values: %w", err)
	}

	userSupplied := rel.Config
	if userSupplied == nil {
		userSupplied = map[string]any{}
	}
	result := &HelmValues{UserSupplied: userSupplied}

	if allValues {
		computed, err := chartutil.CoalesceValues(rel.Chart, rel.Config)
		if err != nil {
			return nil, fmt.Errorf("failed to compute helm release values: %w", err)
		}
		result.Computed = computed
	}

	return result, nil
}

// GetValuesDiff returns a values diff between two revisions.
func (c *Client) GetValuesDiff(namespace, name string, revision1, revision2 int, allValues bool) (*ValuesDiff, error) {
	return c.getValuesDiff(namespace, name, revision1, revision2, allValues, nil, false)
}

// GetValuesDiffAsUser is GetValuesDiff with K8s impersonation.
func (c *Client) GetValuesDiffAsUser(namespace, name string, revision1, revision2 int, allValues bool, user *pkgauth.User) (*ValuesDiff, error) {
	return c.getValuesDiff(namespace, name, revision1, revision2, allValues, user, false)
}

// GetValuesDiffAsUserRedacted redacts credential values in both revisions before
// diffing for AI callers. The REST/UI diff retains the user's own values.
func (c *Client) GetValuesDiffAsUserRedacted(namespace, name string, revision1, revision2 int, allValues bool, user *pkgauth.User) (*ValuesDiff, error) {
	return c.getValuesDiff(namespace, name, revision1, revision2, allValues, user, true)
}

func (c *Client) getValuesDiff(namespace, name string, revision1, revision2 int, allValues bool, user *pkgauth.User, redact bool) (*ValuesDiff, error) {
	values1, err := c.GetValuesRevisionAsUser(namespace, name, allValues, revision1, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get values for revision %d: %w", revision1, err)
	}
	values2, err := c.GetValuesRevisionAsUser(namespace, name, allValues, revision2, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get values for revision %d: %w", revision2, err)
	}
	var diff string
	if redact {
		diff, err = computeRedactedValuesDiff(values1, values2, revision1, revision2, allValues)
	} else {
		diff, err = computeValuesDiff(values1, values2, revision1, revision2, allValues)
	}
	if err != nil {
		return nil, err
	}
	return &ValuesDiff{Revision1: revision1, Revision2: revision2, AllValues: allValues, Diff: diff}, nil
}

// GetManifestDiff returns the diff between two revisions
func (c *Client) GetManifestDiff(namespace, name string, revision1, revision2 int) (*ManifestDiff, error) {
	return c.getManifestDiff(namespace, name, revision1, revision2, nil, false)
}

// GetManifestDiffAsUser is GetManifestDiff with K8s impersonation.
func (c *Client) GetManifestDiffAsUser(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*ManifestDiff, error) {
	return c.getManifestDiff(namespace, name, revision1, revision2, user, false)
}

// GetManifestDiffAsUserRedacted is GetManifestDiffAsUser with Secret values
// removed, for callers that hand the diff to an AI model. The REST/UI diff
// stays unredacted: it is the user's own view.
func (c *Client) GetManifestDiffAsUserRedacted(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*ManifestDiff, error) {
	return c.getManifestDiff(namespace, name, revision1, revision2, user, true)
}

func (c *Client) getManifestDiff(namespace, name string, revision1, revision2 int, user *pkgauth.User, redact bool) (*ManifestDiff, error) {
	manifest1, err := c.GetManifestAsUser(namespace, name, revision1, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest for revision %d: %w", revision1, err)
	}

	manifest2, err := c.GetManifestAsUser(namespace, name, revision2, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest for revision %d: %w", revision2, err)
	}

	// Compute unified diff
	var diff string
	if redact {
		diff = computeRedactedManifestDiff(manifest1, manifest2, revision1, revision2)
	} else {
		diff = computeDiff(manifest1, manifest2, revision1, revision2)
	}

	return &ManifestDiff{
		Revision1: revision1,
		Revision2: revision2,
		Diff:      diff,
	}, nil
}

// GetNotesDiff returns a release notes diff between two revisions.
func (c *Client) GetNotesDiff(namespace, name string, revision1, revision2 int) (*NotesDiff, error) {
	return c.getNotesDiff(namespace, name, revision1, revision2, nil)
}

// GetNotesDiffAsUser is GetNotesDiff with K8s impersonation.
func (c *Client) GetNotesDiffAsUser(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*NotesDiff, error) {
	return c.getNotesDiff(namespace, name, revision1, revision2, user)
}

func (c *Client) getNotesDiff(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*NotesDiff, error) {
	rel1, err := c.getReleaseRevisionAsUser(namespace, name, revision1, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision1, err)
	}
	rel2, err := c.getReleaseRevisionAsUser(namespace, name, revision2, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision2, err)
	}
	return &NotesDiff{
		Revision1: revision1,
		Revision2: revision2,
		Diff:      computeDiff(releaseNotes(rel1), releaseNotes(rel2), revision1, revision2),
	}, nil
}

// computeRedactedManifestDiff diffs two manifests with every Secret's data and
// stringData values replaced by [REDACTED], so key additions and removals
// still show. RedactSecrets then runs over the diff as a second layer, for
// credentials that sit outside a Secret.
func computeRedactedManifestDiff(manifest1, manifest2 string, rev1, rev2 int) string {
	diff := computeDiff(redactSecretManifests(manifest1), redactSecretManifests(manifest2), rev1, rev2)
	return aicontext.RedactSecrets(diff)
}

// manifestSeparator matches the document separators releaseutil.SplitManifests
// splits on.
var manifestSeparator = regexp.MustCompile(`(?:^|\s*\n)---\s*`)

// redactSecretManifests rewrites the documents of a rendered manifest that can
// carry Secret values and leaves every other document byte-for-byte untouched.
func redactSecretManifests(manifest string) string {
	seps := manifestSeparator.FindAllStringIndex(manifest, -1)
	var out strings.Builder
	start := 0
	for _, sep := range append(seps, []int{len(manifest), len(manifest)}) {
		out.WriteString(redactSecretDocument(manifest[start:sep[0]]))
		out.WriteString(manifest[sep[0]:sep[1]])
		start = sep[1]
	}
	return out.String()
}

const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactSecretDocument masks Secret values in one manifest document. A document
// that doesn't parse is withheld whole: the diff goes to an AI model, and a
// regex can't tell a malformed Secret from anything else.
func redactSecretDocument(doc string) string {
	if strings.TrimSpace(stripYAMLComments(doc)) == "" {
		return doc
	}
	var obj map[string]any
	if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
		return "# [REDACTED: unparseable manifest document]"
	}
	if !redactSecretObject(obj, "") {
		return doc
	}
	b, err := yaml.Marshal(obj)
	if err != nil {
		return "# [REDACTED: unparseable manifest document]"
	}
	// Keep the "# Source:" comment Helm puts above each document.
	var comments strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(line), "# Source: ") {
			comments.WriteString(line + "\n")
		}
	}
	return comments.String() + strings.TrimSuffix(string(b), "\n")
}

// redactSecretObject masks Secret data and stringData values, drops the
// last-applied annotation (a full copy of the object, Secret values included)
// and descends into List items. It reports whether anything changed.
func redactSecretObject(obj map[string]any, defaultKind string) bool {
	kind, _ := obj["kind"].(string)
	if kind == "" {
		kind = defaultKind
	}
	changed := false
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if ann, ok := meta["annotations"].(map[string]any); ok {
			if _, has := ann[lastAppliedAnnotation]; has {
				delete(ann, lastAppliedAnnotation)
				changed = true
			}
		}
	}
	if kind == "Secret" {
		for _, field := range []string{"data", "stringData"} {
			values, ok := obj[field].(map[string]any)
			if !ok {
				if obj[field] != nil {
					obj[field] = "[REDACTED]"
					changed = true
				}
				continue
			}
			for k := range values {
				values[k] = "[REDACTED]"
				changed = true
			}
		}
	}
	if items, ok := obj["items"].([]any); ok {
		itemKind := ""
		if kind == "SecretList" {
			itemKind = "Secret"
		}
		for _, item := range items {
			if m, ok := item.(map[string]any); ok && redactSecretObject(m, itemKind) {
				changed = true
			}
		}
	}
	return changed
}

func stripYAMLComments(doc string) string {
	var b strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func releaseNotes(rel *release.Release) string {
	if rel == nil || rel.Info == nil {
		return ""
	}
	return rel.Info.Notes
}

// GetHooksDiff returns a hook metadata diff between two revisions.
func (c *Client) GetHooksDiff(namespace, name string, revision1, revision2 int) (*HooksDiff, error) {
	return c.getHooksDiff(namespace, name, revision1, revision2, nil)
}

// GetHooksDiffAsUser is GetHooksDiff with K8s impersonation.
func (c *Client) GetHooksDiffAsUser(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*HooksDiff, error) {
	return c.getHooksDiff(namespace, name, revision1, revision2, user)
}

func (c *Client) getHooksDiff(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*HooksDiff, error) {
	rel1, err := c.getReleaseRevisionAsUser(namespace, name, revision1, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision1, err)
	}
	rel2, err := c.getReleaseRevisionAsUser(namespace, name, revision2, user)
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
	return c.getResourceDiff(namespace, name, revision1, revision2, nil)
}

// GetResourceDiffAsUser is GetResourceDiff with K8s impersonation.
func (c *Client) GetResourceDiffAsUser(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*ResourceDiff, error) {
	return c.getResourceDiff(namespace, name, revision1, revision2, user)
}

func (c *Client) getResourceDiff(namespace, name string, revision1, revision2 int, user *pkgauth.User) (*ResourceDiff, error) {
	rel1, err := c.getReleaseRevisionAsUser(namespace, name, revision1, user)
	if err != nil {
		return nil, fmt.Errorf("failed to get release revision %d: %w", revision1, err)
	}
	rel2, err := c.getReleaseRevisionAsUser(namespace, name, revision2, user)
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

func (c *Client) getReleaseRevisionAsUser(namespace, name string, revision int, user *pkgauth.User) (*release.Release, error) {
	var (
		actionConfig *action.Configuration
		err          error
	)
	if user == nil {
		actionConfig, err = c.getActionConfig(namespace)
	} else {
		actionConfig, err = c.getActionConfigForUser(namespace, user)
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

func computeRedactedValuesDiff(values1, values2 *HelmValues, rev1, rev2 int, allValues bool) (string, error) {
	if allValues {
		aicontext.RedactHelmValues(values1.Computed)
		aicontext.RedactHelmValues(values2.Computed)
	} else {
		aicontext.RedactHelmValues(values1.UserSupplied)
		aicontext.RedactHelmValues(values2.UserSupplied)
	}
	return computeValuesDiff(values1, values2, rev1, rev2, allValues)
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
	hr := toHelmReleaseSummary(rel, storageNamespace)
	addResourceHealth(&hr, rel)
	return hr
}

func toHelmReleaseSummary(rel *release.Release, storageNamespace string) HelmRelease {
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
	return hr
}

// addResourceHealth computes health from the resources the release's
// manifest declares.
func addResourceHealth(hr *HelmRelease, rel *release.Release) {
	resources := parseManifestResources(rel.Manifest, rel.Namespace)
	enrichResourcesWithStatus(resources)
	hr.ResourceHealth, hr.HealthIssue, hr.HealthSummary = computeResourceHealth(resources)
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
	return fluxHelmReleaseKeys(hrs)
}

func fluxHelmReleaseKeys(hrs []*unstructured.Unstructured) map[string]string {
	out := make(map[string]string, len(hrs))
	for _, hr := range hrs {
		// A release installed through spec.kubeConfig lives in another
		// cluster's Helm storage; a same-named local release is not Flux's.
		if !gitops.FluxTargetsLocalCluster(hr) {
			continue
		}
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
	deployed          map[string]*release.Release
	storageNamespaces map[string]string
	histories         map[string][]HelmRevision
	latest            []*release.Release
}

// storageClient reads Helm release Secrets on the same cluster as c's action
// configurations: the explicit rest.Config when c has one, else Radar's
// current cluster.
func (c *Client) storageClient(user *pkgauth.User) (kubernetes.Interface, error) {
	c.mu.RLock()
	restConfig := c.restConfig
	c.mu.RUnlock()
	if restConfig != nil {
		var client kubernetes.Interface
		var err error
		if user != nil {
			client, err = kubernetes.NewForConfig(k8s.UserRESTConfig(restConfig, user))
		} else {
			client, err = kubernetes.NewForConfig(restConfig)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to build client for release storage lookup: %w", err)
		}
		return client, nil
	}
	client := k8s.GetClientInterface()
	if user != nil {
		impersonated, err := k8s.UserClient(user)
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

// helmReleaseStorageSnapshotWithClient reads every Helm release Secret in
// namespace ("" for all). The list carries each revision's full encoded
// release, so on clusters with many or large releases it is the slowest read
// Radar makes; ctx bounds both the read and the decoding.
func helmReleaseStorageSnapshotWithClient(ctx context.Context, client kubernetes.Interface, namespace string) (*helmReleaseStorageSnapshot, error) {
	secrets, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect release storage namespaces: %w", err)
	}
	return helmReleaseStorageSnapshotFromSecrets(ctx, secrets.Items)
}

// helmReleaseStorageSnapshotFromSecrets decodes release Secrets and groups
// their revisions by release.
func helmReleaseStorageSnapshotFromSecrets(ctx context.Context, secrets []corev1.Secret) (*helmReleaseStorageSnapshot, error) {
	snapshot := &helmReleaseStorageSnapshot{
		storageNamespaces: make(map[string]string, len(secrets)),
		histories:         make(map[string][]HelmRevision),
	}
	latestByRelease := make(map[string]*release.Release)
	for _, secret := range secrets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		encoded := secret.Data["release"]
		if len(encoded) == 0 {
			continue
		}
		rel, err := decodeHelmReleaseData(string(encoded))
		if err != nil {
			log.Printf("[helm] failed to decode release secret %s/%s: %v", secret.Namespace, secret.Name, err)
			continue
		}
		// Helm stores Release.Labels on the Secret, not in the encoded payload.
		// Restore them like Helm's own List does, so the batch upgrade check sees
		// the recorded chart source.
		rel.Labels = secret.Labels
		snapshot.storageNamespaces[releaseStorageKey(rel)] = secret.Namespace

		historyKey := releaseHistoryKey(rel)
		if rel.Info != nil && rel.Info.Status == release.StatusDeployed {
			if snapshot.deployed == nil {
				snapshot.deployed = make(map[string]*release.Release)
			}
			if previous := snapshot.deployed[historyKey]; previous == nil || rel.Version > previous.Version {
				snapshot.deployed[historyKey] = rel
			}
		}
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
		Deleted:     rel.Info.Deleted.Time,
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
			Deleted:     r.Deleted,
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
	return c.checkForUpgrade(namespace, name, nil)
}

// CheckForUpgradeAsUser is CheckForUpgrade with K8s impersonation on the
// release read.
func (c *Client) CheckForUpgradeAsUser(namespace, name string, user *pkgauth.User) (*UpgradeInfo, error) {
	return c.checkForUpgrade(namespace, name, user)
}

func (c *Client) checkForUpgrade(namespace, name string, user *pkgauth.User) (*UpgradeInfo, error) {
	var actionConfig *action.Configuration
	var err error
	if user != nil {
		actionConfig, err = c.getActionConfigForUser(namespace, user)
	} else {
		actionConfig, err = c.getActionConfig(namespace)
	}
	if err != nil {
		return nil, err
	}

	op, err := releaseReadContext(actionConfig, name)
	if err != nil {
		return nil, err
	}
	rel := op.Latest
	if rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}
	info := &UpgradeInfo{CurrentVersion: rel.Chart.Metadata.Version}
	source, issue, message := upgradeSourceState(op)
	if issue != "" {
		markUpgradeSourceIssue(info, issue, message)
		return info, nil
	}
	if !c.applyRecordedUpgrade(info, *source, rel.Chart.Metadata.Name, rel.Chart.Metadata.Version, nil) {
		markUpgradeSourceIssue(info, UpgradeSourceIssueUnavailable, recordedSourceUnavailableMessage)
	}
	return info, nil
}

func markUpgradeSourceIssue(info *UpgradeInfo, issue UpgradeSourceIssue, message string) {
	info.Error = message
	info.SourceIssue = issue
	info.Untracked = issue == UpgradeSourceIssueUntracked
}

// AvailableVersions is AvailableVersionsAsUser without impersonation.
func (c *Client) AvailableVersions(namespace, name string) ([]string, error) {
	return c.availableVersions(namespace, name, nil)
}

// AvailableVersionsAsUser returns the newest-first list of chart versions a
// release could be upgraded (or downgraded) to, resolved from its source — the
// matching classic repo's index or, failing that, a registered OCI source. Lets
// the upgrade dialog offer a specific target version instead of only "latest".
// Returns an empty list (not an error) when the source can't be determined; the
// dialog then falls back to latest-only.
func (c *Client) AvailableVersionsAsUser(namespace, name string, user *pkgauth.User) ([]string, error) {
	return c.availableVersions(namespace, name, user)
}

func (c *Client) availableVersions(namespace, name string, user *pkgauth.User) ([]string, error) {
	var actionConfig *action.Configuration
	var err error
	if user != nil {
		actionConfig, err = c.getActionConfigForUser(namespace, user)
	} else {
		actionConfig, err = c.getActionConfig(namespace)
	}
	if err != nil {
		return nil, err
	}

	op, err := releaseReadContext(actionConfig, name)
	if err != nil {
		return nil, err
	}
	source, issue, _ := upgradeSourceState(op)
	if issue != "" {
		return nil, nil
	}
	return c.sourceVersions(*source, op.Latest.Chart.Metadata.Name)
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
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}
	return c.rollbackWithProgressUsing(actionConfig, name, revision, progressCh)
}

// RollbackWithProgressAsUser is RollbackWithProgress with K8s impersonation.
func (c *Client) RollbackWithProgressAsUser(namespace, name string, revision int, user *pkgauth.User, progressCh chan<- InstallProgress) error {
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	return c.rollbackWithProgressUsing(actionConfig, name, revision, progressCh)
}

func (c *Client) rollbackWithProgressUsing(actionConfig *action.Configuration, name string, revision int, progressCh chan<- InstallProgress) error {
	sendProgress := progressSender(progressCh)
	sendProgress("preparing", fmt.Sprintf("Preparing rollback of %s to revision %d...", name, revision), "")
	sendProgress("rolling-back", fmt.Sprintf("Rolling back %s to revision %d...", name, revision), "")
	if err := c.rollbackWith(actionConfig, name, revision); err != nil {
		return err
	}
	sendProgress("complete", fmt.Sprintf("Successfully rolled back %s to revision %d", name, revision), "")
	return nil
}

// RollbackAsUser performs a rollback with K8s impersonation.
func (c *Client) RollbackAsUser(namespace, name string, revision int, user *pkgauth.User) error {
	return c.RollbackWithProgressAsUser(namespace, name, revision, user, nil)
}

func useReleaseTargetNamespace(cfg *action.Configuration, name string) error {
	rel, err := cfg.Releases.Last(name)
	if err != nil {
		return err
	}
	// Storage retains its namespace; only the Kubernetes object builder changes.
	if client, ok := cfg.KubeClient.(*kube.Client); ok {
		client.Namespace = rel.Namespace
	}
	return nil
}

func (c *Client) rollbackWith(actionConfig *action.Configuration, name string, revision int) error {
	if err := useReleaseTargetNamespace(actionConfig, name); err != nil {
		return err
	}

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
func (c *Client) UninstallAsUser(namespace, name string, user *pkgauth.User) error {
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	return c.uninstallWith(actionConfig, name)
}

func (c *Client) uninstallWith(actionConfig *action.Configuration, name string) error {
	if err := useReleaseTargetNamespace(actionConfig, name); err != nil {
		return err
	}

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
func (c *Client) UpgradeWithProgressAsUser(namespace, name, targetVersion, repositoryName string, user *pkgauth.User, progressCh chan<- InstallProgress) error {
	sendProgress := progressSender(progressCh)
	sendProgress("preparing", fmt.Sprintf("Getting current release %s...", name), "")

	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	return c.upgradeWith(actionConfig, name, targetVersion, repositoryName, sendProgress)
}

// UpgradeAsUser upgrades a release with K8s impersonation.
func (c *Client) UpgradeAsUser(namespace, name, targetVersion, repositoryName string, user *pkgauth.User) error {
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	noop := func(phase, message, detail string) {}
	return c.upgradeWith(actionConfig, name, targetVersion, repositoryName, noop)
}

// UpgradeWithValuesProgress upgrades a release to a target version applying the
// supplied user values (WYSIWYG), reporting progress via a channel.
func (c *Client) UpgradeWithValuesProgress(namespace, name, targetVersion, repositoryName string, newValues map[string]any, progressCh chan<- InstallProgress) error {
	sendProgress := progressSender(progressCh)
	sendProgress("preparing", fmt.Sprintf("Getting current release %s...", name), "")

	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return err
	}
	return c.upgradeWithValues(actionConfig, name, targetVersion, repositoryName, newValues, sendProgress)
}

// UpgradeWithValuesProgressAsUser upgrades a release to a target version applying
// the supplied user values with K8s impersonation and progress reporting.
func (c *Client) UpgradeWithValuesProgressAsUser(namespace, name, targetVersion, repositoryName string, newValues map[string]any, user *pkgauth.User, progressCh chan<- InstallProgress) error {
	sendProgress := progressSender(progressCh)
	sendProgress("preparing", fmt.Sprintf("Getting current release %s...", name), "")

	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	return c.upgradeWithValues(actionConfig, name, targetVersion, repositoryName, newValues, sendProgress)
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

	source, sourceErr := c.normalUpgradeSource(actionConfig, rel, repositoryName)
	if sourceErr != nil {
		return sourceErr
	}
	targetChart, err := c.chartForUpgradeTarget(actionConfig, rel, targetVersion, repositoryName, sendProgress)
	if err != nil {
		return err
	}

	// Create upgrade action — don't use Wait=true because Radar already
	// shows real-time resource status via SSE. Waiting blocks the dialog
	// for minutes with zero feedback; users can monitor the rollout in the UI.
	if err := useReleaseTargetNamespace(actionConfig, name); err != nil {
		return err
	}

	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = rel.Namespace
	upgradeAction.Timeout = 120 * time.Second
	upgradeAction.Labels = boundChartSourceLabels(rel.Labels, source, targetChart)
	// Reset to the new chart's defaults, then re-merge the user's previously-supplied
	// values on top — preserves their overrides while picking up the new chart's new
	// default keys. Plain ReuseValues keeps the old merged values and can render nil
	// for keys a newer chart added (a cross-version upgrade footgun).
	upgradeAction.ResetThenReuseValues = true

	sendProgress("upgrading", fmt.Sprintf("Applying %s %s...", rel.Chart.Metadata.Name, targetVersion), "")

	// Run the upgrade
	if err := releaseSourceUnchanged(actionConfig, rel); err != nil {
		return err
	}
	_, err = upgradeAction.Run(name, targetChart, rel.Config)
	if err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	sendProgress("complete", fmt.Sprintf("Successfully upgraded %s to %s", name, targetVersion), "")
	return nil
}

// upgradeWithValues upgrades a release to a target chart version applying exactly
// the supplied user values (WYSIWYG — ResetValues, no merge with prior overrides).
// The plain upgradeWith path keeps ResetThenReuseValues for the blind carry-over case.
func (c *Client) upgradeWithValues(actionConfig *action.Configuration, name, targetVersion, repositoryName string, newValues map[string]any, sendProgress func(phase, message, detail string)) error {
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return fmt.Errorf("failed to get current release: %w", err)
	}

	source, sourceErr := c.normalUpgradeSource(actionConfig, rel, repositoryName)
	if sourceErr != nil {
		return sourceErr
	}
	targetChart, err := c.chartForUpgradeTarget(actionConfig, rel, targetVersion, repositoryName, sendProgress)
	if err != nil {
		return err
	}

	if err := useReleaseTargetNamespace(actionConfig, name); err != nil {
		return err
	}

	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = rel.Namespace
	upgradeAction.Timeout = 120 * time.Second
	upgradeAction.Labels = boundChartSourceLabels(rel.Labels, source, targetChart)
	upgradeAction.ResetValues = true // WYSIWYG: apply only the edited values

	sendProgress("upgrading", fmt.Sprintf("Applying %s %s...", rel.Chart.Metadata.Name, targetVersion), "")

	if err := releaseSourceUnchanged(actionConfig, rel); err != nil {
		return err
	}
	_, err = upgradeAction.Run(name, targetChart, newValues)
	if err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	sendProgress("complete", fmt.Sprintf("Successfully upgraded %s to %s", name, targetVersion), "")
	return nil
}

func (c *Client) chartForUpgradeTarget(actionConfig *action.Configuration, rel *release.Release, targetVersion, repositoryName string, sendProgress func(phase, message, detail string)) (*chart.Chart, error) {
	return c.chartForUpgradeTargetWithLoader(actionConfig, rel, targetVersion, repositoryName, sendProgress, c.loadTargetChart)
}

type targetChartLoader func(*action.Configuration, *release.Release, string, string, func(string, string, string)) (*chart.Chart, error)

// chartForUpgradeTargetWithLoader returns a chart that is safe to render. Helm's
// stored release representation does not serialize Chart.dependencies, so an
// umbrella release can retain dependency declarations in Chart.yaml while all
// child chart bodies are absent. Reusing that object for a same-version values
// operation renders only the parent and makes the resulting upgrade destructive.
//
// A complete in-memory release chart remains reusable. An incomplete one is
// reconstructed through the same configured-source resolver used by upgrades,
// pinned to the installed chart version. The loaded chart is checked again so a
// source package that merely declares (but does not vendor) dependencies fails
// closed.
func (c *Client) chartForUpgradeTargetWithLoader(actionConfig *action.Configuration, rel *release.Release, targetVersion, repositoryName string, sendProgress func(phase, message, detail string), load targetChartLoader) (*chart.Chart, error) {
	if rel == nil || rel.Chart == nil || rel.Chart.Metadata == nil {
		return nil, fmt.Errorf("release has no usable chart metadata")
	}

	currentVersion := rel.Chart.Metadata.Version
	if targetVersion == "" {
		targetVersion = currentVersion
	}

	reconstructingStoredChart := false
	if targetVersion == currentVersion {
		if err := validateChartDependencyBodies(rel.Chart); err == nil {
			return rel.Chart, nil
		}
		reconstructingStoredChart = true
	}

	loaded, err := load(actionConfig, rel, targetVersion, repositoryName, sendProgress)
	if err != nil {
		if errors.Is(err, errSourceRequired) || errors.Is(err, errChartDigest) {
			return nil, err
		}
		if reconstructingStoredChart {
			return nil, fmt.Errorf("cannot edit values for this release: Radar could not rebuild the full chart. This release has subcharts, and Helm does not store their contents, so Radar has to fetch %s version %s again - and no configured Helm repository has it. Add the repository this release was installed from, then try again. Applying without the subcharts would delete the resources they created: %w", rel.Chart.Metadata.Name, targetVersion, err)
		}
		return nil, err
	}
	if err := validateChartDependencyBodies(loaded); err != nil {
		return nil, fmt.Errorf("cannot use chart %s version %s: the package declares subcharts but does not include them, so rendering it would delete the resources those subcharts created. The chart needs fixing where it is published: %w", rel.Chart.Metadata.Name, targetVersion, err)
	}
	if reconstructingStoredChart {
		if err := storedChartMatchesReconstructed(rel.Chart, loaded); err != nil {
			return nil, fmt.Errorf("cannot edit values for this release: Radar found %s version %s in a configured Helm repository, but that copy does not match the chart this release was installed from, so applying it could replace your release with a different chart. Add the repository this release came from so Radar can pick the right one: %w", rel.Chart.Metadata.Name, targetVersion, err)
		}
	}
	return loaded, nil
}

// storedChartMatchesReconstructed compares the parent chart we downloaded against
// the parent chart Helm has stored for the release.
//
// Reconstruction resolves by chart name and version through the configured
// sources. The values-apply path supplies no repository hint at all, and the
// resolver accepts a sole candidate, so on a cluster where more than one source
// publishes the same name and version it can return a chart that is not the one
// the release was installed from - and then apply it. Release storage drops the
// dependency bodies but keeps the parent's own templates and values, so the
// stored chart is trustworthy ground truth for exactly the comparison that
// catches this.
//
// Scoped to reconstruction on purpose. A real version upgrade is expected to
// bring different parent content, so this must not run on that path.
//
// Only the parent's templates and values schema are compared, and that is a
// deliberate ceiling rather than an oversight. Helm rewrites a chart on its way
// into release storage, so most of what storage keeps cannot be compared against
// a freshly downloaded package of the same chart:
//
//   - Dependency.Name is replaced by the alias, so a chart declaring redis twice
//     as cache-a and cache-b is stored as two dependencies named cache-a and
//     cache-b, matching neither declaration in the package
//   - Dependency.Enabled is set while conditions are resolved
//   - subchart defaults and a global key are coalesced into the parent's Values
//
// Comparing any of those rejects ordinary umbrella releases. The residual gap is
// real: two charts sharing a name, a version, their parent templates and their
// schema still compare equal while carrying different subchart bodies. Closing
// it needs recorded provenance of where the release was installed from, not a
// better fingerprint of a chart Helm has already altered.
func storedChartMatchesReconstructed(stored, loaded *chart.Chart) error {
	if stored == nil || loaded == nil {
		return fmt.Errorf("chart missing for comparison")
	}
	if err := filesMatch("parent template", stored.Templates, loaded.Templates); err != nil {
		return err
	}
	if !bytes.Equal(stored.Schema, loaded.Schema) {
		return fmt.Errorf("values schema differs from the installed release")
	}
	return nil
}

func filesMatch(label string, stored, loaded []*chart.File) error {
	storedByName := filesByName(stored)
	loadedByName := filesByName(loaded)
	if len(storedByName) != len(loadedByName) {
		return fmt.Errorf("installed release has %d %ss, resolved chart has %d", len(storedByName), label, len(loadedByName))
	}
	names := make([]string, 0, len(storedByName))
	for name := range storedByName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		loadedBody, ok := loadedByName[name]
		if !ok {
			return fmt.Errorf("resolved chart is missing %s %q", label, name)
		}
		if !bytes.Equal(storedByName[name], loadedBody) {
			return fmt.Errorf("%s %q differs from the installed release", label, name)
		}
	}
	return nil
}

func filesByName(files []*chart.File) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		if f == nil {
			continue
		}
		out[f.Name] = f.Data
	}
	return out
}

func canonicalMatch(label string, stored, loaded any) error {
	storedJSON, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("could not compare %s of the installed release: %w", label, err)
	}
	loadedJSON, err := json.Marshal(loaded)
	if err != nil {
		return fmt.Errorf("could not compare %s of the resolved chart: %w", label, err)
	}
	if !bytes.Equal(storedJSON, loadedJSON) {
		return fmt.Errorf("%s differ from the installed release", label)
	}
	return nil
}

// validateChartDependencyBodies verifies that every dependency declared by each
// chart in the tree has a corresponding loaded chart body. Alias names are
// accepted because Helm rewrites an aliased child name while processing values.
//
// A body satisfies every declaration that names it and is deliberately not
// consumed by the first match. Helm vendors one directory per chart name, so a
// chart declaring the same subchart twice under two aliases legitimately ships
// fewer bodies than declarations; chartutil.ProcessDependencies clones the body
// per alias at render time. Consuming a body would reject such a chart as
// incomplete, and since this selection path also serves ordinary upgrades and
// upgrade preview, that rejection would reach every operation on it.
func validateChartDependencyBodies(ch *chart.Chart) error {
	if ch == nil || ch.Metadata == nil {
		return fmt.Errorf("chart metadata is missing")
	}

	children := ch.Dependencies()
	for _, declared := range ch.Metadata.Dependencies {
		if declared == nil {
			continue
		}
		found := false
		for _, child := range children {
			if child == nil || child.Metadata == nil {
				continue
			}
			if child.Metadata.Name == declared.Name || (declared.Alias != "" && child.Metadata.Name == declared.Alias) {
				found = true
				break
			}
		}
		if !found {
			name := declared.Name
			if declared.Alias != "" {
				name += " (alias " + declared.Alias + ")"
			}
			return fmt.Errorf("dependency body %q declared by chart %q is missing", name, ch.Metadata.Name)
		}
	}

	for _, child := range children {
		if err := validateChartDependencyBodies(child); err != nil {
			return err
		}
	}
	return nil
}

// loadTargetChart resolves, downloads and loads the chart for a target upgrade
// version, refusing a silent chart-swap (the resolved source must publish the
// same chart the release runs). Shared by the upgrade and preview paths so they
// resolve the target version identically.
func (c *Client) loadTargetChart(actionConfig *action.Configuration, rel *release.Release, targetVersion, repositoryName string, sendProgress func(phase, message, detail string)) (*chart.Chart, error) {
	source, err := c.normalUpgradeSource(actionConfig, rel, repositoryName)
	if errors.Is(err, errSourceRequired) && targetVersion == rel.Chart.Metadata.Version {
		if recorded, ok := chartSourceFromRelease(rel); ok {
			if repositoryName != "" {
				configured := c.configuredRepositoryForSource(*recorded)
				if configured == nil || configured.Name != repositoryName {
					return nil, errSourceConflict
				}
			}
			loaded, _, loadErr := c.loadSourceChart(actionConfig, *recorded, rel.Chart.Metadata.Name, targetVersion)
			if loadErr != nil {
				return nil, loadErr
			}
			if chartSourceBinding(loaded) != chartSourceBinding(rel.Chart) {
				return nil, errSourceRequired
			}
			return loaded, nil
		}
	}
	if err != nil {
		return nil, err
	}
	sendProgress("downloading", "Downloading from the recorded chart source...", "")
	ch, _, err := c.loadSourceChart(actionConfig, *source, rel.Chart.Metadata.Name, targetVersion)
	return ch, err
}

func isAbsoluteChartURL(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || registry.IsOCI(path)
}

// resolveOCIChartURL accepts either a complete OCI chart reference or a
// registry/repository prefix. InstallWizard sends the selected repository URL
// and the chart name separately, so append the name only when it is not already
// the final path component.
func resolveOCIChartURL(source, chartName string) (string, error) {
	source = strings.TrimSuffix(strings.TrimSpace(source), "/")
	chartName = strings.Trim(strings.TrimSpace(chartName), "/")
	if !registry.IsOCI(source) {
		return "", fmt.Errorf("chart source %q is not an OCI reference", source)
	}
	if chartName == "" {
		return "", fmt.Errorf("chart name is empty")
	}
	chartPath := source
	lastSlash := strings.LastIndex(chartPath, "/")
	if lastSlash >= 0 {
		lastComponent := chartPath[lastSlash+1:]
		if selector := strings.IndexAny(lastComponent, ":@"); selector >= 0 {
			chartPath = chartPath[:lastSlash+1] + lastComponent[:selector]
		}
	}
	if strings.HasSuffix(chartPath, "/"+chartName) {
		return source, nil
	}
	return source + "/" + chartName, nil
}

// locateChartPath resolves one exact chart version. OCI pulls require a
// registry client explicitly; Radar builds it from Helm's existing registry
// credential store and never changes the configured OCI source inventory.
func (c *Client) locateChartPath(actionConfig *action.Configuration, chartURL, version string) (string, error) {
	if registry.IsOCI(chartURL) && version == "latest" {
		version = ""
	}
	if c.chartPathLocator != nil {
		return c.chartPathLocator(actionConfig, chartURL, version)
	}
	if actionConfig == nil {
		var err error
		actionConfig, err = c.getActionConfig("")
		if err != nil {
			return "", err
		}
	}
	locator := action.NewInstall(actionConfig)
	locator.Version = version
	if registry.IsOCI(chartURL) {
		registryClient, err := c.newRegistryClientForChartPull()
		if err != nil {
			return "", fmt.Errorf("failed to build OCI registry client: %w", err)
		}
		locator.SetRegistryClient(registryClient)
	}
	return locator.ChartPathOptions.LocateChart(chartURL, c.settings)
}

// BatchCheckUpgrades checks for upgrades for all releases at once (more efficient)
func (c *Client) BatchCheckUpgrades(ctx context.Context, namespace string) (*BatchUpgradeInfo, error) {
	return c.batchCheckUpgrades(ctx, namespace, nil)
}

// BatchCheckUpgradesAsUser is BatchCheckUpgrades with K8s impersonation on
// the release listing (the repo index reads are local-file only and don't
// touch K8s).
func (c *Client) BatchCheckUpgradesAsUser(ctx context.Context, namespace string, user *pkgauth.User) (*BatchUpgradeInfo, error) {
	return c.batchCheckUpgrades(ctx, namespace, user)
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
func (c *Client) BatchCheckUpgradesAcrossNamespaces(ctx context.Context, namespaces []string, user *pkgauth.User) (*BatchUpgradeInfo, error) {
	if namespaces == nil {
		return c.BatchCheckUpgradesAsUser(ctx, "", user)
	}
	merged := &BatchUpgradeInfo{Releases: make(map[string]*UpgradeInfo)}
	for _, ns := range namespaces {
		info, err := c.BatchCheckUpgradesAsUser(ctx, ns, user)
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

func (c *Client) batchCheckUpgrades(ctx context.Context, namespace string, user *pkgauth.User) (*BatchUpgradeInfo, error) {
	// Full *release.Release objects are needed (Chart.Metadata.Home/Sources
	// drive source-affinity disambiguation). The storage snapshot carries
	// them along with each release's storage namespace from one Secret list
	// that stops with ctx, which Helm's own list action can't do.
	client, err := c.storageClient(user)
	if err != nil {
		return nil, err
	}
	snapshot, err := helmReleaseStorageSnapshotWithClient(ctx, client, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list helm releases: %w", err)
	}
	result := &BatchUpgradeInfo{Releases: make(map[string]*UpgradeInfo)}
	versionsBySource := make(map[string][]string)
	indexes := make(map[string]*repo.IndexFile)
	for _, rel := range snapshot.latest {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if rel.Chart == nil || rel.Chart.Metadata == nil {
			continue
		}
		info := &UpgradeInfo{CurrentVersion: rel.Chart.Metadata.Version}
		key := releaseUpgradeKey(rel, snapshot.storageNamespaces[releaseStorageKey(rel)])
		baseline := snapshot.deployed[releaseHistoryKey(rel)]
		if baseline == nil {
			baseline = rel
		}
		source, issue, message := upgradeSourceState(releaseSourceContext{rel, baseline})
		if issue != "" {
			markUpgradeSourceIssue(info, issue, message)
		} else {
			sourceKey := source.Type + "|" + source.URL + "|" + source.Reference + "|" + rel.Chart.Metadata.Name
			versions, cached := versionsBySource[sourceKey]
			if !cached {
				versions, _ = c.sourceVersionsWithIndexes(*source, rel.Chart.Metadata.Name, indexes)
				versionsBySource[sourceKey] = versions
			}
			if !c.applyRecordedVersions(info, *source, versions, rel.Chart.Metadata.Version) {
				markUpgradeSourceIssue(info, UpgradeSourceIssueUnavailable, recordedSourceUnavailableMessage)
			}
		}
		result.Releases[key] = info
	}
	return result, nil
}

// PreviewValuesChange previews the effect of new values on a release via dry-run.
// When targetVersion is non-empty and differs from the running chart version, the
// dry-run renders against that target chart instead of the current chart.
func (c *Client) PreviewValuesChange(namespace, name string, newValues map[string]any, targetVersion, repositoryName string) (*ValuesPreviewResponse, error) {
	actionConfig, err := c.getActionConfig(namespace)
	if err != nil {
		return nil, err
	}
	return c.previewValuesChangeWith(actionConfig, name, newValues, targetVersion, repositoryName)
}

func (c *Client) PreviewValuesChangeAsUser(namespace, name string, newValues map[string]any, targetVersion, repositoryName string, user *pkgauth.User) (*ValuesPreviewResponse, error) {
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return nil, err
	}
	return c.previewValuesChangeWith(actionConfig, name, newValues, targetVersion, repositoryName)
}

func (c *Client) previewValuesChangeWith(actionConfig *action.Configuration, name string, newValues map[string]any, targetVersion, repositoryName string) (*ValuesPreviewResponse, error) {
	// Get the current release
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return nil, fmt.Errorf("failed to get current release: %w", err)
	}

	op, opErr := releaseContext(actionConfig, name)
	if opErr != nil {
		return nil, opErr
	}
	if failedSourceChange(op) {
		return nil, errSourceRequired
	}
	if targetVersion != "" || repositoryName != "" {
		if _, err := c.normalUpgradeSource(actionConfig, rel, repositoryName); err != nil {
			return nil, err
		}
	}
	// Get current user-supplied values
	currentValues := op.Baseline.Config

	// Get current manifest
	currentManifest := op.Baseline.Manifest

	noop := func(phase, message, detail string) {}
	previewChart, err := c.chartForUpgradeTarget(actionConfig, rel, targetVersion, repositoryName, noop)
	if err != nil {
		return nil, err
	}

	// Perform a dry-run upgrade with the new values
	if err := useReleaseTargetNamespace(actionConfig, name); err != nil {
		return nil, err
	}

	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = rel.Namespace
	upgradeAction.DryRun = true
	upgradeAction.DryRunOption = "client"
	upgradeAction.ResetValues = true // Use only the provided values, don't merge

	// Run the dry-run upgrade
	newRel, err := upgradeAction.Run(name, previewChart, newValues)
	if err != nil {
		return nil, fmt.Errorf("failed to preview values change: %w", err)
	}

	// Compute the manifest diff
	diff := computeDiff(currentManifest, newRel.Manifest, op.Baseline.Version, newRel.Version)

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
func (c *Client) ApplyValuesAsUser(namespace, name string, newValues map[string]any, user *pkgauth.User) error {
	actionConfig, err := c.getActionConfigForUser(namespace, user)
	if err != nil {
		return err
	}
	return c.applyValuesWith(actionConfig, name, newValues)
}

func (c *Client) applyValuesWith(actionConfig *action.Configuration, name string, newValues map[string]any) error {
	// Get the current release and reconstruct its exact installed chart when
	// Helm storage omitted vendored dependency bodies.
	getAction := action.NewGet(actionConfig)
	rel, err := getAction.Run(name)
	if err != nil {
		return fmt.Errorf("failed to get current release: %w", err)
	}
	op, opErr := releaseContext(actionConfig, name)
	if opErr != nil {
		return opErr
	}
	if failedSourceChange(op) {
		return errSourceRequired
	}
	noop := func(phase, message, detail string) {}
	applyChart, err := c.chartForUpgradeTarget(actionConfig, rel, "", "", noop)
	if err != nil {
		return err
	}

	// Create upgrade action — no Wait, Radar shows resource status in real-time
	if err := useReleaseTargetNamespace(actionConfig, name); err != nil {
		return err
	}

	upgradeAction := action.NewUpgrade(actionConfig)
	upgradeAction.Namespace = rel.Namespace
	upgradeAction.Timeout = 120 * time.Second
	if source, ok := confirmedChartSource(rel); ok {
		upgradeAction.Labels = boundChartSourceLabels(rel.Labels, source, applyChart)
	}
	upgradeAction.ResetValues = true // Use only the provided values, don't merge

	// Run the upgrade with the complete installed-version chart and new values.
	if err := releaseSourceUnchanged(actionConfig, rel); err != nil {
		return err
	}
	_, err = upgradeAction.Run(name, applyChart, newValues)
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
		if errors.Is(err, os.ErrNotExist) {
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
	chartRepo, err := repo.NewChartRepository(repoEntry, c.repositoryGetters(repoEntry))
	if err != nil {
		return fmt.Errorf("failed to create chart repository %q: %s", repoName, repositoryRefreshErrorURL.ReplaceAllString(err.Error(), "<redacted>"))
	}

	if err := os.MkdirAll(c.settings.RepositoryCache, 0o755); err != nil {
		return fmt.Errorf("prepare repository cache: %w", err)
	}
	stageDir, err := os.MkdirTemp(c.settings.RepositoryCache, ".radar-repository-")
	if err != nil {
		return fmt.Errorf("stage repository index: %w", err)
	}
	defer os.RemoveAll(stageDir)
	chartRepo.CachePath = stageDir

	stagedIndex, err := chartRepo.DownloadIndexFile()
	if err != nil {
		return fmt.Errorf("failed to download index for repository %q: %s", repoName, repositoryRefreshErrorURL.ReplaceAllString(err.Error(), "<redacted>"))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	current, err := repo.LoadFile(repoFile)
	if err != nil {
		return fmt.Errorf("reload repository configuration: %w", err)
	}
	if entry := current.Get(repoName); entry == nil || *entry != *repoEntry {
		return fmt.Errorf("%w: repository %q changed while refreshing; retry the request", errRepositoryConflict, repoName)
	}
	return publishStagedRepositoryCache(stageDir, stagedIndex, c.settings.RepositoryCache)
}

// SearchCharts searches for charts across all repositories
func (c *Client) SearchCharts(query string, allVersions bool) (*ChartSearchResult, error) {
	repoFile := c.settings.RepositoryConfig
	f, err := repo.LoadFile(repoFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
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
	if registry.IsOCI(repoName) {
		chartURL, err := resolveOCIChartURL(repoName, chartName)
		if err != nil {
			return nil, err
		}
		chartPath, err := c.locateChartPath(nil, chartURL, version)
		if err != nil {
			return nil, fmt.Errorf("failed to locate OCI chart: %w", err)
		}
		loadedChart, err := loader.Load(chartPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load OCI chart: %w", err)
		}
		return chartDetailFromChart(loadedChart, ChartInfo{
			Name:        loadedChart.Metadata.Name,
			Version:     loadedChart.Metadata.Version,
			AppVersion:  loadedChart.Metadata.AppVersion,
			Description: loadedChart.Metadata.Description,
			Icon:        loadedChart.Metadata.Icon,
			Repository:  repoName,
			Home:        loadedChart.Metadata.Home,
			Deprecated:  loadedChart.Metadata.Deprecated,
		}), nil
	}

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
	if !isAbsoluteChartURL(chartURL) {
		chartURL = strings.TrimSuffix(repoEntry.URL, "/") + "/" + chartURL
	}

	var loadedChart *chart.Chart
	if registry.IsOCI(chartURL) {
		cp, locateErr := c.locateChartPath(nil, chartURL, chartVersion.Version)
		if locateErr != nil {
			return &ChartDetail{ChartInfo: chartVersionToInfo(chartVersion, repoName)}, nil
		}
		loadedChart, err = loadChartWithDigest(cp, chartVersion.Digest)
	} else {
		body, fetchErr := c.getRepositoryChart(repoEntry, chartURL)
		if fetchErr != nil {
			log.Printf("[helm] Could not download chart details for %s/%s; showing repository index information: %v", repoName, chartName, fetchErr)
			return &ChartDetail{ChartInfo: chartVersionToInfo(chartVersion, repoName)}, nil
		}
		if err := verifyChartDigest(body.Bytes(), chartVersion.Digest); err != nil {
			return nil, fmt.Errorf("chart %s version %s: %w", chartName, chartVersion.Version, withChartDigestGuidance(err, false))
		}
		loadedChart, err = loader.LoadArchive(body)
	}
	if err != nil {
		if errors.Is(err, errChartDigest) {
			return nil, fmt.Errorf("chart %s version %s: %w", chartName, chartVersion.Version, withChartDigestGuidance(err, false))
		}
		return &ChartDetail{ChartInfo: chartVersionToInfo(chartVersion, repoName)}, nil
	}

	return chartDetailFromChart(loadedChart, chartVersionToInfo(chartVersion, repoName)), nil
}

func chartDetailFromChart(chart *chart.Chart, info ChartInfo) *ChartDetail {
	detail := &ChartDetail{ChartInfo: info}

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

	return detail
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
func (c *Client) InstallAsUser(req *InstallRequest, user *pkgauth.User) (*HelmRelease, error) {
	actionConfig, err := c.getActionConfigForUser(req.Namespace, user)
	if err != nil {
		return nil, err
	}
	return c.installWith(actionConfig, req)
}

func (c *Client) installWith(actionConfig *action.Configuration, req *InstallRequest) (*HelmRelease, error) {

	var chartURL string
	var indexDigest string
	var downloadEntry *repo.Entry
	resolvedVersion := req.Version

	// Check if the repository is a URL (for ArtifactHub installs) or a local repo name
	isRepoURL := strings.HasPrefix(req.Repository, "http://") || strings.HasPrefix(req.Repository, "https://")
	isOCI := registry.IsOCI(req.Repository)

	if isOCI {
		var err error
		chartURL, err = resolveOCIChartURL(req.Repository, req.ChartName)
		if err != nil {
			return nil, err
		}
	} else if isRepoURL {
		downloadEntry = &repo.Entry{URL: req.Repository}
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
		resolvedVersion = chartVersion.Version
		indexDigest = chartVersion.Digest

		// Build chart URL
		chartURL = chartVersion.URLs[0]
		if !isAbsoluteChartURL(chartURL) {
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

		downloadEntry = repoEntry
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
		resolvedVersion = chartVersion.Version
		indexDigest = chartVersion.Digest

		// Build chart URL
		chartURL = chartVersion.URLs[0]
		if !isAbsoluteChartURL(chartURL) {
			chartURL = strings.TrimSuffix(repoEntry.URL, "/") + "/" + chartURL
		}
	}

	mode, err := preInstallCheck(actionConfig, req.ReleaseName, req.Namespace)
	if err != nil {
		return nil, err
	}
	resolvedSource, err := c.installChartSource(req)
	if err != nil {
		return nil, fmt.Errorf("failed to record chart source: %w", err)
	}
	if resolvedSource != nil {
		if err := validateChartSourceCandidate(resolvedSource); err != nil {
			return nil, err
		}
	}
	req.resolvedSource = resolvedSource

	var loadedChart *chart.Chart
	if registry.IsOCI(chartURL) {
		cp, locateErr := c.locateChartPath(actionConfig, chartURL, resolvedVersion)
		if locateErr != nil {
			return nil, fmt.Errorf("failed to locate chart: %w", locateErr)
		}
		loadedChart, err = loadChartWithDigest(cp, indexDigest)
	} else {
		body, fetchErr := c.getRepositoryChart(downloadEntry, chartURL)
		if fetchErr != nil {
			return nil, fmt.Errorf("failed to download chart: %w", fetchErr)
		}
		if err := verifyChartDigest(body.Bytes(), indexDigest); err != nil {
			return nil, fmt.Errorf("chart %s version %s: %w", req.ChartName, resolvedVersion, withChartDigestGuidance(err, isRepoURL))
		}
		loadedChart, err = loader.LoadArchive(body)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load chart %s version %s: %w", req.ChartName, resolvedVersion, withChartDigestGuidance(err, isRepoURL))
	}

	if mode != installFresh {
		log.Printf("[helm] install %q/%q: prior release record exists, recovering via %s", req.Namespace, req.ReleaseName, recoveryMode(mode))
	}
	rel, err := runInstallOrUpgrade(actionConfig, req, loadedChart, mode)
	if err != nil {
		return nil, fmt.Errorf("install failed: %w", err)
	}

	return installedHelmRelease(rel), nil
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
func (c *Client) InstallWithProgressAsUser(req *InstallRequest, progressCh chan<- InstallProgress, user *pkgauth.User) (*HelmRelease, error) {
	actionConfig, err := c.getActionConfigForUser(req.Namespace, user)
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
	var indexDigest string
	var downloadEntry *repo.Entry
	resolvedVersion := req.Version

	// Check if the repository is a URL (for ArtifactHub installs) or a local repo name
	isRepoURL := strings.HasPrefix(req.Repository, "http://") || strings.HasPrefix(req.Repository, "https://")
	isOCI := registry.IsOCI(req.Repository)

	if isOCI {
		var err error
		chartURL, err = resolveOCIChartURL(req.Repository, req.ChartName)
		if err != nil {
			return nil, err
		}
		sendProgress("resolving", "Resolving chart from OCI registry...", chartURL)
	} else if isRepoURL {
		downloadEntry = &repo.Entry{URL: req.Repository}
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
		resolvedVersion = chartVersion.Version
		indexDigest = chartVersion.Digest

		chartURL = chartVersion.URLs[0]
		if !isAbsoluteChartURL(chartURL) {
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

		downloadEntry = repoEntry
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
		resolvedVersion = chartVersion.Version
		indexDigest = chartVersion.Digest

		chartURL = chartVersion.URLs[0]
		if !isAbsoluteChartURL(chartURL) {
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
	resolvedSource, err := c.installChartSource(req)
	if err != nil {
		return nil, fmt.Errorf("failed to record chart source: %w", err)
	}
	if resolvedSource != nil {
		if err := validateChartSourceCandidate(resolvedSource); err != nil {
			return nil, err
		}
	}
	req.resolvedSource = resolvedSource

	sendProgress("downloading", fmt.Sprintf("Downloading chart %s-%s...", req.ChartName, req.Version), chartURL)

	var loadedChart *chart.Chart
	if registry.IsOCI(chartURL) {
		chartPath, err := c.locateChartPath(actionConfig, chartURL, resolvedVersion)
		if err != nil {
			return nil, fmt.Errorf("failed to locate chart: %w", err)
		}
		sendProgress("loading", "Loading chart...", chartPath)
		loadedChart, err = loadChartWithDigest(chartPath, indexDigest)
		if err != nil {
			return nil, fmt.Errorf("failed to load chart %s version %s: %w", req.ChartName, resolvedVersion, withChartDigestGuidance(err, isRepoURL))
		}
	} else {
		body, fetchErr := c.getRepositoryChart(downloadEntry, chartURL)
		if fetchErr != nil {
			return nil, fmt.Errorf("failed to download chart: %w", fetchErr)
		}
		sendProgress("loading", "Loading chart...", "")
		if err := verifyChartDigest(body.Bytes(), indexDigest); err != nil {
			return nil, fmt.Errorf("chart %s version %s: %w", req.ChartName, resolvedVersion, withChartDigestGuidance(err, isRepoURL))
		}
		loadedChart, err = loader.LoadArchive(body)
		if err != nil {
			return nil, fmt.Errorf("failed to load chart %s version %s: %w", req.ChartName, resolvedVersion, withChartDigestGuidance(err, isRepoURL))
		}

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

	rel, err := runInstallOrUpgrade(actionConfig, req, loadedChart, mode)
	if err != nil {
		return nil, fmt.Errorf("install failed: %w", err)
	}

	sendProgress("complete", fmt.Sprintf("Successfully installed %s", req.ReleaseName), "")

	return installedHelmRelease(rel), nil
}

// installedHelmRelease converts the immediate result of an install action.
// Unlike toHelmRelease (the list/detail path), it deliberately does not query
// Radar's informer cache for post-install resource health.
func installedHelmRelease(rel *release.Release) *HelmRelease {
	return &HelmRelease{
		Name: rel.Name, Namespace: rel.Namespace,
		Chart: rel.Chart.Metadata.Name, ChartVersion: rel.Chart.Metadata.Version,
		AppVersion: rel.Chart.Metadata.AppVersion, Status: rel.Info.Status.String(),
		Revision: rel.Version, Updated: rel.Info.LastDeployed.Time,
	}
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
	Official              bool                       `json:"official"`
	Signed                bool                       `json:"signed"`
	HasValuesSchema       bool                       `json:"has_values_schema"`
	SecurityReportSummary *artifactHubSecurityReport `json:"security_report_summary,omitempty"`
	ProductionOrgsCount   int                        `json:"production_organizations_count"`
	TS                    int64                      `json:"ts"` // Unix timestamp
	Repository            artifactHubRepo            `json:"repository"`
	License               string                     `json:"license,omitempty"`
}

type artifactHubRepo struct {
	Name                    string `json:"name"`
	URL                     string `json:"url"`
	Official                bool   `json:"official"`
	VerifiedPublisher       bool   `json:"verified_publisher"`
	OrganizationName        string `json:"organization_name,omitempty"`
	OrganizationDisplayName string `json:"organization_display_name,omitempty"`
	UserAlias               string `json:"user_alias,omitempty"`
	DisplayName             string `json:"display_name,omitempty"`
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
		Official:    pkg.Official,
		Stars:       pkg.Stars,
		License:     pkg.License,
		UpdatedAt:   pkg.TS,
		Signed:      pkg.Signed,
		HasSchema:   pkg.HasValuesSchema,
		OrgCount:    pkg.ProductionOrgsCount,
		Repository: ArtifactHubRepository{
			Name:                    pkg.Repository.Name,
			URL:                     pkg.Repository.URL,
			Official:                pkg.Repository.Official,
			VerifiedPublisher:       pkg.Repository.VerifiedPublisher,
			OrganizationName:        pkg.Repository.OrganizationName,
			OrganizationDisplayName: pkg.Repository.OrganizationDisplayName,
			UserAlias:               pkg.Repository.UserAlias,
			DisplayName:             pkg.Repository.DisplayName,
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
