package server

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

func (s *Server) handleUpgradeReadiness(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Cache not initialized")
		return
	}

	namespaces := s.upgradeReadinessNamespaces(r)
	noAccess := noNamespaceAccess(namespaces)
	var scanInput *k8s.ResourceCache
	if !noAccess {
		scanInput = cache
	}

	var manifestResources []upgradereadiness.ManifestResource
	var helmUnavailableNamespaces []string
	var manifestParseErrors int
	var deprecatedRequests []upgradereadiness.DeprecatedAPIRequest
	var deprecatedMetricsWindow string
	var prometheusRules []*unstructured.Unstructured
	var prometheusUnavailableNamespaces []string
	var prometheusInstalled, discoveryAvailable bool
	var sourceObjects []metav1.Object
	var sourceObjectUnavailableKinds []string
	var admissionConfigs, crds []*unstructured.Unstructured
	var apiServices []*unstructured.Unstructured
	var endpointSlices []*discoveryv1.EndpointSlice
	var additionalServices []*corev1.Service
	var nodeRuntimeEvidence []upgradereadiness.NodeRuntimeEvidence
	if !noAccess {
		if helmNamespaces, ok := s.resolveHelmNamespacesForScope(r, namespaces); ok {
			manifestResources, helmUnavailableNamespaces, manifestParseErrors = collectUpgradeHelmManifests(r, helmNamespaces)
		}
		deprecatedRequests, deprecatedMetricsWindow = collectDeprecatedAPIRequests(r)
		prometheusRules, prometheusInstalled, discoveryAvailable, prometheusUnavailableNamespaces = s.collectUpgradePrometheusRules(r, namespaces)
		sourceObjects, sourceObjectUnavailableKinds = collectUpgradeSourceObjects(r, namespaces)
		admissionConfigs, crds, endpointSlices, additionalServices = s.collectUpgradeWebhookEvidence(r)
		apiServices = s.collectUpgradeAPIServices(r)
		if s.canReadSubresource(r, "", "nodes", "proxy", "", "get") && cache.Nodes() != nil {
			nodes, _ := cache.Nodes().List(labels.Everything())
			nodeRuntimeEvidence = collectUpgradeNodeRuntimeEvidence(r.Context(), nodes)
		}
	}
	platform, _ := k8s.GetClusterPlatform(r.Context())
	results, err := audit.RunUpgradeReadinessFromCache(scanInput, namespaces, audit.UpgradeReadinessOptions{
		CurrentVersion:                      k8s.GetServerVersion(),
		TargetVersion:                       r.URL.Query().Get("target"),
		Platform:                            platform,
		ManifestResources:                   manifestResources,
		HelmUnavailableNamespaces:           helmUnavailableNamespaces,
		ManifestParseErrors:                 manifestParseErrors,
		DeprecatedAPIRequests:               deprecatedRequests,
		DeprecatedAPIMetricsWindow:          deprecatedMetricsWindow,
		PrometheusRules:                     prometheusRules,
		PrometheusRulesInstalled:            prometheusInstalled,
		PrometheusRulesDiscoveryAvailable:   discoveryAvailable,
		PrometheusRuleUnavailableNamespaces: prometheusUnavailableNamespaces,
		CanReadNodes:                        !noAccess && s.canRead(r, "", "nodes", "", "list"),
		CanReadPersistentVolumes:            !noAccess && s.canRead(r, "", "persistentvolumes", "", "list"),
		SourceObjects:                       sourceObjects,
		SourceObjectUnavailableKinds:        sourceObjectUnavailableKinds,
		AdmissionWebhookConfigurations:      admissionConfigs,
		CustomResourceDefinitions:           crds,
		APIServices:                         apiServices,
		EndpointSlices:                      endpointSlices,
		AdditionalServices:                  additionalServices,
		NodeRuntimeEvidence:                 nodeRuntimeEvidence,
	})
	if err != nil {
		switch {
		case errors.Is(err, upgradereadiness.ErrInvalidTargetVersion), errors.Is(err, upgradereadiness.ErrNonForwardTarget):
			s.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, upgradereadiness.ErrInvalidCurrentVersion):
			s.writeError(w, http.StatusServiceUnavailable, "Unable to determine the cluster Kubernetes version")
		default:
			s.writeError(w, http.StatusInternalServerError, "Upgrade impact scan failed")
		}
		return
	}
	if noAccess {
		results.Coverage.State = "no_access"
		results.Coverage.UnavailableKinds = nil
	}

	s.writeJSON(w, results)
}

