package k8s

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/homedir"

	"github.com/skyhook-io/radar/internal/errorlog"
)

var (
	k8sClient         *kubernetes.Clientset
	k8sConfig         *rest.Config
	discoveryClient   *discovery.DiscoveryClient
	dynamicClient     dynamic.Interface
	initOnce          sync.Once
	initErr           error
	kubeconfigPath    string
	kubeconfigPaths   []string // Multiple kubeconfig paths when using --kubeconfig-dir or KUBECONFIG env
	kubeconfigMode    string   // One of: "in-cluster", "single", "multi-env", "multi-dir"
	totalContextCount int      // Total number of contexts exposed across all kubeconfig files
	// contextRegistry maps each user-facing context name to its source file and
	// the name it has inside that file. Populated when Radar loads more than one
	// kubeconfig file (multi-dir, multi-env with >1 paths, or CAPI-added files).
	// Each file is loaded in isolation via ExplicitPath rather than merged via
	// Precedence — a shared user/cluster/context name across files no longer
	// clobbers anything, which is the whole point of the registry. See issue
	// #519 (and #411, #514) for the bug this replaces.
	contextRegistry map[string]contextEntry
	// perFileConfigs caches each file's parsed api.Config so GetAvailableContexts
	// doesn't re-read N files on every call. Keyed by absolute file path.
	perFileConfigs map[string]*clientcmdapi.Config
	// perFileMtimes lets refreshContextRegistry detect rewritten or
	// removed kubeconfig files between calls. Without this the
	// registry is built once at startup and never refreshes, so
	// destroyed clusters / removed contexts linger in the dropdown
	// (they only error out when the user tries to switch to them).
	// Same lifecycle / lock as perFileConfigs.
	perFileMtimes      map[string]time.Time
	contextName        string
	clusterName        string
	contextNamespace   string   // Default namespace from kubeconfig context
	fallbackNamespace  string   // Explicit namespace from --namespace flag
	fallbackNamespaces []string // Explicit namespace candidates from --namespaces flag
	// fallbackNamespaceContext is the context that was active when --namespace was
	// set at startup. --namespace is an *initial* value, so it only pins the cache
	// scope for that context — after switching clusters, the scope target comes
	// from the new context's namespace (or a saved pick), not the stale startup value.
	fallbackNamespaceContext string
	namespaceScopeOverride   string // Runtime namespace selected by local --namespace-scope rescope
	namespaceScopeResolver   func(contextName string) (string, bool)
	contextUsesExec          bool // True when the current context uses an exec credential plugin
	// execPluginCommands is the set of unique exec-auth plugin command basenames
	// referenced by any context in the merged kubeconfig. Populated from
	// rawConfig.AuthInfos at load time and refreshed on SwitchContext. Stored
	// as basenames only so diagnostics never leak full binary paths. Used by
	// GetKubeconfigSummary() to produce present/missing lists against the
	// current process PATH.
	execPluginCommands []string
	// enrichedKubeconfigFromShell is set by the desktop app's enrichEnv() when
	// it successfully captured KUBECONFIG from the user's login shell. Surfaced
	// in diagnostics so we can tell whether the GUI app's env was enriched or
	// whether we fell back to whatever the parent process handed us. All access
	// goes through clientMu like the rest of the globals in this file —
	// callers use SetEnrichedKubeconfigFromShell to write.
	enrichedKubeconfigFromShell bool
	// clientMu protects access to client variables during context switches.
	// Readers use RLock, context switch uses Lock.
	clientMu sync.RWMutex
)

// SetEnrichedKubeconfigFromShell records that the desktop app's enrichEnv()
// successfully captured KUBECONFIG from the user's login shell. Used only for
// diagnostic reporting — does not affect K8s client behavior. Takes clientMu
// like every other write to the package-level state.
func SetEnrichedKubeconfigFromShell(v bool) {
	clientMu.Lock()
	defer clientMu.Unlock()
	enrichedKubeconfigFromShell = v
}

// InitOptions configures the K8s client initialization
type InitOptions struct {
	KubeconfigPath string
	KubeconfigDirs []string // Directories containing kubeconfig files
}

// Initialize initializes the K8s client with the given options
func Initialize(opts InitOptions) error {
	initOnce.Do(func() {
		initErr = doInit(opts)
	})
	return initErr
}

// MustInitialize is like Initialize but panics on error
func MustInitialize(opts InitOptions) {
	if err := Initialize(opts); err != nil {
		panic(fmt.Sprintf("failed to initialize k8s client: %v", err))
	}
}

