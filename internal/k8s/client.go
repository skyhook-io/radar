package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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
	k8sClient                    *kubernetes.Clientset
	k8sConfig                    *rest.Config
	discoveryClient              *discovery.DiscoveryClient
	dynamicClient                dynamic.Interface
	initOnce                     sync.Once
	initErr                      error
	kubeconfigPath               string
	kubeconfigPaths              []string // Multiple kubeconfig paths when using directories, KUBECONFIG, or combined sources
	kubeconfigMode               string   // One of: "in-cluster", "single", "multi-env", "multi-dir", "multi-source"
	kubeconfigDirectoryFileCount int
	kubeconfigDirectoryPaths     map[string]struct{}
	kubeconfigEnvWasIgnored      bool
	kubeconfigEnvIgnoredReason   string
	totalContextCount            int // Total number of contexts exposed across all kubeconfig files
	// contextRegistry maps each user-facing context name to its source file and
	// the name it has inside that file. Populated for directory-backed loading,
	// multiple explicit paths, or CAPI-added files.
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
	contextBinding     string
	activeSourceFile   string
	activeSourceName   string
	activeSourceConfig *clientcmdapi.Config
	clusterName        string
	contextNamespace   string   // Default namespace from kubeconfig context
	fallbackNamespace  string   // Explicit namespace from --namespace flag
	fallbackNamespaces []string // Explicit namespace candidates from --namespaces flag
	// fallbackNamespacesExplicit distinguishes SetFallbackNamespaces (the
	// plural --namespaces flag, which also seeds the per-user picker) from
	// SetFallbackNamespace (--namespace, which only steers RBAC probing).
	fallbackNamespacesExplicit bool
	// fallbackNamespaceContext is the context that was active when --namespace was
	// set at startup. --namespace is an *initial* value, so it only pins the cache
	// scope for that context — after switching clusters, the scope target comes
	// from the new context's namespace (or a saved pick), not the stale startup value.
	fallbackNamespaceContext string
	namespaceScopeOverride   string // Runtime namespace selected by local --namespace-scope rescope
	namespaceScopeResolver   func(contextName string) (string, bool)
	contextUsesExec          bool // True when the current context uses an exec credential plugin
	// execPluginCommands is the set of unique exec-auth plugin command basenames
	// referenced by any context in the resolved kubeconfig sources. Populated from
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
	// initializationStarted distinguishes package state that has not been
	// initialized yet from a failed local initialization. Several tests and
	// embedded callers inspect IsInCluster before initialization, where the
	// historical empty-path heuristic still applies.
	initializationStarted bool
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
	// PreferredContext is the context to start on instead of the kubeconfig's
	// current-context. Ignored when it doesn't resolve, so a stale preference
	// can never keep Radar from starting.
	PreferredContext ContextRef
}

