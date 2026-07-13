package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	mcppkg "github.com/skyhook-io/radar/internal/mcp"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/internal/settings"
	"github.com/skyhook-io/radar/internal/static"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	versionpkg "github.com/skyhook-io/radar/internal/version"
)

var clusterConnectionProbe = k8s.TestClusterConnection

// AppConfig holds all parsed configuration for the Radar application.
type AppConfig struct {
	Kubeconfig               string
	KubeconfigDirs           []string
	Namespace                string
	Namespaces               []string
	Port                     int
	NoBrowser                bool
	Browser                  string
	DevMode                  bool
	HistoryLimit             int
	DebugEvents              bool
	FakeInCluster            bool
	DisableHelmWrite         bool
	DisableExec              bool
	DisableLocalTerminal     bool
	PodShellDefault          string
	DebugImage               string
	ListPageSize             int64
	NamespaceScope           bool
	TimelineStorage          string
	TimelineDBPath           string
	TimelineRetention        time.Duration
	TimelineMaxSizeBytes     int64
	PrometheusURL            string
	PrometheusHeaders        map[string]string
	PrometheusHeadersFromEnv map[string]string
	Version                  string
	MCPEnabled               bool
	AIHistory                bool   // persist AI investigations across restarts
	AIHistoryDBPath          string // "" = ~/.radar/ai-runs.db
	AuthConfig               auth.Config
}

// SetGlobals applies debug/test flags to global state.
func SetGlobals(cfg AppConfig) {
	k8s.DebugEvents = cfg.DebugEvents
	k8s.TimingLogs = cfg.DevMode
	k8s.ForceInCluster = cfg.FakeInCluster
	k8s.ForceDisableHelmWrite = cfg.DisableHelmWrite
	k8s.ForceDisableExec = cfg.DisableExec
	k8s.ForceDisableLocalTerminal = cfg.DisableLocalTerminal
	k8s.ListPageSize = cfg.ListPageSize
	k8s.ForceNamespaceScope = cfg.NamespaceScope
	server.DefaultPodShellCommand = cfg.PodShellDefault
	versionpkg.SetCurrent(cfg.Version)
}