func doInit(opts InitOptions) error {
	var config *rest.Config
	var err error

	// Configuration precedence (matches kubectl behavior):
	//   1. --kubeconfig flag (opts.KubeconfigPath)
	//   2. KUBECONFIG environment variable
	//   3. --kubeconfig-dir flag (opts.KubeconfigDirs)
	//   4. In-cluster config (when KUBERNETES_SERVICE_HOST is set)
	//   5. Default ~/.kube/config
	//
	// We only try in-cluster config if no explicit kubeconfig is specified.
	// This handles the case where KUBERNETES_SERVICE_HOST is set (e.g., inside
	// a pod) but the user wants to connect to a different cluster via kubeconfig.
	// See: https://github.com/kubernetes/kubernetes/issues/43662
	if opts.KubeconfigPath == "" && os.Getenv("KUBECONFIG") == "" && len(opts.KubeconfigDirs) == 0 {
		config, err = rest.InClusterConfig()
		if err == nil {
			contextName = "in-cluster"
			clusterName = "in-cluster"
			kubeconfigMode = "in-cluster"
		}
	}

	if config == nil {
		// Use kubeconfig (for local development / CLI usage)
		var loadingRules *clientcmd.ClientConfigLoadingRules
		configOverrides := &clientcmd.ConfigOverrides{}

		if len(opts.KubeconfigDirs) > 0 {
			// Multi-kubeconfig mode: discover files and, if more than one,
			// load them in isolation via the context registry (see issue #519).
			configs, err := discoverKubeconfigs(opts.KubeconfigDirs)
			if err != nil {
				return fmt.Errorf("failed to discover kubeconfigs: %w", err)
			}
			if len(configs) == 0 {
				return fmt.Errorf("no valid kubeconfig files found in directories: %v", opts.KubeconfigDirs)
			}
			log.Printf("Discovered %d kubeconfig files from %d directories", len(configs), len(opts.KubeconfigDirs))
			kubeconfigPaths = configs
			kubeconfigMode = "multi-dir"
			if len(configs) == 1 {
				loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: configs[0]}
			} else {
				lr, ovr, err := setupIsolatedLoad(configs)
				if err != nil {
					return err
				}
				loadingRules, configOverrides = lr, ovr
			}
		} else {
			// Single kubeconfig mode (existing behavior)
			kubeconfig := opts.KubeconfigPath
			if kubeconfig == "" {
				kubeconfig = os.Getenv("KUBECONFIG")
			}
			if kubeconfig == "" {
				if home := homedir.HomeDir(); home != "" {
					kubeconfig = filepath.Join(home, ".kube", "config")
				}
			}

			// KUBECONFIG can contain multiple paths separated by the OS path
			// list separator (colon on Unix, semicolon on Windows). With more
			// than one path we go through the isolated-load path rather than
			// client-go's Precedence merge — same reason as multi-dir.
			if paths := filepath.SplitList(kubeconfig); len(paths) > 1 {
				kubeconfigPaths = paths
				kubeconfigMode = "multi-env"
				lr, ovr, err := setupIsolatedLoad(paths)
				if err != nil {
					return err
				}
				loadingRules, configOverrides = lr, ovr
				log.Printf("KUBECONFIG contains %d paths, using isolated per-file loading", len(paths))
			} else {
				kubeconfigPath = kubeconfig
				kubeconfigMode = "single"
				loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
			}
		}

		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

		// Get raw config to extract context/cluster names. If this fails
		// we still let ClientConfig() run below — it's likely to fail too,
		// but we record the two failures separately so the snapshot shows
		// the first error without bookkeeping getting silently skipped.
		// Emitting at "error" severity (not "warning") because a RawConfig
		// failure zeroes out every downstream diagnostic field this PR
		// exists to surface — the entry must not be easy to overlook.
		rawConfig, rawErr := kubeConfig.RawConfig()
		if rawErr != nil {
			log.Printf("Kubeconfig metadata load failed (mode=%s): %v", kubeconfigMode, rawErr)
			errorlog.Record("k8s-init", "error",
				"RawConfig() failed; context metadata and diagnostic counts unavailable: %v", rawErr)
		} else {
			// In isolated-load mode, rawConfig reflects the single chosen
			// file — which is all the current context needs, but the
			// "how many contexts can the user pick from?" number must come
			// from the registry (sum across all files), and exec plugin
			// discovery must cover every file.
			if contextRegistry != nil {
				// contextName was already set to the qualified name by
				// setupIsolatedLoad; don't overwrite with the original name
				// inside the single chosen file.
				totalContextCount = len(contextRegistry)
				cmds, emptyAIs := aggregateExecPluginCommands(kubeconfigPaths, perFileConfigs)
				execPluginCommands = cmds
				if len(emptyAIs) > 0 {
					recordEmptyCommandWarning("k8s-init", emptyAIs)
				}
				// Look up the current context's cluster/namespace/exec via
				// the registry-resolved file. rawConfig.Contexts is keyed by
				// the *original* name inside the chosen file.
				if entry, ok := contextRegistry[contextName]; ok {
					if ctx, ok := rawConfig.Contexts[entry.InFileName]; ok {
						clusterName = ctx.Cluster
						contextNamespace = ctx.Namespace
						if ai, ok := rawConfig.AuthInfos[ctx.AuthInfo]; ok && ai.Exec != nil {
							contextUsesExec = true
						}
					}
				}
			} else {
				contextName = rawConfig.CurrentContext
				totalContextCount = len(rawConfig.Contexts)
				cmds, emptyAIs := collectExecPluginCommands(&rawConfig)
				execPluginCommands = cmds
				if len(emptyAIs) > 0 {
					// Aggregate into a single errorlog entry — a pathological
					// kubeconfig with hundreds of broken AuthInfos would otherwise
					// flood the 200-entry ring buffer and evict other diagnostics.
					recordEmptyCommandWarning("k8s-init", emptyAIs)
				}
				if ctx, ok := rawConfig.Contexts[contextName]; ok {
					clusterName = ctx.Cluster
					contextNamespace = ctx.Namespace
					if ai, ok := rawConfig.AuthInfos[ctx.AuthInfo]; ok && ai.Exec != nil {
						contextUsesExec = true
					}
				}
			}
			fileCount := len(kubeconfigPaths)
			if fileCount == 0 && kubeconfigPath != "" {
				fileCount = 1
			}
			// Total contexts across all files (pre-#519 this was the post-merge
			// count, which silently hid colliding user/cluster definitions;
			// now every file's contexts are individually reachable).
			log.Printf("Kubeconfig loaded: mode=%s, files=%d, contexts=%d, exec-plugins=%d",
				kubeconfigMode, fileCount, totalContextCount, len(execPluginCommands))
		}

		config, err = kubeConfig.ClientConfig()
		if err != nil {
			// Record to errorlog so the failure lands in the diagnostics
			// snapshot's recentErrors. Include only the file count and mode —
			// never the kubeconfig paths — so the snapshot stays shareable.
			errorlog.Record("k8s-init", "error",
				"failed to build kubeconfig client config (mode=%s, files=%d): %v",
				kubeconfigMode, len(kubeconfigPaths), err)
			if len(kubeconfigPaths) > 0 {
				return fmt.Errorf("failed to build kubeconfig from %d files: %w", len(kubeconfigPaths), err)
			}
			return fmt.Errorf("failed to build kubeconfig from %s: %w", kubeconfigPath, err)
		}
	}

	// Increase QPS/Burst to speed up CRD discovery and reduce throttling
	// Default client-go is 5 QPS / 10 Burst, kubectl uses 50/100
	// This is safe for a read-only visibility tool
	config.QPS = 50
	config.Burst = 100

	k8sConfig = config

	k8sClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create k8s clientset: %w", err)
	}

	// Create discovery client for API resource discovery
	discoveryClient, err = discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Create dynamic client for CRD access
	dynamicClient, err = dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return nil
}

