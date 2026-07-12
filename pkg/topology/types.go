package topology

import (
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/hpadiag"
	k8score "github.com/skyhook-io/radar/pkg/k8score"
)

// NodeKind represents the type of a topology node
//
// When adding a new NodeKind constant, also update:
//   - builder.go: node creation + edge creation (both resources and traffic views)
//   - builder.go: genericCRDExclusion check (if kind is handled via dynamic cache)
//   - relationships.go: buildNodeID + normalizeKind maps, EdgeRoutesTo dispatch
//   - history.go: diff dispatch switch
//   - dashboard.go: resource counting (if applicable)
//   - capabilities.go: ResourcePermissions struct + permCheck array (if needs RBAC)
//   - dynamic_cache.go: warmup list (if CRD)
//   - if the kind is cluster-scoped: add an entry to topology.ClusterScopedKinds
//     in pkg/topology/cluster_scoped_kinds.go so the topology strip helpers
//     AND neighborhood per-node gates can SAR-check it. Missing the entry
//     leaks the cluster-scoped node to namespace-restricted users via
//     /api/topology, get_topology MCP, AND get_neighborhood MCP/REST.
type NodeKind string

const (
	KindInternet                       NodeKind = "Internet"
	KindIngress                        NodeKind = "Ingress"
	KindGateway                        NodeKind = "Gateway"
	KindHTTPRoute                      NodeKind = "HTTPRoute"
	KindGRPCRoute                      NodeKind = "GRPCRoute"
	KindTCPRoute                       NodeKind = "TCPRoute"
	KindTLSRoute                       NodeKind = "TLSRoute"
	KindService                        NodeKind = "Service"
	KindDeployment                     NodeKind = "Deployment"
	KindRollout                        NodeKind = "Rollout"
	KindApplication                    NodeKind = "Application"          // ArgoCD Application
	KindKustomization                  NodeKind = "Kustomization"        // FluxCD Kustomization
	KindHelmRelease                    NodeKind = "HelmRelease"          // FluxCD HelmRelease (Flux, not native Helm)
	KindGitRepository                  NodeKind = "GitRepository"        // FluxCD GitRepository
	KindCertificate                    NodeKind = "Certificate"          // cert-manager Certificate
	KindNode                           NodeKind = "Node"                 // Kubernetes Node (only shown when Karpenter-managed)
	KindNodePool                       NodeKind = "NodePool"             // Karpenter NodePool
	KindNodeClaim                      NodeKind = "NodeClaim"            // Karpenter NodeClaim
	KindNodeClass                      NodeKind = "NodeClass"            // Karpenter NodeClass (EC2NodeClass, AKSNodeClass, etc.)
	KindScaledObject                   NodeKind = "ScaledObject"         // KEDA ScaledObject
	KindScaledJob                      NodeKind = "ScaledJob"            // KEDA ScaledJob
	KindGatewayClass                   NodeKind = "GatewayClass"         // Gateway API GatewayClass
	KindVirtualService                 NodeKind = "VirtualService"       // Istio VirtualService
	KindDestinationRule                NodeKind = "DestinationRule"      // Istio DestinationRule
	KindIstioGateway                   NodeKind = "IstioGateway"         // Istio Gateway (networking.istio.io, NOT Gateway API)
	KindServiceEntry                   NodeKind = "ServiceEntry"         // Istio ServiceEntry
	KindPeerAuthentication             NodeKind = "PeerAuthentication"   // Istio PeerAuthentication
	KindAuthorizationPolicy            NodeKind = "AuthorizationPolicy"  // Istio AuthorizationPolicy
	KindKnativeService                 NodeKind = "KnativeService"       // KNative Serving Service
	KindKnativeConfiguration           NodeKind = "KnativeConfiguration" // KNative Serving Configuration
	KindKnativeRevision                NodeKind = "KnativeRevision"      // KNative Serving Revision
	KindKnativeRoute                   NodeKind = "KnativeRoute"         // KNative Serving Route
	KindBroker                         NodeKind = "Broker"               // KNative Eventing Broker
	KindTrigger                        NodeKind = "Trigger"              // KNative Eventing Trigger
	KindPingSource                     NodeKind = "PingSource"           // KNative Eventing PingSource
	KindApiServerSource                NodeKind = "ApiServerSource"      // KNative Eventing ApiServerSource
	KindContainerSource                NodeKind = "ContainerSource"      // KNative Eventing ContainerSource
	KindSinkBinding                    NodeKind = "SinkBinding"          // KNative Eventing SinkBinding
	KindChannel                        NodeKind = "Channel"              // KNative Messaging Channel
	KindIngressRoute                   NodeKind = "IngressRoute"         // Traefik IngressRoute
	KindIngressRouteTCP                NodeKind = "IngressRouteTCP"      // Traefik IngressRouteTCP
	KindIngressRouteUDP                NodeKind = "IngressRouteUDP"      // Traefik IngressRouteUDP
	KindMiddleware                     NodeKind = "Middleware"           // Traefik Middleware
	KindMiddlewareTCP                  NodeKind = "MiddlewareTCP"        // Traefik MiddlewareTCP
	KindTraefikService                 NodeKind = "TraefikService"       // Traefik TraefikService (advanced LB)
	KindServersTransport               NodeKind = "ServersTransport"     // Traefik ServersTransport
	KindServersTransportTCP            NodeKind = "ServersTransportTCP"  // Traefik ServersTransportTCP
	KindTLSOption                      NodeKind = "TLSOption"            // Traefik TLSOption
	KindTLSStore                       NodeKind = "TLSStore"             // Traefik TLSStore
	KindHTTPProxy                      NodeKind = "HTTPProxy"            // Contour HTTPProxy
	KindDaemonSet                      NodeKind = "DaemonSet"
	KindStatefulSet                    NodeKind = "StatefulSet"
	KindReplicaSet                     NodeKind = "ReplicaSet"
	KindPod                            NodeKind = "Pod"
	KindPodGroup                       NodeKind = "PodGroup"
	KindConfigMap                      NodeKind = "ConfigMap"
	KindSecret                         NodeKind = "Secret"
	KindServiceAccount                 NodeKind = "ServiceAccount"
	KindSealedSecret                   NodeKind = "SealedSecret"
	KindServiceMonitor                 NodeKind = "ServiceMonitor"
	KindPodMonitor                     NodeKind = "PodMonitor"
	KindHPA                            NodeKind = "HorizontalPodAutoscaler"
	KindJob                            NodeKind = "Job"
	KindCronJob                        NodeKind = "CronJob"
	KindWorkflow                       NodeKind = "Workflow"
	KindCronWorkflow                   NodeKind = "CronWorkflow"
	KindWorkflowTemplate               NodeKind = "WorkflowTemplate"
	KindClusterWorkflowTemplate        NodeKind = "ClusterWorkflowTemplate"
	KindPVC                            NodeKind = "PersistentVolumeClaim"
	KindPV                             NodeKind = "PersistentVolume"
	KindStorageClass                   NodeKind = "StorageClass"
	KindPDB                            NodeKind = "PodDisruptionBudget"
	KindNetworkPolicy                  NodeKind = "NetworkPolicy"
	KindCiliumNetworkPolicy            NodeKind = "CiliumNetworkPolicy"
	KindCiliumClusterwideNetworkPolicy NodeKind = "CiliumClusterwideNetworkPolicy"
	KindClusterNetworkPolicy           NodeKind = "ClusterNetworkPolicy"
	KindVPA                            NodeKind = "VerticalPodAutoscaler"
	KindNamespace                      NodeKind = "Namespace"
	KindCAPICluster                    NodeKind = "CAPICluster"         // Cluster API Cluster
	KindMachineDeployment              NodeKind = "MachineDeployment"   // Cluster API MachineDeployment
	KindMachineSet                     NodeKind = "MachineSet"          // Cluster API MachineSet
	KindMachine                        NodeKind = "Machine"             // Cluster API Machine
	KindMachinePool                    NodeKind = "MachinePool"         // Cluster API MachinePool
	KindKubeadmControlPlane            NodeKind = "KubeadmControlPlane" // Cluster API KubeadmControlPlane
	KindClusterClass                   NodeKind = "ClusterClass"        // Cluster API ClusterClass
	KindMachineHealthCheck             NodeKind = "MachineHealthCheck"  // Cluster API MachineHealthCheck
)

