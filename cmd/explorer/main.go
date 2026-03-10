package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Register all auth provider plugins (OIDC, GCP, Azure, etc.)
	"k8s.io/klog/v2"
)

var (
	version = "dev"
)

func main() {
	startupStart := time.Now()

	// Load persistent config (~/.radar/config.json) for flag defaults.
	// CLI flags override config file values.
	fileCfg := config.Load()

	// Parse flags (defaults come from config file, falling back to hardcoded values)
	kubeconfig := flag.String("kubeconfig", fileCfg.Kubeconfig, "Path to kubeconfig file (default: ~/.kube/config)")
	kubeconfigDir := flag.String("kubeconfig-dir", fileCfg.KubeconfigDirsFlag(), "Comma-separated directories containing kubeconfig files (mutually exclusive with --kubeconfig)")
	namespace := flag.String("namespace", fileCfg.Namespace, "Initial namespace filter (empty = all namespaces)")
	port := flag.Int("port", fileCfg.PortOr(9280), "Server port")
	noBrowser := flag.Bool("no-browser", fileCfg.NoBrowser, "Don't auto-open browser")
	devMode := flag.Bool("dev", false, "Development mode (serve frontend from filesystem)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	historyLimit := flag.Int("history-limit", fileCfg.HistoryLimitOr(10000), "Maximum number of events to retain in timeline")
	debugEvents := flag.Bool("debug-events", false, "Enable verbose event debugging (logs all event drops)")
	fakeInCluster := flag.Bool("fake-in-cluster", false, "Simulate in-cluster mode for testing (shows kubectl copy buttons instead of port-forward)")
	disableHelmWrite := flag.Bool("disable-helm-write", false, "Simulate restricted Helm permissions (disables install/upgrade/rollback/uninstall)")
	// Timeline storage options
	timelineStorage := flag.String("timeline-storage", fileCfg.TimelineStorageOr("memory"), "Timeline storage backend: memory or sqlite")
	timelineDBPath := flag.String("timeline-db", fileCfg.TimelineDBPath, "Path to timeline database file (default: ~/.radar/timeline.db)")
	// Traffic/metrics options
	prometheusURL := flag.String("prometheus-url", fileCfg.PrometheusURL, "Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)")
	// MCP server
	noMCP := flag.Bool("no-mcp", !fileCfg.MCPEnabledOr(true), "Disable MCP (Model Context Protocol) server for AI tools")
	flag.Parse()

	if *showVersion {
		fmt.Printf("radar %s\n", version)
		os.Exit(0)
	}

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

	cfg := app.AppConfig{
		Kubeconfig:       *kubeconfig,
		KubeconfigDirs:   app.ParseKubeconfigDirs(*kubeconfigDir),
		Namespace:        *namespace,
		Port:             *port,
		NoBrowser:        *noBrowser,
		DevMode:          *devMode,
		HistoryLimit:     *historyLimit,
		DebugEvents:      *debugEvents,
		FakeInCluster:    *fakeInCluster,
		DisableHelmWrite: *disableHelmWrite,
		TimelineStorage:  *timelineStorage,
		TimelineDBPath:   *timelineDBPath,
		PrometheusURL:    *prometheusURL,
		MCPEnabled:       !*noMCP,
		Version:          version,
	}

	// Set global flags
	app.SetGlobals(cfg)

	// Initialize K8s client (local only — parses kubeconfig, no network)
	t := time.Now()
	if err := app.InitializeK8s(cfg); err != nil {
		log.Fatalf("%v", err)
	}
	k8s.LogTiming(" K8s client init: %v", time.Since(t))

	// Build timeline config and register callbacks
	t = time.Now()
	timelineStoreCfg := app.BuildTimelineStoreConfig(cfg)
	app.RegisterCallbacks(cfg, timelineStoreCfg)
	k8s.LogTiming(" Callbacks registered: %v", time.Since(t))

	// Create server
	t = time.Now()
	srv := app.CreateServer(cfg)
	k8s.LogTiming(" Server created: %v", time.Since(t))

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		app.Shutdown(srv)
		os.Exit(0)
	}()

	// Start server in background — wait for it to actually bind the port
	ready := make(chan struct{})
	go func() {
		if err := srv.StartWithReady(ready); err != nil {
			// "use of closed network connection" is expected when the listener
			// is closed during graceful shutdown — not an actual error.
			if !errors.Is(err, net.ErrClosed) {
				log.Fatalf("Server error: %v", err)
			}
		}
	}()
	<-ready
	k8s.LogTiming(" Server listening: %v (since start)", time.Since(startupStart))

	// Open browser — server is confirmed ready to accept connections
	if !cfg.NoBrowser {
		url := fmt.Sprintf("http://localhost:%d", cfg.Port)
		if cfg.Namespace != "" {
			url += fmt.Sprintf("?namespace=%s", cfg.Namespace)
		}
		go app.OpenBrowser(url)
	}

	// Now initialize cluster connection and caches (browser will see progress via SSE)
	app.InitializeCluster()
	k8s.LogTiming(" Total startup (to connected): %v", time.Since(startupStart))

	// Track opens and maybe prompt to star the repo on GitHub (non-blocking)
	app.MaybePromptGitHubStar()

	// Block forever (server is running in background)
	select {}
}