// discoverKubeconfigs scans directories for valid kubeconfig files
func discoverKubeconfigs(dirs []string) ([]string, error) {
	var configs []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			log.Printf("Warning: cannot read kubeconfig directory %s: %v", dir, err)
			// Surface scan failures in the diagnostics snapshot so "my
			// dropdown is empty" reports can tell permission/missing-dir
			// apart from "dir was there but held no valid configs".
			// Strip full paths from the error text via *os.PathError so
			// the snapshot stays shareable — just Op + underlying cause.
			errorlog.Record("k8s-init", "warning",
				"kubeconfig dir %q scan failed: %s",
				filepath.Base(dir), scrubPathError(err))
			continue // Skip inaccessible dirs
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// Skip hidden files and common non-config files
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			path := filepath.Join(dir, name)
			if isValidKubeconfig(path) {
				configs = append(configs, path)
				log.Printf("Found kubeconfig: %s", path)
			} else {
				log.Printf("Skipping invalid kubeconfig: %s", path)
				// Per-file parse/validation failures are invisible from
				// the merged-config counts alone — a broken file lowers
				// fileCount without explaining why. Record the basename
				// (never the full path) so the triager knows which file
				// to ask the user about.
				errorlog.Record("k8s-init", "warning",
					"skipping invalid kubeconfig file %q during directory scan",
					filepath.Base(path))
			}
		}
	}
	return configs, nil
}

// scrubPathError returns the underlying error cause (e.g. "permission denied",
// "no such file or directory") without the filesystem path that produced it,
// so errorlog entries derived from os.ReadDir / os.Open can safely ship in a
// bug report. Errors that aren't an `*os.PathError` (or whose inner Err is
// nil) are *not* passed through via err.Error() — their text may still
// contain the originating path — so they collapse to a conservative
// "unscrubbable" placeholder. The helper's entire point is the privacy
// contract; a future caller adding a non-PathError must not silently leak.
func scrubPathError(err error) string {
	if err == nil {
		return ""
	}
	var pErr *os.PathError
	if errors.As(err, &pErr) && pErr.Err != nil {
		return pErr.Op + ": " + pErr.Err.Error()
	}
	return "(unscrubbable error — omitted to avoid leaking paths)"
}

// isValidKubeconfig checks if a file is a valid kubeconfig
func isValidKubeconfig(path string) bool {
	// Try to load the file as a kubeconfig
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return false
	}
	// A valid kubeconfig should have at least one context or cluster
	return len(config.Contexts) > 0 || len(config.Clusters) > 0
}

// GetClient returns the K8s clientset
func GetClient() *kubernetes.Clientset {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return k8sClient
}

// GetConfig returns the K8s rest config
func GetConfig() *rest.Config {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return k8sConfig
}

// GetDiscoveryClient returns the K8s discovery client for API resource discovery
func GetDiscoveryClient() *discovery.DiscoveryClient {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return discoveryClient
}

// GetDynamicClient returns the K8s dynamic client for CRD access
func GetDynamicClient() dynamic.Interface {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return dynamicClient
}

// GetKubeconfigPath returns the path to the kubeconfig file used
func GetKubeconfigPath() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return kubeconfigPath
}

// KubeconfigSummary is a non-sensitive snapshot of kubeconfig loading state,
// suitable for inclusion in diagnostic output. It never includes the resolved
// paths themselves, only counts, mode flags, and exec plugin basenames.
type KubeconfigSummary struct {
	Mode                   string   // "in-cluster", "single", "multi-env", "multi-dir", or "" if not initialized
	FileCount              int      // Number of kubeconfig files loaded (0 for in-cluster)
	ContextCount           int      // Number of contexts exposed after client-go merged all files
	EnrichedFromShell      bool     // Desktop app captured KUBECONFIG from login shell
	CurrentContextUsesExec bool     // Current context's AuthInfo uses an exec credential plugin
	ExecPluginsPresent     []string // Unique exec plugin command basenames (any context) resolvable on $PATH
	ExecPluginsMissing     []string // Unique exec plugin command basenames (any context) NOT resolvable on $PATH
}

// GetKubeconfigSummary returns the current kubeconfig loading state for
// diagnostics. All values are safe to include in a bug report.
//
// ExecPluginsPresent/Missing are computed lazily against the *current*
// process PATH at snapshot time (not init time) so a user who installs
// `gke-gcloud-auth-plugin` (or similar) *after* launching Radar sees the
// plugin move from "missing" to "present" in their next snapshot without
// restarting — and a user whose PATH is smaller in a long-running session
// still gets accurate data.
func GetKubeconfigSummary() KubeconfigSummary {
	clientMu.RLock()
	mode := kubeconfigMode
	fileCount := len(kubeconfigPaths)
	if fileCount == 0 && kubeconfigPath != "" {
		fileCount = 1
	}
	contextCount := totalContextCount
	enriched := enrichedKubeconfigFromShell
	currentExec := contextUsesExec
	cmds := append([]string(nil), execPluginCommands...)
	clientMu.RUnlock()

	// LookPath outside the lock — it can stat the filesystem and we don't
	// want to hold clientMu across I/O.
	var present, missing []string
	for _, cmd := range cmds {
		if _, err := exec.LookPath(cmd); err == nil {
			present = append(present, cmd)
		} else {
			missing = append(missing, cmd)
		}
	}

	return KubeconfigSummary{
		Mode:                   mode,
		FileCount:              fileCount,
		ContextCount:           contextCount,
		EnrichedFromShell:      enriched,
		CurrentContextUsesExec: currentExec,
		ExecPluginsPresent:     present,
		ExecPluginsMissing:     missing,
	}
}

