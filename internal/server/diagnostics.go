package server

import (
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/version"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/perfstats"
)

// DiagConfig holds sanitized configuration for the diagnostics endpoint.
// No sensitive values (kubeconfig paths, Prometheus URLs, etc.).
type DiagConfig struct {
	Port                 int    `json:"port"`
	DevMode              bool   `json:"devMode"`
	Namespace            string `json:"namespace,omitempty"`
	TimelineStorage      string `json:"timelineStorage"`
	HistoryLimit         int    `json:"historyLimit"`
	DebugEvents          bool   `json:"debugEvents"`
	MCPEnabled           bool   `json:"mcpEnabled"`
	HasPrometheusURL     bool   `json:"hasPrometheusURL"`
	HasPrometheusHeaders bool   `json:"hasPrometheusHeaders"`
}

// DiagnosticsSnapshot is the top-level diagnostics response.
type DiagnosticsSnapshot struct {
	Timestamp    string `json:"timestamp"`
	RadarVersion string `json:"radarVersion"`
	GoVersion    string `json:"goVersion"`
	GOOS         string `json:"goos"`
	GOARCH       string `json:"goarch"`
	Uptime       string `json:"uptime"`
	UptimeSec    int64  `json:"uptimeSec"`

	Connection          *DiagConnection              `json:"connection,omitempty"`
	Kubeconfig          *DiagKubeconfig              `json:"kubeconfig,omitempty"`
	Cluster             *DiagCluster                 `json:"cluster,omitempty"`
	Cache               *DiagCache                   `json:"cache,omitempty"`
	Metrics             *k8s.MetricsCollectionHealth `json:"metrics,omitempty"`
	Timeline            *DiagTimeline                `json:"timeline,omitempty"`
	EventPipeline       *DiagEventPipeline           `json:"eventPipeline,omitempty"`
	Informers           *DiagInformers               `json:"informers,omitempty"`
	Prometheus          *DiagPrometheus              `json:"prometheus,omitempty"`
	Traffic             *DiagTraffic                 `json:"traffic,omitempty"`
	Permissions         *DiagPermissions             `json:"permissions,omitempty"`
	APIDiscovery        *DiagAPIDiscovery            `json:"apiDiscovery,omitempty"`
	SSE                 *DiagSSE                     `json:"sse,omitempty"`
	Perf                *perfstats.Snapshot          `json:"perf,omitempty"`
	Runtime             *DiagRuntime                 `json:"runtime,omitempty"`
	Config              *DiagConfig                  `json:"config,omitempty"`
	RecentErrors        []errorlog.ErrorEntry        `json:"recentErrors,omitempty"`
	TotalErrorsRecorded int64                        `json:"totalErrorsRecorded,omitempty"`
	Errors              []string                     `json:"errors,omitempty"`
}