// HealthStatus represents the health status of a node
type HealthStatus string

const (
	StatusHealthy   HealthStatus = "healthy"
	StatusDegraded  HealthStatus = "degraded"
	StatusUnhealthy HealthStatus = "unhealthy"
	// StatusNeutral = intentional/idle (suspended, scaled-to-0, completed) —
	// renders sky on the frontend, distinct from StatusUnknown (gray).
	StatusNeutral HealthStatus = "neutral"
	StatusUnknown HealthStatus = "unknown"
)

// EdgeType represents the type of connection between nodes
type EdgeType string

const (
	EdgeRoutesTo   EdgeType = "routes-to"
	EdgeExposes    EdgeType = "exposes"
	EdgeManages    EdgeType = "manages"
	EdgeUses       EdgeType = "uses"
	EdgeProtects   EdgeType = "protects"
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
	PolicyEffect      string   `json:"policyEffect,omitempty"`      // "allowed", "blocked", or "unprotected" — set when ShowPolicyEffect is true
}

// PolicyEffect constants for edge annotation
const (
	PolicyEffectAllowed     = "allowed"     // Traffic explicitly allowed by a NetworkPolicy ingress rule
	PolicyEffectBlocked     = "blocked"     // Target has a selecting policy but source is not in any allow list
	PolicyEffectUnprotected = "unprotected" // Target has no selecting NetworkPolicy (default-allow)
)