// collectExecPluginCommands walks every context in raw and returns:
//
//   - cmds: the unique, sorted basenames of any exec plugin command
//     referenced by a context's AuthInfo. Basenames only — never full
//     paths — so the result is safe to surface in diagnostics.
//   - emptyCommandAuthInfos: the unique, sorted names of AuthInfos that
//     reference an exec block with an empty Command. This is a user
//     misconfiguration that will fail at auth time — the caller should
//     record each one via errorlog so it shows up in a bug report.
//
// Orphan AuthInfos (not referenced by any context) are intentionally
// skipped: they can't cause a context switch to fail, so there's no
// signal in them.
//
// The function is pure on its *clientcmdapi.Config argument and touches
// no shared state, so it is safe to call without any lock held. Callers
// are responsible for assigning the returned cmds slice to the package
// global `execPluginCommands` under clientMu.Lock.
func collectExecPluginCommands(raw *clientcmdapi.Config) (cmds []string, emptyCommandAuthInfos []string) {
	if raw == nil {
		return nil, nil
	}
	seenCmds := make(map[string]struct{})
	seenEmpty := make(map[string]struct{})
	for _, ctx := range raw.Contexts {
		if ctx == nil {
			continue
		}
		ai, ok := raw.AuthInfos[ctx.AuthInfo]
		if !ok || ai == nil || ai.Exec == nil {
			continue
		}
		if ai.Exec.Command == "" {
			// Malformed exec block — surface via the second return
			// so the caller can record a warning. Dedupe by AuthInfo
			// name since the same AuthInfo may be referenced by
			// multiple contexts.
			if _, dup := seenEmpty[ctx.AuthInfo]; !dup {
				seenEmpty[ctx.AuthInfo] = struct{}{}
				emptyCommandAuthInfos = append(emptyCommandAuthInfos, ctx.AuthInfo)
			}
			continue
		}
		base := filepath.Base(ai.Exec.Command)
		if _, dup := seenCmds[base]; dup {
			continue
		}
		seenCmds[base] = struct{}{}
		cmds = append(cmds, base)
	}
	sort.Strings(cmds)
	sort.Strings(emptyCommandAuthInfos)
	return cmds, emptyCommandAuthInfos
}

// recordEmptyCommandWarning records a single aggregated errorlog entry for a
// batch of AuthInfos that reference exec plugins with an empty Command. A
// single errorlog call (rather than one-per-name) is deliberate — a
// pathological or corrupted kubeconfig with hundreds of broken AuthInfos
// would otherwise flood the 200-entry ring buffer and evict unrelated
// diagnostics. Listing is capped at the first maxListed names so the
// message text itself stays bounded; the count is always accurate.
func recordEmptyCommandWarning(source string, authInfos []string) {
	if len(authInfos) == 0 {
		return
	}
	const maxListed = 10
	listed := authInfos
	truncated := false
	if len(listed) > maxListed {
		listed = listed[:maxListed]
		truncated = true
	}
	suffix := ""
	if truncated {
		suffix = fmt.Sprintf(" (+%d more)", len(authInfos)-maxListed)
	}
	errorlog.Record(source, "warning",
		"%d AuthInfo(s) reference exec plugins with empty command — context switches to these identities will fail at auth time: %v%s",
		len(authInfos), listed, suffix)
}

// WriteKubeconfigForCurrentContext creates a temporary kubeconfig file with
// current-context set to Radar's active context. The caller must remove the
// file when done. Returns the temp file path.
func WriteKubeconfigForCurrentContext() (string, error) {
	clientMu.RLock()
	ctx := contextName
	registry := contextRegistry
	fileConfigs := perFileConfigs
	singlePath := kubeconfigPath
	clientMu.RUnlock()

	var rawConfig clientcmdapi.Config
	var currentContextForFile string

	if registry != nil {
		// Isolated-load mode: write only the current context's source file,
		// with CurrentContext set to the name it has inside that file. This
		// avoids leaking other files' (possibly colliding) definitions into
		// the temp kubeconfig we hand out.
		entry, ok := registry[ctx]
		if !ok {
			return "", fmt.Errorf("current context %q not found in registry", ctx)
		}
		cfg, ok := fileConfigs[entry.SourceFile]
		if !ok {
			return "", fmt.Errorf("no cached config for file %q", entry.SourceFile)
		}
		rawConfig = *cfg.DeepCopy()
		currentContextForFile = entry.InFileName
	} else {
		if singlePath == "" {
			return "", fmt.Errorf("kubeconfig path not set")
		}
		loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: singlePath}
		loaded, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules, &clientcmd.ConfigOverrides{},
		).RawConfig()
		if err != nil {
			return "", fmt.Errorf("failed to load kubeconfig: %w", err)
		}
		rawConfig = loaded
		currentContextForFile = ctx
	}

	if currentContextForFile != "" {
		rawConfig.CurrentContext = currentContextForFile
	}

	tmpFile, err := os.CreateTemp("", "radar-kubeconfig-*.yaml")
	if err != nil {
		return "", fmt.Errorf("failed to create temp kubeconfig: %w", err)
	}
	tmpFile.Close()

	if err := clientcmd.WriteToFile(rawConfig, tmpFile.Name()); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp kubeconfig: %w", err)
	}

	return tmpFile.Name(), nil
}

// GetContextName returns the current kubeconfig context name
func GetContextName() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return contextName
}

