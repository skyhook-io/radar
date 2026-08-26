package upgrade

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

// Options carries evidence that is not available from the
// typed informer cache. A nil slice means the collector could not read that
// source; an empty non-nil slice means it was inspected and had no matches.
type Options struct {
	CurrentVersion                       string
	TargetVersion                        string
	Platform                             string
	ManifestResources                    []upgradereadiness.ManifestResource
	HelmUnavailableNamespaces            []string
	HelmScopedNamespaces                 []string
	ManifestParseErrors                  int
	DeprecatedAPIRequests                []upgradereadiness.DeprecatedAPIRequest
	DeprecatedAPIMetricsWindow           string
	PrometheusRules                      []*unstructured.Unstructured
	PrometheusRulesInstalled             bool
	PrometheusRulesDiscoveryAvailable    bool
	PrometheusRuleUnavailableNamespaces  []string
	CanReadNodes                         bool
	CanReadPersistentVolumes             bool
	SourceObjects                        []metav1.Object
	AdmissionWebhookConfigurations       []*unstructured.Unstructured
	AdmissionWebhookUnavailableKinds     []string
	CustomResourceDefinitions            []*unstructured.Unstructured
	APIServices                          []*unstructured.Unstructured
	EndpointSlices                       []*discoveryv1.EndpointSlice
	NodeRuntimeEvidence                  []upgradereadiness.NodeRuntimeEvidence
	WebhookServices                      []*corev1.Service
	SourceObjectUnavailableKinds         []string
	CSIDrivers                           []*storagev1.CSIDriver
	SchedulingV1Alpha2Objects            []*unstructured.Unstructured
	SchedulingV1Alpha2Installed          bool
	SchedulingV1Alpha2DiscoveryAvailable bool
	SchedulingV1Alpha2UnavailableKinds   []string
}

func RunFromCache(cache *k8s.ResourceCache, namespaces []string, opts Options) (*upgradereadiness.ScanResults, error) {
	// Source manifest evidence must come from the direct collector: informer
	// transforms intentionally remove kubectl's last-applied annotation.
	input := &upgradereadiness.Input{
		Namespaces:                           cloneStrings(namespaces),
		EndpointSlices:                       opts.EndpointSlices,
		WebhookServices:                      opts.WebhookServices,
		AdmissionWebhookConfigurations:       opts.AdmissionWebhookConfigurations,
		AdmissionWebhookUnavailableKinds:     opts.AdmissionWebhookUnavailableKinds,
		CustomResourceDefinitions:            opts.CustomResourceDefinitions,
		APIServices:                          opts.APIServices,
		NodeRuntimeEvidence:                  opts.NodeRuntimeEvidence,
		SourceObjects:                        opts.SourceObjects,
		SourceObjectUnavailableKinds:         opts.SourceObjectUnavailableKinds,
		ManifestResources:                    opts.ManifestResources,
		HelmUnavailableNamespaces:            opts.HelmUnavailableNamespaces,
		HelmScopedNamespaces:                 opts.HelmScopedNamespaces,
		ManifestParseErrors:                  opts.ManifestParseErrors,
		DeprecatedAPIRequests:                opts.DeprecatedAPIRequests,
		DeprecatedAPIMetricsWindow:           opts.DeprecatedAPIMetricsWindow,
		PrometheusRules:                      opts.PrometheusRules,
		PrometheusRulesInstalled:             opts.PrometheusRulesInstalled,
		PrometheusRulesDiscoveryAvailable:    opts.PrometheusRulesDiscoveryAvailable,
		PrometheusRuleUnavailableNamespaces:  opts.PrometheusRuleUnavailableNamespaces,
		Platform:                             opts.Platform,
		CSIDrivers:                           opts.CSIDrivers,
		SchedulingV1Alpha2Objects:            opts.SchedulingV1Alpha2Objects,
		SchedulingV1Alpha2Installed:          opts.SchedulingV1Alpha2Installed,
		SchedulingV1Alpha2DiscoveryAvailable: opts.SchedulingV1Alpha2DiscoveryAvailable,
		SchedulingV1Alpha2UnavailableKinds:   opts.SchedulingV1Alpha2UnavailableKinds,
	}
	if cache == nil {
		return upgradereadiness.Scan(input, opts.CurrentVersion, opts.TargetVersion)
	}

	typed := audit.CollectTypedInput(cache, namespaces)
	replicaSets := audit.ListNamespaced(cache.ReplicaSets(), namespaces)
	var persistentVolumes []*corev1.PersistentVolume
	if opts.CanReadPersistentVolumes {
		persistentVolumes = filterPersistentVolumesForNamespaces(audit.ListNamespaced(cache.PersistentVolumes(), nil), namespaces)
	}
	var nodes []*corev1.Node
	if opts.CanReadNodes {
		nodes = audit.ListNamespaced(cache.Nodes(), namespaces)
	}
	events := audit.ListNamespaced(cache.Events(), namespaces)

	input.CacheScopedKinds = upgradeCacheScopedKinds(cache, namespaces, opts.CurrentVersion, opts.TargetVersion)
	input.Pods = typed.Pods
	input.Deployments = typed.Deployments
	input.ReplicaSets = replicaSets
	input.StatefulSets = typed.StatefulSets
	input.DaemonSets = typed.DaemonSets
	input.Jobs = typed.Jobs
	input.CronJobs = typed.CronJobs
	input.Services = typed.Services
	input.ConfigMaps = typed.ConfigMaps
	input.PersistentVolumeClaims = audit.ListNamespaced(cache.PersistentVolumeClaims(), namespaces)
	input.PersistentVolumes = persistentVolumes
	input.Nodes = nodes
	input.Events = events
	input.PodDisruptionBudgets = typed.PodDisruptionBudgets
	return upgradereadiness.Scan(input, opts.CurrentVersion, opts.TargetVersion)
}

func upgradeCacheScopedKinds(cache *k8s.ResourceCache, scanNamespaces []string, currentVersion, targetVersion string) map[string][]string {
	if cache == nil {
		return nil
	}
	resources := upgradeCacheScopeResources(currentVersion, targetVersion)
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

func upgradeCacheScopeResources(currentVersion, targetVersion string) []string {
	resources := []string{
		string(k8score.Pods), string(k8score.Deployments), string(k8score.ReplicaSets),
		string(k8score.StatefulSets), string(k8score.DaemonSets), string(k8score.Jobs),
		string(k8score.CronJobs), string(k8score.Services), string(k8score.PodDisruptionBudgets),
	}
	crosses135, _ := upgradereadiness.UpgradePathIncludesRelease(currentVersion, targetVersion, "1.35")
	crosses137, _ := upgradereadiness.UpgradePathIncludesRelease(currentVersion, targetVersion, "1.37")
	if crosses135 || crosses137 {
		resources = append(resources, string(k8score.Events))
	}
	if crosses137 {
		resources = append(resources, string(k8score.ConfigMaps), string(k8score.PersistentVolumeClaims))
	}
	return resources
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
