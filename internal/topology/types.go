package topology

// NodeKind represents the type of a topology node
//
// When adding a new NodeKind constant, also update:
// - builder.go: node creation + edge creation (both resources and traffic views)
// - builder.go: genericCRDExclusion check (if kind is handled via dynamic cache)
// - relationships.go: buildNodeID + normalizeKind maps, EdgeRoutesTo dispatch
// - history.go: diff dispatch switch
// - dashboard.go: resource counting (if applicable)
// - capabilities.go: ResourcePermissions struct + permCheck array (if needs RBAC)
// - dynamic_cache.go: warmup list (if CRD)
type NodeKind string

const (
	KindInternet      NodeKind = "Internet"
	KindIngress       NodeKind = "Ingress"
	KindGateway       NodeKind = "Gateway"
	KindHTTPRoute     NodeKind = "HTTPRoute"
	KindGRPCRoute     NodeKind = "GRPCRoute"
	KindTCPRoute      NodeKind = "TCPRoute"
	KindTLSRoute      NodeKind = "TLSRoute"
	KindService       NodeKind = "Service"
	KindDeployment    NodeKind = "Deployment"
	KindRollout       NodeKind = "Rollout"
	KindApplication   NodeKind = "Application"   // ArgoCD Application
	KindKustomization NodeKind = "Kustomization" // FluxCD Kustomization
	KindHelmRelease   NodeKind = "HelmRelease"   // FluxCD HelmRelease (Flux, not native Helm)
	KindGitRepository NodeKind = "GitRepository" // FluxCD GitRepository
	KindDaemonSet     NodeKind = "DaemonSet"
	KindStatefulSet   NodeKind = "StatefulSet"
	KindReplicaSet    NodeKind = "ReplicaSet"
	KindPod           NodeKind = "Pod"
	KindPodGroup      NodeKind = "PodGroup"
	KindConfigMap     NodeKind = "ConfigMap"
	KindSecret        NodeKind = "Secret"
	KindHPA           NodeKind = "HorizontalPodAutoscaler"
	KindJob           NodeKind = "Job"
	KindCronJob       NodeKind = "CronJob"
	KindPVC           NodeKind = "PersistentVolumeClaim"
	KindNamespace     NodeKind = "Namespace"
)

// HealthStatus represents the health status of a node
type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"
	StatusDegraded  HealthStatus = "degraded"
	StatusUnhealthy HealthStatus = "unhealthy"
	StatusUnknown   HealthStatus = "unknown"
)

// EdgeType represents the type of connection between nodes
type EdgeType string

const (
	EdgeRoutesTo   EdgeType = "routes-to"
	EdgeExposes    EdgeType = "exposes"
	EdgeManages    EdgeType = "manages"
	EdgeUses       EdgeType = "uses"
	EdgeConfigures EdgeType = "configures"
)

// Node represents a node in the topology graph
type Node struct {
	ID     string         `json:"id"`
	Kind   NodeKind       `json:"kind"`
	Name   string         `json:"name"`
	Status HealthStatus   `json:"status"`
	Data   map[string]any `json:"data"`
}

// Edge represents a connection between two nodes
type Edge struct {
	ID                string   `json:"id"`
	Source            string   `json:"source"`
	Target            string   `json:"target"`
	Type              EdgeType `json:"type"`
	Label             string   `json:"label,omitempty"`
	SkipIfKindVisible string   `json:"skipIfKindVisible,omitempty"` // Hide this edge if this kind is visible (for shortcut edges)
}

// Topology represents the complete graph
type Topology struct {
	Nodes              []Node   `json:"nodes"`
	Edges              []Edge   `json:"edges"`
	Warnings           []string `json:"warnings,omitempty"`           // Warnings about resources that failed to load
	Truncated          bool     `json:"truncated,omitempty"`          // True if topology was truncated due to size limit
	TotalNodes         int      `json:"totalNodes,omitempty"`         // Total nodes before truncation (only set if truncated)
	LargeCluster       bool     `json:"largeCluster,omitempty"`       // True if cluster exceeds large cluster threshold
	HiddenKinds        []string `json:"hiddenKinds,omitempty"`        // Resource kinds auto-hidden for performance
	CRDDiscoveryStatus string   `json:"crdDiscoveryStatus,omitempty"` // CRD discovery status: idle, discovering, ready
}