// ActiveClusterContext is the cluster-identity stamp for timeline events and
// the filter value for cluster-scoped timeline reads. In-cluster mode has no
// kubeconfig context name; the sentinel keeps those events distinguishable
// from legacy rows recorded before provenance was tracked (empty string).
func ActiveClusterContext() string {
	if name := GetContextName(); name != "" {
		return name
	}
	return "in-cluster"
}

// GetClusterName returns the current cluster name from kubeconfig
func GetClusterName() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return clusterName
}

// GetContextNamespace returns the default namespace from the kubeconfig context
func GetContextNamespace() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return contextNamespace
}

// UsesExecAuth returns true if the current context uses an exec credential plugin.
// This covers any plugin configured in kubeconfig AuthInfo.Exec (e.g., GKE, EKS,
// AKS, OIDC/Dex/Keycloak, Teleport). These plugins can hang when credentials
// expire, causing generic timeouts instead of auth errors.
func UsesExecAuth() bool {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return contextUsesExec
}

// SetFallbackNamespace sets an explicit namespace to use as RBAC fallback
// (typically from the --namespace CLI flag). Used when the kubeconfig context
// doesn't specify a namespace but the user wants namespace-scoped access.
func SetFallbackNamespace(ns string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	fallbackNamespace = ns
	fallbackNamespaces = dedupeNamespacesLocked([]string{ns})
	// Record the context this --namespace applies to, so it only pins the cache
	// scope while we're on that context (see GetNamespaceScopeTarget).
	fallbackNamespaceContext = contextName
}

// SetFallbackNamespaces sets explicit namespace candidates from --namespaces.
// The first namespace also becomes the legacy fallback namespace so older
// single-namespace code paths have a deterministic default, but the full list
// is preserved for RBAC probing and namespace discovery.
func SetFallbackNamespaces(namespaces []string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	fallbackNamespaces = dedupeNamespacesLocked(namespaces)
	if len(fallbackNamespaces) > 0 {
		fallbackNamespace = fallbackNamespaces[0]
	} else {
		fallbackNamespace = ""
	}
	fallbackNamespaceContext = contextName
}

func dedupeNamespacesLocked(namespaces []string) []string {
	if len(namespaces) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(namespaces))
	out := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	return out
}

func fallbackNamespaceCandidatesLocked() []string {
	candidates := make([]string, 0, len(fallbackNamespaces)+1)
	candidates = append(candidates, fallbackNamespaces...)
	if fallbackNamespace != "" {
		candidates = append(candidates, fallbackNamespace)
	}
	return dedupeNamespacesLocked(candidates)
}

// SetNamespaceScopeOverride sets the runtime namespace used by local
// --namespace-scope rescope. It is separate from fallbackNamespace so a real
// kubeconfig context switch can drop the runtime pick without losing the
// explicit --namespace startup flag.
func SetNamespaceScopeOverride(ns string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	namespaceScopeOverride = ns
}

func ClearNamespaceScopeOverride() {
	clientMu.Lock()
	defer clientMu.Unlock()
	namespaceScopeOverride = ""
}

func SetNamespaceScopePreferenceResolver(resolver func(contextName string) (string, bool)) {
	clientMu.Lock()
	defer clientMu.Unlock()
	namespaceScopeResolver = resolver
}

func RestoreNamespaceScopePreference(contextName string) {
	clientMu.RLock()
	resolver := namespaceScopeResolver
	clientMu.RUnlock()
	if resolver == nil {
		return
	}
	if namespace, ok := resolver(contextName); ok && namespace != "" {
		SetNamespaceScopeOverride(namespace)
	}
}

// GetNamespaceScopeTarget returns the namespace used when informer caches are
// explicitly namespace-scoped. A local runtime rescope wins first, then the
// explicit CLI namespace (only while on the context it was set for), then the
// kubeconfig context namespace.
func GetNamespaceScopeTarget() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if namespaceScopeOverride != "" {
		return namespaceScopeOverride
	}
	// --namespace is an *initial* filter, so it only pins the scope while we're on
	// the context it was set for. After a cross-cluster switch the new context's own
	// namespace takes over — the stale startup value must not follow across clusters.
	if fallbackNamespace != "" && contextName == fallbackNamespaceContext {
		return fallbackNamespace
	}
	return contextNamespace
}

// ProspectiveNamespaceScopeTarget resolves what GetNamespaceScopeTarget would
// return after switching to newContext, without mutating any client state. The
// context-switch path uses it to reject a --namespace-scope switch that would
// land on a context with no usable scope target *before* tearing down the
// current caches. Keep its precedence in sync with GetNamespaceScopeTarget:
// saved pick → startup --namespace (only for its context) → context namespace.
func ProspectiveNamespaceScopeTarget(newContext string) string {
	clientMu.RLock()
	resolver := namespaceScopeResolver
	startupFallback := fallbackNamespace
	startupContext := fallbackNamespaceContext
	clientMu.RUnlock()

	if resolver != nil {
		if ns, ok := resolver(newContext); ok && ns != "" {
			return ns
		}
	}
	if startupFallback != "" && newContext == startupContext {
		return startupFallback
	}
	// GetAvailableContexts reads the kubeconfig off disk and takes clientMu, so
	// it must run after the snapshot above is released.
	if contexts, err := GetAvailableContexts(); err == nil {
		for _, c := range contexts {
			if c.Name == newContext {
				return c.Namespace
			}
		}
	}
	return ""
}

// GetEffectiveNamespace returns the namespace to use for RBAC fallback checks.
// Precedence: kubeconfig context namespace > --namespace flag.
func GetEffectiveNamespace() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if contextNamespace != "" {
		return contextNamespace
	}
	return fallbackNamespace
}

// HasNamespaceFallback reports whether the current kubeconfig/context provides
// a namespace fallback (kubeconfig context namespace or --namespace flag).
func HasNamespaceFallback() bool {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return contextNamespace != "" || fallbackNamespace != "" || len(fallbackNamespaces) > 0
}