// Topology represents the complete graph
type Topology struct {
	Nodes                   []Node   `json:"nodes"`
	Edges                   []Edge   `json:"edges"`
	Warnings                []string `json:"warnings,omitempty"`                // Warnings about resources that failed to load
	LargeCluster            bool     `json:"largeCluster,omitempty"`            // True if cluster exceeds large cluster threshold
	HiddenKinds             []string `json:"hiddenKinds,omitempty"`             // Resource kinds auto-hidden for performance
	RequiresNamespaceFilter bool     `json:"requiresNamespaceFilter,omitempty"` // True if cluster is too large for all-namespace topology
	CRDDiscoveryStatus      string   `json:"crdDiscoveryStatus,omitempty"`      // CRD discovery status: idle, discovering, ready
	EstimatedNodes          int      `json:"estimatedNodes,omitempty"`          // Pre-build node count estimate from the large-cluster optimizer; exposed so SSE / UI can tune debounce + render mode off the same signal
	SummaryMode             bool     `json:"summaryMode,omitempty"`             // True when the pod tier was collapsed into per-workload/service counts (see SummaryModeThreshold)
}

// StripNodeKinds removes nodes whose Kind is in deny, plus every edge that
// references one of the dropped node IDs. Used to hide cluster-scoped
// resources (Nodes, Karpenter NodePool, GatewayClass, …) from users who
// lack the per-kind RBAC to list them — the topology builder pulls those
// from the SA-populated cache regardless of the caller's namespace scope.
func (t *Topology) StripNodeKinds(deny map[NodeKind]bool) {
	if t == nil || len(deny) == 0 {
		return
	}
	dropped := make(map[string]bool)
	kept := t.Nodes[:0]
	for _, n := range t.Nodes {
		if deny[n.Kind] {
			dropped[n.ID] = true
			continue
		}
		kept = append(kept, n)
	}
	t.Nodes = kept
	if len(dropped) == 0 {
		return
	}
	keptEdges := t.Edges[:0]
	for _, e := range t.Edges {
		if dropped[e.Source] || dropped[e.Target] {
			continue
		}
		keptEdges = append(keptEdges, e)
	}
	t.Edges = keptEdges
}

// ViewMode determines how the topology is built
type ViewMode string

const (
	ViewModeTraffic   ViewMode = "traffic"   // Network-focused (Ingress/Gateway -> Service -> Pod)
	ViewModeResources ViewMode = "resources" // Comprehensive tree
)

// Large cluster threshold - when pre-grouped node count exceeds this, apply optimizations
// (aggressive pod grouping + auto-hide ConfigMaps/PVCs).
const LargeClusterThreshold = 1000

// Summary mode threshold - a second, higher tier above LargeClusterThreshold.
// When a namespace-filtered build's estimated node count crosses this, the
// builder drops the entire pod tier (no Pod / PodGroup nodes) and instead
// rolls pod health up onto the owning workload (resources view) or routing
// Service (traffic view) as a PodSummary. This bounds rendered nodes to
// workloads + networking regardless of pod count — the fix for tabs hanging
// on namespaces with thousands of pods.
const SummaryModeThreshold = 2000

// PodSummary aggregates pod health for a workload or service in summary mode,
// stamped onto the owning node's Data as "podSummary". Invariant maintained by
// addPodHealth: Total == Healthy + Degraded + Unhealthy. Unknown-phase pods are
// counted as Unhealthy (matching the bucketing GroupPods uses for PodGroups).
type PodSummary struct {
	Total     int `json:"total"`
	Healthy   int `json:"healthy"`
	Degraded  int `json:"degraded"`
	Unhealthy int `json:"unhealthy"`
}

