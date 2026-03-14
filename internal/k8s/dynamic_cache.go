package k8s

import (
	"fmt"
	"log"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

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

		// Build namespace scope from permission result
		var nsScoped bool
		var ns string
		if permResult := GetCachedPermissionResult(); permResult != nil && permResult.NamespaceScoped && permResult.Namespace != "" {
			nsScoped = true
			ns = permResult.Namespace
		}

		discovery := GetResourceDiscovery()
		var sharedDiscovery *k8score.ResourceDiscovery
		if discovery != nil {
			sharedDiscovery = discovery.ResourceDiscovery
		}

		core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{
			DynamicClient:   client,
			Discovery:       sharedDiscovery,
			Changes:         changeCh,
			NamespaceScoped: nsScoped,
			Namespace:       ns,
			DebugEvents:     DebugEvents,
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

	// Common CRDs that should be warmed up for timeline visibility
	commonCRDs := []string{
		"Rollout",                      // Argo Rollouts
		"Workflow",                     // Argo Workflows
		"CronWorkflow",                 // Argo Workflows
		"Certificate",                  // cert-manager
		"CertificateRequest",           // cert-manager
		"Order",                        // cert-manager ACME
		"Challenge",                    // cert-manager ACME
		"GitRepository",                // FluxCD source
		"OCIRepository",                // FluxCD source
		"HelmRepository",               // FluxCD source
		"Kustomization",                // FluxCD kustomize
		"HelmRelease",                  // FluxCD helm
		"Alert",                        // FluxCD notification
		"ApplicationSet",               // ArgoCD
		"AppProject",                   // ArgoCD
		"Gateway",                      // Gateway API
		"HTTPRoute",                    // Gateway API
		"GRPCRoute",                    // Gateway API
		"TCPRoute",                     // Gateway API
		"TLSRoute",                     // Gateway API
		"VulnerabilityReport",          // Trivy Operator
		"ConfigAuditReport",            // Trivy Operator
		"ExposedSecretReport",          // Trivy Operator
		"RbacAssessmentReport",         // Trivy Operator
		"ClusterRbacAssessmentReport",  // Trivy Operator
		"ClusterComplianceReport",      // Trivy Operator
		"SbomReport",                   // Trivy Operator
		"ClusterSbomReport",            // Trivy Operator
		"InfraAssessmentReport",        // Trivy Operator
		"ClusterInfraAssessmentReport", // Trivy Operator
		"NodePool",                     // Karpenter
		"NodeClaim",                    // Karpenter
		"EC2NodeClass",                 // Karpenter (AWS)
		"AKSNodeClass",                 // Karpenter (Azure)
		"GCPNodeClass",                 // Karpenter (GCP)
		"ScaledObject",                 // KEDA
		"ScaledJob",                    // KEDA
		"TriggerAuthentication",        // KEDA
		"ClusterTriggerAuthentication", // KEDA
		"GatewayClass",                 // Gateway API
		"VerticalPodAutoscaler",        // VPA
		"ServiceMonitor",               // Prometheus Operator
		"PodMonitor",                   // Prometheus Operator
		"PrometheusRule",               // Prometheus Operator
		"Alertmanager",                 // Prometheus Operator
		"Revision",                     // KNative Serving
		"DomainMapping",                // KNative Serving
		"ServerlessService",            // KNative Serving (internal)
		"Trigger",                      // KNative Eventing
		"EventType",                    // KNative Eventing
		"InMemoryChannel",              // KNative Messaging
		"Subscription",                 // KNative Messaging
		"ApiServerSource",              // KNative Sources
		"ContainerSource",              // KNative Sources
		"PingSource",                   // KNative Sources
		"SinkBinding",                  // KNative Sources
		"Sequence",                     // KNative Flows
		"Parallel",                     // KNative Flows
		"IngressRoute",                 // Traefik
		"IngressRouteTCP",              // Traefik
		"IngressRouteUDP",              // Traefik
		"Middleware",                    // Traefik
		"MiddlewareTCP",                // Traefik
		"TraefikService",               // Traefik
		"ServersTransport",             // Traefik
		"ServersTransportTCP",          // Traefik
		"TLSOption",                    // Traefik
		"TLSStore",                     // Traefik
		"HTTPProxy",                    // Contour
	}

	var gvrs []schema.GroupVersionResource
	for _, kind := range commonCRDs {
		if gvr, ok := discovery.GetGVR(kind); ok {
			gvrs = append(gvrs, gvr)
			log.Printf("Warming up CRD: %s", kind)
		}
	}

	// ArgoCD Application needs group-qualified lookup
	if gvr, ok := discovery.GetGVRWithGroup("Application", "argoproj.io"); ok {
		gvrs = append(gvrs, gvr)
		log.Printf("Warming up CRD: Application (argoproj.io)")
	}

	// Istio kinds that have topology edges (VirtualService→Service, Gateway→VirtualService, DestinationRule→Service)
	istioQualified := []struct{ kind, group string }{
		{"VirtualService", "networking.istio.io"},
		{"DestinationRule", "networking.istio.io"},
		{"Gateway", "networking.istio.io"},
	}
	for _, iq := range istioQualified {
		if gvr, ok := discovery.GetGVRWithGroup(iq.kind, iq.group); ok {
			gvrs = append(gvrs, gvr)
			log.Printf("Warming up CRD: %s (%s)", iq.kind, iq.group)
		}
	}

	// KNative kinds that collide with core/other CRDs
	knativeQualified := []struct{ kind, group string }{
		{"Service", "serving.knative.dev"},
		{"Ingress", "networking.internal.knative.dev"},
		{"Certificate", "networking.internal.knative.dev"},
		{"Channel", "messaging.knative.dev"},
		{"Configuration", "serving.knative.dev"},
		{"Route", "serving.knative.dev"},
		{"Broker", "eventing.knative.dev"},
	}
	for _, kq := range knativeQualified {
		if gvr, ok := discovery.GetGVRWithGroup(kq.kind, kq.group); ok {
			gvrs = append(gvrs, gvr)
			log.Printf("Warming up CRD: %s (%s)", kq.kind, kq.group)
		}
	}

	if len(gvrs) > 0 {
		cache.WarmupParallel(gvrs, 10*time.Second)
	}
}