// GetAccessibleNamespaces returns the list of namespaces the user has
// access to plus a flag indicating whether the list is authoritative.
//
//   - If the cluster-wide `list namespaces` succeeds (cluster-wide read),
//     returns every namespace and authoritative=true.
//   - On 403/401 the user is namespace-restricted; returns a best-effort
//     short list (kubeconfig context namespace + --namespace flag, deduped)
//     and authoritative=false.
//   - On any other (transient) error, returns the same best-effort list
//     with authoritative=false AND logs the error so a flapping apiserver
//     surfaces in diagnostics rather than silently degrading the UI.
func GetAccessibleNamespaces(ctx context.Context) ([]string, bool) {
	client := GetClient()
	if client == nil {
		return nil, false
	}

	listCtx, cancel := context.WithTimeout(ctx, NamespaceListTimeout)
	defer cancel()

	list, err := client.CoreV1().Namespaces().List(listCtx, metav1.ListOptions{})
	if err == nil {
		names := make([]string, 0, len(list.Items))
		for _, ns := range list.Items {
			names = append(names, ns.Name)
		}
		sort.Strings(names)
		return names, true
	}

	if !apierrors.IsForbidden(err) && !apierrors.IsUnauthorized(err) {
		log.Printf("[k8s] GetAccessibleNamespaces: non-auth error listing namespaces: %v (falling back to best-effort short list)", err)
	}

	// Cluster-wide list denied (or transient). Best-effort fallback so
	// the picker isn't empty for a namespace-scoped user.
	seen := map[string]bool{}
	var fallback []string
	clientMu.RLock()
	candidates := append([]string{contextNamespace}, fallbackNamespaceCandidatesLocked()...)
	for _, ns := range candidates {
		if ns != "" && !seen[ns] {
			seen[ns] = true
			fallback = append(fallback, ns)
		}
	}
	clientMu.RUnlock()
	sort.Strings(fallback)
	return fallback, false
}

// ForceInCluster overrides in-cluster detection for testing
var ForceInCluster bool

// IsInCluster returns true if running inside a Kubernetes cluster
func IsInCluster() bool {
	return ForceInCluster || (kubeconfigPath == "" && len(kubeconfigPaths) == 0)
}

// ContextInfo represents information about a kubeconfig context
type ContextInfo struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	User      string `json:"user"`
	Namespace string `json:"namespace"`
	IsCurrent bool   `json:"isCurrent"`
	// Source labels the kubeconfig file this context came from
	// (e.g. "kube-cluster-paris" or "prod"). Set only in multi-file
	// mode; populated for every context — not just colliding ones — so
	// the dropdown can show provenance even without ambiguity.
	Source string `json:"source,omitempty"`
}

// GetAvailableContexts returns all available contexts from the kubeconfig
func GetAvailableContexts() ([]ContextInfo, error) {
	if IsInCluster() {
		// In-cluster mode - only one "context" available
		return []ContextInfo{
			{
				Name:      "in-cluster",
				Cluster:   "in-cluster",
				User:      "service-account",
				Namespace: "",
				IsCurrent: true,
			},
		}, nil
	}

	// Reconcile registry against disk before reading. This is the
	// only refresh point in multi-file (isolated-load) mode — without
	// it, kubeconfigs that were rewritten or deleted on disk after
	// startup keep showing up in the dropdown until the user
	// restarts Radar (the "junk clusters" complaint).
	//
	// refreshContextRegistry returns NEW maps when anything changes,
	// so we publish them atomically under the write lock. Snapshot
	// readers (SwitchContext, WriteKubeconfigForCurrentContext) take
	// bare references under RLock and use them after the unlock — that
	// pattern is only safe as long as the maps they captured are never
	// mutated. Returning fresh maps preserves that invariant.
	clientMu.Lock()
	if contextRegistry != nil {
		// Lazy init: a future code path that promotes single-file mode
		// to isolated-load without touching perFileMtimes would leave
		// it nil. Seeding it here is safe because we always hold the
		// write lock and refresh's nil guard catches it too.
		if perFileMtimes == nil {
			perFileMtimes = make(map[string]time.Time, len(perFileConfigs))
		}
		newRegistry, newFileConfigs, newFileMtimes, changed := refreshContextRegistry(
			contextRegistry, perFileConfigs, perFileMtimes,
		)
		if changed {
			contextRegistry = newRegistry
			perFileConfigs = newFileConfigs
			perFileMtimes = newFileMtimes
		}
	}
	registry := contextRegistry
	fileConfigs := perFileConfigs
	currentCtx := contextName
	clientMu.Unlock()

	if registry != nil {
		// Isolated-load mode: enumerate every registered context, pulling
		// cluster/user/namespace from the file it originally lives in.
		// No merge happens — shared names across files stay distinct.
		// Iterating outside the lock is safe because refresh publishes
		// fresh maps on change rather than mutating in place, so the
		// snapshot we captured is frozen.
		contexts := make([]ContextInfo, 0, len(registry))
		for qName, entry := range registry {
			cfg, ok := fileConfigs[entry.SourceFile]
			if !ok {
				continue
			}
			ctx, ok := cfg.Contexts[entry.InFileName]
			if !ok || ctx == nil {
				continue
			}
			contexts = append(contexts, ContextInfo{
				Name:      qName,
				Cluster:   ctx.Cluster,
				User:      ctx.AuthInfo,
				Namespace: ctx.Namespace,
				IsCurrent: qName == currentCtx,
				Source:    kubeconfigSourceLabel(entry.SourceFile),
			})
		}
		return contexts, nil
	}

	// Single-file fallback: load the one file and enumerate its contexts.
	kubeconfig := kubeconfigPath
	if kubeconfig == "" {
		return nil, fmt.Errorf("kubeconfig path not set")
	}
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	if currentCtx == "" {
		// Fall back to kubeconfig's current-context if we haven't switched yet
		currentCtx = rawConfig.CurrentContext
	}
	contexts := make([]ContextInfo, 0, len(rawConfig.Contexts))
	for name, ctx := range rawConfig.Contexts {
		contexts = append(contexts, ContextInfo{
			Name:      name,
			Cluster:   ctx.Cluster,
			User:      ctx.AuthInfo,
			Namespace: ctx.Namespace,
			IsCurrent: name == currentCtx,
		})
	}
	return contexts, nil
}

