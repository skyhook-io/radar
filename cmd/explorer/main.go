package main

import (
	"context"
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
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
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
	disableExec := flag.Bool("disable-exec", false, "Simulate restricted exec permissions (disables terminal, debug shell)")
	disableLocalTerminal := flag.Bool("disable-local-terminal", false, "Disable local terminal feature")
	podShellDefault := flag.String("pod-shell-default", "", "Override the default pod exec shell command (runs as 'sh -c <value>'; empty = built-in bash -il → ash → sh cascade)")
	// Timeline storage options
	timelineStorage := flag.String("timeline-storage", fileCfg.TimelineStorageOr("memory"), "Timeline storage backend: memory or sqlite")
	timelineDBPath := flag.String("timeline-db", fileCfg.TimelineDBPath, "Path to timeline database file (default: ~/.radar/timeline.db)")
	// Traffic/metrics options
	prometheusURL := flag.String("prometheus-url", fileCfg.PrometheusURL, "Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)")
	// MCP server
	noMCP := flag.Bool("no-mcp", !fileCfg.MCPEnabledOr(true), "Disable MCP (Model Context Protocol) server for AI tools")
	// Auth flags
	authMode := flag.String("auth-mode", "none", "Authentication mode: none, proxy, or oidc")
	authSecret := flag.String("auth-secret", "", "HMAC secret key for session cookies (auto-generated if empty)")
	authCookieTTL := flag.Duration("auth-cookie-ttl", 4*time.Hour, "Session cookie TTL (sliding — extends on activity)")
	authUserHeader := flag.String("auth-user-header", "X-Forwarded-User", "Header for username (proxy mode)")
	authGroupsHeader := flag.String("auth-groups-header", "X-Forwarded-Groups", "Header for groups (proxy mode)")
	authOIDCIssuer := flag.String("auth-oidc-issuer", "", "OIDC issuer URL")
	authOIDCClientID := flag.String("auth-oidc-client-id", "", "OIDC client ID")
	authOIDCClientSecret := flag.String("auth-oidc-client-secret", "", "OIDC client secret")
	authOIDCRedirectURL := flag.String("auth-oidc-redirect-url", "", "OIDC redirect URL")
	authOIDCGroupsClaim := flag.String("auth-oidc-groups-claim", "groups", "JWT claim for groups")
	authOIDCPostLogoutRedirectURL := flag.String("auth-oidc-post-logout-redirect-url", "", "URL to redirect after OIDC provider logout (must be registered with IdP)")
	authOIDCUsernamePrefix := flag.String("auth-oidc-username-prefix", "", "Prefix added to OIDC username for K8s impersonation (must match kube-apiserver --oidc-username-prefix)")
	authOIDCGroupsPrefix := flag.String("auth-oidc-groups-prefix", "", "Prefix added to OIDC groups for K8s impersonation (must match kube-apiserver --oidc-groups-prefix)")
	authOIDCInsecureSkipVerify := flag.Bool("auth-oidc-insecure-skip-verify", false, "Skip TLS certificate verification for OIDC provider (insecure, dev/test only)")
	authOIDCCACert := flag.String("auth-oidc-ca-cert", "", "Path to CA certificate file for OIDC provider TLS verification")
	authOIDCBackchannelLogout := flag.Bool("auth-oidc-backchannel-logout", false, "Enable OIDC Back-Channel Logout endpoint (single-replica only)")
	// Radar Hub (cloud) flags — enable hosted mode when --hub-url is set.
	// Local-binary behavior is unchanged when these flags are empty. Each
	// flag falls back to an env var so Kubernetes deployments can source
	// the token from a Secret without exposing it in `ps` output.
	hubURL := flag.String("hub-url", os.Getenv("RADAR_HUB_URL"), "Radar Hub WebSocket URL (e.g. wss://api.radar.skyhook.io/agent) — empty = local-only. Env: RADAR_HUB_URL")
	hubToken := flag.String("hub-token", os.Getenv("RADAR_HUB_TOKEN"), "Cluster token from the Radar Hub install wizard (rhc_<random>). Env: RADAR_HUB_TOKEN")
	hubClusterName := flag.String("cluster-name", os.Getenv("RADAR_HUB_CLUSTER_NAME"), "Human-readable cluster name for Radar Hub (required with --hub-url). Env: RADAR_HUB_CLUSTER_NAME")
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

	// Validate flags
	switch *authMode {
	case "none", "proxy", "oidc":
		// valid
	default:
		log.Fatalf("Invalid --auth-mode %q: must be none, proxy, or oidc", *authMode)
	}
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
		DisableExec:          *disableExec,
		DisableLocalTerminal: *disableLocalTerminal,
		PodShellDefault:      *podShellDefault,
		TimelineStorage:  *timelineStorage,
		TimelineDBPath:   *timelineDBPath,
		PrometheusURL:    *prometheusURL,
		MCPEnabled:       !*noMCP,
		Version:          version,
		AuthConfig: auth.Config{
			Mode:            *authMode,
			Secret:          *authSecret,
			CookieTTL:       *authCookieTTL,
			UserHeader:      *authUserHeader,
			GroupsHeader:    *authGroupsHeader,
			OIDCIssuer:      *authOIDCIssuer,
			OIDCClientID:    *authOIDCClientID,
			OIDCClientSecret: *authOIDCClientSecret,
			OIDCRedirectURL:           *authOIDCRedirectURL,
			OIDCGroupsClaim:           *authOIDCGroupsClaim,
			OIDCPostLogoutRedirectURL:  *authOIDCPostLogoutRedirectURL,
			OIDCUsernamePrefix:         *authOIDCUsernamePrefix,
			OIDCGroupsPrefix:           *authOIDCGroupsPrefix,
			OIDCInsecureSkipVerify:     *authOIDCInsecureSkipVerify,
			OIDCCACert:                 *authOIDCCACert,
			OIDCBackchannelLogout:      *authOIDCBackchannelLogout,
		},
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

	// Root context cancelled on SIGINT/SIGTERM. Long-running background
	// workers (hub tunnel, etc.) observe this to shut down cleanly before
	// the process exits.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		rootCancel()
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

	// Write port file so MCP clients can discover the running server
	app.WriteMCPPortFile(srv.ActualPort())

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

	// When --hub-url is set, dial out to the hub and serve the existing
	// router over yamux-tunneled streams. No behavior change when empty.
	if *hubURL != "" {
		if *hubToken == "" || *hubClusterName == "" {
			log.Fatalf("--hub-url requires --hub-token and --cluster-name")
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[cloud] panic in hub tunnel: %v — local Radar continues to serve", r)
				}
			}()
			runErr := cloud.Run(rootCtx, cloud.Config{
				HubURL:      *hubURL,
				Token:       *hubToken,
				ClusterID:   *hubClusterName,
				ClusterName: *hubClusterName,
				Handler:     srv.Handler(),
			})
			if runErr != nil && !errors.Is(runErr, context.Canceled) {
				log.Printf("[cloud] tunnel exited: %v", runErr)
			}
		}()
	}

	// Track opens and maybe prompt to star the repo on GitHub (non-blocking)
	app.MaybePromptGitHubStar()

	// Block forever (server is running in background)
	select {}
}