// ViewMode determines how the topology is built
type ViewMode string

const (
	ViewModeTraffic   ViewMode = "traffic"   // Network-focused (Ingress/Gateway -> Service -> Pod)
	ViewModeResources ViewMode = "resources" // Comprehensive tree
)

// Large cluster threshold - when pre-grouped node count exceeds this, apply optimizations
const LargeClusterThreshold = 1000

// BuildOptions configures topology building
type BuildOptions struct {
	Namespaces         []string // Filter to specific namespaces (empty = all)
	ViewMode           ViewMode // How to display topology
	MaxIndividualPods  int      // Above this, pods are grouped (default: 5)
	MaxNodes           int      // Maximum nodes to return (0 = unlimited, default: 500)
	IncludeSecrets     bool     // Include Secret nodes
	IncludeConfigMaps  bool     // Include ConfigMap nodes
	IncludePVCs        bool     // Include PersistentVolumeClaim nodes
	IncludeReplicaSets bool     // Include ReplicaSet nodes (noisy intermediate objects)
	IncludeGenericCRDs bool     // Include CRDs with owner refs to topology nodes (default: true)
}

// MatchesNamespace returns true if ns is in the allowed list, or if the list is empty (all allowed).
// This is a standalone function that can be used by any code needing namespace filtering.
func MatchesNamespace(namespaces []string, ns string) bool {
	if len(namespaces) == 0 {
		return true
	}
	for _, allowed := range namespaces {
		if allowed == ns {
			return true
		}
	}
	return false
}

// MatchesNamespaceFilter returns true if the given namespace matches the filter.
// An empty filter means all namespaces match.
func (opts BuildOptions) MatchesNamespaceFilter(ns string) bool {
	return MatchesNamespace(opts.Namespaces, ns)
}

// NamespaceFilter returns the namespace to use for API queries.
// If exactly one namespace is filtered, return it (for efficient API filtering).
// Otherwise return empty string (query all, filter client-side).
func (opts BuildOptions) NamespaceFilter() string {
	if len(opts.Namespaces) == 1 {
		return opts.Namespaces[0]
	}
	return ""
}

// DefaultBuildOptions returns sensible defaults
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{
		Namespaces:         nil, // Empty = all namespaces
		ViewMode:           ViewModeResources,
		MaxIndividualPods:  5,
		MaxNodes:           2000, // Limit to prevent browser crashes on large clusters
		IncludeSecrets:     false, // Secrets are sensitive
		IncludeConfigMaps:  true,
		IncludePVCs:        true,
		IncludeReplicaSets: false, // Hidden by default - noisy intermediate between Deployment and Pod
		IncludeGenericCRDs: true,  // Show CRDs with owner refs to topology nodes
	}
}

// ResourceRef is a reference to a related K8s resource
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Group     string `json:"group,omitempty"` // API group for CRDs (e.g., "cert-manager.io")
}

// Relationships holds computed relationships for a resource
type Relationships struct {
	Owner       *ResourceRef  `json:"owner,omitempty"`       // Parent via ownerReference (manages edge)
	Children    []ResourceRef `json:"children,omitempty"`    // Resources this owns (manages edge)
	Services    []ResourceRef `json:"services,omitempty"`    // Services selecting/exposing this
	Ingresses   []ResourceRef `json:"ingresses,omitempty"`   // Ingresses routing to this
	Gateways    []ResourceRef `json:"gateways,omitempty"`    // Gateways routing to this (via routes)
	Routes      []ResourceRef `json:"routes,omitempty"`      // Routes attached to this Gateway
	ConfigRefs  []ResourceRef `json:"configRefs,omitempty"`  // ConfigMaps/Secrets used by this
	HPA         *ResourceRef  `json:"hpa,omitempty"`         // HPA scaling this
	ScaleTarget *ResourceRef  `json:"scaleTarget,omitempty"` // For HPA: what it scales
	Pods        []ResourceRef `json:"pods,omitempty"`        // For Service: pods it routes to
}

// ResourceWithRelationships wraps a K8s resource with computed relationships
type ResourceWithRelationships struct {
	Resource      any            `json:"resource"`
	Relationships *Relationships `json:"relationships,omitempty"`
}