// SwitchContext switches the K8s client to use a different context
// This reinitializes all clients (k8sClient, discoveryClient, dynamicClient)
func SwitchContext(name string) error {
	if IsInCluster() {
		return fmt.Errorf("cannot switch context when running in-cluster")
	}

	// Snapshot registry-related globals under the lock. MergeAndSwitchContext
	// can mutate all three concurrently, so reads have to be atomic as a set.
	clientMu.RLock()
	registry := contextRegistry
	pathsSnapshot := append([]string(nil), kubeconfigPaths...)
	configsSnapshot := make(map[string]*clientcmdapi.Config, len(perFileConfigs))
	for k, v := range perFileConfigs {
		configsSnapshot[k] = v
	}
	clientMu.RUnlock()

	var loadingRules *clientcmd.ClientConfigLoadingRules
	var overrideContextName string

	if registry != nil {
		// Isolated-load mode: resolve the qualified name to the source file
		// and load only that file. Every other file is ignored here, so
		// colliding user/cluster names in sibling files can't pollute this
		// context's credentials (issue #519).
		entry, ok := registry[name]
		if !ok {
			return fmt.Errorf("context %q not found in kubeconfig", name)
		}
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: entry.SourceFile}
		overrideContextName = entry.InFileName
	} else {
		kubeconfig := kubeconfigPath
		if kubeconfig == "" {
			return fmt.Errorf("kubeconfig path not set")
		}
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
		overrideContextName = name
	}

	// Build config with the new context
	configOverrides := &clientcmd.ConfigOverrides{CurrentContext: overrideContextName}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	// Verify the context exists
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	ctx, ok := rawConfig.Contexts[overrideContextName]
	if !ok {
		return fmt.Errorf("context %q not found in kubeconfig", name)
	}

	// Build the REST config for the new context
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to build config for context %q: %w", name, err)
	}

	// Apply the same QPS/Burst settings as initial client creation.
	// Without this, new clients use the default 5 QPS / 10 Burst, causing
	// severe client-side throttling during CRD discovery after context switch.
	config.QPS = 50
	config.Burst = 100

	// Create new clients
	newK8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create k8s client for context %q: %w", name, err)
	}

	newDiscoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create discovery client for context %q: %w", name, err)
	}

	newDynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client for context %q: %w", name, err)
	}

	// Update global variables atomically
	usesExec := false
	if ai, ok := rawConfig.AuthInfos[ctx.AuthInfo]; ok && ai.Exec != nil {
		usesExec = true
	}
	// Re-collect exec plugin commands. In isolated-load mode rawConfig only
	// reflects the one chosen file, so we walk the full registry to keep
	// the diagnostic honest about which plugins span the whole configuration.
	var execCmds, emptyAIs []string
	var totalContexts int
	if registry != nil {
		execCmds, emptyAIs = aggregateExecPluginCommands(pathsSnapshot, configsSnapshot)
		totalContexts = len(registry)
	} else {
		execCmds, emptyAIs = collectExecPluginCommands(&rawConfig)
		totalContexts = len(rawConfig.Contexts)
	}
	if len(emptyAIs) > 0 {
		recordEmptyCommandWarning("context-switch", emptyAIs)
	}

	clientMu.Lock()
	k8sConfig = config
	k8sClient = newK8sClient
	discoveryClient = newDiscoveryClient
	dynamicClient = newDynamicClient
	contextName = name
	clusterName = ctx.Cluster
	contextNamespace = ctx.Namespace
	contextUsesExec = usesExec
	totalContextCount = totalContexts
	execPluginCommands = execCmds
	clientMu.Unlock()

	return nil
}

// capiKubeconfigs tracks temp kubeconfig files by context name to avoid accumulation.
var capiKubeconfigs = make(map[string]string) // contextName -> tmpPath