// BuildOptions configures topology building
type BuildOptions struct {
	Namespaces           []string // Filter to specific namespaces (empty = all)
	ViewMode             ViewMode // How to display topology
	MaxIndividualPods    int      // Above this, pods are grouped (default: 5)
	IncludeSecrets       bool     // Include Secret nodes
	IncludeConfigMaps    bool     // Include ConfigMap nodes
	IncludePVCs          bool     // Include PersistentVolumeClaim nodes
	IncludeReplicaSets   bool     // Include ReplicaSet nodes (noisy intermediate objects)
	IncludeGenericCRDs   bool     // Include CRDs with owner refs to topology nodes (default: true)
	ForRelationshipCache bool     // Skip large cluster guard — used for internal relationship cache builds
	ShowPolicyEffect     bool     // Evaluate NetworkPolicies and annotate edges with allow/block/unprotected
	SummaryMode          bool     // Collapse the pod tier into per-workload/service counts (set by Build when estimate ≥ SummaryModeThreshold)
}

// MatchesNamespace returns true if ns is in the allowed list.
// nil means no filter (all namespaces match). An explicit empty slice means nothing matches.
// This is a standalone function that can be used by any code needing namespace filtering.
func MatchesNamespace(namespaces []string, ns string) bool {
	if namespaces == nil {
		return true
	}
	return slices.Contains(namespaces, ns)
}

// MatchesNamespaceFilter returns true if the given namespace matches the filter.
// An empty filter means all namespaces match.
func (opts BuildOptions) MatchesNamespaceFilter(ns string) bool {
	return MatchesNamespace(opts.Namespaces, ns)
}

// DefaultBuildOptions returns sensible defaults
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{
		Namespaces:         nil, // Empty = all namespaces
		ViewMode:           ViewModeResources,
		MaxIndividualPods:  5,
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
	Owner           *ResourceRef  `json:"owner,omitempty"`           // Parent via ownerReference (manages edge)
	Deployment      *ResourceRef  `json:"deployment,omitempty"`      // Grandparent Deployment (for Pods owned by ReplicaSets)
	Children        []ResourceRef `json:"children,omitempty"`        // Resources this owns (manages edge)
	Services        []ResourceRef `json:"services,omitempty"`        // Services selecting/exposing this
	Ingresses       []ResourceRef `json:"ingresses,omitempty"`       // Ingresses routing to this
	Gateways        []ResourceRef `json:"gateways,omitempty"`        // Gateways routing to this (via routes)
	Routes          []ResourceRef `json:"routes,omitempty"`          // Routes attached to this Gateway
	ConfigRefs      []ResourceRef `json:"configRefs,omitempty"`      // ConfigMaps/Secrets used by this
	Consumers       []ResourceRef `json:"consumers,omitempty"`       // For ConfigMap/Secret: workloads that reference this
	Scalers         []ResourceRef `json:"scalers,omitempty"`         // HPA/ScaledObject/ScaledJob scaling this
	StorageRefs     []ResourceRef `json:"storageRefs,omitempty"`     // PersistentVolumeClaims used by this workload
	ScaleTarget     *ResourceRef  `json:"scaleTarget,omitempty"`     // For HPA/ScaledObject: what it scales
	PDBs            []ResourceRef `json:"pdbs,omitempty"`            // PodDisruptionBudgets protecting this workload
	NetworkPolicies []ResourceRef `json:"networkPolicies,omitempty"` // NetworkPolicy / CiliumNetworkPolicy / ClusterNetworkPolicy / CiliumClusterwideNetworkPolicy selecting this workload
	Pods            []ResourceRef `json:"pods,omitempty"`            // For Service: pods it routes to

	// ServiceAccount is the ServiceAccount selected by this Pod or workload.
	ServiceAccount *ResourceRef `json:"serviceAccount,omitempty"`
	// Node is the Node this Pod is scheduled on (Pod-only field, derived from
	// pod.Spec.NodeName). Omitted when the Pod is unscheduled.
	Node *ResourceRef `json:"node,omitempty"`
	// ResourceClaims are the DRA ResourceClaims this Pod references (Pod-only
	// field). Direct claims come from pod.Spec.ResourceClaims; template-generated
	// claim names resolve via pod.Status.ResourceClaimStatuses.
	ResourceClaims []ResourceRef `json:"resourceClaims,omitempty"`
	// ManagedBy walks the owner-ref chain up to the topmost meaningful manager
	// — ArgoCD Application > Flux Kustomization/HelmRelease > Helm release >
	// topmost K8s owner (Deployment > ReplicaSet > Pod). Empty when no
	// meaningful manager is detectable.
	ManagedBy []ResourceRef `json:"managedBy,omitempty"`
}

