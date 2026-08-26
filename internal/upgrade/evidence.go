package upgrade

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

// EvidenceAuthorizer is the seam through which upgrade-readiness
// evidence collection asks authorization questions, so the HTTP handler and
// the MCP tool produce identical scans from identical decisions. Identity for
// impersonated reads (Helm, dynamic lists, kubelet metrics) is NOT part of the
// interface: both surfaces attach the user to ctx via the shared
// pkg/auth context key, and the collectors read it from there
// (k8s.ClientFromContext and friends).
type EvidenceAuthorizer interface {
	// Namespaces returns the identity's namespace ceiling for the scan:
	// nil means cluster-wide, a non-nil empty slice means no namespaced
	// access. Implementations must ignore the browsing namespace picker —
	// an upgrade affects the cluster (see upgradeReadinessNamespaces).
	Namespaces() []string
	// CanList authorizes a single list. namespace "" is a cluster-scoped
	// check.
	CanList(group, resource, namespace string) bool
	// CanGetSubresource authorizes a cluster-scoped subresource get
	// (nodes/proxy for kubelet metrics).
	CanGetSubresource(group, resource, subresource string) bool
	// FilterNamespacesByCanList returns the subset of namespaces where the
	// identity can list the resource.
	FilterNamespacesByCanList(group, resource string, namespaces []string) []string
}

// noNamespaceAccess returns true when a namespace filter explicitly grants no
// access (non-nil empty slice from auth filtering). Mirrors the server-side
// helper of the same name.
func noNamespaceAccess(namespaces []string) bool {
	return namespaces != nil && len(namespaces) == 0
}

// ResolveHelmNamespaces applies Helm's Secret-specific RBAC and
// no-auth fallback behavior to an already resolved workload namespace scope.
// Shared by Server.resolveHelmNamespacesForScope (HTTP) and the upgrade scan
// (both surfaces via the authorizer seam).
func ResolveHelmNamespaces(ctx context.Context, authz EvidenceAuthorizer, namespaces []string) ([]string, bool) {
	if noNamespaceAccess(namespaces) {
		return nil, false
	}
	if namespaces == nil {
		if auth.UserFromContext(ctx) == nil {
			// "All namespaces" in no-auth mode. A namespace-restricted
			// ServiceAccount can't list cluster-wide; resolve to the namespaces
			// it can actually see so the Helm list degrades gracefully instead
			// of 403-ing. Authenticated users are handled below; Helm lists
			// impersonate them directly, so narrowing them with the backend
			// client's fallback namespaces would under-list users whose RBAC is
			// wider than Radar's own ServiceAccount.
			if fallback := helm.ResolveNoAuthListNamespaces(ctx); len(fallback) > 0 {
				return fallback, true
			}
		} else if !authz.CanList("", "secrets", "") {
			// Authenticated user with cluster-wide pod access but NOT
			// cluster-wide `list secrets`. Helm storage is Secrets, so a single
			// cluster-wide list would 403 wholesale and blank the view. Resolve
			// to the namespaces where the user CAN list secrets — a
			// per-namespace SAR memoized on the user's perms (2-min TTL), so
			// repeat scans don't re-probe. Falls through to the cluster-wide
			// path (→ honest 403) when they can't read secrets anywhere.
			if allowed := authz.FilterNamespacesByCanList("", "secrets", k8s.AllNamespaceNames()); len(allowed) > 0 {
				return allowed, true
			}
		}
	}
	return namespaces, true
}

