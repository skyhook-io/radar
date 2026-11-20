package topology

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Profile controls how broadly a neighborhood expands. The public surface is
// intentionally small for agent ergonomics: auto picks a bounded edge set from
// the root kind, while all is the explicit escape hatch.
type Profile string

const (
	// ProfileAll walks every edge type.
	ProfileAll Profile = "all"
	// ProfileAuto picks an edge-type set based on the root kind. See
	// edgeTypesForAuto for the per-kind mapping.
	ProfileAuto Profile = "auto"
)

// ResolveProfile normalizes a user-supplied profile string to a Profile
// constant. Empty, whitespace, or unrecognized values fall back to
// ProfileAuto. Callers wanting the broadest expansion must pass all
// explicitly; unknown strings never silently broaden traversal.
func ResolveProfile(s string) Profile {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ProfileAuto):
		return ProfileAuto
	case string(ProfileAll):
		return ProfileAll
	default:
		return ProfileAuto
	}
}

// NeighborhoodOptions configures WalkNeighborhood. Zero values are replaced
// with sensible defaults: Profile=auto, Hops=1, MaxNodes=25.
type NeighborhoodOptions struct {
	Profile  Profile
	Hops     int
	MaxNodes int

	// Allow gates each candidate node during BFS expansion. nil means
	// allow all. When non-nil, BFS skips nodes for which Allow returns
	// false — they don't consume the MaxNodes budget, can't appear as
	// path-fragments between two allowed nodes, and set
	// Subgraph.RBACDenied so callers can report omissions to the user.
	// The root node is also checked so callers can hide roots that pass
	// coarse preflight checks but fail per-kind gates such as Secrets.
	//
	// Allow must decide by kind and namespace alone: WalkNeighborhood calls
	// it once for each (group, kind, namespace), with a node that has no
	// name, and applies the answer to every object of that kind there.
	//
	// This is the security boundary: forbidden nodes must not influence
	// the visible graph shape. Post-hoc filtering would let an
	// unauthorized node act as a bridge in the BFS frontier (or consume
	// truncation budget) before being dropped, leaking its existence
	// indirectly.
	Allow func(*Node) bool
}

// Subgraph is the BFS-expanded neighborhood of a root resource. Nodes are
// the existing topology Node shape (already summary-minified — id / kind /
// name / status / small data map). Edges are filtered by the profile and
// only included when both endpoints are present in Nodes.
type Subgraph struct {
	Root      ResourceRef `json:"root"`
	Nodes     []Node      `json:"nodes"`
	Edges     []Edge      `json:"edges"`
	Truncated bool        `json:"truncated,omitempty"`

	// RBACDenied is nonzero when Allow withheld some related node. Reported
	// as a single aggregated omission by the caller; we intentionally do NOT
	// track which specific nodes were denied, since surfacing those names
	// would re-introduce the existence-leak the Allow gate exists to prevent.
	RBACDenied int `json:"-"`

	// AmbiguousRoot is set when the root lookup matched more than one API
	// group and the caller omitted ResourceRef.Group. Callers should ask the
	// user/agent to provide group instead of returning an arbitrary first match.
	AmbiguousRoot bool `json:"-"`

	// RootUnavailable is the read reason Radar couldn't read the root for,
	// when no candidate the caller may read was found and at least one
	// couldn't be read: its kind still loading, not collected, or refused to
	// Radar's identity, for example. Empty when the root was found or is
	// absent. It is set only for candidates Allow admitted, so it reveals
	// nothing about one the caller may not read.
	RootUnavailable readreason.Code `json:"-"`

	// UncollectedRoots are the root candidates Allow admitted whose kind Radar
	// doesn't collect, which a host may start collecting before asking again.
	UncollectedRoots []resourceid.Ref `json:"-"`
}

// neighborhoodMaxHops is the hard ceiling on BFS depth. Two hops is enough
// to reach grandparents (Pod → ReplicaSet → Deployment) without exploding
// into the whole namespace.
const neighborhoodMaxHops = 2

// neighborhoodDefaultHops is the default BFS depth when Hops is unset.
const neighborhoodDefaultHops = 1

// neighborhoodDefaultMaxNodes is the default node-budget when MaxNodes is
// unset. 25 fits roughly a workload + its owners + selecting services +
// attached policies without spilling into cluster-level dependencies.
const neighborhoodDefaultMaxNodes = 25

