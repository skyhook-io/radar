package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/static"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	versionpkg "github.com/skyhook-io/radar/internal/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Register all auth provider plugins (OIDC, GCP, Azure, etc.)
	"k8s.io/klog/v2"
)

var (
	version = "dev"
)

func main() {
	// Parse flags
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
	kubeconfigDir := flag.String("kubeconfig-dir", "", "Comma-separated directories containing kubeconfig files (mutually exclusive with --kubeconfig)")
	namespace := flag.String("namespace", "", "Initial namespace filter (empty = all namespaces)")
	port := flag.Int("port", 9280, "Server port")
	noBrowser := flag.Bool("no-browser", false, "Don't auto-open browser")
	devMode := flag.Bool("dev", false, "Development mode (serve frontend from filesystem)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	historyLimit := flag.Int("history-limit", 10000, "Maximum number of events to retain in timeline")
	debugEvents := flag.Bool("debug-events", false, "Enable verbose event debugging (logs all event drops)")
	fakeInCluster := flag.Bool("fake-in-cluster", false, "Simulate in-cluster mode for testing (shows kubectl copy buttons instead of port-forward)")
	// Timeline storage options
	timelineStorage := flag.String("timeline-storage", "memory", "Timeline storage backend: memory or sqlite")
	timelineDBPath := flag.String("timeline-db", "", "Path to timeline database file (default: ~/.radar/timeline.db)")
	flag.Parse()

	// Set debug mode for event tracking
	k8s.DebugEvents = *debugEvents
	k8s.ForceInCluster = *fakeInCluster

	if *showVersion {
		fmt.Printf("radar %s\n", version)
		os.Exit(0)
	}

	// Set version for update checking
	versionpkg.SetCurrent(version)

	// Suppress verbose client-go logs (reflector errors, traces, etc.)
	klog.InitFlags(nil)
	_ = flag.Set("v", "0")
	_ = flag.Set("logtostderr", "false")
	_ = flag.Set("alsologtostderr", "false")
	klog.SetOutput(os.Stderr)

	log.Printf("Radar %s starting...", version)

	// Validate mutually exclusive flags
	if *kubeconfig != "" && *kubeconfigDir != "" {
		log.Fatalf("--kubeconfig and --kubeconfig-dir are mutually exclusive")
	}

	// Parse kubeconfig directories if provided
	var kubeconfigDirs []string
	if *kubeconfigDir != "" {
		for _, dir := range strings.Split(*kubeconfigDir, ",") {
			dir = strings.TrimSpace(dir)
			if dir != "" {
				kubeconfigDirs = append(kubeconfigDirs, dir)
			}
		}
	}

	// Initialize K8s client
	err := k8s.Initialize(k8s.InitOptions{
		KubeconfigPath: *kubeconfig,
		KubeconfigDirs: kubeconfigDirs,
	})
	if err != nil {
		log.Fatalf("Failed to initialize K8s client: %v", err)
	}

	if len(kubeconfigDirs) > 0 {
		log.Printf("Using kubeconfigs from directories: %v", kubeconfigDirs)
	} else if kubepath := k8s.GetKubeconfigPath(); kubepath != "" {
		log.Printf("Using kubeconfig: %s", kubepath)
	} else {
		log.Printf("Using in-cluster config")
	}

	// Set initial connecting state
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Starting server...",
	})

	// Initialize timeline store config (needed for context switch reinit)
	timelineStoreCfg := timeline.StoreConfig{
		Type:    timeline.StoreTypeMemory,
		MaxSize: *historyLimit,
	}
	if *timelineStorage == "sqlite" {
		timelineStoreCfg.Type = timeline.StoreTypeSQLite
		dbPath := *timelineDBPath
		if dbPath == "" {
			homeDir, _ := os.UserHomeDir()
			dbPath = filepath.Join(homeDir, ".radar", "timeline.db")
		}
		timelineStoreCfg.Path = dbPath
	}

	// Register Helm reset/reinit functions for context switching (before any init)
	k8s.RegisterHelmFuncs(helm.ResetClient, helm.ReinitClient)

	// Register timeline store reset/reinit functions for context switching
	k8s.RegisterTimelineFuncs(timeline.ResetStore, func() error {
		return timeline.ReinitStore(timelineStoreCfg)
	})

	// Register traffic reset/reinit functions for context switching
	k8s.RegisterTrafficFuncs(traffic.Reset, func() error {
		return traffic.ReinitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
	})

	// Create server (but don't start yet)
	cfg := server.Config{
		Port:       *port,
		DevMode:    *devMode,
		StaticFS:   static.FS,
		StaticRoot: "dist",
	}

	srv := server.New(cfg)

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		srv.Stop()
		if cache := k8s.GetResourceCache(); cache != nil {
			cache.Stop()
		}
		if dynCache := k8s.GetDynamicResourceCache(); dynCache != nil {
			dynCache.Stop()
		}
		// Close timeline store
		timeline.ResetStore()
		os.Exit(0)
	}()

	// Start server in background so browser can connect while we initialize
	go func() {
		if err := srv.Start(); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Give server a moment to start accepting connections
	time.Sleep(100 * time.Millisecond)

	// Open browser - it can now connect and see progress updates
	if !*noBrowser {
		url := fmt.Sprintf("http://localhost:%d", *port)
		if *namespace != "" {
			url += fmt.Sprintf("?namespace=%s", *namespace)
		}
		go openBrowser(url)
	}

	// Now initialize cluster connection and caches (browser will see progress via SSE)
	initializeCluster(timelineStoreCfg)

	// Block forever (server is running in background)
	select {}
}

