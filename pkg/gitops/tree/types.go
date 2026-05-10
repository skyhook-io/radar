package tree

// ResourceRef identifies a Kubernetes resource in a GitOps tree.
type ResourceRef struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

// NodeRole describes how a resource participates in the tree.
type NodeRole string

const (
	RoleRoot      NodeRole = "root"
	RoleDeclared  NodeRole = "declared"
	RoleGenerated NodeRole = "generated"
	RoleGroup     NodeRole = "group"
)

// Tool is the GitOps controller family that owns the root.
type Tool string

const (
	ToolArgoCD Tool = "argocd"
	ToolFluxCD Tool = "fluxcd"
)

// Node is a renderable resource node in a GitOps ownership tree.
type Node struct {
	ID             string         `json:"id"`
	Ref            ResourceRef    `json:"ref"`
	Role           NodeRole       `json:"role"`
	Tool           Tool           `json:"tool"`
	Sync           string         `json:"sync,omitempty"`
	Health         string         `json:"health,omitempty"`
	TopologyStatus string         `json:"topologyStatus,omitempty"`
	Info           []InfoItem     `json:"info,omitempty"`
	Resource       any            `json:"resource,omitempty"`
	GroupedNodeIDs []string       `json:"groupedNodeIDs,omitempty"`
	Count          int            `json:"count,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
}

// InfoItem is a small key/value displayed on resource nodes.
type InfoItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// EdgeType labels the relationship represented by an Edge.
type EdgeType string

const (
	// EdgeOwns: the source declared (owns) the target.
	EdgeOwns EdgeType = "owns"
	// EdgeSource: the target was rendered from this Flux source repository.
	EdgeSource EdgeType = "source"
	// EdgeDependsOn: Flux dependsOn ordering — target waits for source.
	EdgeDependsOn EdgeType = "dependsOn"
)

// Edge is an ownership edge between tree nodes.
type Edge struct {
	Source string   `json:"source"`
	Target string   `json:"target"`
	Type   EdgeType `json:"type"`
}

// Summary contains high-level counts for the tree.
type Summary struct {
	Declared  int `json:"declared"`
	Generated int `json:"generated"`
	Grouped   int `json:"grouped"`
	Degraded  int `json:"degraded"`
	OutOfSync int `json:"outOfSync"`
}

// ResourceTree is the API response for a GitOps resource tree.
type ResourceTree struct {
	Root     Node     `json:"root"`
	Nodes    []Node   `json:"nodes"`
	Edges    []Edge   `json:"edges"`
	Warnings []string `json:"warnings,omitempty"`
	Summary  Summary  `json:"summary"`
}

type managedResource struct {
	Ref    ResourceRef
	Sync   string
	Health string
	Data   map[string]any
}

type relatedResource struct {
	Ref  ResourceRef
	Type EdgeType
	Data map[string]any
}
