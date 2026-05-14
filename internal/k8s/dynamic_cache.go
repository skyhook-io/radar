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
		if permResult := GetCachedPermissionResult(); permResult != nil && permResult.NamespaceScoped && permResult.Namespace != "" {
			nsFallback = permResult.Namespace
		}

		discovery := GetResourceDiscovery()
		var sharedDiscovery *k8score.ResourceDiscovery
		if discovery != nil {
			sharedDiscovery = discovery.ResourceDiscovery
		}

		core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{
			DynamicClient:     client,
			Discovery:         sharedDiscovery,
			Changes:           changeCh,
			NamespaceFallback: nsFallback,
			DebugEvents:       DebugEvents,
			OnReceived: func(kind string) {
				timeline.IncrementReceived(kind)
			},
			OnChange: func(change k8score.ResourceChange, obj, oldObj any) {
				u := extractUnstructured(obj)
				if u == nil {
					return
				}
				recordToTimelineStore(
					change.Kind,
					change.Namespace,
					change.Name,
					change.UID,
					change.Operation,
					oldObj,
					obj,
				)
			},
			OnDrop: func(kind, ns, name, reason, op string) {
				timeline.RecordDrop(kind, ns, name, reason, op)
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
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "workflows", Kind: "Workflow", Namespaced: true},
	{Group: "argoproj.io", Versions: []string{"v1alpha1"}, Resource: "cronworkflows", Kind: "CronWorkflow", Namespaced: true},
	{Group: "cert-manager.io", Versions: []string{"v1"}, Resource: "certificates", Kind: "Certificate", Namespaced: true},
	{Group: "cert-manager.io", Versions: []string{"v1"}, Resource: "certificaterequests", Kind: "CertificateRequest", Namespaced: true},
	{Group: "acme.cert-manager.io", Versions: []string{"v1"}, Resource: "orders", Kind: "Order", Namespaced: true},
	{Group: "acme.cert-manager.io", Versions: []string{"v1"}, Resource: "challenges", Kind: "Challenge", Namespaced: true},
	{Group: "source.toolkit.fluxcd.io", Versions: []string{"v1", "v1beta2"}, Resource: "gitrepositories", Kind: "GitRepository", Namespaced: true},
	{Group: "source.toolkit.fluxcd.io", Versions: []string{"v1beta2"}, Resource: "ocirepositories", Kind: "OCIRepository", Namespaced: true},
	{Group: "source.toolkit.fluxcd.io", Versions: []string{"v1", "v1beta2"}, Resource: "helmrepositories", Kind: "HelmRepository", Namespaced: true},
	{Group: "kustomize.toolkit.fluxcd.io", Versions: []string{"v1", "v1beta2"}, Resource: "kustomizations", Kind: "Kustomization", Namespaced: true},
	{Group: "helm.toolkit.fluxcd.io", Versions: []string{"v2", "v2beta2", "v2beta1"}, Resource: "helmreleases", Kind: "HelmRelease", Namespaced: true},
	{Group: "notification.toolkit.fluxcd.io", Versions: []string{"v1beta3", "v1beta2"}, Resource: "alerts", Kind: "Alert", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "gatewayclasses", Kind: "GatewayClass", Namespaced: false},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "gateways", Kind: "Gateway", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1", "v1beta1"}, Resource: "grpcroutes", Kind: "GRPCRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1alpha2"}, Resource: "tcproutes", Kind: "TCPRoute", Namespaced: true},
	{Group: "gateway.networking.k8s.io", Versions: []string{"v1alpha2"}, Resource: "tlsroutes", Kind: "TLSRoute", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "virtualservices", Kind: "VirtualService", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "destinationrules", Kind: "DestinationRule", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "gateways", Kind: "Gateway", Namespaced: true},
	{Group: "networking.istio.io", Versions: []string{"v1", "v1beta1", "v1alpha3"}, Resource: "serviceentries", Kind: "ServiceEntry", Namespaced: true},
	{Group: "karpenter.sh", Versions: []string{"v1", "v1beta1"}, Resource: "nodepools", Kind: "NodePool", Namespaced: false},
	{Group: "karpenter.sh", Versions: []string{"v1", "v1beta1"}, Resource: "nodeclaims", Kind: "NodeClaim", Namespaced: false},
	{Group: "karpenter.k8s.aws", Versions: []string{"v1", "v1beta1"}, Resource: "ec2nodeclasses", Kind: "EC2NodeClass", Namespaced: false},
	{Group: "karpenter.azure.com", Versions: []string{"v1alpha2", "v1alpha1"}, Resource: "aksnodeclasses", Kind: "AKSNodeClass", Namespaced: false},
	{Group: "karpenter.gcp.compute.com", Versions: []string{"v1alpha1"}, Resource: "gcpnodeclasses", Kind: "GCPNodeClass", Namespaced: false},
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
	{Group: "projectcontour.io", Versions: []string{"v1"}, Resource: "httpproxies", Kind: "HTTPProxy", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "clusters", Kind: "Cluster", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinedeployments", Kind: "MachineDeployment", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinesets", Kind: "MachineSet", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machines", Kind: "Machine", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinepools", Kind: "MachinePool", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "clusterclasses", Kind: "ClusterClass", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "machinehealthchecks", Kind: "MachineHealthCheck", Namespaced: true},
	{Group: "cluster.x-k8s.io", Versions: []string{"v1beta2"}, Resource: "machinedrainrules", Kind: "MachineDrainRule", Namespaced: true},
	{Group: "controlplane.cluster.x-k8s.io", Versions: []string{"v1beta2", "v1beta1"}, Resource: "kubeadmcontrolplanes", Kind: "KubeadmControlPlane", Namespaced: true},
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
	// Kyverno admission/policy CRDs. Watching Policy/ClusterPolicy directly
	// is what flips the conditional PolicyReport warmup (see policy_reports.go) —
	// presence of these in discovery is the signal that the cluster runs Kyverno.
	{Group: "kyverno.io", Versions: []string{"v1", "v2", "v2beta1"}, Resource: "policies", Kind: "Policy", Namespaced: true},
	{Group: "kyverno.io", Versions: []string{"v1", "v2", "v2beta1"}, Resource: "clusterpolicies", Kind: "ClusterPolicy", Namespaced: false},
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

	nsFallback := ""
	if permResult := GetCachedPermissionResult(); permResult != nil && permResult.NamespaceScoped && permResult.Namespace != "" {
		nsFallback = permResult.Namespace
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
				namespace, ok := fallbackListProbe(client, gvr, c.Namespaced, nsFallback)
				if !ok {
					continue
				}
				if !fallbackWatchProbe(client, gvr, namespace) {
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

func fallbackListProbe(client dynamic.Interface, gvr schema.GroupVersionResource, namespaced bool, nsFallback string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 1})
	if err == nil {
		return "", true
	}
	if namespaced && nsFallback != "" {
		if !isExpectedFallbackProbeDenial(err) {
			log.Printf("[crd-fallback] Cluster-wide list probe failed for %s.%s/%s: %v", gvr.Resource, gvr.Group, gvr.Version, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = client.Resource(gvr).Namespace(nsFallback).List(ctx, metav1.ListOptions{Limit: 1})
		if err == nil {
			return nsFallback, true
		}
		if !isExpectedFallbackProbeDenial(err) {
			log.Printf("[crd-fallback] Namespace list probe failed for %s.%s/%s in ns=%q: %v", gvr.Resource, gvr.Group, gvr.Version, nsFallback, err)
		}
		return "", false
	}
	if !isExpectedFallbackProbeDenial(err) {
		log.Printf("[crd-fallback] List probe failed for %s.%s/%s: %v", gvr.Resource, gvr.Group, gvr.Version, err)
	}
	return "", false
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