// CertificateInfo holds parsed X.509 certificate metadata for a single certificate.
type CertificateInfo struct {
	Subject      string   `json:"subject"`
	SANs         []string `json:"sans,omitempty"`
	Issuer       string   `json:"issuer"`
	SelfSigned   bool     `json:"selfSigned,omitempty"`
	KeyType      string   `json:"keyType"`
	SerialNumber string   `json:"serialNumber"`
	NotBefore    string   `json:"notBefore"`
	NotAfter     string   `json:"notAfter"`
	DaysLeft     int      `json:"daysLeft"`
	Expired      bool     `json:"expired,omitempty"`
}

// SecretCertificateInfo holds parsed certificate data for a TLS secret.
// Certificates are in PEM order (leaf-first: index 0 is the server cert, subsequent entries are intermediates/root).
type SecretCertificateInfo struct {
	Certificates []CertificateInfo `json:"certificates"`
}

// CascadeDeletePreview represents all resources that will be garbage-collected
// when a parent resource is deleted via Kubernetes owner reference cascade.
type CascadeDeletePreview struct {
	Root       ResourceRef   `json:"root"`
	Dependents []ResourceRef `json:"dependents"`
}

// ResourceWithRelationships wraps a K8s resource with computed relationships
type ResourceWithRelationships struct {
	Resource        any                    `json:"resource"`
	Relationships   *Relationships         `json:"relationships,omitempty"`
	CertificateInfo *SecretCertificateInfo `json:"certificateInfo,omitempty"`
	HPADiagnosis    *hpadiag.Diagnosis     `json:"hpaDiagnosis,omitempty"`
}

// ResourceStatus holds computed status for a resource.
type ResourceStatus struct {
	Status  string
	Ready   string
	Message string
	Summary string
	Issue   string
}

// ResourceProvider is the data source for the topology builder.
// internal/k8s.ResourceCache implements this via topologyAdapter.
type ResourceProvider interface {
	Pods() ([]*corev1.Pod, error)
	Services() ([]*corev1.Service, error)
	Deployments() ([]*appsv1.Deployment, error)
	DaemonSets() ([]*appsv1.DaemonSet, error)
	StatefulSets() ([]*appsv1.StatefulSet, error)
	ReplicaSets() ([]*appsv1.ReplicaSet, error)
	Jobs() ([]*batchv1.Job, error)
	CronJobs() ([]*batchv1.CronJob, error)
	Ingresses() ([]*networkingv1.Ingress, error)
	ConfigMaps() ([]*corev1.ConfigMap, error)
	Secrets() ([]*corev1.Secret, error)
	PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error)
	PersistentVolumes() ([]*corev1.PersistentVolume, error)
	HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error)
	PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error)
	NetworkPolicies() ([]*networkingv1.NetworkPolicy, error)
	Nodes() ([]*corev1.Node, error)
	// GetResourceStatus returns health status for a resource; nil if unknown.
	GetResourceStatus(kind, namespace, name string) *ResourceStatus
}

// ServiceAccountProvider is implemented by providers that can expose workload identities.
type ServiceAccountProvider interface {
	ServiceAccounts() ([]*corev1.ServiceAccount, error)
}

// DynamicProvider adds CRD/dynamic resource support (pass nil to skip CRD nodes).
// It combines DynamicResourceCache + ResourceDiscovery methods.
type DynamicProvider interface {
	List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
	// ListNamespaces unions a GVR across an explicit namespace set (or reads
	// cluster-wide for an empty set / cluster-scoped resource). Topology uses
	// this instead of List(gvr, "") so namespace-restricted users with several
	// allowed namespaces still see CRD nodes from all of them.
	ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error)
	Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error)
	GetWatchedResources() []schema.GroupVersionResource
	GetDiscoveryStatus() k8score.CRDDiscoveryStatus
	GetGVR(kindOrName string) (schema.GroupVersionResource, bool)
	GetGVRWithGroup(kindOrName, group string) (schema.GroupVersionResource, bool)
	GetKindForGVR(gvr schema.GroupVersionResource) string
	IsCRD(kind string) bool
}
