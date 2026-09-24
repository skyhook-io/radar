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
	ID     string      `json:"id"`
	Ref    ResourceRef `json:"ref"`
	Role   NodeRole    `json:"role"`
	Tool   Tool        `json:"tool"`
	Sync   string      `json:"sync,omitempty"`
	Health string      `json:"health,omitempty"`
	// HealthSource says who produced Health. The GitOps controller's own
	// verdict (HealthSourceController) and Radar's own read of the live
	// object (HealthSourceRadar) are different claims and the UI labels the
	// second one; consumers must not present a Radar-derived value as the
	// controller's. Empty when Health is empty.
	HealthSource   HealthSource   `json:"healthSource,omitempty"`
	HealthReason   string         `json:"healthReason,omitempty"`
	HealthMessage  string         `json:"healthMessage,omitempty"`
	HealthSeverity string         `json:"healthSeverity,omitempty"`
	TopologyStatus string         `json:"topologyStatus,omitempty"`
	Info           []InfoItem     `json:"info,omitempty"`
	Resource       any            `json:"resource,omitempty"`
	GroupedNodeIDs []string       `json:"groupedNodeIDs,omitempty"`
	Count          int            `json:"count,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	// Remote marks a resource that lives on the destination cluster of a
	// remote tree. Radar's API reads this cluster, so a same-named object
	// here is a different resource, not this node.
	Remote bool `json:"remote,omitempty"`
}

// HealthSource identifies who assessed a node's Health.
type HealthSource string

const (
	// HealthSourceController: the GitOps controller wrote this health into
	// its own CR (Argo status.resources[].health, Flux conditions).
	HealthSourceController HealthSource = "controller"
	// HealthSourceRadar: Radar derived it from the live object — the topology
	// builder's status for kinds it models, or the issues engine's
	// classification overlaid by the host. Never a "Healthy" verdict from the
	// issues engine: that path only ever reports problems.
	HealthSourceRadar HealthSource = "radar"
	// HealthSourceControllerAPI: the controller's own verdict, read from its
	// API server (argocd-server) because the CR doesn't carry it. Same
	// authority as HealthSourceController; a distinct value so the UI can say
	// where it came from.
	HealthSourceControllerAPI HealthSource = "controllerApi"
)

// HealthMode says where an Argo CD Application keeps per-resource health.
type HealthMode string

const (
	// HealthModeInline: Argo persisted per-resource health into
	// status.resources[] (the pre-3.0 default, or
	// controller.resource.health.persist=true).
	HealthModeInline HealthMode = "inline"
	// HealthModeAppTree: Argo keeps per-resource health outside the CR
	// (status.resourceHealthSource=appTree, the 3.0+ default). Every
	// status.resources[] entry then lacks health, for every kind.
	HealthModeAppTree HealthMode = "appTree"
)

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
	// HealthMode is set for Argo CD roots only. In appTree mode the
	// controller's per-resource verdicts are not in the CR, so any node
	// health present came from Radar (HealthSourceRadar) unless a host
	// overlaid the controller's answer from the Argo CD API.
	HealthMode HealthMode `json:"healthMode,omitempty"`
	// HealthFromAPI is true when a host filled per-resource health from the
	// controller's API server (HealthSourceControllerAPI). In appTree mode
	// that makes Argo's verdicts present after all, so nothing is derived and
	// no notice is due.
	HealthFromAPI bool `json:"healthFromApi,omitempty"`
	// HealthAPIError: the host asked the controller's API server and got no
	// usable answer, in the user's words ("the token isn't accepted for
	// this application"). Empty when nothing was configured to ask.
	HealthAPIError string `json:"healthApiError,omitempty"`
	// RemoteDestination is true for an Argo CD Application whose
	// spec.destination is another cluster, or a Flux object with
	// spec.kubeConfig. Radar's own reads describe the
	// local cluster, so nothing Radar derives can be attributed to such an
	// app's resources; hosts must not overlay Radar health onto it.
	RemoteDestination bool `json:"remoteDestination,omitempty"`
}

type managedResource struct {
	Ref          ResourceRef
	Sync         string
	Health       string
	HealthSource HealthSource
	Data         map[string]any
}

type relatedResource struct {
	Ref  ResourceRef
	Type EdgeType
	Data map[string]any
}
