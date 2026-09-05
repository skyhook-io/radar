package k8s

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// DynamicResourceCache wraps the shared k8score implementation.
// Singleton + Radar-specific warmup list stay here.
type DynamicResourceCache struct {
	*k8score.DynamicResourceCache
}

var (
	dynamicResourceCache *DynamicResourceCache
	dynamicCacheOnce     = new(sync.Once)
	dynamicCacheMu       sync.Mutex
)

// InitDynamicResourceCache initializes the dynamic resource cache.
// If changeCh is provided, change notifications will be sent to it (for SSE).
func InitDynamicResourceCache(changeCh chan k8score.ResourceChange) error {
	var initErr error
	dynamicCacheOnce.Do(func() {
		client := GetDynamicClient()
		if client == nil {
			initErr = fmt.Errorf("dynamic client not initialized")
			return
		}

		// The cache always boots cluster-wide (or kubeconfig-fallback when
		// cluster-wide is denied); per-user namespace filtering happens at
		// the HTTP layer (see internal/server/namespace_scope.go).
		var nsFallback string
		var nsFallbacks []string
		if permResult := GetCachedPermissionResult(); permResult != nil {
			if permResult.NamespaceScoped && permResult.Namespace != "" {
				nsFallback = permResult.Namespace
			}
			// Candidates apply regardless of the typed outcome: an identity
			// can hold cluster-wide built-in access but namespace-only CRD
			// access (or the reverse) — the per-GVR probe decides.
			nsFallbacks = permResult.ScopeCandidates
		}

		// --namespace-scope pins namespaced CRD informers to the target namespace
		// (typed informers are pinned via probeResourceAccess). Cluster-scoped CRDs
		// stay cluster-wide — enforced by the gvrIsNamespaced guard in probeScope.
		var nsScoped bool
		var nsTarget string
		if ForceNamespaceScope {
			if t := GetNamespaceScopeTarget(); t != "" {
				nsScoped = true
				nsTarget = t
			}
		}

		discovery := GetResourceDiscovery()
		var sharedDiscovery *k8score.ResourceDiscovery
		if discovery != nil {
			sharedDiscovery = discovery.ResourceDiscovery
		}

		// Wiring-time capture — same rationale as InitResourceCache: a late
		// callback after a context switch must stamp its own cluster.
		recordClusterContext := ActiveClusterContext()

		core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{
			DynamicClient:      client,
			Discovery:          sharedDiscovery,
			Changes:            changeCh,
			NamespaceFallback:  nsFallback,
			NamespaceFallbacks: nsFallbacks,
			NamespaceScoped:    nsScoped,
			Namespace:          nsTarget,
			DebugEvents:        DebugEvents,
			OnReceived: func(kind string) {
				timeline.IncrementReceived(kind)
			},
			OnChange: func(change k8score.ResourceChange, obj, oldObj any) {
				u := extractUnstructured(obj)
				if u == nil {
					return
				}
				recordToTimelineStore(
					recordClusterContext,
					change.Kind,
					change.Namespace,
					change.Name,
					change.UID,
					change.Operation,
					oldObj,
					obj,
					change.Diff,
					true,
				)
			},
			OnDrop: func(kind, ns, name, reason, op string) {
				timeline.RecordDrop(kind, ns, name, reason, op, recordClusterContext)
			},
			OnRecorded: func(kind string) {
				timeline.IncrementRecorded(kind)
			},
			ComputeDiff: func(kind string, oldObj, newObj any) *k8score.DiffInfo {
				return ComputeDiff(kind, oldObj, newObj)
			},
		})
		if err != nil {
			initErr = err
			return
		}

		dynamicResourceCache = &DynamicResourceCache{DynamicResourceCache: core}
	})
	return initErr
}

// extractUnstructured safely gets the *unstructured.Unstructured from an any.
func extractUnstructured(obj any) interface{ GetName() string } {
	type hasName interface{ GetName() string }
	if u, ok := obj.(hasName); ok {
		return u
	}
	return nil
}

// GetDynamicResourceCache returns the singleton dynamic cache instance.
func GetDynamicResourceCache() *DynamicResourceCache {
	return dynamicResourceCache
}

// ResetDynamicResourceCache stops and clears the dynamic resource cache.
func ResetDynamicResourceCache() {
	dynamicCacheMu.Lock()
	defer dynamicCacheMu.Unlock()

	if dynamicResourceCache != nil {
		dynamicResourceCache.Stop()
		dynamicResourceCache = nil
	}
	dynamicCacheOnce = new(sync.Once)
}