type kubeconfigSources struct {
	paths                      []string
	mode                       string
	useRegistry                bool
	tryInCluster               bool
	ignoredKubeconfigEnv       bool
	ignoredKubeconfigEnvReason string
	directoryFileCount         int
	directoryPaths             []string
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
	// Publish initialization as one coherent state transition. Readers may run
	// concurrently in embedded uses and on startup failure paths; holding the
	// same lock they use keeps them from observing a mode without its matching
	// paths, registry, or clients.
	clientMu.Lock()
	defer clientMu.Unlock()
	initializationStarted = true

	sources, err := resolveKubeconfigSources(opts, os.Getenv("KUBECONFIG"), homedir.HomeDir())
	if err != nil {
		return err
	}
	kubeconfigEnvWasIgnored = sources.ignoredKubeconfigEnv
	kubeconfigEnvIgnoredReason = sources.ignoredKubeconfigEnvReason
	kubeconfigDirectoryFileCount = sources.directoryFileCount
	kubeconfigDirectoryPaths = make(map[string]struct{}, len(sources.directoryPaths))
	for _, path := range sources.directoryPaths {
		kubeconfigDirectoryPaths[path] = struct{}{}
	}
	if sources.ignoredKubeconfigEnv {
		log.Printf("KUBECONFIG is set but ignored: %s", sources.ignoredKubeconfigEnvReason)
		errorlog.Record("k8s-init", "warning",
			"KUBECONFIG was ignored: %s", sources.ignoredKubeconfigEnvReason)
	}

	if sources.tryInCluster {
		config, err = rest.InClusterConfig()
		if err == nil {
			contextName = "in-cluster"
			contextBinding = ""
			activeSourceFile = ""
			activeSourceName = ""
			activeSourceConfig = nil
			clusterName = "in-cluster"
			kubeconfigMode = "in-cluster"
			if !opts.PreferredContext.Empty() {
				log.Printf("[k8s-init] ignoring preferred context %q: running in-cluster", opts.PreferredContext.Name)
			}
		}
	}

	if config == nil {
		if len(sources.paths) == 0 {
			return fmt.Errorf("in-cluster config is unavailable and no home directory was found for the default kubeconfig")
		}
		if sources.tryInCluster {
			path := sources.paths[0]
			if err := validateKubeconfigFileType(path); err != nil {
				reason := kubeconfigDiagnosticError(err)
				errorlog.Record("k8s-init", "error", "default kubeconfig %q is unusable: %s",
					filepath.Base(path), reason)
				return fmt.Errorf("default kubeconfig %q is unusable: %w", path, err)
			}
		}
		var loadingRules *clientcmd.ClientConfigLoadingRules
		configOverrides := &clientcmd.ConfigOverrides{}

		if sources.useRegistry {
			kubeconfigPaths = sources.paths
			kubeconfigMode = sources.mode
			lr, ovr, err := setupIsolatedLoad(sources.paths, opts.PreferredContext)
			if err != nil {
				return err
			}
			kubeconfigDirectoryFileCount = loadedDirectoryKubeconfigCount(perFileConfigs, kubeconfigDirectoryPaths)
			loadingRules, configOverrides = lr, ovr
			log.Printf("Using %d kubeconfig files in %s isolated-load mode", len(sources.paths), sources.mode)
		} else {
			if len(sources.paths) != 1 {
				return fmt.Errorf("resolved kubeconfig source has %d paths in direct mode", len(sources.paths))
			}
			kubeconfigPath = sources.paths[0]
			kubeconfigMode = sources.mode
			loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
			applyContextPreference(kubeconfigPath, opts.PreferredContext, configOverrides)
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
				"RawConfig() failed; context metadata and diagnostic counts unavailable: %s", kubeconfigDiagnosticError(rawErr))
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
					activeSourceFile = entry.SourceFile
					activeSourceName = entry.InFileName
					activeSourceConfig = rawConfig.DeepCopy()
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
				if configOverrides.CurrentContext != "" {
					contextName = configOverrides.CurrentContext
				}
				contextBinding = sourceContextBinding(kubeconfigPath, contextName)
				activeSourceFile = kubeconfigPath
				activeSourceName = contextName
				activeSourceConfig = rawConfig.DeepCopy()
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
		}

		config, err = kubeConfig.ClientConfig()
		if err != nil {
			// Record to errorlog so the failure lands in the diagnostics
			// snapshot's recentErrors. Include only the file count and mode —
			// never the kubeconfig paths — so the snapshot stays shareable.
			errorlog.Record("k8s-init", "error",
				"failed to build kubeconfig client config (mode=%s, files=%d): %s",
				kubeconfigMode, len(kubeconfigPaths), kubeconfigDiagnosticError(err))
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

	clients, err := newSharedKubernetesClients(config)
	if err != nil {
		return err
	}

	k8sConfig = config
	k8sClient = clients.clientset
	discoveryClient = clients.discovery
	dynamicClient = clients.dynamic
	activeClientGeneration = clients.generation

	return nil
}

func resolveKubeconfigSources(opts InitOptions, kubeconfigEnv, homeDir string) (kubeconfigSources, error) {
	var primaryPaths []string
	if opts.KubeconfigPath != "" {
		primaryPath, err := normalizeKubeconfigPath(opts.KubeconfigPath, homeDir)
		if err != nil {
			return kubeconfigSources{}, fmt.Errorf("normalize configured kubeconfig: %w", err)
		}
		if err := validateKubeconfigFileType(primaryPath); err != nil {
			errorlog.Record("k8s-init", "error", "primary kubeconfig unusable (%q): %s",
				filepath.Base(primaryPath), kubeconfigDiagnosticError(err))
			return kubeconfigSources{}, fmt.Errorf("configured primary kubeconfig is unusable (%q): %w",
				filepath.Base(primaryPath), err)
		}
		primaryPaths = []string{primaryPath}
	}

	if len(opts.KubeconfigDirs) > 0 {
		dirs, err := normalizeKubeconfigDirectories(opts.KubeconfigDirs, homeDir)
		if err != nil {
			return kubeconfigSources{}, fmt.Errorf("normalize kubeconfig directories: %w", err)
		}
		discovered := discoverKubeconfigs(dirs)
		if len(primaryPaths) > 0 {
			if ok, cause := hasUsableKubeconfig(primaryPaths[0]); !ok {
				if cause != nil {
					errorlog.Record("k8s-init", "error", "primary kubeconfig unusable (%q): %s",
						filepath.Base(primaryPaths[0]), kubeconfigDiagnosticError(cause))
					return kubeconfigSources{}, fmt.Errorf("configured primary kubeconfig is unusable (%q): %w",
						filepath.Base(primaryPaths[0]), cause)
				}
				errorlog.Record("k8s-init", "error", "primary kubeconfig contains no contexts")
				return kubeconfigSources{}, fmt.Errorf("configured primary kubeconfig contains no usable contexts")
			}
		}
		if len(primaryPaths) == 0 && len(discovered) == 0 {
			return kubeconfigSources{}, fmt.Errorf("no valid kubeconfig files found in directories: %v", opts.KubeconfigDirs)
		}

		paths := dedupeKubeconfigPaths(append(append([]string(nil), primaryPaths...), discovered...))
		directoryPaths := paths
		if len(primaryPaths) > 0 {
			directoryPaths = paths[1:]
		}
		mode := "multi-dir"
		ignoredReason := ""
		if len(primaryPaths) > 0 {
			mode = "multi-source"
		}
		if kubeconfigEnv != "" {
			ignoredReason = "directories-only configuration"
			if len(primaryPaths) > 0 {
				ignoredReason = "primary kubeconfig configured"
			}
		}
		return kubeconfigSources{
			paths:                      paths,
			mode:                       mode,
			useRegistry:                true,
			ignoredKubeconfigEnv:       ignoredReason != "",
			ignoredKubeconfigEnvReason: ignoredReason,
			directoryFileCount:         len(paths) - len(primaryPaths),
			directoryPaths:             directoryPaths,
		}, nil
	}

	if len(primaryPaths) == 1 {
		ignoredReason := ""
		if kubeconfigEnv != "" {
			ignoredReason = "primary kubeconfig configured"
		}
		return kubeconfigSources{
			paths:                      primaryPaths,
			mode:                       "single",
			ignoredKubeconfigEnv:       ignoredReason != "",
			ignoredKubeconfigEnvReason: ignoredReason,
		}, nil
	}

	if kubeconfigEnv != "" {
		paths, err := normalizeKubeconfigPathList(kubeconfigEnv, homeDir)
		if err != nil {
			return kubeconfigSources{}, fmt.Errorf("normalize KUBECONFIG: %w", err)
		}
		if len(paths) == 0 {
			return kubeconfigSources{}, fmt.Errorf("no kubeconfig paths resolved")
		}
		regularPaths := make([]string, 0, len(paths))
		firstSkipped := ""
		for _, path := range paths {
			if err := validateKubeconfigFileType(path); err != nil {
				reason := kubeconfigDiagnosticError(err)
				log.Printf("Skipping unusable KUBECONFIG entry %s: %v", path, err)
				errorlog.Record("k8s-init", "warning", "skipping unusable KUBECONFIG entry %q: %s",
					filepath.Base(path), reason)
				if firstSkipped == "" {
					firstSkipped = fmt.Sprintf("%q: %s", filepath.Base(path), reason)
				}
				continue
			}
			regularPaths = append(regularPaths, path)
		}
		paths = regularPaths
		if len(paths) == 0 {
			return kubeconfigSources{}, fmt.Errorf("KUBECONFIG contains no usable files (%s)", firstSkipped)
		}
		paths = dedupeKubeconfigPaths(paths)
		if len(paths) > 1 {
			return kubeconfigSources{paths: paths, mode: "multi-env", useRegistry: true}, nil
		}
		return kubeconfigSources{paths: paths, mode: "single"}, nil
	}

	if homeDir == "" {
		return kubeconfigSources{mode: "single", tryInCluster: true}, nil
	}
	defaultPath, err := normalizeKubeconfigPath(filepath.Join(homeDir, ".kube", "config"), homeDir)
	if err != nil {
		return kubeconfigSources{}, fmt.Errorf("normalize default kubeconfig: %w", err)
	}
	return kubeconfigSources{paths: []string{defaultPath}, mode: "single", tryInCluster: true}, nil
}

func normalizeKubeconfigPathList(value, homeDir string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	var paths []string
	for _, path := range filepath.SplitList(value) {
		if path == "" {
			continue
		}
		normalized, err := normalizeKubeconfigPath(path, homeDir)
		if err != nil {
			return nil, err
		}
		paths = append(paths, normalized)
	}
	return paths, nil
}

func normalizeKubeconfigDirectories(dirs []string, homeDir string) ([]string, error) {
	result := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		normalized, err := normalizeKubeconfigPath(dir, homeDir)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	return result, nil
}

func normalizeKubeconfigPath(path, homeDir string) (string, error) {
	if path == "~" {
		if homeDir == "" {
			return "", fmt.Errorf("cannot expand %q without a home directory", path)
		}
		path = homeDir
	} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if homeDir == "" {
			return "", fmt.Errorf("cannot expand %q without a home directory", path)
		}
		path = filepath.Join(homeDir, path[2:])
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return absolute, nil
}

// NormalizeKubeconfigPath applies Radar's path expansion to one explicitly configured file.
func NormalizeKubeconfigPath(value string) (string, error) {
	return normalizeKubeconfigPath(value, homedir.HomeDir())
}

func hasUsableKubeconfig(path string) (bool, error) {
	if err := validateKubeconfigFileType(path); err != nil {
		return false, err
	}
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return false, err
	}
	return len(cfg.Contexts) > 0, nil
}

