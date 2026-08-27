package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
)

const (
	upgradeSourceObjectCollectionTimeout = 10 * time.Second
	upgradeNodeRuntimeCollectionTimeout  = 20 * time.Second
	upgradeNodeEvidenceResponseLimit     = 8 << 20
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
		if !canListEvidence(authz, gvr.Group, gvr.Resource, "") {
			return nil, true, true, nil
		}
	} else {
		readable := make([]string, 0, len(namespaces))
		for _, namespace := range namespaces {
			if canListEvidence(authz, gvr.Group, gvr.Resource, namespace) {
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

func collectUpgradeNodeRuntimeEvidence(ctx context.Context, nodes []*corev1.Node, includeConfig bool) ([]upgradereadiness.NodeRuntimeEvidence, bool) {
	client := k8s.ClientFromContext(ctx)
	if client == nil || client.CoreV1().RESTClient() == nil {
		return nil, false
	}
	return collectUpgradeNodeRuntimeEvidenceWithClient(ctx, client.CoreV1().RESTClient(), nodes, includeConfig)
}

func collectUpgradeNodeRuntimeEvidenceWithClient(ctx context.Context, client rest.Interface, nodes []*corev1.Node, includeConfig bool) ([]upgradereadiness.NodeRuntimeEvidence, bool) {
	results := make([]upgradereadiness.NodeRuntimeEvidence, len(nodes))
	forbiddenByNode := make([]bool, len(nodes))
	failureCountByNode := make([]int, len(nodes))
	firstFailureByNode := make([]error, len(nodes))
	type nodeRuntimeJob struct {
		index int
		node  *corev1.Node
	}
	type nodeRuntimeFetch struct {
		evidence  upgradereadiness.NodeRuntimeEvidence
		forbidden bool
		err       error
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
				metricsCh := make(chan nodeRuntimeFetch, 1)
				configCh := make(chan nodeRuntimeFetch, 1)
				if strings.EqualFold(job.node.Status.NodeInfo.OperatingSystem, "windows") {
					metricsCh <- nodeRuntimeFetch{}
				} else {
					go func() {
						raw, err := fetchUpgradeNodeEvidence(ctx, client, job.node.Name, "metrics")
						if err != nil {
							forbidden := apierrors.IsForbidden(err)
							if forbidden {
								err = nil
							}
							metricsCh <- nodeRuntimeFetch{forbidden: forbidden, err: err}
							return
						}
						parsed, err := parseUpgradeNodeMetrics(raw)
						if err != nil {
							parsed = upgradereadiness.NodeRuntimeEvidence{}
						}
						metricsCh <- nodeRuntimeFetch{evidence: parsed, err: err}
					}()
				}
				if includeConfig {
					go func() {
						raw, err := fetchUpgradeNodeEvidence(ctx, client, job.node.Name, "configz")
						if err != nil {
							forbidden := apierrors.IsForbidden(err)
							if forbidden {
								err = nil
							}
							configCh <- nodeRuntimeFetch{forbidden: forbidden, err: err}
							return
						}
						parsed, err := parseUpgradeNodeConfig(raw)
						if err != nil {
							parsed = upgradereadiness.NodeRuntimeEvidence{}
						}
						configCh <- nodeRuntimeFetch{evidence: parsed, err: err}
					}()
				} else {
					configCh <- nodeRuntimeFetch{}
				}
				metricsResult := <-metricsCh
				configResult := <-configCh
				mergeNodeRuntimeEvidence(&result, metricsResult.evidence)
				mergeNodeRuntimeEvidence(&result, configResult.evidence)
				results[job.index] = result
				forbiddenByNode[job.index] = metricsResult.forbidden || configResult.forbidden
				for _, fetch := range []struct {
					endpoint string
					result   nodeRuntimeFetch
				}{{"metrics", metricsResult}, {"configz", configResult}} {
					if fetch.result.err == nil {
						continue
					}
					failureCountByNode[job.index]++
					if firstFailureByNode[job.index] == nil {
						firstFailureByNode[job.index] = fmt.Errorf("%s/%s: %w", job.node.Name, fetch.endpoint, fetch.result.err)
					}
				}
			}
		}()
	}
dispatch:
	for i, node := range nodes {
		if node == nil {
			continue
		}
		results[i].NodeName = node.Name
		if !includeConfig && strings.EqualFold(node.Status.NodeInfo.OperatingSystem, "windows") {
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
	forbiddenObserved := false
	failedRequests := 0
	failedNodes := 0
	var firstFailure error
	for i, forbidden := range forbiddenByNode {
		if forbidden {
			forbiddenObserved = true
		}
		if failureCountByNode[i] > 0 {
			failedRequests += failureCountByNode[i]
			failedNodes++
			if firstFailure == nil {
				firstFailure = firstFailureByNode[i]
			}
		}
	}
	if failedRequests > 0 {
		log.Printf("[upgrade-impact] node runtime evidence had %d non-authorization failures across %d/%d nodes (first: %v)", failedRequests, failedNodes, len(nodes), firstFailure)
	}
	return results, forbiddenObserved
}

func fetchUpgradeNodeEvidence(ctx context.Context, client rest.Interface, nodeName, endpoint string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stream, err := client.Get().AbsPath("/api/v1/nodes/" + url.PathEscape(nodeName) + "/proxy/" + endpoint).Stream(probeCtx)
	if err != nil {
		return nil, err
	}
	return readBoundedUpgradeResponse(stream, upgradeNodeEvidenceResponseLimit)
}

func parseUpgradeNodeMetrics(raw []byte) (upgradereadiness.NodeRuntimeEvidence, error) {
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return upgradereadiness.NodeRuntimeEvidence{}, err
	}
	result := upgradereadiness.NodeRuntimeEvidence{MetricsAvailable: true}
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
	result.SELinuxMismatchWarnings = metricFamilySum(families["volume_manager_selinux_volume_context_mismatch_warnings_total"])
	result.SELinuxMismatchErrors = metricFamilySum(families["volume_manager_selinux_volume_context_mismatch_errors_total"])
	return result, nil
}

func parseUpgradeNodeConfig(raw []byte) (upgradereadiness.NodeRuntimeEvidence, error) {
	var payload struct {
		KubeletConfig *struct {
			EventRecordQPS *int32          `json:"eventRecordQPS"`
			FeatureGates   map[string]bool `json:"featureGates"`
		} `json:"kubeletconfig"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return upgradereadiness.NodeRuntimeEvidence{}, err
	}
	if payload.KubeletConfig == nil {
		return upgradereadiness.NodeRuntimeEvidence{}, fmt.Errorf("configz response does not contain kubeletconfig")
	}
	result := upgradereadiness.NodeRuntimeEvidence{ConfigAvailable: true}
	if payload.KubeletConfig.EventRecordQPS != nil {
		result.EventRecordQPSAvailable = true
		result.EventRecordQPS = *payload.KubeletConfig.EventRecordQPS
	}
	result.FeatureGates = make(map[string]bool, len(payload.KubeletConfig.FeatureGates))
	for name, enabled := range payload.KubeletConfig.FeatureGates {
		result.FeatureGates[name] = enabled
	}
	return result, nil
}

func mergeNodeRuntimeEvidence(target *upgradereadiness.NodeRuntimeEvidence, source upgradereadiness.NodeRuntimeEvidence) {
	if source.MetricsAvailable {
		target.MetricsAvailable = true
		target.CgroupVersion = source.CgroupVersion
		target.CgroupVersionAvailable = source.CgroupVersionAvailable
		target.CRILosingSupportVersion = source.CRILosingSupportVersion
		target.CRILosingSupportAvailable = source.CRILosingSupportAvailable
		target.SELinuxMismatchWarnings = source.SELinuxMismatchWarnings
		target.SELinuxMismatchErrors = source.SELinuxMismatchErrors
	}
	if source.ConfigAvailable {
		target.ConfigAvailable = true
		target.EventRecordQPS = source.EventRecordQPS
		target.EventRecordQPSAvailable = source.EventRecordQPSAvailable
		target.FeatureGates = source.FeatureGates
	}
}

func metricFamilySum(family *dto.MetricFamily) float64 {
	if family == nil {
		return 0
	}
	total := 0.0
	for _, metric := range family.Metric {
		total += metricValue(family.GetType(), metric)
	}
	return total
}