// OnCRDDiscoveryComplete registers a callback to be called when CRD discovery completes.
// This is a package-level function for backward compatibility.
func OnCRDDiscoveryComplete(callback func()) {
	if dynamicResourceCache != nil && dynamicResourceCache.DynamicResourceCache != nil {
		dynamicResourceCache.DynamicResourceCache.OnCRDDiscoveryComplete(callback)
	}
}

type supportedCRDResource struct {
	Group      string
	Versions   []string
	Resource   string
	Kind       string
	Namespaced bool
}

var supportedCRDFallbacks = []supportedCRDResource{
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "applications", Kind: "Application", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "applicationsets", Kind: "ApplicationSet", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "appprojects", Kind: "AppProject", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "rollouts", Kind: "Rollout", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "analysisruns", Kind: "AnalysisRun", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "analysistemplates", Kind: "AnalysisTemplate", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "clusteranalysistemplates", Kind: "ClusterAnalysisTemplate", Namespaced: false},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "experiments", Kind: "Experiment", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "workflows", Kind: "Workflow", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "cronworkflows", Kind: "CronWorkflow", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "workflowtemplates", Kind: "WorkflowTemplate", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "clusterworkflowtemplates", Kind: "ClusterWorkflowTemplate", Namespaced: false},
	{Group: "tekton.dev", Versions: []string{"v1", "v1beta1"}, Resource: "pipelines", Kind: "Pipeline", Namespaced: true},
	{Group: "tekton.dev", Versions: []string{"v1", "v1beta1"}, Resource: "pipelineruns", Kind: "PipelineRun", Namespaced: true},
	{Group: "tekton.dev", Versions: []string{"v1", "v1beta1"}, Resource: "taskruns", Kind: "TaskRun", Namespaced: true},
	{Group: "cert-manager.io", Versions: []string{"v1"}, Resource: "certificates", Kind: "Certificate", Namespaced: true},
	{Group: "cert-manager.io", Versions: []string{"v1"}, Resource: "issuers", Kind: "Issuer", Namespaced: true},
	{Group: "cert-manager.io", Versions: []string{"v1"}, Resource: "clusterissuers", Kind: "ClusterIssuer", Namespaced: false},
	{Group: "cert-manager.io", Versions: []string{"v1"}, Resource: "certificaterequests", Kind: "CertificateRequest", Namespaced: true},
	{Group: "acme.cert-manager.io", Versions: []string{"v1"}, Resource: "orders", Kind: "Order", Namespaced: true},
	{Group: "acme.cert-manager.io", Versions: []string{"v1"}, Resource: "challenges", Kind: "Challenge", Namespaced: true},
	{Group: "source.toolkit.fluxcd.io", Versions: []string{"v1", "v1beta2"}, Resource: "gitrepositories", Kind: "GitRepository", Namespaced: true},
	{Group: "source.toolkit.fluxcd.io", Versions: []string{"v1beta2"}, Resource: "ocirepositories", Kind: "OCIRepository", Namespaced: true},
	{Group: "source.toolkit.fluxcd.io", Versions: []string{"v1", "v1beta2"}, Resource: "helmrepositories", Kind: "HelmRepository", Namespaced: true},
	{Group: "kustomize.toolkit.fluxcd.io", Versions: []string{"v1", "v1beta2"}, Resource: "kustomizations", Kind: "Kustomization", Namespaced: true},
	{Group: "helm.toolkit.fluxcd.io", Versions: []string{"v2", "v2beta2", "v2beta1"}, Resource: "helmreleases", Kind: "HelmRelease", Namespaced: true},
	{Group: "notification.toolkit.fluxcd.io", Versions: []string{"v1beta3", "v1beta2"}, Resource: "alerts", Kind: "Alert", Namespaced: true},
	{Group: "apiregistration.k8s.io", Versions: []string{"v1"}, Resource: "apiservices", Kind: "APIService", Namespaced: false},
	{Group: "apiextensions.k8s.io", Versions: []string{"v1"}, Resource: "customresourcedefinitions", Kind: "CustomResourceDefinition", Namespaced: false},
	// Admission webhook configs gate or silently rewrite admission for every
	// matching resource — high blast radius, cluster-scoped, very low churn.
	// Watched so a webhook appearing/changing shortly before failures surfaces
	// in get_changes (a mutating webhook that rewrites a field leaves no event
	// trail otherwise — see classifyAdmissionFailure, which only sees denials).
	{Group: "admissionregistration.k8s.io", Versions: []string{"v1"}, Resource: "mutatingwebhookconfigurations", Kind: "MutatingWebhookConfiguration", Namespaced: false},
	{Group: "admissionregistration.k8s.io", Versions: []string{"v1"}, Resource: "validatingwebhookconfigurations", Kind: "ValidatingWebhookConfiguration", Namespaced: false},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "gatewayclasses", Kind: "GatewayClass", Namespaced: false},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "gateways", Kind: "Gateway", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "grpcroutes", Kind: "GRPCRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1alpha2"}, Resource: "tcproutes", Kind: "TCPRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1alpha2"}, Resource: "tlsroutes", Kind: "TLSRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1beta1", "v1alpha2"}, Resource: "referencegrants", Kind: "ReferenceGrant", Namespaced: true},
	{Group: "external-secrets.io", Versions: []string{"v1", "v1beta1"}, Resource: "externalsecrets", Kind: "ExternalSecret", Namespaced: true},
	{Group: "external-secrets.io", Versions: []string{"v1", "v1beta1"}, Resource: "clusterexternalsecrets", Kind: "ClusterExternalSecret", Namespaced: false},
	{Group: "projectcalico.org", Versions: []string{"v3"}, Resource: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true},
	{Group: "projectcalico.org", Versions: []string{"v3"}, Resource: "globalnetworkpolicies", Kind: "GlobalNetworkPolicy", Namespaced: false},
	{Group: "projectcalico.org", Versions: []string{"v3"}, Resource: "stagednetworkpolicies", Kind: "StagedNetworkPolicy", Namespaced: true},
	{Group: "projectcalico.org", Versions: []string{"v3"}, Resource: "stagedglobalnetworkpolicies", Kind: "StagedGlobalNetworkPolicy", Namespaced: false},
	{Group: "projectcalico.org", Versions: []string{"v3"}, Resource: "stagedkubernetesnetworkpolicies", Kind: "StagedKubernetesNetworkPolicy", Namespaced: true},
	{Group: "crd.projectcalico.org", Versions: []string{"v1"}, Resource: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true},
	{Group: "crd.projectcalico.org", Versions: []string{"v1"}, Resource: "globalnetworkpolicies", Kind: "GlobalNetworkPolicy", Namespaced: false},
	{Group: "crd.projectcalico.org", Versions: []string{"v1"}, Resource: "stagednetworkpolicies", Kind: "StagedNetworkPolicy", Namespaced: true},
	{Group: "crd.projectcalico.org", Versions: []string{"v1"}, Resource: "stagedglobalnetworkpolicies", Kind: "StagedGlobalNetworkPolicy", Namespaced: false},
	{Group: "crd.projectcalico.org", Versions: []string{"v1"}, Resource: "stagedkubernetesnetworkpolicies", Kind: "StagedKubernetesNetworkPolicy", Namespaced: true},
	{Group: "external-secrets.io", Versions: []string{"v1", "v1beta1"}, Resource: "secretstores", Kind: "SecretStore", Namespaced: true},
	{Group: "external-secrets.io", Versions: []string{"v1", "v1beta1"}, Resource: "clustersecretstores", Kind: "ClusterSecretStore", Namespaced: false},
	{Group: "bitnami.com", Versions: []string{"v1alpha1"}, Resource: "sealedsecrets", Kind: "SealedSecret", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "virtualservices", Kind: "VirtualService", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "destinationrules", Kind: "DestinationRule", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "gateways", Kind: "Gateway", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "serviceentries", Kind: "ServiceEntry", Namespaced: true},
	{Group: "karpenter.sh", Versions: []string{"v1", "v1beta1"}, Resource: "nodepools", Kind: "NodePool", Namespaced: false},
	{Group: "karpenter.sh", Versions: []string{"v1", "v1beta1"}, Resource: "nodeclaims", Kind: "NodeClaim", Namespaced: false},
	{Group: "karpenter.k8s.aws", Versions: []string{"v1", "v1beta1"}, Resource: "ec2nodeclasses", Kind: "EC2NodeClass", Namespaced: false},
	{Group: "karpenter.azure.com", Versions: []string{"v1alpha2", "v1alpha1"}, Resource: "aksnodeclasses", Kind: "AKSNodeClass", Namespaced: false},
	{Group: "karpenter.k8s.gcp", Versions: []string{"v1alpha1"}, Resource: "gcenodeclasses", Kind: "GCENodeClass", Namespaced: false},
	{Group: "keda.sh", Versions: []string{"v1alpha1"}, Resource: "scaledobjects", Kind: "ScaledObject", Namespaced: true},
	{Group: "keda.sh", Versions: []string{"v1alpha1"}, Resource: "scaledjobs", Kind: "ScaledJob", Namespaced: true},
	{Group: "keda.sh", Versions: []string{"v1alpha1"}, Resource: "triggerauthentications", Kind: "TriggerAuthentication", Namespaced: true},
	{Group: "keda.sh", Versions: []string{"v1alpha1"}, Resource: "clustertriggerauthentications", Kind: "ClusterTriggerAuthentication", Namespaced: false},
	{Group: "autoscaling.k8s.io", Versions: []string{"v1", "v1beta2"}, Resource: "verticalpodautoscalers", Kind: "VerticalPodAutoscaler", Namespaced: true},
	{Group: "monitoring.coreos.com", Versions: []string{"v1"}, Resource: "servicemonitors", Kind: "ServiceMonitor", Namespaced: true},
	{Group: "monitoring.coreos.com", Versions: []string{"v1"}, Resource: "podmonitors", Kind: "PodMonitor", Namespaced: true},
	{Group: "monitoring.coreos.com", Versions: []string{"v1"}, Resource: "prometheusrules", Kind: "PrometheusRule", Namespaced: true},
	{Group: "monitoring.coreos.com", Versions: []string{"v1"}, Resource: "alertmanagers", Kind: "Alertmanager", Namespaced: true},
	{Group: "serving.knative.dev", Versions: []string{"v1"}, Resource: "services", Kind: "Service", Namespaced: true},
	{Group: "serving.knative.dev", Versions: []string{"v1"}, Resource: "configurations", Kind: "Configuration", Namespaced: true},
	{Group: "serving.knative.dev", Versions: []string{"v1"}, Resource: "revisions", Kind: "Revision", Namespaced: true},
	{Group: "serving.knative.dev", Versions: []string{"v1"}, Resource: "routes", Kind: "Route", Namespaced: true},
	{Group: "serving.knative.dev", Versions: []string{"v1beta1"}, Resource: "domainmappings", Kind: "DomainMapping", Namespaced: true},
	{Group: "networking.internal.knative.dev", Versions: []string{"v1alpha1"}, Resource: "ingresses", Kind: "Ingress", Namespaced: true},
	{Group: "networking.internal.knative.dev", Versions: []string{"v1alpha1"}, Resource: "certificates", Kind: "Certificate", Namespaced: true},
	{Group: "networking.internal.knative.dev", Versions: []string{"v1alpha1"}, Resource: "serverlessservices", Kind: "ServerlessService", Namespaced: true},
	{Group: "eventing.knative.dev", Versions: []string{"v1"}, Resource: "brokers", Kind: "Broker", Namespaced: true},
	{Group: "eventing.knative.dev", Versions: []string{"v1"}, Resource: "triggers", Kind: "Trigger", Namespaced: true},
	{Group: "eventing.knative.dev", Versions: []string{"v1beta2"}, Resource: "eventtypes", Kind: "EventType", Namespaced: true},
	{Group: "messaging.knative.dev", Versions: []string{"v1"}, Resource: "channels", Kind: "Channel", Namespaced: true},
	{Group: "messaging.knative.dev", Versions: []string{"v1"}, Resource: "inmemorychannels", Kind: "InMemoryChannel", Namespaced: true},
	{Group: "messaging.knative.dev", Versions: []string{"v1"}, Resource: "subscriptions", Kind: "Subscription", Namespaced: true},
	{Group: "sources.knative.dev", Versions: []string{"v1"}, Resource: "apiserversources", Kind: "ApiServerSource", Namespaced: true},
	{Group: "sources.knative.dev", Versions: []string{"v1"}, Resource: "containersources", Kind: "ContainerSource", Namespaced: true},
	{Group: "sources.knative.dev", Versions: []string{"v1"}, Resource: "pingsources", Kind: "PingSource", Namespaced: true},
	{Group: "sources.knative.dev", Versions: []string{"v1"}, Resource: "sinkbindings", Kind: "SinkBinding", Namespaced: true},
	{Group: "flows.knative.dev", Versions: []string{"v1"}, Resource: "sequences", Kind: "Sequence", Namespaced: true},
	{Group: "flows.knative.dev", Versions: []string{"v1"}, Resource: "parallels", Kind: "Parallel", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "ingressroutes", Kind: "IngressRoute", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "ingressroutetcps", Kind: "IngressRouteTCP", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "ingressrouteudps", Kind: "IngressRouteUDP", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "middlewares", Kind: "Middleware", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "middlewaretcps", Kind: "MiddlewareTCP", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "traefikservices", Kind: "TraefikService", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "serverstransports", Kind: "ServersTransport", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "serverstransporttcps", Kind: "ServersTransportTCP", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "tlsoptions", Kind: "TLSOption", Namespaced: true},
	{Group: "traefik.io", Versions: []string{"v1alpha1"}, Resource: "tlsstores", Kind: "TLSStore", Namespaced: true},
	// Legacy Traefik group (Traefik ≤ ~v2.10; v2.11+ and v3 use traefik.io).
	// Still a large install base. Discovery resolves single-group clusters fine
	// either way — these entries just restore pre-warm parity so legacy clusters
	// don't pay first-list latency. On a cluster serving BOTH groups, GetGVR
	// resolves to one of them; true dual-group topology is tracked separately.
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "ingressroutes", Kind: "IngressRoute", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "ingressroutetcps", Kind: "IngressRouteTCP", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "ingressrouteudps", Kind: "IngressRouteUDP", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "middlewares", Kind: "Middleware", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "middlewaretcps", Kind: "MiddlewareTCP", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "traefikservices", Kind: "TraefikService", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "serverstransports", Kind: "ServersTransport", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "serverstransporttcps", Kind: "ServersTransportTCP", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "tlsoptions", Kind: "TLSOption", Namespaced: true},
	{Group: "traefik.containo.us", Versions: []string{"v1alpha1"}, Resource: "tlsstores", Kind: "TLSStore", Namespaced: true},
	{Group: "projectcontour.io", Versions: []string{"v1"}, Resource: "httpproxies", Kind: "HTTPProxy", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "clusters", Kind: "Cluster", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "poolers", Kind: "Pooler", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "backups", Kind: "Backup", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "scheduledbackups", Kind: "ScheduledBackup", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "databases", Kind: "Database", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "publications", Kind: "Publication", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "subscriptions", Kind: "Subscription", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "imagecatalogs", Kind: "ImageCatalog", Namespaced: true},
	{Group: "postgresql.cnpg.io", Versions: []string{"v1"}, Resource: "clusterimagecatalogs", Kind: "ClusterImageCatalog", Namespaced: false},
	// The barman-cloud plugin ships its own group; the in-tree backup settings it
	// replaces had no CR at all.
	{Group: "barmancloud.cnpg.io", Versions: []string{"v1"}, Resource: "objectstores", Kind: "ObjectStore", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "clusters", Kind: "Cluster", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinedeployments", Kind: "MachineDeployment", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinesets", Kind: "MachineSet", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machines", Kind: "Machine", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinepools", Kind: "MachinePool", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "clusterclasses", Kind: "ClusterClass", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinehealthchecks", Kind: "MachineHealthCheck", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2"}, Resource: "machinedrainrules", Kind: "MachineDrainRule", Namespaced: true},
	{Group: "controlplane.cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "kubeadmcontrolplanes", Kind: "KubeadmControlPlane", Namespaced: true},
	{Group: "bootstrap.cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "kubeadmconfigs", Kind: "KubeadmConfig", Namespaced: true},
	{Group: "bootstrap.cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "kubeadmconfigtemplates", Kind: "KubeadmConfigTemplate", Namespaced: true},
	// The issue adapter reads all six: a failed Backup is only visible if its
	// informer is running, and BackupRepository/BSL are the backup targets whose
	// health explains why the runs fail.
	{Group: "velero.io", Versions: []string{"v1"}, Resource: "backups", Kind: "Backup", Namespaced: true},
	{Group: "velero.io", Versions: []string{"v1"}, Resource: "restores", Kind: "Restore", Namespaced: true},
	{Group: "velero.io", Versions: []string{"v1"}, Resource: "schedules", Kind: "Schedule", Namespaced: true},
	{Group: "velero.io", Versions: []string{"v1"}, Resource: "backupstoragelocations", Kind: "BackupStorageLocation", Namespaced: true},
	{Group: "velero.io", Versions: []string{"v1"}, Resource: "volumesnapshotlocations", Kind: "VolumeSnapshotLocation", Namespaced: true},
	{Group: "velero.io", Versions: []string{"v1"}, Resource: "backuprepositories", Kind: "BackupRepository", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "vulnerabilityreports", Kind: "VulnerabilityReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "configauditreports", Kind: "ConfigAuditReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "exposedsecretreports", Kind: "ExposedSecretReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "rbacassessmentreports", Kind: "RbacAssessmentReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "clusterrbacassessmentreports", Kind: "ClusterRbacAssessmentReport", Namespaced: false},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "clustercompliancereports", Kind: "ClusterComplianceReport", Namespaced: false},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "sbomreports", Kind: "SbomReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "clustersbomreports", Kind: "ClusterSbomReport", Namespaced: false},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "infraassessmentreports", Kind: "InfraAssessmentReport", Namespaced: true},
	{Group: "aquasecurity.github.io", Versions: []string{"v1alpha1"}, Resource: "clusterinfraassessmentreports", Kind: "ClusterInfraAssessmentReport", Namespaced: false},
	// Crossplane core (apiextensions = XRDs/Compositions; pkg = Provider/Function/Configuration).
	// Managed Resources (Bucket, Database, etc.) and XRs from XRDs are intentionally NOT warmed —
	// the kind set is unbounded per provider/XRD, so they're picked up lazily on first list.
	{Group: "apiextensions.crossplane.io", Versions: []string{"v1", "v2", "v1beta1"}, Resource: "compositeresourcedefinitions", Kind: "CompositeResourceDefinition", Namespaced: false},
	{Group: "apiextensions.crossplane.io", Versions: []string{"v1", "v2"}, Resource: "compositions", Kind: "Composition", Namespaced: false},
	{Group: "apiextensions.crossplane.io", Versions: []string{"v1", "v2"}, Resource: "compositionrevisions", Kind: "CompositionRevision", Namespaced: false},
	{Group: "apiextensions.crossplane.io", Versions: []string{"v1beta1"}, Resource: "environmentconfigs", Kind: "EnvironmentConfig", Namespaced: false},
	{Group: "pkg.crossplane.io", Versions: []string{"v1", "v1beta1"}, Resource: "providers", Kind: "Provider", Namespaced: false},
	{Group: "pkg.crossplane.io", Versions: []string{"v1", "v1beta1"}, Resource: "providerrevisions", Kind: "ProviderRevision", Namespaced: false},
	{Group: "pkg.crossplane.io", Versions: []string{"v1", "v1beta1"}, Resource: "functions", Kind: "Function", Namespaced: false},
	{Group: "pkg.crossplane.io", Versions: []string{"v1"}, Resource: "functionrevisions", Kind: "FunctionRevision", Namespaced: false},
	{Group: "pkg.crossplane.io", Versions: []string{"v1"}, Resource: "configurations", Kind: "Configuration", Namespaced: false},
	{Group: "pkg.crossplane.io", Versions: []string{"v1beta1"}, Resource: "deploymentruntimeconfigs", Kind: "DeploymentRuntimeConfig", Namespaced: false},
	// provider-kubernetes (no cloud creds needed, ideal for demo clusters)
	{Group: "kubernetes.crossplane.io", Versions: []string{"v1alpha2", "v1alpha1"}, Resource: "providerconfigs", Kind: "ProviderConfig", Namespaced: false},
	{Group: "kubernetes.crossplane.io", Versions: []string{"v1alpha2", "v1alpha1"}, Resource: "objects", Kind: "Object", Namespaced: false},
	// provider-helm
	{Group: "helm.crossplane.io", Versions: []string{"v1beta1"}, Resource: "providerconfigs", Kind: "ProviderConfig", Namespaced: false},
	{Group: "helm.crossplane.io", Versions: []string{"v1beta1"}, Resource: "releases", Kind: "Release", Namespaced: false},
	// Kyverno admission/policy CRDs. Presence of either family in discovery is
	// the signal that the cluster runs Kyverno, which flips the conditional
	// PolicyReport warmup (see policy_reports.go).
	//
	// Legacy kyverno.io family — deprecated in Kyverno 1.18, removal planned
	// for 1.20. Kept because the installed base still runs it; it gets no new
	// feature investment.
	{Group: "kyverno.io", Versions: []string{"v1", "v2", "v2beta1"}, Resource: "policies", Kind: "Policy", Namespaced: true},
	{Group: "kyverno.io", Versions: []string{"v1", "v2", "v2beta1"}, Resource: "clusterpolicies", Kind: "ClusterPolicy", Namespaced: false},
	{Group: "kyverno.io", Versions: []string{"v2", "v2beta1"}, Resource: "cleanuppolicies", Kind: "CleanupPolicy", Namespaced: true},
	{Group: "kyverno.io", Versions: []string{"v2", "v2beta1"}, Resource: "clustercleanuppolicies", Kind: "ClusterCleanupPolicy", Namespaced: false},
	{Group: "kyverno.io", Versions: []string{"v2", "v2beta1"}, Resource: "policyexceptions", Kind: "PolicyException", Namespaced: true},
	{Group: "kyverno.io", Versions: []string{"v2"}, Resource: "updaterequests", Kind: "UpdateRequest", Namespaced: true},
	{Group: "kyverno.io", Versions: []string{"v2", "v2beta1", "v2alpha1"}, Resource: "globalcontextentries", Kind: "GlobalContextEntry", Namespaced: false},
	// Intermediate reports the controller writes before aggregating into
	// PolicyReports; they carry their own group.
	{Group: "reports.kyverno.io", Versions: []string{"v1"}, Resource: "ephemeralreports", Kind: "EphemeralReport", Namespaced: true},
	{Group: "reports.kyverno.io", Versions: []string{"v1"}, Resource: "clusterephemeralreports", Kind: "ClusterEphemeralReport", Namespaced: false},
	// Modern policies.kyverno.io CEL family — stabilized at v1 in Kyverno
	// 1.17/1.18, and the family that survives the 1.20 removal. Discovery
	// already auto-watches small CRDs, so these entries buy the guarantee plus
	// partial-discovery recovery rather than first-time visibility.
	//
	// PolicyException appears in BOTH groups with the same Kind and the same
	// plural but a different spec shape; consumers must dispatch on the API
	// group, never on the plural alone.
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1", "v1alpha1"}, Resource: "validatingpolicies", Kind: "ValidatingPolicy", Namespaced: false},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1", "v1alpha1"}, Resource: "imagevalidatingpolicies", Kind: "ImageValidatingPolicy", Namespaced: false},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1", "v1alpha1"}, Resource: "mutatingpolicies", Kind: "MutatingPolicy", Namespaced: false},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1", "v1alpha1"}, Resource: "generatingpolicies", Kind: "GeneratingPolicy", Namespaced: false},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1", "v1alpha1"}, Resource: "deletingpolicies", Kind: "DeletingPolicy", Namespaced: false},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1"}, Resource: "namespacedvalidatingpolicies", Kind: "NamespacedValidatingPolicy", Namespaced: true},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1"}, Resource: "namespacedimagevalidatingpolicies", Kind: "NamespacedImageValidatingPolicy", Namespaced: true},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1"}, Resource: "namespacedmutatingpolicies", Kind: "NamespacedMutatingPolicy", Namespaced: true},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1"}, Resource: "namespacedgeneratingpolicies", Kind: "NamespacedGeneratingPolicy", Namespaced: true},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1"}, Resource: "namespaceddeletingpolicies", Kind: "NamespacedDeletingPolicy", Namespaced: true},
	{Group: "policies.kyverno.io", Versions: []string{"v1", "v1beta1", "v1alpha1"}, Resource: "policyexceptions", Kind: "PolicyException", Namespaced: true},
	// DRA (built-in resource.k8s.io, GA in K8s 1.34). Normal discovery flags
	// these IsCRD (group not in coreAPIGroups) and watches them; the fallback
	// entries cover partial-discovery clusters. v1beta2 serves 1.32-1.33.
	{Group: "resource.k8s.io", Versions: []string{"v1", "v1beta2"}, Resource: "resourceclaims", Kind: "ResourceClaim", Namespaced: true},
	{Group: "resource.k8s.io", Versions: []string{"v1", "v1beta2"}, Resource: "resourceclaimtemplates", Kind: "ResourceClaimTemplate", Namespaced: true},
	{Group: "resource.k8s.io", Versions: []string{"v1", "v1beta2"}, Resource: "deviceclasses", Kind: "DeviceClass", Namespaced: false},
	{Group: "resource.k8s.io", Versions: []string{"v1", "v1beta2"}, Resource: "resourceslices", Kind: "ResourceSlice", Namespaced: false},
	// NVIDIA GPU Operator. ClusterPolicy collides with Kyverno's — group
	// disambiguates everywhere downstream.
	{Group: "nvidia.com", Versions: []string{"v1"}, Resource: "clusterpolicies", Kind: "ClusterPolicy", Namespaced: false},
	{Group: "nvidia.com", Versions: []string{"v1alpha1"}, Resource: "nvidiadrivers", Kind: "NVIDIADriver", Namespaced: false},
	// NOTE: the wgpolicyk8s.io PolicyReport CRDs are intentionally NOT in
	// this list. They are warmed up conditionally — only when Kyverno is
	// detected — via WarmupKyvernoPolicyReports in policy_reports.go. Adding
	// them here would warm them up on every cluster that has the CRD
	// installed (e.g. for Trivy reports), which we don't want until we have
	// a generic per-engine policy index. See T5 in the plan.
}

