package audit

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
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
	CustomResourceDefinitions           []*unstructured.Unstructured
	APIServices                         []*unstructured.Unstructured
	EndpointSlices                      []*discoveryv1.EndpointSlice
	NodeRuntimeEvidence                 []upgradereadiness.NodeRuntimeEvidence
	AdditionalServices                  []*corev1.Service
	SourceObjectUnavailableKinds        []string
}

func RunUpgradeReadinessFromCache(cache *k8s.ResourceCache, namespaces []string, opts UpgradeReadinessOptions) (*upgradereadiness.ScanResults, error) {
	input := &upgradereadiness.Input{
		Namespaces:                          cloneStrings(namespaces),
		EndpointSlices:                      opts.EndpointSlices,
		AdmissionWebhookConfigurations:      opts.AdmissionWebhookConfigurations,
		CustomResourceDefinitions:           opts.CustomResourceDefinitions,
		APIServices:                         opts.APIServices,
		NodeRuntimeEvidence:                 opts.NodeRuntimeEvidence,
		SourceObjects:                       opts.SourceObjects,
		SourceObjectUnavailableKinds:        opts.SourceObjectUnavailableKinds,
		ManifestResources:                   opts.ManifestResources,
		HelmUnavailableNamespaces:           opts.HelmUnavailableNamespaces,
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
	sourceObjects := make([]metav1.Object, 0,
		len(typed.Pods)+len(typed.Deployments)+len(replicaSets)+len(typed.StatefulSets)+
			len(typed.DaemonSets)+len(typed.Jobs)+len(typed.CronJobs)+len(typed.Services)+
			len(typed.Ingresses)+len(typed.HorizontalPodAutoscalers)+len(typed.PodDisruptionBudgets))
	for _, object := range typed.Pods {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.Deployments {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range replicaSets {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.StatefulSets {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.DaemonSets {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.Jobs {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.CronJobs {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.Services {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.Ingresses {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.HorizontalPodAutoscalers {
		sourceObjects = append(sourceObjects, object)
	}
	for _, object := range typed.PodDisruptionBudgets {
		sourceObjects = append(sourceObjects, object)
	}
	if opts.SourceObjects != nil {
		sourceObjects = opts.SourceObjects
	}
	events := listNamespaced(cache.Events(), namespaces)

	services := mergeServices(typed.Services, opts.AdditionalServices)
	input.Pods = typed.Pods
	input.Deployments = typed.Deployments
	input.ReplicaSets = replicaSets
	input.StatefulSets = typed.StatefulSets
	input.DaemonSets = typed.DaemonSets
	input.Jobs = typed.Jobs
	input.CronJobs = typed.CronJobs
	input.Services = services
	input.PersistentVolumes = persistentVolumes
	input.Nodes = nodes
	input.Events = events
	input.PodDisruptionBudgets = typed.PodDisruptionBudgets
	input.SourceObjects = sourceObjects
	return upgradereadiness.Scan(input, opts.CurrentVersion, opts.TargetVersion)
}

func mergeServices(groups ...[]*corev1.Service) []*corev1.Service {
	var merged []*corev1.Service
	seen := map[string]bool{}
	for _, services := range groups {
		for _, service := range services {
			if service == nil {
				continue
			}
			key := service.Namespace + "/" + service.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, service)
		}
	}
	return merged
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
