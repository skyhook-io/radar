package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/app"
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
	disableHelmWrite := flag.Bool("disable-helm-write", false, "Simulate restricted Helm permissions (disables install/upgrade/rollback/uninstall)")
	// Timeline storage options
	timelineStorage := flag.String("timeline-storage", "memory", "Timeline storage backend: memory or sqlite")
	timelineDBPath := flag.String("timeline-db", "", "Path to timeline database file (default: ~/.radar/timeline.db)")
	// Traffic/metrics options
	prometheusURL := flag.String("prometheus-url", "", "Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)")
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
		Version:          version,
	}

	// Set global flags
	app.SetGlobals(cfg)

	// Initialize K8s client
	if err := app.InitializeK8s(cfg); err != nil {
		log.Fatalf("%v", err)
	}

	// Build timeline config and register callbacks
	timelineStoreCfg := app.BuildTimelineStoreConfig(cfg)
	app.RegisterCallbacks(cfg, timelineStoreCfg)

	// Create server
	srv := app.CreateServer(cfg)

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		app.Shutdown(srv)
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
	if !cfg.NoBrowser {
		url := fmt.Sprintf("http://localhost:%d", cfg.Port)
		if cfg.Namespace != "" {
			url += fmt.Sprintf("?namespace=%s", cfg.Namespace)
		}
		go app.OpenBrowser(url)
	}

	// Now initialize cluster connection and caches (browser will see progress via SSE)
	app.InitializeCluster()

	// Block forever (server is running in background)
	select {}
}