// initializeCluster connects to the cluster and initializes all caches.
// Progress is broadcast via SSE so the browser can show updates.
func initializeCluster(timelineStoreCfg timeline.StoreConfig) {
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Testing cluster connectivity...",
	})

	// Preflight check: verify cluster connectivity before starting informers
	clusterAccessErr := checkClusterAccess()

	// If cluster access failed, set disconnected state and skip cache initialization
	if clusterAccessErr != nil {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   k8s.GetContextName(),
			Error:     clusterAccessErr.Error(),
			ErrorType: k8s.ClassifyError(clusterAccessErr),
		})
		log.Printf("Warning: Cluster not reachable, starting in disconnected mode")
		return
	}

	// Cluster is accessible - initialize all caches with progress updates
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Initializing timeline...",
	})
	if err := timeline.InitStore(timelineStoreCfg); err != nil {
		log.Fatalf("Failed to initialize timeline store: %v", err)
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Loading workloads...",
	})
	if err := k8s.InitResourceCache(); err != nil {
		log.Fatalf("Failed to initialize resource cache: %v", err)
	}
	log.Printf("Resource cache initialized with %d resources", k8s.GetResourceCache().GetResourceCount())

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Discovering API resources...",
	})
	if err := k8s.InitResourceDiscovery(); err != nil {
		log.Printf("Warning: Failed to initialize resource discovery: %v", err)
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Loading custom resources...",
	})
	changeCh := k8s.GetResourceCache().ChangesRaw()
	if err := k8s.InitDynamicResourceCache(changeCh); err != nil {
		log.Printf("Warning: Failed to initialize dynamic resource cache: %v", err)
	}

	// Warm up dynamic cache for common CRDs so they appear in initial timeline
	k8s.WarmupCommonCRDs()

	// Start full CRD discovery in background (for generic CRD topology support)
	if dynamicCache := k8s.GetDynamicResourceCache(); dynamicCache != nil {
		dynamicCache.DiscoverAllCRDs()
	}

	// Initialize metrics history collection (polls metrics-server every 30s)
	k8s.InitMetricsHistory()

	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnecting,
		Context:     k8s.GetContextName(),
		ProgressMsg: "Loading Helm releases...",
	})
	if err := helm.Initialize(k8s.GetKubeconfigPath()); err != nil {
		log.Printf("Warning: Failed to initialize Helm client: %v", err)
	}

	// Initialize traffic source manager with full config for port-forward support
	if err := traffic.InitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName()); err != nil {
		log.Printf("Warning: Failed to initialize traffic manager: %v", err)
	}

	// Set connected state
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:       k8s.StateConnected,
		Context:     k8s.GetContextName(),
		ClusterName: k8s.GetClusterName(),
	})
}

func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		log.Printf("Cannot open browser on %s, please open manually: %s", runtime.GOOS, url)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to open browser: %v", err)
		log.Printf("Please open manually: %s", url)
	}
}

// checkClusterAccess verifies connectivity to the Kubernetes cluster before starting informers.
// Returns the error from the K8s API if connection or authentication fails.
// The error is handled gracefully - the UI will show it with retry options.
func checkClusterAccess() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientset := k8s.GetClient()
	if clientset == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}

	// Try to list namespaces as a basic connectivity check
	_, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	return err
}