// upgradeReadinessNamespaces intentionally ignores the active namespace picker:
// an upgrade affects the cluster, while the picker is only a browsing filter.
// Authenticated users are still limited to their full RBAC namespace ceiling,
// and --namespace-scope remains an explicit hard boundary on cached evidence.
func (s *Server) upgradeReadinessNamespaces(r *http.Request) []string {
	if k8s.ForceNamespaceScope {
		if namespace := k8s.GetNamespaceScopeTarget(); namespace != "" {
			return s.getUserNamespaces(r, []string{namespace})
		}
		return []string{}
	}
	return s.getUserNamespaces(r, nil)
}

func (s *Server) canReadSubresource(r *http.Request, group, resource, subresource, namespace, verb string) bool {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return true
	}
	client := k8s.GetClient()
	if client == nil {
		return false
	}
	allowed, err := auth.SubjectCanISubresource(r.Context(), client, user.Username, user.Groups, namespace, group, resource, subresource, verb)
	if err != nil {
		log.Printf("[upgrade-impact] authorization failed for %s on %s/%s: %v", verb, resource, subresource, err)
		return false
	}
	return allowed
}

func collectUpgradeHelmManifests(r *http.Request, namespaces []string) ([]upgradereadiness.ManifestResource, []string, int) {
	client := helm.GetClient()
	if client == nil {
		return nil, nil, 0
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(r.Context()); user != nil {
		username, groups = user.Username, user.Groups
	}
	resources, unavailableNamespaces, parseErrors, err := client.ListManifestResourcesAcrossNamespaces(namespaces, username, groups)
	if err != nil {
		if !helm.IsForbiddenError(err) {
			log.Printf("[upgrade-impact] failed to inspect Helm manifests: %v", err)
		}
		return nil, unavailableNamespaces, parseErrors
	}
	result := make([]upgradereadiness.ManifestResource, 0, len(resources))
	for _, resource := range resources {
		result = append(result, upgradereadiness.ManifestResource{
			APIVersion:      resource.Resource.APIVersion,
			Kind:            resource.Resource.Kind,
			Namespace:       resource.Resource.Namespace,
			Name:            resource.Resource.Name,
			Source:          "Helm",
			SourceNamespace: resource.ReleaseNamespace,
			SourceName:      resource.ReleaseName,
			Object:          resource.Object,
		})
	}
	return result, unavailableNamespaces, parseErrors
}

func collectDeprecatedAPIRequests(r *http.Request) ([]upgradereadiness.DeprecatedAPIRequest, string) {
	client := k8s.ClientFromContext(r.Context())
	if client == nil || client.Discovery().RESTClient() == nil {
		return nil, ""
	}
	raw, err := client.Discovery().RESTClient().Get().AbsPath("/metrics").DoRaw(r.Context())
	if err != nil {
		return nil, ""
	}
	requests, processStartedAt, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		log.Printf("[upgrade-impact] failed to parse API server metrics: %v", err)
		return nil, ""
	}
	window := ""
	if !processStartedAt.IsZero() {
		age := time.Since(processStartedAt)
		if age >= 0 {
			window = k8s.FormatAge(age)
		}
	}
	return requests, window
}

func parseDeprecatedAPIRequests(raw []byte) ([]upgradereadiness.DeprecatedAPIRequest, time.Time, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return nil, time.Time{}, err
	}
	var processStartedAt time.Time
	if processFamily := families["process_start_time_seconds"]; processFamily != nil && len(processFamily.Metric) > 0 {
		if startedAt := metricValue(processFamily.GetType(), processFamily.Metric[0]); startedAt > 0 {
			seconds, fraction := math.Modf(startedAt)
			processStartedAt = time.Unix(int64(seconds), int64(fraction*float64(time.Second)))
		}
	}
	family := families["apiserver_requested_deprecated_apis"]
	if family == nil {
		return []upgradereadiness.DeprecatedAPIRequest{}, processStartedAt, nil
	}
	requests := make([]upgradereadiness.DeprecatedAPIRequest, 0, len(family.Metric))
	for _, metric := range family.Metric {
		labels := metricLabels(metric)
		removed := labels["removed_release"]
		version, resource := labels["version"], labels["resource"]
		if removed == "" || version == "" || resource == "" {
			continue
		}
		requests = append(requests, upgradereadiness.DeprecatedAPIRequest{
			Group: labels["group"], Version: version, Resource: resource,
			Subresource: labels["subresource"], RemovedRelease: removed,
			Requests: metricValue(family.GetType(), metric),
		})
	}
	return requests, processStartedAt, nil
}