var (
	errKubeconfigNotRegular        = errors.New("not a regular file")
	errKubeconfigContextNotFound   = errors.New("selected context not found")
	errKubeconfigClientSetupFailed = errors.New("selected context client setup failed")
)

func validateKubeconfigFileType(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errKubeconfigNotRegular
	}
	return nil
}

func dedupeKubeconfigPaths(paths []string) []string {
	type fileIdentity struct {
		path string
		info os.FileInfo
	}
	seen := make([]fileIdentity, 0, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		info, _ := os.Stat(path)
		duplicate := false
		for _, existing := range seen {
			if path == existing.path || (info != nil && existing.info != nil && os.SameFile(info, existing.info)) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		seen = append(seen, fileIdentity{path: path, info: info})
		result = append(result, path)
	}
	return result
}

func sourceContextBinding(sourceFile, inFileName string) string {
	if sourceFile == "" || inFileName == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceFile + "\x00" + inFileName))
	return "kcb1_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// CAPIClusterSafetyBinding derives a stable workload-cluster identity from
// the management-cluster source and the CAPI Cluster object that owns the
// generated kubeconfig. Temporary kubeconfig paths are deliberately excluded.
func CAPIClusterSafetyBinding(parentBinding, namespace, name string) string {
	if parentBinding == "" || namespace == "" || name == "" {
		return ""
	}
	return sourceContextBinding(parentBinding+"\x00capi-cluster\x00"+namespace, name)
}

func sourceSafetyBindingLocked(sourceFile, inFileName string) string {
	for binding, path := range capiKubeconfigs {
		if path == sourceFile {
			return binding
		}
	}
	return sourceContextBinding(sourceFile, inFileName)
}

// discoverKubeconfigs scans directories for valid kubeconfig files
func discoverKubeconfigs(dirs []string) []string {
	var configs []string
	for _, dir := range dirs {
		before := len(configs)
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
			info, statErr := os.Stat(path)
			if statErr != nil || !info.Mode().IsRegular() {
				log.Printf("Skipping non-regular kubeconfig candidate %s", path)
				errorlog.Record("k8s-init", "warning",
					"skipping kubeconfig candidate %q because it is not a readable regular file",
					filepath.Base(path))
				continue
			}
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
		if len(configs) == before {
			errorlog.Record("k8s-init", "warning",
				"kubeconfig dir %q yielded no valid files",
				filepath.Base(dir))
		}
	}
	return configs
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

func kubeconfigDiagnosticError(err error) string {
	var pathErr *os.PathError
	errText := ""
	if err != nil {
		errText = strings.ToLower(err.Error())
	}
	switch {
	case err == nil:
		return ""
	case errors.As(err, &pathErr):
		return scrubPathError(err)
	case errors.Is(err, errKubeconfigNotRegular):
		return errKubeconfigNotRegular.Error()
	case errors.Is(err, errKubeconfigContextNotFound):
		return errKubeconfigContextNotFound.Error()
	case errors.Is(err, errKubeconfigClientSetupFailed):
		return errKubeconfigClientSetupFailed.Error()
	case clientcmd.IsEmptyConfig(err):
		return "empty kubeconfig (no configuration provided)"
	case clientcmd.IsContextNotFound(err):
		return "selected context not found"
	case clientcmd.IsConfigurationInvalid(err):
		return "invalid kubeconfig configuration"
	case strings.Contains(errText, "yaml") ||
		strings.Contains(errText, "json") ||
		strings.Contains(errText, "cannot unmarshal") ||
		strings.Contains(errText, "did not find expected"):
		return "invalid kubeconfig syntax"
	default:
		return "unclassified error"
	}
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

func GetClientSafetySnapshot() (*kubernetes.Clientset, string) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return k8sClient, contextBinding
}

// GetConfig returns the K8s rest config
func GetConfig() *rest.Config {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return k8sConfig
}

func GetConfigSnapshot() (*rest.Config, string) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if k8sConfig == nil {
		return nil, activeClusterContextLocked()
	}
	return rest.CopyConfig(k8sConfig), activeClusterContextLocked()
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

func GetDynamicClientSnapshot() (dynamic.Interface, string) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return dynamicClient, activeClusterContextLocked()
}

// GetKubeconfigPath returns the path to the kubeconfig file used
func GetKubeconfigPath() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if contextRegistry != nil {
		return ""
	}
	return kubeconfigPath
}

