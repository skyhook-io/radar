package k8s

import (
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

// The scope must already be filtered by the caller's authorization gates.
func InventoryNamespaceHint(cache *ResourceCache, resource string, authorizedScope []string) string {
	if cache == nil || cache.KindReadinessFor(resource) != k8score.KindReady {
		return ""
	}
	var choices []string
	for _, namespace := range cache.KindNamespaces(resource) {
		if authorizedScope == nil || slices.Contains(authorizedScope, namespace) {
			choices = append(choices, namespace)
		}
	}
	if len(choices) == 0 {
		return ""
	}
	return "Watched namespaces within this authorized scope: " + strings.Join(choices, ", ") + ". Request one of these namespaces for a complete list."
}

// topologyResourceProvider adapts *ResourceCache to topology.ResourceProvider.
type topologyResourceProvider struct {
	cache           *ResourceCache
	namespace       string
	requireComplete bool
}

// NewTopologyResourceProvider wraps a ResourceCache as a topology.ResourceProvider.
// Returns nil if cache is nil; topology.Builder.Build() will return an error in that case.
func NewTopologyResourceProvider(cache *ResourceCache) topology.ResourceProvider {
	if cache == nil {
		return nil
	}
	return &topologyResourceProvider{cache: cache}
}

func (a *topologyResourceProvider) ForNamespace(namespace string) topology.ResourceProvider {
	if namespace == "" {
		namespace = a.namespace
	}
	return &topologyResourceProvider{cache: a.cache, namespace: namespace, requireComplete: true}
}

// A lister can expose positive observations before its initial LIST completes.
// Those observations cannot establish absence in a complete view.
func (a *topologyResourceProvider) checkReady(key string) error {
	switch a.cache.KindReadinessFor(key) {
	case k8score.KindPending:
		return &topology.InventoryError{Resource: key, State: topology.InventoryPending}
	case k8score.KindFailed:
		return &topology.InventoryError{Resource: key, State: topology.InventoryFailed}
	case k8score.KindUnavailable:
		return nil
	}
	if a.namespace != "" && !a.cache.KindCoversNamespace(key, a.namespace) {
		return &topology.InventoryError{Resource: key, State: topology.InventoryUncovered, Namespace: a.namespace}
	}
	return nil
}

func (a *topologyResourceProvider) InventoryError(resource, namespace string) error {
	if err := a.checkReady(resource); err != nil {
		return err
	}
	if a.cache.KindReadinessFor(resource) == k8score.KindUnavailable {
		return &topology.InventoryError{Resource: resource, State: topology.InventoryUnavailable}
	}
	switch resource {
	case k8score.Nodes, k8score.Namespaces, k8score.PersistentVolumes:
		return nil
	}
	if a.namespace != "" && namespace != a.namespace {
		return &topology.InventoryError{Resource: resource, State: topology.InventoryUncovered, Namespace: namespace}
	}
	if namespace != "" {
		if !a.cache.KindCoversNamespace(resource, namespace) {
			return &topology.InventoryError{Resource: resource, State: topology.InventoryUncovered, Namespace: namespace}
		}
	} else if len(a.cache.KindNamespaces(resource)) > 0 {
		return &topology.InventoryError{Resource: resource, State: topology.InventoryUncovered}
	}
	return nil
}

func (a *topologyResourceProvider) Pods() ([]*corev1.Pod, error) {
	return readTypedTopologyInventory(a, k8score.Pods, func() ([]*corev1.Pod, error) {
		lister := a.cache.Pods()
		if lister == nil {
			return nil, fmt.Errorf("pods not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.Pods(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Services() ([]*corev1.Service, error) {
	return readTypedTopologyInventory(a, k8score.Services, func() ([]*corev1.Service, error) {
		lister := a.cache.Services()
		if lister == nil {
			return nil, fmt.Errorf("services not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.Services(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Deployments() ([]*appsv1.Deployment, error) {
	return readTypedTopologyInventory(a, k8score.Deployments, func() ([]*appsv1.Deployment, error) {
		lister := a.cache.Deployments()
		if lister == nil {
			return nil, fmt.Errorf("deployments not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.Deployments(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) DaemonSets() ([]*appsv1.DaemonSet, error) {
	return readTypedTopologyInventory(a, k8score.DaemonSets, func() ([]*appsv1.DaemonSet, error) {
		lister := a.cache.DaemonSets()
		if lister == nil {
			return nil, fmt.Errorf("daemonsets not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.DaemonSets(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	return readTypedTopologyInventory(a, k8score.StatefulSets, func() ([]*appsv1.StatefulSet, error) {
		lister := a.cache.StatefulSets()
		if lister == nil {
			return nil, fmt.Errorf("statefulsets not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.StatefulSets(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	return readTypedTopologyInventory(a, k8score.ReplicaSets, func() ([]*appsv1.ReplicaSet, error) {
		lister := a.cache.ReplicaSets()
		if lister == nil {
			return nil, fmt.Errorf("replicasets not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.ReplicaSets(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Jobs() ([]*batchv1.Job, error) {
	return readTypedTopologyInventory(a, k8score.Jobs, func() ([]*batchv1.Job, error) {
		lister := a.cache.Jobs()
		if lister == nil {
			return nil, fmt.Errorf("jobs not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.Jobs(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) CronJobs() ([]*batchv1.CronJob, error) {
	return readTypedTopologyInventory(a, k8score.CronJobs, func() ([]*batchv1.CronJob, error) {
		lister := a.cache.CronJobs()
		if lister == nil {
			return nil, fmt.Errorf("cronjobs not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.CronJobs(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Ingresses() ([]*networkingv1.Ingress, error) {
	return readTypedTopologyInventory(a, k8score.Ingresses, func() ([]*networkingv1.Ingress, error) {
		lister := a.cache.Ingresses()
		if lister == nil {
			return nil, fmt.Errorf("ingresses not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.Ingresses(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) ConfigMaps() ([]*corev1.ConfigMap, error) {
	return readTypedTopologyInventory(a, k8score.ConfigMaps, func() ([]*corev1.ConfigMap, error) {
		lister := a.cache.ConfigMaps()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.ConfigMaps) {
				return nil, nil
			}
			return nil, fmt.Errorf("configmaps not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.ConfigMaps(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Secrets() ([]*corev1.Secret, error) {
	return readTypedTopologyInventory(a, k8score.Secrets, func() ([]*corev1.Secret, error) {
		lister := a.cache.Secrets()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.Secrets) {
				return nil, nil
			}
			return nil, fmt.Errorf("secrets not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.Secrets(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) ServiceAccounts() ([]*corev1.ServiceAccount, error) {
	return readTypedTopologyInventory(a, k8score.ServiceAccounts, func() ([]*corev1.ServiceAccount, error) {
		lister := a.cache.ServiceAccounts()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.ServiceAccounts) {
				return nil, nil
			}
			return nil, fmt.Errorf("serviceaccounts not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.ServiceAccounts(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Namespaces() ([]*corev1.Namespace, error) {
	return readTypedTopologyInventory(a, k8score.Namespaces, func() ([]*corev1.Namespace, error) {
		lister := a.cache.Namespaces()
		if lister == nil {
			return nil, fmt.Errorf("namespaces not available (RBAC not granted)")
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return readTypedTopologyInventory(a, k8score.PersistentVolumeClaims, func() ([]*corev1.PersistentVolumeClaim, error) {
		lister := a.cache.PersistentVolumeClaims()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.PersistentVolumeClaims) {
				return nil, nil
			}
			return nil, fmt.Errorf("persistentvolumeclaims not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.PersistentVolumeClaims(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	return readTypedTopologyInventory(a, k8score.PersistentVolumes, func() ([]*corev1.PersistentVolume, error) {
		lister := a.cache.PersistentVolumes()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.PersistentVolumes) {
				return nil, nil
			}
			return nil, fmt.Errorf("persistentvolumes not available (RBAC not granted)")
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return readTypedTopologyInventory(a, k8score.HorizontalPodAutoscalers, func() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
		lister := a.cache.HorizontalPodAutoscalers()
		if lister == nil {
			return nil, fmt.Errorf("horizontalpodautoscalers not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.HorizontalPodAutoscalers(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return readTypedTopologyInventory(a, k8score.PodDisruptionBudgets, func() ([]*policyv1.PodDisruptionBudget, error) {
		lister := a.cache.PodDisruptionBudgets()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.PodDisruptionBudgets) {
				return nil, nil
			}
			return nil, fmt.Errorf("poddisruptionbudgets not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.PodDisruptionBudgets(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	return readTypedTopologyInventory(a, k8score.NetworkPolicies, func() ([]*networkingv1.NetworkPolicy, error) {
		lister := a.cache.NetworkPolicies()
		if lister == nil {
			if !a.requireComplete && a.cache.IsDeferredPending(k8score.NetworkPolicies) {
				return nil, nil
			}
			return nil, fmt.Errorf("networkpolicies not available (RBAC not granted)")
		}
		if a.namespace != "" {
			return lister.NetworkPolicies(a.namespace).List(labels.Everything())
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) Nodes() ([]*corev1.Node, error) {
	return readTypedTopologyInventory(a, k8score.Nodes, func() ([]*corev1.Node, error) {
		lister := a.cache.Nodes()
		if lister == nil {
			return nil, fmt.Errorf("nodes not available (RBAC not granted)")
		}
		return lister.List(labels.Everything())
	})
}

func (a *topologyResourceProvider) GetResourceStatus(kind, namespace, name string) *topology.ResourceStatus {
	return a.cache.GetResourceStatus(kind, namespace, name)
}

// topologyDynamicProvider adapts *DynamicResourceCache + *ResourceDiscovery to topology.DynamicProvider.
type topologyDynamicProvider struct {
	dynCache  *DynamicResourceCache
	discovery *ResourceDiscovery
}

// NewTopologyDynamicProvider wraps DynamicResourceCache and ResourceDiscovery as a topology.DynamicProvider.
func NewTopologyDynamicProvider(dynCache *DynamicResourceCache, discovery *ResourceDiscovery) topology.DynamicProvider {
	if dynCache == nil || discovery == nil {
		return nil
	}
	return &topologyDynamicProvider{dynCache: dynCache, discovery: discovery}
}

func (a *topologyDynamicProvider) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	return a.dynCache.List(gvr, namespace)
}

func (a *topologyDynamicProvider) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	return a.dynCache.ListNamespaces(gvr, namespaces)
}

func (a *topologyDynamicProvider) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return a.dynCache.Get(gvr, namespace, name)
}

func (a *topologyDynamicProvider) GetWatchedResources() []schema.GroupVersionResource {
	return a.dynCache.GetWatchedResources()
}

func (a *topologyDynamicProvider) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return a.dynCache.GetDiscoveryStatus()
}

func (a *topologyDynamicProvider) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	return a.discovery.GetGVR(kindOrName)
}

func (a *topologyDynamicProvider) GetGVRWithGroup(kindOrName, group string) (schema.GroupVersionResource, bool) {
	return a.discovery.GetGVRWithGroup(kindOrName, group)
}

func (a *topologyDynamicProvider) GetKindForGVR(gvr schema.GroupVersionResource) string {
	return a.discovery.GetKindForGVR(gvr)
}

func (a *topologyDynamicProvider) IsCRD(kind string) bool {
	return a.discovery.IsCRD(kind)
}

func (a *topologyDynamicProvider) IsCRDGVR(gvr schema.GroupVersionResource) bool {
	return a.discovery.IsCRDGVR(gvr)
}

func readTypedTopologyInventory[T any](a *topologyResourceProvider, resource string, list func() ([]T, error)) ([]T, error) {
	readiness := a.checkReady(resource)
	if a.requireComplete && readiness != nil {
		return nil, readiness
	}
	items, err := list()
	if readiness != nil {
		return items, readiness
	}
	return items, err
}

func (a *topologyDynamicProvider) ListWatchedReadOnly(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	return a.dynCache.ListWatchedReadOnly(gvr)
}

func (a *topologyDynamicProvider) Observation(gvr schema.GroupVersionResource) k8score.DynamicResourceObservation {
	return a.dynCache.Observation(gvr)
}