func RegisterSupportedCRDFallbacks() {
	discovery := GetResourceDiscovery()
	client := GetDynamicClient()
	if discovery == nil || client == nil {
		return
	}
	if !discovery.HasPartialDiscovery() {
		return
	}

	var nsFallbacks []string
	if permResult := GetCachedPermissionResult(); permResult != nil {
		nsFallbacks = permResult.ScopeCandidates
		if len(nsFallbacks) == 0 && permResult.NamespaceScoped && permResult.Namespace != "" {
			nsFallbacks = []string{permResult.Namespace}
		}
	}

	const maxConcurrentProbes = 12
	sem := make(chan struct{}, maxConcurrentProbes)
	var wg sync.WaitGroup
	var mu sync.Mutex
	registered := 0

	for _, candidate := range supportedCRDFallbacks {
		if _, ok := discovery.GetResourceWithGroup(candidate.Kind, candidate.Group); ok {
			continue
		}
		if !discovery.GroupHadPartialDiscovery(candidate.Group) {
			continue
		}
		c := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			for _, version := range c.Versions {
				gvr := schema.GroupVersionResource{Group: c.Group, Version: version, Resource: c.Resource}
				namespaces, ok := fallbackListProbe(client, gvr, c.Namespaced, nsFallbacks)
				if !ok {
					continue
				}
				watchable := false
				for _, namespace := range namespaces {
					if fallbackWatchProbe(client, gvr, namespace) {
						watchable = true
						break
					}
				}
				if !watchable {
					continue
				}
				discovery.AddAPIResource(k8score.APIResource{
					Group:      c.Group,
					Version:    version,
					Kind:       c.Kind,
					Name:       c.Resource,
					Namespaced: c.Namespaced,
					IsCRD:      true,
					Verbs:      []string{"get", "list", "watch"},
				})
				mu.Lock()
				registered++
				mu.Unlock()
				log.Printf("[crd-fallback] Registered %s.%s/%s after direct probe", c.Resource, c.Group, version)
				return
			}
		}()
	}

	wg.Wait()
	if registered > 0 {
		log.Printf("[crd-fallback] Registered %d supported CRDs missing from API discovery", registered)
	}
}

