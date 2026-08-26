package upgrade

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

const (
	upgradeSourceObjectCollectionTimeout = 10 * time.Second
	upgradeNodeRuntimeCollectionTimeout  = 20 * time.Second
	upgradeNodeMetricsResponseLimit      = 8 << 20
	upgradeAPIMetricsCollectionTimeout   = 10 * time.Second
	upgradeAPIMetricsResponseLimit       = 16 << 20
)

func sameNamespaceScope(a, b []string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func collectUpgradeHelmManifests(ctx context.Context, namespaces []string) ([]upgradereadiness.ManifestResource, []string, int) {
	client := helm.GetClient()
	if client == nil {
		return nil, nil, 0
	}
	username, groups := "", []string(nil)
	if user := auth.UserFromContext(ctx); user != nil {
		username, groups = user.Username, user.Groups
	}
	resources, unavailableNamespaces, parseErrors, err := client.ListManifestResourcesAcrossNamespaces(ctx, namespaces, username, groups)
	if err != nil {
		if !helm.IsForbiddenError(err) {
			log.Printf("[upgrade-impact] failed to inspect Helm manifests: %v", err)
		}
		if len(unavailableNamespaces) == 0 {
			return nil, nil, parseErrors
		}
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

func collectDeprecatedAPIRequests(ctx context.Context) ([]upgradereadiness.DeprecatedAPIRequest, string) {
	client := k8s.ClientFromContext(ctx)
	if client == nil || client.Discovery().RESTClient() == nil {
		return nil, ""
	}
	metricsCtx, cancel := context.WithTimeout(ctx, upgradeAPIMetricsCollectionTimeout)
	defer cancel()
	stream, err := client.Discovery().RESTClient().Get().AbsPath("/metrics").Stream(metricsCtx)
	if err != nil {
		return nil, ""
	}
	raw, err := readBoundedUpgradeResponse(stream, upgradeAPIMetricsResponseLimit)
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

func readBoundedUpgradeResponse(stream io.ReadCloser, limit int64) ([]byte, error) {
	defer stream.Close()
	raw, err := io.ReadAll(io.LimitReader(stream, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("upgrade evidence response exceeds limit")
	}
	return raw, nil
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

func collectUpgradePrometheusRules(ctx context.Context, authz EvidenceAuthorizer, namespaces []string) (rules []*unstructured.Unstructured, installed, discoveryAvailable bool, unavailableNamespaces []string) {
	discovery := k8s.GetResourceDiscovery()
	gvr, installed, discoveryAvailable := discoverUpgradePrometheusRule(discovery)
	if !discoveryAvailable {
		return nil, false, false, nil
	}
	if !installed {
		return []*unstructured.Unstructured{}, false, true, nil
	}
	if len(namespaces) == 0 {
		if !authz.CanList(gvr.Group, gvr.Resource, "") {
			return nil, true, true, nil
		}
	} else {
		readable := make([]string, 0, len(namespaces))
		for _, namespace := range namespaces {
			if authz.CanList(gvr.Group, gvr.Resource, namespace) {
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

func discoverUpgradePrometheusRule(discovery *k8s.ResourceDiscovery) (schema.GroupVersionResource, bool, bool) {
	if discovery == nil {
		return schema.GroupVersionResource{}, false, false
	}
	gvr, ok := discovery.GetGVRWithGroup("PrometheusRule", "monitoring.coreos.com")
	if ok {
		return gvr, true, true
	}
	return schema.GroupVersionResource{}, false, !discovery.GroupHadPartialDiscovery("monitoring.coreos.com")
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
	type nodeRuntimeJob struct {
		index int
		node  *corev1.Node
	}
	jobs := make(chan nodeRuntimeJob)
	workerCount := min(8, len(nodes))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				result := upgradereadiness.NodeRuntimeEvidence{NodeName: job.node.Name}
				probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
				stream, err := client.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes/" + url.PathEscape(job.node.Name) + "/proxy/metrics").Stream(probeCtx)
				var raw []byte
				if err == nil {
					raw, err = readBoundedUpgradeResponse(stream, upgradeNodeMetricsResponseLimit)
				}
				cancel()
				if err == nil {
					parser := expfmt.NewTextParser(model.LegacyValidation)
					families, parseErr := parser.TextToMetricFamilies(bytes.NewReader(raw))
					if parseErr == nil {
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
					}
				}
				results[job.index] = result
			}
		}()
	}
dispatch:
	for i, node := range nodes {
		if node == nil {
			continue
		}
		results[i].NodeName = node.Name
		if strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
			continue
		}
		select {
		case jobs <- nodeRuntimeJob{index: i, node: node}:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return results
}