// KubeconfigSummary is a non-sensitive snapshot of kubeconfig loading state,
// suitable for inclusion in diagnostic output. It never includes the resolved
// paths themselves, only counts, mode flags, and exec plugin basenames.
type KubeconfigSummary struct {
	Mode                       string   // "in-cluster", "single", "multi-env", "multi-dir", "multi-source", or "" if not initialized
	FileCount                  int      // Number of kubeconfig files loaded (0 for in-cluster)
	DirectoryFileCount         int      // Number of loaded files discovered from configured directories
	ContextCount               int      // Number of contexts exposed after source resolution
	EnrichedFromShell          bool     // Desktop app captured KUBECONFIG from login shell
	KubeconfigEnvIgnored       bool     // KUBECONFIG was suppressed by configured sources
	KubeconfigEnvIgnoredReason string   // Non-sensitive reason KUBECONFIG was suppressed
	CurrentContextUsesExec     bool     // Current context's AuthInfo uses an exec credential plugin
	ExecPluginsPresent         []string // Unique exec plugin command basenames (any context) resolvable on $PATH
	ExecPluginsMissing         []string // Unique exec plugin command basenames (any context) NOT resolvable on $PATH
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
	fileCount := 0
	if contextRegistry != nil {
		fileCount = len(perFileConfigs)
	} else if kubeconfigPath != "" {
		fileCount = 1
	}
	contextCount := totalContextCount
	directoryFileCount := kubeconfigDirectoryFileCount
	enriched := enrichedKubeconfigFromShell
	envIgnored := kubeconfigEnvWasIgnored
	envIgnoredReason := kubeconfigEnvIgnoredReason
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
		Mode:                       mode,
		FileCount:                  fileCount,
		DirectoryFileCount:         directoryFileCount,
		ContextCount:               contextCount,
		EnrichedFromShell:          enriched,
		KubeconfigEnvIgnored:       envIgnored,
		KubeconfigEnvIgnoredReason: envIgnoredReason,
		CurrentContextUsesExec:     currentExec,
		ExecPluginsPresent:         present,
		ExecPluginsMissing:         missing,
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
	activeFile := activeSourceFile
	activeName := activeSourceName
	activeConfig := activeSourceConfig
	registry := contextRegistry
	fileConfigs := perFileConfigs
	singlePath := kubeconfigPath
	clientMu.RUnlock()

	var rawConfig clientcmdapi.Config
	var currentContextForFile string

	if activeFile != "" && activeName != "" && activeConfig != nil {
		candidate := fileConfigs[activeFile]
		if validateKubeconfigFileType(activeFile) == nil {
			loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: activeFile}
			if loaded, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				loadingRules, &clientcmd.ConfigOverrides{},
			).RawConfig(); err == nil {
				candidate = &loaded
			}
		}
		if sameKubeconfigTarget(activeConfig, candidate, activeName) {
			rawConfig = *candidate.DeepCopy()
		} else {
			rawConfig = *activeConfig.DeepCopy()
		}
		currentContextForFile = activeName
	} else if registry != nil {
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
		if err := validateKubeconfigFileType(singlePath); err != nil {
			return "", fmt.Errorf("failed to load kubeconfig: %w", err)
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

func sameKubeconfigTarget(active, candidate *clientcmdapi.Config, contextName string) bool {
	if active == nil || candidate == nil {
		return false
	}
	activeContext := active.Contexts[contextName]
	candidateContext := candidate.Contexts[contextName]
	if activeContext == nil || candidateContext == nil || activeContext.Cluster != candidateContext.Cluster {
		return false
	}
	return reflect.DeepEqual(active.Clusters[activeContext.Cluster], candidate.Clusters[candidateContext.Cluster])
}

// GetContextName returns the current kubeconfig context name
func GetContextName() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return contextName
}

// ClusterSafetyBinding scopes persisted safety decisions to the active source.
// In-cluster mode has no kubeconfig source, so it uses the cluster UID identity;
// if that identity cannot be established, callers receive an empty value and
// must not persist the decision.
func ClusterSafetyBinding(ctx context.Context) string {
	_, binding := ClusterSafetySnapshot(ctx)
	return binding
}

// ClusterSafetySnapshot returns the readable active-cluster label and the
// opaque identity used to scope persisted safety decisions from one coherent
// client-state snapshot. In-cluster mode replaces its generic display sentinel
// with the cluster UID identity when one can be established.
func ClusterSafetySnapshot(ctx context.Context) (display, binding string) {
	clientMu.RLock()
	display = activeClusterContextLocked()
	binding = contextBinding
	mode := kubeconfigMode
	clientMu.RUnlock()
	if binding != "" {
		return display, binding
	}
	if mode != "in-cluster" {
		return display, ""
	}
	binding = ClusterIdentity(ctx)
	if display == "in-cluster" && binding != "" {
		display = binding
	}
	return display, binding
}

// ActiveClusterContext is the cluster-identity stamp for timeline events and
// the filter value for cluster-scoped timeline reads. In-cluster mode has no
// kubeconfig context name; the sentinel keeps those events distinguishable
// from legacy rows recorded before provenance was tracked (empty string).
func ActiveClusterContext() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return activeClusterContextLocked()
}

func activeClusterContextLocked() string {
	if contextName != "" {
		return contextName
	}
	return "in-cluster"
}

var (
	clusterUIDMu      sync.Mutex
	clusterUIDCache   = map[string]string{}    // context name → "cluster-<kube-system uid>"
	clusterUIDRetryAt = map[string]time.Time{} // context name → when a FAILED lookup may retry
)

// clusterUIDNegativeTTL bounds how long a failed kube-system lookup is
// remembered. Short on purpose: long enough that an RBAC-denied install doesn't
// re-issue the doomed GET on every resource navigation, short enough that a
// permission fix self-heals within a minute instead of after a restart.
const clusterUIDNegativeTTL = 45 * time.Second

// ClusterIdentity returns a stable per-cluster identifier for scoping
// cluster-keyed client state (e.g. the in-cluster-test consent memory). A real
// kubeconfig context name wins. In-cluster deployments all report the SAME
// "in-cluster" sentinel - useless as an identity when one shared origin (Radar
// Hub) fronts many clusters - so they fall back to the kube-system namespace
// UID: immutable, unique per cluster, readable with radar's existing RBAC.
// Returns "" when even that read fails; callers must treat empty as "no stable
// identity" and never persist per-cluster state under a shared fallback key.
func ClusterIdentity(ctx context.Context) string {
	clientMu.RLock()
	name, mode := contextName, kubeconfigMode
	clientMu.RUnlock()
	if name != "" && mode != "in-cluster" {
		return name
	}
	client := GetClient()
	if client == nil {
		return ""
	}
	return cachedClusterUIDIdentity(ctx, name, client)
}

