package topology

// NodeKind represents the type of a topology node
type NodeKind string

const (
	KindInternet      NodeKind = "Internet"
	KindIngress       NodeKind = "Ingress"
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
	KindHPA           NodeKind = "HPA"
	KindJob           NodeKind = "Job"
	KindCronJob       NodeKind = "CronJob"
	KindPVC           NodeKind = "PVC"
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
	Nodes    []Node   `json:"nodes"`
	Edges    []Edge   `json:"edges"`
	Warnings []string `json:"warnings,omitempty"` // Warnings about resources that failed to load
}

// ViewMode determines how the topology is built
type ViewMode string

const (
	ViewModeTraffic   ViewMode = "traffic"   // Network-focused (Ingress -> Service -> Pod)
	ViewModeResources ViewMode = "resources" // Comprehensive tree
)

// BuildOptions configures topology building
type BuildOptions struct {
	Namespace          string   // Filter to specific namespace (empty = all)
	ViewMode           ViewMode // How to display topology
	MaxIndividualPods  int      // Above this, pods are grouped (default: 5)
	IncludeSecrets     bool     // Include Secret nodes
	IncludeConfigMaps  bool     // Include ConfigMap nodes
	IncludePVCs        bool     // Include PersistentVolumeClaim nodes
	IncludeReplicaSets bool     // Include ReplicaSet nodes (noisy intermediate objects)
}

// DefaultBuildOptions returns sensible defaults
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{
		Namespace:          "",
		ViewMode:           ViewModeResources,
		MaxIndividualPods:  5,
		IncludeSecrets:     false, // Secrets are sensitive
		IncludeConfigMaps:  true,
		IncludePVCs:        true,
		IncludeReplicaSets: false, // Hidden by default - noisy intermediate between Deployment and Pod
	}
}

// ResourceRef is a reference to a related K8s resource
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// Relationships holds computed relationships for a resource
type Relationships struct {
	Owner       *ResourceRef  `json:"owner,omitempty"`       // Parent via ownerReference (manages edge)
	Children    []ResourceRef `json:"children,omitempty"`    // Resources this owns (manages edge)
	Services    []ResourceRef `json:"services,omitempty"`    // Services selecting/exposing this
	Ingresses   []ResourceRef `json:"ingresses,omitempty"`   // Ingresses routing to this
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
