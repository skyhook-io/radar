package app

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/ai"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	mcppkg "github.com/skyhook-io/radar/internal/mcp"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/static"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	versionpkg "github.com/skyhook-io/radar/internal/version"
)

// AppConfig holds all parsed configuration for the Radar application.
type AppConfig struct {
	Kubeconfig       string
	KubeconfigDirs   []string
	Namespace        string
	Port             int
	NoBrowser        bool
	DevMode          bool
	HistoryLimit     int
	DebugEvents      bool
	FakeInCluster    bool
	DisableHelmWrite bool
	TimelineStorage  string
	TimelineDBPath   string
	PrometheusURL    string
	Version          string
	MCPEnabled       bool
	CacheDBPath      string // Path to state cache database (default: ~/.radar/cache.db)
	NoCache          bool   // Disable state caching (full discovery on every startup)
	// AI options
	AIProvider string // AI provider: openai, anthropic, ollama
	AIModel    string // AI model name override
	AIAPIKey   string // API key for OpenAI or Anthropic
	OllamaURL  string // Ollama server URL (default: http://localhost:11434)
}

// SetGlobals applies debug/test flags to global state.
func SetGlobals(cfg AppConfig) {
	k8s.DebugEvents = cfg.DebugEvents
	k8s.ForceInCluster = cfg.FakeInCluster
	k8s.ForceDisableHelmWrite = cfg.DisableHelmWrite
	versionpkg.SetCurrent(cfg.Version)
}

// InitializeK8s creates and configures the Kubernetes client.
func InitializeK8s(cfg AppConfig) error {
	err := k8s.Initialize(k8s.InitOptions{
		KubeconfigPath: cfg.Kubeconfig,
		KubeconfigDirs: cfg.KubeconfigDirs,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize K8s client: %w", err)
	}

	if cfg.Namespace != "" {
		k8s.SetFallbackNamespace(cfg.Namespace)
	}

	if len(cfg.KubeconfigDirs) > 0 {
		log.Printf("Using kubeconfigs from directories: %v", cfg.KubeconfigDirs)
	} else if kubepath := k8s.GetKubeconfigPath(); kubepath != "" {
		log.Printf("Using kubeconfig: %s", kubepath)
	} else {
		log.Printf("Using in-cluster config")
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Starting server...",
	})

	return nil
}

// BuildTimelineStoreConfig creates the timeline store configuration from app config.
func BuildTimelineStoreConfig(cfg AppConfig) timeline.StoreConfig {
	storeCfg := timeline.StoreConfig{
		Type:    timeline.StoreTypeMemory,
		MaxSize: cfg.HistoryLimit,
	}
	if cfg.TimelineStorage == "sqlite" {
		storeCfg.Type = timeline.StoreTypeSQLite
		dbPath := cfg.TimelineDBPath
		if dbPath == "" {
			homeDir, _ := os.UserHomeDir()
			dbPath = filepath.Join(homeDir, ".radar", "timeline.db")
		}
		storeCfg.Path = dbPath
	}
	return storeCfg
}

// RegisterCallbacks registers Helm, timeline, and traffic reset/reinit functions
// used for both initial cluster initialization and context switching.
// Must be called before InitializeCluster.
func RegisterCallbacks(cfg AppConfig, timelineStoreCfg timeline.StoreConfig) {
	k8s.RegisterHelmFuncs(helm.ResetClient, helm.ReinitClient)

	k8s.RegisterTimelineFuncs(timeline.ResetStore, func() error {
		return timeline.ReinitStore(timelineStoreCfg)
	})

	if cfg.PrometheusURL != "" {
		u, err := url.Parse(cfg.PrometheusURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			log.Fatalf("Invalid --prometheus-url %q: must be a valid HTTP(S) URL (e.g., http://prometheus-server.monitoring:9090)", cfg.PrometheusURL)
		}
		traffic.SetMetricsURL(cfg.PrometheusURL)
	}

	k8s.RegisterTrafficFuncs(traffic.Reset, func() error {
		return traffic.ReinitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
	})
}