// cachedClusterUIDIdentity memoizes the kube-system UID lookup per context name:
// a success is cached forever (the UID is immutable), a failure is cached for
// clusterUIDNegativeTTL so an install that can't read kube-system doesn't hammer
// the apiserver with a doomed GET per request. Split from ClusterIdentity so the
// caching is testable with a fake client.
func cachedClusterUIDIdentity(ctx context.Context, name string, client kubernetes.Interface) string {
	clusterUIDMu.Lock()
	cached, ok := clusterUIDCache[name]
	retryAt := clusterUIDRetryAt[name]
	clusterUIDMu.Unlock()
	if ok {
		return cached
	}
	if time.Now().Before(retryAt) {
		return ""
	}
	if client == nil {
		return ""
	}
	id := clusterUIDIdentity(ctx, client)
	clusterUIDMu.Lock()
	defer clusterUIDMu.Unlock()
	if id == "" {
		clusterUIDRetryAt[name] = time.Now().Add(clusterUIDNegativeTTL)
		return ""
	}
	delete(clusterUIDRetryAt, name)
	clusterUIDCache[name] = id
	return id
}

// clusterUIDIdentity derives the fallback cluster identity from the kube-system
// namespace UID. Split from ClusterIdentity so it is testable with a fake client.
func clusterUIDIdentity(ctx context.Context, client kubernetes.Interface) string {
	ns, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil || ns == nil || ns.UID == "" {
		return ""
	}
	return "cluster-" + string(ns.UID)
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
	fallbackNamespacesExplicit = false
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
	fallbackNamespacesExplicit = len(fallbackNamespaces) > 0
	if len(fallbackNamespaces) > 0 {
		fallbackNamespace = fallbackNamespaces[0]
	} else {
		fallbackNamespace = ""
	}
	fallbackNamespaceContext = contextName
}

// ConfiguredNamespacesForCurrentContext returns the --namespaces list when
// the current kubeconfig context is still the one the flag was configured
// against. --namespaces is an *initial* value: after a context switch the
// list references the previous cluster's namespaces and must not seed picks.
func ConfiguredNamespacesForCurrentContext() []string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if !fallbackNamespacesExplicit || fallbackNamespaceContext != contextName || len(fallbackNamespaces) == 0 {
		return nil
	}
	return append([]string(nil), fallbackNamespaces...)
}

// ConfiguredNamespaceForCurrentContext returns the singular --namespace flag
// value when the current context is still the one it was configured against.
// Unlike the plural list, the singular flag mainly steers RBAC probing — as a
// picker seed it only outranks the kubeconfig context namespace, covering
// flows where the launch URL doesn't carry it (--no-browser, embedders).
func ConfiguredNamespaceForCurrentContext() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if fallbackNamespacesExplicit || fallbackNamespaceContext != contextName {
		return ""
	}
	return fallbackNamespace
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
	namespaceScopeOverride = ns
	clientMu.Unlock()
	// After the write, and outside clientMu: the permissions cache retires a
	// probe by comparing the generation it captured before reading its inputs,
	// so retiring first would leave a probe holding the old scope under the new
	// generation.
	InvalidateResourcePermissionsCache()
}

func ClearNamespaceScopeOverride() {
	clientMu.Lock()
	namespaceScopeOverride = ""
	clientMu.Unlock()
	InvalidateResourcePermissionsCache()
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
	clientMu.RLock()
	defer clientMu.RUnlock()
	return isInClusterLocked()
}

func isInClusterLocked() bool {
	if ForceInCluster {
		return true
	}
	if initializationStarted {
		return kubeconfigMode == "in-cluster"
	}
	return kubeconfigPath == "" && len(kubeconfigPaths) == 0
}

