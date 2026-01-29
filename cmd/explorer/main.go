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

	"github.com/skyhook-io/skyhook-explorer/internal/helm"
	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
	"github.com/skyhook-io/skyhook-explorer/internal/server"
	"github.com/skyhook-io/skyhook-explorer/internal/static"
	"github.com/skyhook-io/skyhook-explorer/internal/timeline"
	"github.com/skyhook-io/skyhook-explorer/internal/traffic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

var (
	version = "dev"
)

func main() {
	// Parse flags
	kubeconfig := flag.String("kubeconfig", "", "Path to kubeconfig file (default: ~/.kube/config)")
	namespace := flag.String("namespace", "", "Initial namespace filter (empty = all namespaces)")
	port := flag.Int("port", 9280, "Server port")
	noBrowser := flag.Bool("no-browser", false, "Don't auto-open browser")
	devMode := flag.Bool("dev", false, "Development mode (serve frontend from filesystem)")
	showVersion := flag.Bool("version", false, "Show version and exit")
	historyLimit := flag.Int("history-limit", 10000, "Maximum number of events to retain in timeline")
	debugEvents := flag.Bool("debug-events", false, "Enable verbose event debugging (logs all event drops)")
	// Timeline storage options
	timelineStorage := flag.String("timeline-storage", "memory", "Timeline storage backend: memory or sqlite")
	timelineDBPath := flag.String("timeline-db", "", "Path to timeline database file (default: ~/.skyhook-explorer/timeline.db)")
	flag.Parse()

	// Set debug mode for event tracking
	k8s.DebugEvents = *debugEvents

	if *showVersion {
		fmt.Printf("skyhook-explorer %s\n", version)
		os.Exit(0)
	}

	// Suppress verbose client-go logs (reflector errors, traces, etc.)
	klog.InitFlags(nil)
	_ = flag.Set("v", "0")
	_ = flag.Set("logtostderr", "false")
	_ = flag.Set("alsologtostderr", "false")
	klog.SetOutput(os.Stderr)

	log.Printf("Skyhook Explorer %s starting...", version)

	// Initialize K8s client
	err := k8s.Initialize(k8s.InitOptions{
		KubeconfigPath: *kubeconfig,
	})
	if err != nil {
		log.Fatalf("Failed to initialize K8s client: %v", err)
	}

	if kubepath := k8s.GetKubeconfigPath(); kubepath != "" {
		log.Printf("Using kubeconfig: %s", kubepath)
	} else {
		log.Printf("Using in-cluster config")
	}

	// Preflight check: verify cluster connectivity before starting informers
	if err := checkClusterAccess(); err != nil {
		// Error already printed with helpful message
		os.Exit(1)
	}

	// Initialize timeline event store (unified storage for all events)
	timelineStoreCfg := timeline.StoreConfig{
		Type:    timeline.StoreTypeMemory,
		MaxSize: *historyLimit,
	}
	if *timelineStorage == "sqlite" {
		timelineStoreCfg.Type = timeline.StoreTypeSQLite
		dbPath := *timelineDBPath
		if dbPath == "" {
			homeDir, _ := os.UserHomeDir()
			dbPath = filepath.Join(homeDir, ".skyhook-explorer", "timeline.db")
		}
		timelineStoreCfg.Path = dbPath
	}
	if err := timeline.InitStore(timelineStoreCfg); err != nil {
		log.Fatalf("Failed to initialize timeline store: %v", err)
	}

	// Initialize resource cache (typed informers for core resources)
	if err := k8s.InitResourceCache(); err != nil {
		log.Fatalf("Failed to initialize resource cache: %v", err)
	}

	log.Printf("Resource cache initialized with %d resources", k8s.GetResourceCache().GetResourceCount())

	// Initialize resource discovery (for CRD support)
	if err := k8s.InitResourceDiscovery(); err != nil {
		log.Printf("Warning: Failed to initialize resource discovery: %v", err)
	}

	// Initialize dynamic resource cache (for CRDs)
	// Share the change channel with the typed cache so all changes go to SSE
	changeCh := k8s.GetResourceCache().ChangesRaw()
	if err := k8s.InitDynamicResourceCache(changeCh); err != nil {
		log.Printf("Warning: Failed to initialize dynamic resource cache: %v", err)
	}

	// Warm up dynamic cache for common CRDs so they appear in initial timeline
	k8s.WarmupCommonCRDs()

	// Initialize metrics history collection (polls metrics-server every 30s)
	k8s.InitMetricsHistory()

	// Initialize Helm client
	if err := helm.Initialize(k8s.GetKubeconfigPath()); err != nil {
		log.Printf("Warning: Failed to initialize Helm client: %v", err)
	}

	// Register Helm reset/reinit functions for context switching
	k8s.RegisterHelmFuncs(helm.ResetClient, helm.ReinitClient)

	// Register timeline store reset/reinit functions for context switching
	k8s.RegisterTimelineFuncs(timeline.ResetStore, func() error {
		return timeline.ReinitStore(timelineStoreCfg)
	})

	// Initialize traffic source manager with full config for port-forward support
	if err := traffic.InitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName()); err != nil {
		log.Printf("Warning: Failed to initialize traffic manager: %v", err)
	}

	// Register traffic reset/reinit functions for context switching
	k8s.RegisterTrafficFuncs(traffic.Reset, func() error {
		return traffic.ReinitializeWithConfig(k8s.GetClient(), k8s.GetConfig(), k8s.GetContextName())
	})

	// Create and start server
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

	// Open browser unless disabled
	if !*noBrowser {
		url := fmt.Sprintf("http://localhost:%d", *port)
		if *namespace != "" {
			url += fmt.Sprintf("?namespace=%s", *namespace)
		}
		go openBrowser(url)
	}

	// Start server (blocks)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
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
// Returns a user-friendly error if authentication or connection fails.
func checkClusterAccess() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientset := k8s.GetClient()
	if clientset == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}

	// Try to list namespaces as a basic connectivity check
	_, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil {
		return nil
	}

	errStr := err.Error()
	errLower := strings.ToLower(errStr)

	// Detect authentication/authorization errors
	if strings.Contains(errLower, "unauthorized") ||
		strings.Contains(errLower, "forbidden") ||
		strings.Contains(errLower, "authentication required") ||
		strings.Contains(errLower, "token has expired") ||
		strings.Contains(errLower, "credentials") ||
		strings.Contains(errLower, "exec plugin") ||
		strings.Contains(errLower, "gke-gcloud-auth-plugin") {

		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "✗ Cluster authentication failed")
		fmt.Fprintln(os.Stderr, "")

		// Detect cloud provider and give specific hints
		kubepath := k8s.GetKubeconfigPath()
		if strings.Contains(errLower, "gke") || strings.Contains(errLower, "gcloud") ||
			strings.Contains(kubepath, "gke") {
			fmt.Fprintln(os.Stderr, "  This looks like a GKE cluster. Try:")
			fmt.Fprintln(os.Stderr, "    gcloud container clusters get-credentials <cluster-name> --region <region>")
		} else if strings.Contains(errLower, "eks") || strings.Contains(kubepath, "eks") {
			fmt.Fprintln(os.Stderr, "  This looks like an EKS cluster. Try:")
			fmt.Fprintln(os.Stderr, "    aws eks update-kubeconfig --name <cluster-name> --region <region>")
		} else if strings.Contains(errLower, "aks") || strings.Contains(kubepath, "aks") {
			fmt.Fprintln(os.Stderr, "  This looks like an AKS cluster. Try:")
			fmt.Fprintln(os.Stderr, "    az aks get-credentials --name <cluster-name> --resource-group <rg>")
		} else {
			fmt.Fprintln(os.Stderr, "  Your cluster credentials may have expired or are invalid.")
			fmt.Fprintln(os.Stderr, "  Refresh your kubeconfig or re-authenticate to your cluster.")
		}
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "  Context: %s\n", getCurrentContext())
		fmt.Fprintln(os.Stderr, "")
		return fmt.Errorf("authentication failed")
	}

	// Detect connection errors
	if strings.Contains(errLower, "connection refused") ||
		strings.Contains(errLower, "no such host") ||
		strings.Contains(errLower, "i/o timeout") ||
		strings.Contains(errLower, "context deadline exceeded") ||
		strings.Contains(errLower, "dial tcp") ||
		strings.Contains(errLower, "tls handshake timeout") {

		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "✗ Cannot connect to Kubernetes cluster")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  Possible causes:")
		fmt.Fprintln(os.Stderr, "    • Cluster is not running or unreachable")
		fmt.Fprintln(os.Stderr, "    • VPN required but not connected")
		fmt.Fprintln(os.Stderr, "    • Firewall blocking the connection")
		fmt.Fprintln(os.Stderr, "    • kubeconfig points to wrong cluster")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "  Context: %s\n", getCurrentContext())
		if cluster := k8s.GetClusterName(); cluster != "" {
			fmt.Fprintf(os.Stderr, "  Cluster: %s\n", cluster)
		}
		fmt.Fprintln(os.Stderr, "")
		return fmt.Errorf("connection failed")
	}

	// Generic error
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "✗ Failed to access Kubernetes cluster")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "  Error: %s\n", truncateError(errStr, 200))
	fmt.Fprintln(os.Stderr, "")
	return fmt.Errorf("cluster access failed")
}

// getCurrentContext returns the current kubeconfig context name
func getCurrentContext() string {
	if ctx := k8s.GetContextName(); ctx != "" {
		return ctx
	}
	// Fallback to kubectl if k8s client doesn't have it
	cmd := exec.Command("kubectl", "config", "current-context")
	out, err := cmd.Output()
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(out))
}

// truncateError shortens an error message if it's too long
func truncateError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