// SetupAIRegistry creates and configures the AI provider registry from config.
// Returns nil if no AI provider is configured.
func SetupAIRegistry(cfg AppConfig) *ai.Registry {
	// Determine API key: flag takes precedence over env
	apiKey := cfg.AIAPIKey

	// Auto-detect provider from env vars if no explicit provider set
	providerName := cfg.AIProvider
	if providerName == "" {
		if apiKey != "" {
			// Key provided but no provider - default to openai
			providerName = "openai"
		} else if os.Getenv("OPENAI_API_KEY") != "" {
			providerName = "openai"
			apiKey = os.Getenv("OPENAI_API_KEY")
		} else if os.Getenv("ANTHROPIC_API_KEY") != "" {
			providerName = "anthropic"
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		} else if os.Getenv("OLLAMA_URL") != "" || cfg.OllamaURL != "" {
			providerName = "ollama"
		}
	} else if apiKey == "" {
		// Provider set explicitly but no key from flag - check env
		switch providerName {
		case "openai":
			apiKey = os.Getenv("OPENAI_API_KEY")
		case "anthropic":
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
	}

	if providerName == "" {
		return nil
	}

	registry := ai.NewRegistry()

	// Determine Ollama URL
	ollamaURL := cfg.OllamaURL
	if ollamaURL == "" {
		ollamaURL = os.Getenv("OLLAMA_URL")
	}

	// Register all providers
	registry.Register(ai.NewOllamaProvider(ollamaURL))
	registry.Register(ai.NewOpenAIProvider(apiKey))
	registry.Register(ai.NewAnthropicProvider(apiKey))

	// Set active provider
	model := cfg.AIModel
	if err := registry.SetActive(providerName, model); err != nil {
		log.Printf("[ai] Warning: failed to set active provider %q: %v", providerName, err)
		return registry
	}

	log.Printf("[ai] Provider: %s, Model: %s", providerName, registry.GetActiveModel())
	return registry
}

// CreateServer creates the HTTP server with the given configuration.
func CreateServer(cfg AppConfig) *server.Server {
	serverCfg := server.Config{
		Port:       cfg.Port,
		DevMode:    cfg.DevMode,
		StaticFS:   static.FS,
		StaticRoot: "dist",
		AIRegistry: SetupAIRegistry(cfg),
	}

	if cfg.MCPEnabled {
		serverCfg.MCPHandler = mcppkg.NewHandler()
		log.Printf("MCP server enabled at http://localhost:%d/mcp", cfg.Port)
	}

	return server.New(serverCfg)
}

// stateCache holds the global state cache instance for the app lifecycle.
var stateCache *k8s.StateCache

// InitializeCluster connects to the cluster and initializes all subsystems.
// Progress is broadcast via SSE so the browser can show updates.
// Callbacks must be registered via RegisterCallbacks before calling this.
func InitializeCluster(cfg AppConfig) {
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Testing cluster connectivity...",
	})

	serverVersion, err := CheckClusterAccessWithVersion()
	if err != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   k8s.GetContextName(),
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		log.Printf("Warning: Cluster not reachable, starting in disconnected mode")
		return
	}

	progressFn := func(msg string) {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:       k8s.StateConnecting,
			Context:     k8s.GetContextName(),
			ProgressMsg: msg,
		})
	}

	// Initialize state cache if enabled
	var initErr error
	if !cfg.NoCache {
		dbPath := cfg.CacheDBPath
		if dbPath == "" {
			homeDir, _ := os.UserHomeDir()
			dbPath = filepath.Join(homeDir, ".radar", "cache.db")
		}

		var err error
		stateCache, err = k8s.NewStateCache(dbPath)
		if err != nil {
			log.Printf("Warning: failed to open state cache: %v (using full init)", err)
			stateCache = nil
		} else {
			// Purge clusters not seen in 30 days
			stateCache.PurgeStale(30 * 24 * time.Hour)
		}
	}

	if stateCache != nil {
		// Make cache available for context switches
		k8s.SetContextStateCache(stateCache)

		// Compute cluster ID from context + server URL + version
		config := k8s.GetConfig()
		serverURL := ""
		if config != nil {
			serverURL = config.Host
		}
		clusterID := k8s.ClusterID(k8s.GetContextName(), serverURL, serverVersion)

		// Save/update cluster record
		stateCache.SaveCluster(clusterID, k8s.GetContextName(), serverURL, serverVersion)

		// Use cached init
		initErr = k8s.InitAllSubsystemsCached(stateCache, clusterID, progressFn)
	} else {
		initErr = k8s.InitAllSubsystems(progressFn)
	}

	if initErr != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   k8s.GetContextName(),
			Error:     initErr.Error(),
			ErrorType: k8s.ClassifyError(initErr),
		})
		log.Printf("Warning: Subsystem init failed, starting in disconnected mode: %v", initErr)
		return
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})

	// Start connection health watchdog to detect API server drops
	k8s.StartConnectionWatchdog()
}

// CheckClusterAccessWithVersion verifies connectivity and returns the server version string.
func CheckClusterAccessWithVersion() (string, error) {
	clientset := k8s.GetClient()
	if clientset == nil {
		return "", fmt.Errorf("kubernetes client not initialized")
	}

	versionInfo, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("failed to connect to cluster: %w", err)
	}
	return versionInfo.GitVersion, nil
}

// Shutdown performs graceful teardown of all subsystems and the HTTP server.
func Shutdown(srv *server.Server) {
	log.Println("Shutting down...")
	k8s.StopConnectionWatchdog()
	srv.Stop()
	k8s.ResetAllSubsystems()
	if stateCache != nil {
		stateCache.Close()
	}
}

// CheckClusterAccess verifies connectivity to the Kubernetes cluster.
// Retries once after a 2-second pause to handle transient failures common with
// exec-based credential plugins (e.g., EKS) that may not be ready on cold start.
// Deterministic errors (RBAC, network) skip the retry.
func CheckClusterAccess() error {
	_, err := CheckClusterAccessWithVersion()
	return err
}

// ParseKubeconfigDirs splits a comma-separated directory string into a slice.
func ParseKubeconfigDirs(dirs string) []string {
	if dirs == "" {
		return nil
	}
	var result []string
	for _, dir := range strings.Split(dirs, ",") {
		dir = strings.TrimSpace(dir)
		if dir != "" {
			result = append(result, dir)
		}
	}
	return result
}
