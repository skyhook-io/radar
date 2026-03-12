package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/updater"
	versionpkg "github.com/skyhook-io/radar/internal/version"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/klog/v2"
)

var (
	version = "dev"
)

func main() {
	// Load persistent config (~/.radar/config.json) for flag defaults.
	fileCfg := config.Load()

	// Parse flags (defaults come from config file, falling back to hardcoded values)
	kubeconfig := flag.String("kubeconfig", fileCfg.Kubeconfig, "Path to kubeconfig file (default: ~/.kube/config)")
	kubeconfigDir := flag.String("kubeconfig-dir", fileCfg.KubeconfigDirsFlag(), "Comma-separated directories containing kubeconfig files (mutually exclusive with --kubeconfig)")
	namespace := flag.String("namespace", fileCfg.Namespace, "Initial namespace filter (empty = all namespaces)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	historyLimit := flag.Int("history-limit", fileCfg.HistoryLimitOr(10000), "Maximum number of events to retain in timeline")
	debugEvents := flag.Bool("debug-events", false, "Enable verbose event debugging")
	fakeInCluster := flag.Bool("fake-in-cluster", false, "Simulate in-cluster mode for testing")
	disableHelmWrite := flag.Bool("disable-helm-write", false, "Simulate restricted Helm permissions")
	timelineStorage := flag.String("timeline-storage", fileCfg.TimelineStorageOr("memory"), "Timeline storage backend: memory or sqlite")
	timelineDBPath := flag.String("timeline-db", fileCfg.TimelineDBPath, "Path to timeline database file (default: ~/.radar/timeline.db)")
	prometheusURL := flag.String("prometheus-url", fileCfg.PrometheusURL, "Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("radar-desktop %s\n", version)
		os.Exit(0)
	}

	// Suppress verbose client-go logs
	klog.InitFlags(nil)
	_ = flag.Set("v", "0")
	_ = flag.Set("logtostderr", "false")
	_ = flag.Set("alsologtostderr", "false")
	klog.SetOutput(os.Stderr)

	log.Printf("Radar Desktop %s starting...", version)

	// GUI apps (macOS .app, Linux .desktop) get a minimal PATH that
	// doesn't include user-installed tools like gke-gcloud-auth-plugin,
	// gcloud, aws CLI, etc. Enrich PATH from the user's login shell.
	enrichPath()

	// On Linux, detect system dark mode via xdg-desktop-portal D-Bus and
	// set GTK_THEME so WebKitGTK's prefers-color-scheme media query works.
	applySystemTheme()

	if *kubeconfig != "" && *kubeconfigDir != "" {
		log.Fatalf("--kubeconfig and --kubeconfig-dir are mutually exclusive")
	}

	cfg := app.AppConfig{
		Kubeconfig:       *kubeconfig,
		KubeconfigDirs:   app.ParseKubeconfigDirs(*kubeconfigDir),
		Namespace:        *namespace,
		Port:             fileCfg.PortOr(0), // Configured port, or random to avoid conflicts with CLI
		DevMode:          false,
		HistoryLimit:     *historyLimit,
		DebugEvents:      *debugEvents,
		FakeInCluster:    *fakeInCluster,
		DisableHelmWrite: *disableHelmWrite,
		TimelineStorage:  *timelineStorage,
		TimelineDBPath:   *timelineDBPath,
		PrometheusURL:    *prometheusURL,
		Version:          version,
		MCPEnabled:       fileCfg.MCPEnabledOr(true),
	}

	app.SetGlobals(cfg)
	versionpkg.SetDesktop(true)

	// Clean up leftover files from previous update
	updater.CleanupOldUpdate()

	if err := app.InitializeK8s(cfg); err != nil {
		log.Fatalf("%v", err)
	}

	timelineStoreCfg := app.BuildTimelineStoreConfig(cfg)
	app.RegisterCallbacks(cfg, timelineStoreCfg)

	// Create server and attach desktop updater
	srv := app.CreateServer(cfg)
	desktopUpdater := updater.New()
	srv.SetUpdater(desktopUpdater)

	// Start server and wait until it's accepting connections
	ready := make(chan struct{})
	go func() {
		if err := srv.StartWithReady(ready); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}()
	<-ready

	// Write port file so MCP clients can discover the running server
	app.WriteMCPPortFile(srv.ActualPort())

	// Initialize cluster in background (browser will see progress via SSE)
	go app.InitializeCluster()

	// Track opens and maybe prompt to star (non-blocking)
	app.MaybePromptGitHubStar()

	// Build window title
	windowTitle := "Radar"
	if ctx := k8s.GetContextName(); ctx != "" {
		windowTitle = "Radar — " + ctx
	}

	// Create desktop app
	desktopApp := NewDesktopApp(srv, timelineStoreCfg)

	// Run Wails application
	err := wails.Run(&options.App{
		Title:            windowTitle,
		Width:            1440,
		Height:           900,
		MinWidth:         800,
		MinHeight:        600,
		MaxWidth:         7680,
		MaxHeight:        4320,
		WindowStartState: options.Maximised,

		AssetServer: &assetserver.Options{
			Handler: NewRedirectHandler(srv.ActualAddr(), cfg.Namespace),
		},

		Menu: createMenu(desktopApp),

		BackgroundColour: options.NewRGBA(10, 10, 15, 255),

		OnStartup:     desktopApp.startup,
		OnDomReady:    desktopApp.domReady,
		OnBeforeClose: desktopApp.beforeClose,
		OnShutdown:    desktopApp.shutdown,

		Bind: []any{
			desktopApp,
		},

		Mac: &mac.Options{
			TitleBar: mac.TitleBarDefault(),
			About: &mac.AboutInfo{
				Title:   "Radar",
				Message: "Kubernetes Visibility Tool\nBuilt by Skyhook\n\nVersion: " + version,
			},
		},

		Linux: &linux.Options{
			ProgramName:      "radar",
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
	})

	if err != nil {
		log.Fatalf("Wails error: %v", err)
	}
}
