package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	mcppkg "github.com/skyhook-io/radar/internal/mcp"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
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
}

// SetGlobals applies debug/test flags to global state.
func SetGlobals(cfg AppConfig) {
	k8s.DebugEvents = cfg.DebugEvents
	k8s.TimingLogs = cfg.DevMode
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

// RegisterCallbacks registers Helm, timeline, traffic, and Prometheus reset/reinit
// functions used for both initial cluster initialization and context switching.
// Must be called before InitializeCluster.
func RegisterCallbacks(cfg AppConfig, timelineStoreCfg timeline.StoreConfig) {
	k8s.RegisterHelmFuncs(helm.ResetClient, helm.ReinitClient)

	k8s.RegisterTimelineFuncs(timeline.ResetStore, func() error {
		return timeline.ReinitStore(timelineStoreCfg)
	})

	// Initialize Prometheus metrics client (must come before SetManualURL)
	prometheuspkg.Initialize(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())

	if cfg.PrometheusURL != "" {
		u, err := url.Parse(cfg.PrometheusURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			log.Fatalf("Invalid --prometheus-url %q: must be a valid HTTP(S) URL (e.g., http://prometheus-server.monitoring:9090)", cfg.PrometheusURL)
		}
		traffic.SetMetricsURL(cfg.PrometheusURL)
		prometheuspkg.SetManualURL(cfg.PrometheusURL)
	}

	k8s.RegisterTrafficFuncs(traffic.Reset, func() error {
		return traffic.ReinitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
	})

	k8s.RegisterPrometheusFuncs(prometheuspkg.Reset, func() error {
		prometheuspkg.Reinitialize(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
		if cfg.PrometheusURL != "" {
			prometheuspkg.SetManualURL(cfg.PrometheusURL)
		}
		return nil
	})
}

// CreateServer creates the HTTP server with the given configuration.
func CreateServer(cfg AppConfig) *server.Server {
	effectiveCfg := &config.Config{
		Kubeconfig:      cfg.Kubeconfig,
		KubeconfigDirs:  cfg.KubeconfigDirs,
		Namespace:       cfg.Namespace,
		Port:            cfg.Port,
		NoBrowser:       cfg.NoBrowser,
		TimelineStorage: cfg.TimelineStorage,
		TimelineDBPath:  cfg.TimelineDBPath,
		HistoryLimit:    cfg.HistoryLimit,
		PrometheusURL:   cfg.PrometheusURL,
		MCP:             &cfg.MCPEnabled,
	}

	serverCfg := server.Config{
		Port:            cfg.Port,
		DevMode:         cfg.DevMode,
		StaticFS:        static.FS,
		StaticRoot:      "dist",
		EffectiveConfig: effectiveCfg,
		DiagConfig: &server.DiagConfig{
			Port:             cfg.Port,
			DevMode:          cfg.DevMode,
			Namespace:        cfg.Namespace,
			TimelineStorage:  cfg.TimelineStorage,
			HistoryLimit:     cfg.HistoryLimit,
			DebugEvents:      cfg.DebugEvents,
			MCPEnabled:       cfg.MCPEnabled,
			HasPrometheusURL: cfg.PrometheusURL != "",
		},
	}

	if cfg.MCPEnabled {
		serverCfg.MCPHandler = mcppkg.NewHandler()
		if cfg.Port != 0 {
			log.Printf("MCP server enabled at http://localhost:%d/mcp", cfg.Port)
		} else {
			log.Printf("MCP server enabled (port will be assigned at startup)")
		}
	}

	return server.New(serverCfg)
}

// InitializeCluster connects to the cluster and initializes all subsystems.
// Progress is broadcast via SSE so the browser can show updates.
// Callbacks must be registered via RegisterCallbacks before calling this.
//
// The /version connectivity check runs in parallel with subsystem init
// (RBAC checks + informer sync) so neither blocks the other. If the
// connectivity check fails, subsystem init is canceled immediately.
func InitializeCluster() {
	// Cancel any in-flight API calls from previous attempts (e.g., browser
	// polling /api/capabilities with RBAC checks through a broken exec plugin).
	k8s.CancelOngoingOperations()

	clusterStart := time.Now()
	log.Printf("[ops] InitializeCluster START (context=%s)", k8s.GetContextName())

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Testing cluster connectivity...",
	})

	// Run connectivity check and subsystem init in parallel.
	// Subsystem init (RBAC + informers) makes API calls that implicitly
	// verify connectivity, so starting them together saves ~1-2s.
	// If /version fails, we cancel subsystem init via context.
	// Derived from the operation context so a context switch or retry
	// cancels our goroutines immediately.
	ctx, cancel := context.WithCancel(k8s.OperationContext())
	defer cancel()

	// Gate: subsystem progress messages only update the UI after /version
	// confirms connectivity. Before that the user sees "Testing cluster
	// connectivity..." / "Retrying cluster connectivity..." from CheckClusterAccess.
	var connected atomic.Bool

	// Exec credential plugins (EKS, GKE) may need 7-10s on first invocation
	// to refresh SSO/OAuth tokens. Give them a longer deadline so the retry
	// loop has room for two full attempts. Without exec auth, 10s is plenty
	// for two 5s attempts.
	versionDeadline := 10 * time.Second
	if k8s.UsesExecAuth() {
		versionDeadline = 25 * time.Second
	}
	versionCtx, versionCancel := context.WithTimeout(ctx, versionDeadline)

	versionErr := make(chan error, 1)
	go func() {
		defer versionCancel()
		versionErr <- CheckClusterAccess(versionCtx)
	}()

	subsystemErr := make(chan error, 1)
	go func() {
		subsystemErr <- k8s.InitAllSubsystems(ctx, func(msg string) {
			if connected.Load() {
				k8s.SetConnectionStatus(k8s.ConnectionStatus{
					State:       k8s.StateConnecting,
					Context:     k8s.GetContextName(),
					ProgressMsg: msg,
				})
			}
		})
	}()

	// Wait for connectivity check first
	if err := <-versionErr; err != nil {
		cancel() // Cancel subsystem init — RBAC goroutines will see ctx.Err()

		// Update status IMMEDIATELY so the UI shows the error page.
		// Don't wait for subsystem drain — exec credential plugins serialize
		// API calls, so draining 20+ RBAC checks can take 30+ seconds.
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   k8s.GetContextName(),
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		log.Printf("[ops] InitializeCluster FAILED: %v (errorType=%s, %v elapsed)", err, k8s.ClassifyError(err), time.Since(clusterStart))

		// Drain subsystem goroutine in background to prevent goroutine leak.
		// Cleanup is handled by the next context switch or retry.
		go func() {
			<-subsystemErr
		}()
		return
	}
	connected.Store(true)
	k8s.LogTiming(" Cluster access check: %v", time.Since(clusterStart))

	// Connectivity confirmed — kick off progress updates for remaining init
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Loading workloads...",
	})

	// Connectivity confirmed — wait for subsystem init to finish
	if err := <-subsystemErr; err != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   k8s.GetContextName(),
			Error:     err.Error(),
			ErrorType: k8s.ClassifyError(err),
		})
		log.Printf("Warning: Subsystem init failed, starting in disconnected mode: %v", err)
		return
	}
	k8s.LogTiming(" Total cluster init: %v", time.Since(clusterStart))

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})

	// Auto-discover Prometheus in the background so charts are ready immediately
	go func() {
		pt := time.Now()
		promCtx, promCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer promCancel()
		client := prometheuspkg.GetClient()
		if client == nil {
			return
		}
		if _, _, err := client.EnsureConnected(promCtx); err != nil {
			log.Printf("[prometheus] Auto-discovery failed (%v): %v", time.Since(pt), err)
		} else {
			log.Printf("[prometheus] Auto-discovery succeeded (%v)", time.Since(pt))
		}
	}()
}

