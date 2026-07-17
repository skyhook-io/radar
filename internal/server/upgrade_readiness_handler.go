package server

import (
	"bytes"
	"errors"
	"log"
	"math"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	var deprecatedRequests []upgradereadiness.DeprecatedAPIRequest
	var deprecatedMetricsWindow string
	var prometheusRules []*unstructured.Unstructured
	var prometheusUnavailableNamespaces []string
	var prometheusInstalled, discoveryAvailable bool
	if !noAccess {
		manifestResources, helmUnavailableNamespaces = collectUpgradeHelmManifests(r, namespaces)
		deprecatedRequests, deprecatedMetricsWindow = collectDeprecatedAPIRequests(r)
		prometheusRules, prometheusInstalled, discoveryAvailable, prometheusUnavailableNamespaces = s.collectUpgradePrometheusRules(r, namespaces)
	}
	platform, _ := k8s.GetClusterPlatform(r.Context())
	results, err := audit.RunUpgradeReadinessFromCache(scanInput, namespaces, audit.UpgradeReadinessOptions{
		CurrentVersion:                      k8s.GetServerVersion(),
		TargetVersion:                       r.URL.Query().Get("target"),
		Platform:                            platform,
		ManifestResources:                   manifestResources,
		HelmUnavailableNamespaces:           helmUnavailableNamespaces,
		DeprecatedAPIRequests:               deprecatedRequests,
		DeprecatedAPIMetricsWindow:          deprecatedMetricsWindow,
		PrometheusRules:                     prometheusRules,
		PrometheusRulesInstalled:            prometheusInstalled,
		PrometheusRulesDiscoveryAvailable:   discoveryAvailable,
		PrometheusRuleUnavailableNamespaces: prometheusUnavailableNamespaces,
		CanReadNodes:                        !noAccess && s.canRead(r, "", "nodes", "", "list"),
		CanReadPersistentVolumes:            !noAccess && s.canRead(r, "", "persistentvolumes", "", "list"),
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

func collectUpgradeHelmManifests(r *http.Request, namespaces []string) ([]upgradereadiness.ManifestResource, []string) {
	client := helm.GetClient()
	if client == nil {
		return nil, nil
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(r.Context()); user != nil {
		username, groups = user.Username, user.Groups
	}
	resources, unavailableNamespaces, err := client.ListManifestResourcesAcrossNamespaces(namespaces, username, groups)
	if err != nil {
		if !helm.IsForbiddenError(err) {
			log.Printf("[upgrade-impact] failed to inspect Helm manifests: %v", err)
		}
		return nil, unavailableNamespaces
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
		})
	}
	return result, unavailableNamespaces
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
