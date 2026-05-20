package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"golang.org/x/net/http/httpguts"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Register all auth provider plugins (OIDC, GCP, Azure, etc.)
	"k8s.io/klog/v2"
)

var (
	version = "dev"
)

func main() {
	startupStart := time.Now()

	// Propagate the build-time version to the cloud dialer so the agent
	// advertises the real version (e.g. "1.5.5") on the tunnel handshake
	// instead of the "dev" default. Dockerfile + Makefile inject
	// `-X main.version=...`; mirror it here rather than adding a second
	// ldflags target so there's a single source of truth.
	cloud.Version = version

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
	timelineRetention := flag.Duration("timeline-retention", fileCfg.TimelineRetentionOr(7*24*time.Hour), "How long to retain timeline events when --timeline-storage=sqlite (e.g. 168h, 720h). 0 disables cleanup (unbounded growth).")
	// Traffic/metrics options
	prometheusURL := flag.String("prometheus-url", fileCfg.PrometheusURL, "Manual Prometheus/VictoriaMetrics URL (skips auto-discovery)")
	// --prometheus-header Key=Value, repeatable. Defaults populated from
	// config file; any --prometheus-header flag replaces the file value rather
	// than merging — matches kubectl semantics (file is the default, CLI wins).
	promHeaders := newHeaderFlag(fileCfg.PrometheusHeaders)
	flag.Var(promHeaders, "prometheus-header", "HTTP header to send with Prometheus requests, e.g. 'Authorization=Bearer <token>' (repeatable). Required for auth-protected backends.")
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
	authOIDCScopes := flag.String("auth-oidc-scopes", "openid,profile,email,groups", "Comma-separated OAuth2 scopes requested at OIDC authorization (e.g. 'openid,profile,email,groups,offline_access')")
	authOIDCPostLogoutRedirectURL := flag.String("auth-oidc-post-logout-redirect-url", "", "URL to redirect after OIDC provider logout (must be registered with IdP)")
	authOIDCUsernamePrefix := flag.String("auth-oidc-username-prefix", "", "Prefix added to OIDC username for K8s impersonation (must match kube-apiserver --oidc-username-prefix)")
	authOIDCGroupsPrefix := flag.String("auth-oidc-groups-prefix", "", "Prefix added to OIDC groups for K8s impersonation (must match kube-apiserver --oidc-groups-prefix)")
	authOIDCInsecureSkipVerify := flag.Bool("auth-oidc-insecure-skip-verify", false, "Skip TLS certificate verification for OIDC provider (insecure, dev/test only)")
	authOIDCCACert := flag.String("auth-oidc-ca-cert", "", "Path to CA certificate file for OIDC provider TLS verification")
	authOIDCBackchannelLogout := flag.Bool("auth-oidc-backchannel-logout", false, "Enable OIDC Back-Channel Logout endpoint (single-replica only)")
	// Radar Cloud flags — enable hosted mode when --cloud-url is set.
	// Local-binary behavior is unchanged when these flags are empty. Each
	// flag falls back to an env var so Kubernetes deployments can source
	// the token from a Secret without exposing it in `ps` output.
	cloudURL := flag.String("cloud-url", os.Getenv("RADAR_CLOUD_URL"), "Radar Cloud WebSocket URL (e.g. wss://api.radarhq.io/agent) — empty = local-only. Env: RADAR_CLOUD_URL")
	cloudToken := flag.String("cloud-token", os.Getenv("RADAR_CLOUD_TOKEN"), "Cluster token from the Radar Cloud install wizard (rhc_<random>). Env: RADAR_CLOUD_TOKEN")
	cloudClusterName := flag.String("cluster-name", os.Getenv("RADAR_CLOUD_CLUSTER_NAME"), "Human-readable cluster name for Radar Cloud (required with --cloud-url). Env: RADAR_CLOUD_CLUSTER_NAME")
	flag.Parse()

	// Cloud-mode: Radar runs inside a customer cluster and fronts Radar
	// Cloud. Under cloud-mode the tunnel is the only path to this listener
	// and it delivers Cloud-authenticated identity headers on every request.
	// Force --auth-mode=proxy so Radar impersonates the Cloud user against
	// the K8s API instead of falling back to the ServiceAccount (which would
	// give every Cloud user full SA permissions).
	// Read once via the cloud package so we use the same normalized
	// parser (strconv.ParseBool — accepts true/1/T/TRUE etc.) as every
	// other site that reads RADAR_CLOUD_MODE. cloud.LogStartupMode
	// emits the resolved value below regardless of true/false so the
	// deployment topology is obvious in startup logs.
	cloudMode := cloud.Mode()
	if cloudMode {
		if *authMode != "none" && *authMode != "proxy" {
			log.Fatalf("RADAR_CLOUD_MODE=true incompatible with --auth-mode=%q: Cloud owns authn, only 'proxy' is supported", *authMode)
		}
		*authMode = "proxy"
		// Pin the header names to the Cloud's wire contract. Operators don't
		// get to retarget these; Cloud always sends X-Forwarded-User/Groups.
		*authUserHeader = "X-Forwarded-User"
		*authGroupsHeader = "X-Forwarded-Groups"
		log.Printf("[cloud] RADAR_CLOUD_MODE=true: auth-mode forced to proxy, trusting tunnel-supplied identity headers")
	}
	// Always log the resolved cloud mode (true OR false) so deployment
	// topology is visible in chart-install logs even when an operator
	// expected Cloud mode but typo'd the env var.
	cloud.LogStartupMode()

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
		Kubeconfig:           *kubeconfig,
		KubeconfigDirs:       app.ParseKubeconfigDirs(*kubeconfigDir),
		Namespace:            *namespace,
		Port:                 *port,
		NoBrowser:            *noBrowser,
		DevMode:              *devMode,
		HistoryLimit:         *historyLimit,
		DebugEvents:          *debugEvents,
		FakeInCluster:        *fakeInCluster,
		DisableHelmWrite:     *disableHelmWrite,
		DisableExec:          *disableExec,
		DisableLocalTerminal: *disableLocalTerminal,
		PodShellDefault:      *podShellDefault,
		TimelineStorage:      *timelineStorage,
		TimelineDBPath:       *timelineDBPath,
		TimelineRetention:    *timelineRetention,
		PrometheusURL:        *prometheusURL,
		PrometheusHeaders:    promHeaders.value(),
		MCPEnabled:           !*noMCP,
		Version:              version,
		AuthConfig: auth.Config{
			Mode:                      *authMode,
			Secret:                    *authSecret,
			CookieTTL:                 *authCookieTTL,
			UserHeader:                *authUserHeader,
			GroupsHeader:              *authGroupsHeader,
			OIDCIssuer:                *authOIDCIssuer,
			OIDCClientID:              *authOIDCClientID,
			OIDCClientSecret:          *authOIDCClientSecret,
			OIDCRedirectURL:           *authOIDCRedirectURL,
			OIDCGroupsClaim:           *authOIDCGroupsClaim,
			OIDCScopes:                parseCSV(*authOIDCScopes),
			OIDCPostLogoutRedirectURL: *authOIDCPostLogoutRedirectURL,
			OIDCUsernamePrefix:        *authOIDCUsernamePrefix,
			OIDCGroupsPrefix:          *authOIDCGroupsPrefix,
			OIDCInsecureSkipVerify:    *authOIDCInsecureSkipVerify,
			OIDCCACert:                *authOIDCCACert,
			OIDCBackchannelLogout:     *authOIDCBackchannelLogout,
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
	// workers (cloud tunnel, etc.) observe this to shut down cleanly before
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

	// When --cloud-url is set, dial out to Radar Cloud and serve the
	// existing router over yamux-tunneled streams. No behavior change
	// when empty.
	if *cloudURL != "" {
		if *cloudToken == "" || *cloudClusterName == "" {
			log.Fatalf("--cloud-url requires --cloud-token and --cluster-name")
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[cloud] panic in cloud tunnel: %v — local Radar continues to serve", r)
				}
			}()
			// Try to discover the external API server URL from
			// kube-public/cluster-info so the hub can correlate this
			// cluster against Argo CD's destination references. Best-
			// effort: empty when the ConfigMap is absent (managed K8s)
			// or RBAC denies the read. 3s timeout — the connect path
			// shouldn't block on a single ConfigMap GET.
			discoverCtx, cancel := context.WithTimeout(rootCtx, 3*time.Second)
			apiServerURL := cloud.DiscoverAPIServerURL(discoverCtx, k8s.GetClient())
			cancel()
			runErr := cloud.Run(rootCtx, cloud.Config{
				URL:          *cloudURL,
				Token:        *cloudToken,
				ClusterID:    *cloudClusterName,
				ClusterName:  *cloudClusterName,
				Namespace:    os.Getenv("MY_POD_NAMESPACE"),
				APIServerURL: apiServerURL,
				Handler:      srv.Handler(),
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

func parseCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// headerFlag is a flag.Value that accumulates repeated --prometheus-header
// Key=Value pairs into a map. The first Set call after construction wipes any
// defaults populated from the config file (kubectl-style: file = default, CLI
// wins outright instead of merging).
type headerFlag struct {
	m         map[string]string
	overrides bool
}

func newHeaderFlag(defaults map[string]string) *headerFlag {
	out := make(map[string]string, len(defaults))
	for k, v := range defaults {
		if !httpguts.ValidHeaderFieldName(k) {
			log.Printf("[config] Dropping invalid prometheus header name %q (must be RFC 7230 tokens)", k)
			continue
		}
		if !httpguts.ValidHeaderFieldValue(v) {
			log.Printf("[config] Dropping prometheus header %q: value contains control characters", k)
			continue
		}
		out[k] = v
	}
	return &headerFlag{m: out}
}

// value returns a defensive copy of the accumulated headers (nil if empty).
func (h *headerFlag) value() map[string]string {
	if len(h.m) == 0 {
		return nil
	}
	out := make(map[string]string, len(h.m))
	maps.Copy(out, h.m)
	return out
}

func (h *headerFlag) String() string {
	if len(h.m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(h.m))
	for k := range h.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+h.m[k])
	}
	return strings.Join(parts, ",")
}

func (h *headerFlag) Set(raw string) error {
	idx := strings.IndexByte(raw, '=')
	if idx <= 0 {
		return fmt.Errorf("expected Key=Value, got %q", raw)
	}
	key := strings.TrimSpace(raw[:idx])
	val := raw[idx+1:]
	if key == "" {
		return fmt.Errorf("empty header key in %q", raw)
	}
	// Reject anything net/http would silently corrupt or refuse at send time
	// (control bytes, separators in the key, CR/LF in the value — the classic
	// CRLF-injection vector for header smuggling).
	if !httpguts.ValidHeaderFieldName(key) {
		return fmt.Errorf("invalid header name %q (must be RFC 7230 tokens)", key)
	}
	if !httpguts.ValidHeaderFieldValue(val) {
		return fmt.Errorf("invalid header value for %q (control characters not allowed)", key)
	}
	if !h.overrides {
		// First CLI flag wipes file defaults — all-or-nothing replacement.
		h.m = make(map[string]string)
		h.overrides = true
	}
	h.m[key] = val
	return nil
}
