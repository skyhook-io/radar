package k8s

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var (
	k8sClient       *kubernetes.Clientset
	k8sConfig       *rest.Config
	discoveryClient *discovery.DiscoveryClient
	dynamicClient   dynamic.Interface
	initOnce        sync.Once
	initErr         error
	kubeconfigPath     string
	kubeconfigPaths    []string // Multiple kubeconfig paths when using --kubeconfig-dir
	contextName        string
	clusterName        string
	contextNamespace   string // Default namespace from kubeconfig context
	fallbackNamespace  string // Explicit namespace from --namespace flag
	// clientMu protects access to client variables during context switches.
	// Readers use RLock, context switch uses Lock.
	clientMu sync.RWMutex
)

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
		}
	}

	if config == nil {
		// Use kubeconfig (for local development / CLI usage)
		var loadingRules *clientcmd.ClientConfigLoadingRules

		if len(opts.KubeconfigDirs) > 0 {
			// Multi-kubeconfig mode: discover and merge configs from directories
			configs, err := discoverKubeconfigs(opts.KubeconfigDirs)
			if err != nil {
				return fmt.Errorf("failed to discover kubeconfigs: %w", err)
			}
			if len(configs) == 0 {
				return fmt.Errorf("no valid kubeconfig files found in directories: %v", opts.KubeconfigDirs)
			}
			log.Printf("Discovered %d kubeconfig files from %d directories", len(configs), len(opts.KubeconfigDirs))
			kubeconfigPaths = configs
			loadingRules = &clientcmd.ClientConfigLoadingRules{Precedence: configs}
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
			kubeconfigPath = kubeconfig
			loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
		}

		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

		// Get raw config to extract context/cluster names
		rawConfig, err := kubeConfig.RawConfig()
		if err == nil {
			contextName = rawConfig.CurrentContext
			if ctx, ok := rawConfig.Contexts[contextName]; ok {
				clusterName = ctx.Cluster
				contextNamespace = ctx.Namespace
			}
		}

		config, err = kubeConfig.ClientConfig()
		if err != nil {
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
			}
		}
	}
	return configs, nil
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

// GetContextName returns the current kubeconfig context name
func GetContextName() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return contextName
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

// SetFallbackNamespace sets an explicit namespace to use as RBAC fallback
// (typically from the --namespace CLI flag). Used when the kubeconfig context
// doesn't specify a namespace but the user wants namespace-scoped access.
func SetFallbackNamespace(ns string) {
	clientMu.Lock()
	defer clientMu.Unlock()
	fallbackNamespace = ns
}

// GetEffectiveNamespace returns the namespace to use for RBAC fallback checks.
// Prefers the kubeconfig context namespace, falls back to the explicit --namespace flag.
func GetEffectiveNamespace() string {
	clientMu.RLock()
	defer clientMu.RUnlock()
	if contextNamespace != "" {
		return contextNamespace
	}
	return fallbackNamespace
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

	var loadingRules *clientcmd.ClientConfigLoadingRules
	if len(kubeconfigPaths) > 0 {
		// Multi-kubeconfig mode
		loadingRules = &clientcmd.ClientConfigLoadingRules{Precedence: kubeconfigPaths}
	} else {
		// Single kubeconfig mode
		kubeconfig := kubeconfigPath
		if kubeconfig == "" {
			return nil, fmt.Errorf("kubeconfig path not set")
		}
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	// Use Explorer's in-memory contextName to determine current context
	// This allows Explorer to switch contexts without modifying the kubeconfig file
	currentCtx := contextName
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

	var loadingRules *clientcmd.ClientConfigLoadingRules
	if len(kubeconfigPaths) > 0 {
		// Multi-kubeconfig mode
		loadingRules = &clientcmd.ClientConfigLoadingRules{Precedence: kubeconfigPaths}
	} else {
		// Single kubeconfig mode
		kubeconfig := kubeconfigPath
		if kubeconfig == "" {
			return fmt.Errorf("kubeconfig path not set")
		}
		loadingRules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	}

	// Build config with the new context
	configOverrides := &clientcmd.ConfigOverrides{CurrentContext: name}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	// Verify the context exists
	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	ctx, ok := rawConfig.Contexts[name]
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
	clientMu.Lock()
	k8sConfig = config
	k8sClient = newK8sClient
	discoveryClient = newDiscoveryClient
	dynamicClient = newDynamicClient
	contextName = name
	clusterName = ctx.Cluster
	contextNamespace = ctx.Namespace
	clientMu.Unlock()

	return nil
}