func fallbackListProbe(client dynamic.Interface, gvr schema.GroupVersionResource, namespaced bool, nsFallbacks []string) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil {
		return []string{""}, true
	}
	if namespaced && len(nsFallbacks) > 0 {
		if !isExpectedFallbackProbeDenial(err) {
			log.Printf("[crd-fallback] Cluster-wide list probe failed for %s.%s/%s: %v", gvr.Resource, gvr.Group, gvr.Version, err)
		}
		// Registration probe only — the dynamic cache re-probes every
		// candidate per-GVR when it starts watching. Return every
		// list-granted candidate so the caller can find one that also
		// grants watch (list-only in the first namespace must not hide a
		// CRD that is fully readable in a later one). One shared budget for
		// the whole walk plus a per-candidate sub-deadline: a fresh 5s per
		// candidate would let a degraded apiserver stall discovery for
		// minutes at the 20-candidate cap.
		walkCtx, walkCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer walkCancel()
		var granted []string
		for _, ns := range nsFallbacks {
			if walkCtx.Err() != nil {
				break
			}
			nsCtx, nsCancel := context.WithTimeout(walkCtx, 2*time.Second)
			nsErr := func() error {
				defer nsCancel()
				_, e := client.Resource(gvr).Namespace(ns).List(nsCtx, metav1.ListOptions{Limit: 1})
				return e
			}()
			if nsErr == nil {
				granted = append(granted, ns)
				continue
			}
			if !isExpectedFallbackProbeDenial(nsErr) {
				log.Printf("[crd-fallback] Namespace list probe failed for %s.%s/%s in ns=%q: %v", gvr.Resource, gvr.Group, gvr.Version, ns, nsErr)
			}
		}
		return granted, len(granted) > 0
	}
	if !isExpectedFallbackProbeDenial(err) {
		log.Printf("[crd-fallback] List probe failed for %s.%s/%s: %v", gvr.Resource, gvr.Group, gvr.Version, err)
	}
	return nil, false
}

