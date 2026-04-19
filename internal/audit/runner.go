package audit

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// RunOptions provides optional data sources for checks that need them.
type RunOptions struct {
	ClusterVersion string   // e.g. "1.30"
	ServedAPIs     []string // e.g. ["apps/v1", "batch/v1beta1"]
}

// RunFromCache fetches resources from Radar's cache and runs all best-practice
// checks. namespaces filters to specific namespaces; empty = all.
func RunFromCache(cache *k8s.ResourceCache, namespaces []string, opts *RunOptions) *bp.ScanResults {
	if cache == nil {
		return &bp.ScanResults{Summary: bp.ScanSummary{Categories: map[string]bp.CategorySummary{}}}
	}

	input := &bp.CheckInput{
		Pods:                     listNamespaced(cache.Pods(), namespaces),
		Deployments:              listNamespaced(cache.Deployments(), namespaces),
		StatefulSets:             listNamespaced(cache.StatefulSets(), namespaces),
		DaemonSets:               listNamespaced(cache.DaemonSets(), namespaces),
		Services:                 listNamespaced(cache.Services(), namespaces),
		Ingresses:                listNamespaced(cache.Ingresses(), namespaces),
		HorizontalPodAutoscalers: listNamespaced(cache.HorizontalPodAutoscalers(), namespaces),
		PodDisruptionBudgets:     listNamespaced(cache.PodDisruptionBudgets(), namespaces),
		ConfigMaps:               listNamespaced(cache.ConfigMaps(), namespaces),
		Secrets:                  listNamespaced(cache.Secrets(), namespaces),
		ServiceAccounts:          listNamespaced(cache.ServiceAccounts(), namespaces),
		LimitRanges:              listNamespaced(cache.LimitRanges(), namespaces),
	}

	if opts != nil {
		input.ClusterVersion = opts.ClusterVersion
		input.ServedAPIs = opts.ServedAPIs
	}

	return bp.RunChecks(input)
}

// lister is a generic interface that all typed K8s listers satisfy.
type lister[T any] interface {
	List(selector labels.Selector) ([]*T, error)
}

// listNamespaced fetches all objects from a lister, optionally filtered by namespaces.
func listNamespaced[T any, L lister[T]](l L, namespaces []string) []*T {
	var zero L
	if any(l) == any(zero) {
		return nil
	}
	if len(namespaces) == 0 {
		items, _ := l.List(labels.Everything())
		return items
	}
	// For namespace-filtered queries we rely on the global list + filter approach
	// since typed listers use different namespace lister types that don't share
	// a common interface. This is simple and fast for cached data.
	all, _ := l.List(labels.Everything())
	nsSet := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		nsSet[ns] = true
	}
	var filtered []*T
	for _, item := range all {
		if ns := extractNamespace(item); ns == "" || nsSet[ns] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// extractNamespace uses type assertions for known types to get namespace.
func extractNamespace(obj any) string {
	switch v := obj.(type) {
	case *corev1.Pod:
		return v.Namespace
	case *appsv1.Deployment:
		return v.Namespace
	case *appsv1.StatefulSet:
		return v.Namespace
	case *appsv1.DaemonSet:
		return v.Namespace
	case *corev1.Service:
		return v.Namespace
	case *networkingv1.Ingress:
		return v.Namespace
	case *autoscalingv2.HorizontalPodAutoscaler:
		return v.Namespace
	case *policyv1.PodDisruptionBudget:
		return v.Namespace
	case *corev1.ConfigMap:
		return v.Namespace
	case *corev1.Secret:
		return v.Namespace
	case *corev1.ServiceAccount:
		return v.Namespace
	case *corev1.LimitRange:
		return v.Namespace
	}
	return ""
}

