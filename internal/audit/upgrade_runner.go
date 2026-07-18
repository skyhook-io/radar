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
	EndpointSlices                      []*discoveryv1.EndpointSlice
	NodeRuntimeEvidence                 []upgradereadiness.NodeRuntimeEvidence
	AdditionalServices                  []*corev1.Service
	SourceObjectUnavailableKinds        []string
}

func RunUpgradeReadinessFromCache(cache *k8s.ResourceCache, namespaces []string, opts UpgradeReadinessOptions) (*upgradereadiness.ScanResults, error) {
	if cache == nil {
		return upgradereadiness.Scan(nil, opts.CurrentVersion, opts.TargetVersion)
	}

	typed := collectTypedInput(cache, namespaces)
	replicaSets := listNamespaced(cache.ReplicaSets(), namespaces)
	var persistentVolumes []*corev1.PersistentVolume
	if opts.CanReadPersistentVolumes {
		persistentVolumes = listNamespaced(cache.PersistentVolumes(), namespaces)
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

	services := append([]*corev1.Service(nil), typed.Services...)
	services = append(services, opts.AdditionalServices...)
	return upgradereadiness.Scan(&upgradereadiness.Input{
		Namespaces:                          append([]string(nil), namespaces...),
		Pods:                                typed.Pods,
		Deployments:                         typed.Deployments,
		ReplicaSets:                         replicaSets,
		StatefulSets:                        typed.StatefulSets,
		DaemonSets:                          typed.DaemonSets,
		Jobs:                                typed.Jobs,
		CronJobs:                            typed.CronJobs,
		Services:                            services,
		PersistentVolumes:                   persistentVolumes,
		Nodes:                               nodes,
		Events:                              events,
		PodDisruptionBudgets:                typed.PodDisruptionBudgets,
		EndpointSlices:                      opts.EndpointSlices,
		AdmissionWebhookConfigurations:      opts.AdmissionWebhookConfigurations,
		CustomResourceDefinitions:           opts.CustomResourceDefinitions,
		NodeRuntimeEvidence:                 opts.NodeRuntimeEvidence,
		SourceObjects:                       sourceObjects,
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
	}, opts.CurrentVersion, opts.TargetVersion)
}