// DiagConnection holds connection state info.
type DiagConnection struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	ClusterName string `json:"clusterName,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorType   string `json:"errorType,omitempty"`
}

// DiagKubeconfig holds non-sensitive kubeconfig loading state. It never
// includes resolved file paths — only counts, mode flags, and exec plugin
// command basenames suitable for inclusion in a public bug report. Helps
// triage issues like "some clusters don't show up in the switcher" or
// "can't switch clusters on the desktop app" (radar#411) — the answer
// typically lives in one of: kubeconfig loading mode, context merge
// collisions, shell env enrichment, or an exec auth plugin missing from
// the desktop app's PATH.
type DiagKubeconfig struct {
	Mode                   string   `json:"mode"`                         // in-cluster, single, multi-env, multi-dir, or "" if not initialized
	FileCount              int      `json:"fileCount"`                    // Number of kubeconfig files loaded
	ContextCount           int      `json:"contextCount"`                 // Contexts exposed after client-go merge
	EnrichedFromShell      bool     `json:"enrichedFromShell"`            // Desktop app captured KUBECONFIG from login shell
	CurrentContextUsesExec bool     `json:"currentContextUsesExec"`       // Current context's AuthInfo uses an exec credential plugin
	ExecPluginsPresent     []string `json:"execPluginsPresent,omitempty"` // Exec plugin command basenames resolvable on $PATH
	ExecPluginsMissing     []string `json:"execPluginsMissing,omitempty"` // Exec plugin command basenames NOT resolvable on $PATH (smoking gun for desktop-app multi-cluster failures)
}

// DiagCluster holds cluster detection info.
type DiagCluster struct {
	Platform          string `json:"platform"`
	KubernetesVersion string `json:"kubernetesVersion"`
	NodeCount         int    `json:"nodeCount"`
	NamespaceCount    int    `json:"namespaceCount"`
	InCluster         bool   `json:"inCluster"`
}

// DiagCache holds resource cache info.
type DiagCache struct {
	WatchedKinds   []string `json:"watchedKinds"`
	TotalResources int      `json:"totalResources"`
}

// DiagTimeline holds timeline store info.
type DiagTimeline struct {
	StorageType string `json:"storageType"`
	// Degraded reports that the configured persistent backend failed to open
	// and this session runs on a volatile in-memory fallback — the first thing
	// to check when history vanished after a restart. StorageType reflects the
	// ACTUAL store in use, not the configured one.
	Degraded       bool   `json:"degraded,omitempty"`
	DegradedReason string `json:"degradedReason,omitempty"`
	TotalEvents    int64  `json:"totalEvents"`
	OldestEvent  string `json:"oldestEvent,omitempty"`
	NewestEvent  string `json:"newestEvent,omitempty"`
	StorageBytes int64  `json:"storageBytes,omitempty"`
	StoreErrors  int64  `json:"storeErrors"`
	TotalDrops   int64  `json:"totalDrops"`

	// SQLite-only retention/cleanup state.
	RetentionAge       string `json:"retentionAge,omitempty"`
	MaxStorageBytes    int64  `json:"maxStorageBytes,omitempty"`
	LastCleanupAt      string `json:"lastCleanupAt,omitempty"`
	LastCleanupDeleted int64  `json:"lastCleanupDeletedRows,omitempty"`
	LastCleanupError   string `json:"lastCleanupError,omitempty"`
}

// DiagEventPipeline holds event pipeline metrics.
type DiagEventPipeline struct {
	Received    map[string]int64      `json:"received"`
	Dropped     map[string]int64      `json:"dropped"`
	Recorded    map[string]int64      `json:"recorded"`
	RecentDrops []timeline.DropRecord `json:"recentDrops"`
	Uptime      string                `json:"uptime"`
}

// DiagInformers holds informer counts and sync status.
type DiagInformers struct {
	TypedCount   int                      `json:"typedCount"`
	DynamicCount int                      `json:"dynamicCount"`
	WatchedCRDs  []string                 `json:"watchedCRDs"`
	SyncStatus   *k8score.CacheSyncStatus `json:"syncStatus,omitempty"`
}

// DiagPrometheus holds Prometheus connection info.
type DiagPrometheus struct {
	Connected        bool   `json:"connected"`
	Address          string `json:"address,omitempty"`
	ServiceName      string `json:"serviceName,omitempty"`
	ServiceNamespace string `json:"serviceNamespace,omitempty"`
}

// DiagTraffic holds traffic source info.
type DiagTraffic struct {
	ActiveSource string   `json:"activeSource"`
	Detected     []string `json:"detected"`
	NotDetected  []string `json:"notDetected"`
}

// DiagSSE holds SSE broadcaster info.
type DiagSSE struct {
	ConnectedClients int `json:"connectedClients"`
}

// DiagPermissions holds RBAC permission info (read-only from cache).
type DiagPermissions struct {
	Exec            bool     `json:"exec"`
	Logs            bool     `json:"logs"`
	PortForward     bool     `json:"portForward"`
	Secrets         bool     `json:"secrets"`
	HelmWrite       bool     `json:"helmWrite"`
	NamespaceScoped bool     `json:"namespaceScoped"`
	Namespace       string   `json:"namespace,omitempty"`
	Restricted      []string `json:"restricted,omitempty"`
}

// DiagAPIDiscovery holds API resource discovery info.
type DiagAPIDiscovery struct {
	TotalResources int    `json:"totalResources"`
	CRDCount       int    `json:"crdCount"`
	LastRefresh    string `json:"lastRefresh,omitempty"`
}

// DiagRuntime holds Go runtime info.
type DiagRuntime struct {
	HeapMB       float64 `json:"heapMB"`
	HeapObjectsK float64 `json:"heapObjectsK"`
	Goroutines   int     `json:"goroutines"`
	NumCPU       int     `json:"numCPU"`
}

// collectSafe runs fn, recovering from panics. On error/panic, appends to errs.
func collectSafe(name string, errs *[]string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("%s: panic: %v", name, r)
			log.Printf("[diagnostics] %s", msg)
			*errs = append(*errs, msg)
		}
	}()
	fn()
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	uptime := now.Sub(s.startTime)

	snap := DiagnosticsSnapshot{
		Timestamp:    now.Format(time.RFC3339),
		RadarVersion: version.Current,
		GoVersion:    runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		Uptime:       uptime.Truncate(time.Second).String(),
		UptimeSec:    int64(uptime.Seconds()),
	}

	var errs []string

	// Connection — always available, no cluster needed
	collectSafe("connection", &errs, func() {
		status := k8s.GetConnectionStatus()
		snap.Connection = &DiagConnection{
			State:       string(status.State),
			Context:     status.Context,
			ClusterName: status.ClusterName,
			Error:       status.Error,
			ErrorType:   status.ErrorType,
		}
	})

	// Kubeconfig loading state — non-sensitive, always safe to include
	collectSafe("kubeconfig", &errs, func() {
		summary := k8s.GetKubeconfigSummary()
		snap.Kubeconfig = &DiagKubeconfig{
			Mode:                   summary.Mode,
			FileCount:              summary.FileCount,
			ContextCount:           summary.ContextCount,
			EnrichedFromShell:      summary.EnrichedFromShell,
			CurrentContextUsesExec: summary.CurrentContextUsesExec,
			ExecPluginsPresent:     summary.ExecPluginsPresent,
			ExecPluginsMissing:     summary.ExecPluginsMissing,
		}
	})

	// Cluster info — requires connection, but errors are captured
	collectSafe("cluster", &errs, func() {
		info, err := k8s.GetClusterInfo(r.Context())
		if err != nil {
			errs = append(errs, fmt.Sprintf("cluster: %v", err))
			return
		}
		snap.Cluster = &DiagCluster{
			Platform:          info.Platform,
			KubernetesVersion: info.KubernetesVersion,
			NodeCount:         info.NodeCount,
			NamespaceCount:    info.NamespaceCount,
			InCluster:         info.InCluster,
		}
	})

	// Cache
	collectSafe("cache", &errs, func() {
		cache := k8s.GetResourceCache()
		if cache == nil {
			return
		}
		enabled := cache.GetEnabledResources()
		kinds := make([]string, 0, len(enabled))
		for kind, ok := range enabled {
			if ok {
				kinds = append(kinds, kind)
			}
		}
		sort.Strings(kinds)
		snap.Cache = &DiagCache{
			WatchedKinds:   kinds,
			TotalResources: cache.GetResourceCount(),
		}
	})

	// Metrics health
	collectSafe("metrics", &errs, func() {
		store := k8s.GetMetricsHistory()
		if store == nil {
			return
		}
		h := store.CollectionHealth()
		snap.Metrics = &h
	})

	// Timeline
	collectSafe("timeline", &errs, func() {
		store := timeline.GetStore()
		if store == nil {
			return
		}
		stats := store.Stats()
		diag := &DiagTimeline{
			TotalEvents:        stats.TotalEvents,
			StorageBytes:       stats.StorageBytes,
			StoreErrors:        timeline.GetStoreErrorCount(),
			TotalDrops:         timeline.GetTotalDropCount(),
			LastCleanupDeleted: stats.LastCleanupDeletedRows,
			LastCleanupError:   stats.LastCleanupError,
		}
		if !stats.OldestEvent.IsZero() {
			diag.OldestEvent = stats.OldestEvent.Format(time.RFC3339)
		}
		if !stats.NewestEvent.IsZero() {
			diag.NewestEvent = stats.NewestEvent.Format(time.RFC3339)
		}
		if !stats.LastCleanupAt.IsZero() {
			diag.LastCleanupAt = stats.LastCleanupAt.Format(time.RFC3339)
		}
		if stats.RetentionAge > 0 {
			diag.RetentionAge = stats.RetentionAge.String()
		}
		diag.MaxStorageBytes = stats.MaxStorageBytes
		if s.diagConfig != nil {
			diag.StorageType = s.diagConfig.TimelineStorage
		}
		if stats.Degraded {
			diag.StorageType = "memory"
			diag.Degraded = true
			diag.DegradedReason = stats.DegradedReason
		}
		snap.Timeline = diag
	})

	// Event pipeline
	collectSafe("eventPipeline", &errs, func() {
		metrics := timeline.GetMetrics()
		if metrics == nil {
			return
		}
		snapshot := metrics.GetSnapshot()
		snap.EventPipeline = &DiagEventPipeline{
			Received:    snapshot.Counters.Received,
			Dropped:     snapshot.Counters.Dropped,
			Recorded:    snapshot.Counters.Recorded,
			RecentDrops: snapshot.RecentDrops,
			Uptime:      snapshot.Uptime,
		}
	})

	// Informers
	collectSafe("informers", &errs, func() {
		diag := &DiagInformers{}

		// Get typed informer count and sync status from cache
		cache := k8s.GetResourceCache()
		if cache != nil {
			enabled := cache.GetEnabledResources()
			count := 0
			for _, ok := range enabled {
				if ok {
					count++
				}
			}
			diag.TypedCount = count
			syncStatus := cache.GetSyncStatus()
			diag.SyncStatus = &syncStatus
		}

		dynCache := k8s.GetDynamicResourceCache()
		if dynCache != nil {
			diag.DynamicCount = dynCache.GetInformerCount()
			watched := dynCache.GetWatchedResources()
			crds := make([]string, 0, len(watched))
			for _, gvr := range watched {
				crds = append(crds, fmt.Sprintf("%s.%s", gvr.Resource, gvr.Group))
			}
			sort.Strings(crds)
			diag.WatchedCRDs = crds
		}
		snap.Informers = diag
	})

	// Prometheus
	collectSafe("prometheus", &errs, func() {
		client := prometheuspkg.GetClient()
		if client == nil {
			return
		}
		status := client.GetStatus()
		diag := &DiagPrometheus{
			Connected: status.Connected,
			Address:   status.Address,
		}
		if status.Service != nil {
			diag.ServiceName = status.Service.Name
			diag.ServiceNamespace = status.Service.Namespace
		}
		snap.Prometheus = diag
	})

	// Traffic — only read cached state, never trigger network I/O
	collectSafe("traffic", &errs, func() {
		manager := traffic.GetManager()
		if manager == nil {
			return
		}
		snap.Traffic = &DiagTraffic{
			ActiveSource: manager.GetActiveSourceName(),
		}
	})

	// Permissions — read-only from cache, no RBAC checks
	collectSafe("permissions", &errs, func() {
		caps := k8s.GetCachedCapabilities()
		permResult := k8s.GetCachedPermissionResult()
		if caps == nil && permResult == nil {
			return
		}
		diag := &DiagPermissions{}
		if caps != nil {
			diag.Exec = caps.Exec
			diag.Logs = caps.Logs
			diag.PortForward = caps.PortForward
			diag.Secrets = caps.Secrets
			diag.HelmWrite = caps.HelmWrite
		}
		if permResult != nil {
			diag.NamespaceScoped = permResult.NamespaceScoped
			diag.Namespace = permResult.Namespace
			if permResult.Perms != nil {
				// Collect restricted resources
				type permEntry struct {
					name    string
					allowed bool
				}
				entries := []permEntry{
					{"pods", permResult.Perms.Pods},
					{"services", permResult.Perms.Services},
					{"deployments", permResult.Perms.Deployments},
					{"daemonSets", permResult.Perms.DaemonSets},
					{"statefulSets", permResult.Perms.StatefulSets},
					{"replicaSets", permResult.Perms.ReplicaSets},
					{"ingresses", permResult.Perms.Ingresses},
					{"configMaps", permResult.Perms.ConfigMaps},
					{"secrets", permResult.Perms.Secrets},
					{"events", permResult.Perms.Events},
					{"nodes", permResult.Perms.Nodes},
					{"jobs", permResult.Perms.Jobs},
					{"cronJobs", permResult.Perms.CronJobs},
				}
				var restricted []string
				for _, e := range entries {
					if !e.allowed {
						restricted = append(restricted, e.name)
					}
				}
				if len(restricted) > 0 {
					diag.Restricted = restricted
				}
			}
		}
		snap.Permissions = diag
	})

	// API Discovery — read-only stats, no refresh
	collectSafe("apiDiscovery", &errs, func() {
		discovery := k8s.GetResourceDiscovery()
		if discovery == nil {
			return
		}
		stats := discovery.Stats()
		diag := &DiagAPIDiscovery{
			TotalResources: stats.TotalResources,
			CRDCount:       stats.CRDCount,
		}
		if !stats.LastRefresh.IsZero() {
			diag.LastRefresh = stats.LastRefresh.Format(time.RFC3339)
		}
		snap.APIDiscovery = diag
	})

	// SSE
	collectSafe("sse", &errs, func() {
		snap.SSE = &DiagSSE{
			ConnectedClients: s.broadcaster.ClientCount(),
		}
	})

	// Perf — always-on counters + sample windows for topology build/SSE
	// behavior at scale. Cheap to collect (atomic loads + copy of a
	// 100-entry ring buffer per metric).
	collectSafe("perf", &errs, func() {
		perf := perfstats.GetSnapshot()
		snap.Perf = &perf
	})

	// Runtime
	collectSafe("runtime", &errs, func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		snap.Runtime = &DiagRuntime{
			HeapMB:       float64(m.HeapAlloc) / 1024 / 1024,
			HeapObjectsK: float64(m.HeapObjects) / 1000,
			Goroutines:   runtime.NumGoroutine(),
			NumCPU:       runtime.NumCPU(),
		}
	})

	// Config
	collectSafe("config", &errs, func() {
		if s.diagConfig != nil {
			snap.Config = s.diagConfig
		}
	})

	// Error log
	collectSafe("errorLog", &errs, func() {
		entries := errorlog.GetEntries()
		if len(entries) > 0 {
			snap.RecentErrors = entries
		}
		if total := errorlog.TotalRecorded(); total > 0 {
			snap.TotalErrorsRecorded = total
		}
	})

	if len(errs) > 0 {
		snap.Errors = errs
	}

	s.writeJSON(w, snap)
}