// validateNamespaceFanout rejects a --namespaces list that cannot fully fit
// in the probe candidate cap. The kubeconfig context namespace is prepended
// to the candidates, so a distinct one occupies a cap slot the flag-time
// length check could not account for (kubeconfig wasn't parsed yet). Failing
// here beats silently dropping the last configured namespace from probing.
func validateNamespaceFanout(namespaces []string, ctxNs string, maxCandidates int) error {
	slots := len(namespaces)
	if ctxNs != "" && !slices.Contains(namespaces, ctxNs) {
		slots++
	}
	if slots > maxCandidates {
		return fmt.Errorf("--namespaces lists %d namespaces and the kubeconfig context namespace %q adds one more probe candidate, exceeding the fanout cap of %d; raise --max-scope-candidates or include the context namespace in the list", len(namespaces), ctxNs, maxCandidates)
	}
	return nil
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

	if len(cfg.Namespaces) > 0 {
		k8s.SetFallbackNamespaces(cfg.Namespaces)
		if err := validateNamespaceFanout(cfg.Namespaces, k8s.GetContextNamespace(), k8s.MaxScopeCandidates); err != nil {
			return err
		}
	} else if cfg.Namespace != "" {
		k8s.SetFallbackNamespace(cfg.Namespace)
	}
	configureNamespaceScopePreferenceResolver(cfg)
	if cfg.NamespaceScope {
		if err := validateNamespaceScopeTarget(k8s.GetNamespaceScopeTarget()); err != nil {
			return err
		}
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

// validateNamespaceScopeTarget enforces that --namespace-scope resolves to
// exactly one valid namespace. Multiple namespaces (e.g. --namespace=a,b) are
// not supported yet — the informer cache pins to a single namespace — so reject
// them at startup with a clear message instead of silently caching an invalid one.
func validateNamespaceScopeTarget(target string) error {
	if target == "" {
		return fmt.Errorf("--namespace-scope requires --namespace or a namespace on the current kubeconfig context")
	}
	if errs := validation.IsDNS1123Label(target); len(errs) > 0 {
		return fmt.Errorf("--namespace-scope supports a single namespace; %q is not a valid namespace name (multiple namespaces are not supported yet): %s", target, strings.Join(errs, "; "))
	}
	return nil
}

func configureNamespaceScopePreferenceResolver(cfg AppConfig) {
	k8s.SetNamespaceScopePreferenceResolver(nil)
	if !cfg.NamespaceScope || cfg.AuthConfig.Enabled() {
		return
	}
	// Resolve the scoped namespace from the local per-context pick. Registered
	// even when --namespace is set, so a UI rescope (which persists its pick)
	// survives a reconnect / context switch instead of snapping back.
	k8s.SetNamespaceScopePreferenceResolver(func(ctxName string) (string, bool) {
		activeNamespaces := settings.Load().ActiveNamespaces
		if len(activeNamespaces[ctxName]) == 1 && activeNamespaces[ctxName][0] != "" {
			return activeNamespaces[ctxName][0], true
		}
		return "", false
	})
	// Treat an explicit --namespace like a UI pick of that namespace: seed it as
	// this run's authoritative starting scope (overwriting a stale pick from a
	// previous run). A later UI rescope overwrites it, and that rescope is what
	// then survives reconnects.
	if cfg.Namespace != "" {
		seedNamespaceScopePick(k8s.GetContextName(), cfg.Namespace)
	}
	k8s.RestoreNamespaceScopePreference(k8s.GetContextName())
}

// seedNamespaceScopePick persists ns as the single active namespace for ctxName,
// mirroring what a UI namespace pick stores. No-op on an empty context name.
func seedNamespaceScopePick(ctxName, ns string) {
	if ctxName == "" {
		return
	}
	if _, err := settings.Update(func(st *settings.Settings) {
		if st.ActiveNamespaces == nil {
			st.ActiveNamespaces = map[string][]string{}
		}
		st.ActiveNamespaces[ctxName] = []string{ns}
	}); err != nil {
		log.Printf("[namespace] failed to seed namespace pick for context %q: %v", ctxName, err)
	}
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
		storeCfg.RetentionAge = cfg.TimelineRetention
		storeCfg.MaxStorageBytes = cfg.TimelineMaxSizeBytes
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
	if len(cfg.PrometheusHeaders) > 0 {
		traffic.SetMetricsHeaders(cfg.PrometheusHeaders)
		prometheuspkg.SetHeaders(cfg.PrometheusHeaders)
	}

	k8s.RegisterTrafficFuncs(traffic.Reset, func() error {
		return traffic.ReinitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
	})

	// Reinitialize carries the current manual URL + headers forward (including any
	// applied live via /integrations/prometheus). Re-applying the captured startup
	// cfg here would revert a live change on context switch, so we don't.
	k8s.RegisterPrometheusFuncs(prometheuspkg.Reset, func() error {
		prometheuspkg.Reinitialize(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
		return nil
	})
}

// CreateServer creates the HTTP server with the given configuration.
func CreateServer(cfg AppConfig) *server.Server {
	effectiveCfg := &config.Config{
		Kubeconfig:               cfg.Kubeconfig,
		KubeconfigDirs:           cfg.KubeconfigDirs,
		Namespace:                cfg.Namespace,
		Namespaces:               cfg.Namespaces,
		Port:                     cfg.Port,
		NoBrowser:                cfg.NoBrowser,
		Browser:                  cfg.Browser,
		TimelineStorage:          cfg.TimelineStorage,
		TimelineDBPath:           cfg.TimelineDBPath,
		TimelineMaxSize:          fmt.Sprintf("%d", cfg.TimelineMaxSizeBytes),
		HistoryLimit:             cfg.HistoryLimit,
		PrometheusURL:            cfg.PrometheusURL,
		PrometheusHeaders:        cfg.PrometheusHeaders,
		PrometheusHeadersFromEnv: cfg.PrometheusHeadersFromEnv,
		DebugImage:               cfg.DebugImage,
		MCP:                      &cfg.MCPEnabled,
	}

	serverCfg := server.Config{
		Port:            cfg.Port,
		DevMode:         cfg.DevMode,
		StaticFS:        static.FS,
		StaticRoot:      "dist",
		EffectiveConfig: effectiveCfg,
		DiagConfig: &server.DiagConfig{
			Port:                 cfg.Port,
			DevMode:              cfg.DevMode,
			Namespace:            cfg.Namespace,
			TimelineStorage:      cfg.TimelineStorage,
			HistoryLimit:         cfg.HistoryLimit,
			DebugEvents:          cfg.DebugEvents,
			MCPEnabled:           cfg.MCPEnabled,
			HasPrometheusURL:     cfg.PrometheusURL != "",
			HasPrometheusHeaders: len(cfg.PrometheusHeaders) > 0,
		},
		AuthConfig: cfg.AuthConfig,
	}

	// AI-history DB path: resolved here (like the timeline DB) so the server
	// only sees a ready-to-open path. Only meaningful where the AI engine can
	// actually enable (no-auth + MCP mounted) — the server checks that gate.
	if cfg.AIHistory {
		dbPath := cfg.AIHistoryDBPath
		if dbPath == "" {
			homeDir, _ := os.UserHomeDir()
			dbPath = filepath.Join(homeDir, ".radar", "ai-runs.db")
		}
		serverCfg.AIHistoryDB = dbPath
	}

	if cfg.MCPEnabled {
		serverCfg.MCPHandler = mcppkg.NewHandler()
		serverCfg.MCPReadOnlyHandler = mcppkg.NewReadOnlyHandler()
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
		errorType := k8s.ClassifyError(err)
		k8s.SetConnectionStatus(k8s.ConnectionStatus{
			State:     k8s.StateDisconnected,
			Context:   k8s.GetContextName(),
			Error:     err.Error(),
			ErrorType: errorType,
		})
		log.Printf("[ops] InitializeCluster FAILED: %v (errorType=%s, %v elapsed)", err, errorType, time.Since(clusterStart))

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

// mcpPortFileDisabled suppresses port-file writes AND removals — an ephemeral
// instance (radar diagnose --standalone) must never clobber or delete the slot
// a real long-running Radar owns.
var mcpPortFileDisabled bool

// DisableMCPPortFile makes Write/RemoveMCPPortFile no-ops for this process.
func DisableMCPPortFile() { mcpPortFileDisabled = true }

// WriteMCPPortFile writes the actual server port to ~/.radar/mcp-port so MCP
// clients can discover the running instance without hardcoding a port.
func WriteMCPPortFile(port int) {
	path := mcpPortFilePath()
	if path == "" || mcpPortFileDisabled {
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
	if path == "" || mcpPortFileDisabled {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[mcp] Failed to remove port file %s: %v", path, err)
	}
}

func mcpPortFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[mcp] Cannot determine home directory: %v (port file will not be written)", err)
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
// Deterministic errors (config, auth, RBAC, network, TLS) skip the retry —
// retrying missing config, bad credentials, denied permissions, bad certs, or
// unreachable hosts won't help. Timeout-shaped exec-auth failures are still
// retryable because the first call may trigger a token refresh, with the cached
// token ready by retry.
func CheckClusterAccess(ctx context.Context) error {
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
			if errType == "config" || errType == "auth" || errType == "rbac" || errType == "network" || errType == "tls" {
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

		attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		t := time.Now()
		err := clusterConnectionProbe(attemptCtx)
		cancel()

		if err == nil {
			k8s.LogTiming("   Cluster /version check (attempt %d): %v", attempt+1, time.Since(t))
			return nil
		}
		if ctx.Err() != nil {
			if lastErr != nil {
				return fmt.Errorf("failed to connect to cluster: %w", lastErr)
			}
			return fmt.Errorf("failed to connect to cluster: %w", err)
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

// ParseNamespaces splits a comma-separated namespace string into a de-duplicated
// slice. Empty items are ignored so flags like "--namespaces a,,b" behave like
// kubectl's comma-separated lists instead of creating an empty namespace pick.
func ParseNamespaces(namespaces string) []string {
	if namespaces == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var result []string
	for ns := range strings.SplitSeq(namespaces, ",") {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		result = append(result, ns)
	}
	return result
}

// ResolveNamespaceSelection applies CLI/config precedence for --namespace and
// --namespaces. Explicit flags win over config defaults; setting both flags
// explicitly is ambiguous and returns an error.
func ResolveNamespaceSelection(namespace, namespaces string, namespaceSet, namespacesSet bool) (string, []string, error) {
	namespace = strings.TrimSpace(namespace)
	parsedNamespaces := ParseNamespaces(namespaces)
	if namespaceSet && namespacesSet && namespace != "" && len(parsedNamespaces) > 0 {
		return "", nil, fmt.Errorf("--namespace and --namespaces are mutually exclusive")
	}
	if len(parsedNamespaces) > 0 && (namespacesSet || !namespaceSet) {
		return "", parsedNamespaces, nil
	}
	if namespace == "" {
		return "", nil, nil
	}
	return namespace, nil, nil
}