// WriteMCPPortFile writes the actual server port to ~/.radar/mcp-port so MCP
// clients can discover the running instance without hardcoding a port.
func WriteMCPPortFile(port int) {
	path := mcpPortFilePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[mcp] Failed to create directory for port file: %v", err)
		return
	}
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d\n", port), 0o644); err != nil {
		log.Printf("[mcp] Failed to write port file: %v", err)
		return
	}
	log.Printf("[mcp] Port file written: %s (port %d)", path, port)
}

// RemoveMCPPortFile removes the port discovery file on shutdown.
func RemoveMCPPortFile() {
	path := mcpPortFilePath()
	if path == "" {
		return
	}
	os.Remove(path)
}

func mcpPortFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".radar", "mcp-port")
}

// Shutdown performs graceful teardown of all subsystems and the HTTP server.
func Shutdown(srv *server.Server) {
	log.Println("Shutting down...")
	RemoveMCPPortFile()
	srv.Stop()
	k8s.ResetAllSubsystems()
}

// CheckClusterAccess verifies connectivity to the Kubernetes cluster.
// The provided context controls the overall deadline — when it expires, the
// check returns immediately even if the exec credential plugin (e.g., GKE,
// EKS) is still blocking.
//
// Retries once after a 2-second pause to handle transient timeouts.
// Deterministic errors (auth, RBAC, network) skip the retry — retrying
// expired credentials or unreachable hosts won't help. Exception: exec auth
// timeouts ARE retried because the first call triggers a token refresh
// (e.g., AWS SSO), and the cached token is available on the next attempt.
func CheckClusterAccess(ctx context.Context) error {
	clientset := k8s.GetClient()
	if clientset == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}

	execAuth := k8s.UsesExecAuth()

	// Exec credential plugins (EKS aws, GKE gcloud) may need 7-10s on first
	// invocation to refresh SSO/OAuth tokens. The standard 5s is too tight.
	attemptTimeout := 5 * time.Second
	if execAuth {
		attemptTimeout = 10 * time.Second
	}

	var lastErr error
	for attempt := range 2 {
		if attempt > 0 {
			// Don't retry errors that won't resolve on their own.
			// Exception: exec auth timeouts are retryable — the first call
			// triggers a token refresh, and the cached token is ready by retry.
			errType := k8s.ClassifyError(lastErr)
			if errType == "rbac" || errType == "network" {
				break
			}
			if errType == "auth" && !execAuth {
				break
			}
			// Don't retry if the parent context is already done
			select {
			case <-ctx.Done():
				return fmt.Errorf("failed to connect to cluster: %w", lastErr)
			default:
			}
			log.Printf("Retrying cluster connectivity check...")
			k8s.SetConnectionStatus(k8s.ConnectionStatus{
				State:       k8s.StateConnecting,
				Context:     k8s.GetContextName(),
				ProgressMsg: "Retrying cluster connectivity...",
			})
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return fmt.Errorf("failed to connect to cluster: %w", lastErr)
			}
		}

		t := time.Now()
		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)

		// Run the API call in a goroutine so we can select on the parent
		// context. This guarantees we return when the deadline hits even
		// if the exec credential plugin blocks beyond the HTTP timeout.
		resultCh := make(chan error, 1)
		go func() {
			_, err := clientset.Discovery().RESTClient().Get().AbsPath("/version").Do(attemptCtx).Raw()
			resultCh <- err
		}()

		var err error
		select {
		case err = <-resultCh:
		case <-ctx.Done():
			cancel()
			if lastErr != nil {
				return fmt.Errorf("failed to connect to cluster: %w", lastErr)
			}
			return fmt.Errorf("failed to connect to cluster: %w", ctx.Err())
		}
		cancel()

		if err == nil {
			k8s.LogTiming("   Cluster /version check (attempt %d): %v", attempt+1, time.Since(t))
			return nil
		}
		log.Printf("Cluster connectivity check failed (attempt %d/2): %v (%v)", attempt+1, err, time.Since(t))
		lastErr = err
	}

	return fmt.Errorf("failed to connect to cluster: %w", lastErr)
}

// ParseKubeconfigDirs splits a comma-separated directory string into a slice.
func ParseKubeconfigDirs(dirs string) []string {
	if dirs == "" {
		return nil
	}
	var result []string
	for dir := range strings.SplitSeq(dirs, ",") {
		dir = strings.TrimSpace(dir)
		if dir != "" {
			result = append(result, dir)
		}
	}
	return result
}