// runUpgradeReadinessScan collects every evidence source under the
// authorizer's decisions and runs the scan. The body is the extracted
// evidence-collection block of handleUpgradeReadiness; behavior must stay
// byte-for-byte identical for the HTTP surface.
func runUpgradeReadinessScan(ctx context.Context, authz EvidenceAuthorizer, namespaces []string, currentVersion, targetVersion string) (*upgradereadiness.ScanResults, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, ErrScanNotReady
	}
	noAccess := noNamespaceAccess(namespaces)
	var scanInput *k8s.ResourceCache
	if !noAccess {
		scanInput = cache
	}

	var manifestResources []upgradereadiness.ManifestResource
	var helmUnavailableNamespaces []string
	var helmScopedNamespaces []string
	var manifestParseErrors int
	var deprecatedRequests []upgradereadiness.DeprecatedAPIRequest
	var deprecatedMetricsWindow string
	var prometheusRules []*unstructured.Unstructured
	var prometheusUnavailableNamespaces []string
	var prometheusInstalled, discoveryAvailable bool
	var sourceObjects []metav1.Object
	var sourceObjectUnavailableKinds []string
	var admissionConfigs, crds []*unstructured.Unstructured
	var admissionConfigUnavailableKinds []string
	var apiServices []*unstructured.Unstructured
	var endpointSlices []*discoveryv1.EndpointSlice
	var additionalServices []*corev1.Service
	var nodeRuntimeEvidence []upgradereadiness.NodeRuntimeEvidence
	canReadNodes := !noAccess && authz.CanList("", "nodes", "")
	if !noAccess {
		if helmNamespaces, ok := ResolveHelmNamespaces(ctx, authz, namespaces); ok {
			manifestResources, helmUnavailableNamespaces, manifestParseErrors = collectUpgradeHelmManifests(ctx, helmNamespaces)
			if !sameNamespaceScope(namespaces, helmNamespaces) {
				helmScopedNamespaces = make([]string, len(helmNamespaces))
				copy(helmScopedNamespaces, helmNamespaces)
			}
		}
		deprecatedRequests, deprecatedMetricsWindow = collectDeprecatedAPIRequests(ctx)
		prometheusRules, prometheusInstalled, discoveryAvailable, prometheusUnavailableNamespaces = collectUpgradePrometheusRules(ctx, authz, namespaces)
		sourceObjectCtx, cancelSourceObjectCollection := context.WithTimeout(ctx, upgradeSourceObjectCollectionTimeout)
		sourceObjects, sourceObjectUnavailableKinds = collectUpgradeSourceObjects(sourceObjectCtx, namespaces)
		cancelSourceObjectCollection()
		admissionConfigs, admissionConfigUnavailableKinds, crds, endpointSlices, additionalServices = collectUpgradeWebhookEvidence(ctx, authz)
		apiServices = collectUpgradeAPIServices(ctx, authz)
		if canReadNodes && authz.CanGetSubresource("", "nodes", "proxy") && cache.Nodes() != nil {
			nodes, _ := cache.Nodes().List(labels.Everything())
			nodeRuntimeCtx, cancelNodeRuntimeCollection := context.WithTimeout(ctx, upgradeNodeRuntimeCollectionTimeout)
			nodeRuntimeEvidence = collectUpgradeNodeRuntimeEvidence(nodeRuntimeCtx, nodes)
			cancelNodeRuntimeCollection()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	platform, _ := k8s.GetClusterPlatform(ctx)
	results, err := RunFromCache(scanInput, namespaces, Options{
		CurrentVersion:                      currentVersion,
		TargetVersion:                       targetVersion,
		Platform:                            platform,
		ManifestResources:                   manifestResources,
		HelmUnavailableNamespaces:           helmUnavailableNamespaces,
		HelmScopedNamespaces:                helmScopedNamespaces,
		ManifestParseErrors:                 manifestParseErrors,
		DeprecatedAPIRequests:               deprecatedRequests,
		DeprecatedAPIMetricsWindow:          deprecatedMetricsWindow,
		PrometheusRules:                     prometheusRules,
		PrometheusRulesInstalled:            prometheusInstalled,
		PrometheusRulesDiscoveryAvailable:   discoveryAvailable,
		PrometheusRuleUnavailableNamespaces: prometheusUnavailableNamespaces,
		CanReadNodes:                        canReadNodes,
		CanReadPersistentVolumes:            !noAccess && authz.CanList("", "persistentvolumes", ""),
		SourceObjects:                       sourceObjects,
		SourceObjectUnavailableKinds:        sourceObjectUnavailableKinds,
		AdmissionWebhookConfigurations:      admissionConfigs,
		AdmissionWebhookUnavailableKinds:    admissionConfigUnavailableKinds,
		CustomResourceDefinitions:           crds,
		APIServices:                         apiServices,
		EndpointSlices:                      endpointSlices,
		WebhookServices:                     additionalServices,
		NodeRuntimeEvidence:                 nodeRuntimeEvidence,
	})
	if err != nil {
		return nil, err
	}
	if noAccess {
		results.Coverage.State = "no_access"
		results.Coverage.UnavailableKinds = nil
	}
	return results, nil
}