// ContextInfo represents information about a kubeconfig context
type ContextInfo struct {
	Name         string `json:"name"`
	OriginalName string `json:"originalName,omitempty"`
	Cluster      string `json:"cluster"`
	User         string `json:"user"`
	Namespace    string `json:"namespace"`
	IsCurrent    bool   `json:"isCurrent"`
	// Source labels the kubeconfig file this context came from
	// (e.g. "kube-cluster-paris" or "prod"). Set for registry-backed
	// loading; populated for every context — not just colliding ones — so
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
	var refreshEmptyAIs []string
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
			contextRegistry, perFileConfigs, perFileMtimes, kubeconfigPaths,
		)
		if changed {
			contextRegistry = newRegistry
			perFileConfigs = newFileConfigs
			perFileMtimes = newFileMtimes
			totalContextCount = len(newRegistry)
			summaryConfigs := newFileConfigs
			if activeSourceConfig != nil {
				if _, found := summaryConfigs[activeSourceFile]; !found {
					summaryConfigs = make(map[string]*clientcmdapi.Config, len(newFileConfigs)+1)
					for path, cfg := range newFileConfigs {
						summaryConfigs[path] = cfg
					}
					summaryConfigs[activeSourceFile] = activeSourceConfig
				}
			}
			execPluginCommands, refreshEmptyAIs = aggregateExecPluginCommands(kubeconfigPaths, summaryConfigs)
			switch kubeconfigMode {
			case "multi-dir", "multi-source":
				kubeconfigDirectoryFileCount = loadedDirectoryKubeconfigCount(
					newFileConfigs, kubeconfigDirectoryPaths,
				)
			}
		}
	}
	registry := contextRegistry
	fileConfigs := perFileConfigs
	currentCtx := contextName
	singlePath := kubeconfigPath
	clientMu.Unlock()
	if len(refreshEmptyAIs) > 0 {
		recordEmptyCommandWarning("kubeconfig-refresh", refreshEmptyAIs)
	}

	if registry != nil {
		// Isolated-load mode: enumerate every registered context, pulling
		// cluster/user/namespace from the file it originally lives in.
		// No merge happens — shared names across files stay distinct.
		// Iterating outside the lock is safe because refresh publishes
		// fresh maps on change rather than mutating in place, so the
		// snapshot we captured is frozen.
		contexts := make([]ContextInfo, 0, len(registry))
		qualifiedNames := make([]string, 0, len(registry))
		for qName := range registry {
			qualifiedNames = append(qualifiedNames, qName)
		}
		sort.Strings(qualifiedNames)
		for _, qName := range qualifiedNames {
			entry := registry[qName]
			cfg, ok := fileConfigs[entry.SourceFile]
			if !ok {
				continue
			}
			ctx, ok := cfg.Contexts[entry.InFileName]
			if !ok || ctx == nil {
				continue
			}
			contexts = append(contexts, ContextInfo{
				Name:         qName,
				OriginalName: entry.InFileName,
				Cluster:      ctx.Cluster,
				User:         ctx.AuthInfo,
				Namespace:    ctx.Namespace,
				IsCurrent:    qName == currentCtx,
				Source:       kubeconfigSourceLabel(entry.SourceFile),
			})
		}
		return contexts, nil
	}

	// Single-file fallback: load the one file and enumerate its contexts.
	kubeconfig := singlePath
	if kubeconfig == "" {
		return nil, fmt.Errorf("kubeconfig path not set")
	}
	if err := validateKubeconfigFileType(kubeconfig); err != nil {
		return nil, fmt.Errorf("kubeconfig source is unavailable: %w", err)
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
	for _, name := range sortedContextNames(&rawConfig) {
		ctx := rawConfig.Contexts[name]
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

func validateContextSwitchTarget(name string) error {
	clientMu.RLock()
	registry := contextRegistry
	singlePath := kubeconfigPath
	clientMu.RUnlock()

	var sourcePath, inFileName string
	if registry != nil {
		entry, ok := registry[name]
		if !ok {
			return fmt.Errorf("%w: %q", errKubeconfigContextNotFound, name)
		}
		sourcePath = entry.SourceFile
		inFileName = entry.InFileName
	} else {
		if singlePath == "" {
			return fmt.Errorf("kubeconfig path not set")
		}
		sourcePath = singlePath
		inFileName = name
	}
	if err := validateKubeconfigFileType(sourcePath); err != nil {
		return err
	}

	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: sourcePath}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: inFileName}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return err
	}
	if _, ok := rawConfig.Contexts[inFileName]; !ok {
		return fmt.Errorf("%w: %q", errKubeconfigContextNotFound, name)
	}
	if _, err := kubeConfig.ClientConfig(); err != nil {
		return fmt.Errorf("%w for %q: %w", errKubeconfigClientSetupFailed, name, err)
	}
	return nil
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
	singlePath := kubeconfigPath
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
			return fmt.Errorf("%w: %q", errKubeconfigContextNotFound, name)
		}
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: entry.SourceFile}
		overrideContextName = entry.InFileName
	} else {
		kubeconfig := singlePath
		if kubeconfig == "" {
			return fmt.Errorf("kubeconfig path not set")
		}
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
		overrideContextName = name
	}
	if err := validateKubeconfigFileType(loadingRules.ExplicitPath); err != nil {
		if errors.Is(err, errKubeconfigNotRegular) {
			return fmt.Errorf("kubeconfig source for context %q is unavailable: %w", name, errKubeconfigNotRegular)
		}
		return fmt.Errorf("kubeconfig source for context %q is unavailable: %s", name, kubeconfigDiagnosticError(err))
	}

	// Build config with the new context
	configOverrides := &clientcmd.ConfigOverrides{CurrentContext: overrideContextName}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	// Verify the context exists
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		log.Printf("[k8s] Failed to load kubeconfig for context %q: %v", name, err)
		return fmt.Errorf("failed to load kubeconfig for context %q: %s", name, kubeconfigDiagnosticError(err))
	}

	ctx, ok := rawConfig.Contexts[overrideContextName]
	if !ok {
		return fmt.Errorf("%w: %q", errKubeconfigContextNotFound, name)
	}

	// Build the REST config for the new context
	config, err := kubeConfig.ClientConfig()
	if err != nil {
		log.Printf("[k8s] Failed to build client config for context %q: %v", name, err)
		return fmt.Errorf("%w for %q: %s", errKubeconfigClientSetupFailed, name, kubeconfigDiagnosticError(err))
	}

	// Apply the same QPS/Burst settings as initial client creation.
	// Without this, new clients use the default 5 QPS / 10 Burst, causing
	// severe client-side throttling during CRD discovery after context switch.
	config.QPS = 50
	config.Burst = 100

	clients, err := newSharedKubernetesClients(config)
	if err != nil {
		return fmt.Errorf("%w for %q: %w", errKubeconfigClientSetupFailed, name, err)
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
	newContextBinding := sourceSafetyBindingLocked(loadingRules.ExplicitPath, overrideContextName)
	k8sConfig = config
	k8sClient = clients.clientset
	discoveryClient = clients.discovery
	dynamicClient = clients.dynamic
	activeClientGeneration = clients.generation
	contextName = name
	contextBinding = newContextBinding
	activeSourceFile = loadingRules.ExplicitPath
	activeSourceName = overrideContextName
	activeSourceConfig = rawConfig.DeepCopy()
	clusterName = ctx.Cluster
	contextNamespace = ctx.Namespace
	contextUsesExec = usesExec
	totalContextCount = totalContexts
	execPluginCommands = execCmds
	clientMu.Unlock()

	return nil
}

// capiKubeconfigs tracks temp kubeconfig files by stable CAPI cluster binding
// to avoid accumulation without treating same-named contexts as the same source.
var capiKubeconfigs = make(map[string]string) // safetyBinding -> tmpPath

type capiPromotionSnapshot struct {
	kubeconfigPath               string
	kubeconfigPaths              []string
	kubeconfigMode               string
	kubeconfigDirectoryFileCount int
	totalContextCount            int
}

var preCapiPromotion *capiPromotionSnapshot

