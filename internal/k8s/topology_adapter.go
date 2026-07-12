package k8s

import (
	"fmt"

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

// topologyResourceProvider adapts *ResourceCache to topology.ResourceProvider.
type topologyResourceProvider struct {
	cache *ResourceCache
}

// NewTopologyResourceProvider wraps a ResourceCache as a topology.ResourceProvider.
// Returns nil if cache is nil; topology.Builder.Build() will return an error in that case.
func NewTopologyResourceProvider(cache *ResourceCache) topology.ResourceProvider {
	if cache == nil {
		return nil
	}
	return &topologyResourceProvider{cache: cache}
}

func (a *topologyResourceProvider) Pods() ([]*corev1.Pod, error) {
	lister := a.cache.Pods()
	if lister == nil {
		return nil, fmt.Errorf("pods not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Services() ([]*corev1.Service, error) {
	lister := a.cache.Services()
	if lister == nil {
		return nil, fmt.Errorf("services not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Deployments() ([]*appsv1.Deployment, error) {
	lister := a.cache.Deployments()
	if lister == nil {
		return nil, fmt.Errorf("deployments not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) DaemonSets() ([]*appsv1.DaemonSet, error) {
	lister := a.cache.DaemonSets()
	if lister == nil {
		return nil, fmt.Errorf("daemonsets not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	lister := a.cache.StatefulSets()
	if lister == nil {
		return nil, fmt.Errorf("statefulsets not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	lister := a.cache.ReplicaSets()
	if lister == nil {
		return nil, fmt.Errorf("replicasets not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Jobs() ([]*batchv1.Job, error) {
	lister := a.cache.Jobs()
	if lister == nil {
		return nil, fmt.Errorf("jobs not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) CronJobs() ([]*batchv1.CronJob, error) {
	lister := a.cache.CronJobs()
	if lister == nil {
		return nil, fmt.Errorf("cronjobs not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Ingresses() ([]*networkingv1.Ingress, error) {
	lister := a.cache.Ingresses()
	if lister == nil {
		return nil, fmt.Errorf("ingresses not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) ConfigMaps() ([]*corev1.ConfigMap, error) {
	lister := a.cache.ConfigMaps()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.ConfigMaps) {
			return nil, nil
		}
		return nil, fmt.Errorf("configmaps not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Secrets() ([]*corev1.Secret, error) {
	lister := a.cache.Secrets()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.Secrets) {
			return nil, nil
		}
		return nil, fmt.Errorf("secrets not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) ServiceAccounts() ([]*corev1.ServiceAccount, error) {
	lister := a.cache.ServiceAccounts()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.ServiceAccounts) {
			return nil, nil
		}
		return nil, fmt.Errorf("serviceaccounts not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	lister := a.cache.PersistentVolumeClaims()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.PersistentVolumeClaims) {
			return nil, nil
		}
		return nil, fmt.Errorf("persistentvolumeclaims not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	lister := a.cache.PersistentVolumes()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.PersistentVolumes) {
			return nil, nil
		}
		return nil, fmt.Errorf("persistentvolumes not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	lister := a.cache.HorizontalPodAutoscalers()
	if lister == nil {
		return nil, fmt.Errorf("horizontalpodautoscalers not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	lister := a.cache.PodDisruptionBudgets()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.PodDisruptionBudgets) {
			return nil, nil
		}
		return nil, fmt.Errorf("poddisruptionbudgets not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	lister := a.cache.NetworkPolicies()
	if lister == nil {
		if a.cache.IsDeferredPending(k8score.NetworkPolicies) {
			return nil, nil
		}
		return nil, fmt.Errorf("networkpolicies not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Nodes() ([]*corev1.Node, error) {
	lister := a.cache.Nodes()
	if lister == nil {
		return nil, fmt.Errorf("nodes not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
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