// MergeAndSwitchContext writes the provided kubeconfig data to a temporary
// file and registers its context so that Radar can switch to it. Returns
// (qualifiedName, tmpPath, error): qualifiedName is the identifier the caller
// must pass to PerformContextSwitch, and may differ from the input contextName
// if another file already owns that name (the registry disambiguates via
// qualifyContextName). tmpPath is the on-disk location of the kubeconfig,
// exposed for diagnostics / logging only.
//
// If Radar started in single-file mode, the first CAPI merge promotes it
// into isolated-load mode by seeding the registry with the original
// kubeconfig plus the new CAPI file — otherwise subsequent CAPI merges
// would silently revert to client-go's Precedence behavior (issue #519).
//
// Concurrency: the entire decision is serialized under clientMu.Lock. The
// input contextName is the stable key for reuse across reconnects (CAPI
// re-emits the same context name each time for the same workload cluster),
// so we can dedupe without having to reverse-lookup the qualified form.
func MergeAndSwitchContext(kubeconfigData []byte, contextName string) (string, string, error) {
	newConfig, err := clientcmd.Load(kubeconfigData)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse kubeconfig: %w", err)
	}
	if _, ok := newConfig.Contexts[contextName]; !ok {
		return "", "", fmt.Errorf("context %q not found in provided kubeconfig", contextName)
	}

	// Hold clientMu for the entire reuse-check + registration path so two
	// concurrent CAPI merges for the same workload cluster can't both see
	// "no existing path" and both create orphan temp files.
	clientMu.Lock()
	defer clientMu.Unlock()

	// Fast path: same CAPI context was registered before. Overwrite the
	// existing temp file so the user gets a fresh exec plugin config, and
	// return the qualified name we assigned on the original merge.
	if existingPath, ok := capiKubeconfigs[contextName]; ok {
		if err := clientcmd.WriteToFile(*newConfig, existingPath); err == nil {
			// Refresh the cached parsed config so subsequent GetAvailableContexts
			// calls reflect any changes in the incoming YAML. Also bump the
			// cached mtime so the next refresh doesn't see a stale value
			// (the WriteToFile above just changed the file's mtime) and
			// uselessly re-parse a file we've already re-parsed here.
			if parsed, perr := clientcmd.LoadFromFile(existingPath); perr == nil {
				perFileConfigs[existingPath] = parsed
				if perFileMtimes != nil {
					if info, serr := os.Stat(existingPath); serr == nil {
						perFileMtimes[existingPath] = info.ModTime()
					}
				}
			}
			qName := findQualifiedNameForPath(contextRegistry, existingPath, contextName)
			if qName == "" {
				// Registry is missing the entry somehow — rebuild it below by
				// falling through to the new-file path. Scrub the stale map
				// entry so we don't keep returning it.
				delete(capiKubeconfigs, contextName)
			} else {
				log.Printf("[capi] Updated existing kubeconfig for context %q: %q", contextName, existingPath)
				return qName, existingPath, nil
			}
		}
		// Overwrite failed — fall through to create a new temp file.
	}

	// Write to a new temp file.
	tmpFile, err := os.CreateTemp("", "radar-capi-kubeconfig-*.yaml")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp kubeconfig: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	if err := clientcmd.WriteToFile(*newConfig, tmpPath); err != nil {
		os.Remove(tmpPath)
		return "", "", fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	// Build a local snapshot of the registry additions we're about to make,
	// then validate before committing to globals. If validation fails we
	// remove the temp file and leave the globals untouched — no half-state.
	var newRegistry map[string]contextEntry
	var newFileConfigs map[string]*clientcmdapi.Config
	var newFileMtimes map[string]time.Time
	var newPaths []string

	if contextRegistry == nil {
		// Promote single-file mode to isolated-load mode.
		seedPaths := []string{}
		if kubeconfigPath != "" {
			seedPaths = append(seedPaths, kubeconfigPath)
		}
		seedPaths = append(seedPaths, tmpPath)
		registry, fileConfigs := buildContextRegistry(seedPaths)
		if _, hasTmp := fileConfigs[tmpPath]; !hasTmp {
			os.Remove(tmpPath)
			return "", "", fmt.Errorf("internal: failed to register CAPI kubeconfig %s", tmpPath)
		}
		newRegistry = registry
		newFileConfigs = fileConfigs
		// Seed the mtime cache for the same set of files. Without
		// this, the next refresh would write to a nil map
		// (perFileMtimes is package-level and stays nil through the
		// promotion). Refresh's nil-map guard would also catch this,
		// but seeding here keeps the invariant "perFileMtimes is
		// non-nil whenever contextRegistry is non-nil".
		newFileMtimes = make(map[string]time.Time, len(seedPaths))
		for _, p := range seedPaths {
			if info, err := os.Stat(p); err == nil {
				newFileMtimes[p] = info.ModTime()
			}
		}
		newPaths = seedPaths
	} else {
		cfg, err := clientcmd.LoadFromFile(tmpPath)
		if err != nil {
			os.Remove(tmpPath)
			return "", "", fmt.Errorf("failed to re-load temp kubeconfig: %w", err)
		}
		// Copy-on-write: stage new maps / slice so we don't publish a
		// partially-updated registry on any error path below.
		newRegistry = make(map[string]contextEntry, len(contextRegistry)+len(cfg.Contexts))
		for k, v := range contextRegistry {
			newRegistry[k] = v
		}
		newFileConfigs = make(map[string]*clientcmdapi.Config, len(perFileConfigs)+1)
		for k, v := range perFileConfigs {
			newFileConfigs[k] = v
		}
		newFileConfigs[tmpPath] = cfg
		newFileMtimes = make(map[string]time.Time, len(perFileMtimes)+1)
		for k, v := range perFileMtimes {
			newFileMtimes[k] = v
		}
		if info, err := os.Stat(tmpPath); err == nil {
			newFileMtimes[tmpPath] = info.ModTime()
		}
		newPaths = append(append([]string(nil), kubeconfigPaths...), tmpPath)
		for name := range cfg.Contexts {
			qName := qualifyContextName(newRegistry, name, tmpPath)
			newRegistry[qName] = contextEntry{
				SourceFile: tmpPath,
				InFileName: name,
			}
		}
	}

	qualifiedName := findQualifiedNameForPath(newRegistry, tmpPath, contextName)
	if qualifiedName == "" {
		os.Remove(tmpPath)
		return "", "", fmt.Errorf("internal: failed to register context %q from %s", contextName, tmpPath)
	}

	// Commit. All globals updated atomically under the single Lock held above.
	contextRegistry = newRegistry
	perFileConfigs = newFileConfigs
	perFileMtimes = newFileMtimes
	kubeconfigPaths = newPaths
	capiKubeconfigs[contextName] = tmpPath

	log.Printf("[capi] Added workload cluster kubeconfig: %q (context: %q)", tmpPath, qualifiedName)
	return qualifiedName, tmpPath, nil
}

// findQualifiedNameForPath returns the qualified registry name of the given
// (file, originalContextName) pair, or "" if none is registered. Used by the
// CAPI merge path to learn the post-disambiguation identifier.
func findQualifiedNameForPath(registry map[string]contextEntry, file, inFileName string) string {
	for qName, entry := range registry {
		if entry.SourceFile == file && entry.InFileName == inFileName {
			return qName
		}
	}
	return ""
}
