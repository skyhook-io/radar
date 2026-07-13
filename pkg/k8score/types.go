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
	Roles                    ResourceType = "roles"
	ClusterRoles             ResourceType = "clusterroles"
	RoleBindings             ResourceType = "rolebindings"
	ClusterRoleBindings      ResourceType = "clusterrolebindings"
	LimitRanges              ResourceType = "limitranges"
	ResourceQuotas           ResourceType = "resourcequotas"
)

// Operation constants for resource change events.
const (
	OpAdd    = "add"
	OpUpdate = "update"
	OpDelete = "delete"
)

// ResourceChange represents a resource change event from an informer callback.
type ResourceChange struct {
	Kind      string // "Service", "Deployment", "Pod", etc.
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

// ResourceScope describes the access scope for a single resource type.
// An empty Namespace means cluster-wide access; a non-empty Namespace means
// the informer is restricted to that namespace.
type ResourceScope struct {
	Enabled   bool   // false = no informer (denied or not requested)
	Namespace string // empty = cluster-wide
}

// CacheConfig holds configuration for creating a ResourceCache.
type CacheConfig struct {
	// Client is the Kubernetes clientset used to create informers.
	Client kubernetes.Interface

	// ResourceTypes lists the resource types to watch. Each key is a
	// ResourceType constant (e.g., Pods, Services). Only types present
	// in this map with a true value will have informers created.
	//
	// Combined with NamespaceScoped+Namespace this gives a single uniform
	// scope for every enabled informer. For mixed scopes (some kinds
	// cluster-wide, some namespace-scoped), use ResourceScopes instead.
	ResourceTypes map[string]bool

	// ResourceScopes optionally specifies per-kind enablement and scope.
	// When non-nil this takes precedence over ResourceTypes,
	// NamespaceScoped, and Namespace — each kind is enabled (or not) and
	// scoped (cluster-wide or to a single namespace) independently.
	//
	// This exists so callers that probe access per kind (e.g. to handle
	// users with cluster-wide permission for a few resources but only
	// namespaced permission for the rest) can wire each informer to the
	// scope it actually has, instead of falling back to all-or-nothing.
	ResourceScopes map[string]ResourceScope

	// ResourceScopeNamespaces optionally expands a namespace-scoped
	// ResourceScopes entry to multiple namespaces. It is keyed by the same
	// resource type strings as ResourceScopes. A kind with cluster-wide scope
	// ignores this field; a namespaced kind with multiple entries starts one
	// informer per namespace and exposes a union lister.
	ResourceScopeNamespaces map[string][]string

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

	// SyncTimeout is a hard cap on the critical-informer wait. When it
	// fires, unsynced critical informers are promoted to deferred and the
	// cache returns. Always applies, including when PatienceWindow is set
	// — in that case it acts as a backstop so a permanently-stuck informer
	// doesn't trap the caller forever. Zero means wait indefinitely.
	SyncTimeout time.Duration

	// PatienceWindow is the soft deadline after which the cache returns as
	// soon as MinimalSet (below) is fully synced. Critical informers not in
	// MinimalSet that are still syncing are promoted to deferred so the
	// caller can render a useful first paint while the rest stream in.
	//
	// When 0 or when MinimalSet is empty, this falls back to the legacy
	// behavior controlled by SyncTimeout.
	PatienceWindow time.Duration

	// MinimalSet is the subset of critical resource types (keyed by
	// ResourceType, e.g. Pods, Services) that the application considers
	// the irreducible minimum for a useful first render. Once these are
	// synced AND the patience window has elapsed, NewResourceCache
	// returns even if other critical informers are still syncing.
	//
	// Keys must reference types also present in ResourceTypes. Members
	// that are deferred or absent are ignored.
	MinimalSet map[string]bool

	// SyncProgress is invoked roughly every second during the critical
	// sync phase, and once more when first paint is ready. `synced` and
	// `total` count critical informers; `minimalReady` is true when the
	// minimal set is satisfied AND the patience window has elapsed (i.e.
	// the cache is about to return on the partial-paint path).
	// Application code uses this to drive a "loading X of Y" indicator.
	SyncProgress func(synced, total int, minimalReady bool)

	// DeferredSyncTimeout caps how long we wait for deferred informers to
	// finish syncing before giving up. When the deadline fires, deferredDone
	// is closed and deferredFailed is set so any still-unsynced types return
	// 403 from HTTP handlers instead of perpetual 503s. Synced types are
	// unaffected. Zero means wait indefinitely.
	DeferredSyncTimeout time.Duration

	// ListPageSize, when > 0, makes high-cardinality informers fetch their
	// initial LIST in pages of this size via a consistent (resourceVersion="")
	// read instead of one unpaginated response. This mitigates response-read
	// timeouts and memory spikes during initial sync on very large clusters
	// where WatchList streaming isn't available — when it is, client-go streams
	// the initial state and this path is never exercised. 0 (the default)
	// keeps the standard factory behavior. Only kinds flagged in
	// buildInformerSetups (currently Pods, ReplicaSets) paginate.
	ListPageSize int64

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

	// NamespaceFallback is used when informers should prefer cluster-wide
	// access but retry namespace-scoped after a cluster-wide 403/401.
	// Ignored when NamespaceScoped is true.
	NamespaceFallback string

	// DebugEvents enables verbose debug logging.
	DebugEvents bool
}
