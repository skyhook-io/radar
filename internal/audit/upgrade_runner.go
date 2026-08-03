package audit

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

// UpgradeReadinessOptions carries evidence that is not available from the
// typed informer cache. A nil slice means the collector could not read that
// source; an empty non-nil slice means it was inspected and had no matches.
type UpgradeReadinessOptions struct {
	CurrentVersion                      string
	TargetVersion                       string
	Platform                            string
	ManifestResources                   []upgradereadiness.ManifestResource
	HelmUnavailableNamespaces           []string
	HelmScopedNamespaces                []string
	ManifestParseErrors                 int
	DeprecatedAPIRequests               []upgradereadiness.DeprecatedAPIRequest
	DeprecatedAPIMetricsWindow          string
	PrometheusRules                     []*unstructured.Unstructured
	PrometheusRulesInstalled            bool
	PrometheusRulesDiscoveryAvailable   bool
	PrometheusRuleUnavailableNamespaces []string
	CanReadNodes                        bool
	CanReadPersistentVolumes            bool
	SourceObjects                       []metav1.Object
	AdmissionWebhookConfigurations      []*unstructured.Unstructured
	AdmissionWebhookUnavailableKinds    []string
	CustomResourceDefinitions           []*unstructured.Unstructured
	APIServices                         []*unstructured.Unstructured
	EndpointSlices                      []*discoveryv1.EndpointSlice
	NodeRuntimeEvidence                 []upgradereadiness.NodeRuntimeEvidence
	WebhookServices                     []*corev1.Service
	SourceObjectUnavailableKinds        []string
}

func RunUpgradeReadinessFromCache(cache *k8s.ResourceCache, namespaces []string, opts UpgradeReadinessOptions) (*upgradereadiness.ScanResults, error) {
	// Source manifest evidence must come from the direct collector: informer
	// transforms intentionally remove kubectl's last-applied annotation.
	input := &upgradereadiness.Input{
		Namespaces:                          cloneStrings(namespaces),
		EndpointSlices:                      opts.EndpointSlices,
		WebhookServices:                     opts.WebhookServices,
		AdmissionWebhookConfigurations:      opts.AdmissionWebhookConfigurations,
		AdmissionWebhookUnavailableKinds:    opts.AdmissionWebhookUnavailableKinds,
		CustomResourceDefinitions:           opts.CustomResourceDefinitions,
		APIServices:                         opts.APIServices,
		NodeRuntimeEvidence:                 opts.NodeRuntimeEvidence,
		SourceObjects:                       opts.SourceObjects,
		SourceObjectUnavailableKinds:        opts.SourceObjectUnavailableKinds,
		ManifestResources:                   opts.ManifestResources,
		HelmUnavailableNamespaces:           opts.HelmUnavailableNamespaces,
		HelmScopedNamespaces:                opts.HelmScopedNamespaces,
		ManifestParseErrors:                 opts.ManifestParseErrors,
		DeprecatedAPIRequests:               opts.DeprecatedAPIRequests,
		DeprecatedAPIMetricsWindow:          opts.DeprecatedAPIMetricsWindow,
		PrometheusRules:                     opts.PrometheusRules,
		PrometheusRulesInstalled:            opts.PrometheusRulesInstalled,
		PrometheusRulesDiscoveryAvailable:   opts.PrometheusRulesDiscoveryAvailable,
		PrometheusRuleUnavailableNamespaces: opts.PrometheusRuleUnavailableNamespaces,
		Platform:                            opts.Platform,
	}
	if cache == nil {
		return upgradereadiness.Scan(input, opts.CurrentVersion, opts.TargetVersion)
	}

	typed := collectTypedInput(cache, namespaces)
	replicaSets := listNamespaced(cache.ReplicaSets(), namespaces)
	var persistentVolumes []*corev1.PersistentVolume
	if opts.CanReadPersistentVolumes {
		persistentVolumes = filterPersistentVolumesForNamespaces(listNamespaced(cache.PersistentVolumes(), nil), namespaces)
	}
	var nodes []*corev1.Node
	if opts.CanReadNodes {
		nodes = listNamespaced(cache.Nodes(), namespaces)
	}
	events := listNamespaced(cache.Events(), namespaces)

	input.CacheScopedKinds = upgradeCacheScopedKinds(cache, namespaces)
	input.Pods = typed.Pods
	input.Deployments = typed.Deployments
	input.ReplicaSets = replicaSets
	input.StatefulSets = typed.StatefulSets
	input.DaemonSets = typed.DaemonSets
	input.Jobs = typed.Jobs
	input.CronJobs = typed.CronJobs
	input.Services = typed.Services
	input.PersistentVolumes = persistentVolumes
	input.Nodes = nodes
	input.Events = events
	input.PodDisruptionBudgets = typed.PodDisruptionBudgets
	return upgradereadiness.Scan(input, opts.CurrentVersion, opts.TargetVersion)
}

func upgradeCacheScopedKinds(cache *k8s.ResourceCache, scanNamespaces []string) map[string][]string {
	if cache == nil {
		return nil
	}
	resources := []string{
		string(k8score.Pods), string(k8score.Deployments), string(k8score.ReplicaSets),
		string(k8score.StatefulSets), string(k8score.DaemonSets), string(k8score.Jobs),
		string(k8score.CronJobs), string(k8score.Services), string(k8score.Events),
		string(k8score.PodDisruptionBudgets),
	}
	limited := map[string][]string{}
	for _, resource := range resources {
		if namespaces := cache.KindNamespaces(resource); len(namespaces) > 0 && !sameNamespaceSet(namespaces, scanNamespaces) {
			limited[resource] = namespaces
		}
	}
	if len(limited) == 0 {
		return nil
	}
	return limited
}

func sameNamespaceSet(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func filterPersistentVolumesForNamespaces(volumes []*corev1.PersistentVolume, namespaces []string) []*corev1.PersistentVolume {
	if namespaces == nil {
		return volumes
	}
	allowed := make(map[string]bool, len(namespaces))
	for _, namespace := range namespaces {
		allowed[namespace] = true
	}
	filtered := make([]*corev1.PersistentVolume, 0, len(volumes))
	for _, volume := range volumes {
		if volume != nil && volume.Spec.ClaimRef != nil && allowed[volume.Spec.ClaimRef.Namespace] {
			filtered = append(filtered, volume)
		}
	}
	return filtered
}