// edgeTypesForProfile returns the set of edge types a profile traverses.
// rootKind is used for ProfileAuto only. Unknown profile values normalize to
// auto rather than all, so typos do not silently broaden traversal.
func edgeTypesForProfile(p Profile, rootKind NodeKind) map[EdgeType]bool {
	switch p {
	case ProfileAll:
		return allEdgeTypes()
	case ProfileAuto, "":
		// Empty falls through here so direct lib callers that leave
		// Profile unset get the documented default (auto), matching what
		// the REST and MCP handlers already produce.
		return edgeTypesForAuto(rootKind)
	default:
		// Unknown profile string (typo, future profile name from an old
		// client): pick auto rather than the broadest possible expansion.
		return edgeTypesForAuto(rootKind)
	}
}

func managementEdgeTypes() map[EdgeType]bool {
	return map[EdgeType]bool{EdgeManages: true}
}

func networkingEdgeTypes() map[EdgeType]bool {
	return map[EdgeType]bool{EdgeRoutesTo: true, EdgeExposes: true}
}

func policyEdgeTypes() map[EdgeType]bool {
	return map[EdgeType]bool{EdgeProtects: true}
}

// allEdgeTypes returns the universal set. Kept centralized so adding an
// EdgeType updates ProfileAll automatically.
func allEdgeTypes() map[EdgeType]bool {
	return map[EdgeType]bool{
		EdgeManages:    true,
		EdgeRoutesTo:   true,
		EdgeExposes:    true,
		EdgeUses:       true,
		EdgeProtects:   true,
		EdgeConfigures: true,
	}
}

// rootOnlyEdgeTypes are the profile's edge types followed only from the root.
// A GitOps object's sources are context: walking on through a shared source
// would fill the answer with the other objects reading from it, and from a
// managed workload would pull in its ConfigMaps and Secrets.
func rootOnlyEdgeTypes(profile Profile, rootKind NodeKind) map[EdgeType]bool {
	if profile == ProfileAll {
		return nil
	}
	switch rootKind {
	case KindApplication, KindKustomization, KindHelmRelease, KindGitRepository:
		return map[EdgeType]bool{EdgeConfigures: true}
	}
	return nil
}

// edgeTypesForAuto picks profile edge-types based on the root's kind. The
// goal is "the agent asked about a workload — show the management chain
// plus the network and policy attachments, not the whole graph."
func edgeTypesForAuto(rootKind NodeKind) map[EdgeType]bool {
	switch rootKind {
	// Workloads / pods: management chain + network exposure + protection.
	case KindPod, KindPodGroup, KindDeployment, KindStatefulSet, KindDaemonSet,
		KindReplicaSet, KindRollout, KindJob, KindCronJob, KindJobSet, KindWorkflow, KindCronWorkflow,
		KindKnativeService, KindKnativeRevision, KindKnativeConfiguration:
		return map[EdgeType]bool{
			EdgeManages:    true,
			EdgeRoutesTo:   true,
			EdgeExposes:    true,
			EdgeProtects:   true,
			EdgeConfigures: rootKind == KindWorkflow || rootKind == KindCronWorkflow,
		}
	case KindWorkflowTemplate, KindClusterWorkflowTemplate:
		return map[EdgeType]bool{EdgeConfigures: true, EdgeManages: true}
	// GitOps controllers: what they manage, and the sources they read from
	// (see rootOnlyEdgeTypes).
	case KindApplication, KindKustomization, KindHelmRelease, KindGitRepository:
		return map[EdgeType]bool{EdgeManages: true, EdgeConfigures: true}
	// Network-shaped resources: routing topology.
	case KindService, KindIngress, KindGateway, KindGatewayClass,
		KindHTTPRoute, KindGRPCRoute, KindTCPRoute, KindTLSRoute, KindUDPRoute,
		KindVirtualService, KindIstioGateway, KindHTTPProxy,
		KindIngressRoute, KindIngressRouteTCP, KindIngressRouteUDP:
		return networkingEdgeTypes()
	// Policies / protectors: who they attach to.
	case KindNetworkPolicy, KindCiliumNetworkPolicy,
		KindCiliumClusterwideNetworkPolicy, KindClusterNetworkPolicy,
		KindCalicoNetworkPolicy, KindCalicoGlobalNetworkPolicy,
		KindCalicoStagedNetworkPolicy, KindCalicoStagedGlobalNetworkPolicy,
		KindCalicoStagedKubernetesNetworkPolicy,
		KindPDB, KindMachineHealthCheck,
		KindPeerAuthentication, KindAuthorizationPolicy:
		return policyEdgeTypes()
	// Nodes / node pools: hosted workloads via management chain.
	case KindNode, KindNodePool, KindNodeClaim, KindNodeClass:
		return managementEdgeTypes()
	default:
		return allEdgeTypes()
	}
}