// MergeAndSwitchContext writes the provided kubeconfig data to a temporary
// file and registers its context so that Radar can switch to it. Returns
// (qualifiedName, tmpPath, created, error): qualifiedName is the identifier the
// caller must pass to PerformContextSwitch, and may differ from the input
// contextName if another file already owns that name (the registry disambiguates
// via qualifyContextName). tmpPath is the on-disk location of the kubeconfig,
// exposed for diagnostics / logging only. created is false when an existing,
// previously published CAPI source was refreshed in place.
//
// If Radar started in single-file mode, the first CAPI merge promotes it
// into isolated-load mode by seeding the registry with the original
// kubeconfig plus the new CAPI file — otherwise subsequent CAPI merges
// would silently revert to client-go's Precedence behavior (issue #519).
//
// Concurrency: contextOpMu keeps registry-visible files stable while a context
// switch reads them, and clientMu serializes the reuse-check + registration
// decision. safetyBinding is derived from the management-cluster source plus
// the CAPI Cluster object identity, so reconnects reuse the source without
// conflating same-named contexts from different workload clusters.
func MergeAndSwitchContext(kubeconfigData []byte, contextName, safetyBinding string) (string, string, bool, error) {
	if safetyBinding == "" {
		return "", "", false, fmt.Errorf("CAPI cluster safety binding is unavailable")
	}
	newConfig, err := clientcmd.Load(kubeconfigData)
	if err != nil {
		return "", "", false, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}
	if _, ok := newConfig.Contexts[contextName]; !ok {
		return "", "", false, fmt.Errorf("context %q not found in provided kubeconfig", contextName)
	}

	activeContextOperations.Add(1)
	contextOpMu.Lock()
	defer func() {
		activeContextOperations.Add(-1)
		contextOpMu.Unlock()
	}()

	// Hold clientMu for the entire reuse-check + registration path so two
	// concurrent CAPI merges for the same workload cluster can't both see
	// "no existing path" and both create orphan temp files.
	clientMu.Lock()
	defer clientMu.Unlock()

	// Fast path: same CAPI context was registered before. Overwrite the
	// existing temp file so the user gets a fresh exec plugin config, and
	// return the qualified name we assigned on the original merge.
	if existingPath, ok := capiKubeconfigs[safetyBinding]; ok {
		pathErr := validateKubeconfigFileType(existingPath)
		replacementErr := pathErr
		if pathErr == nil || errors.Is(pathErr, fs.ErrNotExist) {
			writeErr := clientcmd.WriteToFile(*newConfig, existingPath)
			if writeErr != nil {
				replacementErr = writeErr
				log.Printf("[capi] Failed to update existing kubeconfig for context %q: %v", contextName, writeErr)
			} else {
				// Refresh the cached parsed config so subsequent GetAvailableContexts
				// calls can use fresh credentials immediately. Leave the cached mtime
				// untouched so the registry refresh still reconciles added, removed,
				// or renamed contexts from the rewritten file.
				parsed, parseErr := clientcmd.LoadFromFile(existingPath)
				qName := findQualifiedNameForPath(contextRegistry, existingPath, contextName)
				if qName != "" {
					if parseErr != nil {
						log.Printf("[capi] Failed to refresh cached kubeconfig for context %q: %v", contextName, parseErr)
						return qName, existingPath, false, nil
					}
					newFileConfigs := make(map[string]*clientcmdapi.Config, len(perFileConfigs))
					for path, cfg := range perFileConfigs {
						newFileConfigs[path] = cfg
					}
					newFileConfigs[existingPath] = parsed
					perFileConfigs = newFileConfigs
					log.Printf("[capi] Updated existing kubeconfig for context %q: %q", contextName, existingPath)
					return qName, existingPath, false, nil
				}
				if parseErr == nil && activeSourceFile == existingPath {
					newRegistry := make(map[string]contextEntry, len(contextRegistry)+len(parsed.Contexts))
					for name, entry := range contextRegistry {
						if entry.SourceFile != existingPath {
							newRegistry[name] = entry
						}
					}
					var qualifications []string
					for _, name := range sortedContextNames(parsed) {
						qualifiedName := qualifyContextName(newRegistry, name, existingPath)
						if qualification := logContextQualification(name, qualifiedName, existingPath); qualification != "" {
							qualifications = append(qualifications, qualification)
						}
						newRegistry[qualifiedName] = contextEntry{SourceFile: existingPath, InFileName: name}
					}
					qName = findQualifiedNameForPath(newRegistry, existingPath, contextName)
					if qName != "" {
						newFileConfigs := make(map[string]*clientcmdapi.Config, len(perFileConfigs)+1)
						for path, cfg := range perFileConfigs {
							newFileConfigs[path] = cfg
						}
						newFileConfigs[existingPath] = parsed
						newFileMtimes := make(map[string]time.Time, len(perFileMtimes)+1)
						for path, mtime := range perFileMtimes {
							newFileMtimes[path] = mtime
						}
						if info, statErr := os.Stat(existingPath); statErr == nil {
							newFileMtimes[existingPath] = info.ModTime()
						}
						contextRegistry = newRegistry
						perFileConfigs = newFileConfigs
						perFileMtimes = newFileMtimes
						totalContextCount = len(newRegistry)
						recordContextQualifications(qualifications)
						log.Printf("[capi] Restored kubeconfig for context %q: %q", contextName, existingPath)
						return qName, existingPath, false, nil
					}
				}
				replacementErr = fmt.Errorf("%w after CAPI source refresh", errKubeconfigContextNotFound)
			}
		} else {
			log.Printf("[capi] Replacing unusable kubeconfig for context %q: %v", contextName, pathErr)
		}
		if activeSourceFile == existingPath {
			return "", "", false, fmt.Errorf("failed to refresh active CAPI kubeconfig: %s", kubeconfigDiagnosticError(replacementErr))
		}
		if err := os.Remove(existingPath); err != nil && !os.IsNotExist(err) {
			return "", "", false, fmt.Errorf("failed to replace stale CAPI kubeconfig: %w", err)
		}
		dropKubeconfigSourceLocked(existingPath)
		delete(capiKubeconfigs, safetyBinding)
	}

	// Write to a new temp file.
	tmpFile, err := os.CreateTemp("", "radar-capi-kubeconfig-*.yaml")
	if err != nil {
		return "", "", false, fmt.Errorf("failed to create temp kubeconfig: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	if err := clientcmd.WriteToFile(*newConfig, tmpPath); err != nil {
		os.Remove(tmpPath)
		return "", "", false, fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	// Build a local snapshot of the registry additions we're about to make,
	// then validate before committing to globals. If validation fails we
	// remove the temp file and leave the globals untouched — no half-state.
	var newRegistry map[string]contextEntry
	var newFileConfigs map[string]*clientcmdapi.Config
	var newFileMtimes map[string]time.Time
	var newPaths []string

	promotedToRegistry := contextRegistry == nil
	var promotionSnapshot capiPromotionSnapshot
	if promotedToRegistry {
		promotionSnapshot = capiPromotionSnapshot{
			kubeconfigPath:               kubeconfigPath,
			kubeconfigPaths:              append([]string(nil), kubeconfigPaths...),
			kubeconfigMode:               kubeconfigMode,
			kubeconfigDirectoryFileCount: kubeconfigDirectoryFileCount,
			totalContextCount:            totalContextCount,
		}
		// Promote single-file mode to isolated-load mode.
		seedPaths := []string{}
		if kubeconfigPath != "" {
			seedPaths = append(seedPaths, kubeconfigPath)
		}
		seedPaths = append(seedPaths, tmpPath)
		registry, fileConfigs := buildContextRegistry(seedPaths)
		if _, hasTmp := fileConfigs[tmpPath]; !hasTmp {
			os.Remove(tmpPath)
			return "", "", false, fmt.Errorf("internal: failed to register CAPI kubeconfig %s", tmpPath)
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
			return "", "", false, fmt.Errorf("failed to re-load temp kubeconfig: %w", err)
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
		var qualifications []string
		for _, name := range sortedContextNames(cfg) {
			qName := qualifyContextName(newRegistry, name, tmpPath)
			if qualification := logContextQualification(name, qName, tmpPath); qualification != "" {
				qualifications = append(qualifications, qualification)
			}
			newRegistry[qName] = contextEntry{
				SourceFile: tmpPath,
				InFileName: name,
			}
		}
		recordContextQualifications(qualifications)
	}

	qualifiedName := findQualifiedNameForPath(newRegistry, tmpPath, contextName)
	if qualifiedName == "" {
		os.Remove(tmpPath)
		return "", "", false, fmt.Errorf("internal: failed to register context %q from %s", contextName, tmpPath)
	}

	// Commit. All globals updated atomically under the single Lock held above.
	contextRegistry = newRegistry
	perFileConfigs = newFileConfigs
	perFileMtimes = newFileMtimes
	kubeconfigPaths = newPaths
	capiKubeconfigs[safetyBinding] = tmpPath
	totalContextCount = len(newRegistry)
	if promotedToRegistry {
		kubeconfigPath = ""
		kubeconfigMode = "multi-source"
		kubeconfigDirectoryFileCount = 0
		preCapiPromotion = &promotionSnapshot
	}

	log.Printf("[capi] Added workload cluster kubeconfig: %q (context: %q)", tmpPath, qualifiedName)
	return qualifiedName, tmpPath, true, nil
}

// DiscardFailedMergedContext removes a newly created CAPI kubeconfig that
// failed before becoming the active client. Reused or active sources remain
// registered because later connection attempts may still need them for retry.
func DiscardFailedMergedContext(path string, created bool) bool {
	if !created {
		return false
	}

	activeContextOperations.Add(1)
	contextOpMu.Lock()
	defer func() {
		activeContextOperations.Add(-1)
		contextOpMu.Unlock()
	}()

	clientMu.Lock()
	defer clientMu.Unlock()

	if path == "" || activeSourceFile == path {
		return false
	}

	trackedBinding := ""
	for binding, source := range capiKubeconfigs {
		if source == path {
			trackedBinding = binding
			break
		}
	}
	if trackedBinding == "" {
		return false
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[capi] Failed to remove inactive kubeconfig %q: %v", path, err)
	}
	dropKubeconfigSourceLocked(path)
	delete(capiKubeconfigs, trackedBinding)

	if preCapiPromotion == nil {
		return true
	}
	snapshot := *preCapiPromotion

	expectedPaths := append([]string(nil), snapshot.kubeconfigPaths...)
	if snapshot.kubeconfigPath != "" {
		expectedPaths = append([]string{snapshot.kubeconfigPath}, expectedPaths...)
	}
	if !slices.Equal(kubeconfigPaths, expectedPaths) {
		return true
	}

	contextRegistry = nil
	perFileConfigs = nil
	perFileMtimes = nil
	kubeconfigPath = snapshot.kubeconfigPath
	kubeconfigPaths = append([]string(nil), snapshot.kubeconfigPaths...)
	kubeconfigMode = snapshot.kubeconfigMode
	kubeconfigDirectoryFileCount = snapshot.kubeconfigDirectoryFileCount
	totalContextCount = snapshot.totalContextCount
	preCapiPromotion = nil
	return true
}

func dropKubeconfigSourceLocked(path string) {
	newRegistry := make(map[string]contextEntry, len(contextRegistry))
	for name, entry := range contextRegistry {
		if entry.SourceFile != path {
			newRegistry[name] = entry
		}
	}
	if contextRegistry != nil {
		contextRegistry = newRegistry
		totalContextCount = len(newRegistry)
	}

	newFileConfigs := make(map[string]*clientcmdapi.Config, len(perFileConfigs))
	for source, config := range perFileConfigs {
		if source != path {
			newFileConfigs[source] = config
		}
	}
	if perFileConfigs != nil {
		perFileConfigs = newFileConfigs
	}

	newFileMtimes := make(map[string]time.Time, len(perFileMtimes))
	for source, mtime := range perFileMtimes {
		if source != path {
			newFileMtimes[source] = mtime
		}
	}
	if perFileMtimes != nil {
		perFileMtimes = newFileMtimes
	}

	paths := make([]string, 0, len(kubeconfigPaths))
	for _, source := range kubeconfigPaths {
		if source != path {
			paths = append(paths, source)
		}
	}
	kubeconfigPaths = paths
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

// GetContextSource resolves a switcher-visible context name to its isolated
// kubeconfig file and original in-file context name. Registry-backed loading
// can resolve any visible name; direct single-file loading can resolve only the
// active name because no registry is maintained in that mode.
func GetContextSource(name string) (sourceFile, inFileName string, ok bool) {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if name == contextName && activeSourceFile != "" && activeSourceName != "" {
		return activeSourceFile, activeSourceName, true
	}
	if entry, found := contextRegistry[name]; found {
		return entry.SourceFile, entry.InFileName, true
	}
	if contextRegistry == nil && kubeconfigPath != "" && name == contextName {
		return kubeconfigPath, name, true
	}
	return "", "", false
}