func metricLabels(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.Label))
	for _, label := range metric.Label {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

func metricValue(metricType dto.MetricType, metric *dto.Metric) float64 {
	switch metricType {
	case dto.MetricType_COUNTER:
		return metric.GetCounter().GetValue()
	case dto.MetricType_GAUGE:
		return metric.GetGauge().GetValue()
	case dto.MetricType_UNTYPED:
		return metric.GetUntyped().GetValue()
	default:
		return 0
	}
}

func (s *Server) collectUpgradePrometheusRules(r *http.Request, namespaces []string) (rules []*unstructured.Unstructured, installed, discoveryAvailable bool, unavailableNamespaces []string) {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return nil, false, false, nil
	}
	discoveryAvailable = true
	gvr, ok := discovery.GetGVRWithGroup("PrometheusRule", "monitoring.coreos.com")
	if !ok {
		return []*unstructured.Unstructured{}, false, true, nil
	}
	if len(namespaces) == 0 {
		if !s.canRead(r, gvr.Group, gvr.Resource, "", "list") {
			return nil, true, true, nil
		}
	} else {
		readable := make([]string, 0, len(namespaces))
		for _, namespace := range namespaces {
			if s.canRead(r, gvr.Group, gvr.Resource, namespace, "list") {
				readable = append(readable, namespace)
			} else {
				unavailableNamespaces = append(unavailableNamespaces, namespace)
			}
		}
		if len(readable) == 0 {
			return nil, true, true, unavailableNamespaces
		}
		namespaces = readable
	}
	dynamicCache := k8s.GetDynamicResourceCache()
	if dynamicCache == nil {
		return nil, true, true, unavailableNamespaces
	}
	rules, synced, err := listSyncedUpgradeResources(dynamicCache, gvr, namespaces)
	if err != nil {
		log.Printf("[upgrade-impact] failed to inspect PrometheusRules: %v", err)
		return nil, true, true, unavailableNamespaces
	}
	if !synced {
		return nil, true, true, unavailableNamespaces
	}
	if rules == nil {
		rules = []*unstructured.Unstructured{}
	}
	return rules, true, true, unavailableNamespaces
}

type upgradeResourceLister interface {
	ListNamespaces(schema.GroupVersionResource, []string) ([]*unstructured.Unstructured, error)
	WaitForSync(schema.GroupVersionResource, time.Duration) bool
	IsClusterWideSynced(schema.GroupVersionResource) bool
}

func listSyncedUpgradeResources(cache upgradeResourceLister, gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, bool, error) {
	// The first list starts any informer that has not already been used. Its
	// result cannot support an absence assertion until that informer syncs.
	if _, err := cache.ListNamespaces(gvr, namespaces); err != nil {
		return nil, false, err
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		return nil, false, nil
	}
	if len(namespaces) == 0 && !cache.IsClusterWideSynced(gvr) {
		return nil, false, nil
	}
	rules, err := cache.ListNamespaces(gvr, namespaces)
	return rules, err == nil, err
}

func collectUpgradeNodeRuntimeEvidence(ctx context.Context, nodes []*corev1.Node) []upgradereadiness.NodeRuntimeEvidence {
	client := k8s.ClientFromContext(ctx)
	if client == nil || client.CoreV1().RESTClient() == nil {
		return nil
	}
	results := make([]upgradereadiness.NodeRuntimeEvidence, len(nodes))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, node := range nodes {
		if node == nil {
			continue
		}
		wg.Add(1)
		go func(i int, node *corev1.Node) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			result := upgradereadiness.NodeRuntimeEvidence{NodeName: node.Name}
			if strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
				results[i] = result
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			raw, err := client.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + url.PathEscape(node.Name) + "/proxy/metrics").DoRaw(probeCtx)
			if err != nil {
				results[i] = result
				return
			}
			parser := expfmt.NewTextParser(model.LegacyValidation)
			families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
			if err != nil {
				results[i] = result
				return
			}
			result.MetricsAvailable = true
			if family := families["kubelet_cgroup_version"]; family != nil && len(family.Metric) > 0 {
				result.CgroupVersionAvailable = true
				result.CgroupVersion = int(metricValue(family.GetType(), family.Metric[0]))
			}
			if family := families["kubelet_cri_losing_support"]; family != nil {
				for _, metric := range family.Metric {
					if metricValue(family.GetType(), metric) > 0 {
						result.CRILosingSupportAvailable = true
						result.CRILosingSupportVersion = metricLabels(metric)["version"]
						break
					}
				}
			}
			results[i] = result
		}(i, node)
	}
	wg.Wait()
	return results
}