// findNodeByRef looks up a node by (kind, namespace, name [+ group]). Used as
// a fallback when buildNodeID's lowercase-kind heuristic produces an ID that
// doesn't match a CRD-prefixed node ID (e.g. "knativeservice/").
//
// When ref.Group is non-empty, the candidate node's apiGroup (derived from
// Data["apiVersion"] via apiVersionGroupFromData) must match — this is how
// callers disambiguate kind-collisions across API groups, e.g. asking for the
// CAPI cluster.x-k8s.io/Cluster vs the KubeFleet cluster.fleet.io/Cluster
// when both share the same kind+namespace+name. When ref.Group is empty,
// multiple matching API groups are reported as ambiguous instead of choosing
// an arbitrary first match; callers should ask for an explicit group.
//
// Pseudo-kinds: some CRDs are stored in the topology under a synthesized kind
// distinct from their API kind (KnativeService for serving.knative.dev/Service,
// CAPICluster for cluster.x-k8s.io/Cluster, …). We map (ref.Kind, ref.Group)
// through NodeKindForGVK BEFORE the direct kind comparison so a caller asking
// for kind=Service&group=serving.knative.dev finds the KnativeService topology
// node. Without this, the comparison Node.Kind="KnativeService" vs
// ref.Kind="Service" never matches and the root lookup silently fails.
func findNodeByRef(nodes []Node, ref ResourceRef, dp DynamicProvider) (*Node, bool) {
	// Resolve plural API resource names through discovery before comparing the
	// caller-facing Kubernetes kind and Radar's topology pseudo-kind.
	resourceKind := normalizeKindWithGroup(ref.Kind, ref.Group, dp)
	wantKind := NodeKindForGVK(resourceKind, ref.Group)
	builtinGroup, builtin := resourceid.BuiltinGroup(resourceKind)
	var match *Node
	matchGroup := ""
	for i := range nodes {
		n := &nodes[i]
		// The real Kubernetes kind catches group-less pseudo-kind lookups; the
		// topology kind catches an explicitly grouped pseudo-kind.
		if !strings.EqualFold(string(n.Kind), wantKind) && !strings.EqualFold(KubernetesKindForNode(n), resourceKind) {
			continue
		}
		if n.Name != ref.Name {
			continue
		}
		ns := nodeNamespaceFromData(n)
		if ns != ref.Namespace {
			continue
		}
		if ref.Group != "" {
			if !nodeMatchesAPIGroup(n, ref.Group) {
				continue
			}
		}
		group := nodeAPIGroupFromData(n)
		if ref.Group == "" && builtin && group != "" && group != builtinGroup {
			continue
		}
		if match != nil && group != matchGroup {
			return nil, true
		}
		match = n
		matchGroup = group
	}
	return match, false
}

func nodeIsCanonicalBuiltin(node *Node) bool {
	group, ok := resourceid.BuiltinGroup(KubernetesKindForNode(node))
	advertisedGroup := nodeAPIGroupFromData(node)
	return ok && (advertisedGroup == "" || advertisedGroup == group)
}