func fallbackWatchProbe(client dynamic.Interface, gvr schema.GroupVersionResource, namespace string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	timeoutSeconds := int64(1)
	resource := client.Resource(gvr)
	var (
		watchInterface interface{ Stop() }
		err            error
	)
	if namespace != "" {
		watchInterface, err = resource.Namespace(namespace).Watch(ctx, metav1.ListOptions{TimeoutSeconds: &timeoutSeconds})
	} else {
		watchInterface, err = resource.Watch(ctx, metav1.ListOptions{TimeoutSeconds: &timeoutSeconds})
	}
	if err != nil {
		if !isExpectedFallbackProbeDenial(err) {
			log.Printf("[crd-fallback] Watch probe failed for %s.%s/%s in ns=%q: %v", gvr.Resource, gvr.Group, gvr.Version, namespace, err)
		}
		return false
	}
	if watchInterface != nil {
		watchInterface.Stop()
	}
	return true
}

func isExpectedFallbackProbeDenial(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err)
}

// WarmupCommonCRDs starts watching common CRDs (Rollouts, Workflows, etc.) at startup.
func WarmupCommonCRDs() {
	cache := GetDynamicResourceCache()
	if cache == nil {
		return
	}

	discovery := GetResourceDiscovery()
	if discovery == nil {
		return
	}

	var gvrs []schema.GroupVersionResource
	seen := make(map[schema.GroupVersionResource]bool)
	for _, candidate := range supportedCRDFallbacks {
		if gvr, ok := discovery.GetGVRWithGroup(candidate.Kind, candidate.Group); ok && !seen[gvr] {
			seen[gvr] = true
			gvrs = append(gvrs, gvr)
			log.Printf("Warming up CRD: %s (%s)", candidate.Kind, candidate.Group)
		}
	}

	if len(gvrs) > 0 {
		cache.WarmupParallel(gvrs, 10*time.Second)
	}
}
