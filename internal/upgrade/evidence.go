package upgrade

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
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
	CanList(group, resource, namespace string) EvidenceAuthorizationDecision
	// CanGetSubresource authorizes a cluster-scoped subresource get
	// (nodes/proxy for kubelet metrics and effective configuration).
	CanGetSubresource(group, resource, subresource string) EvidenceAuthorizationDecision
	// FilterNamespacesByCanList returns the subset of namespaces where the
	// identity can list the resource.
	FilterNamespacesByCanList(group, resource string, namespaces []string) []string
}

// EvidenceAuthorizationDecision keeps an explicit denial distinct from an
// authorization check that could not reach an authoritative result.
type EvidenceAuthorizationDecision struct {
	Allowed       bool
	Authoritative bool
}

func canListEvidence(authz EvidenceAuthorizer, group, resource, namespace string) bool {
	return authz.CanList(group, resource, namespace).Allowed
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
		} else if !canListEvidence(authz, "", "secrets", "") {
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

func resolveCachedEvidenceNamespaces(authz EvidenceAuthorizer, group, resource string, namespaces []string) []string {
	if noNamespaceAccess(namespaces) {
		return []string{}
	}
	if canListEvidence(authz, group, resource, "") {
		return cloneStrings(namespaces)
	}
	candidates := namespaces
	if candidates == nil {
		candidates = k8s.AllNamespaceNames()
	}
	allowed := authz.FilterNamespacesByCanList(group, resource, candidates)
	if allowed == nil {
		return []string{}
	}
	return cloneStrings(allowed)
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
	resolvedTarget, err := upgradereadiness.EffectiveTarget(currentVersion, targetVersion)
	if err != nil {
		return nil, err
	}
	targetVersion = resolvedTarget
	collect137Evidence, err := upgradereadiness.UpgradePathIncludesRelease(currentVersion, targetVersion, "1.37")
	if err != nil {
		return nil, err
	}
	collect135Evidence, err := upgradereadiness.UpgradePathIncludesRelease(currentVersion, targetVersion, "1.35")
	if err != nil {
		return nil, err
	}
	collectKubeProxyModeEvidence, err := upgradereadiness.UpgradePathIncludesKubeProxyModeTransition(currentVersion, targetVersion)
	if err != nil {
		return nil, err
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
	var admissionConfigDeniedKinds []string
	var apiServices []*unstructured.Unstructured
	var endpointSlices []*discoveryv1.EndpointSlice
	var additionalServices []*corev1.Service
	var nodeRuntimeEvidence []upgradereadiness.NodeRuntimeEvidence
	var nodeProxyForbidden bool
	var csiDrivers []*storagev1.CSIDriver
	var schedulingV1Alpha2Objects []*unstructured.Unstructured
	var schedulingV1Alpha2UnavailableKinds []string
	var schedulingV1Alpha2Installed, schedulingV1Alpha2DiscoveryAvailable bool
	configMapNamespaces := cloneStrings(namespaces)
	persistentVolumeClaimNamespaces := cloneStrings(namespaces)
	eventNamespaces := cloneStrings(namespaces)
	canReadNodes := !noAccess && canListEvidence(authz, "", "nodes", "")
	nodeProxyDecision := EvidenceAuthorizationDecision{}
	if !noAccess {
		nodeProxyDecision = authz.CanGetSubresource("", "nodes", "proxy")
	}
	canGetNodeProxy := nodeProxyDecision.Allowed
	nodeProxyForbidden = canReadNodes && nodeProxyDecision.Authoritative && !nodeProxyDecision.Allowed
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
		admissionConfigs, admissionConfigUnavailableKinds, admissionConfigDeniedKinds, crds, endpointSlices, additionalServices = collectUpgradeWebhookEvidence(ctx, authz)
		apiServices = collectUpgradeAPIServices(ctx, authz)
		if collect137Evidence {
			csiDrivers = collectUpgradeCSIDrivers(ctx, authz)
			schedulingV1Alpha2Objects, schedulingV1Alpha2Installed, schedulingV1Alpha2DiscoveryAvailable, schedulingV1Alpha2UnavailableKinds = collectSchedulingV1Alpha2Evidence(ctx, authz, namespaces)
			persistentVolumeClaimNamespaces = resolveCachedEvidenceNamespaces(authz, "", "persistentvolumeclaims", namespaces)
		}
		if collect137Evidence || collectKubeProxyModeEvidence {
			configMapNamespaces = resolveCachedEvidenceNamespaces(authz, "", "configmaps", namespaces)
		}
		if collect135Evidence || collect137Evidence {
			eventNamespaces = resolveCachedEvidenceNamespaces(authz, "", "events", namespaces)
		}
		if canReadNodes && canGetNodeProxy && cache.Nodes() != nil {
			nodes, _ := cache.Nodes().List(labels.Everything())
			nodeRuntimeCtx, cancelNodeRuntimeCollection := context.WithTimeout(ctx, upgradeNodeRuntimeCollectionTimeout)
			var observedForbidden bool
			nodeRuntimeEvidence, observedForbidden = collectUpgradeNodeRuntimeEvidence(nodeRuntimeCtx, nodes, collect137Evidence)
			nodeProxyForbidden = nodeProxyForbidden || observedForbidden
			cancelNodeRuntimeCollection()
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	platform, _ := k8s.GetClusterPlatform(ctx)
	results, err := RunFromCache(scanInput, namespaces, Options{
		CurrentVersion:                       currentVersion,
		TargetVersion:                        targetVersion,
		Platform:                             platform,
		ManifestResources:                    manifestResources,
		HelmUnavailableNamespaces:            helmUnavailableNamespaces,
		HelmScopedNamespaces:                 helmScopedNamespaces,
		ManifestParseErrors:                  manifestParseErrors,
		DeprecatedAPIRequests:                deprecatedRequests,
		DeprecatedAPIMetricsWindow:           deprecatedMetricsWindow,
		PrometheusRules:                      prometheusRules,
		PrometheusRulesInstalled:             prometheusInstalled,
		PrometheusRulesDiscoveryAvailable:    discoveryAvailable,
		PrometheusRuleUnavailableNamespaces:  prometheusUnavailableNamespaces,
		ConfigMapNamespaces:                  configMapNamespaces,
		PersistentVolumeClaimNamespaces:      persistentVolumeClaimNamespaces,
		EventNamespaces:                      eventNamespaces,
		CanReadNodes:                         canReadNodes,
		NodeProxyForbidden:                   nodeProxyForbidden,
		CanReadPersistentVolumes:             !noAccess && canListEvidence(authz, "", "persistentvolumes", ""),
		SourceObjects:                        sourceObjects,
		SourceObjectUnavailableKinds:         sourceObjectUnavailableKinds,
		AdmissionWebhookConfigurations:       admissionConfigs,
		AdmissionWebhookUnavailableKinds:     admissionConfigUnavailableKinds,
		AdmissionWebhookDeniedKinds:          admissionConfigDeniedKinds,
		CustomResourceDefinitions:            crds,
		APIServices:                          apiServices,
		EndpointSlices:                       endpointSlices,
		WebhookServices:                      additionalServices,
		NodeRuntimeEvidence:                  nodeRuntimeEvidence,
		CSIDrivers:                           csiDrivers,
		SchedulingV1Alpha2Objects:            schedulingV1Alpha2Objects,
		SchedulingV1Alpha2Installed:          schedulingV1Alpha2Installed,
		SchedulingV1Alpha2DiscoveryAvailable: schedulingV1Alpha2DiscoveryAvailable,
		SchedulingV1Alpha2UnavailableKinds:   schedulingV1Alpha2UnavailableKinds,
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