// NodeKindForGVK maps an (API kind, API group) to the synthesized topology
// kind the builder assigns when both differ. For (kind, group) pairs that
// aren't pseudo-mapped (or when group is empty), returns kind unchanged.
//
// Examples:
//
//	("Service",       "serving.knative.dev") → "KnativeService"
//	("Configuration", "serving.knative.dev") → "KnativeConfiguration"
//	("Cluster",       "cluster.x-k8s.io")    → "CAPICluster"
//	("Cluster",       "postgresql.cnpg.io")  → "Cluster"  (no pseudo-mapping)
//
// Keep this table in sync with the kinds the topology builder synthesizes —
// see the NodeKind constants prefixed Knative*/CAPI* in types.go. Missing an
// entry here means the caller-supplied API kind never matches the topology
// node, so the neighborhood root lookup silently returns "not found."
func NodeKindForGVK(kind, group string) string {
	if group == "" {
		return kind
	}
	// Accept both singular and plural lowercase forms because REST hits
	// this with URL-path kinds (plural — "services") after normalizeKind
	// lowercases them, while MCP arrives in Pascal-case singular
	// ("Service"). Without case + plurality insensitivity, REST root
	// lookups silently 404 for any cross-group CRD whose plural collides
	// with a built-in (e.g. serving.knative.dev/Service vs core/v1
	// Service, cluster.x-k8s.io/Cluster vs other CRDs).
	switch group {
	case "serving.knative.dev":
		switch strings.ToLower(kind) {
		case "service", "services":
			return string(KindKnativeService)
		case "configuration", "configurations":
			return string(KindKnativeConfiguration)
		case "revision", "revisions":
			return string(KindKnativeRevision)
		case "route", "routes":
			return string(KindKnativeRoute)
		}
	case "cluster.x-k8s.io":
		switch strings.ToLower(kind) {
		case "cluster", "clusters":
			return string(KindCAPICluster)
		}
	case "networking.istio.io":
		// Istio Gateway lives under IstioGateway to avoid collision with
		// gateway.networking.k8s.io/Gateway.
		switch strings.ToLower(kind) {
		case "gateway", "gateways":
			return string(KindIstioGateway)
		}
	case "projectcalico.org", "crd.projectcalico.org":
		switch strings.ToLower(kind) {
		case "networkpolicy", "networkpolicies", "caliconetworkpolicy":
			return string(KindCalicoNetworkPolicy)
		case "globalnetworkpolicy", "globalnetworkpolicies", "calicoglobalnetworkpolicy":
			return string(KindCalicoGlobalNetworkPolicy)
		case "stagednetworkpolicy", "stagednetworkpolicies", "calicostagednetworkpolicy":
			return string(KindCalicoStagedNetworkPolicy)
		case "stagedglobalnetworkpolicy", "stagedglobalnetworkpolicies", "calicostagedglobalnetworkpolicy":
			return string(KindCalicoStagedGlobalNetworkPolicy)
		case "stagedkubernetesnetworkpolicy", "stagedkubernetesnetworkpolicies", "calicostagedkubernetesnetworkpolicy":
			return string(KindCalicoStagedKubernetesNetworkPolicy)
		}
	}
	return kind
}

// nodeNamespaceFromData reads the namespace from a Node's Data map. Mirrors
// the convention used by the builder ("namespace" → string).
func nodeNamespaceFromData(n *Node) string {
	if n.Data == nil {
		return ""
	}
	if ns, ok := n.Data["namespace"].(string); ok {
		return ns
	}
	return ""
}

// nodeMatchesAPIGroup reports whether a node can be addressed through the given
// API group. A Calico policy is served by both projectcalico.org and
// crd.projectcalico.org, and is represented by a single node, so a reference
// through either group has to resolve to it.
func nodeMatchesAPIGroup(n *Node, group string) bool {
	if n == nil {
		return false
	}
	if group == "" {
		return true
	}
	if nodeAPIGroupFromData(n) == group {
		return true
	}
	if nodeAPIGroupFromData(n) == "" && resourceid.GroupForBuiltinKind(KubernetesKindForNode(n)) == group {
		return true
	}
	if !IsCalicoPolicyKind(n.Kind) {
		return false
	}
	for _, served := range calicoNodeAPIGroups(n) {
		if strings.EqualFold(served, group) {
			return true
		}
	}
	return false
}

// NodeReference is what a topology node records about the object it draws.
// Nodes built from typed informers carry no apiVersion, so their group is
// missing rather than known to be core.
func NodeReference(n *Node) resourceid.Reference {
	apiVersion, _ := n.Data["apiVersion"].(string)
	return resourceid.ReferenceFromAPIVersion(apiVersion, KubernetesKindForNode(n), nodeNamespaceFromData(n), n.Name)
}

// nodeGroup is a node's API group resolved against the current cluster; it is
// "" for the core group and for groups that cannot be established.
func nodeGroup(n *Node) string {
	return resourceid.ResolveCurrent(NodeReference(n), nil).Ref.Group
}

// nodeAPIGroupFromData extracts the API group from a Node's Data map.
// Returns "" when no apiVersion is set on the node.
func nodeAPIGroupFromData(n *Node) string {
	if n.Data == nil {
		return ""
	}
	v, ok := n.Data["apiVersion"].(string)
	if !ok {
		return ""
	}
	return resourceid.GroupFromAPIVersion(v)
}
