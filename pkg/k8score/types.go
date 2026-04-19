// Package k8score provides a shared Kubernetes resource caching layer built on
// SharedInformers. It is designed to be imported by both Radar (the Explorer
// desktop app) and skyhook-connector (the in-cluster agent), extracting the
// common informer setup, transform, lister, and change-notification logic.
//
// This package has NO imports of any internal/ package. Application-specific
// behavior (timeline recording, noisy-resource filtering, diff computation)
// is injected via callbacks in CacheConfig.
package k8score

import (
	"log"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// ResourceType identifies a Kubernetes resource type tracked by the cache.
type ResourceType = string

const (
	Pods                     ResourceType = "pods"
	Services                 ResourceType = "services"
	Nodes                    ResourceType = "nodes"
	Namespaces               ResourceType = "namespaces"
	ConfigMaps               ResourceType = "configmaps"
	Secrets                  ResourceType = "secrets"
	Events                   ResourceType = "events"
	PersistentVolumeClaims   ResourceType = "persistentvolumeclaims"
	PersistentVolumes        ResourceType = "persistentvolumes"
	Deployments              ResourceType = "deployments"
	DaemonSets               ResourceType = "daemonsets"
	StatefulSets             ResourceType = "statefulsets"
	ReplicaSets              ResourceType = "replicasets"
	Ingresses                ResourceType = "ingresses"
	IngressClasses           ResourceType = "ingressclasses"
	Jobs                     ResourceType = "jobs"
	CronJobs                 ResourceType = "cronjobs"
	HorizontalPodAutoscalers ResourceType = "horizontalpodautoscalers"
	StorageClasses           ResourceType = "storageclasses"
	PodDisruptionBudgets     ResourceType = "poddisruptionbudgets"
	NetworkPolicies          ResourceType = "networkpolicies"
	ServiceAccounts          ResourceType = "serviceaccounts"
	LimitRanges              ResourceType = "limitranges"
)

// Operation constants for resource change events.
const (
	OpAdd    = "add"
	OpUpdate = "update"
	OpDelete = "delete"
)

// ResourceChange represents a resource change event from an informer callback.
type ResourceChange struct {
	Kind      string    // "Service", "Deployment", "Pod", etc.
	Namespace string
	Name      string
	UID       string
	Operation string    // "add", "update", "delete"
	Diff      *DiffInfo // Diff details for updates (optional)
}

// DiffInfo contains the diff details for an update operation.
type DiffInfo struct {
	Fields  []FieldChange `json:"fields"`
	Summary string        `json:"summary"`
}

// FieldChange represents a single field that changed.
type FieldChange struct {
	Path     string `json:"path"`
	OldValue any    `json:"oldValue"`
	NewValue any    `json:"newValue"`
}

// OwnerInfo represents the owner/controller of a resource.
type OwnerInfo struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// CacheConfig holds configuration for creating a ResourceCache.
type CacheConfig struct {
	// Client is the Kubernetes clientset used to create informers.
	Client kubernetes.Interface

	// ResourceTypes lists the resource types to watch. Each key is a
	// ResourceType constant (e.g., Pods, Services). Only types present
	// in this map with a true value will have informers created.
	ResourceTypes map[string]bool

	// DeferredTypes lists resource types whose informers sync in the
	// background after critical informers complete. Their listers return
	// nil until sync finishes. If nil, no resources are deferred.
	DeferredTypes map[string]bool

	// OnReceived is called for every non-Event resource change before any
	// filtering (noisy checks, suppress-initial-adds). Used for metrics
	// tracking (e.g., timeline.IncrementReceived). May be nil.
	OnReceived func(kind string)

	// OnChange is called for each non-Event resource change after the
	// change is sent to the changes channel. It receives the change plus
	// the raw new and old objects for application-specific processing
	// (e.g., timeline recording). May be nil.
	OnChange func(change ResourceChange, obj, oldObj any)

	// OnEventChange is called for K8s Event resource changes. Events use
	// a separate handler path (no noisy filtering, no diff computation).
	// May be nil.
	OnEventChange func(obj any, op string)

	// OnDrop is called when a change is dropped (channel full, noisy filter, etc.).
	// Parameters: kind, namespace, name, reason, operation. May be nil.
	OnDrop func(kind, ns, name, reason, op string)

	// ComputeDiff is called for update operations to compute a diff between
	// old and new objects. Returns nil if no meaningful changes. May be nil.
	ComputeDiff func(kind string, oldObj, newObj any) *DiffInfo

	// IsNoisyResource returns true if this resource change should skip both the
	// OnChange callback and the changes channel. Noisy resources are silently
	// dropped to reduce pressure on the event pipeline.
	// May be nil (nothing is treated as noisy).
	IsNoisyResource func(kind, name, op string) bool

	// ChannelSize is the buffer size for the changes channel. Defaults to 10000.
	ChannelSize int

	// NamespaceScoped restricts informers to a single namespace.
	// When true, Namespace must be set.
	NamespaceScoped bool
	Namespace       string

	// SuppressInitialAdds, when true, suppresses all "add" operations
	// during the initial sync phase. The OnChange callback will not be
	// called for adds until IsSyncComplete() returns true.
	// In Radar mode this is false (Radar's callback decides per-add).
	// In connector mode this is true.
	SuppressInitialAdds bool

	// SyncTimeout is the maximum time to wait for critical informers to sync
	// before proceeding with partial data. Unsynced critical informers are
	// promoted to deferred and continue syncing in the background.
	// Zero means wait indefinitely (original behavior).
	SyncTimeout time.Duration

	// DeferredSyncTimeout caps how long we wait for deferred informers to
	// finish syncing before giving up. When the deadline fires, deferredDone
	// is closed and deferredFailed is set so any still-unsynced types return
	// 403 from HTTP handlers instead of perpetual 503s. Synced types are
	// unaffected. Zero means wait indefinitely.
	DeferredSyncTimeout time.Duration

	// DebugEvents enables verbose event debug logging.
	DebugEvents bool

	// Logger is used for log output. If nil, the standard logger is used.
	Logger *log.Logger

	// TimingLogger is called to emit startup timing lines. May be nil.
	TimingLogger func(format string, args ...any)
}

// ---------------------------------------------------------------------------
// Dynamic (CRD / unstructured) cache types
// ---------------------------------------------------------------------------

// CRDDiscoveryStatus represents the state of CRD discovery.
type CRDDiscoveryStatus string

const (
	CRDDiscoveryIdle       CRDDiscoveryStatus = "idle"        // Not started
	CRDDiscoveryInProgress CRDDiscoveryStatus = "discovering" // Discovery in progress
	CRDDiscoveryComplete   CRDDiscoveryStatus = "ready"       // Discovery complete
)

// DynamicCacheConfig holds configuration for creating a DynamicResourceCache.
// All application-specific behavior is injected via callbacks — the cache
// itself has no imports of any internal/ package.
type DynamicCacheConfig struct {
	// DynamicClient is the dynamic Kubernetes client used to create informers.
	DynamicClient dynamic.Interface

	// Discovery is used for GVR→Kind resolution and watch-verb checks.
	// May be nil; when nil the cache falls back to heuristic kind names.
	Discovery *ResourceDiscovery

	// Changes is the shared channel for resource change notifications.
	// Pass ResourceCache.ChangesRaw() here so typed and dynamic resource changes
	// are delivered on the same channel (unified fan-in). May be nil if change
	// events are not needed.
	Changes chan ResourceChange

	// OnReceived is called for every dynamic resource change before processing.
	// May be nil.
	OnReceived func(kind string)

	// OnChange is called for each change after it is recorded. It receives
	// the ResourceChange plus the raw new and old objects. May be nil.
	OnChange func(change ResourceChange, obj, oldObj any)

	// OnDrop is called when a change cannot be sent to the channel.
	// Parameters: kind, namespace, name, reason, operation. May be nil.
	OnDrop func(kind, ns, name, reason, op string)

	// OnRecorded is called after a change is successfully sent to the channel.
	// May be nil.
	OnRecorded func(kind string)

	// ComputeDiff is called for update operations to diff old/new objects.
	// May be nil.
	ComputeDiff func(kind string, oldObj, newObj any) *DiffInfo

	// NamespaceScoped restricts informers to a single namespace.
	NamespaceScoped bool
	Namespace       string

	// DebugEvents enables verbose debug logging.
	DebugEvents bool
}
